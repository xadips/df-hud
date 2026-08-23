//! Bundled TTF → grayscale atlas. No Fontconfig, no Pango, no RGB subpixel AA.
//!
//! `font_path` / TOML waits for Phase 4. Gpu on EGL and WGL share this.

#![allow(dead_code)]

use std::collections::HashMap;
use std::error::Error;

/// Go Mono Bold. Same family the Go HUD embeds on Windows; GTK is `font-weight: bold`.
/// License: `assets/fonts/Go-fonts-LICENSE` (BSD, Bigelow & Holmes / Go project).
const TTF: &[u8] = include_bytes!("../assets/fonts/Go-Mono-Bold.ttf");

const ATLAS_START: u32 = 512;
/// Extra texels so LINEAR filtering does not pick a neighbour glyph.
const PAD: u32 = 2;

pub struct Font {
    inner: fontdue::Font,
}

#[derive(Clone, Copy)]
pub struct Glyph {
    pub atlas_x: u32,
    pub atlas_y: u32,
    pub width: u32,
    pub height: u32,
    pub xmin: i32,
    pub ymin: i32,
    pub advance: f32,
    pub outline_x: u32,
    pub outline_y: u32,
    pub outline_w: u32,
    pub outline_h: u32,
}

pub struct Atlas {
    pub width: u32,
    pub height: u32,
    pub pixels: Vec<u8>,
    pub dirty: bool,
    shelf_x: u32,
    shelf_y: u32,
    shelf_h: u32,
    glyphs: HashMap<(char, u16), Glyph>,
}

impl Font {
    pub fn load() -> Result<Self, Box<dyn Error>> {
        let inner = fontdue::Font::from_bytes(TTF, fontdue::FontSettings::default())
            .map_err(|err| format!("parse bundled Go Mono Bold: {err}"))?;
        Ok(Self { inner })
    }

    pub fn ascent(&self, px: f32) -> f32 {
        self.inner
            .horizontal_line_metrics(px)
            .map(|m| m.ascent)
            .unwrap_or(px)
    }

    fn rasterize(&self, ch: char, px: f32) -> (fontdue::Metrics, Vec<u8>) {
        self.inner.rasterize(ch, px)
    }
}

impl Atlas {
    pub fn new() -> Self {
        let width = ATLAS_START;
        let height = ATLAS_START;
        let mut pixels = vec![0u8; (width * height) as usize];
        // Shader's untextured path samples this 1×1 white texel.
        pixels[0] = 255;
        Self {
            width,
            height,
            pixels,
            dirty: true,
            shelf_x: 1 + PAD,
            shelf_y: PAD,
            shelf_h: 1,
            glyphs: HashMap::new(),
        }
    }

    pub fn white_uv(&self) -> (f32, f32) {
        let w = self.width as f32;
        let h = self.height as f32;
        (0.5 / w, 0.5 / h)
    }

    pub fn uv(&self, x: u32, y: u32, w: u32, h: u32) -> (f32, f32, f32, f32) {
        let aw = self.width as f32;
        let ah = self.height as f32;
        (
            x as f32 / aw,
            y as f32 / ah,
            (x + w) as f32 / aw,
            (y + h) as f32 / ah,
        )
    }

    pub fn reset(&mut self) {
        self.glyphs.clear();
        self.pixels.fill(0);
        self.pixels[0] = 255;
        self.shelf_x = 1 + PAD;
        self.shelf_y = PAD;
        self.shelf_h = 1;
        self.dirty = true;
    }

    pub fn get(&self, ch: char, px: f32) -> Option<Glyph> {
        self.glyphs.get(&(ch, px_key(px))).copied()
    }

    pub fn glyph(&mut self, font: &Font, ch: char, px: f32) -> Result<Glyph, Box<dyn Error>> {
        let key = (ch, px_key(px));
        if let Some(glyph) = self.glyphs.get(&key) {
            return Ok(*glyph);
        }
        let (metrics, bitmap) = font.rasterize(ch, px);
        let gw = metrics.width as u32;
        let gh = metrics.height as u32;
        let mut glyph = Glyph {
            atlas_x: 0,
            atlas_y: 0,
            width: gw,
            height: gh,
            xmin: metrics.xmin,
            ymin: metrics.ymin,
            advance: metrics.advance_width,
            outline_x: 0,
            outline_y: 0,
            outline_w: 0,
            outline_h: 0,
        };
        if gw > 0 && gh > 0 {
            let (atlas_x, atlas_y) = self.pack(gw, gh)?;
            self.blit(atlas_x, atlas_y, gw, gh, &bitmap);
            let (outline, ow, oh) = dilate(&bitmap, gw, gh);
            let (outline_x, outline_y) = self.pack(ow, oh)?;
            self.blit(outline_x, outline_y, ow, oh, &outline);
            glyph.atlas_x = atlas_x;
            glyph.atlas_y = atlas_y;
            glyph.outline_x = outline_x;
            glyph.outline_y = outline_y;
            glyph.outline_w = ow;
            glyph.outline_h = oh;
            self.dirty = true;
        }
        self.glyphs.insert(key, glyph);
        Ok(glyph)
    }

    fn pack(&mut self, gw: u32, gh: u32) -> Result<(u32, u32), Box<dyn Error>> {
        let need_w = gw + PAD;
        let need_h = gh + PAD;
        loop {
            if self.shelf_x + need_w > self.width {
                self.shelf_y += self.shelf_h;
                self.shelf_x = PAD;
                self.shelf_h = 0;
            }
            if self.shelf_y + need_h <= self.height {
                let x = self.shelf_x;
                let y = self.shelf_y;
                self.shelf_x += need_w;
                self.shelf_h = self.shelf_h.max(need_h);
                return Ok((x, y));
            }
            self.grow()?;
        }
    }

    fn blit(&mut self, x: u32, y: u32, w: u32, h: u32, src: &[u8]) {
        for row in 0..h {
            let dst = ((y + row) * self.width + x) as usize;
            let src_i = (row * w) as usize;
            self.pixels[dst..dst + w as usize].copy_from_slice(&src[src_i..src_i + w as usize]);
        }
    }

    fn grow(&mut self) -> Result<(), Box<dyn Error>> {
        let new_w = self.width.saturating_mul(2);
        let new_h = self.height.saturating_mul(2);
        if new_w > 4096 || new_h > 4096 {
            return Err("font atlas exceeded 4096²".into());
        }
        let mut next = vec![0u8; (new_w * new_h) as usize];
        for row in 0..self.height {
            let src = (row * self.width) as usize;
            let dst = (row * new_w) as usize;
            next[dst..dst + self.width as usize]
                .copy_from_slice(&self.pixels[src..src + self.width as usize]);
        }
        next[0] = 255;
        self.pixels = next;
        self.width = new_w;
        self.height = new_h;
        self.dirty = true;
        Ok(())
    }
}

fn px_key(px: f32) -> u16 {
    (px * 10.0).round().clamp(1.0, 65535.0) as u16
}

/// 8-neighbour max of coverage, +1px all around. Drawn in black under the fill
/// so the visible ring is one anti-aliased pixel, not eight stacked offset quads.
fn dilate(src: &[u8], w: u32, h: u32) -> (Vec<u8>, u32, u32) {
    let dw = w + 2;
    let dh = h + 2;
    let mut out = vec![0u8; (dw * dh) as usize];
    for y in 0..h {
        for x in 0..w {
            let v = src[(y * w + x) as usize];
            if v == 0 {
                continue;
            }
            for dy in 0..3u32 {
                for dx in 0..3u32 {
                    let i = ((y + dy) * dw + (x + dx)) as usize;
                    if v > out[i] {
                        out[i] = v;
                    }
                }
            }
        }
    }
    (out, dw, dh)
}
