//! Bundled TTF → hinted LCD atlas. No Fontconfig, no Pango, no `FreeType`.
//!
//! Linux prefers a system monospace bold (Noto / Liberation / `DejaVu`), then
//! falls back to the Go font Windows already embeds.
//!
//! The atlas is RGB coverage so ClearType-style edges survive; a transparent
//! overlay can still fringe on bright game pixels. Gpu on EGL and WGL share this.

use std::borrow::Cow;
use std::cell::RefCell;
use std::collections::HashMap;
use std::error::Error;
use std::path::{Path, PathBuf};

use swash::scale::image::Content;
use swash::scale::{Render, ScaleContext, Source};
use swash::zeno::Format;
use swash::FontRef;

/// Go Mono Bold. License: `assets/fonts/Go-fonts-LICENSE` (BSD, Bigelow & Holmes / Go project).
const BUNDLED_TTF: &[u8] = include_bytes!("../../assets/fonts/Go-Mono-Bold.ttf");
const BUNDLED_NAME: &str = "Go Mono Bold";

/// System monospace bold faces, in the order we prefer. Filenames only — no Fontconfig.
const SYSTEM_MONO_BOLD: &[&str] = &[
    "NotoSansMono-Bold.ttf",
    "NotoSansMono-Bold.otf",
    "LiberationMono-Bold.ttf",
    "DejaVuSansMono-Bold.ttf",
    "Cousine-Bold.ttf",
    "courbd.ttf",
];

const ATLAS_START: u32 = 512;
/// Extra texels so LINEAR filtering does not pick a neighbour glyph.
const PAD: u32 = 2;
const RGB: usize = 3;

pub struct Font {
    data: Cow<'static, [u8]>,
    fallback: Option<Cow<'static, [u8]>>,
    ctx: RefCell<ScaleContext>,
    pub name: String,
}

#[derive(Clone, Copy)]
pub struct Glyph {
    pub atlas_x: u32,
    pub atlas_y: u32,
    pub width: u32,
    pub height: u32,
    pub xmin: i32,
    pub top: i32,
    pub advance: f32,
    pub outline_x: u32,
    pub outline_y: u32,
    pub outline_w: u32,
    pub outline_h: u32,
}

struct Raster {
    width: u32,
    height: u32,
    xmin: i32,
    top: i32,
    advance: f32,
    rgb: Vec<u8>,
}

pub struct Atlas {
    pub width: u32,
    pub height: u32,
    pub pixels: Vec<u8>,
    pub dirty: bool,
    shelf_x: u32,
    shelf_y: u32,
    shelf_h: u32,
    glyphs: HashMap<(char, u16, bool), Glyph>,
}

impl Font {
    pub fn load(want: Option<&str>) -> Result<Self, Box<dyn Error>> {
        match try_load(want) {
            Ok(font) => Ok(font),
            Err(err) => {
                eprintln!("font: {err}; using auto");
                Ok(load_auto())
            }
        }
    }

    fn font_ref(&self) -> Result<FontRef<'_>, Box<dyn Error>> {
        FontRef::from_index(self.data.as_ref(), 0)
            .ok_or_else(|| format!("{}: not a TTF/OTF", self.name).into())
    }

    fn fallback_ref(&self) -> Option<FontRef<'_>> {
        FontRef::from_index(self.fallback.as_deref()?, 0)
    }

    pub fn ascent(&self, px: f32) -> f32 {
        let Ok(font) = self.font_ref() else {
            return px;
        };
        font.metrics(&[]).scale(px).ascent
    }

    fn rasterize(&self, ch: char, px: f32, lcd: bool) -> Raster {
        let Ok(primary) = self.font_ref() else {
            return Raster::empty();
        };
        let id = primary.charmap().map(ch);
        if id != 0 || ch == '\0' {
            return rasterize_ref(&self.ctx, primary, id, px, lcd);
        }
        if let Some(fb) = self.fallback_ref() {
            let fid = fb.charmap().map(ch);
            if fid != 0 {
                return rasterize_ref(&self.ctx, fb, fid, px, lcd);
            }
        }
        rasterize_ref(&self.ctx, primary, id, px, lcd)
    }
}

pub(crate) fn try_load(want: Option<&str>) -> Result<Font, Box<dyn Error>> {
    let want = want.map(str::trim).filter(|s| !s.is_empty());
    match want {
        None => Ok(load_auto()),
        Some(want) => load_named(want),
    }
}

fn load_auto() -> Font {
    if let Some((bytes, name)) = load_system_mono_bold() {
        return parse_font(Cow::Owned(bytes), name, true).expect("system TTF");
    }
    parse_font(Cow::Borrowed(BUNDLED_TTF), BUNDLED_NAME.to_string(), false).expect("bundled TTF")
}

fn load_named(want: &str) -> Result<Font, Box<dyn Error>> {
    let path = resolve_font_path(want)?;
    let Some((bytes, name)) = read_ttf(&path) else {
        return Err(format!("{}: not a TTF/OTF", path.display()).into());
    };
    parse_font(Cow::Owned(bytes), name, true)
}

fn resolve_font_path(want: &str) -> Result<PathBuf, Box<dyn Error>> {
    if looks_like_path(want) {
        let path = PathBuf::from(crate::config::expand_home(want));
        if path.is_file() {
            return Ok(path);
        }
        return Err(format!("{}: not found", path.display()).into());
    }
    let base = Path::new(want)
        .file_name()
        .map_or_else(|| PathBuf::from(want), PathBuf::from);
    for dir in font_dirs() {
        let path = dir.join(&base);
        if path.is_file() {
            return Ok(path);
        }
    }
    Err(format!("{want}: not found in font dirs").into())
}

fn looks_like_path(want: &str) -> bool {
    want.starts_with('~')
        || want.contains('/')
        || want.contains('\\')
        || Path::new(want).is_absolute()
}

fn rasterize_ref(
    ctx: &RefCell<ScaleContext>,
    font: FontRef<'_>,
    id: u16,
    px: f32,
    lcd: bool,
) -> Raster {
    let advance = font.glyph_metrics(&[]).scale(px).advance_width(id);
    let mut ctx = ctx.borrow_mut();
    let mut scaler = ctx.builder(font).size(px).hint(true).build();
    let format = if lcd { Format::Subpixel } else { Format::Alpha };
    let Some(image) = Render::new(&[Source::Outline])
        .format(format)
        .render(&mut scaler, id)
    else {
        return Raster {
            width: 0,
            height: 0,
            xmin: 0,
            top: 0,
            advance,
            rgb: Vec::new(),
        };
    };
    let rgb = image_to_rgb(&image);
    Raster {
        width: image.placement.width,
        height: image.placement.height,
        xmin: image.placement.left,
        top: image.placement.top,
        advance,
        rgb,
    }
}

impl Raster {
    fn empty() -> Self {
        Self {
            width: 0,
            height: 0,
            xmin: 0,
            top: 0,
            advance: 0.0,
            rgb: Vec::new(),
        }
    }
}

fn parse_font(
    data: Cow<'static, [u8]>,
    name: String,
    fallback: bool,
) -> Result<Font, Box<dyn Error>> {
    if FontRef::from_index(data.as_ref(), 0).is_none() {
        return Err(format!("{name}: not a TTF/OTF").into());
    }
    Ok(Font {
        data,
        fallback: fallback.then_some(Cow::Borrowed(BUNDLED_TTF)),
        ctx: RefCell::new(ScaleContext::new()),
        name,
    })
}

fn image_to_rgb(image: &swash::scale::image::Image) -> Vec<u8> {
    let n = image.placement.width as usize * image.placement.height as usize;
    match image.content {
        Content::SubpixelMask | Content::Color if image.data.len() >= n * 4 => image
            .data
            .as_chunks::<4>()
            .0
            .iter()
            .take(n)
            .flat_map(|p| [p[0], p[1], p[2]])
            .collect(),
        Content::SubpixelMask if image.data.len() >= n * 3 => image.data[..n * 3].to_vec(),
        Content::Mask if image.data.len() >= n => {
            image.data.iter().take(n).flat_map(|&a| [a, a, a]).collect()
        }
        _ => vec![0u8; n * 3],
    }
}

fn load_system_mono_bold() -> Option<(Vec<u8>, String)> {
    for dir in font_dirs() {
        for file in SYSTEM_MONO_BOLD {
            let path = dir.join(file);
            if let Some(got) = read_ttf(&path) {
                return Some(got);
            }
        }
    }
    None
}

fn read_ttf(path: &Path) -> Option<(Vec<u8>, String)> {
    let bytes = std::fs::read(path).ok()?;
    if bytes.len() < 8 {
        return None;
    }
    let name = path
        .file_stem()
        .and_then(|s| s.to_str())
        .unwrap_or("system mono")
        .replace('-', " ");
    Some((bytes, name))
}

fn font_dirs() -> Vec<PathBuf> {
    let mut dirs = vec![
        PathBuf::from("/usr/share/fonts/noto"),
        PathBuf::from("/usr/share/fonts/truetype/noto"),
        PathBuf::from("/usr/share/fonts/TTF"),
        PathBuf::from("/usr/share/fonts/liberation"),
        PathBuf::from("/usr/share/fonts/truetype/liberation"),
        PathBuf::from("/usr/share/fonts/dejavu"),
        PathBuf::from("/usr/share/fonts/truetype/dejavu"),
        PathBuf::from("/usr/local/share/fonts"),
    ];
    if let Some(home) = std::env::var_os("HOME") {
        dirs.push(PathBuf::from(home).join(".local/share/fonts"));
    }
    if let Some(windir) = std::env::var_os("WINDIR") {
        dirs.push(PathBuf::from(windir).join("Fonts"));
    } else {
        dirs.push(PathBuf::from(r"C:\Windows\Fonts"));
    }
    dirs
}

impl Atlas {
    pub fn new() -> Self {
        let width = ATLAS_START;
        let height = ATLAS_START;
        let mut pixels = vec![0u8; (width * height) as usize * RGB];
        pixels[0] = 255;
        pixels[1] = 255;
        pixels[2] = 255;
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
        self.pixels[1] = 255;
        self.pixels[2] = 255;
        self.shelf_x = 1 + PAD;
        self.shelf_y = PAD;
        self.shelf_h = 1;
        self.dirty = true;
    }

    pub fn get(&self, ch: char, px: f32, lcd: bool) -> Option<Glyph> {
        self.glyphs.get(&(ch, px_key(px), lcd)).copied()
    }

    pub fn glyph(
        &mut self,
        font: &Font,
        ch: char,
        px: f32,
        lcd: bool,
    ) -> Result<Glyph, Box<dyn Error>> {
        let key = (ch, px_key(px), lcd);
        if let Some(glyph) = self.glyphs.get(&key) {
            return Ok(*glyph);
        }
        let raster = font.rasterize(ch, px, lcd);
        let gw = raster.width;
        let gh = raster.height;
        let mut glyph = Glyph {
            atlas_x: 0,
            atlas_y: 0,
            width: gw,
            height: gh,
            xmin: raster.xmin,
            top: raster.top,
            advance: raster.advance,
            outline_x: 0,
            outline_y: 0,
            outline_w: 0,
            outline_h: 0,
        };
        if gw > 0 && gh > 0 && raster.rgb.len() >= (gw * gh) as usize * RGB {
            let (atlas_x, atlas_y) = self.pack(gw, gh)?;
            self.blit(atlas_x, atlas_y, gw, gh, &raster.rgb);
            let (outline, ow, oh) = dilate_rgb(&raster.rgb, gw, gh);
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
            let dst = ((y + row) * self.width + x) as usize * RGB;
            let src_i = (row * w) as usize * RGB;
            self.pixels[dst..dst + w as usize * RGB]
                .copy_from_slice(&src[src_i..src_i + w as usize * RGB]);
        }
    }

    fn grow(&mut self) -> Result<(), Box<dyn Error>> {
        let new_w = self.width.saturating_mul(2);
        let new_h = self.height.saturating_mul(2);
        if new_w > 4096 || new_h > 4096 {
            return Err("font atlas exceeded 4096²".into());
        }
        let mut next = vec![0u8; (new_w * new_h) as usize * RGB];
        for row in 0..self.height {
            let src = (row * self.width) as usize * RGB;
            let dst = (row * new_w) as usize * RGB;
            let n = self.width as usize * RGB;
            next[dst..dst + n].copy_from_slice(&self.pixels[src..src + n]);
        }
        next[0] = 255;
        next[1] = 255;
        next[2] = 255;
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

fn dilate_rgb(src: &[u8], w: u32, h: u32) -> (Vec<u8>, u32, u32) {
    let mut cov = vec![0u8; (w * h) as usize];
    for (i, value) in cov.iter_mut().enumerate() {
        let o = i * RGB;
        *value = src[o].max(src[o + 1]).max(src[o + 2]);
    }
    let (gray, ow, oh) = dilate(&cov, w, h);
    let mut rgb = vec![0u8; gray.len() * RGB];
    for (i, v) in gray.iter().enumerate() {
        rgb[i * RGB] = *v;
        rgb[i * RGB + 1] = *v;
        rgb[i * RGB + 2] = *v;
    }
    (rgb, ow, oh)
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn load_rasterizes_map_glyphs() {
        let font = Font::load(None).unwrap();
        for ch in ['I', 'B', 'Δ', '▼', 'M'] {
            let g = font.rasterize(ch, 18.0, true);
            assert!(g.width > 0 && g.height > 0, "{ch} empty in {}", font.name);
            assert_eq!(g.rgb.len(), (g.width * g.height) as usize * RGB);
        }
    }

    #[test]
    fn linux_prefers_a_system_mono_when_present() {
        let font = Font::load(None).unwrap();
        if Path::new("/usr/share/fonts/noto/NotoSansMono-Bold.ttf").is_file() {
            assert!(
                font.name.to_ascii_lowercase().contains("noto"),
                "GTK monospace is Noto here, got {}",
                font.name
            );
        }
    }

    #[test]
    fn missing_file_falls_back_to_auto() {
        let font = Font::load(Some("/no/such/df-hud-font.ttf")).unwrap();
        let g = font.rasterize('A', 18.0, true);
        assert!(g.width > 0 && g.height > 0, "empty in {}", font.name);
    }

    #[test]
    fn basename_finds_a_system_mono() {
        let path = Path::new("/usr/share/fonts/noto/NotoSansMono-Bold.ttf");
        if !path.is_file() {
            return;
        }
        let font = try_load(Some("NotoSansMono-Bold.ttf")).unwrap();
        assert!(
            font.name.to_ascii_lowercase().contains("noto"),
            "got {}",
            font.name
        );
    }
}
