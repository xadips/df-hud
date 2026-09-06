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
        Duration::from_nanos(self.0.max(0).cast_unsigned())
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

/// A game deadline. `Forever` is the server's "never expires" sentinel; it
/// counts as set but has no countdown.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Deadline {
    #[default]
    None,
    At(DateTime<Utc>),
    Forever,
}

impl Deadline {
    #[cfg(test)]
    pub fn set(self) -> bool {
        self != Self::None
    }

    pub fn remaining(self, now: DateTime<Utc>) -> Duration {
        match self {
            Self::At(at) if at > now => (at - now).to_std().unwrap_or(Duration::ZERO),
            _ => Duration::ZERO,
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
    pub exp_since_start: Option<i64>,
    /// `(x, y, z)` city coordinates.
    pub position: Option<(i32, i32, i32)>,
    pub trade_zone: i32,
    pub in_outpost: bool,
    pub danger_level: Option<i32>,
    pub block_support: Deadline,
    pub hp: i32,
    pub hp_max: i32,
    pub cash: Option<i64>,
    pub bank_cash: i64,
    pub nourishment: Option<i32>,
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
    /// `(x, y)` city block the client reports standing in.
    pub position: Option<(i32, i32)>,
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
    /// `None` while the window cannot yield a rate; `why` says why.
    pub per_hour: Option<f64>,
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
    /// `None` when the server sent no player score for this objective.
    pub score: Option<i64>,
}

impl Objective {
    pub fn done(&self) -> bool {
        self.target > 0 && self.score.unwrap_or(0) >= self.target
    }
}

/// Hand-written to keep the Go wire shape: `Score` is always present (`0`
/// when unknown) and `HasScore` says whether it was.
impl Serialize for Objective {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        let mut st = s.serialize_struct("Objective", 4)?;
        st.serialize_field("Name", &self.name)?;
        st.serialize_field("Target", &self.target)?;
        st.serialize_field("Score", &self.score.unwrap_or(0))?;
        st.serialize_field("HasScore", &self.score.is_some())?;
        st.end()
    }
}

#[derive(Clone, Debug, Default, Serialize)]
#[serde(rename_all = "PascalCase")]
pub struct Challenge {
    pub index: i32,
    #[serde(rename = "ID")]
    pub id: String,
    pub name: String,
    pub desc: String,
    pub clan: bool,
    #[serde(serialize_with = "rfc3339_secs")]
    pub start: DateTime<Utc>,
    #[serde(serialize_with = "rfc3339_secs")]
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
    /// Sticky completion for this cycle. Not in `--once` JSON; `complete()`
    /// consults it so a clan-size target recompute cannot un-finish a
    /// challenge already seen done.
    #[serde(skip)]
    pub remembered: bool,
}

impl Challenge {
    pub fn complete(&self) -> bool {
        self.remembered || self.live_complete()
    }

    pub(crate) fn live_complete(&self) -> bool {
        !self.objectives.is_empty() && self.objectives.iter().all(Objective::done)
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
        self.objectives.iter().any(|o| o.score.is_some())
    }

    pub fn progress(&self) -> (i64, i64) {
        self.objectives.iter().fold((0, 0), |(score, target), o| {
            (score + o.score.unwrap_or(0), target + o.target)
        })
    }

    #[cfg(test)]
    pub fn started(&self) -> bool {
        self.objectives.iter().any(|o| o.score.unwrap_or(0) > 0)
    }
}

#[derive(Clone, Debug, Default, Serialize)]
#[serde(rename_all = "PascalCase")]
pub struct MasteryBonus {
    pub name: String,
    /// Percent per level, absolute value.
    pub scale: f64,
    /// Percent cap. `0` means uncapped, which never counts as capped.
    pub max: f64,
    /// `scale * level`, clamped to `max`. What the game page shows as the boost.
    pub value: f64,
}

impl MasteryBonus {
    pub fn capped(&self) -> bool {
        self.max != 0.0 && self.value >= self.max
    }
}

#[derive(Clone, Debug, Default)]
pub struct Mastery {
    pub index: i32,
    pub name: String,
    pub desc: String,
    pub level: i32,
    /// Progress within the current level, not a lifetime total.
    pub exp: i64,
    /// What `exp` has to reach for the next level:
    /// `ceil(start_point * scale_factor^(level+1))`.
    pub next_exp: i64,
    pub bonuses: Vec<MasteryBonus>,
}

impl Mastery {
    /// Every bonus at its cap: levels keep climbing but the boost cannot grow.
    pub fn mastered(&self) -> bool {
        !self.bonuses.is_empty() && self.bonuses.iter().all(MasteryBonus::capped)
    }
}

/// Hand-written because `Mastered` is computed, not stored; a derive cannot
/// add a key without a field behind it.
impl Serialize for Mastery {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        let mut st = s.serialize_struct("Mastery", 8)?;
        st.serialize_field("Index", &self.index)?;
        st.serialize_field("Name", &self.name)?;
        st.serialize_field("Desc", &self.desc)?;
        st.serialize_field("Level", &self.level)?;
        st.serialize_field("Exp", &self.exp)?;
        st.serialize_field("NextExp", &self.next_exp)?;
        st.serialize_field("Bonuses", &self.bonuses)?;
        st.serialize_field("Mastered", &self.mastered())?;
        st.end()
    }
}

#[derive(Clone, Debug, Default, Serialize)]
#[serde(rename_all = "PascalCase")]
pub struct CityEvent {
    #[serde(rename = "ID")]
    pub id: String,
    pub kind: CityEventKind,
    pub event_type: String,
    pub title: String,
    pub enemies: Vec<String>,
    pub objectives: Vec<String>,
    pub reward_exp: i64,
    pub slot: i32,
    pub locations: Vec<[i32; 2]>,
    #[serde(serialize_with = "rfc3339_secs")]
    pub start: DateTime<Utc>,
    #[serde(serialize_with = "rfc3339_secs")]
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

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "PascalCase")]
pub struct Walk {
    pub blocks: i32,
    #[serde(rename = "DX")]
    pub dx: i32,
    #[serde(rename = "DY")]
    pub dy: i32,
    pub detour: i32,
}

#[derive(Clone, Debug, Default, Serialize)]
#[serde(rename_all = "PascalCase")]
pub struct CityMark {
    pub marker: String,
    pub label: String,
    pub enemies: Vec<String>,
    pub objectives: Vec<String>,
    pub kind: CityEventKind,
    /// Overlay-only: tells a Death Row QRF from a Wasteland one. Not in
    /// `--once` JSON, which already carries the resolved marker and label.
    #[serde(skip)]
    pub event_type: String,
    pub x: i32,
    pub y: i32,
    pub ends_in: Ns,
    pub off_map: bool,
    pub walk: Walk,
    pub reachable: bool,
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
    pub has_onslaught_countdown: bool,
    pub onslaught_countdown: Ns,
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
    pub masteries: Option<Vec<Mastery>>,
    pub mastery_status: String,
    pub status: String,
    /// True for routine asks (open a page, wait); drawn amber. False means
    /// something is wrong (config error, server failing); drawn red.
    pub status_is_prompt: bool,
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
            has_onslaught_countdown: false,
            onslaught_countdown: Ns(0),
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
            masteries: None,
            mastery_status: String::new(),
            status: String::new(),
            status_is_prompt: false,
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

/// The `--once` JSON wire shape, pinned field by field. Every value is
/// non-default so a dropped or renamed key, a changed timestamp format, or a
/// leaked internal field (`remembered`, `event_type` on a mark) shows up.
#[cfg(test)]
mod json_tests {
    use super::*;

    fn t(secs: i64) -> DateTime<Utc> {
        DateTime::from_timestamp(secs, 0).unwrap()
    }

    fn objective() -> Objective {
        Objective {
            name: "Kill Regular Infected".into(),
            target: 100,
            score: Some(55),
        }
    }

    fn challenge() -> Challenge {
        Challenge {
            index: 3,
            id: "8017".into(),
            name: "Summer Death".into(),
            desc: "Kill \"things\" & more".into(),
            clan: true,
            start: t(1_784_880_000),
            end: t(1_787_299_200),
            objectives: vec![
                objective(),
                Objective {
                    name: "Travel Blocks".into(),
                    target: 360,
                    score: None,
                },
            ],
            min_level: 1,
            max_level: 415,
            repeatable: true,
            reward_exp: 1_037_500,
            reward_cash: 31_000,
            reward_credits: 5,
            reward_points: 20,
            reward_items: "medkit|2".into(),
            reward_special: "summerticket|10".into(),
            remembered: true,
        }
    }

    fn bonus() -> MasteryBonus {
        MasteryBonus {
            name: "Item Find Chance".into(),
            scale: 0.005,
            max: 5.0,
            value: 1.02,
        }
    }

    fn mastery() -> Mastery {
        Mastery {
            index: 2,
            name: "Artisan".into(),
            desc: "Craft anything.".into(),
            level: 400,
            exp: 37,
            next_exp: 103,
            bonuses: vec![MasteryBonus {
                name: "Damage".into(),
                scale: 0.05,
                max: 20.0,
                value: 20.0,
            }],
        }
    }

    fn mastery_in_progress() -> Mastery {
        Mastery {
            index: 0,
            name: "Looter".into(),
            desc: "Loot anything.".into(),
            level: 204,
            exp: 37,
            next_exp: 103,
            bonuses: vec![bonus()],
        }
    }

    fn city_event() -> CityEvent {
        CityEvent {
            id: "509679".into(),
            kind: CityEventKind::Mission,
            event_type: "mission".into(),
            title: "The Clue".into(),
            enemies: vec!["3 x Flaming Titan".into(), "1 x Bandits".into()],
            objectives: vec!["Find the clue (2)".into()],
            reward_exp: 4000,
            slot: 4,
            locations: vec![[1002, 1000], [3000, 3000]],
            start: t(1_786_527_000),
            end: t(1_786_530_600),
            started: true,
            ended: true,
            onslaught: true,
        }
    }

    fn walk() -> Walk {
        Walk {
            blocks: 12,
            dx: -3,
            dy: 9,
            detour: 2,
        }
    }

    fn city_mark() -> CityMark {
        CityMark {
            marker: "M5".into(),
            label: "To The Slaughter".into(),
            enemies: vec!["2 x Bandits".into()],
            objectives: vec!["Eliminate the Flaming Titans (3)".into()],
            kind: CityEventKind::Qrf,
            event_type: "qrfdr".into(),
            x: 1047,
            y: 987,
            ends_in: Ns(90 * 1_000_000_000 + 999),
            off_map: true,
            walk: walk(),
            reachable: true,
        }
    }

    fn pretty<T: Serialize>(v: &T) -> String {
        serde_json::to_string_pretty(v).unwrap()
    }

    #[test]
    fn objective_json() {
        assert_eq!(
            pretty(&objective()),
            r#"{
  "Name": "Kill Regular Infected",
  "Target": 100,
  "Score": 55,
  "HasScore": true
}"#
        );
    }

    #[test]
    fn challenge_json() {
        assert_eq!(
            pretty(&challenge()),
            r#"{
  "Index": 3,
  "ID": "8017",
  "Name": "Summer Death",
  "Desc": "Kill \"things\" & more",
  "Clan": true,
  "Start": "2026-07-24T08:00:00Z",
  "End": "2026-08-21T08:00:00Z",
  "Objectives": [
    {
      "Name": "Kill Regular Infected",
      "Target": 100,
      "Score": 55,
      "HasScore": true
    },
    {
      "Name": "Travel Blocks",
      "Target": 360,
      "Score": 0,
      "HasScore": false
    }
  ],
  "MinLevel": 1,
  "MaxLevel": 415,
  "Repeatable": true,
  "RewardExp": 1037500,
  "RewardCash": 31000,
  "RewardCredits": 5,
  "RewardPoints": 20,
  "RewardItems": "medkit|2",
  "RewardSpecial": "summerticket|10"
}"#
        );
    }

    #[test]
    fn mastery_bonus_json() {
        assert_eq!(
            pretty(&bonus()),
            r#"{
  "Name": "Item Find Chance",
  "Scale": 0.005,
  "Max": 5.0,
  "Value": 1.02
}"#
        );
    }

    #[test]
    fn mastery_json() {
        assert_eq!(
            pretty(&mastery()),
            r#"{
  "Index": 2,
  "Name": "Artisan",
  "Desc": "Craft anything.",
  "Level": 400,
  "Exp": 37,
  "NextExp": 103,
  "Bonuses": [
    {
      "Name": "Damage",
      "Scale": 0.05,
      "Max": 20.0,
      "Value": 20.0
    }
  ],
  "Mastered": true
}"#
        );
        assert_eq!(
            pretty(&mastery_in_progress()),
            r#"{
  "Index": 0,
  "Name": "Looter",
  "Desc": "Loot anything.",
  "Level": 204,
  "Exp": 37,
  "NextExp": 103,
  "Bonuses": [
    {
      "Name": "Item Find Chance",
      "Scale": 0.005,
      "Max": 5.0,
      "Value": 1.02
    }
  ],
  "Mastered": false
}"#
        );
    }

    #[test]
    fn city_event_json() {
        assert_eq!(
            pretty(&city_event()),
            r#"{
  "ID": "509679",
  "Kind": 1,
  "EventType": "mission",
  "Title": "The Clue",
  "Enemies": [
    "3 x Flaming Titan",
    "1 x Bandits"
  ],
  "Objectives": [
    "Find the clue (2)"
  ],
  "RewardExp": 4000,
  "Slot": 4,
  "Locations": [
    [
      1002,
      1000
    ],
    [
      3000,
      3000
    ]
  ],
  "Start": "2026-08-12T09:30:00Z",
  "End": "2026-08-12T10:30:00Z",
  "Started": true,
  "Ended": true,
  "Onslaught": true
}"#
        );
    }

    #[test]
    fn walk_json() {
        assert_eq!(
            pretty(&walk()),
            r#"{
  "Blocks": 12,
  "DX": -3,
  "DY": 9,
  "Detour": 2
}"#
        );
    }

    #[test]
    fn city_mark_json() {
        assert_eq!(
            pretty(&city_mark()),
            r#"{
  "Marker": "M5",
  "Label": "To The Slaughter",
  "Enemies": [
    "2 x Bandits"
  ],
  "Objectives": [
    "Eliminate the Flaming Titans (3)"
  ],
  "Kind": 2,
  "X": 1047,
  "Y": 987,
  "EndsIn": 90,
  "OffMap": true,
  "Walk": {
    "Blocks": 12,
    "DX": -3,
    "DY": 9,
    "Detour": 2
  },
  "Reachable": true
}"#
        );
    }
}
