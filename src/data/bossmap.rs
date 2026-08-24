//! One fetch of the DFProfiler bossmap feed, indexed by block.

use chrono::{DateTime, Utc};
use serde_json::Value;
use std::collections::HashMap;
use std::time::Duration;

use crate::model::{CityEvent, CityEventKind, CityMark, Walk};

pub const ONSLAUGHT_COORD: i32 = 3000;
/// DFProfiler's Wasteland QRF triangle.
pub const QRF_MARKER: &str = "Δ";
/// Death Row, inverted so the two QRFs do not share a glance at Δ.
pub const QRF_DEATH_ROW_MARKER: &str = "▼";
const PAST_WINDOW: Duration = Duration::from_secs(12 * 60);
const LEGENDARY_BANDIT_PACK: i32 = 8;

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub enum MarkCategory {
    Boss,
    Nest,
    Bandits,
    Mission,
    Qrf,
    Other,
}

#[derive(Clone, Debug, Default)]
pub struct BossMap {
    /// Parse time. Overlay does not draw map age yet.
    #[allow(dead_code)]
    pub fetched_at: DateTime<Utc>,
    pub server_time: DateTime<Utc>,
    pub hash: String,
    pub events: Vec<CityEvent>,
    pub outpost_attack: bool,
    by_block: HashMap<(i32, i32), Vec<usize>>,
}

impl BossMap {
    pub fn at(&self, x: i32, y: i32, now: DateTime<Utc>) -> Vec<CityEvent> {
        self.events_at(x, y, |e| e.active_at(now))
    }

    pub fn at_ended(&self, x: i32, y: i32, now: DateTime<Utc>) -> Vec<CityEvent> {
        self.events_at(x, y, |e| e.ended_recently_at(now, PAST_WINDOW))
    }

    pub fn at_upcoming(&self, x: i32, y: i32, now: DateTime<Utc>) -> Vec<CityEvent> {
        self.events_at(x, y, |e| e.upcoming_at(now))
    }

    fn events_at(&self, x: i32, y: i32, pred: impl Fn(&CityEvent) -> bool) -> Vec<CityEvent> {
        let Some(idxs) = self.by_block.get(&(x, y)) else {
            return Vec::new();
        };
        idxs.iter()
            .map(|&i| &self.events[i])
            .filter(|e| pred(e))
            .cloned()
            .collect()
    }

    #[allow(dead_code)]
    pub fn age(&self, now: DateTime<Utc>) -> Duration {
        if now > self.fetched_at {
            (now - self.fetched_at).to_std().unwrap_or(Duration::ZERO)
        } else {
            Duration::ZERO
        }
    }

    pub fn active_marks(&self, now: DateTime<Utc>, from: [i32; 2], dist: &[i32]) -> Vec<CityMark> {
        let in_onslaught = from[0] == ONSLAUGHT_COORD && from[1] == ONSLAUGHT_COORD;
        let active: Vec<&CityEvent> = self
            .events
            .iter()
            .filter(|e| e.active_at(now))
            .filter(|e| !e.onslaught || in_onslaught)
            .collect();
        let markers = event_markers(&active);
        let mut marks = Vec::new();
        for (e, marker) in active.into_iter().zip(markers) {
            let ends_in = crate::model::Ns::from_chrono(if e.end > now {
                e.end - now
            } else {
                chrono::Duration::zero()
            });
            for loc in &e.locations {
                let mut walk = Walk::default();
                let mut reachable = false;
                let off_map = loc[0] == ONSLAUGHT_COORD && loc[1] == ONSLAUGHT_COORD;
                if !off_map {
                    if let Some(w) = crate::data::citymap::default()
                        .route_from(dist, from[0], from[1], loc[0], loc[1])
                    {
                        walk = crate::model::Walk {
                            blocks: w.blocks,
                            dx: w.dx,
                            dy: w.dy,
                            detour: w.detour,
                        };
                        reachable = true;
                    }
                }
                marks.push(CityMark {
                    marker: marker.clone(),
                    label: if e.kind == CityEventKind::Qrf {
                        qrf_display_name(&e.event_type, &e.title)
                    } else {
                        e.title.clone()
                    },
                    enemies: e.enemies.clone(),
                    kind: e.kind,
                    event_type: e.event_type.clone(),
                    x: loc[0],
                    y: loc[1],
                    ends_in,
                    off_map,
                    walk,
                    reachable,
                });
            }
        }
        marks
    }
}

pub fn nearest_mark(marks: &[CityMark]) -> Option<CityMark> {
    marks
        .iter()
        .filter(|m| m.reachable)
        .min_by_key(|m| m.walk.blocks)
        .cloned()
}

pub fn fetch(
    url: &str,
    user_agent: &str,
    timeout: Duration,
    now: DateTime<Utc>,
) -> Result<BossMap, String> {
    let body = crate::net::http::get_bytes(
        url,
        user_agent,
        timeout,
        4 << 20,
        &[
            ("Accept", "application/json"),
            ("X-Requested-With", "XMLHttpRequest"),
        ],
    )
    .map_err(|e| format!("bossmap: {e}"))?;
    parse(&body, now)
}

pub fn parse(data: &[u8], fetched_at: DateTime<Utc>) -> Result<BossMap, String> {
    let v: Value = serde_json::from_slice(data).map_err(|e| e.to_string())?;
    let obj = v
        .as_object()
        .ok_or_else(|| "bossmap: expected object".to_string())?;
    let mut out = BossMap {
        fetched_at,
        ..BossMap::default()
    };
    if let Some(h) = obj.get("bosshash").and_then(|v| v.as_str()) {
        out.hash = h.to_string();
    }
    if let Some(t) = obj.get("servertime").and_then(|v| v.as_i64()) {
        out.server_time = DateTime::from_timestamp(t, 0).unwrap_or(DateTime::<Utc>::UNIX_EPOCH);
    }
    for (key, raw) in obj {
        if matches!(key.as_str(), "bosshash" | "servertime" | "version") {
            continue;
        }
        let Some(ev) = raw.as_object() else {
            continue;
        };
        let isoa = ev.get("isoa").and_then(|v| v.as_str()) == Some("1")
            || ev.get("isoa").and_then(|v| v.as_i64()) == Some(1);
        let ended = ev.get("ended").and_then(|v| v.as_str()) == Some("1");
        if isoa {
            if !ended {
                out.outpost_attack = true;
            }
            continue;
        }
        let special = ev
            .get("special_enemy_type")
            .and_then(|v| v.as_str())
            .unwrap_or("");
        let need_briefing = ev
            .get("need_briefing")
            .and_then(|v| v.as_str())
            .unwrap_or("");
        let event_type = ev.get("event_type").and_then(|v| v.as_str()).unwrap_or("");
        let mut event = CityEvent {
            id: ev
                .get("event_id")
                .and_then(|v| v.as_str())
                .unwrap_or(key)
                .to_string(),
            kind: classify_boss_event(need_briefing, special, event_type),
            event_type: event_type.to_string(),
            title: html_unescape(ev.get("title").and_then(|v| v.as_str()).unwrap_or("")),
            enemies: split_enemy_types(special),
            objectives: Vec::new(),
            reward_exp: ev
                .get("reward_exp")
                .and_then(|v| v.as_str())
                .and_then(|s| s.parse().ok())
                .unwrap_or(0),
            slot: ev
                .get("boss_num")
                .and_then(|v| v.as_str())
                .and_then(|s| s.parse().ok())
                .unwrap_or(0),
            locations: Vec::new(),
            start: DateTime::<Utc>::UNIX_EPOCH,
            end: DateTime::<Utc>::UNIX_EPOCH,
            started: ev.get("started").and_then(|v| v.as_str()) == Some("1"),
            ended,
            onslaught: false,
        };
        if let Some(locs) = ev.get("locations").and_then(|v| v.as_array()) {
            for loc in locs {
                let Some(pair) = loc.as_array() else {
                    continue;
                };
                if pair.len() != 2 {
                    continue;
                }
                let x: i32 = pair[0]
                    .as_str()
                    .and_then(|s| s.parse().ok())
                    .or_else(|| pair[0].as_i64().map(|n| n as i32))
                    .unwrap_or(0);
                let y: i32 = pair[1]
                    .as_str()
                    .and_then(|s| s.parse().ok())
                    .or_else(|| pair[1].as_i64().map(|n| n as i32))
                    .unwrap_or(0);
                if x == ONSLAUGHT_COORD && y == ONSLAUGHT_COORD {
                    event.onslaught = true;
                }
                event.locations.push([x, y]);
            }
        }
        if let Some(t) = unix_field(ev.get("start_time")) {
            event.start = DateTime::from_timestamp(t, 0).unwrap_or(DateTime::<Utc>::UNIX_EPOCH);
        }
        if let Some(t) = unix_field(ev.get("end_time")) {
            event.end = DateTime::from_timestamp(t, 0).unwrap_or(DateTime::<Utc>::UNIX_EPOCH);
        }
        let idx = out.events.len();
        for loc in &event.locations {
            out.by_block.entry((loc[0], loc[1])).or_default().push(idx);
        }
        out.events.push(event);
    }
    Ok(out)
}

fn unix_field(v: Option<&Value>) -> Option<i64> {
    let v = v?;
    if let Some(s) = v.as_str() {
        return s.parse().ok();
    }
    v.as_i64()
}

/// Missions also carry `special_enemy_type`, so that test must not run first.
fn classify_boss_event(need_briefing: &str, special: &str, event_type: &str) -> CityEventKind {
    if need_briefing == "1" {
        CityEventKind::Mission
    } else if special != "0" && !special.is_empty() {
        CityEventKind::Spawn
    } else if !event_type.is_empty() {
        CityEventKind::Qrf
    } else {
        CityEventKind::Unknown
    }
}

fn split_enemy_types(s: &str) -> Vec<String> {
    if s.is_empty() || s == "0" {
        return Vec::new();
    }
    let s = s
        .replace("<br />", "\n")
        .replace("<br/>", "\n")
        .replace("<br>", "\n");
    s.split('\n')
        .filter_map(|part| {
            let t = html_unescape(part.trim());
            if t.is_empty() {
                None
            } else {
                Some(t)
            }
        })
        .collect()
}

fn html_unescape(s: &str) -> String {
    s.replace("&amp;", "&")
        .replace("&lt;", "<")
        .replace("&gt;", ">")
        .replace("&quot;", "\"")
        .replace("&#39;", "'")
        .replace("&nbsp;", " ")
}

pub fn mark_category(kind: CityEventKind, enemies: &[String]) -> MarkCategory {
    match kind {
        CityEventKind::Mission => MarkCategory::Mission,
        CityEventKind::Qrf => MarkCategory::Qrf,
        CityEventKind::Unknown => MarkCategory::Other,
        CityEventKind::Spawn => {
            if enemies.len() > 1 {
                MarkCategory::Nest
            } else if enemies.len() == 1 && enemies[0].to_lowercase().contains("bandit") {
                MarkCategory::Bandits
            } else {
                MarkCategory::Boss
            }
        }
    }
}

/// DFProfiler's Death Row QRF (`qrfdr`) has no cell border; Wasteland (`qrf`) does.
pub fn qrf_is_death_row(event_type: &str, title: &str) -> bool {
    let kind = event_type.trim().to_ascii_lowercase();
    if kind == "qrfdr" {
        return true;
    }
    title.to_ascii_lowercase().contains("death row")
}

pub fn qrf_marker(event_type: &str, title: &str) -> &'static str {
    if qrf_is_death_row(event_type, title) {
        QRF_DEATH_ROW_MARKER
    } else {
        QRF_MARKER
    }
}

pub fn qrf_display_name(event_type: &str, title: &str) -> String {
    let title = title.trim();
    let low = title.to_ascii_lowercase();
    if qrf_is_death_row(event_type, title) {
        if low.contains("death row") && !title.is_empty() {
            title.to_string()
        } else {
            "QRF Death Row".into()
        }
    } else if low.contains("wasteland") && !title.is_empty() {
        title.to_string()
    } else {
        "QRF Wasteland".into()
    }
}

pub fn daily_marker(enemies: &[String]) -> String {
    for e in enemies {
        let low = e.to_lowercase();
        for (name, marker) in [
            ("devil hound", "DH"),
            ("volatile leaper", "VL"),
            ("behemoth", "BH"),
        ] {
            if low.contains(name) {
                return marker.into();
            }
        }
        if low.contains("bandit") && enemy_count(e) >= LEGENDARY_BANDIT_PACK {
            return "LB".into();
        }
    }
    String::new()
}

fn enemy_count(enemy: &str) -> i32 {
    let Some((n, _)) = enemy.split_once(" x ") else {
        return 0;
    };
    n.trim().parse().unwrap_or(0)
}

fn event_markers(events: &[&CityEvent]) -> Vec<String> {
    let mut ranks: HashMap<MarkCategory, HashMap<i32, i32>> = HashMap::new();
    for e in events {
        let cat = mark_category(e.kind, &e.enemies);
        match cat {
            MarkCategory::Boss | MarkCategory::Nest | MarkCategory::Bandits => {
                if !daily_marker(&e.enemies).is_empty() && cat != MarkCategory::Nest {
                    continue;
                }
            }
            _ => continue,
        }
        ranks.entry(cat).or_default().entry(e.slot).or_insert(0);
    }
    for slots in ranks.values_mut() {
        let mut ordered: Vec<i32> = slots.keys().copied().collect();
        ordered.sort_unstable();
        for (i, slot) in ordered.iter().enumerate() {
            slots.insert(*slot, (i + 1) as i32);
        }
    }

    let mut out = Vec::with_capacity(events.len());
    for e in events {
        let cat = mark_category(e.kind, &e.enemies);
        let daily = daily_marker(&e.enemies);
        if !daily.is_empty() && cat != MarkCategory::Nest {
            out.push(daily);
            continue;
        }
        let marker = match cat {
            MarkCategory::Mission => format!("M{}", e.slot + 1),
            MarkCategory::Qrf => qrf_marker(&e.event_type, &e.title).to_string(),
            MarkCategory::Bandits => format!(
                "B{}",
                ranks
                    .get(&cat)
                    .and_then(|s| s.get(&e.slot))
                    .copied()
                    .unwrap_or(0)
            ),
            MarkCategory::Nest => format!(
                "N{}",
                ranks
                    .get(&cat)
                    .and_then(|s| s.get(&e.slot))
                    .copied()
                    .unwrap_or(0)
            ),
            MarkCategory::Boss => format!(
                "I{}",
                ranks
                    .get(&cat)
                    .and_then(|s| s.get(&e.slot))
                    .copied()
                    .unwrap_or(0)
            ),
            MarkCategory::Other => "?".into(),
        };
        out.push(marker);
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::Path;

    fn fixture() -> (BossMap, DateTime<Utc>) {
        let raw =
            std::fs::read(Path::new(env!("CARGO_MANIFEST_DIR")).join("testdata/bossmap.json"))
                .unwrap();
        let v: Value = serde_json::from_slice(&raw).unwrap();
        let t = v.get("servertime").and_then(|x| x.as_i64()).unwrap();
        let now = DateTime::from_timestamp(t, 0).unwrap();
        (parse(&raw, now).unwrap(), now)
    }

    #[test]
    fn parse_event_at_block() {
        let now = DateTime::from_timestamp(1000, 0).unwrap();
        let raw = br#"{
	  "0":{"event_id":"1","isoa":"0","locations":[["1055","985"]],"started":"1","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Titan","special_enemy_amount":"1","boss_num":"17",
	       "event_type":"","dfp_objectives":[],"start_time":"900","end_time":"5000"},
	  "bosshash":"abc","servertime":1000,"version":"1"}"#;
        let m = parse(raw, now).unwrap();
        let events = m.at(1055, 985, now);
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].enemies[0], "1 x Titan");
        assert_eq!(events[0].kind, CityEventKind::Spawn);
        assert!(m.at(1054, 987, now).is_empty());
    }

    #[test]
    fn classify_checks_briefing_before_special_enemy() {
        assert_eq!(
            classify_boss_event("1", "3 x Flaming Titan", "mission"),
            CityEventKind::Mission
        );
        assert_eq!(
            classify_boss_event("0", "1 x Bandits", ""),
            CityEventKind::Spawn
        );
        assert_eq!(classify_boss_event("0", "0", "qrf"), CityEventKind::Qrf);
        assert_eq!(classify_boss_event("0", "0", "qrfdr"), CityEventKind::Qrf);
        assert_eq!(classify_boss_event("0", "", ""), CityEventKind::Unknown);
    }

    #[test]
    fn split_nests_on_br() {
        let got = split_enemy_types("2 x Flaming Zombie<br />1 x Riot Shield Guy");
        assert_eq!(
            got,
            vec![
                "2 x Flaming Zombie".to_string(),
                "1 x Riot Shield Guy".to_string()
            ]
        );
        assert!(split_enemy_types("0").is_empty());
    }

    #[test]
    fn qrf_names_split_death_row_from_wasteland() {
        assert!(qrf_is_death_row("qrfdr", "QRF Extermination Mission"));
        assert!(!qrf_is_death_row("qrf", "QRF Extermination Mission"));
        assert_eq!(
            qrf_display_name("qrfdr", "QRF Extermination Mission"),
            "QRF Death Row"
        );
        assert_eq!(
            qrf_display_name("qrf", "QRF Extermination Mission"),
            "QRF Wasteland"
        );
        assert_eq!(qrf_display_name("qrfdr", "QRF Death Row"), "QRF Death Row");
        assert_eq!(qrf_marker("qrf", ""), QRF_MARKER);
        assert_eq!(qrf_marker("qrfdr", ""), QRF_DEATH_ROW_MARKER);
        assert_ne!(QRF_MARKER, QRF_DEATH_ROW_MARKER);
    }

    #[test]
    fn daily_marker_does_not_overmatch() {
        assert!(daily_marker(&["1 x Flaming Flesh Hound".into()]).is_empty());
        assert!(daily_marker(&["4 x Flaming Rumblers".into()]).is_empty());
        assert!(daily_marker(&["6 x Bandits".into()]).is_empty());
        assert!(daily_marker(&["7 x Bandits".into()]).is_empty());
        assert_eq!(daily_marker(&["1 x Devil Hound".into()]), "DH");
        assert_eq!(daily_marker(&["1 x Charred Devil Hound".into()]), "DH");
        assert_eq!(daily_marker(&["2 x Volatile Leaper".into()]), "VL");
        assert_eq!(daily_marker(&["1 x Behemoth".into()]), "BH");
        assert_eq!(daily_marker(&["8 x Bandits".into()]), "LB");
        assert_eq!(daily_marker(&["12 x Bandits".into()]), "LB");
    }

    #[test]
    fn event_markers_follow_the_feeds_own_slots() {
        let (m, now) = fixture();
        let marks = m.active_marks(now, [1020, 1000], &[]);
        let mut got: HashMap<String, String> = HashMap::new();
        for mk in &marks {
            if mk.off_map {
                continue;
            }
            let joined = mk.enemies.join(" + ");
            if let Some(was) = got.get(&mk.marker) {
                assert_eq!(was, &joined, "marker {} on two events", mk.marker);
            }
            got.insert(mk.marker.clone(), joined);
        }
        for marker in got.keys() {
            let n = marker.chars().count();
            assert!(
                n >= 2 || marker == QRF_MARKER || marker == QRF_DEATH_ROW_MARKER,
                "marker {marker:?} is a bare letter"
            );
        }
        assert_eq!(got.get("B1").map(String::as_str), Some("1 x Bandits"));
        assert_eq!(got.get("B2").map(String::as_str), Some("2 x Bandits"));
        assert_eq!(got.get("B3").map(String::as_str), Some("2 x Bandits"));
        assert_eq!(got.get("B4").map(String::as_str), Some("4 x Bandits"));
        assert_eq!(got.get("B5").map(String::as_str), Some("6 x Bandits"));
        assert!(!got.contains_key("B6"));
        for mk in &marks {
            if mk.kind == CityEventKind::Mission && mk.x == 1002 && mk.y == 1000 {
                assert_eq!(mk.marker, "M1");
            }
            if mk.kind == CityEventKind::Mission && mk.x == 1047 && mk.y == 987 {
                assert_eq!(mk.marker, "M5");
            }
        }
        assert!(got.contains_key("N1") && got.contains_key("N8"));
        assert!(!got.contains_key("N9"));
        assert!(got.contains_key("I1") && got.contains_key("I7"));
        assert!(!got.contains_key("I8"));
        let qrfs: Vec<_> = marks
            .iter()
            .filter(|m| m.kind == CityEventKind::Qrf)
            .collect();
        assert!(!qrfs.is_empty());
        for mk in &qrfs {
            assert_eq!(
                mk.marker,
                qrf_marker(&mk.event_type, &mk.label),
                "{}",
                mk.label
            );
        }
        let glyphs: std::collections::HashSet<_> =
            qrfs.iter().map(|mk| mk.marker.as_str()).collect();
        if qrfs.len() > 1 {
            assert!(
                glyphs.len() > 1,
                "Death Row and Wasteland must not share a glyph, got {glyphs:?}"
            );
        }
    }

    #[test]
    fn daily_boss_keeps_nest_as_n() {
        let (m, now) = fixture();
        let marks = m.active_marks(now, [1020, 1000], &[]);
        let alone = marks.iter().find(|mk| mk.x == 1017 && mk.y == 1024);
        let nest = marks.iter().find(|mk| mk.x == 1019 && mk.y == 1021);
        let alone = alone.expect("lone Devil Hound");
        let nest = nest.expect("nest containing Devil Hound");
        assert_eq!(alone.marker, "DH");
        assert!(nest.marker.starts_with('N'), "{}", nest.marker);
        assert!(!daily_marker(&alone.enemies).is_empty());
        assert!(!daily_marker(&nest.enemies).is_empty());
    }

    #[test]
    fn active_marks_skip_onslaught_unless_you_are_in_it() {
        let (m, now) = fixture();
        for mk in m.active_marks(now, [1048, 1010], &[]) {
            assert!(!mk.off_map, "{} at {},{}", mk.label, mk.x, mk.y);
        }
        let off = m
            .active_marks(now, [ONSLAUGHT_COORD, ONSLAUGHT_COORD], &[])
            .into_iter()
            .filter(|mk| mk.off_map)
            .count();
        assert!(off > 0, "standing in Onslaught should mark its cycles");
    }

    #[test]
    fn active_marks_number_events_in_order() {
        let (m, now) = fixture();
        let marks = m.active_marks(now, [0, 0], &[]);
        assert!(!marks.is_empty());
        let mut by_marker: HashMap<String, String> = HashMap::new();
        for mk in &marks {
            if let Some(label) = by_marker.get(&mk.marker) {
                assert_eq!(label, &mk.label, "marker {} reused", mk.marker);
            }
            by_marker.insert(mk.marker.clone(), mk.label.clone());
            assert!(!mk.reachable, "no distance table");
            assert!(!mk.marker.is_empty() && mk.marker != "?");
        }
    }
}
