//! The one sink for diagnostics. `error!` / `warn!` / `info!` / `debug!`,
//! the run header (`start`) and the last words (`fatal`) all format here.
//! A line is `2026-11-05 14:47:03.120 WARN  <message>`; under systemd the
//! journal stamps lines itself, so those get a `<4>` priority prefix instead
//! (`journalctl -p warning` filters on it). `DF_HUD_LOG` picks the lowest
//! level that still prints (`error|warn|info|debug`, default `info`). On
//! Windows `win32::init_stdio` has already pointed stderr at `df-hud.log`.

use std::fmt::{self, Display};
use std::sync::OnceLock;

use chrono::{DateTime, Local};

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub enum Level {
    Error,
    Warn,
    Info,
    Debug,
}

impl Level {
    const DEFAULT: Level = Level::Info;

    /// `None` for anything but the four names (case-insensitive, trimmed).
    pub fn parse(s: &str) -> Option<Level> {
        match s.trim().to_ascii_lowercase().as_str() {
            "error" => Some(Level::Error),
            "warn" | "warning" => Some(Level::Warn),
            "info" => Some(Level::Info),
            "debug" => Some(Level::Debug),
            _ => None,
        }
    }

    /// Unset or unparsable → the default.
    pub fn from_env(var: Option<&str>) -> Level {
        var.and_then(Level::parse).unwrap_or(Level::DEFAULT)
    }

    fn tag(self) -> &'static str {
        match self {
            Level::Error => "ERROR",
            Level::Warn => "WARN",
            Level::Info => "INFO",
            Level::Debug => "DEBUG",
        }
    }

    /// sd-daemon(3) priority: err, warning, info, debug.
    fn syslog(self) -> u8 {
        match self {
            Level::Error => 3,
            Level::Warn => 4,
            Level::Info => 6,
            Level::Debug => 7,
        }
    }
}

fn threshold() -> Level {
    static THRESHOLD: OnceLock<Level> = OnceLock::new();
    *THRESHOLD.get_or_init(|| Level::from_env(std::env::var("DF_HUD_LOG").ok().as_deref()))
}

pub fn enabled(level: Level) -> bool {
    level <= threshold()
}

enum Prefix {
    Journal,
    Time(DateTime<Local>),
}

fn prefix() -> Prefix {
    static JOURNAL: OnceLock<bool> = OnceLock::new();
    if *JOURNAL.get_or_init(stderr_is_journal) {
        Prefix::Journal
    } else {
        Prefix::Time(Local::now())
    }
}

fn format_line(level: Level, prefix: Prefix, msg: impl Display) -> String {
    match prefix {
        Prefix::Journal => format!("<{}>{msg}", level.syslog()),
        Prefix::Time(at) => format!(
            "{} {:<5} {msg}",
            at.format("%Y-%m-%d %H:%M:%S%.3f"),
            level.tag()
        ),
    }
}

/// A finished line for a sink other than stderr (the Windows fatal log when
/// stderr is a console).
pub fn line(level: Level, msg: impl Display) -> String {
    format_line(level, prefix(), msg)
}

/// systemd sets `JOURNAL_STREAM=dev:inode` for the stream it connected; a
/// child that inherited the variable but writes to a terminal must not match.
#[cfg(target_os = "linux")]
fn stderr_is_journal() -> bool {
    use std::os::unix::fs::MetadataExt;
    let Some(stream) = std::env::var_os("JOURNAL_STREAM") else {
        return false;
    };
    let Some((dev, ino)) = stream.to_str().and_then(|s| s.split_once(':')) else {
        return false;
    };
    let Ok(meta) = std::fs::metadata("/proc/self/fd/2") else {
        return false;
    };
    dev.parse() == Ok(meta.dev()) && ino.parse() == Ok(meta.ino())
}

#[cfg(not(target_os = "linux"))]
fn stderr_is_journal() -> bool {
    false
}

#[doc(hidden)]
pub fn write(level: Level, args: fmt::Arguments<'_>) {
    eprintln!("{}", line(level, args));
}

/// The first line of a run: which build is talking. Printed at any level.
pub fn start() {
    let header = line(Level::Info, format_args!("--- df-hud {} ---", crate::BUILD));
    match prefix() {
        Prefix::Journal => eprintln!("{header}"),
        Prefix::Time(_) => eprintln!("\n{header}"),
    }
}

/// The error that ends the process. Printed at any level.
pub fn fatal(err: &dyn Display) {
    write(Level::Error, format_args!("fatal: {err}"));
}

/// The level check happens before the arguments are formatted, so a filtered
/// line evaluates nothing: `debug!("{}", expensive())` costs one comparison.
macro_rules! log_at {
    ($level:expr, $($arg:tt)*) => {{
        if $crate::log::enabled($level) {
            $crate::log::write($level, format_args!($($arg)*));
        }
    }};
}
macro_rules! error {
    ($($arg:tt)*) => { log_at!($crate::log::Level::Error, $($arg)*) };
}
macro_rules! warn {
    ($($arg:tt)*) => { log_at!($crate::log::Level::Warn, $($arg)*) };
}
macro_rules! info {
    ($($arg:tt)*) => { log_at!($crate::log::Level::Info, $($arg)*) };
}
macro_rules! debug {
    ($($arg:tt)*) => { log_at!($crate::log::Level::Debug, $($arg)*) };
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::TimeZone;

    #[test]
    fn parse_accepts_the_four_names_in_any_case() {
        assert_eq!(Level::parse("error"), Some(Level::Error));
        assert_eq!(Level::parse(" WARN "), Some(Level::Warn));
        assert_eq!(Level::parse("Info"), Some(Level::Info));
        assert_eq!(Level::parse("debug"), Some(Level::Debug));
        assert_eq!(Level::parse("trace"), None);
        assert_eq!(Level::parse(""), None);
    }

    #[test]
    fn env_falls_back_to_info() {
        assert_eq!(Level::from_env(None), Level::Info);
        assert_eq!(Level::from_env(Some("loud")), Level::Info);
        assert_eq!(Level::from_env(Some("error")), Level::Error);
    }

    #[test]
    fn a_threshold_admits_itself_and_everything_more_severe() {
        assert!(Level::Error <= Level::Warn);
        assert!(Level::Warn <= Level::Info);
        assert!(Level::Info <= Level::Debug);
        assert!(Level::Debug > Level::Info, "debug is the chattiest");
    }

    #[test]
    fn a_file_line_carries_time_and_level() {
        let at = Local.with_ymd_and_hms(2026, 11, 5, 14, 47, 3).unwrap();
        assert_eq!(
            format_line(Level::Warn, Prefix::Time(at), "hud: hiding"),
            "2026-11-05 14:47:03.000 WARN  hud: hiding"
        );
        assert_eq!(
            format_line(Level::Error, Prefix::Time(at), "x"),
            "2026-11-05 14:47:03.000 ERROR x"
        );
    }

    #[test]
    fn a_journal_line_leaves_the_time_to_journald() {
        assert_eq!(
            format_line(Level::Warn, Prefix::Journal, "hud: hiding"),
            "<4>hud: hiding"
        );
        assert_eq!(format_line(Level::Debug, Prefix::Journal, "x"), "<7>x");
    }

    #[test]
    fn a_filtered_line_does_not_evaluate_its_arguments() {
        use std::cell::Cell;
        let evaluated = Cell::new(0u32);
        let tick = || {
            evaluated.set(evaluated.get() + 1);
            "unused"
        };
        // `error` always passes the threshold; `debug` only under
        // DF_HUD_LOG=debug, which the test environment may or may not set.
        error!("log test: {}", tick());
        assert_eq!(evaluated.get(), 1);
        debug!("log test: {}", tick());
        assert_eq!(
            evaluated.get(),
            if enabled(Level::Debug) { 2 } else { 1 },
            "arguments are formatted only for lines that print"
        );
    }
}
