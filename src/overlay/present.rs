//! model.View → `scene::View`.

use chrono::{DateTime, Utc};

use crate::app::groups::Groups;
use crate::config::{Config, MasteriesWidget};
use crate::data::bossmap::{self, MarkCategory};
use crate::data::citymap;
use crate::format;
use crate::model::{
    Challenge, CityEvent, CityEventKind, CityMark, Mastery, View as ModelView, XpStability,
};
use crate::overlay::layout::Viewport;
use crate::overlay::scene::{self, Line, MapCell, MapMarker, MapView, TextRun, View};

const BOARD_GAP_PX: f32 = 6.0;
const DONE_RGB: [f32; 4] = [
    0x9b as f32 / 255.0,
    0xe5 as f32 / 255.0,
    0x64 as f32 / 255.0,
    1.0,
];
const EXPIRING_RGB: [f32; 4] = [
    0xff as f32 / 255.0,
    0x6b as f32 / 255.0,
    0x6b as f32 / 255.0,
    1.0,
];
const SHAKY_RGB: [f32; 4] = [
    0xff as f32 / 255.0,
    0xd1 as f32 / 255.0,
    0x66 as f32 / 255.0,
    1.0,
];
const UNSTABLE_RGB: [f32; 4] = EXPIRING_RGB;
const DAILY_RGB: [f32; 4] = [
    0xff as f32 / 255.0,
    0x8a as f32 / 255.0,
    0x00 as f32 / 255.0,
    1.0,
];
const HEADING_ALPHA: f32 = 0.50;
const DONE_ALPHA: f32 = 0.60;
const OBJECTIVE_ALPHA: f32 = 0.78;
const COUNTDOWN_ALPHA: f32 = 0.70;

const NEAREST_RANGE: i32 = 12;
const XP_PENDING: &str = "--";
const XP_ROUGH: &str = "~";
const ONSLAUGHT: i32 = 3000;

pub fn overlay_scene(
    model: &ModelView,
    cfg: &Config,
    groups: &Groups,
    width: f32,
    height: f32,
) -> scene::Scene {
    overlay_scene_view(&from_view(model, cfg, groups), cfg, width, height)
}

#[cfg(target_os = "linux")]
pub fn empty_overlay_scene(cfg: &Config, width: f32, height: f32) -> scene::Scene {
    overlay_scene_view(&scene::View::default(), cfg, width, height)
}

fn overlay_scene_view(view: &View, cfg: &Config, width: f32, height: f32) -> scene::Scene {
    scene::build(view, cfg, Viewport { width, height })
}

pub fn hud_lines(v: &ModelView, cfg: &Config, groups: &Groups) -> Vec<String> {
    let scene = from_view(v, cfg, groups);
    let mut lines = Vec::new();
    if !scene.status.is_empty() {
        lines.push(scene.status);
    }
    let mut rows: Vec<(i32, i32, Vec<String>)> = Vec::new();
    if !scene.block.is_empty() {
        let mut text = vec![scene.block];
        if !scene.block_sub.is_empty() {
            text.push(scene.block_sub);
        }
        let [x, y] = cfg.hud.place(
            cfg.widget.block.anchor,
            cfg.widget.block.x,
            cfg.widget.block.y,
        );
        rows.push((y, x, text));
    }
    if !scene.bosses.is_empty() {
        let [x, y] = cfg.hud.place(
            cfg.widget.bosses.anchor,
            cfg.widget.bosses.x,
            cfg.widget.bosses.y,
        );
        rows.push((y, x, scene.bosses.iter().map(line_plain).collect()));
    }
    if !scene.clock.is_empty() {
        let [x, y] = cfg.hud.place(
            cfg.widget.session.anchor,
            cfg.widget.session.x,
            cfg.widget.session.y,
        );
        rows.push((
            y,
            x,
            vec![format!("{}{}", cfg.widget.session.prefix, scene.clock)],
        ));
    }
    if !scene.xp.is_empty() {
        let [x, y] = cfg
            .hud
            .place(cfg.widget.xp.anchor, cfg.widget.xp.x, cfg.widget.xp.y);
        rows.push((y, x, vec![format!("{}{}", cfg.widget.xp.prefix, scene.xp)]));
    }
    if !scene.challenges.is_empty() {
        let [x, y] = cfg.hud.place(
            cfg.widget.challenges.anchor,
            cfg.widget.challenges.x,
            cfg.widget.challenges.y,
        );
        rows.push((y, x, scene.challenges.into_iter().map(|l| l.text).collect()));
    }
    if !scene.masteries.is_empty() {
        let [x, y] = cfg.hud.place(
            cfg.widget.masteries.anchor,
            cfg.widget.masteries.x,
            cfg.widget.masteries.y,
        );
        rows.push((y, x, scene.masteries.into_iter().map(|l| l.text).collect()));
    }
    if !scene.keybinds.is_empty() {
        let [x, y] = cfg.hud.place(
            cfg.widget.keybinds.anchor,
            cfg.widget.keybinds.x,
            cfg.widget.keybinds.y,
        );
        rows.push((y, x, scene.keybinds));
    }
    rows.sort_by(|a, b| a.0.cmp(&b.0).then(a.1.cmp(&b.1)));
    for (_, _, text) in rows {
        lines.extend(text);
    }
    lines
}

pub fn format_hud(v: &ModelView, cfg: &Config, groups: &Groups) -> String {
    let lines = hud_lines(v, cfg, groups);
    if lines.is_empty() {
        "[hud empty]\n".into()
    } else {
        format!("{}\n---\n", lines.join("\n"))
    }
}

pub fn from_view(v: &ModelView, cfg: &Config, groups: &Groups) -> View {
    let mut out = View {
        status: if v.status.is_empty() {
            String::new()
        } else {
            v.status.clone()
        },
        status_color: if v.status.is_empty() {
            None
        } else if v.status_is_prompt {
            Some(SHAKY_RGB)
        } else {
            Some(EXPIRING_RGB)
        },
        ..View::default()
    };
    if !groups.hidden("session")
        && let Some(text) = session_line(v, cfg)
    {
        out.clock = text;
    }
    if !groups.hidden("xp")
        && let Some((text, color)) = xp_line(v, cfg)
    {
        out.xp = text;
        out.xp_color = color;
    }
    if !groups.hidden("block")
        && let Some((head, sub)) = block_lines(v, cfg)
    {
        out.block = head;
        out.block_sub = sub;
    }
    if !groups.hidden("challenges") {
        out.challenges = challenge_lines(v, cfg);
    }
    if !groups.hidden("masteries") {
        out.masteries = mastery_lines(v, cfg);
    }
    if !groups.hidden("bosses") {
        out.bosses = boss_lines(v, cfg);
    }
    if !groups.hidden("keybinds") && cfg.widget.keybinds.enabled {
        out.keybinds = keybind_lines(cfg);
    }
    if !groups.hidden("map") && cfg.widget.map.enabled && v.has_position && !in_onslaught(v) {
        out.map = map_view(v, cfg);
    }
    out
}

fn keybind_lines(cfg: &Config) -> Vec<String> {
    let slots = [
        (cfg.hotkeys.map.as_str(), "Minimap"),
        (cfg.hotkeys.challenges.as_str(), "Challenges"),
        (cfg.hotkeys.overlay.as_str(), "Overlay"),
        (cfg.hotkeys.run_start.as_str(), "Run clock"),
        (cfg.hotkeys.xp_reset.as_str(), "XP/hr"),
    ];
    let mut out = Vec::new();
    for (raw, label) in slots {
        if raw.trim().is_empty() {
            continue;
        }
        let Ok(binding) = crate::app::hotkeys::parse_binding(raw) else {
            continue;
        };
        out.push(format!("{} - {label}", binding.canonical()));
    }
    out
}

fn in_onslaught(v: &ModelView) -> bool {
    v.has_position && v.position_x == ONSLAUGHT && v.position_y == ONSLAUGHT
}

fn line_plain(l: &Line) -> String {
    let mut s = String::new();
    if !l.label.is_empty() {
        s.push_str(&l.label);
        s.push_str("  ");
    }
    s.push_str(&l.text);
    if !l.timer.is_empty() {
        s.push_str("  ");
        s.push_str(&l.timer);
    }
    s
}

fn session_line(v: &ModelView, cfg: &Config) -> Option<String> {
    if !cfg.widget.session.enabled || !v.game_running || !v.has_session {
        return None;
    }
    Some(format::clock(v.session_time.std()))
}

fn xp_line(v: &ModelView, cfg: &Config) -> Option<(String, Option<[f32; 4]>)> {
    if !cfg.widget.xp.enabled || !v.have_data {
        return None;
    }
    if !v.xp_available {
        return Some((XP_PENDING.into(), None));
    }
    let n = format::rate(v.xp_per_hour);
    if v.xp_provisional {
        // Neither dashes nor a provisional rate carries a stability colour.
        return Some((format!("{XP_ROUGH}{n}"), None));
    }
    let color = match v.xp_stability {
        XpStability::Shaky => Some(SHAKY_RGB),
        XpStability::Unstable => Some(UNSTABLE_RGB),
        XpStability::Steady => None,
    };
    Some((n, color))
}

fn block_lines(v: &ModelView, cfg: &Config) -> Option<(String, String)> {
    if !cfg.widget.block.enabled || !v.have_data || !v.has_position {
        return None;
    }
    let head = if v.in_outpost && !v.outpost_name.is_empty() {
        v.outpost_name.clone()
    } else if v.in_outpost {
        "Outpost".into()
    } else {
        v.zone_name.clone()
    };
    let mut parts = Vec::new();
    if cfg.widget.block.show_position {
        parts.push(format::position(v.position_x, v.position_y, v.position_z));
    }
    let support = format::countdown(v.block_support.std());
    if !support.is_empty() {
        parts.push(format!("support {support}"));
    }
    Some((head, parts.join("  ")))
}

fn boss_lines(v: &ModelView, cfg: &Config) -> Vec<Line> {
    if !cfg.widget.bosses.enabled || !v.have_data {
        return Vec::new();
    }
    let mut rows = Vec::new();
    if v.outpost_attack {
        rows.push(Line {
            text: "OUTPOST ATTACK".into(),
            color: Some(EXPIRING_RGB),
            ..Line::default()
        });
    }
    if let Some(panel) = onslaught_panel(v) {
        rows.extend(panel);
        return rows;
    }
    for e in v.block_events.iter().flatten() {
        // One countdown per event, on the first row. A nest shares one end time
        // across every type; repeating it would print the same number down the
        // group. A missing or already-passed end shows nothing, not a placeholder.
        let timer = event_time_left(e, v.now);
        for (i, text) in event_body_rows(e, "").into_iter().enumerate() {
            rows.push(Line {
                text,
                timer: if i == 0 { timer.clone() } else { String::new() },
                ..Line::default()
            });
        }
        for text in event_objective_rows(e, "") {
            rows.push(objective_line(text));
        }
    }
    if cfg.widget.bosses.show_nearest
        && let Some(text) = nearest_line(v)
    {
        rows.push(Line {
            text,
            ..Line::default()
        });
    }
    rows
}

fn onslaught_header_timer(v: &ModelView) -> Option<String> {
    if !v.have_data || !v.has_onslaught_countdown {
        return None;
    }
    Some(format::mmss(v.onslaught_countdown.std()))
}

fn onslaught_panel(v: &ModelView) -> Option<Vec<Line>> {
    if !v.have_data || v.position_x != ONSLAUGHT || v.position_y != ONSLAUGHT {
        return None;
    }
    let mut rows = Vec::new();
    rows.push(Line {
        text: "Onslaught Cycles".into(),
        timer: onslaught_header_timer(v).unwrap_or_default(),
        color: Some(rgb(0xff, 0xff, 0xff)),
        ..Line::default()
    });
    rows.extend(onslaught_section(
        "prev",
        v.block_events_past.as_deref().unwrap_or(&[]),
        rgb(0xb5, 0xb5, 0xb5),
        "cleared",
    ));
    if let Some(past) = v.block_events_past.as_deref().and_then(|e| e.first())
        && let Some(end) = onslaught_cycle_end(past)
        && v.now >= end
    {
        let since = v.now - end;
        let text = if since.num_minutes() >= 1 {
            format!("ended {}m ago", since.num_minutes())
        } else {
            "ended just now".into()
        };
        rows.push(Line {
            text,
            color: Some(rgb(0x6f, 0x6f, 0x6f)),
            ..Line::default()
        });
    }
    rows.extend(onslaught_section(
        "now",
        v.block_events.as_deref().unwrap_or(&[]),
        rgb(0xff, 0x4d, 0x4d),
        "nothing this cycle",
    ));
    rows.extend(onslaught_section(
        "next",
        v.block_events_upcoming.as_deref().unwrap_or(&[]),
        rgb(0x4f, 0xc3, 0xff),
        "not announced",
    ));
    Some(rows)
}

fn onslaught_cycle_end(e: &CityEvent) -> Option<DateTime<Utc>> {
    if e.end.timestamp() == 0 {
        return None;
    }
    if e.enemies.len() < 2 || e.start.timestamp() == 0 {
        return Some(e.end);
    }
    let cycle = e.end - e.start;
    if cycle <= chrono::Duration::zero() {
        return Some(e.end);
    }
    Some(e.end + cycle * (e.enemies.len() as i32 - 1))
}

fn onslaught_section(label: &str, events: &[CityEvent], color: [f32; 4], empty: &str) -> Vec<Line> {
    let mut texts = Vec::new();
    for e in events {
        texts.extend(onslaught_event_rows(e));
    }
    if texts.is_empty() {
        return vec![Line {
            label: label.into(),
            text: empty.into(),
            color: Some(rgb(0x6f, 0x6f, 0x6f)),
            ..Line::default()
        }];
    }
    texts
        .into_iter()
        .enumerate()
        .map(|(i, text)| Line {
            label: if i == 0 { label.into() } else { String::new() },
            text,
            color: Some(color),
            ..Line::default()
        })
        .collect()
}

fn onslaught_event_rows(e: &CityEvent) -> Vec<String> {
    let mut ev = e.clone();
    if ev.enemies.len() > 1 {
        ev.enemies = vec![ev.enemies[ev.enemies.len() - 1].clone()];
    }
    event_rows(&ev, "")
}

fn event_rows(e: &CityEvent, prefix: &str) -> Vec<String> {
    let mut rows = event_body_rows(e, prefix);
    rows.extend(event_objective_rows(e, prefix));
    rows
}

fn event_body_rows(e: &CityEvent, prefix: &str) -> Vec<String> {
    let mut rows = Vec::new();
    if e.kind != CityEventKind::Spawn || e.enemies.is_empty() {
        rows.push(format!("{prefix}{}", e.title));
    }
    for enemy in &e.enemies {
        rows.push(format!("{prefix}{enemy}"));
    }
    rows
}

fn event_objective_rows(e: &CityEvent, prefix: &str) -> Vec<String> {
    e.objectives
        .iter()
        .map(|o| format!("{prefix}{o}"))
        .collect()
}

fn objective_line(text: String) -> Line {
    Line {
        runs: vec![TextRun {
            text: text.clone(),
            alpha: OBJECTIVE_ALPHA,
        }],
        text,
        ..Line::default()
    }
}

fn event_time_left(e: &CityEvent, now: DateTime<Utc>) -> String {
    if e.end.timestamp() == 0 || now.timestamp() == 0 || e.end <= now {
        return String::new();
    }
    format::countdown((e.end - now).to_std().unwrap_or_default())
}

fn nearest_line(v: &ModelView) -> Option<String> {
    if !v.has_nearest || v.client_loading {
        return None;
    }
    if v.nearest_distance_in_blocks > NEAREST_RANGE {
        return None;
    }
    let mut parts = Vec::new();
    match v.nearest_dy.cmp(&0) {
        std::cmp::Ordering::Less => parts.push(format!("{} up", -v.nearest_dy)),
        std::cmp::Ordering::Greater => parts.push(format!("{} down", v.nearest_dy)),
        std::cmp::Ordering::Equal => {}
    }
    match v.nearest_dx.cmp(&0) {
        std::cmp::Ordering::Less => parts.push(format!("{} left", -v.nearest_dx)),
        std::cmp::Ordering::Greater => parts.push(format!("{} right", v.nearest_dx)),
        std::cmp::Ordering::Equal => {}
    }
    if parts.is_empty() {
        return None;
    }
    let mut line = format!("nearest {}", parts.join(" "));
    if v.nearest_detour > 0 {
        line.push_str(&format!(", {} blocks", v.nearest_distance_in_blocks));
    }
    line.push_str(&format!("  {}, {}", v.nearest_x, v.nearest_y));
    Some(line)
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Category {
    Repeatable,
    Clan,
    Personal,
}

fn category_of(c: &Challenge) -> Category {
    if c.clan {
        Category::Clan
    } else if c.repeatable {
        Category::Repeatable
    } else {
        Category::Personal
    }
}

fn show_challenge(c: &Challenge, cfg: &Config) -> bool {
    if c.complete() && !cfg.widget.challenges.show_completed {
        return false;
    }
    match category_of(c) {
        Category::Repeatable => cfg.widget.challenges.show_repeatable,
        Category::Clan => cfg.widget.challenges.show_clan,
        Category::Personal => cfg.widget.challenges.show_personal,
    }
}

#[derive(Clone, Default)]
struct ChallengeRow {
    name: String,
    objective: String,
    progress: String,
    countdown: String,
    done: bool,
    urgent: bool,
    sub: bool,
    heading: bool,
    pad: usize,
    rule: usize,
    gap: bool,
}

fn challenge_lines(v: &ModelView, cfg: &Config) -> Vec<Line> {
    if !cfg.widget.challenges.enabled {
        return Vec::new();
    }
    let board = v.challenges.as_deref().unwrap_or(&[]);
    let cap = if cfg.widget.challenges.max_shown > 0 {
        cfg.widget.challenges.max_shown as usize
    } else {
        usize::MAX
    };
    let shown: Vec<Challenge> = board
        .iter()
        .filter(|c| show_challenge(c, cfg))
        .take(cap)
        .cloned()
        .collect();
    if shown.is_empty() {
        if !v.challenge_status.is_empty() && board.is_empty() {
            return vec![Line {
                text: format!("challenges: {}", v.challenge_status),
                ..Line::default()
            }];
        }
        return Vec::new();
    }

    let sections = group_by_category(&shown);
    let headings = cfg.widget.challenges.show_sections && sections.len() > 1;
    let mut rows = Vec::new();
    for section in &sections {
        let mut prefix = String::new();
        if headings {
            prefix = shared_prefix(&section.challenges);
            let mut label = section.category.label().to_string();
            if !prefix.is_empty() {
                label.push_str(" - ");
                label.push_str(prefix.trim().trim_end_matches('-').trim());
            }
            rows.push(ChallengeRow {
                name: label.trim().to_string(),
                heading: true,
                ..ChallengeRow::default()
            });
        }
        for c in &section.challenges {
            let mut c = c.clone();
            if !prefix.is_empty() {
                c.name = c.name.replacen(&prefix, "", 1).trim().to_string();
            }
            rows.extend(challenge_rows(&c, v.now, cfg));
        }
    }

    let mut pad = 0usize;
    for r in &rows {
        if r.progress.is_empty() && r.countdown.is_empty() {
            continue;
        }
        pad = pad.max(r.label().chars().count());
    }
    let mut board_w = 0usize;
    for (i, r) in rows.iter_mut().enumerate() {
        r.pad = pad;
        r.gap = i > 0 && !r.sub;
        board_w = board_w.max(r.width());
    }
    for r in &mut rows {
        if r.heading {
            r.rule = board_w;
        }
    }
    let mut lines: Vec<Line> = rows
        .iter()
        .map(|r| Line {
            text: r.overlay_text(),
            color: if r.done {
                Some(DONE_RGB)
            } else if r.urgent {
                Some(EXPIRING_RGB)
            } else {
                None
            },
            extra_ascent_px: if r.gap { BOARD_GAP_PX } else { 0.0 },
            strike: r.done,
            runs: r.runs(),
            ..Line::default()
        })
        .collect();
    if !v.challenge_status.is_empty() {
        lines.insert(
            0,
            Line {
                text: format!("challenges: {}", v.challenge_status),
                ..Line::default()
            },
        );
    }
    lines
}

#[derive(Clone)]
struct ChallengeSection {
    category: Category,
    challenges: Vec<Challenge>,
}

impl Category {
    fn label(self) -> &'static str {
        match self {
            Category::Repeatable => "event",
            Category::Clan => "clan",
            Category::Personal => "yours",
        }
    }
}

fn group_by_category(board: &[Challenge]) -> Vec<ChallengeSection> {
    let mut out: Vec<ChallengeSection> = Vec::new();
    for c in board {
        let k = category_of(c);
        if let Some(s) = out.iter_mut().find(|s| s.category == k) {
            s.challenges.push(c.clone());
        } else {
            out.push(ChallengeSection {
                category: k,
                challenges: vec![c.clone()],
            });
        }
    }
    out
}

fn shared_prefix(challenges: &[Challenge]) -> String {
    if challenges.len() < 2 {
        return String::new();
    }
    const SEP: &str = " - ";
    let Some(i) = challenges[0].name.find(SEP) else {
        return String::new();
    };
    if i == 0 {
        return String::new();
    }
    let prefix = challenges[0].name[..i + SEP.len()].to_string();
    for c in challenges {
        if !c.name.starts_with(&prefix) {
            return String::new();
        }
        if c.name[prefix.len()..].trim().is_empty() {
            return String::new();
        }
    }
    prefix
}

impl ChallengeRow {
    fn label(&self) -> String {
        if self.heading {
            return self.heading_text();
        }
        let mut b = String::new();
        if self.sub {
            b.push_str("  ");
        }
        b.push_str(&self.name);
        if !self.objective.is_empty() {
            if !self.name.is_empty() {
                b.push_str(": ");
            }
            b.push_str(&self.objective);
        }
        b
    }

    fn padding(&self, label: &str) -> String {
        let mut width = 2;
        let extra = self.pad.saturating_sub(label.chars().count());
        if extra > 0 {
            width += extra;
        }
        " ".repeat(width)
    }

    fn heading_text(&self) -> String {
        let head = format!("── {} ", self.name);
        if self.rule > head.chars().count() {
            let fill = self.rule - head.chars().count();
            head + &"─".repeat(fill)
        } else {
            head
        }
    }

    fn width(&self) -> usize {
        if self.heading {
            return 0;
        }
        let label = self.label();
        let mut n = label.chars().count();
        if !self.progress.is_empty() {
            n += self.padding(&label).chars().count() + self.progress.chars().count();
            if !self.countdown.is_empty() {
                n += 2 + self.countdown.chars().count();
            }
        } else if !self.countdown.is_empty() {
            n += self.padding(&label).chars().count() + self.countdown.chars().count();
        }
        n
    }

    fn text(&self) -> String {
        if self.heading {
            return self.heading_text();
        }
        let label = self.label();
        let mut b = label.clone();
        if !self.progress.is_empty() {
            b.push_str(&self.padding(&label));
            b.push_str(&self.progress);
            if !self.countdown.is_empty() {
                b.push_str("  ");
                b.push_str(&self.countdown);
            }
        } else if !self.countdown.is_empty() {
            b.push_str(&self.padding(&label));
            b.push_str(&self.countdown);
        }
        if self.done {
            // Overlay draws a strike instead. Plain text still says it, for
            // --print-hud and the tests that have no strikethrough.
            b.push_str(" done");
        }
        b
    }

    fn overlay_text(&self) -> String {
        let mut text = self.text();
        if self.done {
            text = text.strip_suffix(" done").unwrap_or(&text).to_string();
        }
        text
    }

    fn runs(&self) -> Vec<TextRun> {
        let row = if self.done { DONE_ALPHA } else { 1.0 };
        let run = |text: String, part: f32| TextRun {
            text,
            alpha: row * part,
        };
        if self.heading {
            return vec![run(self.heading_text(), HEADING_ALPHA)];
        }

        let mut out = Vec::new();
        let mut name = self.name.clone();
        if self.sub {
            name.insert_str(0, "  ");
        }
        if !self.objective.is_empty() {
            if !self.name.is_empty() {
                name.push_str(": ");
            }
            if !name.is_empty() {
                out.push(run(name, 1.0));
            }
            out.push(run(self.objective.clone(), OBJECTIVE_ALPHA));
        } else if !name.is_empty() {
            out.push(run(name, 1.0));
        }

        let label = self.label();
        if !self.progress.is_empty() {
            out.push(run(self.padding(&label), 1.0));
            out.push(run(self.progress.clone(), 1.0));
            if !self.countdown.is_empty() {
                out.push(run(format!("  {}", self.countdown), COUNTDOWN_ALPHA));
            }
        } else if !self.countdown.is_empty() {
            out.push(run(self.padding(&label), 1.0));
            out.push(run(self.countdown.clone(), COUNTDOWN_ALPHA));
        }
        out
    }
}

fn challenge_rows(c: &Challenge, now: DateTime<Utc>, cfg: &Config) -> Vec<ChallengeRow> {
    let remaining = c.remaining(now);
    let mut countdown = String::new();
    if remaining > std::time::Duration::ZERO && remaining < std::time::Duration::from_hours(24) {
        countdown = format::countdown(remaining);
    }
    let urgent = !c.complete()
        && cfg.widget.challenges.urgent_within.0 > std::time::Duration::ZERO
        && remaining > std::time::Duration::ZERO
        && remaining <= cfg.widget.challenges.urgent_within.0;

    if c.objectives.len() == 1 && name_covers_objective(&c.name, &c.objectives[0].name) {
        let (score, target) = c.progress();
        return vec![ChallengeRow {
            name: c.name.clone(),
            progress: format!("{}/{}", format::int(score), format::int(target)),
            countdown,
            done: c.complete(),
            urgent,
            ..ChallengeRow::default()
        }];
    }

    let mut rows = vec![ChallengeRow {
        name: c.name.clone(),
        countdown,
        done: c.complete(),
        urgent,
        ..ChallengeRow::default()
    }];
    for o in &c.objectives {
        rows.push(ChallengeRow {
            objective: o.name.clone(),
            progress: format!("{}/{}", format::int(o.score), format::int(o.target)),
            done: o.done(),
            urgent: urgent && !o.done(),
            sub: true,
            ..ChallengeRow::default()
        });
    }
    rows
}

fn name_covers_objective(name: &str, objective: &str) -> bool {
    if objective.is_empty() {
        return true;
    }
    name.to_lowercase().contains(&objective.to_lowercase())
}

/// Case does not matter, and the weapon masteries' " Expert" suffix is
/// optional: "smg" pins "SMG Expert". The stripped names collide with nothing
/// (Looter, Artisan and Master carry no suffix).
fn pin_matches(pin: &str, name: &str) -> bool {
    if pin.eq_ignore_ascii_case(name) {
        return true;
    }
    name.strip_suffix(" Expert")
        .is_some_and(|base| pin.eq_ignore_ascii_case(base))
}

fn show_mastery(m: &Mastery, w: &MasteriesWidget) -> bool {
    // Pinning is "watch these": it overrides the other filters, so a pinned
    // mastered mastery (or Artisan) still shows.
    if !w.pin.is_empty() {
        return w.pin.iter().any(|p| pin_matches(p, &m.name));
    }
    if m.mastered() && !w.show_mastered {
        return false;
    }
    if !w.show_artisan && m.name.eq_ignore_ascii_case("artisan") {
        return false;
    }
    true
}

fn mastery_lines(v: &ModelView, cfg: &Config) -> Vec<Line> {
    let w = &cfg.widget.masteries;
    if !w.enabled {
        return Vec::new();
    }
    let all = v.masteries.as_deref().unwrap_or(&[]);
    let cap = if w.max_shown > 0 {
        w.max_shown as usize
    } else {
        usize::MAX
    };
    let status_line = || Line {
        text: format!("masteries: {}", v.mastery_status),
        ..Line::default()
    };
    let shown: Vec<&Mastery> = all.iter().filter(|m| show_mastery(m, w)).take(cap).collect();
    if shown.is_empty() {
        if !v.mastery_status.is_empty() && all.is_empty() {
            return vec![status_line()];
        }
        return Vec::new();
    }
    let label = |m: &Mastery| format!("{} {}", m.name, m.level);
    let pad = shown
        .iter()
        .map(|m| label(m).chars().count())
        .max()
        .unwrap_or(0);
    let mut lines: Vec<Line> = shown
        .iter()
        .map(|m| {
            let label = label(m);
            let progress = if m.mastered() {
                "MAX".to_string()
            } else {
                format!("{}/{}", format::int(m.exp), format::int(m.next_exp))
            };
            let gap = " ".repeat(2 + pad - label.chars().count());
            Line {
                text: format!("{label}{gap}{progress}"),
                color: if m.mastered() { Some(DONE_RGB) } else { None },
                runs: vec![
                    TextRun {
                        text: label,
                        alpha: 1.0,
                    },
                    TextRun {
                        text: gap,
                        alpha: 1.0,
                    },
                    TextRun {
                        text: progress,
                        alpha: OBJECTIVE_ALPHA,
                    },
                ],
                ..Line::default()
            }
        })
        .collect();
    if !v.mastery_status.is_empty() {
        lines.insert(0, status_line());
    }
    lines
}

fn map_view(v: &ModelView, cfg: &Config) -> MapView {
    let city = citymap::default();
    let radius = cfg.widget.map.radius;
    let (origin_x, origin_y, w, h) = map_window(v, city, radius);
    let mut cells = Vec::new();
    for y in origin_y..origin_y + h {
        for x in origin_x..origin_x + w {
            if !city.is_block(x, y) {
                continue;
            }
            if let Some(s) = city.shade(x, y) {
                cells.push(MapCell {
                    x,
                    y,
                    fill: [
                        f32::from(s.r) / 255.0,
                        f32::from(s.g) / 255.0,
                        f32::from(s.b) / 255.0,
                        s.alpha as f32,
                    ],
                });
            }
        }
    }
    let mut markers = Vec::new();
    for o in citymap::outposts() {
        if o.x >= origin_x
            && o.x < origin_x + w
            && o.y >= origin_y
            && o.y < origin_y + h
            && let Some(letter) = outpost_letter(o.name)
        {
            let outpost = [0.75, 1.0, 0.75, 1.0];
            markers.push(MapMarker {
                x: o.x,
                y: o.y,
                text: letter.into(),
                ink: outpost,
                color: outpost,
                ring: false,
            });
        }
    }
    let mut list = Vec::new();
    if let Some(marks) = &v.city_marks {
        let visible: Vec<&CityMark> = marks
            .iter()
            .filter(|m| {
                m.off_map
                    || (m.x >= origin_x
                        && m.x < origin_x + w
                        && m.y >= origin_y
                        && m.y < origin_y + h)
            })
            .collect();
        for m in &visible {
            markers.push(MapMarker {
                x: m.x,
                y: m.y,
                text: m.marker.clone(),
                ink: [1.0, 1.0, 1.0, 1.0],
                color: mark_color(m),
                ring: city_mark_ringed(m),
            });
        }
        if cfg.widget.map.show_list {
            list = map_list(&visible, cfg.widget.map.max_listed, in_onslaught(v));
        }
    }
    MapView {
        player_x: v.position_x,
        player_y: v.position_y,
        cells,
        markers,
        dividers_x: city.dividers_x.clone(),
        dividers_y: city.dividers_y.clone(),
        list,
    }
}

fn map_window(v: &ModelView, city: &citymap::Map, radius: i32) -> (i32, i32, i32, i32) {
    if radius <= 0 || !v.has_position || !city.is_block(v.position_x, v.position_y) {
        return (city.origin_x, city.origin_y, city.width, city.height);
    }
    let side = 2 * radius + 1;
    if side >= city.width && side >= city.height {
        return (city.origin_x, city.origin_y, city.width, city.height);
    }
    let w = side.min(city.width);
    let h = side.min(city.height);
    let x = (v.position_x - radius)
        .max(city.origin_x)
        .min(city.origin_x + city.width - w);
    let y = (v.position_y - radius)
        .max(city.origin_y)
        .min(city.origin_y + city.height - h);
    (x, y, w, h)
}

fn outpost_letter(name: &str) -> Option<&'static str> {
    citymap::outposts()
        .iter()
        .find(|o| o.name == name)
        .map(|o| o.letter)
}

fn rgb(r: u8, g: u8, b: u8) -> [f32; 4] {
    [
        f32::from(r) / 255.0,
        f32::from(g) / 255.0,
        f32::from(b) / 255.0,
        1.0,
    ]
}

fn mark_color(m: &CityMark) -> [f32; 4] {
    if !bossmap::daily_marker(&m.enemies).is_empty() {
        return DAILY_RGB;
    }
    match bossmap::mark_category(m.kind, &m.enemies) {
        MarkCategory::Nest => rgb(0xf0, 0x5c, 0xff),
        // Blue used to be missions. Red is reserved for mission rings/chips.
        MarkCategory::Boss => rgb(0x55, 0xa8, 0xff),
        MarkCategory::Bandits => rgb(0xff, 0xd1, 0x66),
        MarkCategory::Mission => rgb(0xff, 0x55, 0x55),
        MarkCategory::Qrf => rgb(0x5c, 0xe6, 0x5c),
        MarkCategory::Other => rgb(0xc0, 0xc0, 0xc0),
    }
}

fn city_mark_ringed(m: &CityMark) -> bool {
    if !bossmap::daily_marker(&m.enemies).is_empty() {
        return true;
    }
    match bossmap::mark_category(m.kind, &m.enemies) {
        MarkCategory::Mission => true,
        MarkCategory::Qrf => !bossmap::qrf_is_death_row(&m.event_type, &m.label),
        _ => false,
    }
}

fn list_key(m: &CityMark) -> String {
    if m.kind == CityEventKind::Qrf {
        format!(
            "{}|{}",
            m.marker,
            if bossmap::qrf_is_death_row(&m.event_type, &m.label) {
                "dr"
            } else {
                "wl"
            }
        )
    } else {
        m.marker.clone()
    }
}

fn closer(a: &CityMark, b: &CityMark) -> bool {
    if a.off_map != b.off_map {
        return b.off_map;
    }
    if a.reachable != b.reachable {
        return a.reachable;
    }
    if a.reachable && b.reachable {
        return a.walk.blocks < b.walk.blocks;
    }
    false
}

fn map_timer(m: &CityMark) -> String {
    let timer = if m.ends_in.0 > 0 {
        format::countdown(m.ends_in.std())
    } else {
        String::new()
    };
    if m.off_map {
        if timer.is_empty() {
            "Onslaught".into()
        } else {
            format!("{timer} Onslaught")
        }
    } else {
        timer
    }
}

/// City bosses, bandit packs, and QRFs share the hourly turnover. Dailies are
/// random, nests last about two hours, missions are their own clock.
fn shares_hourly_cycle(m: &CityMark) -> bool {
    if m.off_map {
        return false;
    }
    let cat = bossmap::mark_category(m.kind, &m.enemies);
    if cat != MarkCategory::Nest && !bossmap::daily_marker(&m.enemies).is_empty() {
        return false;
    }
    matches!(
        cat,
        MarkCategory::Boss | MarkCategory::Bandits | MarkCategory::Qrf
    )
}

/// Indices whose hourly countdown is a duplicate of an earlier (nearer) row.
fn hourly_cycle_dupes(entries: &[&CityMark]) -> std::collections::HashSet<usize> {
    let mut buckets: std::collections::HashMap<u64, Vec<usize>> = std::collections::HashMap::new();
    for (i, m) in entries.iter().enumerate() {
        if !shares_hourly_cycle(m) || m.ends_in.0 <= 0 {
            continue;
        }
        buckets
            .entry(m.ends_in.std().as_secs())
            .or_default()
            .push(i);
    }
    let mut hide = std::collections::HashSet::new();
    for idxs in buckets.values() {
        if idxs.len() < 2 {
            continue;
        }
        hide.extend(idxs.iter().copied().skip(1));
    }
    hide
}

fn map_list(marks: &[&CityMark], max_listed: i32, in_onslaught: bool) -> Vec<Line> {
    let mut order: Vec<String> = Vec::new();
    let mut best: std::collections::HashMap<String, CityMark> = std::collections::HashMap::new();
    for m in marks {
        let key = list_key(m);
        if let Some(prev) = best.get(&key) {
            if closer(m, prev) {
                best.insert(key, (*m).clone());
            }
        } else {
            order.push(key.clone());
            best.insert(key, (*m).clone());
        }
    }
    let mut entries: Vec<CityMark> = order.into_iter().filter_map(|k| best.remove(&k)).collect();
    entries.sort_by(|a, b| {
        if in_onslaught && a.off_map != b.off_map {
            return b.off_map.cmp(&a.off_map);
        }
        if closer(a, b) {
            std::cmp::Ordering::Less
        } else if closer(b, a) {
            std::cmp::Ordering::Greater
        } else {
            std::cmp::Ordering::Equal
        }
    });

    let cap = if max_listed <= 0 {
        entries.len()
    } else {
        max_listed as usize
    };
    let shown: Vec<&CityMark> = entries.iter().take(cap).collect();
    let hide_timer = hourly_cycle_dupes(&shown);
    let mut rows = Vec::new();
    for (i, m) in entries.iter().enumerate() {
        if i == cap {
            rows.push(Line {
                text: format!("+{} more", entries.len() - i),
                ..Line::default()
            });
            break;
        }
        let heading = if m.kind == CityEventKind::Qrf {
            if m.label.is_empty() {
                bossmap::qrf_display_name(&m.event_type, "")
            } else {
                m.label.clone()
            }
        } else if m.kind == CityEventKind::Mission {
            m.label.clone()
        } else {
            String::new()
        };
        let names: Vec<&str> = if m.kind == CityEventKind::Qrf || m.kind == CityEventKind::Mission {
            let mut names = Vec::new();
            if !heading.is_empty() {
                names.push(heading.as_str());
            }
            names.extend(
                m.enemies
                    .iter()
                    .map(String::as_str)
                    .filter(|s| m.kind != CityEventKind::Mission || bossmap::named_enemy(s)),
            );
            if names.is_empty() {
                names.push("mission");
            }
            names
        } else if m.enemies.is_empty() {
            vec![m.label.as_str()]
        } else {
            m.enemies.iter().map(String::as_str).collect()
        };
        let extra = if hide_timer.contains(&i) {
            String::new()
        } else {
            map_timer(m)
        };
        let extra = extra.trim();
        rows.push(Line {
            text: format!("{}  {}", m.marker, names[0]),
            chip: Some(mark_color(m)),
            timer: extra.to_string(),
            ..Line::default()
        });
        if m.kind == CityEventKind::Mission {
            for o in &m.objectives {
                rows.push(objective_line(format!("        {o}")));
            }
        }
        for n in names.iter().skip(1) {
            rows.push(Line {
                text: format!("        {n}"),
                ..Line::default()
            });
        }
    }
    rows
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::{Ns, Objective, Walk};
    use chrono::Duration as ChronoDuration;
    use std::time::Duration;

    fn all_on() -> Config {
        let mut cfg = Config::default();
        cfg.widget.challenges.enabled = true;
        cfg.widget.challenges.show_repeatable = true;
        cfg.widget.challenges.show_clan = true;
        cfg.widget.challenges.show_personal = true;
        cfg.widget.challenges.show_completed = true;
        cfg.widget.challenges.show_sections = false;
        cfg.widget.challenges.urgent_within = crate::config::Duration(Duration::ZERO);
        cfg
    }

    fn board(now: DateTime<Utc>) -> Vec<Challenge> {
        let end = now + ChronoDuration::days(5);
        let soon = now + ChronoDuration::minutes(20);
        vec![
            Challenge {
                name: "Summer Death".into(),
                repeatable: true,
                end,
                objectives: vec![Objective {
                    name: "Kill Regular Infected".into(),
                    target: 100,
                    score: 55,
                    has_score: true,
                }],
                ..Challenge::default()
            },
            Challenge {
                name: "Summer Loot".into(),
                repeatable: true,
                end,
                objectives: vec![Objective {
                    name: "Loot Anything".into(),
                    target: 10,
                    score: 10,
                    has_score: true,
                }],
                ..Challenge::default()
            },
            Challenge {
                name: "Untouched".into(),
                end,
                objectives: vec![Objective {
                    name: "Do Nothing".into(),
                    target: 10,
                    ..Objective::default()
                }],
                ..Challenge::default()
            },
            Challenge {
                name: "Nearly There".into(),
                end: soon,
                objectives: vec![Objective {
                    name: "Kill Dogs".into(),
                    target: 100,
                    score: 95,
                    has_score: true,
                }],
                ..Challenge::default()
            },
            Challenge {
                name: "Already Done".into(),
                end,
                objectives: vec![Objective {
                    name: "Kill Anything".into(),
                    target: 10,
                    score: 10,
                    has_score: true,
                }],
                ..Challenge::default()
            },
            Challenge {
                name: "Weekly Challenge - Kill Infected".into(),
                clan: true,
                end,
                objectives: vec![Objective {
                    name: "Kill Infected".into(),
                    target: 162401,
                    score: 159487,
                    has_score: true,
                }],
                ..Challenge::default()
            },
        ]
    }

    fn texts(lines: &[Line]) -> Vec<String> {
        lines.iter().map(|l| l.text.clone()).collect()
    }

    fn spawn(enemies: &[&str]) -> CityEvent {
        CityEvent {
            kind: CityEventKind::Spawn,
            enemies: enemies.iter().map(|s| (*s).to_string()).collect(),
            ..CityEvent::default()
        }
    }

    fn onslaught_view(now: DateTime<Utc>) -> ModelView {
        ModelView {
            now,
            have_data: true,
            has_position: true,
            position_x: ONSLAUGHT,
            position_y: ONSLAUGHT,
            ..ModelView::default()
        }
    }

    fn mark(
        marker: &str,
        blocks: i32,
        reachable: bool,
        ends: Duration,
        enemies: &[&str],
    ) -> CityMark {
        CityMark {
            marker: marker.into(),
            label: enemies.join(" + "),
            enemies: enemies.iter().map(|s| (*s).to_string()).collect(),
            x: 1020,
            y: 1000,
            ends_in: Ns::from_std(ends),
            walk: Walk {
                blocks,
                ..Walk::default()
            },
            reachable,
            ..CityMark::default()
        }
    }

    #[test]
    fn empty_view_is_silent() {
        let v = ModelView::default();
        let cfg = Config::default();
        let g = Groups::new();
        let s = from_view(&v, &cfg, &g);
        assert!(s.block.is_empty());
        assert!(s.xp.is_empty() || s.xp == XP_PENDING);
        assert!(s.challenges.is_empty());
        assert!(s.bosses.is_empty());
        assert!(s.map.list.is_empty());
        assert_eq!(
            s.keybinds,
            [
                "G - Minimap",
                "Z - Challenges",
                "J - Overlay",
                "K - Run clock",
                "U - XP/hr",
            ]
        );
    }

    #[test]
    fn show_position_prints_coords_beside_the_name() {
        let city = ModelView {
            have_data: true,
            has_position: true,
            position_x: 1054,
            position_y: 1015,
            zone_name: "South Eastern".into(),
            ..ModelView::default()
        };
        let mut cfg = Config::default();
        let g = Groups::new();
        let hidden = from_view(&city, &cfg, &g);
        assert_eq!(hidden.block, "South Eastern");
        assert!(hidden.block_sub.is_empty());
        cfg.widget.block.show_position = true;
        let shown = from_view(&city, &cfg, &g);
        assert_eq!(shown.block, "South Eastern");
        assert_eq!(shown.block_sub, "1054, 1015");

        let outpost = ModelView {
            in_outpost: true,
            outpost_name: "Nastya's Holdout".into(),
            have_data: true,
            has_position: true,
            position_x: 1058,
            position_y: 1019,
            ..ModelView::default()
        };
        cfg.widget.block.show_position = false;
        let hidden = from_view(&outpost, &cfg, &g);
        assert_eq!(hidden.block, "Nastya's Holdout");
        assert!(hidden.block_sub.is_empty());
        cfg.widget.block.show_position = true;
        let shown = from_view(&outpost, &cfg, &g);
        assert_eq!(shown.block, "Nastya's Holdout");
        assert_eq!(shown.block_sub, "1058, 1019");
    }

    #[test]
    fn keybinds_skip_empty_bindings_and_follow_toggle() {
        let mut cfg = Config::default();
        cfg.hotkeys.map = String::new();
        cfg.hotkeys.overlay = "ctrl+shift+j".into();
        let g = Groups::new();
        let shown = from_view(&ModelView::default(), &cfg, &g);
        assert_eq!(
            shown.keybinds,
            [
                "Z - Challenges",
                "Ctrl+Shift+J - Overlay",
                "K - Run clock",
                "U - XP/hr",
            ]
        );
        assert!(g.toggle("keybinds").unwrap());
        assert!(
            from_view(&ModelView::default(), &cfg, &g)
                .keybinds
                .is_empty()
        );
        cfg.widget.keybinds.enabled = false;
        let g = Groups::new();
        assert!(
            from_view(&ModelView::default(), &cfg, &g)
                .keybinds
                .is_empty()
        );
    }

    #[test]
    fn map_is_empty_without_a_player_tile() {
        let v = ModelView {
            have_data: true,
            ..ModelView::default()
        };
        let cfg = Config::default();
        let g = Groups::new();
        assert!(!g.toggle("map").unwrap());
        let s = from_view(&v, &cfg, &g);
        assert!(s.map.cells.is_empty());
        assert!(s.map.markers.is_empty());
        assert!(s.map.list.is_empty());
    }

    #[test]
    fn block_boss_countdown_sits_on_the_first_row_only() {
        let now = DateTime::from_timestamp(10_000, 0).unwrap();
        let mut nest = spawn(&["3 x Evolved Longarms", "1 x Irradiated Wraith"]);
        nest.end = now + ChronoDuration::minutes(55);
        let v = ModelView {
            have_data: true,
            now,
            block_events: Some(vec![nest]),
            ..ModelView::default()
        };
        let rows = boss_lines(&v, &Config::default());
        assert_eq!(rows[0].text, "3 x Evolved Longarms");
        assert_eq!(rows[0].timer, "55m");
        assert_eq!(rows[1].text, "1 x Irradiated Wraith");
        assert!(rows[1].timer.is_empty());
        assert!(rows[0].color.is_none(), "name keeps the widget amber");

        let mut over = spawn(&["1 x Titan"]);
        over.end = now;
        let none = boss_lines(
            &ModelView {
                have_data: true,
                now,
                block_events: Some(vec![over, spawn(&["6 x Bandits"])]),
                ..ModelView::default()
            },
            &Config::default(),
        );
        assert!(none.iter().all(|r| r.timer.is_empty()));
    }

    #[test]
    fn mission_objective_is_dim_on_the_block() {
        let now = DateTime::from_timestamp(10_000, 0).unwrap();
        let mission = CityEvent {
            kind: CityEventKind::Mission,
            title: "Disarmed".into(),
            enemies: vec!["3 x Flaming Titan".into()],
            objectives: vec!["Find O'Connell's arms (2)".into()],
            end: now + ChronoDuration::minutes(40),
            ..CityEvent::default()
        };
        let rows = boss_lines(
            &ModelView {
                have_data: true,
                now,
                block_events: Some(vec![mission]),
                ..ModelView::default()
            },
            &Config::default(),
        );
        assert_eq!(rows[0].text, "Disarmed");
        assert!(rows[0].runs.is_empty());
        assert_eq!(rows[1].text, "3 x Flaming Titan");
        assert!(rows[1].runs.is_empty());
        assert_eq!(rows[2].text, "Find O'Connell's arms (2)");
        assert_eq!(rows[2].runs.len(), 1);
        assert!((rows[2].runs[0].alpha - OBJECTIVE_ALPHA).abs() < 1e-5);
    }

    #[test]
    fn outpost_attack_is_alarm_red() {
        let v = ModelView {
            have_data: true,
            outpost_attack: true,
            ..ModelView::default()
        };
        let rows = boss_lines(&v, &Config::default());
        assert_eq!(rows[0].text, "OUTPOST ATTACK");
        assert_eq!(rows[0].color, Some(EXPIRING_RGB));
    }

    #[test]
    fn xp_stability_colours_the_rate() {
        let mut v = ModelView {
            have_data: true,
            xp_available: true,
            xp_per_hour: 1_234_567.0,
            xp_stability: XpStability::Shaky,
            ..ModelView::default()
        };
        let cfg = Config::default();
        assert_eq!(
            xp_line(&v, &cfg),
            Some(("1,234,567".into(), Some(SHAKY_RGB)))
        );
        v.xp_stability = XpStability::Unstable;
        assert_eq!(
            xp_line(&v, &cfg),
            Some(("1,234,567".into(), Some(UNSTABLE_RGB)))
        );
        v.xp_provisional = true;
        assert_eq!(
            xp_line(&v, &cfg),
            Some((format!("{XP_ROUGH}1,234,567"), None))
        );
    }

    #[test]
    fn challenge_lines_follow_the_board() {
        let now = Utc::now();
        let v = ModelView {
            now,
            challenges: Some(board(now)),
            ..ModelView::default()
        };
        let lines = texts(&challenge_lines(&v, &all_on()));
        assert_eq!(lines.len(), 11, "{lines:?}");
        assert_eq!(lines[0], "Summer Death");
        assert!(
            lines[1].starts_with("  Kill Regular Infected") && lines[1].contains("55/100"),
            "{}",
            lines[1]
        );
        for line in &lines {
            assert!(!line.contains("5d"), "{line}");
        }
        assert!(
            lines[6].contains("Nearly There") && lines[6].contains("20m"),
            "{}",
            lines[6]
        );
        assert!(
            lines[2].contains("Summer Loot") && !lines[2].contains("done"),
            "{}",
            lines[2]
        );
        assert!(!lines[3].contains("done"), "{}", lines[3]);
        let board = challenge_lines(&v, &all_on());
        assert!(board[2].strike && board[3].strike);
        assert!(
            lines[10].starts_with("Weekly Challenge - Kill Infected")
                && lines[10].contains("159,487/162,401"),
            "{}",
            lines[10]
        );
    }

    #[test]
    fn challenge_name_covers_a_clan_objective() {
        let now = Utc::now();
        let cfg = all_on();
        let first = Challenge {
            name: "First Strike".into(),
            end: now + ChronoDuration::hours(20),
            objectives: vec![Objective {
                name: "Kill Any Boss".into(),
                target: 7,
                ..Objective::default()
            }],
            ..Challenge::default()
        };
        let rows: Vec<String> = challenge_rows(&first, now, &cfg)
            .iter()
            .map(|r| r.text())
            .collect();
        assert_eq!(rows.len(), 2);
        assert!(rows[0].contains("First Strike") && !rows[0].contains("Kill Any Boss"));
        assert!(
            rows[1].starts_with("  ")
                && rows[1].contains("Kill Any Boss")
                && rows[1].contains("0/7")
        );

        let clan = Challenge {
            name: "Weekly Challenge - Kill Infected".into(),
            clan: true,
            end: now + ChronoDuration::days(4),
            objectives: vec![Objective {
                name: "Kill Infected".into(),
                target: 162401,
                score: 162423,
                has_score: true,
            }],
            ..Challenge::default()
        };
        let rows = challenge_rows(&clan, now, &cfg);
        assert_eq!(rows.len(), 1);
        let text = rows[0].text();
        assert_eq!(text.to_lowercase().matches("kill infected").count(), 1);
        assert!(text.contains("162,423/162,401") && text.contains("done"));
    }

    #[test]
    fn challenge_aligns_progress_and_gaps() {
        let now = Utc::now();
        let v = ModelView {
            now,
            challenges: Some(board(now)),
            ..ModelView::default()
        };
        let rows = challenge_lines(&v, &all_on());
        assert!((rows[0].extra_ascent_px - 0.0).abs() < f32::EPSILON);
        let gaps = rows.iter().filter(|r| r.extra_ascent_px > 0.0).count();
        assert_eq!(gaps, 5);
        for (i, r) in rows.iter().enumerate() {
            if r.text.starts_with("  ") {
                assert!(
                    r.extra_ascent_px == 0.0,
                    "objective row {i} must not break the group"
                );
            }
        }

        let mut column = None;
        let mut counted = 0;
        for r in &rows {
            let Some(at) = r.text.find('/') else {
                continue;
            };
            let start = r.text[..at]
                .rfind(|c: char| !c.is_ascii_digit() && c != ',')
                .map(|i| i + 1)
                .unwrap_or(0);
            counted += 1;
            match column {
                None => column = Some(start),
                Some(col) => assert_eq!(start, col, "{}", r.text),
            }
        }
        assert!(counted >= 5, "board should carry progress");
    }

    #[test]
    fn challenge_colours_done_and_expiring() {
        let mut cfg = all_on();
        cfg.widget.challenges.urgent_within =
            crate::config::Duration(Duration::from_secs(2 * 3600));
        let now = Utc::now();
        let done = Challenge {
            name: "Summer Loot".into(),
            end: now + ChronoDuration::days(5),
            objectives: vec![Objective {
                name: "Loot Anything".into(),
                target: 10,
                score: 10,
                has_score: true,
            }],
            ..Challenge::default()
        };
        assert!(challenge_rows(&done, now, &cfg)[0].done);
        assert!(!challenge_rows(&done, now, &cfg)[0].urgent);

        let soon = Challenge {
            name: "Big Fancy Dinner".into(),
            end: now + ChronoDuration::minutes(90),
            objectives: vec![Objective {
                name: "Loot Food".into(),
                target: 25,
                score: 8,
                has_score: true,
            }],
            ..Challenge::default()
        };
        let rows = challenge_rows(&soon, now, &cfg);
        assert!(rows[0].urgent && !rows[0].done);
        assert!(rows[1].urgent);

        let later = Challenge {
            name: "Who Let The Dogs Out?".into(),
            end: now + ChronoDuration::hours(18),
            objectives: vec![Objective {
                name: "Kill Dog Infected".into(),
                target: 1000,
                score: 316,
                has_score: true,
            }],
            ..Challenge::default()
        };
        assert!(!challenge_rows(&later, now, &cfg)[0].urgent);

        let finished = Challenge {
            name: "Just In Time".into(),
            end: now + ChronoDuration::minutes(10),
            objectives: vec![Objective {
                name: "Kill Anything".into(),
                target: 5,
                score: 5,
                has_score: true,
            }],
            ..Challenge::default()
        };
        let row = &challenge_rows(&finished, now, &cfg)[0];
        assert!(row.done && !row.urgent);

        cfg.widget.challenges.urgent_within = crate::config::Duration(Duration::ZERO);
        assert!(!challenge_rows(&soon, now, &cfg)[0].urgent);
        assert!(challenge_rows(&done, now, &cfg)[0].done);
    }

    #[test]
    fn challenge_sections_and_filters() {
        let now = Utc::now();
        let mut extra = board(now);
        extra.push(Challenge {
            name: "Weekly Challenge - Loot Anything".into(),
            clan: true,
            end: now + ChronoDuration::days(5),
            objectives: vec![Objective {
                name: "Loot Anything".into(),
                target: 100,
                score: 4,
                has_score: true,
            }],
            ..Challenge::default()
        });
        let v = ModelView {
            now,
            challenges: Some(extra),
            ..ModelView::default()
        };
        let mut cfg = all_on();
        cfg.widget.challenges.show_sections = true;
        let lines = texts(&challenge_lines(&v, &cfg));
        let joined = lines.join("\n");
        let headings: Vec<usize> = lines
            .iter()
            .enumerate()
            .filter(|(_, l)| l.starts_with("──"))
            .map(|(i, _)| i)
            .collect();
        assert_eq!(headings.len(), 3, "{joined}");
        assert_eq!(headings[0], 0);
        assert!(lines[headings[0]].contains("event"));
        assert!(lines[headings[2]].contains("clan"));
        assert!(lines[headings[2]].contains("Weekly Challenge"));
        assert_eq!(joined.matches("Weekly Challenge").count(), 1);
        assert!(joined.contains("Kill Infected") && joined.contains("159,487/162,401"));

        cfg.widget.challenges.show_repeatable = false;
        cfg.widget.challenges.show_clan = false;
        cfg.widget.challenges.show_sections = true;
        let only_personal = texts(&challenge_lines(&v, &cfg));
        assert!(only_personal.iter().all(|l| !l.starts_with("──")));

        let hidden = ModelView {
            now,
            challenges: Some(board(now)),
            challenge_status: "stale".into(),
            ..ModelView::default()
        };
        let mut none = Config::default();
        none.widget.challenges.enabled = true;
        none.widget.challenges.show_repeatable = false;
        none.widget.challenges.show_clan = false;
        none.widget.challenges.show_personal = false;
        assert!(challenge_lines(&hidden, &none).is_empty());

        let empty = ModelView {
            now,
            challenge_status: "bridge script sends no salt - update it".into(),
            ..ModelView::default()
        };
        let lines = challenge_lines(&empty, &all_on());
        assert_eq!(lines.len(), 1);
        assert!(lines[0].text.contains("sends no salt"));
    }

    #[test]
    fn challenge_status_shows_on_a_stale_board() {
        let now = Utc::now();
        let v = ModelView {
            now,
            challenges: Some(board(now)),
            challenge_status: "could not load the board (retrying)".into(),
            ..ModelView::default()
        };
        let lines = texts(&challenge_lines(&v, &all_on()));
        assert!(
            lines[0].contains("could not load the board"),
            "{}",
            lines[0]
        );
        assert!(
            lines.iter().any(|l| l.contains("Summer Death")),
            "{lines:?}"
        );
    }

    fn run_alpha(line: &Line, contains: &str) -> f32 {
        line.runs
            .iter()
            .find(|r| r.text.contains(contains))
            .unwrap_or_else(|| panic!("no run containing {contains:?} in {:?}", line.runs))
            .alpha
    }

    #[test]
    fn challenge_runs_dim_name_objective_countdown_heading_and_done() {
        let now = Utc::now();
        let v = ModelView {
            now,
            challenges: Some(vec![
                Challenge {
                    name: "First Strike".into(),
                    end: now + ChronoDuration::minutes(20),
                    objectives: vec![Objective {
                        name: "Kill Any Boss".into(),
                        target: 7,
                        score: 2,
                        has_score: true,
                    }],
                    ..Challenge::default()
                },
                Challenge {
                    name: "Summer Loot".into(),
                    repeatable: true,
                    end: now + ChronoDuration::days(5),
                    objectives: vec![Objective {
                        name: "Loot Anything".into(),
                        target: 10,
                        score: 10,
                        has_score: true,
                    }],
                    ..Challenge::default()
                },
            ]),
            ..ModelView::default()
        };
        let mut cfg = all_on();
        cfg.widget.challenges.show_sections = true;
        let lines = challenge_lines(&v, &cfg);

        let heading = lines
            .iter()
            .find(|l| l.text.starts_with("──"))
            .expect("section heading");
        assert!(heading.color.is_none());
        assert!(!heading.strike);
        assert_eq!(heading.runs.len(), 1);
        assert!(
            (heading.runs[0].alpha - HEADING_ALPHA).abs() < 1e-5,
            "{}",
            heading.runs[0].alpha
        );

        let name = lines
            .iter()
            .find(|l| l.text.contains("First Strike"))
            .expect("name");
        assert!((run_alpha(name, "First Strike") - 1.0).abs() < 1e-5);
        assert!((run_alpha(name, "20m") - COUNTDOWN_ALPHA).abs() < 1e-5);

        let objective = lines
            .iter()
            .find(|l| l.text.contains("Kill Any Boss"))
            .expect("objective");
        assert!((run_alpha(objective, "Kill Any Boss") - OBJECTIVE_ALPHA).abs() < 1e-5);

        let done_name = lines
            .iter()
            .find(|l| l.text.contains("Summer Loot"))
            .expect("done name");
        assert!(done_name.strike);
        assert_eq!(done_name.color, Some(DONE_RGB));
        assert!((run_alpha(done_name, "Summer Loot") - DONE_ALPHA).abs() < 1e-5);

        let done_obj = lines
            .iter()
            .find(|l| l.text.contains("Loot Anything"))
            .expect("done objective");
        assert!(done_obj.strike);
        assert!(
            (run_alpha(done_obj, "Loot Anything") - DONE_ALPHA * OBJECTIVE_ALPHA).abs() < 1e-5,
            "{}",
            run_alpha(done_obj, "Loot Anything")
        );

        let joined: String = done_obj.runs.iter().map(|r| r.text.as_str()).collect();
        assert_eq!(joined, done_obj.text);
    }

    fn mastery(name: &str, level: i32, exp: i64, next: i64, value: f64, max: f64) -> Mastery {
        Mastery {
            name: name.into(),
            level,
            exp,
            next_exp: next,
            bonuses: vec![crate::model::MasteryBonus {
                name: "Bonus".into(),
                scale: 0.1,
                max,
                value,
            }],
            ..Mastery::default()
        }
    }

    fn masteries() -> Vec<Mastery> {
        vec![
            mastery("Looter", 204, 37, 103, 1.02, 5.0),
            mastery("Melee Expert", 48, 512, 750, 4.8, 20.0),
            mastery("Artisan", 120, 9, 130, 6.0, 20.0),
            mastery("SMG Expert", 200, 3, 1200, 20.0, 20.0),
        ]
    }

    fn masteries_on() -> Config {
        let mut cfg = Config::default();
        cfg.widget.masteries.enabled = true;
        cfg
    }

    #[test]
    fn mastery_lines_hide_artisan_and_mastered_by_default() {
        let v = ModelView {
            masteries: Some(masteries()),
            ..ModelView::default()
        };
        let cfg = masteries_on();
        let lines = mastery_lines(&v, &cfg);
        let text = texts(&lines);
        assert_eq!(text.len(), 2, "{text:?}");
        assert_eq!(text[0], "Looter 204       37/103");
        assert_eq!(text[1], "Melee Expert 48  512/750");
        assert!(lines.iter().all(|l| l.color.is_none() && !l.strike));

        let mut disabled = cfg.clone();
        disabled.widget.masteries.enabled = false;
        assert!(mastery_lines(&v, &disabled).is_empty());
    }

    #[test]
    fn mastery_show_toggles_bring_rows_back() {
        let v = ModelView {
            masteries: Some(masteries()),
            ..ModelView::default()
        };
        let mut cfg = masteries_on();
        cfg.widget.masteries.show_mastered = true;
        let lines = mastery_lines(&v, &cfg);
        let smg = lines
            .iter()
            .find(|l| l.text.contains("SMG Expert"))
            .expect("mastered row shown");
        assert!(smg.text.ends_with("MAX"), "{}", smg.text);
        assert_eq!(smg.color, Some(DONE_RGB));
        assert!(!smg.strike, "MAX already says it; no strikethrough");

        cfg.widget.masteries.show_artisan = true;
        let text = texts(&mastery_lines(&v, &cfg));
        assert_eq!(text.len(), 4, "{text:?}");
        assert!(text.iter().any(|l| l.contains("Artisan")));
    }

    #[test]
    fn mastery_pin_shows_only_pinned_and_beats_other_filters() {
        let v = ModelView {
            masteries: Some(masteries()),
            ..ModelView::default()
        };
        let mut cfg = masteries_on();
        cfg.widget.masteries.pin = vec!["smg expert".into(), "ARTISAN".into()];
        let text = texts(&mastery_lines(&v, &cfg));
        assert_eq!(text.len(), 2, "{text:?}");
        assert!(text[0].contains("Artisan"), "board order kept: {text:?}");
        assert!(text[1].contains("SMG Expert"));

        cfg.widget.masteries.pin = vec!["No Such Mastery".into()];
        assert!(mastery_lines(&v, &cfg).is_empty());
    }

    #[test]
    fn mastery_pin_expert_suffix_is_optional() {
        assert!(pin_matches("melee", "Melee Expert"));
        assert!(pin_matches("SMG", "SMG Expert"));
        assert!(pin_matches("machine gun", "Machine Gun Expert"));
        assert!(pin_matches("Looter", "Looter"));
        assert!(!pin_matches("Expert", "Melee Expert"));
        assert!(!pin_matches("lee", "Melee Expert"), "not a substring match");

        let v = ModelView {
            masteries: Some(masteries()),
            ..ModelView::default()
        };
        let mut cfg = masteries_on();
        cfg.widget.masteries.pin = vec!["melee".into()];
        let text = texts(&mastery_lines(&v, &cfg));
        assert_eq!(text.len(), 1, "{text:?}");
        assert!(text[0].contains("Melee Expert"));
    }

    #[test]
    fn mastery_max_shown_caps_rows() {
        let v = ModelView {
            masteries: Some(masteries()),
            ..ModelView::default()
        };
        let mut cfg = masteries_on();
        cfg.widget.masteries.max_shown = 1;
        let text = texts(&mastery_lines(&v, &cfg));
        assert_eq!(text.len(), 1, "{text:?}");
        assert!(text[0].contains("Looter"));
    }

    #[test]
    fn mastery_status_line_mirrors_challenges() {
        let empty = ModelView {
            mastery_status: "no session yet - install the bridge script".into(),
            ..ModelView::default()
        };
        let lines = mastery_lines(&empty, &masteries_on());
        assert_eq!(lines.len(), 1);
        assert_eq!(
            lines[0].text,
            "masteries: no session yet - install the bridge script"
        );

        let stale = ModelView {
            masteries: Some(masteries()),
            mastery_status: "could not load masteries (retrying)".into(),
            ..ModelView::default()
        };
        let lines = texts(&mastery_lines(&stale, &masteries_on()));
        assert!(lines[0].contains("could not load masteries"), "{lines:?}");
        assert!(lines.iter().any(|l| l.contains("Looter")), "{lines:?}");
    }

    #[test]
    fn mastery_group_toggle_hides_the_widget() {
        let v = ModelView {
            masteries: Some(masteries()),
            ..ModelView::default()
        };
        let cfg = masteries_on();
        let g = Groups::new();
        assert!(!from_view(&v, &cfg, &g).masteries.is_empty());
        assert!(g.toggle("masteries").unwrap());
        assert!(from_view(&v, &cfg, &g).masteries.is_empty());
    }

    #[test]
    fn shared_prefix_cuts_at_a_separator() {
        let one = |names: &[&str]| {
            names
                .iter()
                .map(|n| Challenge {
                    name: (*n).into(),
                    ..Challenge::default()
                })
                .collect::<Vec<_>>()
        };
        assert_eq!(
            shared_prefix(&one(&[
                "Weekly Challenge - Kill Infected",
                "Weekly Challenge - Travel Blocks"
            ])),
            "Weekly Challenge - "
        );
        assert_eq!(
            shared_prefix(&one(&["Weekly Challenge - Kill Infected"])),
            ""
        );
        assert_eq!(
            shared_prefix(&one(&["Kill Infected", "Kill Dog Infected"])),
            ""
        );
        assert_eq!(
            shared_prefix(&one(&[
                "Weekly Challenge - A",
                "Weekly Challenge - B",
                "Summer Death"
            ])),
            ""
        );
        assert_eq!(
            shared_prefix(&one(&["Weekly Challenge - ", "Weekly Challenge - B"])),
            ""
        );
    }

    #[test]
    fn map_list_collapses_and_orders() {
        let a = mark("1", 9, true, Duration::from_secs(60), &["6 x Bandits"]);
        let b = mark("1", 3, true, Duration::from_secs(60), &["6 x Bandits"]);
        let c = mark("1", 14, true, Duration::from_secs(60), &["6 x Bandits"]);
        let rows = map_list(&[&a, &b, &c], 20, false);
        assert_eq!(rows.len(), 1);
        assert!(rows[0].text.contains("Bandits") && rows[0].chip.is_some());
        assert!(rows[0].timer.contains("1m"));

        let far = mark("a", 12, true, Duration::from_secs(60), &["far"]);
        let near = mark("b", 2, true, Duration::from_secs(60), &["near"]);
        let unk = mark(
            "c",
            0,
            false,
            Duration::from_secs(60),
            &["unknown distance"],
        );
        let rows = map_list(&[&far, &near, &unk], 20, false);
        let order: Vec<String> = rows
            .iter()
            .filter(|r| r.chip.is_some())
            .map(|r| {
                let marker = r.text.split_whitespace().next().unwrap_or("");
                let name = r.text.split("  ").last().unwrap_or("");
                format!("{marker} {name}")
            })
            .collect();
        assert_eq!(order, vec!["b near", "a far", "c unknown distance"]);
    }

    #[test]
    fn map_list_nest_onslaught_and_cap() {
        let pack = mark("1", 1, true, Duration::from_secs(60), &["6 x Bandits"]);
        let nest = mark(
            "4",
            7,
            true,
            Duration::from_secs(30),
            &[
                "3 x Evolved Longarms",
                "1 x Irradiated Wraith",
                "1 x Mega Mother",
            ],
        );
        let rows = map_list(&[&pack, &nest], 20, false);
        assert_eq!(rows.len(), 4);
        assert!(rows[1].text.contains("4") && rows[1].text.contains("3 x Evolved Longarms"));
        assert!(rows[2].chip.is_none() && rows[2].text.contains("Irradiated"));

        let mission = CityMark {
            marker: "7".into(),
            label: "mission: The Clue".into(),
            kind: CityEventKind::Mission,
            x: 1002,
            y: 1000,
            walk: Walk {
                blocks: 4,
                ..Walk::default()
            },
            reachable: true,
            ..CityMark::default()
        };
        let rows = map_list(&[&mission], 20, false);
        assert_eq!(rows.len(), 1);
        assert!(rows[0].text.contains("The Clue"));

        let typed = CityMark {
            marker: "M5".into(),
            label: "Disarmed".into(),
            kind: CityEventKind::Mission,
            enemies: vec!["200 x 64".into()],
            objectives: vec!["Find O'Connell's arms (2)".into()],
            walk: Walk {
                blocks: 2,
                ..Walk::default()
            },
            reachable: true,
            ..CityMark::default()
        };
        let rows = map_list(&[&typed], 20, false);
        assert!(
            rows[0].text.contains("Disarmed"),
            "title first, got {:?}",
            rows[0].text
        );
        assert!(
            rows.iter()
                .any(|r| r.chip.is_none() && r.text.contains("O'Connell")),
            "objective under the title: {:?}",
            rows
        );
        assert!(
            rows.iter().all(|r| !r.text.contains("200 x 64")),
            "type id leaked: {:?}",
            rows
        );

        let inferno = CityMark {
            marker: "M2".into(),
            label: "Red Inferno".into(),
            kind: CityEventKind::Mission,
            enemies: vec!["3 x Flaming Titan".into()],
            objectives: vec!["Eliminate the Flaming Titans (3)".into()],
            walk: Walk {
                blocks: 3,
                ..Walk::default()
            },
            reachable: true,
            ..CityMark::default()
        };
        let rows = map_list(&[&inferno], 20, false);
        assert!(rows[0].text.contains("Red Inferno"));
        assert!(
            rows[1].chip.is_none() && rows[1].text.contains("Eliminate the Flaming Titans"),
            "objective under the title: {:?}",
            rows
        );
        assert_eq!(rows[1].runs.len(), 1);
        assert!((rows[1].runs[0].alpha - OBJECTIVE_ALPHA).abs() < 1e-5);
        assert!(
            rows.iter()
                .any(|r| r.text.contains("Flaming Titan") && r.runs.is_empty())
        );

        let dropped = [
            mark("1", 1, true, Duration::ZERO, &["a"]),
            mark("2", 2, true, Duration::ZERO, &["b"]),
            mark("3", 3, true, Duration::ZERO, &["c"]),
            mark("4", 4, true, Duration::ZERO, &["d"]),
        ];
        let refs: Vec<&CityMark> = dropped.iter().collect();
        let rows = map_list(&refs, 2, false);
        assert_eq!(rows.last().unwrap().text, "+2 more");

        let mut off = mark(
            "z",
            0,
            false,
            Duration::from_secs(4 * 60),
            &["3 x Mega Wraith"],
        );
        off.off_map = true;
        let on = mark("a", 3, true, Duration::from_secs(60), &["6 x Bandits"]);
        let rows = map_list(&[&on, &off], 20, true);
        assert!(rows[0].text.contains("Mega Wraith") && rows[0].timer.contains("Onslaught"));
    }

    #[test]
    fn city_mark_rings_daily_mission_and_qrf() {
        let bandits = mark("B1", 1, true, Duration::from_secs(60), &["6 x Bandits"]);
        assert!(!city_mark_ringed(&bandits));
        let daily = mark("DH", 1, true, Duration::from_secs(60), &["1 x Devil Hound"]);
        assert!(city_mark_ringed(&daily));
        assert_eq!(mark_color(&daily), DAILY_RGB);
        let mission = CityMark {
            kind: CityEventKind::Mission,
            enemies: vec![],
            ..CityMark::default()
        };
        assert!(city_mark_ringed(&mission));
        let qrf = CityMark {
            kind: CityEventKind::Qrf,
            event_type: "qrf".into(),
            label: "QRF Wasteland".into(),
            ..CityMark::default()
        };
        assert!(city_mark_ringed(&qrf));
        let death_row = CityMark {
            kind: CityEventKind::Qrf,
            event_type: "qrfdr".into(),
            label: "QRF Death Row".into(),
            ..CityMark::default()
        };
        assert!(!city_mark_ringed(&death_row));
        let boss = mark("I4", 1, true, Duration::from_secs(60), &["2 x Titan"]);
        assert!(!city_mark_ringed(&boss));
        assert_ne!(mark_color(&boss)[0], mark_color(&mission)[0]);
        assert!(mark_color(&mission)[0] > 0.9 && mark_color(&mission)[1] < 0.4);
        assert!(mark_color(&boss)[2] > 0.6);
    }

    #[test]
    fn city_mark_letters_stay_white_on_the_map() {
        let boss = mark("I4", 1, true, Duration::from_secs(60), &["2 x Titan"]);
        let bandits = mark("B4", 1, true, Duration::from_secs(60), &["4 x Bandits"]);
        let v = ModelView {
            city_marks: Some(vec![boss, bandits]),
            ..ModelView::default()
        };
        let mv = map_view(&v, &Config::default());
        let i4 = mv.markers.iter().find(|m| m.text == "I4").expect("I4");
        let b4 = mv.markers.iter().find(|m| m.text == "B4").expect("B4");
        assert_eq!(i4.ink, [1.0, 1.0, 1.0, 1.0]);
        assert_eq!(b4.ink, [1.0, 1.0, 1.0, 1.0]);
        assert!(
            i4.color[2] > 0.6,
            "boss chip/ring stays blue, not the letter"
        );
        assert!(
            b4.color[0] > 0.9 && b4.color[1] > 0.7,
            "bandit chip stays amber"
        );
        assert!(!i4.ring && !b4.ring);
        let rows = map_list(
            &[&mark(
                "I4",
                1,
                true,
                Duration::from_secs(60),
                &["2 x Titan"],
            )],
            20,
            false,
        );
        assert_eq!(rows[0].chip, Some(i4.color));
        assert!(!rows[0].timer.is_empty());
        assert!(!rows[0].text.contains(&rows[0].timer));
    }

    #[test]
    fn qrf_legend_keeps_both_glyphs_and_names() {
        let waste = CityMark {
            marker: bossmap::QRF_MARKER.into(),
            label: "QRF Wasteland".into(),
            kind: CityEventKind::Qrf,
            event_type: "qrf".into(),
            ends_in: Ns::from_std(Duration::from_secs(60)),
            walk: Walk {
                blocks: 4,
                ..Walk::default()
            },
            reachable: true,
            ..CityMark::default()
        };
        let death = CityMark {
            marker: bossmap::QRF_DEATH_ROW_MARKER.into(),
            label: "QRF Death Row".into(),
            kind: CityEventKind::Qrf,
            event_type: "qrfdr".into(),
            ends_in: Ns::from_std(Duration::from_secs(60)),
            walk: Walk {
                blocks: 8,
                ..Walk::default()
            },
            reachable: true,
            ..CityMark::default()
        };
        let rows = map_list(&[&waste, &death], 20, false);
        let texts: Vec<_> = rows.iter().map(|r| r.text.as_str()).collect();
        assert!(
            texts
                .iter()
                .any(|t| t.contains(bossmap::QRF_MARKER) && t.contains("Wasteland")),
            "{texts:?}"
        );
        assert!(
            texts
                .iter()
                .any(|t| t.contains(bossmap::QRF_DEATH_ROW_MARKER) && t.contains("Death Row")),
            "{texts:?}"
        );
        assert_eq!(rows.iter().filter(|r| r.chip.is_some()).count(), 2);
        assert_eq!(
            rows.iter().filter(|r| r.chip_ring).count(),
            0,
            "legend chips stay one size; the grid ring already marks wasteland"
        );
        assert!(
            !rows[0].timer.is_empty() && rows[1].timer.is_empty(),
            "shared hour once, on the nearer QRF: {:?}",
            rows.iter()
                .map(|r| (r.text.as_str(), r.timer.as_str()))
                .collect::<Vec<_>>()
        );
    }

    #[test]
    fn map_list_hides_shared_hourly_timers() {
        let hour = Duration::from_secs(19 * 60 + 36);
        let bandits = mark("B4", 2, true, hour, &["4 x Bandits"]);
        let boss = mark("I6", 5, true, hour, &["4 x Flaming Long Arms"]);
        let qrf = CityMark {
            marker: bossmap::QRF_MARKER.into(),
            label: "QRF Wasteland".into(),
            kind: CityEventKind::Qrf,
            event_type: "qrf".into(),
            ends_in: Ns::from_std(hour),
            walk: Walk {
                blocks: 6,
                ..Walk::default()
            },
            reachable: true,
            ..CityMark::default()
        };
        let nest = mark(
            "N4",
            7,
            true,
            Duration::from_secs(2 * 3600),
            &["3 x Flaming Titan", "1 x Flaming Mother"],
        );
        let daily = mark("DH", 8, true, hour, &["1 x Devil Hound"]);
        let mission = CityMark {
            marker: "M5".into(),
            label: "Wrath of the Wraiths".into(),
            kind: CityEventKind::Mission,
            enemies: vec!["5 x Flaming Wraith".into()],
            ends_in: Ns::from_std(Duration::from_secs(10 * 3600 - 60)),
            walk: Walk {
                blocks: 9,
                ..Walk::default()
            },
            reachable: true,
            ..CityMark::default()
        };
        let rows = map_list(&[&bandits, &boss, &qrf, &nest, &daily, &mission], 20, false);
        let headed: Vec<_> = rows.iter().filter(|r| r.chip.is_some()).collect();
        let timer_of = |marker: &str| {
            headed
                .iter()
                .find(|r| r.text.starts_with(marker))
                .map(|r| r.timer.as_str())
                .unwrap_or("")
        };
        let hour_text = crate::format::countdown(hour);
        assert_eq!(timer_of("B4"), hour_text);
        assert!(
            timer_of("I6").is_empty(),
            "same hour as B4: {:?}",
            timer_of("I6")
        );
        assert!(timer_of(bossmap::QRF_MARKER).is_empty(), "same hour as B4");
        assert_eq!(
            timer_of("N4"),
            crate::format::countdown(Duration::from_secs(2 * 3600)),
            "nests keep the two-hour window"
        );
        assert_eq!(
            timer_of("DH"),
            hour_text,
            "dailies keep their own clock even when it matches the hour"
        );
        assert!(!timer_of("M5").is_empty());
    }

    #[test]
    fn hud_lines_orders_by_position_and_leads_with_status() {
        let mut cfg = Config::default();
        let groups = Groups::new();
        let mut v = ModelView {
            game_running: true,
            has_session: true,
            session_time: Ns::from_std(Duration::from_secs(3600)),
            have_data: true,
            has_position: true,
            zone_name: "South Eastern".into(),
            ..ModelView::default()
        };
        assert_eq!(
            hud_lines(&v, &cfg, &groups),
            [
                "IC Time: 1:00:00",
                "Xp/Hr: --",
                "South Eastern",
                "G - Minimap",
                "Z - Challenges",
                "J - Overlay",
                "K - Run clock",
                "U - XP/hr",
            ]
        );
        cfg.widget.session.y = 900;
        let lines = hud_lines(&v, &cfg, &groups);
        assert_eq!(lines.last().map(String::as_str), Some("IC Time: 1:00:00"));
        v.status = "session expired".into();
        assert_eq!(hud_lines(&v, &cfg, &groups)[0], "session expired");
        cfg.widget.block.enabled = false;
        cfg.widget.session.enabled = false;
        cfg.widget.xp.enabled = false;
        cfg.widget.keybinds.enabled = false;
        assert_eq!(hud_lines(&v, &cfg, &groups), ["session expired"]);
    }

    #[test]
    fn status_prompts_are_amber_errors_red() {
        let cfg = Config::default();
        let g = Groups::new();
        let fix = ModelView {
            status: "waiting for the bridge script".into(),
            status_is_prompt: true,
            ..ModelView::default()
        };
        assert_eq!(from_view(&fix, &cfg, &g).status_color, Some(SHAKY_RGB));
        let stuck = ModelView {
            status: "server not responding (retrying)".into(),
            status_is_prompt: false,
            ..ModelView::default()
        };
        assert_eq!(from_view(&stuck, &cfg, &g).status_color, Some(EXPIRING_RGB));
    }

    fn panel_body(rows: &[Line]) -> &[Line] {
        assert_eq!(rows[0].text, "Onslaught Cycles");
        &rows[1..]
    }

    #[test]
    fn onslaught_panel_orders_prev_now_next() {
        let v = ModelView {
            block_events_past: Some(vec![spawn(&["3 x Irradiated Wraith"])]),
            block_events: Some(vec![spawn(&["3 x Charred Giant Spider"])]),
            block_events_upcoming: Some(vec![spawn(&["2 x Titan"])]),
            ..onslaught_view(Utc::now())
        };
        let rows = onslaught_panel(&v).expect("in Onslaught");
        let body = panel_body(&rows);
        assert_eq!(body.len(), 3);
        assert_eq!(body[0].label, "prev");
        assert_eq!(body[0].text, "3 x Irradiated Wraith");
        assert_eq!(body[0].color, Some(rgb(0xb5, 0xb5, 0xb5)));
        assert_eq!(body[1].label, "now");
        assert_eq!(body[1].text, "3 x Charred Giant Spider");
        assert_eq!(body[1].color, Some(rgb(0xff, 0x4d, 0x4d)));
        assert_eq!(body[2].label, "next");
        assert_eq!(body[2].text, "2 x Titan");
        assert_eq!(body[2].color, Some(rgb(0x4f, 0xc3, 0xff)));
    }

    #[test]
    fn onslaught_panel_keeps_only_the_last_bundled_name() {
        let v = ModelView {
            block_events_past: Some(vec![spawn(&[
                "3 x Irradiated Giant Spider",
                "3 x Mega Giant Spider",
            ])]),
            ..onslaught_view(Utc::now())
        };
        let rows = onslaught_panel(&v).unwrap();
        let body = panel_body(&rows);
        assert_eq!(body[0].text, "3 x Mega Giant Spider");
    }

    #[test]
    fn onslaught_panel_shows_placeholders_when_empty() {
        let rows = onslaught_panel(&onslaught_view(Utc::now())).unwrap();
        let body = panel_body(&rows);
        assert_eq!(body.len(), 3);
        let empty = Some(rgb(0x6f, 0x6f, 0x6f));
        assert_eq!(body[0].text, "cleared");
        assert_eq!(body[0].color, empty);
        assert_eq!(body[1].text, "nothing this cycle");
        assert_eq!(body[1].color, empty);
        assert_eq!(body[2].text, "not announced");
        assert_eq!(body[2].color, empty);
    }

    #[test]
    fn onslaught_panel_shows_age_of_the_previous_cycle() {
        let now = DateTime::from_timestamp(10_000, 0).unwrap();
        let mut past = spawn(&["1 x Titan"]);
        past.end = now - ChronoDuration::minutes(3);
        let v = ModelView {
            block_events_past: Some(vec![past]),
            ..onslaught_view(now)
        };
        let rows = onslaught_panel(&v).unwrap();
        let body = panel_body(&rows);
        let age = body
            .iter()
            .find(|r| r.text == "ended 3m ago")
            .expect("age row");
        assert_eq!(age.color, Some(rgb(0x6f, 0x6f, 0x6f)));
        assert!(age.label.is_empty());
    }

    #[test]
    fn onslaught_panel_ages_the_displayed_boss_not_the_bundle() {
        let start = DateTime::from_timestamp(1_786_828_501, 0).unwrap();
        let now = start + ChronoDuration::minutes(13) + ChronoDuration::seconds(50);
        let mut past = spawn(&["3 x Mega Giant Spider", "3 x Irradiated Mother"]);
        past.start = start;
        past.end = start + ChronoDuration::minutes(5);
        let v = ModelView {
            block_events_past: Some(vec![past]),
            ..onslaught_view(now)
        };
        let rows = onslaught_panel(&v).unwrap();
        let ages: Vec<_> = panel_body(&rows)
            .iter()
            .filter(|r| r.text.starts_with("ended "))
            .map(|r| r.text.as_str())
            .collect();
        assert_eq!(ages, ["ended 3m ago"]);
    }

    #[test]
    fn onslaught_panel_omits_the_age_when_the_bundle_is_still_running() {
        let start = DateTime::from_timestamp(1_786_828_501, 0).unwrap();
        let mut past = spawn(&["3 x Mega Giant Spider", "3 x Irradiated Mother"]);
        past.start = start;
        past.end = start + ChronoDuration::minutes(5);
        let v = ModelView {
            block_events_past: Some(vec![past]),
            ..onslaught_view(start + ChronoDuration::minutes(7))
        };
        for r in panel_body(&onslaught_panel(&v).unwrap()) {
            assert!(
                !r.text.starts_with("ended "),
                "age {} while displayed cycle still running",
                r.text
            );
        }
    }

    #[test]
    fn onslaught_panel_only_applies_in_onslaught() {
        let v = ModelView {
            have_data: true,
            has_position: true,
            position_x: 1058,
            position_y: 1016,
            block_events: Some(vec![spawn(&["6 x Bandits"])]),
            ..ModelView::default()
        };
        assert!(onslaught_panel(&v).is_none());
    }

    #[test]
    fn onslaught_header_timer_is_mmss() {
        let v = ModelView {
            have_data: true,
            has_onslaught_countdown: true,
            onslaught_countdown: Ns::from_std(Duration::from_secs(3 * 60 + 59)),
            ..ModelView::default()
        };
        assert_eq!(onslaught_header_timer(&v).as_deref(), Some("3:59"));
        assert!(
            onslaught_header_timer(&ModelView {
                have_data: true,
                ..ModelView::default()
            })
            .is_none()
        );
        assert!(
            onslaught_header_timer(&ModelView {
                has_onslaught_countdown: true,
                onslaught_countdown: Ns::from_std(Duration::from_secs(60)),
                ..ModelView::default()
            })
            .is_none()
        );
    }

    #[test]
    fn map_is_hidden_in_onslaught() {
        let cfg = Config::default();
        let g = Groups::new();
        assert!(!g.toggle("map").unwrap());
        let onslaught = ModelView {
            city_marks: Some(vec![mark(
                "z",
                0,
                false,
                Duration::from_secs(60),
                &["3 x Mega Wraith"],
            )]),
            ..onslaught_view(Utc::now())
        };
        let hidden = from_view(&onslaught, &cfg, &g);
        assert!(hidden.map.cells.is_empty());
        assert!(hidden.map.markers.is_empty());
        assert!(hidden.map.list.is_empty());

        let city = ModelView {
            have_data: true,
            has_position: true,
            position_x: 1016,
            position_y: 1020,
            ..ModelView::default()
        };
        assert!(!from_view(&city, &cfg, &g).map.cells.is_empty());
    }

    #[test]
    fn nearest_is_skipped_in_onslaught() {
        let mut cfg = Config::default();
        cfg.widget.bosses.show_nearest = true;
        let v = ModelView {
            has_nearest: true,
            nearest_dx: 2,
            nearest_dy: -1,
            nearest_x: 1055,
            nearest_y: 985,
            nearest_distance_in_blocks: 3,
            ..onslaught_view(Utc::now())
        };
        let lines = boss_lines(&v, &cfg);
        assert!(lines.iter().all(|l| !l.text.contains("nearest")));
        assert!(lines.iter().any(|l| l.text == "Onslaught Cycles"));
    }
}
