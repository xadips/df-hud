//! Single source of HUD truth. Poller writes snapshots in; UI calls Derive.

use chrono::{DateTime, Utc};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use crate::data::bossmap::{self, BossMap};
use crate::data::catalog::Catalog;
use crate::data::citymap;
use crate::data::xp;
use crate::format;
use crate::model::{
    Challenge, Deadline, GameState, Ns, PollerStatus, PresenceState, RunState, Snapshot, Tick,
    View, XpRate, XpSample, XpSource, XpStability,
};

const DF_TIME_OFFSET: i64 = 1_200_000_000;
const DF_FOREVER: i64 = (1 << 31) - 8;
const DF_PLAUSIBLE_WINDOW: Duration = Duration::from_secs(365 * 24 * 3600);
const PRESENCE_MAX_AGE: Duration = Duration::from_secs(2 * 60);

pub fn parse_snapshot(
    vars: &HashMap<String, String>,
    at: DateTime<Utc>,
    catalog: Option<&Catalog>,
) -> Snapshot {
    let mut s = Snapshot {
        at,
        ..Snapshot::default()
    };
    s.level = int_var(vars, "df_level").unwrap_or(0);
    s.exp_in_level = int64_var(vars, "df_exp").unwrap_or(0);
    s.free_points = int_var(vars, "df_freepoints").unwrap_or(0);

    if let Some(total) = int64_var(vars, "df_exptotal").filter(|v| *v > 0) {
        s.cumulative_xp = total;
        s.xp_source = XpSource::ExpTotal;
    } else if let Some(c) = catalog {
        if let Some(total) = c.cumulative_xp(s.level, s.exp_in_level) {
            s.cumulative_xp = total;
            s.xp_source = XpSource::Table;
        }
    }
    if let Some(c) = catalog {
        if s.level > 0 {
            if let Some(needed) = c.exp_needed(s.level) {
                s.exp_needed = needed;
                s.pending_levels = pending_levels(c, s.level, s.exp_in_level);
            }
        }
    }

    let x = int_var(vars, "df_positionx");
    let y = int_var(vars, "df_positiony");
    if let (Some(x), Some(y)) = (x, y) {
        s.position_x = x;
        s.position_y = y;
        s.has_position = true;
        s.position_z = int_var(vars, "df_positionz").unwrap_or(0);
    }

    s.trade_zone = int_var(vars, "df_tradezone").unwrap_or(0);
    s.in_outpost = bool_var(vars, "df_inoutpost");
    if let Some(d) = int_var(vars, "df_dangerlevel") {
        s.danger_level = d;
        s.has_danger = true;
    }
    s.block_support = deadline_var(vars, "df_block_support_until", at);

    if let Some(start) = int64_var(vars, "df_expstart") {
        if s.xp_source == XpSource::ExpTotal && s.cumulative_xp >= start {
            s.exp_since_start = s.cumulative_xp - start;
            s.has_exp_since_start = true;
        }
    }

    s.hp = int_var(vars, "df_hpcurrent").unwrap_or(0);
    s.hp_max = int_var(vars, "df_hpmax").unwrap_or(0);
    if let Some(cash) = int64_var(vars, "df_cash") {
        s.cash = cash;
        s.has_cash = true;
    }
    s.bank_cash = int64_var(vars, "df_bankcash").unwrap_or(0);
    if let Some(n) = int_var(vars, "df_hungerhp") {
        s.nourishment = n;
        s.has_hunger = true;
    }
    s.boost_exp = deadline_var(vars, "df_boostexpuntil", at);
    s.gold_member = bool_var(vars, "df_goldmember");
    s.dead = bool_var(vars, "df_dead");
    s.server_time = df_compact_time_var(vars, "df_servertime");
    s.session_3d = fingerprint(vars.get("df_session3d").map(String::as_str).unwrap_or(""));
    s
}

fn pending_levels(c: &Catalog, level: i32, mut exp: i64) -> i32 {
    let mut levels = 0;
    loop {
        let Some(need) = c.exp_needed(level + levels) else {
            return levels;
        };
        if need <= 0 || exp < need {
            return levels;
        }
        exp -= need;
        levels += 1;
        if levels > 500 {
            return levels;
        }
    }
}

fn fingerprint(v: &str) -> String {
    if v.is_empty() {
        return String::new();
    }
    let sum = Sha256::digest(v.as_bytes());
    format!("{:02x}{:02x}{:02x}{:02x}", sum[0], sum[1], sum[2], sum[3])
}

fn int_var(vars: &HashMap<String, String>, key: &str) -> Option<i32> {
    vars.get(key)?.trim().parse().ok()
}

fn int64_var(vars: &HashMap<String, String>, key: &str) -> Option<i64> {
    vars.get(key)?.trim().parse().ok()
}

fn bool_var(vars: &HashMap<String, String>, key: &str) -> bool {
    vars.get(key).map(|s| s.trim() == "1").unwrap_or(false)
}

fn df_compact_time_var(vars: &HashMap<String, String>, key: &str) -> Option<DateTime<Utc>> {
    let v = int64_var(vars, key)?;
    if v <= 0 {
        return None;
    }
    DateTime::from_timestamp(v + DF_TIME_OFFSET, 0)
}

fn deadline_var(vars: &HashMap<String, String>, key: &str, now: DateTime<Utc>) -> Deadline {
    let Some(v) = int64_var(vars, key) else {
        return Deadline::default();
    };
    if v <= 0 {
        return Deadline::default();
    }
    if v >= DF_FOREVER {
        return Deadline {
            forever: true,
            ..Deadline::default()
        };
    }
    let Some(at) = DateTime::from_timestamp(v, 0) else {
        return Deadline::default();
    };
    let skew = if now >= at {
        (now - at).to_std().unwrap_or(Duration::MAX)
    } else {
        (at - now).to_std().unwrap_or(Duration::MAX)
    };
    if skew > DF_PLAUSIBLE_WINDOW {
        return Deadline::default();
    }
    Deadline {
        at: Some(at),
        forever: false,
    }
}

struct Inner {
    snapshot: Snapshot,
    have_snap: bool,
    prev_snap: Snapshot,
    have_prev: bool,
    game: GameState,
    catalog: Option<Catalog>,
    poller: PollerStatus,
    creds_at: Option<DateTime<Utc>>,
    run_start: Option<DateTime<Utc>>,
    run_seed: Option<RunState>,
    run_terminal: bool,
    on_run_change: Option<Arc<dyn Fn() + Send + Sync>>,
    presence: PresenceState,
    have_presence: bool,
    presence_connected: bool,
    boss_map: Option<BossMap>,
    board: Vec<Challenge>,
    have_board: bool,
    board_status: String,
    xp_samples: Option<Arc<dyn Fn() -> Vec<XpSample> + Send + Sync>>,
    xp_min_samples: i32,
    missed_ticks: i32,
    visibility: crate::model::Visibility,
}

pub struct Store {
    inner: Mutex<Inner>,
}

impl Store {
    pub fn new(catalog: Option<Catalog>) -> Self {
        Self {
            inner: Mutex::new(Inner {
                snapshot: Snapshot::default(),
                have_snap: false,
                prev_snap: Snapshot::default(),
                have_prev: false,
                game: GameState::default(),
                catalog,
                poller: PollerStatus::default(),
                creds_at: None,
                run_start: None,
                run_seed: None,
                run_terminal: false,
                on_run_change: None,
                presence: PresenceState::default(),
                have_presence: false,
                presence_connected: false,
                boss_map: None,
                board: Vec::new(),
                have_board: false,
                board_status: String::new(),
                xp_samples: None,
                xp_min_samples: 3,
                missed_ticks: 0,
                visibility: crate::model::Visibility {
                    visible: true,
                    ..crate::model::Visibility::default()
                },
            }),
        }
    }

    pub fn apply_tick(&self, tick: Tick) -> bool {
        let (applied, run_changed, on_change) = {
            let mut s = self.inner.lock().unwrap();
            if tick.err.is_some() {
                if tick.scheduled {
                    s.missed_ticks += 1;
                }
                return false;
            }
            let cat = s.catalog.clone();
            let snap = parse_snapshot(&tick.vars, tick.at, cat.as_ref());
            if s.have_snap {
                s.prev_snap = s.snapshot.clone();
                s.have_prev = true;
            }
            let run_changed = update_run_locked(&mut s, &snap);
            s.snapshot = snap;
            s.have_snap = true;
            s.missed_ticks = 0;
            (true, run_changed, s.on_run_change.clone())
        };
        fire_run_change(on_change, run_changed);
        applied
    }

    pub fn set_game(&self, g: GameState) {
        let (run_changed, on_change) = {
            let mut s = self.inner.lock().unwrap();
            let prev = s.game;
            s.game = g;
            let session_changed = (g.running || prev.running) && !g.same_session(prev);
            if session_changed {
                s.presence = PresenceState::default();
                s.have_presence = false;
                s.presence_connected = false;
            }
            if g.running && !g.same_session(prev) {
                s.run_terminal = false;
            }
            let mut run_changed = false;
            if !g.running {
                run_changed = end_run_locked(&mut s, Utc::now(), "the game closed");
                s.run_terminal = false;
                s.prev_snap = Snapshot::default();
                s.have_prev = false;
                s.snapshot = Snapshot::default();
                s.have_snap = false;
            } else if prev.running && !g.same_session(prev) {
                run_changed = end_run_locked(&mut s, Utc::now(), "the game relaunched");
                s.prev_snap = Snapshot::default();
                s.have_prev = false;
                s.snapshot = Snapshot::default();
                s.have_snap = false;
            } else if let Some(seed) = s.run_seed.take() {
                if seed.matches(g) {
                    s.run_start = Some(seed.started_at);
                    run_changed = true;
                    let ago = Utc::now().signed_duration_since(seed.started_at);
                    eprintln!(
                        "session: resuming the run started {} ago",
                        format::ago(ago.to_std().unwrap_or_default())
                    );
                }
            }
            (run_changed, s.on_run_change.clone())
        };
        fire_run_change(on_change, run_changed);
    }

    pub fn set_visibility(&self, v: crate::model::Visibility) {
        self.inner.lock().unwrap().visibility = v;
    }

    pub fn visibility(&self) -> crate::model::Visibility {
        self.inner.lock().unwrap().visibility.clone()
    }

    pub fn set_presence(&self, p: PresenceState) {
        let (run_changed, on_change) = {
            let mut s = self.inner.lock().unwrap();
            if !s.game.running || s.game.started_at.map(|t| p.at < t).unwrap_or(false) {
                return;
            }
            s.presence_connected = true;
            s.presence = p.clone();
            s.have_presence = true;
            (
                update_run_from_presence_locked(&mut s, &p),
                s.on_run_change.clone(),
            )
        };
        fire_run_change(on_change, run_changed);
    }

    pub fn set_presence_connected(&self, connected: bool) {
        let mut s = self.inner.lock().unwrap();
        s.presence_connected = connected;
        if !connected {
            s.have_presence = false;
        }
    }

    pub fn presence_connected(&self) -> bool {
        self.inner.lock().unwrap().presence_connected
    }

    pub fn set_credentials_at(&self, t: DateTime<Utc>) {
        self.inner.lock().unwrap().creds_at = Some(t);
    }

    pub fn set_catalog(&self, c: Catalog) {
        self.inner.lock().unwrap().catalog = Some(c);
    }

    pub fn set_xp_window(
        &self,
        samples: impl Fn() -> Vec<XpSample> + Send + Sync + 'static,
        min_samples: i32,
    ) {
        let mut s = self.inner.lock().unwrap();
        s.xp_samples = Some(Arc::new(samples));
        s.xp_min_samples = min_samples;
    }

    pub fn set_challenges(&self, board: Vec<Challenge>) {
        let mut s = self.inner.lock().unwrap();
        s.board = board;
        s.have_board = true;
    }

    pub fn set_challenge_status(&self, reason: String) {
        self.inner.lock().unwrap().board_status = reason;
    }

    pub fn set_boss_map(&self, m: BossMap) {
        self.inner.lock().unwrap().boss_map = Some(m);
    }

    pub fn set_poller_status(&self, st: PollerStatus) {
        self.inner.lock().unwrap().poller = st;
    }

    pub fn snapshot(&self) -> Option<Snapshot> {
        let s = self.inner.lock().unwrap();
        if s.have_snap {
            Some(s.snapshot.clone())
        } else {
            None
        }
    }

    #[cfg(test)]
    pub fn missed_ticks(&self) -> i32 {
        self.inner.lock().unwrap().missed_ticks
    }

    pub fn previous_snapshot(&self) -> Option<Snapshot> {
        let s = self.inner.lock().unwrap();
        if s.have_prev {
            Some(s.prev_snap.clone())
        } else {
            None
        }
    }

    pub fn run(&self) -> (Option<DateTime<Utc>>, GameState) {
        let s = self.inner.lock().unwrap();
        (s.run_start, s.game)
    }

    pub fn set_run_seed(&self, run: Option<RunState>) {
        self.inner.lock().unwrap().run_seed = run;
    }

    pub fn set_on_run_change(&self, f: impl Fn() + Send + Sync + 'static) {
        self.inner.lock().unwrap().on_run_change = Some(Arc::new(f));
    }

    pub fn restart_run(&self, at: DateTime<Utc>) -> bool {
        let (ok, on_change) = {
            let mut s = self.inner.lock().unwrap();
            if !s.game.running || s.run_terminal {
                return false;
            }
            s.run_start = Some(at);
            s.run_seed = None;
            (true, s.on_run_change.clone())
        };
        fire_run_change(on_change, ok);
        ok
    }

    pub fn client_in_world(&self, now: DateTime<Utc>) -> bool {
        let s = self.inner.lock().unwrap();
        match presence_position_locked(&s, now) {
            Some(p) => !p.loading,
            None => true,
        }
    }

    pub fn effective_position(&self, now: DateTime<Utc>) -> Option<(i32, i32)> {
        let s = self.inner.lock().unwrap();
        if let Some(p) = presence_position_locked(&s, now) {
            if p.has_position {
                return Some((p.x, p.y));
            }
            if p.in_outpost || p.loading {
                return None;
            }
        }
        if s.have_snap && s.snapshot.has_position {
            return Some((s.snapshot.position_x, s.snapshot.position_y));
        }
        None
    }

    pub fn derive(&self, now: DateTime<Utc>) -> View {
        let s = self.inner.lock().unwrap();
        let mut v = View {
            now,
            game_running: s.game.running,
            client_loading: s.game.running && s.presence_connected && !s.have_presence,
            client_uptime: Ns::from_std(s.game.elapsed(now)),
            ..View::default()
        };
        if s.game.running {
            if let Some(start) = s.run_start {
                v.has_session = true;
                if now > start {
                    v.session_time = Ns::from_chrono(now - start);
                }
            }
        }
        if s.have_snap {
            let snap = &s.snapshot;
            v.have_data = true;
            v.has_position = snap.has_position;
            v.position_x = snap.position_x;
            v.position_y = snap.position_y;
            v.position_z = snap.position_z;
            v.zone_name = citymap::trade_zone_name(snap.trade_zone).to_string();
            v.in_outpost = snap.in_outpost;
            v.outpost_name = citymap::outpost_name(snap.position_x, snap.position_y).to_string();
            v.block_support = Ns::from_std(snap.block_support.remaining(now));
        }
        if let Some(p) = presence_position_locked(&s, now) {
            v.client_loading = p.loading;
            if p.has_position {
                v.has_position = true;
                v.position_x = p.x;
                v.position_y = p.y;
                v.in_outpost = false;
                v.outpost_name.clear();
            } else if p.in_outpost {
                // Presence publishes the name only. Each outpost is a fixed
                // tile, so the ring can move now instead of waiting for the poll.
                v.in_outpost = true;
                v.outpost_name = p.outpost_name.clone();
                if let Some((x, y)) = citymap::outpost_coords(&p.outpost_name) {
                    v.has_position = true;
                    v.position_x = x;
                    v.position_y = y;
                }
            }
        }
        if let Some(boss) = &s.boss_map {
            v.outpost_attack = boss.outpost_attack;
            if v.has_position {
                let events = boss.at(v.position_x, v.position_y, now);
                if !events.is_empty() {
                    v.block_events = Some(events);
                }
                if v.position_x == bossmap::ONSLAUGHT_COORD
                    && v.position_y == bossmap::ONSLAUGHT_COORD
                {
                    let past = boss.at_ended(v.position_x, v.position_y, now);
                    if !past.is_empty() {
                        v.block_events_past = Some(past);
                    }
                    let upcoming = boss.at_upcoming(v.position_x, v.position_y, now);
                    if !upcoming.is_empty() {
                        v.block_events_upcoming = Some(upcoming);
                    }
                }
            }
            let mut from = [0; 2];
            let mut dist = Vec::new();
            if v.has_position && citymap::default().is_block(v.position_x, v.position_y) {
                from = [v.position_x, v.position_y];
                dist = citymap::default().walk_distances(v.position_x, v.position_y);
            }
            let marks = boss.active_marks(now, from, &dist);
            if !marks.is_empty() {
                v.city_marks = Some(marks.clone());
            }
            let block_empty = v
                .block_events
                .as_ref()
                .map(|e| e.is_empty())
                .unwrap_or(true);
            if v.has_position && block_empty && !v.client_loading {
                if let Some(m) = bossmap::nearest_mark(&marks) {
                    v.has_nearest = true;
                    v.nearest_dx = m.walk.dx;
                    v.nearest_dy = m.walk.dy;
                    v.nearest_x = m.x;
                    v.nearest_y = m.y;
                    v.nearest_distance_in_blocks = m.walk.blocks;
                    v.nearest_detour = m.walk.detour;
                }
            }
        }
        if s.have_board {
            v.challenges = Some(s.board.clone());
        }
        v.challenge_status = s.board_status.clone();
        if let Some(get) = &s.xp_samples {
            let samples = get();
            let rate = xp::compute_rate(&samples, s.xp_min_samples, stability_locked(&s));
            apply_rate(&mut v, rate);
        }
        if s.poller.stale {
            v.status = "session expired - open any Dead Frontier page to refresh".into();
            v.status_is_fix = true;
        } else if s.creds_at.is_none() {
            v.status = "waiting for the bridge script".into();
            v.status_is_fix = true;
        } else if s.poller.paused && !s.poller.pause_reason.is_empty() {
            v.status = s.poller.pause_reason.clone();
        } else if s.poller.failures > 0 {
            v.status = "server not responding (retrying)".into();
        } else if !s.have_snap {
            v.status = "waiting for the first poll".into();
        }
        v
    }
}

fn apply_rate(v: &mut View, rate: XpRate) {
    v.xp_available = rate.available;
    v.xp_provisional = rate.provisional;
    v.xp_per_hour = rate.per_hour;
    v.xp_stability = rate.stability;
    // Window total is the per-hour numerator. Go's View has no Gained field;
    // the HUD shows the rate, tests assert the total.
    let _ = rate.gained;
    let _ = rate.why;
    let _ = rate.span;
    let _ = rate.samples;
}

fn stability_locked(s: &Inner) -> XpStability {
    match s.missed_ticks {
        0 => XpStability::Steady,
        1 => XpStability::Shaky,
        _ => XpStability::Unstable,
    }
}

fn fire_run_change(on_change: Option<Arc<dyn Fn() + Send + Sync>>, changed: bool) {
    if changed {
        if let Some(f) = on_change {
            f();
        }
    }
}

fn end_run_locked(s: &mut Inner, at: DateTime<Utc>, why: &str) -> bool {
    let Some(start) = s.run_start.take() else {
        return false;
    };
    if at > start {
        eprintln!(
            "session: run ended after {} ({why})",
            format::ago((at - start).to_std().unwrap_or_default())
        );
    }
    true
}

fn presence_position_locked(s: &Inner, now: DateTime<Utc>) -> Option<PresenceState> {
    if !s.presence_connected || !s.have_presence || !s.game.running {
        return None;
    }
    if now
        .signed_duration_since(s.presence.at)
        .to_std()
        .unwrap_or(Duration::MAX)
        > PRESENCE_MAX_AGE
    {
        return None;
    }
    Some(s.presence.clone())
}

fn update_run_from_presence_locked(s: &mut Inner, p: &PresenceState) -> bool {
    if s.run_seed.is_some() || s.run_terminal {
        return false;
    }
    if (p.has_position || p.in_outpost) && s.run_start.is_none() {
        s.run_start = Some(p.at);
        let kind = if p.has_position {
            "a city position"
        } else {
            "an outpost"
        };
        eprintln!("session: run started (the client reported {kind})");
        return true;
    }
    false
}

fn update_run_locked(s: &mut Inner, snap: &Snapshot) -> bool {
    let moved = s.have_prev
        && s.prev_snap.has_position
        && snap.has_position
        && (s.prev_snap.position_x != snap.position_x
            || s.prev_snap.position_y != snap.position_y
            || s.prev_snap.position_z != snap.position_z);
    let left_outpost = s.have_prev && s.prev_snap.in_outpost && !snap.in_outpost;
    if snap.dead {
        let changed = end_run_locked(s, snap.at, "you died");
        s.run_terminal = true;
        return changed;
    }
    if s.run_terminal {
        return false;
    }
    if presence_position_locked(s, snap.at).is_some_and(|p| p.loading) {
        return false;
    }
    if s.run_start.is_none() && (left_outpost || moved) {
        s.run_start = Some(snap.at);
        let why = if left_outpost {
            "left the outpost".to_string()
        } else {
            format!("moved to {}, {}", snap.position_x, snap.position_y)
        };
        eprintln!("session: run started ({why})");
        return true;
    }
    false
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::game::presence;
    use crate::net::dfclient::parse_flash;
    use chrono::TimeZone;
    use std::path::Path;

    fn itoa64(n: i64) -> String {
        n.to_string()
    }

    fn load_fixture_catalog() -> Catalog {
        let raw = std::fs::read_to_string(
            Path::new(env!("CARGO_MANIFEST_DIR")).join("testdata/allstats.txt"),
        )
        .unwrap();
        let vars = parse_flash(&raw).unwrap();
        crate::data::catalog::parse(&vars, Utc::now()).unwrap()
    }

    fn sample_player_record() -> HashMap<String, String> {
        HashMap::from([
            ("df_level".into(), "415".into()),
            ("df_exp".into(), "10000".into()),
            ("df_exptotal".into(), "10000000".into()),
            ("df_freepoints".into(), "0".into()),
            ("df_positionx".into(), "1058".into()),
            ("df_positiony".into(), "1019".into()),
            ("df_positionz".into(), "0".into()),
            ("df_tradezone".into(), "21".into()),
            ("df_inoutpost".into(), "1".into()),
            ("df_dangerlevel".into(), "0".into()),
            ("df_block_support_until".into(), "0".into()),
            ("df_hpcurrent".into(), "100".into()),
            ("df_hpmax".into(), "100".into()),
            ("df_cash".into(), "1000".into()),
            ("df_bankcash".into(), "50000".into()),
            ("df_hungerhp".into(), "50".into()),
            ("df_dead".into(), "0".into()),
        ])
    }

    #[test]
    fn parse_snapshot_prefers_exp_total() {
        let c = load_fixture_catalog();
        let snap = parse_snapshot(&sample_player_record(), Utc::now(), Some(&c));
        assert_eq!(snap.xp_source, XpSource::ExpTotal);
        assert_eq!(snap.cumulative_xp, 10_000_000);
    }

    #[test]
    fn parse_snapshot_falls_back_to_the_table() {
        let c = load_fixture_catalog();
        let mut vars = sample_player_record();
        vars.remove("df_exptotal");
        vars.insert("df_level".into(), "200".into());
        vars.insert("df_exp".into(), "1000".into());
        let snap = parse_snapshot(&vars, Utc::now(), Some(&c));
        assert_eq!(snap.xp_source, XpSource::Table);
        assert_eq!(snap.cumulative_xp, c.cumulative_xp(200, 1000).unwrap());
        let snap = parse_snapshot(&vars, Utc::now(), None);
        assert_eq!(snap.xp_source, XpSource::None);
        assert_eq!(snap.cumulative_xp, 0);
    }

    #[test]
    fn parse_snapshot_at_the_level_cap() {
        let c = load_fixture_catalog();
        let snap = parse_snapshot(&sample_player_record(), Utc::now(), Some(&c));
        assert_eq!(snap.level, 415);
        assert_eq!(snap.exp_needed, 0);
        assert_eq!(snap.pending_levels, 0);
        assert!(snap.exp_in_level > 176_000_000);
    }

    #[test]
    fn parse_snapshot_pending_levels() {
        let c = load_fixture_catalog();
        let mut vars = sample_player_record();
        vars.remove("df_exptotal");
        vars.insert("df_level".into(), "200".into());
        let needed = c.exp_needed(200).unwrap();
        vars.insert("df_exp".into(), itoa64(needed * 40));
        let snap = parse_snapshot(&vars, Utc::now(), Some(&c));
        assert!(snap.pending_levels >= 5);
        let mut level = 200;
        let mut exp = needed * 40;
        let mut want = 0;
        loop {
            let Some(need) = c.exp_needed(level) else {
                break;
            };
            if exp < need {
                break;
            }
            exp -= need;
            level += 1;
            want += 1;
        }
        assert_eq!(snap.pending_levels, want);
    }

    #[test]
    fn parse_snapshot_positions_and_place() {
        let snap = parse_snapshot(&sample_player_record(), Utc::now(), None);
        assert!(snap.has_position);
        assert_eq!((snap.position_x, snap.position_y), (1058, 1019));
        assert!(snap.in_outpost);
        assert_eq!(
            citymap::outpost_name(snap.position_x, snap.position_y),
            "Ground Zero"
        );
    }

    #[test]
    fn parse_snapshot_distinguishes_absent_from_zero() {
        let mut vars = sample_player_record();
        let snap = parse_snapshot(&vars, Utc::now(), None);
        assert!(snap.has_danger && snap.danger_level == 0);
        vars.remove("df_dangerlevel");
        let snap = parse_snapshot(&vars, Utc::now(), None);
        assert!(!snap.has_danger);
        vars.remove("df_positionx");
        let snap = parse_snapshot(&vars, Utc::now(), None);
        assert!(!snap.has_position);
    }

    #[test]
    fn parse_snapshot_survives_garbage() {
        let vars = HashMap::from([
            ("df_level".into(), "not a number".into()),
            ("df_exp".into(), "".into()),
            ("df_positionx".into(), "1058".into()),
            ("df_positiony".into(), "abc".into()),
            ("df_dangerlevel".into(), "3".into()),
            ("df_cash".into(), "1000".into()),
        ]);
        let snap = parse_snapshot(&vars, Utc::now(), None);
        assert_eq!(snap.level, 0);
        assert_eq!(snap.exp_in_level, 0);
        assert!(!snap.has_position);
        assert_eq!(snap.cash, 1000);
        assert_eq!(snap.danger_level, 3);
    }

    #[test]
    fn parse_snapshot_decodes_game_timestamps() {
        let now = DateTime::from_timestamp(1_786_484_051, 0).unwrap();
        let target = now + chrono::Duration::seconds(90);
        let snap = parse_snapshot(
            &HashMap::from([("df_boostexpuntil".into(), itoa64(target.timestamp()))]),
            now,
            None,
        );
        assert_eq!(snap.boost_exp.at, Some(target));
        assert!(!snap.boost_exp.forever);
        assert_eq!(snap.boost_exp.remaining(now), Duration::from_secs(90));

        let snap = parse_snapshot(
            &HashMap::from([("df_servertime".into(), "586484051".into())]),
            now,
            None,
        );
        assert_eq!(snap.server_time.unwrap().timestamp(), 1_786_484_051);

        let snap = parse_snapshot(
            &HashMap::from([("df_boostexpuntil".into(), "0".into())]),
            now,
            None,
        );
        assert!(!snap.boost_exp.set());
    }

    #[test]
    fn parse_snapshot_forever_sentinel() {
        let now = DateTime::from_timestamp(1_786_484_051, 0).unwrap();
        for raw in ["2147483647", "2147483646"] {
            let snap = parse_snapshot(
                &HashMap::from([("df_boostexpuntil".into(), raw.into())]),
                now,
                None,
            );
            assert!(snap.boost_exp.forever, "{raw}");
            assert_eq!(snap.boost_exp.remaining(now), Duration::ZERO);
            assert!(snap.boost_exp.set());
        }
    }

    #[test]
    fn parse_snapshot_rejects_implausible_deadlines() {
        let now = DateTime::from_timestamp(1_786_484_051, 0).unwrap();
        let far = itoa64((now + chrono::Duration::days(5 * 365)).timestamp());
        for raw in ["586484051", "1000000000", far.as_str()] {
            let snap = parse_snapshot(
                &HashMap::from([("df_block_support_until".into(), raw.to_string())]),
                now,
                None,
            );
            assert!(!snap.block_support.set(), "{raw}");
        }
    }

    #[test]
    fn store_apply_tick_and_derive() {
        let s = Store::new(Some(load_fixture_catalog()));
        let now = Utc::now();
        let v = s.derive(now);
        assert!(!v.have_data);
        assert!(!v.status.is_empty());

        assert!(s.apply_tick(Tick {
            at: now,
            vars: sample_player_record(),
            err: None,
            scheduled: true,
        }));
        s.set_credentials_at(now);
        s.set_game(GameState {
            running: true,
            pid: 1,
            started_at: Some(now - chrono::Duration::minutes(42)),
        });
        let v = s.derive(now + chrono::Duration::seconds(3));
        assert!(v.have_data);
        assert_eq!(
            v.client_uptime,
            Ns::from_std(Duration::from_secs(42 * 60 + 3))
        );
        assert!(!v.has_session);
        let snap = s.snapshot().unwrap();
        assert_eq!(snap.at, now);
        assert_eq!(snap.level, 415);
        assert_eq!(snap.cumulative_xp, 10_000_000);
        assert_eq!(v.outpost_name, "Ground Zero");
        assert_eq!(v.zone_name, "Outpost");
        assert_eq!(v.status, "");
    }

    #[test]
    fn store_counts_missed_ticks() {
        let s = Store::new(None);
        let now = Utc::now();
        s.set_credentials_at(now);
        s.apply_tick(Tick {
            at: now,
            vars: sample_player_record(),
            err: None,
            scheduled: true,
        });
        for i in 1..=3 {
            assert!(!s.apply_tick(Tick {
                at: now,
                vars: HashMap::new(),
                err: Some("boom".into()),
                scheduled: true,
            }));
            assert_eq!(s.missed_ticks(), i);
        }
        s.apply_tick(Tick {
            at: now,
            vars: HashMap::new(),
            err: Some("boom".into()),
            scheduled: false,
        });
        assert_eq!(s.missed_ticks(), 3);
        s.apply_tick(Tick {
            at: now,
            vars: sample_player_record(),
            err: None,
            scheduled: true,
        });
        assert_eq!(s.missed_ticks(), 0);
        assert!(s.derive(now).have_data);
    }

    #[test]
    fn store_status_priority() {
        let now = Utc::now();
        let s = Store::new(None);
        assert!(s.derive(now).status.contains("bridge"));
        s.set_credentials_at(now);
        s.set_poller_status(PollerStatus {
            failures: 2,
            ..PollerStatus::default()
        });
        assert!(s.derive(now).status.contains("not responding"));
        s.set_poller_status(PollerStatus {
            stale: true,
            paused: true,
            pause_reason: "whatever".into(),
            failures: 9,
            ..PollerStatus::default()
        });
        let v = s.derive(now);
        assert!(v.status.contains("session expired"));
        assert!(v.status_is_fix);
    }

    fn running_store(at: DateTime<Utc>) -> Store {
        let s = Store::new(None);
        s.set_game(GameState {
            running: true,
            pid: 1,
            started_at: Some(at - chrono::Duration::minutes(1)),
        });
        s
    }

    fn polled_position(s: &Store, at: DateTime<Utc>) {
        s.apply_tick(Tick {
            at,
            vars: HashMap::from([
                ("df_positionx".into(), "1054".into()),
                ("df_positiony".into(), "987".into()),
                ("df_level".into(), "415".into()),
            ]),
            err: None,
            scheduled: false,
        });
    }

    #[test]
    fn store_prefers_fresh_presence_position() {
        let now = DateTime::from_timestamp(1000, 0).unwrap();
        let s = running_store(now);
        polled_position(&s, now);
        s.set_presence(presence::parse_details("Inner City 1055 x 985", now));
        let view = s.derive(now);
        assert_eq!((view.position_x, view.position_y), (1055, 985));
        let view = s.derive(
            now + chrono::Duration::from_std(PRESENCE_MAX_AGE).unwrap()
                + chrono::Duration::seconds(1),
        );
        assert_eq!((view.position_x, view.position_y), (1054, 987));
    }

    #[test]
    fn store_rejects_presence_while_game_closed() {
        let now = DateTime::from_timestamp(1000, 0).unwrap();
        let s = Store::new(None);
        s.set_presence(presence::parse_details("Inner City 1055 x 985", now));
        polled_position(&s, now);
        let view = s.derive(now);
        assert_eq!((view.position_x, view.position_y), (1054, 987));
    }

    #[test]
    fn block_events_follow_presence_position() {
        let now = DateTime::from_timestamp(1000, 0).unwrap();
        let s = running_store(now);
        let raw = br#"{
	  "0":{"event_id":"1","isoa":"0","locations":[["1055","985"]],"started":"1","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Titan","special_enemy_amount":"1","boss_num":"17",
	       "event_type":"","dfp_objectives":[],"start_time":"900","end_time":"5000"},
	  "bosshash":"abc","servertime":1000,"version":"1"}"#;
        s.set_boss_map(bossmap::parse(raw, now).unwrap());
        polled_position(&s, now);
        assert!(
            s.derive(now).block_events.is_none()
                || s.derive(now).block_events.as_ref().unwrap().is_empty()
        );
        s.set_presence(presence::parse_details("Inner City 1055 x 985", now));
        let events = s.derive(now).block_events.unwrap();
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].enemies[0], "1 x Titan");
    }

    #[test]
    fn presence_loading_keeps_polled_position() {
        let now = DateTime::from_timestamp(1000, 0).unwrap();
        let s = running_store(now);
        polled_position(&s, now);
        s.set_presence(presence::parse_details("Loading...", now));
        let view = s.derive(now);
        assert_eq!((view.position_x, view.position_y), (1054, 987));
        assert!(view.client_loading);
    }

    #[test]
    fn disconnect_falls_back_to_polled_position() {
        let now = DateTime::from_timestamp(1000, 0).unwrap();
        let s = running_store(now);
        polled_position(&s, now);
        s.set_presence(presence::parse_details("Inner City 1055 x 985", now));
        assert_eq!(
            (s.derive(now).position_x, s.derive(now).position_y),
            (1055, 985)
        );
        assert_eq!(s.effective_position(now), Some((1055, 985)));
        s.set_presence_connected(false);
        assert!(!s.presence_connected());
        let view = s.derive(now);
        assert_eq!((view.position_x, view.position_y), (1054, 987));
        assert_eq!(s.effective_position(now), Some((1054, 987)));
    }

    #[test]
    fn stale_loading_presence_does_not_block_run_start() {
        let now = DateTime::from_timestamp(1000, 0).unwrap();
        let s = running_store(now);
        polled_position(&s, now);
        s.set_presence(presence::parse_details("Loading...", now));
        let later = now
            + chrono::Duration::from_std(PRESENCE_MAX_AGE).unwrap()
            + chrono::Duration::seconds(1);
        s.apply_tick(Tick {
            at: later,
            vars: HashMap::from([
                ("df_positionx".into(), "1055".into()),
                ("df_positiony".into(), "987".into()),
                ("df_level".into(), "415".into()),
            ]),
            err: None,
            scheduled: false,
        });
        let view = s.derive(later);
        assert!(view.has_session);
        assert_eq!((view.position_x, view.position_y), (1055, 987));
    }

    #[test]
    fn presence_outpost_snaps_to_table_tile() {
        let now = DateTime::from_timestamp(1000, 0).unwrap();
        let s = running_store(now);
        s.apply_tick(Tick {
            at: now,
            vars: HashMap::from([
                ("df_positionx".into(), "1054".into()),
                ("df_positiony".into(), "986".into()),
                ("df_level".into(), "415".into()),
            ]),
            err: None,
            scheduled: false,
        });
        for o in citymap::outposts() {
            s.set_presence(presence::parse_details(o.name, now));
            let view = s.derive(now);
            assert_eq!((view.position_x, view.position_y), (o.x, o.y), "{}", o.name);
            assert!(view.has_position, "{}", o.name);
            assert!(view.in_outpost, "{}", o.name);
            assert_eq!(view.outpost_name, o.name);
        }
    }

    #[test]
    fn new_game_session_clears_presence_connection() {
        let now = DateTime::from_timestamp(1000, 0).unwrap();
        let s = running_store(now);
        s.set_presence_connected(true);
        assert!(s.presence_connected());
        s.set_game(GameState {
            running: true,
            pid: 2,
            started_at: Some(now),
        });
        assert!(!s.presence_connected());
    }

    #[test]
    fn trade_zone_names() {
        for (zone, want) in [
            (1, "North Western"),
            (5, "Central"),
            (9, "South Eastern"),
            (10, "Wastelands"),
            (21, "Outpost"),
            (22, "Valcrest"),
        ] {
            assert_eq!(citymap::trade_zone_name(zone), want);
        }
        assert_eq!(citymap::trade_zone_name(99), "");
        assert_eq!(citymap::trade_zone_short(3), "NE");
    }

    #[test]
    fn outpost_names() {
        for (x, y, want) in [
            (1000, 1000, "Nastya's Holdout"),
            (1005, 985, "Dogg's Stockade"),
            (1012, 1019, "Precinct 13"),
            (1029, 1003, "Fort Pastor"),
            (1054, 987, "Secronom Bunker"),
            (1032, 985, "Valcrest"),
            (1058, 1019, "Ground Zero"),
        ] {
            assert_eq!(citymap::outpost_name(x, y), want);
        }
        assert_eq!(citymap::outpost_name(1040, 1000), "");
    }

    #[test]
    fn run_seed_matches_only_same_game() {
        let launch = Utc::now() - chrono::Duration::hours(2);
        let game = GameState {
            running: true,
            pid: 4242,
            started_at: Some(launch),
        };
        let seed = RunState {
            started_at: Utc::now() - chrono::Duration::minutes(40),
            game_pid: game.pid,
            game_started_at: game.started_at,
        };
        let s = Store::new(None);
        s.set_run_seed(Some(seed.clone()));
        s.set_game(game);
        let view = s.derive(Utc::now());
        assert!(view.has_session);
        assert!(view.session_time.std() >= Duration::from_secs(39 * 60));

        let other = Store::new(None);
        other.set_run_seed(Some(seed.clone()));
        other.set_game(GameState {
            running: true,
            pid: game.pid,
            started_at: Some(Utc::now()),
        });
        assert!(!other.derive(Utc::now()).has_session);
        assert!(!seed.matches(GameState::default()));
        assert!(!RunState::default().matches(game));
    }

    #[test]
    fn run_change_callback_fires_only_on_boundaries() {
        let s = Store::new(None);
        let changes = Arc::new(std::sync::atomic::AtomicU32::new(0));
        let count = changes.clone();
        s.set_on_run_change(move || {
            count.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        });
        let now = Utc::now();
        s.set_game(GameState {
            running: true,
            pid: 4242,
            started_at: Some(now - chrono::Duration::minutes(1)),
        });
        s.set_presence(PresenceState {
            at: now,
            has_position: true,
            x: 1054,
            y: 986,
            ..PresenceState::default()
        });
        s.set_presence(PresenceState {
            at: now + chrono::Duration::seconds(1),
            has_position: true,
            x: 1054,
            y: 986,
            ..PresenceState::default()
        });
        assert_eq!(changes.load(std::sync::atomic::Ordering::SeqCst), 1);
        s.set_presence(PresenceState {
            at: now + chrono::Duration::seconds(2),
            in_outpost: true,
            outpost_name: "Secronom Bunker".into(),
            ..PresenceState::default()
        });
        assert_eq!(changes.load(std::sync::atomic::Ordering::SeqCst), 1);
        s.set_game(GameState::default());
        assert_eq!(changes.load(std::sync::atomic::Ordering::SeqCst), 2);
    }

    fn xp_samples(
        start: DateTime<Utc>,
        n: usize,
        spacing: chrono::Duration,
        base: i64,
        step: i64,
    ) -> Vec<XpSample> {
        (0..n)
            .map(|i| XpSample {
                at: start + spacing * i as i32,
                cumulative: base + i as i64 * step,
                source: "df_exptotal".into(),
            })
            .collect()
    }

    #[test]
    fn derive_includes_rate() {
        let s = Store::new(None);
        let start = Utc.timestamp_opt(1_786_484_051, 0).unwrap();
        let samples = xp_samples(start, 4, chrono::Duration::seconds(10), 1_000_000, 1000);
        s.set_xp_window(move || samples.clone(), 3);
        s.set_credentials_at(start);
        s.apply_tick(Tick {
            at: start,
            vars: sample_player_record(),
            err: None,
            scheduled: true,
        });
        let view = s.derive(start + chrono::Duration::seconds(1));
        assert!(view.xp_available);
        assert_eq!(view.xp_per_hour, 360_000.0);
        assert_eq!(view.xp_stability, XpStability::Steady);

        let bare = Store::new(None);
        bare.apply_tick(Tick {
            at: start,
            vars: sample_player_record(),
            err: None,
            scheduled: true,
        });
        assert!(!bare.derive(start).xp_available);
    }

    #[test]
    fn derive_counts_remembered_challenges() {
        let s = Store::new(None);
        s.set_challenges(vec![Challenge {
            name: "Travel".into(),
            remembered: true,
            objectives: vec![crate::model::Objective {
                target: 800,
                score: 500,
                has_score: true,
                ..crate::model::Objective::default()
            }],
            ..Challenge::default()
        }]);
        let view = s.derive(Utc::now());
        let board = view.challenges.as_deref().unwrap();
        assert_eq!(board.len(), 1);
        assert_eq!(board.iter().filter(|c| c.complete()).count(), 1);
    }
}
