//! The `log` sink. `DF_HUD_LOG` picks the lowest level df-hud prints
//! (`error|warn|info|debug|trace`, default `info`); libraries print warnings
//! and errors only.

use std::fmt::{self, Display};
use std::io::Write;
use std::sync::OnceLock;

use chrono::{DateTime, Local};
use log::{Level, LevelFilter, Metadata, Record};

const LIBRARIES: LevelFilter = LevelFilter::Warn;

/// `None` for anything `log` does not name, except the `warning` alias.
pub fn parse_level(s: &str) -> Option<LevelFilter> {
    let s = s.trim();
    if s.eq_ignore_ascii_case("warning") {
        return Some(LevelFilter::Warn);
    }
    s.parse().ok()
}

fn from_env(var: Option<&str>) -> LevelFilter {
    var.and_then(parse_level).unwrap_or(LevelFilter::Info)
}

struct Logger {
    own: LevelFilter,
}

impl Logger {
    fn admits(&self, meta: &Metadata<'_>) -> bool {
        let limit = if is_own(meta.target()) {
            self.own
        } else {
            LIBRARIES
        };
        meta.level() <= limit
    }
}

fn is_own(target: &str) -> bool {
    target == "df_hud" || target.starts_with("df_hud::")
}

impl log::Log for Logger {
    fn enabled(&self, meta: &Metadata<'_>) -> bool {
        self.admits(meta)
    }

    fn log(&self, record: &Record<'_>) {
        if !self.admits(record.metadata()) {
            return;
        }
        if is_own(record.target()) {
            write(record.level(), *record.args());
        } else {
            write(
                record.level(),
                format_args!("{}: {}", record.target(), record.args()),
            );
        }
    }

    fn flush(&self) {}
}

pub fn init() {
    static LOGGER: OnceLock<Logger> = OnceLock::new();
    let logger = LOGGER.get_or_init(|| Logger {
        own: from_env(std::env::var("DF_HUD_LOG").ok().as_deref()),
    });
    if log::set_logger(logger).is_ok() {
        log::set_max_level(logger.own.max(LIBRARIES));
    }
}

fn tag(level: Level) -> &'static str {
    match level {
        Level::Error => "ERROR",
        Level::Warn => "WARN",
        Level::Info => "INFO",
        Level::Debug => "DEBUG",
        Level::Trace => "TRACE",
    }
}

fn syslog(level: Level) -> u8 {
    match level {
        Level::Error => 3,
        Level::Warn => 4,
        Level::Info => 6,
        Level::Debug | Level::Trace => 7,
    }
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
    let msg = msg.to_string();
    let mut lines = msg.lines().filter(|l| !l.trim().is_empty());
    let (head, more) = match prefix {
        Prefix::Journal => {
            let p = format!("<{}>", syslog(level));
            let more = format!("{p}    ");
            (p, more)
        }
        Prefix::Time(at) => {
            let p = format!("{} {:<5} ", at.format("%Y-%m-%d %H:%M:%S%.3f"), tag(level));
            let more = " ".repeat(p.len());
            (p, more)
        }
    };
    let mut out = head + lines.next().unwrap_or("");
    for line in lines {
        out.push('\n');
        out.push_str(&more);
        out.push_str(line);
    }
    out
}

pub fn line(level: Level, msg: impl Display) -> String {
    format_line(level, prefix(), msg)
}

/// The variable is inherited by children whose stderr may be a terminal.
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

/// `eprintln!` panics on a failed write, and release builds abort on panic.
fn emit(text: &str) {
    let _ = std::io::stderr().write_all(text.as_bytes());
}

fn write(level: Level, args: fmt::Arguments<'_>) {
    let mut text = line(level, args);
    text.push('\n');
    emit(&text);
}

pub fn start() {
    let header = format!(
        "--- df-hud {} (UTC{}) ---",
        crate::BUILD,
        Local::now().format("%:z")
    );
    if !matches!(prefix(), Prefix::Journal) {
        emit("\n");
    }
    write(Level::Info, format_args!("{header}"));
}

pub fn fatal(err: &dyn Display) {
    write(Level::Error, format_args!("fatal: {err}"));
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::TimeZone;

    #[test]
    fn levels_parse_in_any_case_with_a_warning_alias() {
        assert_eq!(parse_level("error"), Some(LevelFilter::Error));
        assert_eq!(parse_level(" WARN "), Some(LevelFilter::Warn));
        assert_eq!(parse_level("Warning"), Some(LevelFilter::Warn));
        assert_eq!(parse_level("debug"), Some(LevelFilter::Debug));
        assert_eq!(parse_level("loud"), None);
        assert_eq!(from_env(None), LevelFilter::Info);
        assert_eq!(from_env(Some("loud")), LevelFilter::Info);
    }

    #[test]
    fn libraries_stop_at_warn_while_df_hud_follows_the_env() {
        let logger = Logger {
            own: LevelFilter::Debug,
        };
        let meta = |target, level| Metadata::builder().target(target).level(level).build();
        assert!(logger.admits(&meta("df_hud::overlay::win32", Level::Debug)));
        assert!(logger.admits(&meta("rustls::conn", Level::Warn)));
        assert!(!logger.admits(&meta("rustls::conn", Level::Info)));
        assert!(!logger.admits(&meta("df_hudx", Level::Info)));
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
    fn a_multi_line_message_stays_one_indented_entry() {
        let at = Local.with_ymd_and_hms(2026, 11, 5, 14, 47, 3).unwrap();
        let msg = "fatal: could not be rebuilt.\n\nwglMakeCurrent: Win32 error 6";
        let indent = " ".repeat("2026-11-05 14:47:03.000 ERROR ".len());
        assert_eq!(
            format_line(Level::Error, Prefix::Time(at), msg),
            format!(
                "2026-11-05 14:47:03.000 ERROR fatal: could not be rebuilt.\n\
                 {indent}wglMakeCurrent: Win32 error 6"
            )
        );
        assert_eq!(
            format_line(Level::Error, Prefix::Journal, msg),
            "<3>fatal: could not be rebuilt.\n<3>    wglMakeCurrent: Win32 error 6"
        );
    }
}
