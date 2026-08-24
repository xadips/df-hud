//! GLES 3.0 / GL 3.3 renderer: one shader, one VAO, hinted LCD atlas, draw list.
//!
//! Does not own the EGL/WGL window and does not see `WlSurface` or HWND.
//! Surfaces make the context current, call [`Gpu::draw`], then swap.
//! Linux stays GLES 3.0 (`#version 300 es`). Windows is GL 3.3 core
//! (`#version 330 core`). Same body; only the version string is `cfg`'d.

use std::error::Error;

use glow::{Context as Glow, HasContext, PixelUnpackData};

use crate::font::{Atlas, Font};
use crate::scene::{Scene, Text};

/// `hud.font_size` is CSS points. Convert with `size * 4/3` to pixels;
/// 12pt → 16px at the 2560×1440 authoring size.
pub const FONT_PT: f32 = 12.0;
const PT_TO_PX: f32 = 4.0 / 3.0;

#[cfg(target_os = "windows")]
const GLSL_VERSION: &str = "#version 330 core";
#[cfg(not(target_os = "windows"))]
const GLSL_VERSION: &str = "#version 300 es";

const VS: &str = r#"
layout(location = 0) in vec2 a_pos;
layout(location = 1) in vec4 a_color;
layout(location = 2) in vec2 a_uv;
uniform vec2 u_resolution;
out vec4 v_color;
out vec2 v_uv;
void main() {
    vec2 clip = (a_pos / u_resolution) * 2.0 - 1.0;
    // y-down pixels → clip. Opposite flip draws the HUD off the top of the screen.
    gl_Position = vec4(clip.x, -clip.y, 0.0, 1.0);
    v_color = a_color;
    v_uv = a_uv;
}
"#;

const FS: &str = r#"
precision mediump float;
in vec4 v_color;
in vec2 v_uv;
uniform sampler2D u_atlas;
out vec4 frag;
void main() {
    // RGB atlas: LCD coverage per channel, or (1,1,1) for untextured fills.
    vec3 lcd = texture(u_atlas, v_uv).rgb;
    float a = v_color.a * max(max(lcd.r, lcd.g), lcd.b);
    // Wayland compositors and DWM layered windows blend premultiplied.
    frag = vec4(v_color.rgb * lcd * v_color.a, a);
}
"#;

const STRIDE: i32 = 32;
const OUTLINE_COLOR: [f32; 4] = [0.0, 0.0, 0.0, 1.0];

pub struct Gpu {
    gl: Glow,
    program: glow::Program,
    vao: glow::VertexArray,
    vbo: glow::Buffer,
    atlas_tex: glow::Texture,
    u_resolution: glow::UniformLocation,
    font: Font,
    font_want: String,
    atlas: Atlas,
    px: f32,
}

impl Gpu {
    pub fn new(gl: Glow, buf_w: i32, buf_h: i32, font_want: &str) -> Result<Self, Box<dyn Error>> {
        unsafe {
            eprintln!(
                "GL renderer={} version={}",
                gl.get_parameter_string(glow::RENDERER),
                gl.get_parameter_string(glow::VERSION)
            );
        }

        let font = Font::load(Some(font_want))?;
        let atlas = Atlas::new();
        let program = unsafe { link_program(&gl)? };
        let u_resolution = unsafe {
            gl.get_uniform_location(program, "u_resolution")
                .ok_or("u_resolution missing")?
        };
        let u_atlas = unsafe {
            gl.get_uniform_location(program, "u_atlas")
                .ok_or("u_atlas missing")?
        };
        let vao = unsafe { gl.create_vertex_array()? };
        let vbo = unsafe { gl.create_buffer()? };
        let atlas_tex = unsafe { gl.create_texture()? };
        unsafe {
            gl.bind_framebuffer(glow::FRAMEBUFFER, None);
            gl.bind_vertex_array(Some(vao));
            gl.bind_buffer(glow::ARRAY_BUFFER, Some(vbo));
            gl.vertex_attrib_pointer_f32(0, 2, glow::FLOAT, false, STRIDE, 0);
            gl.enable_vertex_attrib_array(0);
            gl.vertex_attrib_pointer_f32(1, 4, glow::FLOAT, false, STRIDE, 8);
            gl.enable_vertex_attrib_array(1);
            gl.vertex_attrib_pointer_f32(2, 2, glow::FLOAT, false, STRIDE, 24);
            gl.enable_vertex_attrib_array(2);
            gl.enable(glow::BLEND);
            gl.blend_func(glow::ONE, glow::ONE_MINUS_SRC_ALPHA);
            gl.disable(glow::DEPTH_TEST);
            gl.disable(glow::CULL_FACE);
            gl.active_texture(glow::TEXTURE0);
            gl.bind_texture(glow::TEXTURE_2D, Some(atlas_tex));
            // NEAREST keeps LCD subpixels; LINEAR smears R/G/B into colour fringes.
            gl.tex_parameter_i32(
                glow::TEXTURE_2D,
                glow::TEXTURE_MIN_FILTER,
                glow::NEAREST as i32,
            );
            gl.tex_parameter_i32(
                glow::TEXTURE_2D,
                glow::TEXTURE_MAG_FILTER,
                glow::NEAREST as i32,
            );
            gl.tex_parameter_i32(
                glow::TEXTURE_2D,
                glow::TEXTURE_WRAP_S,
                glow::CLAMP_TO_EDGE as i32,
            );
            gl.tex_parameter_i32(
                glow::TEXTURE_2D,
                glow::TEXTURE_WRAP_T,
                glow::CLAMP_TO_EDGE as i32,
            );
            gl.use_program(Some(program));
            gl.uniform_1_i32(Some(&u_atlas), 0);
            gl.viewport(0, 0, buf_w, buf_h);
        }

        let mut gpu = Self {
            gl,
            program,
            vao,
            vbo,
            atlas_tex,
            u_resolution,
            font,
            font_want: font_want.trim().to_string(),
            atlas,
            px: 0.0,
        };
        gpu.upload_atlas()?;
        eprintln!(
            "font={} atlas={}x{} (hinted LCD, 1px outline)",
            gpu.font.name, gpu.atlas.width, gpu.atlas.height
        );
        Ok(gpu)
    }

    pub fn resize(&self, buf_w: i32, buf_h: i32) {
        unsafe { self.gl.viewport(0, 0, buf_w, buf_h) };
    }

    pub fn set_font(&mut self, want: &str) {
        let want = want.trim();
        if want == self.font_want {
            return;
        }
        match crate::font::try_load(Some(want)) {
            Ok(font) => {
                self.font = font;
                self.font_want = want.to_string();
                self.atlas.reset();
                eprintln!(
                    "font={} atlas={}x{} (hinted LCD, 1px outline)",
                    self.font.name, self.atlas.width, self.atlas.height
                );
            }
            Err(err) => {
                eprintln!("font: {err}; keeping {}", self.font.name);
                self.font_want = want.to_string();
            }
        }
    }

    pub fn draw(
        &mut self,
        buf_w: i32,
        buf_h: i32,
        logical_w: i32,
        logical_h: i32,
        scene: &Scene,
    ) -> Result<(), Box<dyn Error>> {
        let sx = buf_w as f32 / logical_w.max(1) as f32;
        let sy = buf_h as f32 / logical_h.max(1) as f32;
        let hud_px = scene
            .texts
            .first()
            .map(|t| t.font_px * sy)
            .unwrap_or(FONT_PT * PT_TO_PX * sy);
        if (hud_px - self.px).abs() > 0.05 {
            self.atlas.reset();
            self.px = hud_px;
        }
        for text in scene.texts.iter().chain(&scene.labels) {
            let px = text.font_px * sy;
            for ch in text.text.chars() {
                self.atlas.glyph(&self.font, ch, px, text.lcd)?;
            }
        }
        if self.atlas.dirty {
            self.upload_atlas()?;
        }

        let mut verts = Vec::new();
        let (wu, wv) = self.atlas.white_uv();
        for text in &scene.texts {
            self.push_text(&mut verts, text, sx, sy)?;
        }
        for fill in &scene.fills {
            push_quad(
                &mut verts,
                fill.x * sx,
                fill.y * sy,
                fill.w * sx,
                fill.h * sy,
                fill.color,
                wu,
                wv,
                wu,
                wv,
            );
        }
        let stroke_s = (sx + sy) * 0.5;
        for stroke in &scene.strokes {
            push_line(
                &mut verts,
                stroke.x0 * sx,
                stroke.y0 * sy,
                stroke.x1 * sx,
                stroke.y1 * sy,
                stroke.width * stroke_s,
                stroke.color,
                wu,
                wv,
            );
        }
        for text in &scene.labels {
            self.push_text(&mut verts, text, sx, sy)?;
        }

        unsafe {
            self.gl.bind_framebuffer(glow::FRAMEBUFFER, None);
            self.gl.clear_color(0.0, 0.0, 0.0, 0.0);
            self.gl.clear(glow::COLOR_BUFFER_BIT);
            self.gl.use_program(Some(self.program));
            self.gl
                .uniform_2_f32(Some(&self.u_resolution), buf_w as f32, buf_h as f32);
            self.gl.active_texture(glow::TEXTURE0);
            self.gl.bind_texture(glow::TEXTURE_2D, Some(self.atlas_tex));
            self.gl.bind_vertex_array(Some(self.vao));
            self.gl.bind_buffer(glow::ARRAY_BUFFER, Some(self.vbo));
            self.gl.buffer_data_u8_slice(
                glow::ARRAY_BUFFER,
                verts_as_bytes(&verts),
                glow::STREAM_DRAW,
            );
            self.gl
                .draw_arrays(glow::TRIANGLES, 0, (verts.len() / 8) as i32);
        }
        Ok(())
    }

    fn push_text(
        &self,
        verts: &mut Vec<f32>,
        text: &Text,
        sx: f32,
        sy: f32,
    ) -> Result<(), Box<dyn Error>> {
        let px = text.font_px * sy;
        let x0 = text.x * sx;
        let y0 = text.y * sy;
        let baseline = y0 + self.font.ascent(px);
        let mut pen = x0;
        let mut ink_top = f32::MAX;
        let mut ink_bot = f32::MIN;
        let mut runs: Vec<(f32, f32, crate::font::Glyph)> = Vec::new();
        for ch in text.text.chars() {
            let glyph = self
                .atlas
                .get(ch, px, text.lcd)
                .ok_or("glyph missing from atlas after rasterize")?;
            if glyph.width > 0 && glyph.height > 0 {
                let gx = (pen + glyph.xmin as f32).round();
                let gy = (baseline - glyph.top as f32).round();
                ink_top = ink_top.min(gy);
                ink_bot = ink_bot.max(gy + glyph.height as f32);
                runs.push((gx, gy, glyph));
            }
            pen += glyph.advance;
        }
        let dy = if let (Some(h), true) = (text.center_h, ink_bot > ink_top) {
            let box_mid = y0 + h * sy * 0.5;
            let ink_mid = (ink_top + ink_bot) * 0.5;
            (box_mid - ink_mid).round()
        } else {
            0.0
        };
        for (gx, gy, glyph) in runs {
            let gy = gy + dy;
            let (ou0, ov0, ou1, ov1) = self.atlas.uv(
                glyph.outline_x,
                glyph.outline_y,
                glyph.outline_w,
                glyph.outline_h,
            );
            if text.outline {
                let oc = text.outline_color.unwrap_or(OUTLINE_COLOR);
                push_quad(
                    verts,
                    gx - 1.0,
                    gy - 1.0,
                    glyph.outline_w as f32,
                    glyph.outline_h as f32,
                    [oc[0], oc[1], oc[2], oc[3] * text.color[3]],
                    ou0,
                    ov0,
                    ou1,
                    ov1,
                );
            }
            let (u0, v0, u1, v1) =
                self.atlas
                    .uv(glyph.atlas_x, glyph.atlas_y, glyph.width, glyph.height);
            push_quad(
                verts,
                gx,
                gy,
                glyph.width as f32,
                glyph.height as f32,
                text.color,
                u0,
                v0,
                u1,
                v1,
            );
        }
        Ok(())
    }

    fn upload_atlas(&mut self) -> Result<(), Box<dyn Error>> {
        unsafe {
            self.gl.pixel_store_i32(glow::UNPACK_ALIGNMENT, 1);
            self.gl.bind_texture(glow::TEXTURE_2D, Some(self.atlas_tex));
            self.gl.tex_image_2d(
                glow::TEXTURE_2D,
                0,
                glow::RGB8 as i32,
                self.atlas.width as i32,
                self.atlas.height as i32,
                0,
                glow::RGB,
                glow::UNSIGNED_BYTE,
                PixelUnpackData::Slice(Some(self.atlas.pixels.as_slice())),
            );
        }
        self.atlas.dirty = false;
        Ok(())
    }
}

impl Drop for Gpu {
    fn drop(&mut self) {
        unsafe {
            self.gl.delete_texture(self.atlas_tex);
            self.gl.delete_buffer(self.vbo);
            self.gl.delete_vertex_array(self.vao);
            self.gl.delete_program(self.program);
        }
    }
}

fn verts_as_bytes(verts: &[f32]) -> &[u8] {
    unsafe { std::slice::from_raw_parts(verts.as_ptr().cast(), std::mem::size_of_val(verts)) }
}

fn push_quad(
    verts: &mut Vec<f32>,
    x: f32,
    y: f32,
    w: f32,
    h: f32,
    color: [f32; 4],
    u0: f32,
    v0: f32,
    u1: f32,
    v1: f32,
) {
    let [r, g, b, a] = color;
    let x2 = x + w;
    let y2 = y + h;
    let verts_px = [
        [x, y, u0, v0],
        [x2, y, u1, v0],
        [x, y2, u0, v1],
        [x, y2, u0, v1],
        [x2, y, u1, v0],
        [x2, y2, u1, v1],
    ];
    for [px, py, u, v] in verts_px {
        verts.extend_from_slice(&[px, py, r, g, b, a, u, v]);
    }
}

fn push_line(
    verts: &mut Vec<f32>,
    x0: f32,
    y0: f32,
    x1: f32,
    y1: f32,
    width: f32,
    color: [f32; 4],
    u: f32,
    v: f32,
) {
    let dx = x1 - x0;
    let dy = y1 - y0;
    let len = (dx * dx + dy * dy).sqrt().max(1e-6);
    let hx = (-dy / len) * (width * 0.5);
    let hy = (dx / len) * (width * 0.5);
    let [r, g, b, a] = color;
    let corners = [
        [x0 + hx, y0 + hy],
        [x1 + hx, y1 + hy],
        [x0 - hx, y0 - hy],
        [x0 - hx, y0 - hy],
        [x1 + hx, y1 + hy],
        [x1 - hx, y1 - hy],
    ];
    for [px, py] in corners {
        verts.extend_from_slice(&[px, py, r, g, b, a, u, v]);
    }
}

unsafe fn link_program(gl: &Glow) -> Result<glow::Program, Box<dyn Error>> {
    unsafe {
        let vs = compile(gl, glow::VERTEX_SHADER, shader_src(VS))?;
        let fs = compile(gl, glow::FRAGMENT_SHADER, shader_src(FS))?;
        let program = gl.create_program()?;
        gl.attach_shader(program, vs);
        gl.attach_shader(program, fs);
        gl.link_program(program);
        if !gl.get_program_link_status(program) {
            return Err(gl.get_program_info_log(program).into());
        }
        gl.delete_shader(vs);
        gl.delete_shader(fs);
        Ok(program)
    }
}

fn shader_src(body: &str) -> String {
    format!("{GLSL_VERSION}\n{body}")
}

unsafe fn compile(gl: &Glow, kind: u32, src: String) -> Result<glow::Shader, Box<dyn Error>> {
    unsafe {
        let shader = gl.create_shader(kind)?;
        gl.shader_source(shader, &src);
        gl.compile_shader(shader);
        if !gl.get_shader_compile_status(shader) {
            return Err(gl.get_shader_info_log(shader).into());
        }
        Ok(shader)
    }
}
