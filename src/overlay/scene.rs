//! Backend-neutral draw list: groups in 2560×1440, map rects/lines.
//!
//! Both Wayland and Win32 consume this. `present` fills View from `model::View`.

use std::collections::HashMap;

use crate::config::{parse_color, Config};
use crate::overlay::layout::{Transform, Viewport};

const PT_TO_PX: f32 = 4.0 / 3.0;
const MAP_MIN_CELL: i32 = 6;
const MAP_MAX_CELL: i32 = 96;
const MAP_BASE_SIZE: f32 = 1180.0;
const MAP_LIST_GAP: f32 = 10.0;
const MAP_MARKER_FONT: f32 = 0.72;
const LINE_HEIGHT: f32 = 1.25;

#[derive(Clone, Debug, Default)]
pub struct View {
    pub status: String,
    pub status_color: Option<[f32; 4]>,
    pub clock: String,
    pub xp: String,
    pub xp_color: Option<[f32; 4]>,
    pub block: String,
    pub block_sub: String,
    pub challenges: Vec<Line>,
    pub bosses: Vec<Line>,
    pub map: MapView,
}

#[derive(Clone, Debug, Default)]
pub struct Line {
    pub text: String,
    pub color: Option<[f32; 4]>,
    pub extra_ascent_px: f32,
    pub chip: Option<[f32; 4]>,
    pub chip_ring: bool,
    pub strike: bool,
    /// Countdown for this row. Empty on nest continuations. Bosses draw it white
    /// so it does not share the amber name colour; the map key dims it instead.
    pub timer: String,
    /// Onslaught prev/now/next caption. Empty on continuation and non-panel rows.
    pub label: String,
    /// Intra-line shades. Empty keeps a single `text` draw.
    pub runs: Vec<TextRun>,
}

/// One stretch of a `Line` at a different opacity than the rest.
#[derive(Clone, Debug)]
pub struct TextRun {
    pub text: String,
    /// Multiplier on `Line.color` (or the widget default). 1.0 is full.
    pub alpha: f32,
}

#[derive(Clone, Debug, Default)]
pub struct MapView {
    pub player_x: i32,
    pub player_y: i32,
    pub cells: Vec<MapCell>,
    pub markers: Vec<MapMarker>,
    pub dividers_x: Vec<i32>,
    pub dividers_y: Vec<i32>,
    pub list: Vec<Line>,
}

#[derive(Clone, Debug)]
pub struct MapCell {
    pub x: i32,
    pub y: i32,
    pub fill: [f32; 4],
}

#[derive(Clone, Debug)]
pub struct MapMarker {
    pub x: i32,
    pub y: i32,
    pub text: String,
    /// Letter on the grid. City marks stay white; only outposts tint the glyph.
    pub ink: [f32; 4],
    /// Ring / category colour. Legend chips use the same table.
    pub color: [f32; 4],
    pub ring: bool,
}

#[derive(Clone, Debug, Default)]
pub struct Scene {
    /// HUD text, drawn under the map (same order as Go).
    pub texts: Vec<Text>,
    pub fills: Vec<Fill>,
    pub strokes: Vec<Stroke>,
    /// Map markers + key, drawn on top.
    pub labels: Vec<Text>,
}

#[derive(Clone, Debug)]
pub struct Text {
    pub x: f32,
    pub y: f32,
    pub color: [f32; 4],
    pub text: String,
    pub font_px: f32,
    /// 1px outline. Off for chip letters: the fill is the contrast.
    pub outline: bool,
    pub outline_color: Option<[f32; 4]>,
    /// LCD subpixel. Off on chips — dark ink on a saturated fill fringes.
    pub lcd: bool,
    /// Vertically center the ink in `[y, y + center_h)`. Map markers use this
    /// so Δ / ▼ / I4 sit in the cell, not on the baseline at the floor.
    pub center_h: Option<f32>,
}

#[derive(Clone, Debug)]
pub struct Fill {
    pub x: f32,
    pub y: f32,
    pub w: f32,
    pub h: f32,
    pub color: [f32; 4],
}

#[derive(Clone, Debug)]
pub struct Stroke {
    pub x0: f32,
    pub y0: f32,
    pub x1: f32,
    pub y1: f32,
    pub width: f32,
    pub color: [f32; 4],
}

pub fn build(view: &View, cfg: &Config, viewport: Viewport) -> Scene {
    let xf = Transform::new(
        cfg.hud.reference_width.max(1) as f32,
        cfg.hud.reference_height.max(1) as f32,
        viewport,
    );
    let hud_a = cfg.hud.opacity.clamp(0.0, 1.0);
    let mut scene = Scene::default();
    let hud_color = parse_color(&cfg.hud.text_color, hud_a);

    if !view.status.is_empty() {
        push_text_group(
            &mut scene,
            xf,
            cfg.hud.margin_left + cfg.widget.status.x,
            cfg.hud.margin_top + cfg.widget.status.y,
            font_px(cfg, cfg.widget.status.font_size, xf),
            view.status_color.unwrap_or(hud_color),
            &[view.status.as_str()],
        );
    }

    if cfg.widget.session.enabled && !view.clock.is_empty() {
        let line = format!("{}{}", cfg.widget.session.prefix, view.clock);
        push_text_group(
            &mut scene,
            xf,
            cfg.hud.margin_left + cfg.widget.session.x,
            cfg.hud.margin_top + cfg.widget.session.y,
            font_px(cfg, cfg.widget.session.font_size, xf),
            widget_color(cfg, &cfg.widget.session.color, hud_a),
            &[&line],
        );
    }

    if cfg.widget.xp.enabled && !view.xp.is_empty() {
        let line = format!("{}{}", cfg.widget.xp.prefix, view.xp);
        push_text_group(
            &mut scene,
            xf,
            cfg.hud.margin_left + cfg.widget.xp.x,
            cfg.hud.margin_top + cfg.widget.xp.y,
            font_px(cfg, cfg.widget.xp.font_size, xf),
            view.xp_color
                .unwrap_or(widget_color(cfg, &cfg.widget.xp.color, hud_a)),
            &[&line],
        );
    }

    if cfg.widget.block.enabled && !view.block.is_empty() {
        let mut rows: Vec<String> = vec![view.block.clone()];
        if !view.block_sub.is_empty() {
            rows.push(view.block_sub.clone());
        }
        let refs: Vec<&str> = rows.iter().map(String::as_str).collect();
        push_text_group(
            &mut scene,
            xf,
            cfg.hud.margin_left + cfg.widget.block.x,
            cfg.hud.margin_top + cfg.widget.block.y,
            font_px(cfg, cfg.widget.block.font_size, xf),
            widget_color(cfg, &cfg.widget.block.color, hud_a),
            &refs,
        );
    }

    if cfg.widget.challenges.enabled && !view.challenges.is_empty() {
        push_lines(
            &mut scene,
            xf,
            LineLayout {
                at: [
                    cfg.hud.margin_left + cfg.widget.challenges.x,
                    cfg.hud.margin_top + cfg.widget.challenges.y,
                ],
                font_px: font_px(cfg, cfg.widget.challenges.font_size, xf),
                default_color: widget_color(cfg, &cfg.widget.challenges.color, hud_a),
                hud_a,
            },
            &view.challenges,
        );
    }

    if cfg.widget.bosses.enabled && !view.bosses.is_empty() {
        let color = widget_color(cfg, &cfg.widget.bosses.color, hud_a);
        push_lines(
            &mut scene,
            xf,
            LineLayout {
                at: [
                    cfg.hud.margin_left + cfg.widget.bosses.x,
                    cfg.hud.margin_top + cfg.widget.bosses.y,
                ],
                font_px: font_px(cfg, cfg.widget.bosses.font_size, xf),
                default_color: color,
                hud_a: color[3],
            },
            &view.bosses,
        );
    }

    if cfg.widget.map.enabled && !view.map.cells.is_empty() {
        push_map(&mut scene, view, cfg, xf, hud_a);
    }

    scene
}

fn widget_color(cfg: &Config, color: &str, hud_a: f32) -> [f32; 4] {
    if color.trim().is_empty() {
        parse_color(&cfg.hud.text_color, hud_a)
    } else {
        parse_color(color, hud_a)
    }
}

fn font_px(cfg: &Config, group_pt: f32, xf: Transform) -> f32 {
    let pt = if group_pt > 0.0 {
        group_pt
    } else {
        cfg.hud.font_size
    };
    xf.size(pt.max(1.0) * PT_TO_PX)
}

fn push_text_group(
    scene: &mut Scene,
    xf: Transform,
    ax: i32,
    ay: i32,
    font_px: f32,
    color: [f32; 4],
    rows: &[&str],
) {
    let lines: Vec<Line> = rows
        .iter()
        .map(|r| Line {
            text: (*r).to_string(),
            ..Line::default()
        })
        .collect();
    push_lines(
        scene,
        xf,
        LineLayout {
            at: [ax, ay],
            font_px,
            default_color: color,
            hud_a: color[3],
        },
        &lines,
    );
}

#[derive(Clone, Copy)]
struct LineLayout {
    at: [i32; 2],
    font_px: f32,
    default_color: [f32; 4],
    hud_a: f32,
}

fn push_lines(scene: &mut Scene, xf: Transform, layout: LineLayout, rows: &[Line]) {
    let LineLayout {
        at: [ax, ay],
        font_px,
        default_color,
        hud_a,
    } = layout;
    let (x, mut y) = xf.point(ax as f32, ay as f32);
    let label_col = rows.iter().any(|r| !r.label.is_empty());
    let label_w = if label_col {
        mono_advance("MMMMM", font_px)
    } else {
        0.0
    };
    let label_gap = if label_col { xf.size(6.0) } else { 0.0 };
    let content_x = x + label_w + label_gap;
    let mut label_color = [
        0x8a as f32 / 255.0,
        0x8a as f32 / 255.0,
        0x8a as f32 / 255.0,
        1.0,
    ];
    label_color[3] *= hud_a;

    let timer_gap = 2.0 * mono_advance(" ", font_px);
    let group_w = rows
        .iter()
        .map(|row| {
            let left = if label_col { label_w + label_gap } else { 0.0 };
            let content = mono_advance(&row.text, font_px);
            let timer = if row.timer.is_empty() {
                0.0
            } else {
                timer_gap + mono_advance(&row.timer, font_px)
            };
            left + content + timer
        })
        .fold(0.0_f32, f32::max);

    for row in rows {
        y += xf.size(row.extra_ascent_px);
        let mut color = row.color.unwrap_or(default_color);
        color[3] *= hud_a;
        if label_col && !row.label.is_empty() {
            scene.texts.push(Text {
                x,
                y,
                color: label_color,
                text: row.label.clone(),
                font_px,
                outline: true,
                outline_color: None,
                lcd: true,
                center_h: None,
            });
        }
        let text_x = if label_col { content_x } else { x };
        let content_w = if row.runs.is_empty() {
            scene.texts.push(Text {
                x: text_x,
                y,
                color,
                text: row.text.clone(),
                font_px,
                outline: true,
                outline_color: None,
                lcd: true,
                center_h: None,
            });
            mono_advance(&row.text, font_px)
        } else {
            let mut run_x = text_x;
            for run in &row.runs {
                let mut run_color = color;
                run_color[3] *= run.alpha;
                scene.texts.push(Text {
                    x: run_x,
                    y,
                    color: run_color,
                    text: run.text.clone(),
                    font_px,
                    outline: true,
                    outline_color: None,
                    lcd: true,
                    center_h: None,
                });
                run_x += mono_advance(&run.text, font_px);
            }
            run_x - text_x
        };
        if !row.timer.is_empty() {
            let tw = mono_advance(&row.timer, font_px);
            scene.texts.push(Text {
                x: x + group_w - tw,
                y,
                color: [1.0, 1.0, 1.0, hud_a],
                text: row.timer.clone(),
                font_px,
                outline: true,
                outline_color: None,
                lcd: true,
                center_h: None,
            });
        }
        if row.strike {
            let mut strike_color = color;
            if let Some(run) = row.runs.first() {
                strike_color[3] *= run.alpha;
            }
            scene.strokes.push(Stroke {
                x0: text_x,
                y0: y + font_px * 0.55,
                x1: text_x + content_w,
                y1: y + font_px * 0.55,
                width: (font_px * 0.08).max(1.0),
                color: strike_color,
            });
        }
        y += font_px * LINE_HEIGHT;
    }
}

fn map_cell_px(cfg: &Config) -> i32 {
    let scale = if cfg.widget.map.scale <= 0.0 {
        1.0
    } else {
        cfg.widget.map.scale
    };
    let side = if cfg.widget.map.radius <= 0 {
        24
    } else {
        2 * cfg.widget.map.radius + 1
    };
    let px = (scale * MAP_BASE_SIZE) as i32 / side;
    px.clamp(MAP_MIN_CELL, MAP_MAX_CELL)
}

fn map_window(view: &MapView, radius: i32) -> (i32, i32, i32, i32) {
    if view.cells.is_empty() {
        return (0, 0, 1, 1);
    }
    let min_x = view.cells.iter().map(|c| c.x).min().unwrap_or(0);
    let max_x = view.cells.iter().map(|c| c.x).max().unwrap_or(0);
    let min_y = view.cells.iter().map(|c| c.y).min().unwrap_or(0);
    let max_y = view.cells.iter().map(|c| c.y).max().unwrap_or(0);
    let city_w = max_x - min_x + 1;
    let city_h = max_y - min_y + 1;
    if radius <= 0 {
        return (min_x, min_y, city_w, city_h);
    }
    let w = 2 * radius + 1;
    let h = w;
    let mut x = view.player_x - radius;
    let mut y = view.player_y - radius;
    x = x.max(min_x).min((max_x - w + 1).max(min_x));
    y = y.max(min_y).min((max_y - h + 1).max(min_y));
    (x, y, w, h)
}

fn push_map(scene: &mut Scene, view: &View, cfg: &Config, xf: Transform, hud_a: f32) {
    let cell_auth = map_cell_px(cfg) as f32;
    let (win_x, win_y, win_w, win_h) = map_window(&view.map, cfg.widget.map.radius);
    let map_w_auth = win_w as f32 * cell_auth;
    let map_h_auth = win_h as f32 * cell_auth;

    let (ax, ay) = if cfg.widget.map.center {
        let usable_w =
            (cfg.hud.reference_width - cfg.hud.margin_left - cfg.hud.margin_right) as f32;
        let usable_h =
            (cfg.hud.reference_height - cfg.hud.margin_top - cfg.hud.margin_bottom) as f32;
        (
            cfg.hud.margin_left as f32
                + (usable_w - map_w_auth) / 2.0
                + cfg.widget.map.offset_x as f32,
            cfg.hud.margin_top as f32
                + (usable_h - map_h_auth) / 2.0
                + cfg.widget.map.offset_y as f32,
        )
    } else {
        (
            (cfg.hud.margin_left + cfg.widget.map.x) as f32,
            (cfg.hud.margin_top + cfg.widget.map.y) as f32,
        )
    };
    let (ox, oy) = xf.point(ax, ay);
    let cell = xf.size(cell_auth);
    let map_a = (cfg.widget.map.opacity.clamp(0.0, 1.0) * hud_a).clamp(0.0, 1.0);

    let lookup: HashMap<(i32, i32), [f32; 4]> = view
        .map
        .cells
        .iter()
        .map(|c| ((c.x, c.y), c.fill))
        .collect();

    for gy in 0..win_h {
        for gx in 0..win_w {
            let bx = win_x + gx;
            let by = win_y + gy;
            let Some(fill) = lookup.get(&(bx, by)) else {
                continue;
            };
            let x = ox + gx as f32 * cell;
            let y = oy + gy as f32 * cell;
            scene.fills.push(Fill {
                x,
                y,
                w: cell,
                h: cell,
                color: [fill[0], fill[1], fill[2], fill[3] * 0.7 * map_a],
            });
            let border = [0.0, 0.0, 0.0, 0.45 * map_a];
            let bw = xf.size(1.0).max(1.0);
            stroke_rect(scene, x, y, cell, cell, bw, border);
        }
    }

    let div_color = [1.0, 1.0, 1.0, 0.35 * hud_a];
    let div_w = xf.size(1.0).max(1.0);
    for &dx in &view.map.dividers_x {
        if dx <= win_x || dx >= win_x + win_w {
            continue;
        }
        let x = ox + (dx - win_x) as f32 * cell;
        scene.strokes.push(Stroke {
            x0: x,
            y0: oy,
            x1: x,
            y1: oy + win_h as f32 * cell,
            width: div_w,
            color: div_color,
        });
    }
    for &dy in &view.map.dividers_y {
        if dy <= win_y || dy >= win_y + win_h {
            continue;
        }
        let y = oy + (dy - win_y) as f32 * cell;
        scene.strokes.push(Stroke {
            x0: ox,
            y0: y,
            x1: ox + win_w as f32 * cell,
            y1: y,
            width: div_w,
            color: div_color,
        });
    }

    let ring_w = xf.size(2.0).max(1.0);
    for marker in &view.map.markers {
        if marker.x < win_x
            || marker.x >= win_x + win_w
            || marker.y < win_y
            || marker.y >= win_y + win_h
        {
            continue;
        }
        let cx = ox + (marker.x - win_x) as f32 * cell;
        let cy = oy + (marker.y - win_y) as f32 * cell;
        let on_player = marker.x == view.map.player_x && marker.y == view.map.player_y;
        if marker.ring && !on_player {
            let ring = [
                marker.color[0],
                marker.color[1],
                marker.color[2],
                marker.color[3] * hud_a,
            ];
            stroke_rect(scene, cx, cy, cell, cell, ring_w, ring);
        }
        let (marker_px, tw) = fit_marker_font(&marker.text, cell);
        scene.labels.push(Text {
            x: cx + (cell - tw) * 0.5,
            y: cy,
            color: [
                marker.ink[0],
                marker.ink[1],
                marker.ink[2],
                marker.ink[3] * hud_a,
            ],
            text: marker.text.clone(),
            font_px: marker_px,
            outline: true,
            outline_color: None,
            lcd: true,
            center_h: Some(cell),
        });
    }

    if view.map.player_x >= win_x
        && view.map.player_x < win_x + win_w
        && view.map.player_y >= win_y
        && view.map.player_y < win_y + win_h
    {
        let x = ox + (view.map.player_x - win_x) as f32 * cell;
        let y = oy + (view.map.player_y - win_y) as f32 * cell;
        stroke_rect(scene, x, y, cell, cell, ring_w, [1.0, 1.0, 1.0, hud_a]);
    }

    if cfg.widget.map.show_list && !view.map.list.is_empty() {
        let list_pt = if cfg.widget.map.font_size > 0.0 {
            cfg.widget.map.font_size
        } else {
            (0.65 * cell_auth).mul_add(10.0, 0.0).round() / 10.0
        };
        let list_pt = list_pt.clamp(8.0, 30.0);
        let list_px = xf.size(list_pt * PT_TO_PX);
        let lx = ox + xf.size(map_w_auth + MAP_LIST_GAP);
        let mut ly = oy;
        let color = parse_color(&cfg.widget.map.color, hud_a);
        const CHIP_INK: [f32; 4] = [16.0 / 255.0, 16.0 / 255.0, 16.0 / 255.0, 1.0];
        let pad_x = xf.size(3.0).max(2.0);
        let pad_y = xf.size(2.0).max(1.0);
        // Two-character chip (I5 / B4). Δ and ▼ sit in the same box so columns line up.
        let chip_w = mono_advance("MM", list_px) + pad_x * 2.0;
        let timer_col = view
            .map
            .list
            .iter()
            .filter(|r| r.chip.is_some())
            .map(|r| mono_advance(&r.timer, list_px))
            .fold(0.0_f32, f32::max);
        let timer_x = lx + chip_w + xf.size(4.0);
        let name_x = timer_x + timer_col + 2.0 * mono_advance(" ", list_px);
        for row in &view.map.list {
            if let Some(chip) = row.chip {
                let glyph = row.text.split("  ").next().unwrap_or("");
                let gh = list_px + pad_y * 2.0;
                scene.fills.push(Fill {
                    x: lx,
                    y: ly - pad_y,
                    w: chip_w,
                    h: gh,
                    color: [chip[0], chip[1], chip[2], chip[3] * hud_a],
                });
                if row.chip_ring {
                    stroke_rect(
                        scene,
                        lx,
                        ly - pad_y,
                        chip_w,
                        gh,
                        xf.size(1.5).max(1.0),
                        [chip[0], chip[1], chip[2], chip[3] * hud_a],
                    );
                }
                let glyph_w = mono_advance(glyph, list_px);
                scene.labels.push(Text {
                    x: lx + (chip_w - glyph_w) * 0.5,
                    y: ly - pad_y,
                    color: [CHIP_INK[0], CHIP_INK[1], CHIP_INK[2], hud_a],
                    text: glyph.to_string(),
                    font_px: list_px,
                    outline: false,
                    outline_color: None,
                    lcd: false,
                    center_h: Some(gh),
                });
                let name = row
                    .text
                    .get(glyph.len()..)
                    .unwrap_or("")
                    .trim_start()
                    .to_string();
                if !row.timer.is_empty() {
                    // Timer at 78% of the list colour so the subject stays the bright run.
                    let dim = [color[0] * 0.78, color[1] * 0.78, color[2] * 0.78, color[3]];
                    scene.labels.push(Text {
                        x: timer_x,
                        y: ly - pad_y,
                        color: dim,
                        text: row.timer.clone(),
                        font_px: list_px,
                        outline: true,
                        outline_color: None,
                        lcd: true,
                        center_h: Some(gh),
                    });
                }
                scene.labels.push(Text {
                    x: name_x,
                    y: ly - pad_y,
                    color,
                    text: name,
                    font_px: list_px,
                    outline: true,
                    outline_color: None,
                    lcd: true,
                    center_h: Some(gh),
                });
            } else {
                let nested = row.text.starts_with("        ");
                scene.labels.push(Text {
                    x: if nested { name_x } else { lx },
                    y: ly,
                    color,
                    text: row.text.trim_start().to_string(),
                    font_px: list_px,
                    outline: true,
                    outline_color: None,
                    lcd: true,
                    center_h: Some(list_px),
                });
            }
            ly += list_px * LINE_HEIGHT;
        }
    }
}

fn mono_advance(text: &str, px: f32) -> f32 {
    text.chars().count() as f32 * px * 0.6
}

fn fit_marker_font(text: &str, cell: f32) -> (f32, f32) {
    let base = cell * MAP_MARKER_FONT;
    let raw = mono_advance(text, base);
    let limit = cell * 0.9;
    let px = if raw > limit && raw > 0.0 {
        base * (limit / raw)
    } else {
        base.min(limit)
    };
    (px, mono_advance(text, px))
}

fn stroke_rect(scene: &mut Scene, x: f32, y: f32, w: f32, h: f32, width: f32, color: [f32; 4]) {
    scene.strokes.push(Stroke {
        x0: x,
        y0: y,
        x1: x + w,
        y1: y,
        width,
        color,
    });
    scene.strokes.push(Stroke {
        x0: x + w,
        y0: y,
        x1: x + w,
        y1: y + h,
        width,
        color,
    });
    scene.strokes.push(Stroke {
        x0: x + w,
        y0: y + h,
        x1: x,
        y1: y + h,
        width,
        color,
    });
    scene.strokes.push(Stroke {
        x0: x,
        y0: y + h,
        x1: x,
        y1: y,
        width,
        color,
    });
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::Config;
    use crate::overlay::dummy;

    fn dummy_view() -> View {
        dummy::view("12:00:00")
    }

    fn vp_1440() -> Viewport {
        Viewport {
            width: 2560.0,
            height: 1440.0,
            game_width: 0.0,
            game_height: 0.0,
        }
    }

    #[test]
    fn block_x_moves_the_block_group() {
        let view = dummy_view();
        let mut cfg = Config::default();
        let a = build(&view, &cfg, vp_1440());
        cfg.widget.block.x = 1800;
        let b = build(&view, &cfg, vp_1440());
        let ax = a
            .texts
            .iter()
            .find(|t| t.text.contains("Holdout"))
            .unwrap()
            .x;
        let bx = b
            .texts
            .iter()
            .find(|t| t.text.contains("Holdout"))
            .unwrap()
            .x;
        assert!((ax - 2340.0).abs() < 0.5, "default block x = {ax}");
        assert!((bx - 1800.0).abs() < 0.5, "moved block x = {bx}");
        assert!((bx - ax).abs() > 100.0);
    }

    #[test]
    fn map_emits_cells_and_player_ring() {
        let view = dummy_view();
        let cfg = Config::default();
        let scene = build(&view, &cfg, vp_1440());
        assert!(!scene.fills.is_empty(), "map cells");
        assert!(
            scene
                .strokes
                .iter()
                .any(|s| s.color[0] > 0.9 && s.width >= 1.9),
            "player ring"
        );
        assert!(
            scene.labels.iter().any(|t| t.text.contains("Holdout")),
            "map list"
        );
    }

    #[test]
    fn linux_and_windows_share_layout_at_same_viewport() {
        let view = dummy_view();
        let cfg = Config::default();
        let a = build(&view, &cfg, vp_1440());
        let b = build(&view, &cfg, vp_1440());
        assert_eq!(a.texts.len(), b.texts.len());
        assert!((a.texts[0].x - b.texts[0].x).abs() < f32::EPSILON);
    }

    #[test]
    fn map_list_chip_and_ringed_cell() {
        let mut view = dummy_view();
        view.map.list[0].chip = Some([1.0, 0.54, 0.0, 1.0]);
        view.map.list[0].text = "DH  Devil Hound".into();
        view.map.list[0].timer = "12m46s".into();
        view.map.markers.push(MapMarker {
            x: 1009,
            y: 1008,
            text: "DH".into(),
            ink: [1.0, 1.0, 1.0, 1.0],
            color: [1.0, 0.54, 0.0, 1.0],
            ring: true,
        });
        let scene = build(&view, &Config::default(), vp_1440());
        assert!(
            scene
                .fills
                .iter()
                .any(|f| (f.color[0] - 1.0).abs() < 0.01 && f.color[1] > 0.4),
            "chip fill"
        );
        assert!(
            scene.labels.iter().any(|t| {
                t.text == "DH" && !t.outline && !t.lcd && t.center_h.is_some() && t.color[0] < 0.1
            }),
            "chip letter is hinted grayscale ink, no outline"
        );
        assert!(scene.labels.iter().any(|t| t.text.contains("Devil")));
        let timer = scene
            .labels
            .iter()
            .find(|t| t.text == "12m46s")
            .expect("timer");
        let name = scene
            .labels
            .iter()
            .find(|t| t.text.contains("Devil"))
            .expect("name");
        assert!(
            timer.color[0] < name.color[0] && (timer.color[0] - name.color[0] * 0.78).abs() < 0.02,
            "timer {:?} vs name {:?}",
            timer.color,
            name.color
        );
        assert!(
            scene
                .strokes
                .iter()
                .any(|s| (s.color[0] - 1.0).abs() < 0.01 && s.color[1] > 0.4 && s.width >= 1.9),
            "daily/mission/QRF ring"
        );
        assert!(
            scene
                .labels
                .iter()
                .any(|t| t.text == "DH" && t.outline && t.color[0] > 0.9 && t.color[1] > 0.9),
            "map letter stays white; colour lives on the ring and chip"
        );
    }

    #[test]
    fn boss_and_bandit_map_letters_are_white() {
        let mut view = dummy_view();
        view.map.markers.push(MapMarker {
            x: 1010,
            y: 1008,
            text: "I4".into(),
            ink: [1.0, 1.0, 1.0, 1.0],
            color: [1.0, 0.33, 0.33, 1.0],
            ring: false,
        });
        view.map.markers.push(MapMarker {
            x: 1011,
            y: 1008,
            text: "B4".into(),
            ink: [1.0, 1.0, 1.0, 1.0],
            color: [1.0, 0.82, 0.4, 1.0],
            ring: false,
        });
        let scene = build(&view, &Config::default(), vp_1440());
        for id in ["I4", "B4"] {
            let letter = scene
                .labels
                .iter()
                .find(|t| t.text == id && t.outline)
                .unwrap_or_else(|| panic!("{id}"));
            assert!(
                letter.color[0] > 0.9 && letter.color[1] > 0.9 && letter.color[2] > 0.9,
                "{id} letter {:?}",
                letter.color
            );
        }
        assert!(
            !scene
                .strokes
                .iter()
                .any(|s| s.width >= 1.9 && (s.color[0] - 1.0).abs() < 0.01 && s.color[1] < 0.5),
            "no red/amber cell ring for I/B"
        );
    }

    #[test]
    fn player_ring_overwrites_event_ring_on_the_standing_cell() {
        let mut view = dummy_view();
        view.map.markers.push(MapMarker {
            x: 1008,
            y: 1008,
            text: "Δ1".into(),
            ink: [1.0, 1.0, 1.0, 1.0],
            color: [0.36, 0.9, 0.36, 1.0],
            ring: true,
        });
        let scene = build(&view, &Config::default(), vp_1440());
        let thick: Vec<_> = scene.strokes.iter().filter(|s| s.width >= 1.9).collect();
        assert!(
            thick
                .iter()
                .any(|s| s.color[0] > 0.9 && s.color[1] > 0.9 && s.color[2] > 0.9),
            "white standing ring"
        );
        assert!(
            thick.iter().all(|s| s.color[1] < 0.85 || s.color[0] > 0.9),
            "QRF green must not stay on the cell you are standing on"
        );
    }

    #[test]
    fn two_character_markers_fit_inside_the_cell() {
        let mut view = dummy_view();
        view.map.markers.push(MapMarker {
            x: 1008,
            y: 1008,
            text: "Δ2".into(),
            ink: [1.0, 1.0, 1.0, 1.0],
            color: [0.36, 0.9, 0.36, 1.0],
            ring: false,
        });
        let scene = build(&view, &Config::default(), vp_1440());
        let label = scene.labels.iter().find(|t| t.text == "Δ2").expect("Δ2");
        let cell = scene
            .fills
            .iter()
            .find(|f| f.w > 8.0 && f.h > 8.0)
            .expect("cell")
            .w;
        assert!(
            label.font_px <= cell * 0.9 + 0.05,
            "font {} vs cell {}",
            label.font_px,
            cell
        );
        let width = label.text.chars().count() as f32 * label.font_px * 0.6;
        assert!(width <= cell * 0.9 + 0.05, "advance {width} vs cell {cell}");
        assert_eq!(label.center_h, Some(cell));
    }

    #[test]
    fn map_list_columns_line_up_for_one_and_two_char_chips() {
        let mut view = dummy_view();
        view.map.list = vec![
            Line {
                text: "Δ  QRF Death Row".into(),
                chip: Some([0.36, 0.9, 0.36, 1.0]),
                timer: "55m29s".into(),
                ..Line::default()
            },
            Line {
                text: "I5  4 x Flaming Rumblers".into(),
                chip: Some([0.33, 0.66, 1.0, 1.0]),
                timer: "1h55m".into(),
                ..Line::default()
            },
            Line {
                text: "        1 x Flaming Mother".into(),
                ..Line::default()
            },
        ];
        let scene = build(&view, &Config::default(), vp_1440());
        let t0 = scene
            .labels
            .iter()
            .find(|t| t.text == "55m29s")
            .expect("long timer");
        let t1 = scene
            .labels
            .iter()
            .find(|t| t.text == "1h55m")
            .expect("short timer");
        assert!((t0.x - t1.x).abs() < 0.05, "timers {} vs {}", t0.x, t1.x);
        let n0 = scene
            .labels
            .iter()
            .find(|t| t.text.contains("Death Row"))
            .expect("qrf");
        let n1 = scene
            .labels
            .iter()
            .find(|t| t.text.contains("Rumblers"))
            .expect("boss");
        let nest = scene
            .labels
            .iter()
            .find(|t| t.text.contains("Mother"))
            .expect("nest");
        assert!((n0.x - n1.x).abs() < 0.05, "names {} vs {}", n0.x, n1.x);
        assert!(
            (nest.x - n0.x).abs() < 0.05,
            "nest continuation {} vs name {}",
            nest.x,
            n0.x
        );
        let green = scene
            .fills
            .iter()
            .find(|f| (f.color[1] - 0.9).abs() < 0.02)
            .expect("Δ chip");
        let blue = scene
            .fills
            .iter()
            .find(|f| (f.color[2] - 1.0).abs() < 0.02 && f.color[0] < 0.4)
            .expect("I5 chip");
        assert!(
            (green.w - blue.w).abs() < 0.05,
            "chip widths {} vs {}",
            green.w,
            blue.w
        );
    }

    #[test]
    fn completed_challenge_is_struck_through() {
        let mut view = dummy_view();
        view.challenges = vec![Line {
            text: "Summer Loot  10/10".into(),
            color: Some([0.61, 0.9, 0.39, 1.0]),
            strike: true,
            ..Line::default()
        }];
        let scene = build(&view, &Config::default(), vp_1440());
        assert!(
            scene
                .strokes
                .iter()
                .any(|s| (s.y0 - s.y1).abs() < 0.01 && s.x1 > s.x0 + 8.0),
            "strikethrough"
        );
    }

    #[test]
    fn challenge_runs_draw_at_nested_alphas_and_strike_the_full_row() {
        let mut view = dummy_view();
        view.status.clear();
        view.clock.clear();
        view.xp.clear();
        view.block.clear();
        view.map = MapView::default();
        view.challenges = vec![Line {
            text: "  Loot Anything  10/10".into(),
            color: Some([0.61, 0.9, 0.39, 1.0]),
            strike: true,
            runs: vec![
                TextRun {
                    text: "  ".into(),
                    alpha: 0.60,
                },
                TextRun {
                    text: "Loot Anything".into(),
                    alpha: 0.60 * 0.78,
                },
                TextRun {
                    text: "  ".into(),
                    alpha: 0.60,
                },
                TextRun {
                    text: "10/10".into(),
                    alpha: 0.60,
                },
            ],
            ..Line::default()
        }];
        let mut cfg = Config::default();
        cfg.hud.opacity = 0.8;
        cfg.widget.challenges.enabled = true;
        let scene = build(&view, &cfg, vp_1440());

        let objective = scene
            .texts
            .iter()
            .find(|t| t.text == "Loot Anything")
            .expect("objective run");
        let want = 0.8 * 0.60 * 0.78;
        assert!(
            (objective.color[3] - want).abs() < 1e-4,
            "objective alpha {} want nested hud/done/objective {want}",
            objective.color[3]
        );

        let name_pad = scene
            .texts
            .iter()
            .find(|t| t.text == "  " && (t.color[3] - 0.8 * 0.60).abs() < 1e-4)
            .expect("name indent");
        let progress = scene
            .texts
            .iter()
            .find(|t| t.text == "10/10")
            .expect("progress");
        let font_px = objective.font_px;
        let want_w = mono_advance("  ", font_px)
            + mono_advance("Loot Anything", font_px)
            + mono_advance("  ", font_px)
            + mono_advance("10/10", font_px);
        let strike = scene
            .strokes
            .iter()
            .find(|s| (s.y0 - s.y1).abs() < 0.01 && s.x1 > s.x0 + 8.0)
            .expect("strikethrough");
        assert!(
            ((strike.x1 - strike.x0) - want_w).abs() < 0.05,
            "strike {} want {want_w}",
            strike.x1 - strike.x0
        );
        assert!((strike.x0 - name_pad.x).abs() < 0.05);
        assert!((progress.x + mono_advance("10/10", font_px) - strike.x1).abs() < 0.05);
        assert!((strike.color[3] - 0.8 * 0.60).abs() < 1e-4);
    }

    #[test]
    fn onslaught_rows_carry_label_column_and_header_timer() {
        let mut view = dummy_view();
        view.map = MapView::default();
        view.block.clear();
        view.clock.clear();
        view.xp.clear();
        view.status.clear();
        view.challenges.clear();
        let prev = [
            0xb5 as f32 / 255.0,
            0xb5 as f32 / 255.0,
            0xb5 as f32 / 255.0,
            1.0,
        ];
        let now = [
            0xff as f32 / 255.0,
            0x4d as f32 / 255.0,
            0x4d as f32 / 255.0,
            1.0,
        ];
        let next = [
            0x4f as f32 / 255.0,
            0xc3 as f32 / 255.0,
            0xff as f32 / 255.0,
            1.0,
        ];
        view.bosses = vec![
            Line {
                text: "Onslaught Cycles".into(),
                timer: "1:30".into(),
                color: Some([1.0, 1.0, 1.0, 1.0]),
                ..Line::default()
            },
            Line {
                label: "prev".into(),
                text: "Wraith".into(),
                color: Some(prev),
                ..Line::default()
            },
            Line {
                label: "now".into(),
                text: "Titan".into(),
                color: Some(now),
                ..Line::default()
            },
            Line {
                label: "next".into(),
                text: "Mother".into(),
                color: Some(next),
                ..Line::default()
            },
        ];
        let mut cfg = Config::default();
        cfg.widget.session.enabled = false;
        cfg.widget.xp.enabled = false;
        cfg.widget.block.enabled = false;
        cfg.widget.map.enabled = false;
        cfg.widget.challenges.enabled = false;
        let scene = build(&view, &cfg, vp_1440());
        let title = scene
            .texts
            .iter()
            .find(|t| t.text == "Onslaught Cycles")
            .expect("title");
        let timer = scene
            .texts
            .iter()
            .find(|t| t.text == "1:30")
            .expect("timer");
        assert!(timer.x > title.x, "timer {} vs title {}", timer.x, title.x);
        let label = scene
            .texts
            .iter()
            .find(|t| t.text == "prev")
            .expect("label");
        let content = scene
            .texts
            .iter()
            .find(|t| t.text == "Wraith")
            .expect("content");
        assert!(content.x > label.x);
        assert!((label.color[0] - 0x8a as f32 / 255.0).abs() < 0.02);
        let now_row = scene.texts.iter().find(|t| t.text == "Titan").expect("now");
        assert!((now_row.color[0] - now[0]).abs() < 0.02);
        let next_row = scene
            .texts
            .iter()
            .find(|t| t.text == "Mother")
            .expect("next");
        assert!((next_row.color[2] - next[2]).abs() < 0.02);
        assert!(
            (timer.color[0] - 1.0).abs() < 0.02
                && (timer.color[1] - 1.0).abs() < 0.02
                && (timer.color[2] - 1.0).abs() < 0.02,
            "onslaught header timer {:?}",
            timer.color
        );
    }

    #[test]
    fn block_boss_timer_is_white_against_amber_names() {
        let mut view = dummy_view();
        view.map = MapView::default();
        view.block.clear();
        view.clock.clear();
        view.xp.clear();
        view.status.clear();
        view.challenges.clear();
        view.bosses = vec![
            Line {
                text: "3 x Evolved Longarms".into(),
                timer: "55m".into(),
                ..Line::default()
            },
            Line {
                text: "1 x Irradiated Wraith".into(),
                ..Line::default()
            },
        ];
        let mut cfg = Config::default();
        cfg.widget.session.enabled = false;
        cfg.widget.xp.enabled = false;
        cfg.widget.block.enabled = false;
        cfg.widget.map.enabled = false;
        cfg.widget.challenges.enabled = false;
        let scene = build(&view, &cfg, vp_1440());
        let name = scene
            .texts
            .iter()
            .find(|t| t.text == "3 x Evolved Longarms")
            .expect("name");
        let timer = scene
            .texts
            .iter()
            .find(|t| t.text == "55m")
            .expect("timer");
        let rest = scene
            .texts
            .iter()
            .find(|t| t.text == "1 x Irradiated Wraith")
            .expect("rest");
        assert!(
            name.color[0] > 0.8 && name.color[1] > 0.7 && name.color[2] < 0.4,
            "name stays HUD amber {:?}",
            name.color
        );
        assert_eq!(name.color, rest.color);
        assert!(
            (timer.color[0] - 1.0).abs() < 0.02
                && (timer.color[1] - 1.0).abs() < 0.02
                && (timer.color[2] - 1.0).abs() < 0.02,
            "timer {:?}",
            timer.color
        );
    }
}
