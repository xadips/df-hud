//! Full TOML config. Unknown keys are errors.
//! Intervals below their floor are startup errors, not clamps. `notify` is
//! banned; [`Watch`] stats mtime on the overlay 1s tick.

use std::error::Error;
use std::path::{Path, PathBuf};
use std::time::{Duration as StdDuration, SystemTime};

use serde::{Deserialize, Deserializer, Serialize, Serializer, de};

use crate::app::hotkeys;

const DEFAULT_BASE_URL: &str = "https://fairview.deadfrontier.com/onlinezombiemmo";
const DEFAULT_ALLSTATS_URL: &str =
    "https://fairview.deadfrontier.com/onlinezombiemmo/dfdata/get_allstats.php?printvars=1";
// Deliberately not 9275: SilverOverlays listens there, and its userscript sends
// a payload df-hud cannot use for the challenge board. Sharing the port would
// mean whichever started first wins the bind.
const DEFAULT_LISTEN: &str = "127.0.0.1:9310";
const DEFAULT_GAME_PROCESS: &str = "DeadFrontier.exe";
const DEFAULT_BOSSMAP_URL: &str = "https://www.dfprofiler.com/bossmap/json/";

const FLOOR_ACTIVE: StdDuration = StdDuration::from_secs(5);
const FLOOR_IDLE: StdDuration = StdDuration::from_secs(30);
const FLOOR_CHALLENGE: StdDuration = StdDuration::from_secs(30);
const FLOOR_CATALOG: StdDuration = StdDuration::from_secs(3600);
const FLOOR_BOSSMAP: StdDuration = StdDuration::from_secs(15);
const FLOOR_TIMEOUT: StdDuration = StdDuration::from_secs(2);
const CEIL_TIMEOUT: StdDuration = StdDuration::from_secs(60);
const CEIL_JITTER: f64 = 0.5;

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub struct Duration(pub StdDuration);

impl Duration {
    pub fn from_secs(n: u64) -> Self {
        Self(StdDuration::from_secs(n))
    }
}

impl Serialize for Duration {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        s.serialize_u64(self.0.as_secs())
    }
}

impl<'de> Deserialize<'de> for Duration {
    fn deserialize<D: Deserializer<'de>>(d: D) -> Result<Self, D::Error> {
        let n = i64::deserialize(d)?;
        if n < 0 {
            return Err(de::Error::custom("seconds cannot be negative"));
        }
        Ok(Duration::from_secs(n as u64))
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Config {
    #[serde(skip)]
    pub source: Option<PathBuf>,
    pub df: Df,
    pub bridge: Bridge,
    pub poll: Poll,
    pub game: Game,
    pub game_keys: GameKeys,
    pub hotkeys: Hotkeys,
    pub paths: Paths,
    pub hud: Hud,
    pub bossmap: BossMap,
    pub presence: Presence,
    pub tray: Tray,
    pub widget: Widget,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Df {
    pub base_url: String,
    pub allstats_url: String,
    pub user_agent: String,
    pub timeout: Duration,
    pub skeygen: String,
    /// Numeric account id for the unauthenticated `get_values` call. Only used
    /// until the bridge delivers a real session; it cannot reach challenges.
    #[serde(default)]
    pub user_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Bridge {
    pub enabled: bool,
    pub listen: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Poll {
    pub active_interval: Duration,
    pub idle_interval: Duration,
    pub challenge_interval: Duration,
    pub catalog_interval: Duration,
    pub jitter: f64,
    pub only_when_game_running: bool,
    pub backoff_max: Duration,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Game {
    pub process: String,
    pub window_class: String,
    pub window_title_ignore: Vec<String>,
    pub scan_interval: Duration,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GameKeys {
    pub fps_display: bool,
    pub fps_key: String,
    pub fps_delay: Duration,
    pub dismiss_launcher: bool,
    pub launcher_key: String,
    pub launcher_title: String,
    pub launcher_delay: Duration,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Hotkeys {
    pub enabled: bool,
    pub map: String,
    pub challenges: String,
    pub run_start: String,
    pub xp_reset: String,
    pub overlay: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Paths {
    pub data_dir: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Hud {
    pub enabled: bool,
    pub only_when_game_running: bool,
    pub follow_game_workspace: bool,
    pub monitor: String,
    pub layer: String,
    pub margin_top: i32,
    pub margin_right: i32,
    pub margin_bottom: i32,
    pub margin_left: i32,
    pub font: String,
    pub font_size: f32,
    pub text_color: String,
    pub opacity: f32,
    pub reference_width: i32,
    pub reference_height: i32,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct BossMap {
    pub enabled: bool,
    pub url: String,
    pub interval: Duration,
    /// Unused; kept so existing configs load.
    pub max_interval: Duration,
    pub onslaught_interval: Duration,
    /// Unused; kept so existing configs load.
    pub onslaught_max_interval: Duration,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Presence {
    pub enabled: bool,
    pub socket: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Tray {
    pub enabled: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Widget {
    pub status: StatusWidget,
    pub session: SessionWidget,
    pub xp: XpWidget,
    pub block: BlockWidget,
    pub bosses: BossesWidget,
    pub map: MapWidget,
    pub challenges: ChallengesWidget,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct StatusWidget {
    pub x: i32,
    pub y: i32,
    pub font_size: f32,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SessionWidget {
    pub enabled: bool,
    pub x: i32,
    pub y: i32,
    pub font_size: f32,
    pub color: String,
    pub prefix: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct XpWidget {
    pub enabled: bool,
    pub x: i32,
    pub y: i32,
    pub font_size: f32,
    pub color: String,
    pub prefix: String,
    pub window: Duration,
    pub min_samples: i32,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct BlockWidget {
    pub enabled: bool,
    pub x: i32,
    pub y: i32,
    pub font_size: f32,
    pub color: String,
    pub show_position: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct BossesWidget {
    pub enabled: bool,
    pub x: i32,
    pub y: i32,
    pub font_size: f32,
    pub color: String,
    pub show_nearest: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct MapWidget {
    pub enabled: bool,
    pub center: bool,
    pub x: i32,
    pub y: i32,
    pub offset_x: i32,
    pub offset_y: i32,
    pub radius: i32,
    pub scale: f32,
    pub opacity: f32,
    pub show_list: bool,
    pub max_listed: i32,
    pub font_size: f32,
    pub color: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ChallengesWidget {
    pub enabled: bool,
    pub x: i32,
    pub y: i32,
    pub font_size: f32,
    pub color: String,
    pub show_repeatable: bool,
    pub show_clan: bool,
    pub show_personal: bool,
    pub show_completed: bool,
    pub show_sections: bool,
    pub max_shown: i32,
    pub urgent_within: Duration,
}

fn default_user_agent() -> String {
    format!(
        "df-hud/{} (+local overlay; contact via Dead Frontier forums)",
        env!("CARGO_PKG_VERSION")
    )
}

pub fn default_data_dir() -> String {
    #[cfg(windows)]
    {
        let local = std::env::var("LOCALAPPDATA").ok();
        let home = std::env::var("USERPROFILE").ok();
        windows_app_dir(local.as_deref(), home.as_deref(), "Local")
            .join("df-hud")
            .to_string_lossy()
            .into_owned()
    }
    #[cfg(not(windows))]
    {
        if let Ok(dir) = std::env::var("XDG_DATA_HOME")
            && !dir.is_empty()
        {
            return format!("{dir}/df-hud");
        }
        match std::env::var("HOME") {
            Ok(home) if !home.is_empty() => format!("{home}/.local/share/df-hud"),
            _ => ".".into(),
        }
    }
}

pub fn default_path() -> PathBuf {
    #[cfg(windows)]
    {
        let appdata = std::env::var("APPDATA").ok();
        let home = std::env::var("USERPROFILE").ok();
        windows_app_dir(appdata.as_deref(), home.as_deref(), "Roaming")
            .join("df-hud")
            .join("config.toml")
    }
    #[cfg(not(windows))]
    {
        if let Ok(dir) = std::env::var("XDG_CONFIG_HOME")
            && !dir.is_empty()
        {
            return PathBuf::from(dir).join("df-hud/config.toml");
        }
        match std::env::var("HOME") {
            Ok(home) if !home.is_empty() => PathBuf::from(home).join(".config/df-hud/config.toml"),
            _ => PathBuf::from("config.toml"),
        }
    }
}

/// `%APPDATA%` / `%LOCALAPPDATA%`, else `%USERPROFILE%\AppData\<kind>`.
#[cfg(any(windows, test))]
fn windows_app_dir(env_val: Option<&str>, home: Option<&str>, kind: &str) -> PathBuf {
    if let Some(dir) = env_val.filter(|s| !s.is_empty()) {
        return PathBuf::from(dir);
    }
    match home.filter(|s| !s.is_empty()) {
        Some(home) => PathBuf::from(home).join("AppData").join(kind),
        None => PathBuf::from("."),
    }
}

/// Connector/device the overlay should sit on. CLI wins; `"auto"` (or empty)
/// follows the game window from Visibility. Empty means the platform default.
pub fn overlay_monitor(cli: Option<&str>, hud_monitor: &str, vis_monitor: &str) -> String {
    if let Some(name) = cli.map(str::trim).filter(|s| !s.is_empty()) {
        return name.to_string();
    }
    let cfg = hud_monitor.trim();
    if !cfg.is_empty() && !cfg.eq_ignore_ascii_case("auto") {
        return cfg.to_string();
    }
    vis_monitor.trim().to_string()
}

impl Default for Config {
    fn default() -> Self {
        Self {
            source: None,
            df: Df {
                base_url: DEFAULT_BASE_URL.into(),
                allstats_url: DEFAULT_ALLSTATS_URL.into(),
                user_agent: default_user_agent(),
                timeout: Duration::from_secs(10),
                skeygen: String::new(),
                user_id: String::new(),
            },
            bridge: Bridge {
                enabled: true,
                listen: DEFAULT_LISTEN.into(),
            },
            poll: Poll {
                active_interval: Duration::from_secs(10),
                idle_interval: Duration(StdDuration::from_secs(120)),
                challenge_interval: Duration::from_secs(30),
                catalog_interval: Duration(StdDuration::from_hours(24)),
                jitter: 0.10,
                only_when_game_running: true,
                backoff_max: Duration(StdDuration::from_secs(300)),
            },
            game: Game {
                process: DEFAULT_GAME_PROCESS.into(),
                window_class: String::new(),
                window_title_ignore: vec!["configuration".into()],
                scan_interval: Duration::from_secs(1),
            },
            game_keys: GameKeys {
                fps_display: false,
                fps_key: "y".into(),
                fps_delay: Duration::from_secs(5),
                dismiss_launcher: false,
                launcher_key: "Return".into(),
                launcher_title: "Dead Frontier Configuration".into(),
                launcher_delay: Duration::from_secs(1),
            },
            hotkeys: Hotkeys {
                enabled: true,
                map: "V".into(),
                challenges: "T".into(),
                run_start: "Grave".into(),
                xp_reset: "X".into(),
                overlay: "K".into(),
            },
            paths: Paths {
                data_dir: default_data_dir(),
            },
            hud: Hud::default(),
            bossmap: BossMap {
                enabled: true,
                url: DEFAULT_BOSSMAP_URL.into(),
                interval: Duration::from_secs(60),
                max_interval: Duration::from_secs(300),
                onslaught_interval: Duration::from_secs(30),
                onslaught_max_interval: Duration::from_secs(60),
            },
            presence: Presence {
                enabled: true,
                socket: String::new(),
            },
            tray: Tray { enabled: true },
            widget: Widget::default(),
        }
    }
}

impl Default for Hud {
    fn default() -> Self {
        Self {
            enabled: true,
            only_when_game_running: true,
            follow_game_workspace: true,
            monitor: "auto".into(),
            layer: "overlay".into(),
            margin_top: 0,
            margin_right: 0,
            margin_bottom: 0,
            margin_left: 0,
            font: String::new(),
            font_size: 12.0,
            text_color: "#e6cc4d".into(),
            opacity: 1.0,
            reference_width: 2560,
            reference_height: 1440,
        }
    }
}

impl Default for Widget {
    fn default() -> Self {
        Self {
            status: StatusWidget {
                x: 10,
                y: 10,
                font_size: 0.0,
            },
            session: SessionWidget {
                enabled: true,
                x: 350,
                y: 60,
                font_size: 0.0,
                color: "#ffffff".into(),
                prefix: "IC Time: ".into(),
            },
            xp: XpWidget {
                enabled: true,
                x: 220,
                y: 80,
                font_size: 0.0,
                color: "#ffffff".into(),
                prefix: "Xp/Hr: ".into(),
                window: Duration(StdDuration::from_secs(60)),
                min_samples: 3,
            },
            block: BlockWidget {
                enabled: true,
                x: 2340,
                y: 300,
                font_size: 0.0,
                color: "#9ecbff".into(),
                show_position: false,
            },
            bosses: BossesWidget {
                enabled: true,
                x: 2240,
                y: 344,
                font_size: 0.0,
                color: String::new(),
                show_nearest: true,
            },
            map: MapWidget {
                enabled: true,
                center: true,
                x: 700,
                y: 240,
                offset_x: -100,
                offset_y: 350,
                radius: 8,
                scale: 0.4,
                opacity: 0.9,
                show_list: true,
                max_listed: 10,
                font_size: 0.0,
                color: "#e8e8e8".into(),
            },
            challenges: ChallengesWidget {
                enabled: true,
                x: 10,
                y: 190,
                font_size: 0.0,
                color: "#e8e8e8".into(),
                show_repeatable: false,
                show_clan: true,
                show_personal: true,
                show_completed: true,
                show_sections: true,
                max_shown: 0,
                urgent_within: Duration(StdDuration::from_hours(2)),
            },
        }
    }
}

impl Poll {
    pub fn effective_challenge_interval(&self, game_running: bool) -> StdDuration {
        if game_running {
            return self.challenge_interval.0;
        }
        if self.idle_interval.0 > self.challenge_interval.0 {
            self.idle_interval.0
        } else {
            self.challenge_interval.0
        }
    }
}

impl XpWidget {
    pub fn effective_window(&self, active_interval: StdDuration) -> StdDuration {
        if self.min_samples < 2 || active_interval.is_zero() {
            return self.window.0;
        }
        let need = active_interval * self.min_samples as u32;
        if self.window.0 < need {
            need
        } else {
            self.window.0
        }
    }
}

impl Hud {
    #[cfg(test)]
    pub fn hides_under_fullscreen(&self) -> bool {
        matches!(parse_layer(&self.layer).as_deref(), Ok(layer) if layer != "overlay")
    }
}

#[cfg(test)]
fn parse_layer(name: &str) -> Result<&'static str, String> {
    match name.trim().to_ascii_lowercase().as_str() {
        "overlay" | "" => Ok("overlay"),
        "top" => Ok("top"),
        "bottom" => Ok("bottom"),
        "background" => Ok("background"),
        _ => Err(format!(
            "{name:?} is not a layer: use overlay (the only one drawn above a fullscreen game), top, bottom or background"
        )),
    }
}

fn canonicalize_hotkey(
    errs: &mut Vec<String>,
    used: &mut std::collections::HashMap<String, &'static str>,
    name: &'static str,
    slot: &mut String,
) {
    *slot = slot.trim().to_string();
    if slot.is_empty() {
        return;
    }
    match hotkeys::parse_binding(slot) {
        Ok(b) => {
            let canon = b.canonical();
            *slot = canon.clone();
            if let Some(prev) = used.insert(canon.clone(), name) {
                errs.push(format!(
                    "hotkeys.{prev} and hotkeys.{name} both use {canon:?}"
                ));
            }
        }
        Err(e) => errs.push(format!("hotkeys.{name}: {e}")),
    }
}

impl Game {
    pub fn window_match(&self) -> crate::game::desktop::Match {
        let class = if self.window_class.trim().is_empty() {
            self.process.clone()
        } else {
            self.window_class.clone()
        };
        crate::game::desktop::Match {
            class,
            ignore_titles: self.window_title_ignore.clone(),
            launcher_title: String::new(),
        }
    }
}

impl Config {
    /// Keep resources that are constructed only at startup on their running
    /// values. Returns the edited keys that were ignored until restart.
    pub fn reloadable_from(&mut self, running: &Self) -> Vec<&'static str> {
        let mut ignored = Vec::new();
        macro_rules! freeze {
            ($field:expr, $running:expr, $name:literal) => {
                if $field != $running {
                    $field = $running.clone();
                    ignored.push($name);
                }
            };
        }

        freeze!(
            self.bridge.enabled,
            running.bridge.enabled,
            "bridge.enabled"
        );
        freeze!(self.bridge.listen, running.bridge.listen, "bridge.listen");
        freeze!(
            self.paths.data_dir,
            running.paths.data_dir,
            "paths.data_dir"
        );
        freeze!(
            self.presence.enabled,
            running.presence.enabled,
            "presence.enabled"
        );
        freeze!(
            self.presence.socket,
            running.presence.socket,
            "presence.socket"
        );
        freeze!(self.tray.enabled, running.tray.enabled, "tray.enabled");
        freeze!(self.hud.layer, running.hud.layer, "hud.layer");
        ignored
    }

    pub fn parse(text: &str) -> Result<Self, Box<dyn Error>> {
        let overlay: toml::Value =
            toml::from_str(text).map_err(|err| format!("invalid TOML ({err})"))?;
        let mut base = toml::Value::try_from(Self::default())
            .map_err(|err| format!("internal config serialize: {err}"))?;
        reject_unknown(&overlay, &base, "")?;
        merge_value(&mut base, overlay);
        let mut cfg: Self = base
            .try_into()
            .map_err(|err| format!("invalid config ({err})"))?;
        cfg.validate().map_err(|e| -> Box<dyn Error> { e.into() })?;
        Ok(cfg)
    }

    pub fn launcher_window_match(&self) -> crate::game::desktop::Match {
        let mut m = self.game.window_match();
        m.launcher_title = self.game_keys.launcher_title.clone();
        m
    }

    pub fn load(path: &Path) -> Result<Self, Box<dyn Error>> {
        match std::fs::read_to_string(path) {
            Ok(text) => {
                let mut cfg =
                    Self::parse(&text).map_err(|err| format!("{}: {err}", path.display()))?;
                cfg.source = Some(path.to_path_buf());
                Ok(cfg)
            }
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => Ok(Self::default()),
            Err(err) => Err(format!("{}: {err}", path.display()).into()),
        }
    }

    pub fn requests_per_hour(&self, active_fraction: f64) -> f64 {
        let active_fraction = active_fraction.clamp(0.0, 1.0);
        let per_hour = |d: StdDuration| {
            if d.is_zero() {
                0.0
            } else {
                3600.0 / d.as_secs_f64()
            }
        };
        let mut total = active_fraction * per_hour(self.poll.active_interval.0)
            + (1.0 - active_fraction) * per_hour(self.poll.idle_interval.0);
        if self.widget.challenges.enabled {
            total += active_fraction * per_hour(self.poll.effective_challenge_interval(true))
                + (1.0 - active_fraction) * per_hour(self.poll.effective_challenge_interval(false));
        }
        total += per_hour(self.poll.catalog_interval.0);
        if self.bossmap.enabled {
            total += active_fraction * per_hour(self.bossmap.interval.0);
        }
        total
    }

    pub fn source_path(&self) -> Option<&Path> {
        self.source.as_deref()
    }

    pub fn describe_source(&self, requested: &Path) -> String {
        match &self.source {
            None => format!(
                "built-in defaults, no config file at {}",
                requested.display()
            ),
            Some(path) => format!("config {}", path.display()),
        }
    }

    pub fn signing_salt(&self, reported: impl Fn() -> String) -> String {
        let got = reported();
        if got.is_empty() {
            self.df.skeygen.clone()
        } else {
            got
        }
    }

    pub fn credentials_path(&self) -> PathBuf {
        PathBuf::from(&self.paths.data_dir).join("credentials.json")
    }

    pub fn state_path(&self) -> PathBuf {
        PathBuf::from(&self.paths.data_dir).join("state.json")
    }

    pub fn catalog_path(&self) -> PathBuf {
        PathBuf::from(&self.paths.data_dir).join("catalog.json")
    }

    pub fn ensure_data_dir(&self) -> Result<(), Box<dyn Error>> {
        let dir = Path::new(&self.paths.data_dir);
        #[cfg(unix)]
        {
            use std::os::unix::fs::DirBuilderExt;
            std::fs::DirBuilder::new()
                .recursive(true)
                .mode(0o700)
                .create(dir)?;
        }
        #[cfg(not(unix))]
        {
            std::fs::create_dir_all(dir)?;
        }
        let probe = dir.join(".writable-probe");
        std::fs::write(&probe, b"")?;
        std::fs::remove_file(&probe)?;
        Ok(())
    }

    fn validate(&mut self) -> Result<(), String> {
        let mut errs: Vec<String> = Vec::new();
        self.df.base_url = self.df.base_url.trim_end_matches('/').to_string();
        if self.df.base_url.is_empty() {
            errs.push("df.base_url is empty".into());
        } else if !is_https_url(&self.df.base_url) {
            errs.push(format!(
                "df.base_url {:?} must be an https URL with a host: every request body carries account credentials",
                self.df.base_url
            ));
        }
        if !self.df.allstats_url.is_empty() && !is_absolute_url(&self.df.allstats_url) {
            errs.push(format!(
                "df.allstats_url {:?} is not an absolute URL",
                self.df.allstats_url
            ));
        }
        if self.df.user_agent.trim().is_empty() {
            errs.push("df.user_agent is empty: identify this tool honestly to the server".into());
        }
        self.df.user_id = self.df.user_id.trim().to_string();
        if !self.df.user_id.is_empty() && !crate::net::dfclient::numeric_id(&self.df.user_id) {
            errs.push(format!(
                "df.user_id {:?} must be digits only: it is the numeric account id, not a name",
                self.df.user_id
            ));
        }
        push_range(
            &mut errs,
            "df.timeout",
            self.df.timeout.0,
            FLOOR_TIMEOUT,
            CEIL_TIMEOUT,
        );
        if self.bridge.enabled
            && let Err(e) = validate_loopback(&self.bridge.listen)
        {
            errs.push(format!("bridge.listen: {e}"));
        }
        push_floor(
            &mut errs,
            "poll.active_interval",
            self.poll.active_interval.0,
            FLOOR_ACTIVE,
        );
        push_floor(
            &mut errs,
            "poll.idle_interval",
            self.poll.idle_interval.0,
            FLOOR_IDLE,
        );
        push_floor(
            &mut errs,
            "poll.challenge_interval",
            self.poll.challenge_interval.0,
            FLOOR_CHALLENGE,
        );
        push_floor(
            &mut errs,
            "poll.catalog_interval",
            self.poll.catalog_interval.0,
            FLOOR_CATALOG,
        );
        if self.poll.idle_interval.0 < self.poll.active_interval.0 {
            errs.push(format!(
                "poll.idle_interval ({}) is shorter than poll.active_interval ({}): idle would poll harder than playing, which is backwards",
                secs_label(self.poll.idle_interval.0),
                secs_label(self.poll.active_interval.0)
            ));
        }
        if self.poll.jitter < 0.0 || self.poll.jitter > CEIL_JITTER {
            errs.push(format!(
                "poll.jitter {:.3} must be between 0 and {CEIL_JITTER} (a fraction of the interval)",
                self.poll.jitter
            ));
        }
        if self.poll.backoff_max.0 < self.poll.active_interval.0 {
            errs.push(format!(
                "poll.backoff_max ({}) must be at least poll.active_interval ({})",
                secs_label(self.poll.backoff_max.0),
                secs_label(self.poll.active_interval.0)
            ));
        }
        self.game.process = self.game.process.trim().to_string();
        if self.game.process.is_empty() {
            errs.push("game.process is empty: df-hud would never detect the game".into());
        }
        if self.game.process.contains('/') || self.game.process.contains('\\') {
            errs.push(format!(
                "game.process {:?} must be a bare executable name, not a path",
                self.game.process
            ));
        }
        push_range(
            &mut errs,
            "game.scan_interval",
            self.game.scan_interval.0,
            StdDuration::from_secs(1),
            StdDuration::from_mins(5),
        );
        self.game_keys.fps_key = self.game_keys.fps_key.trim().to_string();
        if self.game_keys.fps_display && self.game_keys.fps_key.is_empty() {
            errs.push("game_keys.fps_display is on but game_keys.fps_key is empty".into());
        }
        if self.hud.enabled {
            if !matches!(
                self.hud.layer.to_ascii_lowercase().as_str(),
                "overlay" | "top" | "bottom" | "background" | ""
            ) {
                errs.push(format!(
                    "hud.layer {:?} is not a layer: use overlay (the only one drawn above a fullscreen game), top, bottom or background",
                    self.hud.layer
                ));
            }
            self.hud.font = self.hud.font.trim().to_string();
            if self.hud.font_size <= 0.0 {
                errs.push(format!(
                    "hud.font_size {} must be positive",
                    self.hud.font_size
                ));
            }
            if self.hud.opacity <= 0.0 || self.hud.opacity > 1.0 {
                errs.push(format!(
                    "hud.opacity {:.2} must be in (0, 1]: 0 would render an invisible HUD",
                    self.hud.opacity
                ));
            }
            if self.hud.reference_width <= 0 || self.hud.reference_height <= 0 {
                errs.push("hud.reference_width and hud.reference_height must be positive".into());
            }
            if let Err(e) = validate_color(&self.hud.text_color) {
                errs.push(format!("hud.text_color: {e}"));
            }
        }
        if self.bossmap.enabled {
            if !is_https_url(&self.bossmap.url) {
                errs.push(format!(
                    "bossmap.url {:?} must be an absolute https URL",
                    self.bossmap.url
                ));
            }
            push_floor(
                &mut errs,
                "bossmap.interval",
                self.bossmap.interval.0,
                FLOOR_BOSSMAP,
            );
            push_floor(
                &mut errs,
                "bossmap.onslaught_interval",
                self.bossmap.onslaught_interval.0,
                FLOOR_BOSSMAP,
            );
            if self.bossmap.max_interval.0 < self.bossmap.interval.0 {
                errs.push(format!(
                    "bossmap.max_interval ({}) is shorter than bossmap.interval ({})",
                    secs_label(self.bossmap.max_interval.0),
                    secs_label(self.bossmap.interval.0)
                ));
            }
        }
        self.paths.data_dir = expand_home(self.paths.data_dir.trim());
        if self.paths.data_dir.is_empty() {
            errs.push("paths.data_dir is empty".into());
        }
        if self.widget.xp.enabled && self.widget.xp.min_samples < 2 {
            errs.push(format!(
                "widget.xp.min_samples {} must be at least 2: a rate needs two points",
                self.widget.xp.min_samples
            ));
        }
        if self.widget.challenges.max_shown < 0 {
            errs.push(format!(
                "widget.challenges.max_shown {} cannot be negative (0 means no cap)",
                self.widget.challenges.max_shown
            ));
        }
        if self.widget.map.enabled {
            if self.widget.map.scale <= 0.0 {
                errs.push(format!(
                    "widget.map.scale {} must be positive",
                    self.widget.map.scale
                ));
            }
            if self.widget.map.opacity <= 0.0 || self.widget.map.opacity > 1.0 {
                errs.push(format!(
                    "widget.map.opacity {} must be above 0 and at most 1",
                    self.widget.map.opacity
                ));
            }
        }
        for (name, x, y, font, color) in [
            (
                "status",
                self.widget.status.x,
                self.widget.status.y,
                self.widget.status.font_size,
                "",
            ),
            (
                "session",
                self.widget.session.x,
                self.widget.session.y,
                self.widget.session.font_size,
                self.widget.session.color.as_str(),
            ),
            (
                "xp",
                self.widget.xp.x,
                self.widget.xp.y,
                self.widget.xp.font_size,
                self.widget.xp.color.as_str(),
            ),
            (
                "block",
                self.widget.block.x,
                self.widget.block.y,
                self.widget.block.font_size,
                self.widget.block.color.as_str(),
            ),
            (
                "bosses",
                self.widget.bosses.x,
                self.widget.bosses.y,
                self.widget.bosses.font_size,
                self.widget.bosses.color.as_str(),
            ),
            (
                "map",
                self.widget.map.x,
                self.widget.map.y,
                self.widget.map.font_size,
                self.widget.map.color.as_str(),
            ),
            (
                "challenges",
                self.widget.challenges.x,
                self.widget.challenges.y,
                self.widget.challenges.font_size,
                self.widget.challenges.color.as_str(),
            ),
        ] {
            if x < 0 || y < 0 {
                errs.push(format!(
                    "widget.{name} position {x}, {y} cannot be negative: it is measured from the top-left of the screen"
                ));
            }
            if font < 0.0 {
                errs.push(format!(
                    "widget.{name}.font_size {font} cannot be negative (0 inherits from [hud])"
                ));
            }
            if !color.is_empty()
                && let Err(e) = validate_color(color)
            {
                errs.push(format!("widget.{name}.color: {e}"));
            }
        }
        let mut used = std::collections::HashMap::new();
        canonicalize_hotkey(&mut errs, &mut used, "map", &mut self.hotkeys.map);
        canonicalize_hotkey(
            &mut errs,
            &mut used,
            "challenges",
            &mut self.hotkeys.challenges,
        );
        canonicalize_hotkey(
            &mut errs,
            &mut used,
            "run_start",
            &mut self.hotkeys.run_start,
        );
        canonicalize_hotkey(&mut errs, &mut used, "xp_reset", &mut self.hotkeys.xp_reset);
        canonicalize_hotkey(&mut errs, &mut used, "overlay", &mut self.hotkeys.overlay);
        if errs.is_empty() {
            Ok(())
        } else {
            Err(errs.join("\n"))
        }
    }
}

fn push_floor(errs: &mut Vec<String>, key: &str, got: StdDuration, floor: StdDuration) {
    if got < floor {
        errs.push(format!(
            "{key} is {}, below the {} minimum: this decides how often df-hud hits somebody's server, so it is rejected rather than quietly raised",
            secs_label(got),
            secs_label(floor)
        ));
    }
}

fn push_range(
    errs: &mut Vec<String>,
    key: &str,
    got: StdDuration,
    lo: StdDuration,
    hi: StdDuration,
) {
    if got < lo || got > hi {
        errs.push(format!(
            "{key} is {}, outside the allowed {}..{}",
            secs_label(got),
            secs_label(lo),
            secs_label(hi)
        ));
    }
}

fn is_https_url(s: &str) -> bool {
    let Some(rest) = s.strip_prefix("https://") else {
        return false;
    };
    let host = rest.split('/').next().unwrap_or("");
    !host.is_empty() && host.contains('.') || host == "localhost" || host.starts_with("127.")
}

fn is_absolute_url(s: &str) -> bool {
    s.starts_with("https://") || s.starts_with("http://")
}

pub(crate) fn validate_loopback(addr: &str) -> Result<(), String> {
    let (host, _port) = addr
        .rsplit_once(':')
        .ok_or_else(|| format!("bridge listen address {addr:?} must be host:port"))?;
    let host = host.trim();
    let host = host
        .strip_prefix('[')
        .and_then(|h| h.strip_suffix(']'))
        .unwrap_or(host);
    if host.is_empty() {
        return Err(format!(
            "bridge listen address {addr:?} has no host: use 127.0.0.1:PORT, because this endpoint receives account credentials"
        ));
    }
    if host.eq_ignore_ascii_case("localhost") {
        return Ok(());
    }
    if host == "127.0.0.1" || host == "::1" || host.starts_with("127.") {
        return Ok(());
    }
    Err(format!(
        "bridge listen host {host:?} is not loopback: this endpoint receives account-equivalent credentials and must never be reachable off this machine"
    ))
}

fn validate_color(s: &str) -> Result<(), String> {
    let s = s.trim();
    if s.is_empty() {
        return Err("empty".into());
    }
    if s.chars().any(|c| matches!(c, '{' | '}' | '"' | '\'' | ';')) {
        return Err(format!("{s:?} is not a colour"));
    }
    if let Some(hex) = s.strip_prefix('#') {
        if !matches!(hex.len(), 3 | 4 | 6 | 8) {
            return Err(format!("{s:?} must have 3, 4, 6 or 8 hex digits after #"));
        }
        if !hex.bytes().all(|b| b.is_ascii_hexdigit()) {
            return Err(format!("{s:?} is not hex"));
        }
    }
    Ok(())
}

pub(crate) fn expand_home(p: &str) -> String {
    if p == "~" {
        return std::env::var("HOME").unwrap_or_else(|_| p.into());
    }
    if let Some(rest) = p.strip_prefix("~/")
        && let Ok(home) = std::env::var("HOME")
    {
        return format!("{home}/{rest}");
    }
    p.to_string()
}

fn reject_unknown(overlay: &toml::Value, schema: &toml::Value, prefix: &str) -> Result<(), String> {
    let Some(ot) = overlay.as_table() else {
        return Ok(());
    };
    let st = schema.as_table();
    for (k, v) in ot {
        let path = if prefix.is_empty() {
            k.clone()
        } else {
            format!("{prefix}.{k}")
        };
        match st.and_then(|t| t.get(k)) {
            None => {
                return Err(format!(
                    "unknown key {path} (a typo here would otherwise be silently ignored; see df-hud.example.toml)"
                ));
            }
            Some(child) => reject_unknown(v, child, &path)?,
        }
    }
    Ok(())
}

fn merge_value(dest: &mut toml::Value, overlay: toml::Value) {
    match (dest, overlay) {
        (toml::Value::Table(dt), toml::Value::Table(ot)) => {
            for (k, v) in ot {
                match dt.get_mut(&k) {
                    Some(existing) => merge_value(existing, v),
                    None => {
                        dt.insert(k, v);
                    }
                }
            }
        }
        (dest, overlay) => {
            *dest = coerce(dest, overlay);
        }
    }
}

fn coerce(dest: &toml::Value, overlay: toml::Value) -> toml::Value {
    match (dest, overlay) {
        (toml::Value::Float(_), toml::Value::Integer(i)) => toml::Value::Float(i as f64),
        (_, v) => v,
    }
}

fn secs_label(d: StdDuration) -> String {
    format!("{}s", d.as_secs())
}

fn write_config_atomically(path: &Path, body: &str) -> Result<(), String> {
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir).map_err(|err| format!("{}: {err}", dir.display()))?;
    }
    let tmp = path.with_extension("toml.tmp");
    std::fs::write(&tmp, body.as_bytes()).map_err(|err| format!("{}: {err}", tmp.display()))?;
    std::fs::rename(&tmp, path).map_err(|err| format!("{}: {err}", path.display()))?;
    Ok(())
}

/// Stat-and-reload a config path. No `notify`.
pub struct Watch {
    path: Option<PathBuf>,
    mtime: Option<SystemTime>,
    pub cfg: Config,
    error: Option<String>,
}

impl Watch {
    pub fn open(path: Option<PathBuf>) -> Result<Self, Box<dyn Error>> {
        let path = path.unwrap_or_else(default_path);
        let cfg = Config::load(&path)?;
        let mtime = std::fs::metadata(&path)
            .ok()
            .and_then(|meta| meta.modified().ok());
        Ok(Self {
            path: Some(path),
            mtime,
            cfg,
            error: None,
        })
    }

    #[cfg(test)]
    pub fn path(&self) -> Option<&Path> {
        self.path.as_deref()
    }

    pub fn error(&self) -> Option<&str> {
        self.error.as_deref()
    }

    pub fn poll(&mut self) -> bool {
        let Some(path) = &self.path else {
            return false;
        };
        let Ok(meta) = std::fs::metadata(path) else {
            return false;
        };
        let mtime = meta.modified().ok();
        if mtime == self.mtime {
            return false;
        }
        match Config::load(path) {
            Ok(cfg) => {
                self.cfg = cfg;
                self.mtime = mtime;
                self.error = None;
                eprintln!("reloaded {}", path.display());
                true
            }
            Err(err) => {
                eprintln!("config reload failed: {err}");
                self.mtime = mtime;
                self.error = Some(err.to_string());
                false
            }
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum TrayOption {
    FpsDisplay,
    DismissLauncher,
}

impl TrayOption {
    fn table_key(self) -> (&'static str, &'static str) {
        match self {
            Self::FpsDisplay => ("game_keys", "fps_display"),
            Self::DismissLauncher => ("game_keys", "dismiss_launcher"),
        }
    }
}

/// Change one tray-backed boolean without rewriting the rest of the file.
pub fn set_tray_option(path: &Path, option: TrayOption, enabled: bool) -> Result<(), String> {
    let (table, key) = option.table_key();
    let src = match std::fs::read_to_string(path) {
        Ok(s) => s,
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => String::new(),
        Err(err) => return Err(format!("{}: {err}", path.display())),
    };
    let next = set_toml_bool(&src, table, key, enabled);
    write_config_atomically(path, &next)
}

fn set_toml_bool(src: &str, table: &str, key: &str, enabled: bool) -> String {
    let want = if enabled { "true" } else { "false" };
    let header = format!("[{table}]");
    let newline = if src.contains("\r\n") { "\r\n" } else { "\n" };
    let mut lines: Vec<String> = if src.is_empty() {
        Vec::new()
    } else {
        src.lines().map(std::string::ToString::to_string).collect()
    };
    let mut current = String::new();
    let mut found_table = false;
    let mut replaced = false;
    for line in &mut lines {
        let trimmed = line.trim();
        if trimmed.starts_with('[') && trimmed.ends_with(']') {
            current = trimmed[1..trimmed.len() - 1].to_string();
            if current == table {
                found_table = true;
            }
            continue;
        }
        if current != table || replaced {
            continue;
        }
        let bare = trimmed.split('#').next().unwrap_or("").trim();
        let Some((lhs, _)) = bare.split_once('=') else {
            continue;
        };
        if lhs.trim() != key {
            continue;
        }
        if let Some(hash) = line.find('#') {
            let prefix = &line[..hash];
            let comment = &line[hash..];
            if let Some(eq) = prefix.find('=') {
                *line = format!("{}= {} {}", &prefix[..eq], want, comment.trim_start());
            }
        } else if let Some(eq) = line.find('=') {
            *line = format!("{}= {}", &line[..eq], want);
        }
        replaced = true;
    }
    if !replaced {
        if found_table {
            if let Some(i) = lines.iter().position(|l| l.trim() == header) {
                lines.insert(i + 1, format!("{key} = {want}"));
            }
        } else {
            if !lines.is_empty() && !lines.last().unwrap().is_empty() {
                lines.push(String::new());
            }
            lines.push(header);
            lines.push(format!("{key} = {want}"));
        }
    }
    let mut out = lines.join(newline);
    if (src.ends_with('\n') || src.is_empty()) && !out.ends_with('\n') {
        out.push_str(newline);
    }
    out
}

pub fn parse_color(hex: &str, alpha: f32) -> [f32; 4] {
    let h = hex.trim().trim_start_matches('#');
    let (r, g, b, a) = match h.len() {
        3 => {
            let n = u32::from_str_radix(h, 16).unwrap_or(0x00ff_ffff);
            (
                (((n >> 8) & 0xf) * 0x11) as f32 / 255.0,
                (((n >> 4) & 0xf) * 0x11) as f32 / 255.0,
                ((n & 0xf) * 0x11) as f32 / 255.0,
                alpha,
            )
        }
        6 => {
            let n = u32::from_str_radix(h, 16).unwrap_or(0x00ff_ffff);
            (
                ((n >> 16) & 0xff) as f32 / 255.0,
                ((n >> 8) & 0xff) as f32 / 255.0,
                (n & 0xff) as f32 / 255.0,
                alpha,
            )
        }
        8 => {
            let n = u32::from_str_radix(h, 16).unwrap_or(0xffff_ffff);
            (
                ((n >> 24) & 0xff) as f32 / 255.0,
                ((n >> 16) & 0xff) as f32 / 255.0,
                ((n >> 8) & 0xff) as f32 / 255.0,
                ((n & 0xff) as f32 / 255.0) * alpha,
            )
        }
        _ => (1.0, 1.0, 1.0, alpha),
    };
    [r, g, b, a.clamp(0.0, 1.0)]
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn unknown_key_is_an_error() {
        let err = Config::parse("widget.blok.x = 1\n").unwrap_err();
        let msg = err.to_string();
        assert!(
            msg.contains("blok") || msg.contains("unknown"),
            "expected unknown-key error, got {msg}"
        );
    }

    #[test]
    fn unknown_nested_key_is_an_error() {
        let err = Config::parse("[widget.block]\nx = 10\nwibble = 1\n").unwrap_err();
        assert!(err.to_string().contains("wibble") || err.to_string().contains("unknown"));
    }

    #[test]
    fn empty_file_is_defaults() {
        let cfg = Config::parse("").unwrap();
        assert_eq!(cfg.widget.block.x, 2340);
        assert_eq!(cfg.hud.reference_width, 2560);
        assert_eq!(cfg.poll.active_interval, Duration::from_secs(10));
    }

    #[test]
    fn reload_keeps_restart_only_fields() {
        let running = Config::default();
        let mut edited = running.clone();
        edited.bridge.enabled = !running.bridge.enabled;
        edited.bridge.listen = "127.0.0.1:9999".into();
        edited.paths.data_dir = "/tmp/elsewhere".into();
        edited.presence.enabled = !running.presence.enabled;
        edited.presence.socket = "/tmp/discord-ipc-9".into();
        edited.tray.enabled = !running.tray.enabled;
        edited.hud.layer = "top".into();
        edited.df.base_url = "https://example.com/game".into();

        let ignored = edited.reloadable_from(&running);
        assert_eq!(
            ignored,
            [
                "bridge.enabled",
                "bridge.listen",
                "paths.data_dir",
                "presence.enabled",
                "presence.socket",
                "tray.enabled",
                "hud.layer",
            ]
        );
        assert_eq!(edited.bridge, running.bridge);
        assert_eq!(edited.paths, running.paths);
        assert_eq!(edited.presence, running.presence);
        assert_eq!(edited.tray, running.tray);
        assert_eq!(edited.hud.layer, running.hud.layer);
        assert_eq!(edited.df.base_url, "https://example.com/game");
    }

    #[test]
    fn stub_toml_moves_block_x() {
        let cfg = Config::parse("[widget.block]\nx = 1800\n").unwrap();
        assert_eq!(cfg.widget.block.x, 1800);
        assert_eq!(cfg.widget.block.y, 300);
        assert_eq!(cfg.poll.active_interval, Duration::from_secs(10));
    }

    #[test]
    fn example_toml_matches_defaults() {
        let path = Path::new(env!("CARGO_MANIFEST_DIR")).join("df-hud.example.toml");
        let mut cfg = Config::load(&path).expect("example toml must load");
        cfg.source = None;
        let want = Config::default();
        assert_eq!(cfg, want);
    }

    #[test]
    fn missing_file_is_defaults() {
        let cfg = Config::load(Path::new("/no/such/df-hud-config.toml")).unwrap();
        assert!(cfg.source.is_none());
        assert_eq!(cfg.poll.active_interval, Duration::from_secs(10));
    }

    #[test]
    fn watch_none_uses_default_path() {
        let watch = Watch::open(None).unwrap();
        assert_eq!(watch.path(), Some(default_path().as_path()));
    }

    #[test]
    fn overlay_monitor_cli_wins_then_named_then_auto() {
        assert_eq!(overlay_monitor(Some("DP-1"), "auto", "DP-2"), "DP-1");
        assert_eq!(overlay_monitor(None, "HDMI-A-1", "DP-2"), "HDMI-A-1");
        assert_eq!(overlay_monitor(None, "auto", "DP-2"), "DP-2");
        assert_eq!(overlay_monitor(None, "", "DP-2"), "DP-2");
        assert_eq!(overlay_monitor(None, "auto", ""), "");
    }

    #[test]
    fn interval_below_floor_is_an_error() {
        let err = Config::parse("[poll]\nactive_interval = 1\n").unwrap_err();
        let msg = err.to_string();
        assert!(msg.contains("active_interval"), "{msg}");
        assert!(msg.contains("minimum") || msg.contains("below"), "{msg}");
    }

    #[test]
    fn idle_faster_than_active_is_backwards() {
        let err = Config::parse("[poll]\nactive_interval = 60\nidle_interval = 30\n").unwrap_err();
        assert!(err.to_string().contains("backwards"), "{}", err);
    }

    #[test]
    fn unknown_widget_table_is_an_error() {
        let err = Config::parse("[widget.xpp]\nenabled = true\n").unwrap_err();
        assert!(
            err.to_string().contains("xpp") || err.to_string().contains("unknown"),
            "{}",
            err
        );
    }

    #[test]
    fn requests_per_hour_defaults() {
        let cfg = Config::default();
        let idle = cfg.requests_per_hour(0.0);
        assert!((55.0..65.0).contains(&idle), "idle budget = {idle}");
        let active = cfg.requests_per_hour(1.0);
        assert!((530.0..550.0).contains(&active), "active budget = {active}");
        let mut no_boss = Config::default();
        no_boss.bossmap.enabled = false;
        assert!(cfg.requests_per_hour(1.0) - no_boss.requests_per_hour(1.0) >= 55.0);
    }

    #[test]
    fn effective_challenge_interval_stretches_when_idle() {
        let cfg = Config::default();
        assert_eq!(
            cfg.poll.effective_challenge_interval(true),
            StdDuration::from_secs(30)
        );
        assert_eq!(
            cfg.poll.effective_challenge_interval(false),
            StdDuration::from_secs(120)
        );
    }

    #[test]
    fn loopback_only_bridge() {
        let err = Config::parse("[bridge]\nlisten = \"0.0.0.0:9310\"\n").unwrap_err();
        assert!(err.to_string().contains("loopback"), "{err}");
    }

    #[test]
    fn https_base_url() {
        let err = Config::parse("[df]\nbase_url = \"http://example.com\"\n").unwrap_err();
        assert!(err.to_string().contains("https"), "{err}");
    }

    #[test]
    fn user_id_defaults_to_empty_and_accepts_digits() {
        assert!(Config::default().df.user_id.is_empty());
        let cfg = Config::parse("[df]\nuser_id = \" 1234567 \"\n").unwrap();
        assert_eq!(cfg.df.user_id, "1234567", "surrounding space is trimmed");
    }

    #[test]
    fn a_non_numeric_user_id_is_an_error() {
        for bad in ["notanid", "123-456", "12 34"] {
            let err = Config::parse(&format!("[df]\nuser_id = {bad:?}\n"))
                .unwrap_err()
                .to_string();
            assert!(err.contains("df.user_id"), "{bad:?} gave {err}");
        }
    }

    #[test]
    fn bad_duration_is_an_error() {
        let err = Config::parse("[poll]\nactive_interval = \"10 seconds\"\n").unwrap_err();
        let msg = err.to_string();
        assert!(
            msg.contains("invalid type") || msg.contains("integer") || msg.contains("i64"),
            "{msg}"
        );
    }

    #[test]
    fn reports_every_problem_at_once() {
        let err = Config::parse(
            "[poll]\nactive_interval = 1\n[hud]\ntext_color = \"#gg0011\"\nopacity = 3\n",
        )
        .unwrap_err();
        let msg = err.to_string();
        assert!(msg.contains("active_interval"), "{msg}");
        assert!(
            msg.contains("text_color") || msg.contains("opacity"),
            "{msg}"
        );
    }

    #[test]
    fn load_overrides_leave_other_defaults() {
        let cfg = Config::parse(
            r#"
[poll]
active_interval = 15
idle_interval = 300
jitter = 0.25

[hud]
layer = "top"
margin_bottom = 12

[widget.session]
x = 900
y = 40
font_size = 18

[widget.xp]
min_samples = 5
window = 120
"#,
        )
        .unwrap();
        assert_eq!(cfg.poll.active_interval, Duration::from_secs(15));
        assert_eq!(cfg.poll.jitter, 0.25);
        assert_eq!(cfg.poll.catalog_interval, Duration::from_secs(24 * 3600));
        assert!(cfg.bridge.enabled);
        assert!(cfg.hud.hides_under_fullscreen());
        assert_eq!(cfg.widget.session.x, 900);
        assert_eq!(cfg.widget.session.y, 40);
        assert_eq!(cfg.widget.session.font_size, 18.0);
        assert_eq!(
            cfg.widget.session.prefix,
            Config::default().widget.session.prefix
        );
        assert_eq!(cfg.widget.xp.x, Config::default().widget.xp.x);
        assert_eq!(cfg.widget.xp.min_samples, 5);
        assert_eq!(cfg.widget.xp.window, Duration(StdDuration::from_secs(120)));
    }

    #[test]
    fn hotkeys_canonicalise_and_allow_unbound() {
        let cfg =
            Config::parse("[hotkeys]\nmap = \"control + shift + m\"\nchallenges = \"\"\n").unwrap();
        assert_eq!(cfg.hotkeys.map, "Ctrl+Shift+M");
        assert_eq!(cfg.hotkeys.challenges, "");
    }

    #[test]
    fn hotkeys_reject_bad_and_duplicate() {
        let err = Config::parse(
            "[hotkeys]\nmap = \"Ctrl+Mouse4\"\nchallenges = \"Alt+F8\"\noverlay = \"alt + f8\"\n",
        )
        .unwrap_err();
        let msg = err.to_string();
        for want in [
            "hotkeys.map",
            "unsupported key",
            "hotkeys.challenges",
            "hotkeys.overlay",
        ] {
            assert!(msg.contains(want), "missing {want:?} in {msg}");
        }
    }

    #[test]
    fn remaining_interval_floors() {
        for body in [
            "[poll]\nidle_interval = 5\n",
            "[poll]\nchallenge_interval = 10\n",
            "[poll]\ncatalog_interval = 60\n",
        ] {
            let err = Config::parse(body).unwrap_err();
            assert!(err.to_string().contains("minimum"), "{err}");
        }
    }

    #[test]
    fn remaining_cross_field_rules() {
        let cases = [
            (
                "[poll]\nactive_interval = 60\nidle_interval = 120\nbackoff_max = 10\n",
                "backoff_max",
            ),
            ("[poll]\njitter = 0.9\n", "jitter"),
            ("[widget.xp]\nmin_samples = 1\n", "two points"),
            ("[widget.challenges]\nmax_shown = -1\n", "max_shown"),
            ("[widget.xp]\nx = -10\n", "cannot be negative"),
            ("[widget.block]\nfont_size = -2\n", "font_size"),
            ("[widget.xp]\ncolor = \"#gg0000\"\n", "widget.xp.color"),
            (
                "[widget.block]\ncolor = \"red; font-size: 90pt\"\n",
                "widget.block.color",
            ),
            ("[hud]\nclick_through = true\n", "unknown key"),
            ("[hud]\ncss = \"x\"\n", "unknown key"),
            ("[console]\nwidth = 720\n", "unknown key"),
            ("[hud]\nfont_family = \"x\"\n", "unknown key"),
            ("[widget.session]\nfont_family = \"x\"\n", "unknown key"),
        ];
        for (body, want) in cases {
            let err = Config::parse(body).unwrap_err();
            assert!(
                err.to_string().contains(want),
                "body {body:?} should mention {want:?}, got {err}"
            );
        }
    }

    #[test]
    fn extra_loopback_and_disabled_bridge() {
        for listen in ["192.168.1.50:9310", ":9310"] {
            let body = format!("[bridge]\nlisten = \"{listen}\"\n");
            assert!(Config::parse(&body).is_err(), "{listen}");
        }
        Config::parse("[bridge]\nenabled = false\nlisten = \"0.0.0.0:9310\"\n").unwrap();
    }

    #[test]
    fn plaintext_base_url_mentions_credentials() {
        let err = Config::parse(
            "[df]\nbase_url = \"http://fairview.deadfrontier.com/onlinezombiemmo\"\n",
        )
        .unwrap_err();
        assert!(err.to_string().contains("credentials"), "{err}");
    }

    #[test]
    fn trims_base_url_slash() {
        let cfg = Config::parse(
            "[df]\nbase_url = \"https://fairview.deadfrontier.com/onlinezombiemmo/\"\n",
        )
        .unwrap();
        assert!(!cfg.df.base_url.ends_with('/'));
    }

    #[test]
    fn parse_layer_names() {
        assert_eq!(parse_layer("overlay").unwrap(), "overlay");
        assert_eq!(parse_layer("OVERLAY").unwrap(), "overlay");
        assert_eq!(parse_layer("").unwrap(), "overlay");
        assert_eq!(parse_layer("top").unwrap(), "top");
        assert_eq!(parse_layer("bottom").unwrap(), "bottom");
        assert_eq!(parse_layer("background").unwrap(), "background");
        assert!(parse_layer("above").is_err());
    }

    #[test]
    fn validate_color_accepts_and_rejects() {
        for ok in [
            "#fff",
            "#ffff",
            "#e6cc4d",
            "#e6cc4dff",
            "yellow",
            "rgb(1,2,3)",
        ] {
            validate_color(ok).unwrap();
        }
        for bad in [
            "",
            "#12345",
            "#gggggg",
            "red; } window { background: black",
            "\"x\"",
        ] {
            assert!(validate_color(bad).is_err(), "{bad}");
        }
    }

    #[test]
    fn derived_paths() {
        let mut cfg = Config::default();
        cfg.paths.data_dir = "/tmp/df-hud-test".into();
        assert_eq!(
            cfg.credentials_path(),
            PathBuf::from("/tmp/df-hud-test/credentials.json")
        );
        assert_eq!(
            cfg.state_path(),
            PathBuf::from("/tmp/df-hud-test/state.json")
        );
        assert_eq!(
            cfg.catalog_path(),
            PathBuf::from("/tmp/df-hud-test/catalog.json")
        );
    }

    #[test]
    fn expand_home_only_at_start() {
        let home = std::env::var("HOME").unwrap();
        assert_eq!(
            expand_home("~/.local/share/df-hud"),
            format!("{home}/.local/share/df-hud")
        );
        assert_eq!(expand_home("/opt/~/x"), "/opt/~/x");
    }

    #[test]
    fn ensure_data_dir_is_private_and_writable() {
        let dir = std::env::temp_dir().join(format!("df-hud-config-test-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let mut cfg = Config::default();
        cfg.paths.data_dir = dir.to_string_lossy().into();
        cfg.ensure_data_dir().unwrap();
        let st = std::fs::metadata(&dir).unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(st.permissions().mode() & 0o777, 0o700);
        }
        assert!(!dir.join(".writable-probe").exists());
        let _ = std::fs::remove_dir_all(&dir);
        let _ = st;
    }

    #[test]
    fn signing_salt_prefers_the_bridge() {
        let mut cfg = Config::default();
        cfg.df.skeygen = "from-config".into();
        let mut reported = String::new();
        assert_eq!(cfg.signing_salt(|| reported.clone()), "from-config");
        reported = "from-bridge".into();
        assert_eq!(cfg.signing_salt(|| reported.clone()), "from-bridge");
    }

    #[test]
    fn xp_effective_window_widens() {
        let cfg = Config::parse(
            "[poll]\nactive_interval = 30\nidle_interval = 120\n[widget.xp]\nwindow = 30\n",
        )
        .unwrap();
        assert_eq!(
            cfg.widget.xp.effective_window(cfg.poll.active_interval.0),
            StdDuration::from_secs(90)
        );
        let mut xp = cfg.widget.xp.clone();
        xp.window = Duration(StdDuration::from_secs(600));
        assert_eq!(
            xp.effective_window(StdDuration::from_secs(30)),
            StdDuration::from_secs(600)
        );
        let def = Config::default();
        assert_eq!(
            def.widget.xp.effective_window(def.poll.active_interval.0),
            StdDuration::from_secs(60)
        );
    }

    #[test]
    fn effective_challenge_already_slower_than_idle_is_left() {
        let mut cfg = Config::default();
        cfg.poll.challenge_interval = Duration(StdDuration::from_secs(600));
        assert_eq!(
            cfg.poll.effective_challenge_interval(false),
            StdDuration::from_secs(600)
        );
    }

    #[test]
    fn set_tray_option_preserves_comments() {
        let dir = std::env::temp_dir().join(format!("df-hud-tray-opt-{}", std::process::id()));
        let _ = std::fs::create_dir_all(&dir);
        let path = dir.join("config.toml");
        std::fs::write(
            &path,
            "[game_keys]\nfps_display = false # keep this\ndismiss_launcher = false\n",
        )
        .unwrap();
        set_tray_option(&path, TrayOption::DismissLauncher, true).unwrap();
        let got = std::fs::read_to_string(&path).unwrap();
        assert!(got.contains("fps_display = false # keep this"), "{got}");
        assert!(got.contains("dismiss_launcher = true"), "{got}");
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn windows_app_dir_prefers_env_then_profile() {
        assert_eq!(
            windows_app_dir(Some(r"C:\Users\tester\AppData\Roaming"), None, "Roaming"),
            PathBuf::from(r"C:\Users\tester\AppData\Roaming")
        );
        assert_eq!(
            windows_app_dir(Some(""), Some(r"C:\Users\tester"), "Local"),
            PathBuf::from(r"C:\Users\tester")
                .join("AppData")
                .join("Local")
        );
        assert_eq!(windows_app_dir(None, None, "Roaming"), PathBuf::from("."));
    }
}
