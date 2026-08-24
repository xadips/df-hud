//! Domain types for the derived view.

use chrono::{DateTime, SecondsFormat, Utc};
use serde::ser::{SerializeStruct, Serializer};
use serde::{Deserialize, Serialize};
use std::time::Duration;

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Ns(pub i64);

impl Ns {
    pub fn from_std(d: Duration) -> Self {
        Self(i64::try_from(d.as_nanos()).unwrap_or(i64::MAX))
    }

    pub fn from_chrono(d: chrono::Duration) -> Self {
        Self(d.num_nanoseconds().unwrap_or(0))
    }

    pub fn std(self) -> Duration {
        Duration::from_nanos(self.0.max(0) as u64)
    }
}

impl Serialize for Ns {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        s.serialize_u64(self.std().as_secs())
    }
}

fn rfc3339_secs<S: Serializer>(t: &DateTime<Utc>, s: S) -> Result<S::Ok, S::Error> {
    s.serialize_str(&t.to_rfc3339_opts(SecondsFormat::Secs, true))
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum XpSource {
    #[default]
    None,
    ExpTotal,
    Table,
}

impl XpSource {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::None => "unavailable",
            Self::ExpTotal => "df_exptotal",
            Self::Table => "exp table reconstruction",
        }
    }
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Deadline {
    pub at: Option<DateTime<Utc>>,
    pub forever: bool,
}

impl Deadline {
    #[cfg(test)]
    pub fn set(self) -> bool {
        self.forever || self.at.is_some()
    }

    pub fn remaining(self, now: DateTime<Utc>) -> Duration {
        if self.forever {
            return Duration::ZERO;
        }
        let Some(at) = self.at else {
            return Duration::ZERO;
        };
        if at > now {
            (at - now).to_std().unwrap_or(Duration::ZERO)
        } else {
            Duration::ZERO
        }
    }
}

#[derive(Clone, Debug, Default)]
pub struct Snapshot {
    pub at: DateTime<Utc>,
    pub level: i32,
    pub exp_in_level: i64,
    pub cumulative_xp: i64,
    pub xp_source: XpSource,
    pub exp_needed: i64,
    pub pending_levels: i32,
    pub free_points: i32,
    pub exp_since_start: i64,
    pub has_exp_since_start: bool,
    pub position_x: i32,
    pub position_y: i32,
    pub position_z: i32,
    pub has_position: bool,
    pub trade_zone: i32,
    pub in_outpost: bool,
    pub danger_level: i32,
    pub has_danger: bool,
    pub block_support: Deadline,
    pub hp: i32,
    pub hp_max: i32,
    pub cash: i64,
    pub has_cash: bool,
    pub bank_cash: i64,
    pub nourishment: i32,
    pub has_hunger: bool,
    /// Remaining XP boost. Copied onto View only when the overlay draws it.
    pub boost_exp: Deadline,
    pub session_3d: String,
    pub gold_member: bool,
    pub dead: bool,
    pub server_time: Option<DateTime<Utc>>,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct GameState {
    pub running: bool,
    pub pid: i32,
    pub started_at: Option<DateTime<Utc>>,
}

impl GameState {
    pub fn elapsed(self, now: DateTime<Utc>) -> Duration {
        if !self.running {
            return Duration::ZERO;
        }
        let Some(started) = self.started_at else {
            return Duration::ZERO;
        };
        if now > started {
            (now - started).to_std().unwrap_or(Duration::ZERO)
        } else {
            Duration::ZERO
        }
    }

    pub fn same_session(self, other: GameState) -> bool {
        self.running
            && other.running
            && self.pid == other.pid
            && self.started_at == other.started_at
    }
}

/// Published HUD visibility decision. Monitor travels with it because the
/// same compositor query answers both questions, and because moving the
/// surface to the game's monitor is only ever done at the moment it is shown.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Visibility {
    pub visible: bool,
    pub reason: String,
    pub monitor: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct PresenceState {
    pub at: DateTime<Utc>,
    pub has_position: bool,
    pub x: i32,
    pub y: i32,
    pub place: String,
    pub indoors: bool,
    pub in_outpost: bool,
    pub outpost_name: String,
    pub loading: bool,
    pub details: String,
}

#[derive(Clone, Debug)]
pub struct Tick {
    pub at: DateTime<Utc>,
    pub vars: std::collections::HashMap<String, String>,
    pub err: Option<String>,
    pub scheduled: bool,
}

#[derive(Clone, Debug, Default)]
pub struct PollerStatus {
    pub paused: bool,
    pub pause_reason: String,
    pub stale: bool,
    pub failures: i32,
    pub last_success: Option<DateTime<Utc>>,
    pub last_attempt: Option<DateTime<Utc>>,
    pub last_error: String,
    pub next_attempt: Option<DateTime<Utc>>,
    pub total_polls: i32,
    pub total_failure: i32,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct XpSample {
    pub at: DateTime<Utc>,
    pub cumulative: i64,
    pub source: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct RunState {
    pub started_at: DateTime<Utc>,
    pub game_pid: i32,
    pub game_started_at: Option<DateTime<Utc>>,
}

impl RunState {
    pub fn matches(&self, g: GameState) -> bool {
        g.running
            && self.game_pid == g.pid
            && self.game_started_at == g.started_at
            && self.started_at.timestamp() != 0
    }
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
#[repr(i32)]
pub enum XpStability {
    #[default]
    Steady = 0,
    Shaky = 1,
    Unstable = 2,
}

impl Serialize for XpStability {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        s.serialize_i32(*self as i32)
    }
}

#[derive(Clone, Debug, Default)]
pub struct XpRate {
    pub available: bool,
    pub per_hour: f64,
    pub gained: i64,
    pub span: Duration,
    pub samples: i32,
    pub stability: XpStability,
    pub provisional: bool,
    pub why: String,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
#[repr(i32)]
pub enum CityEventKind {
    #[default]
    Spawn = 0,
    Mission = 1,
    Qrf = 2,
    Unknown = 3,
}

impl Serialize for CityEventKind {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        s.serialize_i32(*self as i32)
    }
}

#[derive(Clone, Debug, Default)]
pub struct Objective {
    pub name: String,
    pub target: i64,
    pub score: i64,
    pub has_score: bool,
}

impl Objective {
    pub fn done(&self) -> bool {
        self.target > 0 && self.score >= self.target
    }
}

impl Serialize for Objective {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        let mut st = s.serialize_struct("Objective", 4)?;
        st.serialize_field("Name", &self.name)?;
        st.serialize_field("Target", &self.target)?;
        st.serialize_field("Score", &self.score)?;
        st.serialize_field("HasScore", &self.has_score)?;
        st.end()
    }
}

#[derive(Clone, Debug, Default)]
pub struct Challenge {
    pub index: i32,
    pub id: String,
    pub name: String,
    pub desc: String,
    pub clan: bool,
    pub start: DateTime<Utc>,
    pub end: DateTime<Utc>,
    pub objectives: Vec<Objective>,
    pub min_level: i32,
    pub max_level: i32,
    pub repeatable: bool,
    pub reward_exp: i64,
    pub reward_cash: i64,
    pub reward_credits: i64,
    pub reward_points: i64,
    pub reward_items: String,
    pub reward_special: String,
    /// Sticky completion for this cycle. Not in `--once` JSON (`Challenge`
    /// has no such field); `complete()` consults it so a clan-size target
    /// recompute cannot un-finish a challenge already seen done.
    pub remembered: bool,
}

impl Challenge {
    pub fn complete(&self) -> bool {
        self.remembered || self.live_complete()
    }

    pub(crate) fn live_complete(&self) -> bool {
        !self.objectives.is_empty() && self.objectives.iter().all(|o| o.done())
    }

    pub fn remaining(&self, now: DateTime<Utc>) -> Duration {
        if self.end.timestamp() <= 0 {
            return Duration::ZERO;
        }
        if self.end > now {
            (self.end - now).to_std().unwrap_or(Duration::ZERO)
        } else {
            Duration::ZERO
        }
    }

    pub fn eligible(&self, level: i32) -> bool {
        if self.clan || (self.min_level == 0 && self.max_level == 0) {
            return true;
        }
        if level >= self.min_level && level <= self.max_level {
            return true;
        }
        self.objectives.iter().any(|o| o.has_score)
    }

    pub fn progress(&self) -> (i64, i64) {
        self.objectives.iter().fold((0, 0), |(score, target), o| {
            (score + o.score, target + o.target)
        })
    }

    #[cfg(test)]
    pub fn started(&self) -> bool {
        self.objectives.iter().any(|o| o.score > 0)
    }
}

impl Serialize for Challenge {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        let mut st = s.serialize_struct("Challenge", 16)?;
        st.serialize_field("Index", &self.index)?;
        st.serialize_field("ID", &self.id)?;
        st.serialize_field("Name", &self.name)?;
        st.serialize_field("Desc", &self.desc)?;
        st.serialize_field("Clan", &self.clan)?;
        st.serialize_field(
            "Start",
            &self.start.to_rfc3339_opts(SecondsFormat::Secs, true),
        )?;
        st.serialize_field("End", &self.end.to_rfc3339_opts(SecondsFormat::Secs, true))?;
        st.serialize_field("Objectives", &self.objectives)?;
        st.serialize_field("MinLevel", &self.min_level)?;
        st.serialize_field("MaxLevel", &self.max_level)?;
        st.serialize_field("Repeatable", &self.repeatable)?;
        st.serialize_field("RewardExp", &self.reward_exp)?;
        st.serialize_field("RewardCash", &self.reward_cash)?;
        st.serialize_field("RewardCredits", &self.reward_credits)?;
        st.serialize_field("RewardPoints", &self.reward_points)?;
        st.serialize_field("RewardItems", &self.reward_items)?;
        st.serialize_field("RewardSpecial", &self.reward_special)?;
        st.end()
    }
}

#[derive(Clone, Debug, Default)]
pub struct CityEvent {
    pub id: String,
    pub kind: CityEventKind,
    pub event_type: String,
    pub title: String,
    pub enemies: Vec<String>,
    pub objectives: Vec<String>,
    pub reward_exp: i64,
    pub slot: i32,
    pub locations: Vec<[i32; 2]>,
    pub start: DateTime<Utc>,
    pub end: DateTime<Utc>,
    pub started: bool,
    pub ended: bool,
    pub onslaught: bool,
}

impl CityEvent {
    pub fn active_at(&self, now: DateTime<Utc>) -> bool {
        if self.start.timestamp() == 0 || self.end.timestamp() == 0 {
            return self.started && !self.ended;
        }
        now >= self.start && now < self.end
    }

    pub fn upcoming_at(&self, now: DateTime<Utc>) -> bool {
        if self.start.timestamp() == 0 {
            return !self.started && !self.ended;
        }
        now < self.start
    }

    pub fn ended_recently_at(&self, now: DateTime<Utc>, past: Duration) -> bool {
        if self.end.timestamp() == 0 {
            return self.ended;
        }
        now >= self.end && (now - self.end).to_std().unwrap_or(Duration::MAX) <= past
    }
}

impl Serialize for CityEvent {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        let mut st = s.serialize_struct("CityEvent", 14)?;
        st.serialize_field("ID", &self.id)?;
        st.serialize_field("Kind", &self.kind)?;
        st.serialize_field("EventType", &self.event_type)?;
        st.serialize_field("Title", &self.title)?;
        st.serialize_field("Enemies", &self.enemies)?;
        st.serialize_field("Objectives", &self.objectives)?;
        st.serialize_field("RewardExp", &self.reward_exp)?;
        st.serialize_field("Slot", &self.slot)?;
        st.serialize_field("Locations", &self.locations)?;
        st.serialize_field(
            "Start",
            &self.start.to_rfc3339_opts(SecondsFormat::Secs, true),
        )?;
        st.serialize_field("End", &self.end.to_rfc3339_opts(SecondsFormat::Secs, true))?;
        st.serialize_field("Started", &self.started)?;
        st.serialize_field("Ended", &self.ended)?;
        st.serialize_field("Onslaught", &self.onslaught)?;
        st.end()
    }
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Walk {
    pub blocks: i32,
    pub dx: i32,
    pub dy: i32,
    pub detour: i32,
}

impl Serialize for Walk {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        let mut st = s.serialize_struct("Walk", 4)?;
        st.serialize_field("Blocks", &self.blocks)?;
        st.serialize_field("DX", &self.dx)?;
        st.serialize_field("DY", &self.dy)?;
        st.serialize_field("Detour", &self.detour)?;
        st.end()
    }
}

#[derive(Clone, Debug, Default)]
pub struct CityMark {
    pub marker: String,
    pub label: String,
    pub enemies: Vec<String>,
    pub kind: CityEventKind,
    pub event_type: String,
    pub x: i32,
    pub y: i32,
    pub ends_in: Ns,
    pub off_map: bool,
    pub walk: Walk,
    pub reachable: bool,
}

impl Serialize for CityMark {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        let mut st = s.serialize_struct("CityMark", 10)?;
        st.serialize_field("Marker", &self.marker)?;
        st.serialize_field("Label", &self.label)?;
        st.serialize_field("Enemies", &self.enemies)?;
        st.serialize_field("Kind", &self.kind)?;
        st.serialize_field("X", &self.x)?;
        st.serialize_field("Y", &self.y)?;
        st.serialize_field("EndsIn", &self.ends_in)?;
        st.serialize_field("OffMap", &self.off_map)?;
        st.serialize_field("Walk", &self.walk)?;
        st.serialize_field("Reachable", &self.reachable)?;
        st.end()
    }
}

/// model.View. Not [`crate::overlay::scene::View`]. Overlay and tray fields only;
/// player record extras stay on [`Snapshot`].
#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "snake_case")]
pub struct View {
    #[serde(serialize_with = "rfc3339_secs")]
    pub now: DateTime<Utc>,
    pub have_data: bool,
    pub game_running: bool,
    pub client_loading: bool,
    pub has_session: bool,
    pub session_time: Ns,
    pub client_uptime: Ns,
    pub has_position: bool,
    pub position_x: i32,
    pub position_y: i32,
    pub position_z: i32,
    pub zone_name: String,
    pub in_outpost: bool,
    pub outpost_name: String,
    pub block_support: Ns,
    pub block_events: Option<Vec<CityEvent>>,
    pub block_events_past: Option<Vec<CityEvent>>,
    pub block_events_upcoming: Option<Vec<CityEvent>>,
    pub outpost_attack: bool,
    pub has_nearest: bool,
    pub nearest_dx: i32,
    pub nearest_dy: i32,
    pub nearest_x: i32,
    pub nearest_y: i32,
    pub nearest_distance_in_blocks: i32,
    pub nearest_detour: i32,
    pub city_marks: Option<Vec<CityMark>>,
    pub xp_per_hour: f64,
    pub xp_available: bool,
    pub xp_provisional: bool,
    pub xp_stability: XpStability,
    pub challenges: Option<Vec<Challenge>>,
    pub challenge_status: String,
    pub status: String,
    pub status_is_fix: bool,
}

impl Default for View {
    fn default() -> Self {
        Self {
            now: DateTime::<Utc>::UNIX_EPOCH,
            have_data: false,
            game_running: false,
            client_loading: false,
            has_session: false,
            session_time: Ns(0),
            client_uptime: Ns(0),
            has_position: false,
            position_x: 0,
            position_y: 0,
            position_z: 0,
            zone_name: String::new(),
            in_outpost: false,
            outpost_name: String::new(),
            block_support: Ns(0),
            block_events: None,
            block_events_past: None,
            block_events_upcoming: None,
            outpost_attack: false,
            has_nearest: false,
            nearest_dx: 0,
            nearest_dy: 0,
            nearest_x: 0,
            nearest_y: 0,
            nearest_distance_in_blocks: 0,
            nearest_detour: 0,
            city_marks: None,
            xp_per_hour: 0.0,
            xp_available: false,
            xp_provisional: false,
            xp_stability: XpStability::Steady,
            challenges: None,
            challenge_status: String::new(),
            status: String::new(),
            status_is_fix: false,
        }
    }
}

pub fn marshal_indent(view: &View) -> Result<String, serde_json::Error> {
    let mut out = serde_json::to_string_pretty(view)?;
    if !out.ends_with('\n') {
        out.push('\n');
    }
    Ok(out)
}
