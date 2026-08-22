use std::error::Error;

use glow::{Context as Glow, HasContext};

use crate::font;
use crate::wgl::GlSurface;

const VS: &str = r#"#version 330 core
layout(location = 0) in vec2 a_pos;
layout(location = 1) in vec4 a_color;
uniform vec2 u_resolution;
out vec4 v_color;
void main() {
    vec2 clip = (a_pos / u_resolution) * 2.0 - 1.0;
    gl_Position = vec4(clip.x, -clip.y, 0.0, 1.0);
    v_color = a_color;
}
"#;

const FS: &str = r#"#version 330 core
in vec4 v_color;
out vec4 frag;
void main() {
    // DWM layered windows blend premultiplied, same as Wayland.
    frag = vec4(v_color.rgb * v_color.a, v_color.a);
}
"#;

pub struct Frame<'a> {
    pub buf_w: i32,
    pub buf_h: i32,
    pub monitor_name: &'a str,
    pub dpi: u32,
    pub inset: i32,
    pub swaps: u32,
    pub hz: f32,
    pub swap_interval: i32,
    pub grid: bool,
    pub solid: bool,
    pub text_only: bool,
}

pub struct Gpu {
    surface: GlSurface,
    gl: Glow,
    program: glow::Program,
    vao: glow::VertexArray,
    vbo: glow::Buffer,
    u_resolution: glow::UniformLocation,
}

impl Gpu {
    pub fn new(surface: GlSurface, buf_w: i32, buf_h: i32) -> Result<Self, Box<dyn Error>> {
        surface.make_current()?;
        let gl = unsafe { Glow::from_loader_function(|name| surface.load_proc(name)) };
        unsafe {
            eprintln!(
                "GL renderer={} version={}",
                gl.get_parameter_string(glow::RENDERER),
                gl.get_parameter_string(glow::VERSION)
            );
        }
        let program = unsafe { link_program(&gl)? };
        let u_resolution = unsafe {
            gl.get_uniform_location(program, "u_resolution")
                .ok_or("u_resolution missing")?
        };
        let vao = unsafe { gl.create_vertex_array()? };
        let vbo = unsafe { gl.create_buffer()? };
        unsafe {
            gl.bind_framebuffer(glow::FRAMEBUFFER, None);
            gl.bind_vertex_array(Some(vao));
            gl.bind_buffer(glow::ARRAY_BUFFER, Some(vbo));
            gl.vertex_attrib_pointer_f32(0, 2, glow::FLOAT, false, 24, 0);
            gl.enable_vertex_attrib_array(0);
            gl.vertex_attrib_pointer_f32(1, 4, glow::FLOAT, false, 24, 8);
            gl.enable_vertex_attrib_array(1);
            gl.enable(glow::BLEND);
            gl.blend_func(glow::ONE, glow::ONE_MINUS_SRC_ALPHA);
            gl.disable(glow::DEPTH_TEST);
            gl.viewport(0, 0, buf_w, buf_h);
        }
        Ok(Self {
            surface,
            gl,
            program,
            vao,
            vbo,
            u_resolution,
        })
    }

    #[allow(dead_code)]
    pub fn resize(&self, buf_w: i32, buf_h: i32) {
        unsafe { self.gl.viewport(0, 0, buf_w, buf_h) };
    }

    pub fn draw(&self, frame: &Frame<'_>) -> Result<(), Box<dyn Error>> {
        self.surface.make_current()?;
        let mut verts = Vec::new();
        let sx = 1.0;
        let sy = 1.0;

        if frame.solid {
            push_rect(
                &mut verts,
                0.0,
                0.0,
                frame.buf_w as f32,
                frame.buf_h as f32,
                [1.0, 0.0, 1.0, 1.0],
            );
        } else if !frame.text_only {
            let pw = 640.0 * sx;
            let ph = 220.0 * sy;
            let px0 = (frame.buf_w as f32 - pw) * 0.5;
            let py0 = (frame.buf_h as f32 - ph) * 0.5;
            push_rect(&mut verts, px0, py0, pw, ph, [0.15, 0.75, 1.0, 1.0]);
        }

        if frame.grid {
            let step = 32.0_f32;
            let line = [1.0, 1.0, 1.0, 0.35];
            let mut x = 0.0;
            while x < frame.buf_w as f32 {
                push_rect(&mut verts, x, 0.0, 1.0, frame.buf_h as f32, line);
                x += step;
            }
            let mut y = 0.0;
            while y < frame.buf_h as f32 {
                push_rect(&mut verts, 0.0, y, frame.buf_w as f32, 1.0, line);
                y += step;
            }
        }

        let px = if frame.text_only { 4.0 } else { 3.0 };
        let text = [
            "df-hud WGL 3.3 spike".to_string(),
            format!(
                "{}  {}x{}  dpi {}  inset {}",
                frame.monitor_name, frame.buf_w, frame.buf_h, frame.dpi, frame.inset
            ),
            format!(
                "click-through  swap-interval {}  {:.0} Hz  swaps {}",
                frame.swap_interval, frame.hz, frame.swaps
            ),
        ];
        let panel_w = 640.0;
        let panel_h = 220.0;
        let text_x = (frame.buf_w as f32 - panel_w) * 0.5 + 24.0;
        let mut ty = (frame.buf_h as f32 - panel_h) * 0.5 + 36.0;
        for line in &text {
            draw_outlined(&mut verts, text_x, ty, px, line);
            ty += (font::height() + 3.0) * px;
        }

        if frame.text_only {
            ty += 12.0;
            let box_w = 120.0;
            let box_h = 48.0;
            for (i, a) in [0.25_f32, 0.5, 0.75, 1.0].iter().enumerate() {
                push_rect(
                    &mut verts,
                    text_x + i as f32 * (box_w + 12.0),
                    ty,
                    box_w,
                    box_h,
                    [1.0, 1.0, 1.0, *a],
                );
            }
        }

        unsafe {
            self.gl.bind_framebuffer(glow::FRAMEBUFFER, None);
            if frame.solid {
                self.gl.clear_color(1.0, 0.0, 1.0, 1.0);
            } else {
                self.gl.clear_color(0.0, 0.0, 0.0, 0.0);
            }
            self.gl.clear(glow::COLOR_BUFFER_BIT);
            self.gl.use_program(Some(self.program));
            self.gl.uniform_2_f32(
                Some(&self.u_resolution),
                frame.buf_w as f32,
                frame.buf_h as f32,
            );
            self.gl.bind_vertex_array(Some(self.vao));
            self.gl.bind_buffer(glow::ARRAY_BUFFER, Some(self.vbo));
            self.gl.buffer_data_u8_slice(
                glow::ARRAY_BUFFER,
                verts_as_bytes(&verts),
                glow::STREAM_DRAW,
            );
            self.gl
                .draw_arrays(glow::TRIANGLES, 0, (verts.len() / 6) as i32);
            let err = self.gl.get_error();
            if err != glow::NO_ERROR && frame.swaps < 3 {
                eprintln!("GL error 0x{err:x} after draw");
            }
        }
        self.surface.swap()?;
        Ok(())
    }
}

fn verts_as_bytes(verts: &[f32]) -> &[u8] {
    unsafe { std::slice::from_raw_parts(verts.as_ptr().cast(), std::mem::size_of_val(verts)) }
}

fn push_rect(verts: &mut Vec<f32>, x: f32, y: f32, w: f32, h: f32, color: [f32; 4]) {
    let [r, g, b, a] = color;
    let x2 = x + w;
    let y2 = y + h;
    for [px, py] in [[x, y], [x2, y], [x, y2], [x, y2], [x2, y], [x2, y2]] {
        verts.extend_from_slice(&[px, py, r, g, b, a]);
    }
}

fn draw_outlined(verts: &mut Vec<f32>, x: f32, y: f32, px: f32, text: &str) {
    let black = [0.0, 0.0, 0.0, 1.0];
    let white = [1.0, 1.0, 1.0, 1.0];
    for dy in -1..=1 {
        for dx in -1..=1 {
            if dx == 0 && dy == 0 {
                continue;
            }
            draw_line(
                verts,
                x + dx as f32 * px,
                y + dy as f32 * px,
                px,
                text,
                black,
            );
        }
    }
    draw_line(verts, x, y, px, text, white);
}

fn draw_line(verts: &mut Vec<f32>, mut x: f32, y: f32, px: f32, text: &str, color: [f32; 4]) {
    for ch in text.bytes() {
        let bits = font::glyph(ch);
        for (row, row_bits) in bits.iter().enumerate() {
            for col in 0..5 {
                if row_bits & (1 << (4 - col)) != 0 {
                    push_rect(
                        verts,
                        x + col as f32 * px,
                        y + row as f32 * px,
                        px,
                        px,
                        color,
                    );
                }
            }
        }
        x += font::advance() * px;
    }
}

unsafe fn link_program(gl: &Glow) -> Result<glow::Program, Box<dyn Error>> {
    unsafe {
        let vs = compile(gl, glow::VERTEX_SHADER, VS)?;
        let fs = compile(gl, glow::FRAGMENT_SHADER, FS)?;
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

unsafe fn compile(gl: &Glow, kind: u32, src: &str) -> Result<glow::Shader, Box<dyn Error>> {
    unsafe {
        let shader = gl.create_shader(kind)?;
        gl.shader_source(shader, src);
        gl.compile_shader(shader);
        if !gl.get_shader_compile_status(shader) {
            return Err(gl.get_shader_info_log(shader).into());
        }
        Ok(shader)
    }
}
