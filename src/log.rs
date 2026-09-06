//! `error!` / `warn!` / `info!` / `debug!` over stderr. Same text as before
//! (module prefix, no level tag); `DF_HUD_LOG` picks the lowest level that
//! still prints (`error|warn|info|debug`, default `info`). On Windows
//! `win32::init_stdio` has already pointed stderr at `df-hud.log`.

use std::sync::OnceLock;

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
}

fn threshold() -> Level {
    static THRESHOLD: OnceLock<Level> = OnceLock::new();
    *THRESHOLD.get_or_init(|| Level::from_env(std::env::var("DF_HUD_LOG").ok().as_deref()))
}

pub fn enabled(level: Level) -> bool {
    level <= threshold()
}

#[doc(hidden)]
pub fn write(args: std::fmt::Arguments<'_>) {
    eprintln!("{args}");
}

/// The level check happens before the arguments are formatted, so a filtered
/// line evaluates nothing: `debug!("{}", expensive())` costs one comparison.
macro_rules! log_at {
    ($level:expr, $($arg:tt)*) => {{
        if $crate::log::enabled($level) {
            $crate::log::write(format_args!($($arg)*));
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
