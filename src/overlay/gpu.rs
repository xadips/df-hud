//! GLES 3.0 / GL 3.3 renderer: one shader, one VAO, hinted LCD atlas, draw list.
//!
//! Does not own the EGL/WGL window and does not see `WlSurface` or HWND.
//! Surfaces make the context current, call [`Gpu::draw`], and swap only when
//! it returns `true`. The draw remembers the last frame it rendered (the
//! [`Frame`] shape and the [`Scene`]) and skips the vertex build, upload,
//! clear and draw when the next one is identical; the surface then also
//! skips `eglSwapBuffers` / `SwapBuffers`. A pending Wayland configure
//! serial is the one thing the surface knows and the GPU cannot, so the
//! surface passes `forced` to make that swap happen regardless.
//! Linux stays GLES 3.0 (`#version 300 es`). Windows is GL 3.3 core
//! (`#version 330 core`). Same body; only the version string is `cfg`'d.

use std::error::Error;

use glow::{Context as Glow, HasContext, PixelUnpackData};

use crate::overlay::font::{Atlas, Font, Glyph};
use crate::overlay::scene::{Scene, Text};

/// `hud.font_size` is CSS points. Convert with `size * 4/3` to pixels;
/// 12pt → 16px at the 2560×1440 authoring size.
pub const FONT_PT: f32 = 12.0;
const PT_TO_PX: f32 = 4.0 / 3.0;

#[cfg(target_os = "windows")]
const GLSL_VERSION: &str = "#version 330 core";
#[cfg(not(target_os = "windows"))]
const GLSL_VERSION: &str = "#version 300 es";

const VS: &str = r"
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
";

const FS: &str = r"
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
";

const STRIDE: i32 = 32;
const OUTLINE_COLOR: [f32; 4] = [0.0, 0.0, 0.0, 1.0];

/// Buffer and logical size of one frame. Part of the skip fingerprint: a
/// resize or a fractional-scale change must redraw even when the scene is
/// the same, because the vertices are scaled by `buf / logical`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Frame {
    pub buf_w: i32,
    pub buf_h: i32,
    pub logical_w: i32,
    pub logical_h: i32,
}

impl Frame {
    /// Buffer and logical size are the same (Win32; no HiDPI scaling here).
    #[cfg(any(test, windows))]
    pub fn square(buf_w: i32, buf_h: i32) -> Self {
        Self {
            buf_w,
            buf_h,
            logical_w: buf_w,
            logical_h: buf_h,
        }
    }
}

/// The frame-skip decision, kept pure so it can be tested without a context.
/// `font_changed` is a font swap since the last draw (atlas rebuilt, same
/// scene would still need new glyph quads); `forced` is the surface's veto,
/// e.g. a Wayland configure that must be acked together with a swap.
fn should_draw(
    last: Option<(&Frame, &Scene)>,
    next: (&Frame, &Scene),
    font_changed: bool,
    forced: bool,
) -> bool {
    if forced || font_changed {
        return true;
    }
    last.is_none_or(|(frame, scene)| frame != next.0 || scene != next.1)
}

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
    /// What is on screen right now. `None` until the first draw of this
    /// context, so a fresh context (map, remap) always draws.
    last: Option<(Frame, Scene)>,
    /// `set_font` swapped the face since the last draw.
    font_changed: bool,
    /// Scratch vertex buffer, reused across frames.
    verts: Vec<f32>,
    /// Scratch glyph placements for one text run.
    runs: Vec<(f32, f32, Glyph)>,
}

impl Gpu {
    /// The surface must have made the context `gl` was loaded from current on
    /// this thread, and keep it current (and alive) for every later call on the
    /// returned `Gpu`, including its `Drop`.
    pub fn new(gl: Glow, buf_w: i32, buf_h: i32, font_want: &str) -> Result<Self, Box<dyn Error>> {
        // SAFETY: the caller's context is current on this thread (see above);
        // these are read-only queries on it.
        unsafe {
            debug!(
                "GL renderer={} version={}",
                gl.get_parameter_string(glow::RENDERER),
                gl.get_parameter_string(glow::VERSION)
            );
        }

        let font = Font::load(Some(font_want));
        let atlas = Atlas::new();
        // SAFETY: same current context; every handle used below was created
        // on it by the line before, and `u_atlas` is bound while `program` is in use.
        let (program, u_resolution, vao, vbo, atlas_tex) = unsafe {
            let program = link_program(&gl)?;
            let u_resolution = gl
                .get_uniform_location(program, "u_resolution")
                .ok_or("u_resolution missing")?;
            let u_atlas = gl
                .get_uniform_location(program, "u_atlas")
                .ok_or("u_atlas missing")?;
            let vao = gl.create_vertex_array()?;
            let vbo = gl.create_buffer()?;
            let atlas_tex = gl.create_texture()?;
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
            (program, u_resolution, vao, vbo, atlas_tex)
        };

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
            last: None,
            font_changed: false,
            verts: Vec::new(),
            runs: Vec::new(),
        };
        gpu.upload_atlas();
        debug!(
            "font={} atlas={}x{} (hinted LCD, 1px outline)",
            gpu.font.name, gpu.atlas.width, gpu.atlas.height
        );
        Ok(gpu)
    }

    #[cfg(target_os = "linux")]
    pub fn resize(&self, buf_w: i32, buf_h: i32) {
        // SAFETY: the surface made this Gpu's context current before calling in.
        unsafe { self.gl.viewport(0, 0, buf_w, buf_h) };
    }

    pub fn set_font(&mut self, want: &str) {
        let want = want.trim();
        if want == self.font_want {
            return;
        }
        match crate::overlay::font::try_load(Some(want)) {
            Ok(font) => {
                self.font = font;
                self.font_want = want.to_string();
                self.atlas.reset();
                self.font_changed = true;
                debug!(
                    "font={} atlas={}x{} (hinted LCD, 1px outline)",
                    self.font.name, self.atlas.width, self.atlas.height
                );
            }
            Err(err) => {
                warn!("font: {err}; keeping {}", self.font.name);
                self.font_want = want.to_string();
            }
        }
    }

    /// Renders `scene` into the bound framebuffer and returns `true`, or
    /// returns `false` without touching GL when the frame would be identical
    /// to the one already on screen. The caller swaps only on `true`.
    pub fn draw(
        &mut self,
        frame: Frame,
        scene: Scene,
        forced: bool,
    ) -> Result<bool, Box<dyn Error>> {
        let last = self.last.as_ref().map(|(f, s)| (f, s));
        if !should_draw(last, (&frame, &scene), self.font_changed, forced) {
            return Ok(false);
        }
        let Frame {
            buf_w,
            buf_h,
            logical_w,
            logical_h,
        } = frame;
        let sx = buf_w as f32 / logical_w.max(1) as f32;
        let sy = buf_h as f32 / logical_h.max(1) as f32;
        let hud_px = scene
            .texts
            .first()
            .map_or(FONT_PT * PT_TO_PX * sy, |t| t.font_px * sy);
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
            self.upload_atlas();
        }

        let verts = &mut self.verts;
        verts.clear();
        let (wu, wv) = self.atlas.white_uv();
        for text in &scene.texts {
            push_text(&self.atlas, &self.font, verts, &mut self.runs, text, sx, sy)?;
        }
        for fill in &scene.fills {
            push_quad(
                verts,
                Rect {
                    x: fill.x * sx,
                    y: fill.y * sy,
                    w: fill.w * sx,
                    h: fill.h * sy,
                },
                fill.color,
                UvRect::point(wu, wv),
            );
        }
        let stroke_s = sx.midpoint(sy);
        for stroke in &scene.strokes {
            push_line(
                verts,
                Segment {
                    x0: stroke.x0 * sx,
                    y0: stroke.y0 * sy,
                    x1: stroke.x1 * sx,
                    y1: stroke.y1 * sy,
                },
                stroke.width * stroke_s,
                stroke.color,
                [wu, wv],
            );
        }
        for text in &scene.labels {
            push_text(&self.atlas, &self.font, verts, &mut self.runs, text, sx, sy)?;
        }

        // SAFETY: the surface made this Gpu's context current; the handles were
        // created on it in `new`, and `verts` is a whole number of 8-float
        // vertices (32-byte stride) uploaded before `draw_arrays` counts them.
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
                verts_as_bytes(verts),
                glow::STREAM_DRAW,
            );
            self.gl
                .draw_arrays(glow::TRIANGLES, 0, (verts.len() / 8) as i32);
        }
        self.last = Some((frame, scene));
        self.font_changed = false;
        Ok(true)
    }

    fn upload_atlas(&mut self) {
        // SAFETY: context current as for `draw`; `atlas.pixels` is exactly
        // `width * height * 3` bytes, which RGB/UNSIGNED_BYTE at unpack
        // alignment 1 reads in full and no further.
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
    }
}

impl Drop for Gpu {
    fn drop(&mut self) {
        // SAFETY: both surfaces drop `Gpu` before the context it was created
        // on (field order in `wayland::App` / `win32::Surface`, and the
        // explicit `take` order when unmapping), so the context is still
        // current and each handle is deleted once.
        unsafe {
            self.gl.delete_texture(self.atlas_tex);
            self.gl.delete_buffer(self.vbo);
            self.gl.delete_vertex_array(self.vao);
            self.gl.delete_program(self.program);
        }
    }
}

fn verts_as_bytes(verts: &[f32]) -> &[u8] {
    // SAFETY: every byte of an `f32` is initialised and any byte is a valid
    // `u8`; the pointer and byte length come from the same live slice, `u8`
    // has alignment 1, and the returned borrow keeps `verts` alive.
    unsafe { std::slice::from_raw_parts(verts.as_ptr().cast(), std::mem::size_of_val(verts)) }
}

/// Glyph quads for one text run. `runs` is caller-owned scratch so the
/// per-text placement list does not allocate every frame.
fn push_text(
    atlas: &Atlas,
    font: &Font,
    verts: &mut Vec<f32>,
    runs: &mut Vec<(f32, f32, Glyph)>,
    text: &Text,
    sx: f32,
    sy: f32,
) -> Result<(), Box<dyn Error>> {
    let px = text.font_px * sy;
    let x0 = text.x * sx;
    let y0 = text.y * sy;
    let baseline = y0 + font.ascent(px);
    let mut pen = x0;
    let mut ink_top = f32::MAX;
    let mut ink_bot = f32::MIN;
    runs.clear();
    for ch in text.text.chars() {
        let glyph = atlas
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
        let ink_mid = ink_top.midpoint(ink_bot);
        (box_mid - ink_mid).round()
    } else {
        0.0
    };
    for &(gx, gy, glyph) in runs.iter() {
        let gy = gy + dy;
        let (ou0, ov0, ou1, ov1) = atlas.uv(
            glyph.outline_x,
            glyph.outline_y,
            glyph.outline_w,
            glyph.outline_h,
        );
        if text.outline {
            let oc = text.outline_color.unwrap_or(OUTLINE_COLOR);
            push_quad(
                verts,
                Rect {
                    x: gx - 1.0,
                    y: gy - 1.0,
                    w: glyph.outline_w as f32,
                    h: glyph.outline_h as f32,
                },
                [oc[0], oc[1], oc[2], oc[3] * text.color[3]],
                UvRect {
                    u0: ou0,
                    v0: ov0,
                    u1: ou1,
                    v1: ov1,
                },
            );
        }
        let (u0, v0, u1, v1) = atlas.uv(glyph.atlas_x, glyph.atlas_y, glyph.width, glyph.height);
        push_quad(
            verts,
            Rect {
                x: gx,
                y: gy,
                w: glyph.width as f32,
                h: glyph.height as f32,
            },
            text.color,
            UvRect { u0, v0, u1, v1 },
        );
    }
    Ok(())
}

#[derive(Clone, Copy)]
struct Rect {
    x: f32,
    y: f32,
    w: f32,
    h: f32,
}

#[derive(Clone, Copy)]
struct UvRect {
    u0: f32,
    v0: f32,
    u1: f32,
    v1: f32,
}

impl UvRect {
    fn point(u: f32, v: f32) -> Self {
        Self {
            u0: u,
            v0: v,
            u1: u,
            v1: v,
        }
    }
}

#[derive(Clone, Copy)]
struct Segment {
    x0: f32,
    y0: f32,
    x1: f32,
    y1: f32,
}

fn push_quad(verts: &mut Vec<f32>, rect: Rect, color: [f32; 4], uv: UvRect) {
    let Rect { x, y, w, h } = rect;
    let UvRect { u0, v0, u1, v1 } = uv;
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

fn push_line(verts: &mut Vec<f32>, segment: Segment, width: f32, color: [f32; 4], uv: [f32; 2]) {
    let Segment { x0, y0, x1, y1 } = segment;
    let [u, v] = uv;
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

/// # Safety
/// The context `gl` was loaded from must be current on this thread.
unsafe fn link_program(gl: &Glow) -> Result<glow::Program, Box<dyn Error>> {
    // SAFETY: the caller's contract above; the shader and program handles are
    // created here and used only while it holds.
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

/// # Safety
/// The context `gl` was loaded from must be current on this thread.
unsafe fn compile(gl: &Glow, kind: u32, src: String) -> Result<glow::Shader, Box<dyn Error>> {
    // SAFETY: the caller's contract above; `shader` was just created on that context.
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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::Config;
    use crate::overlay::layout::Viewport;
    use crate::overlay::{dummy, scene};

    fn dummy_scene(clock: &str, w: i32, h: i32) -> Scene {
        scene::build(
            &dummy::view(clock),
            &Config::default(),
            Viewport {
                width: w as f32,
                height: h as f32,
            },
        )
    }

    #[test]
    fn first_frame_of_a_context_always_draws() {
        let frame = Frame::square(320, 180);
        let scene = dummy_scene("12:00:00", 320, 180);
        assert!(should_draw(None, (&frame, &scene), false, false));
    }

    #[test]
    fn identical_frame_is_skipped() {
        let frame = Frame::square(320, 180);
        let last = dummy_scene("12:00:00", 320, 180);
        let next = dummy_scene("12:00:00", 320, 180);
        assert!(!should_draw(
            Some((&frame, &last)),
            (&frame, &next),
            false,
            false
        ));
    }

    #[test]
    fn changed_scene_draws() {
        let frame = Frame::square(320, 180);
        let last = dummy_scene("12:00:00", 320, 180);
        let next = dummy_scene("12:00:01", 320, 180);
        assert!(should_draw(
            Some((&frame, &last)),
            (&frame, &next),
            false,
            false
        ));
    }

    #[test]
    fn pending_configure_forces_the_swap() {
        let frame = Frame::square(320, 180);
        let scene = dummy_scene("12:00:00", 320, 180);
        assert!(should_draw(
            Some((&frame, &scene)),
            (&frame, &scene),
            false,
            true
        ));
    }

    #[test]
    fn size_or_scale_change_draws() {
        let scene = dummy_scene("12:00:00", 320, 180);
        let last = Frame::square(320, 180);
        let resized = Frame::square(640, 360);
        assert!(should_draw(
            Some((&last, &scene)),
            (&resized, &scene),
            false,
            false
        ));
        // Same logical size, bigger buffer: a fractional-scale change.
        let rescaled = Frame {
            buf_w: 400,
            buf_h: 225,
            logical_w: 320,
            logical_h: 180,
        };
        assert!(should_draw(
            Some((&last, &scene)),
            (&rescaled, &scene),
            false,
            false
        ));
    }

    #[test]
    fn font_change_draws_the_same_scene() {
        let frame = Frame::square(320, 180);
        let scene = dummy_scene("12:00:00", 320, 180);
        assert!(should_draw(
            Some((&frame, &scene)),
            (&frame, &scene),
            true,
            false
        ));
    }

    /// One frame through the real stack: EGL context, shader compile, glyph
    /// atlas, draw, readback. Mesa's surfaceless platform needs no compositor
    /// (llvmpipe on CI). Skips where that platform is missing, unless
    /// DF_HUD_REQUIRE_RENDER_TEST says a skip is a failure (set on CI).
    #[cfg(target_os = "linux")]
    #[test]
    fn renders_a_dummy_frame_headless() {
        use crate::overlay::egl::Egl;

        let skip = |why: &str| {
            assert!(
                std::env::var_os("DF_HUD_REQUIRE_RENDER_TEST").is_none(),
                "render test is required here but got skipped: {why}"
            );
            eprintln!("render smoke: skipped: {why}");
        };
        let egl = match Egl::load() {
            Ok(egl) => egl,
            Err(err) => return skip(&err.to_string()),
        };
        let display = match egl.get_surfaceless_display() {
            Ok(display) => display,
            Err(err) => return skip(&err.to_string()),
        };
        if let Err(err) = egl.initialize(display) {
            return skip(&err.to_string());
        }

        // From here on everything is expected to work: failures are bugs.
        egl.bind_es().unwrap();
        let config = egl.choose_es3_pbuffer_config(display).unwrap();
        let (w, h) = (320, 180);
        let surface = egl.create_pbuffer_surface(display, config, w, h).unwrap();
        let ctx = egl.create_es3_context(display, config).unwrap();
        egl.make_current(display, surface, surface, ctx).unwrap();
        // SAFETY: `ctx` is current on this thread, so `get_proc_address`
        // resolves each GLES entry point for it (or null, which glow tolerates).
        let gl = unsafe { Glow::from_loader_function(|name| egl.get_proc_address(name)) };

        let mut gpu = Gpu::new(gl, w, h, "").unwrap();
        let built = dummy_scene("12:00:00", w, h);
        assert!(!built.texts.is_empty(), "the dummy view must draw text");
        let frame = Frame::square(w, h);
        assert!(gpu.draw(frame, built.clone(), false).unwrap());
        assert!(
            !gpu.draw(frame, built.clone(), false).unwrap(),
            "the same scene again must be skipped"
        );
        assert!(
            gpu.draw(frame, built, true).unwrap(),
            "a forced frame (pending configure) must draw"
        );
        assert!(
            gpu.draw(frame, dummy_scene("12:00:01", w, h), false)
                .unwrap(),
            "a new clock reading must draw"
        );

        let mut px = vec![0u8; (w * h * 4) as usize];
        // SAFETY: `ctx` is still current and `px` holds the `w * h * 4` bytes
        // that an RGBA/UNSIGNED_BYTE readback of the pbuffer writes.
        unsafe {
            gpu.gl.read_pixels(
                0,
                0,
                w,
                h,
                glow::RGBA,
                glow::UNSIGNED_BYTE,
                glow::PixelPackData::Slice(Some(&mut px)),
            );
        }
        assert!(
            px.iter().any(|&b| b != 0),
            "the frame stayed transparent black"
        );

        egl.unbind(display);
        egl.destroy_surface(display, surface);
        egl.destroy_context(display, ctx);
        egl.terminate(display);
    }
}
