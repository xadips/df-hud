//! One argv walk. Overlay draws `store.derive` through `present`; GPU skipped here.

use std::error::Error;
use std::ffi::OsString;
use std::path::{Path, PathBuf};
use std::time::Duration;

#[cfg(test)]
use chrono::{DateTime, Utc};
#[cfg(test)]
use serde::Deserialize;
#[cfg(test)]
use std::collections::HashMap;

use crate::app::store::Store;
use crate::config::{self, Config};
#[cfg(test)]
use crate::data::catalog;
use crate::data::challenges;
use crate::format;
use crate::game;
use crate::game::desktop;
use crate::model::{self, GameState, Tick};
use crate::net::creds::Store as Creds;
use crate::net::dfclient::Client;

const WITHHELD_FIELD: &[&str] = &["pass", "token", "cookie", "auth", "secretkey", "session"];

/// Single-dash longs from the Go CLI (`-once`). lexopt would split them as shorts.
const LONG_ALIASES: &[&str] = &[
    "check-config",
    "check-game",
    "config",
    "dump-challenges",
    "dump-fields",
    "headless",
    "once",
    "print-hud",
    "print-view",
    "version",
];

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct OverlayArgs {
    pub config: Option<PathBuf>,
    pub print_hud: bool,
    pub duration: Duration,
    pub requested: bool,
    pub output: Option<String>,
    pub namespace: String,
    pub list_outputs: bool,
    pub monitor: Option<String>,
    pub list_monitors: bool,
}

impl Default for OverlayArgs {
    fn default() -> Self {
        Self {
            config: None,
            print_hud: false,
            duration: Duration::ZERO,
            requested: false,
            output: None,
            namespace: "df-hud".into(),
            list_outputs: false,
            monitor: None,
            list_monitors: false,
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Launch {
    Help,
    Version,
    CheckConfig {
        config: Option<PathBuf>,
    },
    CheckGame {
        config: Option<PathBuf>,
    },
    Once {
        config: Option<PathBuf>,
        dump_fields: bool,
    },
    DumpChallenges {
        config: Option<PathBuf>,
        raw: bool,
    },
    Headless {
        config: Option<PathBuf>,
        print_hud: bool,
    },
    Overlay(OverlayArgs),
}

pub fn parse() -> Result<Launch, Box<dyn Error>> {
    parse_from(std::env::args_os().skip(1))
}

#[allow(unused_mut)] // platform overlay flags are cfg-gated in the match
pub fn parse_from<I, S>(args: I) -> Result<Launch, Box<dyn Error>>
where
    I: IntoIterator<Item = S>,
    S: Into<OsString>,
{
    use lexopt::prelude::*;

    let args: Vec<OsString> = args.into_iter().map(|s| normalize_arg(s.into())).collect();
    let mut parser = lexopt::Parser::from_args(args);

    let mut help = false;
    let mut version = false;
    let mut print_hud = false;
    let mut check_config = false;
    let mut check_game = false;
    let mut once = false;
    let mut dump_fields = false;
    let mut dump_challenges = false;
    let mut headless = false;
    let mut config = None;
    let mut duration = Duration::ZERO;
    let mut duration_set = false;
    let mut output = None;
    let mut output_set = false;
    let mut namespace = "df-hud".to_string();
    let mut namespace_set = false;
    let mut list_outputs = false;
    let mut monitor = None;
    let mut monitor_set = false;
    let mut list_monitors = false;

    while let Some(arg) = parser.next()? {
        match arg {
            Short('h') | Long("help") => help = true,
            Long("version") => version = true,
            Long("print-view") => once = true,
            Long("print-hud") => print_hud = true,
            Long("check-config") => check_config = true,
            Long("check-game") => check_game = true,
            Long("once") => once = true,
            Long("dump-fields") => dump_fields = true,
            Long("dump-challenges") => dump_challenges = true,
            Long("headless") => headless = true,
            Long("config") => {
                config = Some(PathBuf::from(parser.value()?.string()?));
            }
            Long("duration") => {
                duration_set = true;
                let secs: f32 = parser.value()?.parse()?;
                duration = if secs <= 0.0 {
                    Duration::ZERO
                } else {
                    Duration::from_secs_f32(secs)
                };
            }
            #[cfg(target_os = "linux")]
            Long("output") => {
                output_set = true;
                output = Some(parser.value()?.string()?);
            }
            #[cfg(target_os = "linux")]
            Long("namespace") => {
                namespace_set = true;
                namespace = parser.value()?.string()?;
            }
            #[cfg(target_os = "linux")]
            Long("list-outputs") => list_outputs = true,
            #[cfg(windows)]
            Long("monitor") => {
                monitor_set = true;
                monitor = Some(parser.value()?.string()?);
            }
            #[cfg(windows)]
            Long("list-monitors") => list_monitors = true,
            _ => return Err(arg.unexpected().into()),
        }
    }

    if help {
        return Ok(Launch::Help);
    }
    if dump_fields && !once && !dump_challenges {
        return Err("--dump-fields is for --once or --dump-challenges".into());
    }
    let exclusive = [version, check_config, check_game, once, dump_challenges]
        .iter()
        .filter(|on| **on)
        .count();
    if exclusive > 1 {
        return Err(
            "use only one of --version, --check-config, --check-game, --once, --dump-challenges"
                .into(),
        );
    }

    let overlay_only =
        duration_set || output_set || namespace_set || list_outputs || monitor_set || list_monitors;
    let mode = if version {
        Some("--version")
    } else if check_config {
        Some("--check-config")
    } else if check_game {
        Some("--check-game")
    } else if once {
        Some("--once")
    } else if dump_challenges {
        Some("--dump-challenges")
    } else if headless {
        Some("--headless")
    } else {
        None
    };
    if overlay_only {
        if let Some(mode) = mode {
            let mut flags = Vec::new();
            if duration_set {
                flags.push("--duration");
            }
            if output_set {
                flags.push("--output");
            }
            if namespace_set {
                flags.push("--namespace");
            }
            if list_outputs {
                flags.push("--list-outputs");
            }
            if monitor_set {
                flags.push("--monitor");
            }
            if list_monitors {
                flags.push("--list-monitors");
            }
            return Err(format!("{} cannot be used with {mode}", flags.join(", ")).into());
        }
    }

    if version {
        return Ok(Launch::Version);
    }
    if check_config {
        return Ok(Launch::CheckConfig { config });
    }
    if check_game {
        return Ok(Launch::CheckGame { config });
    }
    if dump_challenges {
        return Ok(Launch::DumpChallenges {
            config,
            raw: dump_fields,
        });
    }
    if once {
        return Ok(Launch::Once {
            config,
            dump_fields,
        });
    }
    if headless {
        return Ok(Launch::Headless { config, print_hud });
    }
    Ok(Launch::Overlay(OverlayArgs {
        requested: config.is_some() || overlay_only,
        config,
        print_hud,
        duration,
        output,
        namespace,
        list_outputs,
        monitor,
        list_monitors,
    }))
}

fn normalize_arg(arg: OsString) -> OsString {
    let Some(text) = arg.to_str() else {
        return arg;
    };
    let Some(name) = text.strip_prefix('-') else {
        return arg;
    };
    if name.starts_with('-') || !LONG_ALIASES.contains(&name) {
        return arg;
    }
    format!("--{name}").into()
}

pub fn print_help() {
    eprint!("{}", help_text());
}

fn help_text() -> String {
    let config = if cfg!(windows) {
        "TOML. default: %APPDATA%\\df-hud\\config.toml (missing = built-in defaults)"
    } else {
        "TOML. default ~/.config/df-hud/config.toml (missing = built-in defaults)"
    };
    let overlay = if cfg!(target_os = "linux") {
        "\
  --output NAME         pin to this connector (DP-1). overrides hud.monitor
  --namespace NAME      layer-shell namespace (default df-hud)
  --duration SECS       exit after SECS; 0 runs until Ctrl-C (default 0)
  --list-outputs        print connector names and exit
"
    } else if cfg!(windows) {
        "\
  --monitor NAME        Win32 device (\\\\.\\DISPLAY1). overrides hud.monitor
  --list-monitors       print monitors and exit
  --duration SECS       exit after SECS; 0 runs until Ctrl-C (default 0)
"
    } else {
        ""
    };
    format!(
        "\
df-hud — overlay (live derive)

  --once                 poll once, print the view, and exit
  --print-view           alias of --once
  --print-hud            print HUD text lines each update
  --dump-fields          with --once / --dump-challenges, print the player record (secrets withheld)
  --dump-challenges      fetch the challenge board once and print it
  --check-config         validate TOML and print the request budget
  --check-game           report whether the game client is detected
  --headless             run pollers without the overlay window
  --version              print the version and exit
  --config PATH          {config}
{overlay}  -h, --help            print this help
"
    )
}

pub fn run(launch: Launch) -> Result<(), Box<dyn Error>> {
    match launch {
        Launch::Help => {
            print_help();
            Ok(())
        }
        Launch::Version => {
            println!("df-hud {}", env!("CARGO_PKG_VERSION"));
            Ok(())
        }
        Launch::CheckConfig { config } => {
            print!("{}", check_config_text(config.as_deref())?);
            Ok(())
        }
        Launch::CheckGame { config } => {
            print!("{}", check_game_text(config.as_deref())?);
            Ok(())
        }
        Launch::Once {
            config,
            dump_fields,
        } => run_once(config.as_deref(), dump_fields),
        Launch::DumpChallenges { config, raw } => dump_challenges(config.as_deref(), raw),
        Launch::Headless { config, print_hud } => run_headless(config, print_hud),
        Launch::Overlay(_) => Err("internal: overlay is started from main".into()),
    }
}

pub fn run_headless(config: Option<PathBuf>, print_hud: bool) -> Result<(), Box<dyn Error>> {
    let path = config.unwrap_or_else(config::default_path);
    let cfg = Config::load(&path)?;
    eprintln!(
        "df-hud {} starting ({})",
        env!("CARGO_PKG_VERSION"),
        cfg.describe_source(&path)
    );
    let handle = crate::app::start_with(cfg, crate::app::PrintOpts { hud: print_hud })?;
    while !handle.stopped() {
        std::thread::sleep(std::time::Duration::from_millis(200));
    }
    Ok(())
}

#[cfg(test)]
#[derive(Deserialize)]
struct PrintViewFixture {
    now: DateTime<Utc>,
    tick_at: DateTime<Utc>,
    credentials_at: DateTime<Utc>,
    game: GameFixture,
    vars: HashMap<String, String>,
}

#[cfg(test)]
#[derive(Deserialize)]
struct GameFixture {
    running: bool,
    pid: i32,
    started_at: DateTime<Utc>,
}

#[cfg(test)]
fn view_from_fixture(path: &Path) -> Result<model::View, Box<dyn Error>> {
    let raw = std::fs::read_to_string(path).map_err(|err| format!("{}: {err}", path.display()))?;
    let fx: PrintViewFixture =
        serde_json::from_str(&raw).map_err(|err| format!("{}: {err}", path.display()))?;
    let catalog = load_allstats_catalog(fx.now)?;
    let store = Store::new(Some(catalog));
    store.apply_tick(Tick {
        at: fx.tick_at,
        vars: fx.vars,
        err: None,
        scheduled: true,
    });
    store.set_credentials_at(fx.credentials_at);
    store.set_game(GameState {
        running: fx.game.running,
        pid: fx.game.pid,
        started_at: Some(fx.game.started_at),
    });
    Ok(store.derive(fx.now))
}

pub fn check_config_text(config: Option<&Path>) -> Result<String, Box<dyn Error>> {
    let path = config.map_or_else(config::default_path, Path::to_path_buf);
    let cfg = Config::load(&path)?;
    Ok(format!(
        "config ok ({})\nrequest budget: about {:.0}/hour while playing, {:.0}/hour idle\n",
        cfg.describe_source(&path),
        cfg.requests_per_hour(1.0),
        cfg.requests_per_hour(0.0)
    ))
}

pub fn check_game_text(config: Option<&Path>) -> Result<String, Box<dyn Error>> {
    let path = config.map_or_else(config::default_path, Path::to_path_buf);
    let cfg = Config::load(&path)?;
    Ok(game_report(&cfg))
}

fn game_process(cfg: &Config) -> &str {
    if cfg.game.process.trim().is_empty() {
        game::DEFAULT_PROCESS
    } else {
        &cfg.game.process
    }
}

fn game_report(cfg: &Config) -> String {
    let exe = game_process(cfg);
    let mut out = format!("{}\n", game::scan_description(exe));
    match game::scan(exe) {
        Err(err) => out.push_str(&format!("{}\n", game::scan_error(&err))),
        Ok(state) if state.running => {
            out.push_str(&format_found(state));
            out.push_str(&window_report(cfg, state));
        }
        Ok(_) => {
            out.push_str("NOT FOUND - the game does not appear to be running.\n");
            let similar = game::similar_processes("frontier");
            if similar.is_empty() {
                out.push_str(
                    "Nothing similar is running either. Start the game and run this again.\n",
                );
            } else {
                out.push_str(
                    "\nProcesses with a similar name, in case the executable is named differently:\n",
                );
                for line in similar {
                    out.push_str(&format!("  {line}\n"));
                }
                out.push_str("\nIf one of those is the game, set game.process to its basename.\n");
            }
            if cfg.poll.only_when_game_running {
                out.push_str(
                    "\nNote: poll.only_when_game_running is on, so df-hud will not poll at all until the game is detected.\n",
                );
            }
        }
    }
    out
}

fn format_found(state: GameState) -> String {
    let launched = state.started_at.map_or_else(
        || "unknown".into(),
        |t| {
            t.with_timezone(&chrono::Local)
                .format("%Y-%m-%d %H:%M:%S")
                .to_string()
        },
    );
    format!(
        "FOUND: pid {}, launched {} ({} ago)\n",
        state.pid,
        launched,
        format::ago(state.elapsed(chrono::Utc::now()))
    )
}

fn window_report(cfg: &Config, state: GameState) -> String {
    let client = desktop::new_client();
    let want = cfg.launcher_window_match();
    match client.game_window(state.pid, &want) {
        Err(err) => format!(
            "\nThe desktop could not be asked where the window is ({err}).\n\
The HUD will still work; window-following visibility is disabled.\n"
        ),
        Ok(place) if place.known => {
            let shown = if place.on_active_workspace {
                "yes"
            } else {
                "no - the HUD will be hidden while it stays there"
            };
            if place.foreground_rule {
                format!(
                    "\nWINDOW: class {:?} on monitor {} (matched by {})\n\
        that window is visible and not minimized: {shown}\n\
        that window is in the foreground: {}\n",
                    place.class, place.monitor, place.matched_by, place.foreground
                )
            } else {
                format!(
                    "\nWINDOW: class {:?} on monitor {}, workspace {} (matched by {})\n\
        that workspace is being shown: {shown}\n",
                    place.class, place.monitor, place.workspace_name, place.matched_by
                )
            }
        }
        Ok(_) => {
            let mut out = format!(
                "\nWINDOW NOT MATCHED: no window with pid {}, and none whose class looks like {:?}.\n",
                state.pid, want.class
            );
            if !cfg.game.window_title_ignore.is_empty() {
                out.push_str(&format!(
                    "        (a title containing {:?} is skipped as the launcher: game.window_title_ignore)\n",
                    cfg.game.window_title_ignore.join(", ")
                ));
            }
            if let Ok(windows) = desktop::listed_windows() {
                out.push_str("\nTop-level windows the desktop knows about:\n");
                for window in windows {
                    let mut title = window.title;
                    if title.len() > 40 {
                        title.truncate(40);
                        title.push_str("...");
                    }
                    out.push_str(&format!(
                        "  class {:<32} pid {:<8} {}\n",
                        window.class, window.pid, title
                    ));
                }
                out.push_str(
                    "\nIf one of those is the game, set game.window_class to its class.\n",
                );
            }
            out.push_str(
                "\nUntil then the HUD cannot follow the game window, and monitor = \"auto\" leaves placement to the desktop.\n",
            );
            out
        }
    }
}

fn withheld_field(name: &str) -> bool {
    let low = name.to_ascii_lowercase();
    low == "sc" || WITHHELD_FIELD.iter().any(|n| low.contains(n))
}

pub fn dump_record_fields(vars: &std::collections::HashMap<String, String>) -> String {
    let mut names: Vec<&String> = vars.keys().collect();
    names.sort();
    let mut out = format!("{} fields returned:\n", names.len());
    for name in names {
        let value = if withheld_field(name) {
            "[withheld]"
        } else {
            vars[name].as_str()
        };
        out.push_str(&format!("  {name} = {value}\n"));
    }
    out
}

type OneshotContext = (Config, std::sync::Arc<Creds>, Client, Store);

fn oneshot_setup(config: Option<&Path>) -> Result<OneshotContext, Box<dyn Error>> {
    let path = config.map_or_else(config::default_path, Path::to_path_buf);
    let cfg = Config::load(&path)?;
    let (creds, catalog) = crate::app::load_creds_and_catalog(&cfg)?;
    let store = Store::new(catalog);
    let client = crate::app::df_client(&cfg);
    Ok((cfg, creds, client, store))
}

fn run_once(config: Option<&Path>, dump_fields: bool) -> Result<(), Box<dyn Error>> {
    let (cfg, creds, mut client, store) = oneshot_setup(config)?;
    let Some((cr, _)) = creds.get() else {
        return Err(
            "no credentials yet: load a Dead Frontier page with the bridge userscript".into(),
        );
    };
    client.cookie = cr.cookie.clone();
    let vars = client
        .get_values(&cr.to_df())
        .map_err(|e| format!("poll failed: {e}"))?;
    if dump_fields {
        print!("{}", dump_record_fields(&vars));
    }
    store.apply_tick(Tick {
        at: chrono::Utc::now(),
        vars,
        err: None,
        scheduled: false,
    });
    if let Ok(state) = game::scan(game_process(&cfg)) {
        store.set_game(state);
    }
    print!(
        "{}",
        model::marshal_indent(&store.derive(chrono::Utc::now()))?
    );
    Ok(())
}

fn dump_challenges(config: Option<&Path>, raw: bool) -> Result<(), Box<dyn Error>> {
    let (cfg, creds, mut client, store) = oneshot_setup(config)?;
    let Some((cr, salt_stored)) = creds.get() else {
        return Err(
            "no credentials yet: load a Dead Frontier page with the bridge userscript".into(),
        );
    };
    client.cookie = cr.cookie.clone();
    let salt = cfg.signing_salt(|| salt_stored.clone());
    if salt.is_empty() {
        return Err(
            "no signing salt: the bridge has not reported one, and df.skeygen is empty. \
Load the Outpost home page with the bridge userscript installed."
                .into(),
        );
    }
    let vars = client
        .load_challenge(&cr.to_df(), &salt)
        .map_err(|e| format!("load_challenge failed: {e}"))?;
    if raw {
        print!("{}", dump_record_fields(&vars));
        return Ok(());
    }
    if store.snapshot().is_none() {
        match client.get_values(&cr.to_df()) {
            Ok(player) => {
                store.apply_tick(Tick {
                    at: chrono::Utc::now(),
                    vars: player,
                    err: None,
                    scheduled: false,
                });
            }
            Err(err) => eprintln!("could not read your level ({err}); reward XP will be omitted"),
        }
    }
    let (level, gold) = store
        .snapshot()
        .map_or((0, false), |s| (s.level, s.gold_member));
    print!(
        "{}",
        format_challenge_board(&vars, level, gold, chrono::Utc::now())
    );
    Ok(())
}

fn format_challenge_board(
    vars: &std::collections::HashMap<String, String>,
    level: i32,
    gold: bool,
    now: chrono::DateTime<chrono::Utc>,
) -> String {
    let board = challenges::parse(vars, level, gold);
    let mut out = format!("{} challenges (level {level})\n", board.len());
    for challenge in board {
        let kind = if challenge.clan { "clan" } else { "personal" };
        let status = if challenge.complete() { "x" } else { " " };
        out.push_str(&format!("\n[{status}] {kind:<8} {}\n", challenge.name));
        let remaining = challenge.remaining(now);
        if !remaining.is_zero() {
            out.push_str(&format!(
                "        ends in {}\n",
                format::countdown(remaining)
            ));
        }
        for objective in &challenge.objectives {
            let frac = if objective.target <= 0 {
                0.0
            } else {
                objective.score as f64 / objective.target as f64 * 100.0
            };
            out.push_str(&format!(
                "        {:<28} {} / {}  ({frac:.0}%)\n",
                objective.name,
                format::int(objective.score),
                format::int(objective.target)
            ));
        }
        if challenge.reward_points > 0 {
            out.push_str(&format!(
                "        reward: {} clan points\n",
                challenge.reward_points
            ));
        } else if challenge.reward_exp > 0 {
            out.push_str(&format!(
                "        reward: {} xp\n",
                format::int(challenge.reward_exp)
            ));
        }
        if !challenge.reward_special.is_empty() {
            out.push_str(&format!("        reward: {}\n", challenge.reward_special));
        }
    }
    out
}

#[cfg(test)]
fn load_allstats_catalog(at: DateTime<Utc>) -> Result<catalog::Catalog, Box<dyn Error>> {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join("testdata/allstats.txt");
    let raw = std::fs::read_to_string(&path).map_err(|err| format!("{}: {err}", path.display()))?;
    let vars = crate::net::dfclient::parse_flash(&raw)?;
    catalog::parse(&vars, at).map_err(|err| err.into())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn parse(args: &[&str]) -> Result<Launch, String> {
        parse_from(args.iter().copied()).map_err(|err| err.to_string())
    }

    fn overlay(args: &[&str]) -> OverlayArgs {
        match parse(args).unwrap() {
            Launch::Overlay(args) => args,
            other => panic!("expected overlay, got {other:?}"),
        }
    }

    #[test]
    fn empty_argv_is_overlay() {
        let args = overlay(&[]);
        assert_eq!(args, OverlayArgs::default());
    }

    #[test]
    fn once_and_go_alias() {
        assert_eq!(
            parse(&["--once"]).unwrap(),
            Launch::Once {
                config: None,
                dump_fields: false
            }
        );
        assert_eq!(
            parse(&["-once", "-dump-fields"]).unwrap(),
            Launch::Once {
                config: None,
                dump_fields: true
            }
        );
    }

    #[test]
    fn unknown_flag_is_an_error() {
        let err = parse(&["--bogus"]).unwrap_err();
        assert!(err.contains("bogus"), "{err}");
        let err = parse(&["--once", "--bogus"]).unwrap_err();
        assert!(err.contains("bogus"), "{err}");
    }

    #[test]
    fn print_view_is_once() {
        let once = Launch::Once {
            config: None,
            dump_fields: false,
        };
        assert_eq!(parse(&["--print-view"]).unwrap(), once);
        assert_eq!(parse(&["-print-view"]).unwrap(), once);
        assert_eq!(parse(&["--once", "--print-view"]).unwrap(), once);
        assert_eq!(
            parse(&["--print-view", "--dump-fields"]).unwrap(),
            Launch::Once {
                config: None,
                dump_fields: true
            }
        );
        let err = parse(&["--print-view", "fx.json"]).unwrap_err();
        assert!(err.contains("fx.json"), "{err}");
    }

    #[test]
    fn help_wins() {
        assert_eq!(parse(&["--help", "--once"]).unwrap(), Launch::Help);
        assert_eq!(parse(&["-h"]).unwrap(), Launch::Help);
    }

    #[test]
    fn dump_fields_needs_a_oneshot() {
        let err = parse(&["--dump-fields"]).unwrap_err();
        assert!(err.contains("--dump-fields"), "{err}");
    }

    #[test]
    fn exclusive_oneshots_conflict() {
        let err = parse(&["--once", "--check-config"]).unwrap_err();
        assert!(err.contains("use only one"), "{err}");
    }

    #[test]
    fn headless_print_view_is_once() {
        assert_eq!(
            parse(&["--headless", "--print-view"]).unwrap(),
            Launch::Once {
                config: None,
                dump_fields: false
            }
        );
    }

    #[test]
    fn config_marks_overlay_requested() {
        let args = overlay(&["--config", "/tmp/df-hud.toml"]);
        assert!(args.requested);
        assert_eq!(args.config.as_deref(), Some(Path::new("/tmp/df-hud.toml")));
    }

    #[test]
    fn leftover_positional_is_an_error() {
        let err = parse(&["--once", "nope"]).unwrap_err();
        assert!(err.contains("nope"), "{err}");
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn linux_output_is_overlay() {
        let args = overlay(&["--output", "DP-1", "--duration", "2"]);
        assert_eq!(args.output.as_deref(), Some("DP-1"));
        assert_eq!(args.duration, Duration::from_secs_f32(2.0));
        assert!(args.requested);
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn linux_overlay_flag_rejected_on_once() {
        let err = parse(&["--once", "--output", "DP-1"]).unwrap_err();
        assert!(err.contains("--output"), "{err}");
        assert!(err.contains("--once"), "{err}");
    }

    #[cfg(windows)]
    #[test]
    fn windows_overlay_flag_rejected_on_once() {
        let err = parse(&["--once", "--monitor", r"\\.\DISPLAY1"]).unwrap_err();
        assert!(err.contains("--monitor"), "{err}");
        assert!(err.contains("--once"), "{err}");
    }

    fn fixture_path() -> PathBuf {
        Path::new(env!("CARGO_MANIFEST_DIR")).join("testdata/print-view.json")
    }

    #[test]
    fn fixture_derive_is_ground_zero() {
        let view = view_from_fixture(&fixture_path()).unwrap();
        assert!(view.have_data);
        assert!(view.game_running);
        assert!(!view.has_session);
        assert!(view.has_position);
        assert!(view.in_outpost);
        assert_eq!(view.outpost_name, "Ground Zero");
        assert_eq!(view.zone_name, "Outpost");
        assert_eq!(
            (view.position_x, view.position_y, view.position_z),
            (1058, 1019, 0)
        );
        assert!(!view.outpost_attack);
        assert!(view.challenges.as_ref().is_none_or(Vec::is_empty));
        assert!(view.status.is_empty());
        assert_eq!(
            crate::overlay::present::hud_lines(
                &view,
                &Config::default(),
                &crate::app::groups::Groups::new()
            ),
            ["Xp/Hr: --", "Ground Zero"]
        );
    }

    #[test]
    fn check_config_example_mentions_budget() {
        let example = Path::new(env!("CARGO_MANIFEST_DIR")).join("df-hud.example.toml");
        let text = check_config_text(Some(&example)).unwrap();
        assert!(text.starts_with("config ok (config "), "{text}");
        assert!(text.contains("request budget: about "), "{text}");
        assert!(text.contains("/hour while playing"), "{text}");
        assert!(text.contains("/hour idle"), "{text}");
        let playing = Config::default().requests_per_hour(1.0);
        let idle = Config::default().requests_per_hour(0.0);
        assert!(
            text.contains(&format!("{playing:.0}/hour while playing")),
            "{text}"
        );
        assert!(text.contains(&format!("{idle:.0}/hour idle")), "{text}");
    }

    #[test]
    fn check_config_missing_file_is_defaults() {
        let path = Path::new("/no/such/df-hud-config.toml");
        let text = check_config_text(Some(path)).unwrap();
        assert!(
            text.contains("built-in defaults, no config file at /no/such/df-hud-config.toml"),
            "{text}"
        );
    }

    #[test]
    fn dump_fields_withholds_secrets() {
        let mut vars = HashMap::new();
        vars.insert("df_level".into(), "50".into());
        vars.insert("password".into(), "hunter2".into());
        vars.insert("sc".into(), "abc".into());
        vars.insert("df_session3d".into(), "secret".into());
        vars.insert("auth_token".into(), "tok".into());
        let text = dump_record_fields(&vars);
        assert!(text.starts_with("5 fields returned:\n"), "{text}");
        assert!(text.contains("df_level = 50"), "{text}");
        assert!(text.contains("password = [withheld]"), "{text}");
        assert!(text.contains("sc = [withheld]"), "{text}");
        assert!(text.contains("df_session3d = [withheld]"), "{text}");
        assert!(text.contains("auth_token = [withheld]"), "{text}");
        assert!(!text.contains("hunter2"), "{text}");
    }

    #[test]
    fn check_game_starts_with_the_matching_rule() {
        let text = check_game_text(None).unwrap();
        let want = game::scan_description(game::DEFAULT_PROCESS);
        assert!(text.starts_with(&want), "{text}");
        assert!(
            text.contains("FOUND:") || text.contains("NOT FOUND"),
            "{text}"
        );
    }

    #[test]
    fn dump_challenges_prints_the_board() {
        let mut vars = HashMap::new();
        vars.insert("challenge_0_challenge_id".into(), "8017".into());
        vars.insert("challenge_0_name".into(), "Summer Death".into());
        vars.insert("challenge_0_min_level".into(), "1".into());
        vars.insert("challenge_0_max_level".into(), "415".into());
        vars.insert("challenge_0_objectives".into(), "1".into());
        vars.insert(
            "challenge_0_objectives_1_name".into(),
            "Kill Regular Infected".into(),
        );
        vars.insert("challenge_0_objectives_1_target".into(), "100".into());
        vars.insert("challenge_0_objective_1_player_score".into(), "55".into());
        vars.insert("challenge_0_reward_exp".into(), "2500".into());
        let now = chrono::DateTime::<chrono::Utc>::from_timestamp(
            crate::data::TIME_OFFSET + 584_880_000 + 60,
            0,
        )
        .unwrap();
        let text = format_challenge_board(&vars, 100, false, now);
        assert!(text.starts_with("1 challenges (level 100)\n"), "{text}");
        assert!(text.contains("[ ] personal Summer Death"), "{text}");
        assert!(text.contains("Kill Regular Infected"), "{text}");
        assert!(text.contains("55 / 100"), "{text}");
        assert!(text.contains("reward: 250,000 xp"), "{text}");
    }
}
