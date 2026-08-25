//! One fetch of the `DFProfiler` bossmap feed, indexed by block.

use chrono::{DateTime, Utc};
use serde_json::Value;
use std::collections::HashMap;
use std::time::Duration;

use crate::model::{CityEvent, CityEventKind, CityMark, Walk};

pub const ONSLAUGHT_COORD: i32 = 3000;
/// `DFProfiler`'s Wasteland QRF triangle.
pub const QRF_MARKER: &str = "Δ";
/// Death Row, inverted so the two QRFs do not share a glance at Δ.
pub const QRF_DEATH_ROW_MARKER: &str = "▼";
const PAST_WINDOW: Duration = Duration::from_mins(12);
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
        edge_group(
            self.events_at(x, y, |e| e.ended_recently_at(now, PAST_WINDOW)),
            |e| e.end,
            |candidate, edge| candidate > edge,
        )
    }

    pub fn at_upcoming(&self, x: i32, y: i32, now: DateTime<Utc>) -> Vec<CityEvent> {
        edge_group(
            self.events_at(x, y, |e| e.upcoming_at(now)),
            |e| e.start,
            |candidate, edge| candidate < edge,
        )
    }

    /// Soonest start or end on this block after `now`. Local clock only.
    pub fn block_boundary(&self, x: i32, y: i32, now: DateTime<Utc>) -> Option<DateTime<Utc>> {
        let idxs = self.by_block.get(&(x, y))?;
        let mut best = None;
        let mut consider = |t: DateTime<Utc>| {
            if t.timestamp() == 0 || t <= now {
                return;
            }
            if best.is_none_or(|b| t < b) {
                best = Some(t);
            }
        };
        for &i in idxs {
            let e = &self.events[i];
            consider(e.start);
            consider(e.end);
        }
        best
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
                if !off_map
                    && let Some(w) = crate::data::citymap::default()
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

fn edge_group(
    events: Vec<CityEvent>,
    field: impl Fn(&CityEvent) -> DateTime<Utc>,
    more_extreme: impl Fn(DateTime<Utc>, DateTime<Utc>) -> bool,
) -> Vec<CityEvent> {
    let Some(first) = events.first() else {
        return Vec::new();
    };
    let mut edge = field(first);
    for e in &events {
        let t = field(e);
        if more_extreme(t, edge) {
            edge = t;
        }
    }
    events.into_iter().filter(|e| field(e) == edge).collect()
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
    if let Some(t) = obj.get("servertime").and_then(serde_json::Value::as_i64) {
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
            || ev.get("isoa").and_then(serde_json::Value::as_i64) == Some(1);
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
        let kind = classify_boss_event(need_briefing, special, event_type);
        let mut enemies = split_enemy_types(special);
        if kind == CityEventKind::Mission {
            // Some missions leave "{count} x {type_id}" (200 x 64) instead of a name.
            enemies.retain(|s| named_enemy(s));
        }
        let mut event = CityEvent {
            id: ev
                .get("event_id")
                .and_then(|v| v.as_str())
                .unwrap_or(key)
                .to_string(),
            kind,
            event_type: event_type.to_string(),
            title: html_unescape(ev.get("title").and_then(|v| v.as_str()).unwrap_or("")),
            enemies,
            objectives: parse_objectives(ev.get("dfp_objectives")),
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
                let (Some(x), Some(y)) = (i32_field(&pair[0]), i32_field(&pair[1])) else {
                    continue;
                };
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

fn i32_field(v: &Value) -> Option<i32> {
    if let Some(s) = v.as_str() {
        return s.parse().ok();
    }
    v.as_i64().and_then(|n| i32::try_from(n).ok())
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

/// True when `special_enemy_type` is a name (`3 x Flaming Titan`), not a
/// leftover type id (`200 x 64`).
pub(crate) fn named_enemy(s: &str) -> bool {
    let Some((_, rest)) = s.split_once(" x ") else {
        return !s.is_empty();
    };
    !rest.trim().bytes().all(|b| b.is_ascii_digit())
}

fn parse_objectives(v: Option<&Value>) -> Vec<String> {
    let Some(obj) = v.and_then(Value::as_object) else {
        return Vec::new();
    };
    let mut out = Vec::new();
    for (key, val) in obj {
        if key == "area_highlight" || key.ends_with("_amount") {
            continue;
        }
        let Some(text) = val.as_str().map(str::trim).filter(|s| !s.is_empty()) else {
            continue;
        };
        let text = html_unescape(text);
        let amount = obj
            .get(&format!("{key}_amount"))
            .and_then(Value::as_str)
            .map(str::trim)
            .filter(|n| !n.is_empty() && *n != "1" && *n != "0");
        out.push(match amount {
            Some(n) => format!("{text} ({n})"),
            None => text,
        });
    }
    out
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
            if t.is_empty() { None } else { Some(t) }
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

/// `DFProfiler`'s Death Row QRF (`qrfdr`) has no cell border; Wasteland (`qrf`) does.
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

    #[test]
    fn coordinate_conversion_rejects_out_of_range_values() {
        assert_eq!(i32_field(&serde_json::json!("3000")), Some(3000));
        assert_eq!(i32_field(&serde_json::json!(3000)), Some(3000));
        assert_eq!(i32_field(&serde_json::json!(i64::MAX)), None);
        assert_eq!(i32_field(&serde_json::json!("not-a-coordinate")), None);
    }

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
    fn mission_uses_the_title_not_a_type_id() {
        let now = DateTime::from_timestamp(1000, 0).unwrap();
        let raw = br#"{
	  "4":{"event_id":"4","isoa":"0","locations":[["1053","985"]],"started":"1","ended":"0",
	       "reward_cash":"31000","reward_exp":"650000","need_briefing":"1","title":"Disarmed","briefing":"",
	       "special_enemy_type":"200 x 64","special_enemy_amount":"200","boss_num":"4",
	       "event_type":"mission","dfp_objectives":{"loot":"Find O'Connell's arms","loot_amount":"2","area_highlight":"1053_985"},
	       "start_time":"900","end_time":"5000"},
	  "bosshash":"abc","servertime":1000,"version":"1"}"#;
        let m = parse(raw, now).unwrap();
        assert_eq!(m.events[0].kind, CityEventKind::Mission);
        assert_eq!(m.events[0].title, "Disarmed");
        assert!(
            m.events[0].enemies.is_empty(),
            "type id leaked: {:?}",
            m.events[0].enemies
        );
        assert_eq!(m.events[0].objectives, ["Find O'Connell's arms (2)"]);

        let named = br#"{
	  "1":{"event_id":"1","isoa":"0","locations":[["1029","1006"]],"started":"1","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"1","title":"Red Inferno","briefing":"",
	       "special_enemy_type":"3 x Flaming Titan","special_enemy_amount":"3","boss_num":"1",
	       "event_type":"mission","dfp_objectives":{"kill":"Eliminate the Flaming Titans","kill_amount":"3","area_highlight":"1029_1006"},
	       "start_time":"900","end_time":"5000"},
	  "bosshash":"abc","servertime":1000,"version":"1"}"#;
        let m = parse(named, now).unwrap();
        assert_eq!(m.events[0].title, "Red Inferno");
        assert_eq!(m.events[0].enemies, ["3 x Flaming Titan"]);
        assert_eq!(m.events[0].objectives, ["Eliminate the Flaming Titans (3)"]);
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
                assert_eq!(mk.label, "To The Slaughter");
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

    fn unix(secs: i64) -> DateTime<Utc> {
        DateTime::from_timestamp(secs, 0).unwrap()
    }

    fn parse_at(raw: &str, now: DateTime<Utc>) -> BossMap {
        parse(raw.as_bytes(), now).unwrap()
    }

    #[test]
    fn at_ended_narrows_to_the_most_recent_cycle() {
        let raw = r#"{
	  "0":{"event_id":"older","isoa":"0","locations":[["1000","1000"]],"started":"1","ended":"1",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Bear","special_enemy_amount":"1","boss_num":"1",
	       "event_type":"","dfp_objectives":[],"start_time":"1","end_time":"100"},
	  "1":{"event_id":"newer","isoa":"0","locations":[["1000","1000"]],"started":"1","ended":"1",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Titan","special_enemy_amount":"1","boss_num":"2",
	       "event_type":"","dfp_objectives":[],"start_time":"101","end_time":"200"},
	  "bosshash":"abc","servertime":250,"version":"1"}"#;
        let m = parse_at(raw, unix(250));
        let got = m.at_ended(1000, 1000, unix(250));
        assert_eq!(got.len(), 1);
        assert_eq!(got[0].enemies[0], "1 x Titan");
    }

    #[test]
    fn at_ended_keeps_the_whole_group_when_tied() {
        let raw = r#"{
	  "0":{"event_id":"a","isoa":"0","locations":[["1000","1000"]],"started":"1","ended":"1",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Bear","special_enemy_amount":"1","boss_num":"1",
	       "event_type":"","dfp_objectives":[],"start_time":"1","end_time":"200"},
	  "1":{"event_id":"b","isoa":"0","locations":[["1000","1000"]],"started":"1","ended":"1",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Titan","special_enemy_amount":"1","boss_num":"2",
	       "event_type":"","dfp_objectives":[],"start_time":"1","end_time":"200"},
	  "bosshash":"abc","servertime":250,"version":"1"}"#;
        let m = parse_at(raw, unix(250));
        assert_eq!(m.at_ended(1000, 1000, unix(250)).len(), 2);
    }

    #[test]
    fn at_upcoming_returns_the_soonest_cycle() {
        let raw = r#"{
	  "0":{"event_id":"later","isoa":"0","locations":[["1000","1000"]],"started":"0","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Bear","special_enemy_amount":"1","boss_num":"1",
	       "event_type":"","dfp_objectives":[],"start_time":"500","end_time":"800"},
	  "1":{"event_id":"sooner","isoa":"0","locations":[["1000","1000"]],"started":"0","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Titan","special_enemy_amount":"1","boss_num":"2",
	       "event_type":"","dfp_objectives":[],"start_time":"400","end_time":"700"},
	  "bosshash":"abc","servertime":100,"version":"1"}"#;
        let now = unix(100);
        let m = parse_at(raw, now);
        let got = m.at_upcoming(1000, 1000, now);
        assert_eq!(got.len(), 1);
        assert_eq!(got[0].enemies[0], "1 x Titan");
        assert!(!got[0].active_at(now));
        assert!(!got[0].ended_recently_at(now, PAST_WINDOW));
    }

    #[test]
    fn block_boundary_is_the_nearest_start_or_end() {
        let raw = r#"{
	  "0":{"event_id":"active","isoa":"0","locations":[["1000","1000"]],"started":"1","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Titan","special_enemy_amount":"1","boss_num":"1",
	       "event_type":"","dfp_objectives":[],"start_time":"1","end_time":"200"},
	  "bosshash":"abc","servertime":100,"version":"1"}"#;
        let m = parse_at(raw, unix(100));
        assert_eq!(m.block_boundary(1000, 1000, unix(100)), Some(unix(200)));
        assert_eq!(m.block_boundary(2000, 2000, unix(100)), None);
    }

    #[test]
    fn onslaught_cycles_shift_when_the_countdown_rolls_over() {
        let raw = r#"{
	  "0":{"event_id":"a","isoa":"0","locations":[["3000","3000"]],"started":"1","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"3 x Mega Mother","special_enemy_amount":"3","boss_num":"1",
	       "event_type":"","dfp_objectives":[],"start_time":"1000","end_time":"1300"},
	  "1":{"event_id":"b","isoa":"0","locations":[["3000","3000"]],"started":"0","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"3 x Eldritch Horror","special_enemy_amount":"3","boss_num":"17",
	       "event_type":"","dfp_objectives":[],"start_time":"1300","end_time":"1600"},
	  "bosshash":"abc","servertime":1147,"version":"1"}"#;
        let m = parse_at(raw, unix(1200));
        let names = |events: Vec<CityEvent>| {
            events
                .into_iter()
                .flat_map(|e| e.enemies)
                .collect::<Vec<_>>()
                .join("+")
        };
        let cycles = |now: DateTime<Utc>| {
            (
                names(m.at_ended(ONSLAUGHT_COORD, ONSLAUGHT_COORD, now)),
                names(m.at(ONSLAUGHT_COORD, ONSLAUGHT_COORD, now)),
                names(m.at_upcoming(ONSLAUGHT_COORD, ONSLAUGHT_COORD, now)),
                m.block_boundary(ONSLAUGHT_COORD, ONSLAUGHT_COORD, now)
                    .map(|b| (b - now).num_seconds()),
            )
        };

        let (prev, cur, next, left) = cycles(unix(1299));
        assert_eq!(
            (prev.as_str(), cur.as_str(), next.as_str()),
            ("", "3 x Mega Mother", "3 x Eldritch Horror")
        );
        assert_eq!(left, Some(1));

        let (prev, cur, next, left) = cycles(unix(1300));
        assert_eq!(prev, "3 x Mega Mother");
        assert_eq!(cur, "3 x Eldritch Horror");
        assert_eq!(next, "");
        assert_eq!(left, Some(300));
    }
}
