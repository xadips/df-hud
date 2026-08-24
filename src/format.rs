//! HUD display formatting.

use std::time::Duration;

pub fn int(n: i64) -> String {
    let s = n.abs().to_string();
    let mut out = String::new();
    if n < 0 {
        out.push('-');
    }
    for (i, c) in s.chars().enumerate() {
        if i > 0 && (s.len() - i) % 3 == 0 {
            out.push(',');
        }
        out.push(c);
    }
    out
}

/// Session clock: H:MM:SS, growing past 24h rather than wrapping.
pub fn clock(d: Duration) -> String {
    let total = d.as_secs();
    let h = total / 3600;
    let m = (total / 60) % 60;
    let s = total % 60;
    format!("{h}:{m:02}:{s:02}")
}

/// Expiration durations at a deliberately coarse precision.
pub fn countdown(d: Duration) -> String {
    if d.is_zero() {
        return String::new();
    }
    let secs = d.as_secs();
    if secs < 60 {
        return format!("{secs}s");
    }
    if secs < 3600 {
        let m = secs / 60;
        let s = secs % 60;
        if s == 0 {
            format!("{m}m")
        } else {
            format!("{m}m {s:02}s")
        }
    } else if secs < 24 * 3600 {
        let h = secs / 3600;
        let m = (secs / 60) % 60;
        if m == 0 {
            format!("{h}h")
        } else {
            format!("{h}h {m:02}m")
        }
    } else {
        let days = secs / (24 * 3600);
        let h = (secs / 3600) % 24;
        if h == 0 {
            format!("{days}d")
        } else {
            format!("{days}d {h}h")
        }
    }
}

/// Stale-reading age; silent below 30 seconds.
#[cfg(test)]
pub fn age(d: Duration) -> String {
    if d < Duration::from_secs(30) {
        String::new()
    } else {
        format!("{} ago", countdown(d))
    }
}

pub fn rate(per_hour: f64) -> String {
    if per_hour < 0.0 {
        String::new()
    } else {
        int(per_hour as i64)
    }
}

#[cfg(test)]
pub fn cash(n: i64) -> String {
    format!("${}", int(n))
}

pub fn position(x: i32, y: i32, z: i32) -> String {
    if z != 0 {
        format!("{x}, {y} (floor {z})")
    } else {
        format!("{x}, {y}")
    }
}

#[cfg(test)]
pub fn exp_progress(exp: i64, needed: i64) -> String {
    if needed <= 0 {
        int(exp)
    } else {
        format!("{} / {}", int(exp), int(needed))
    }
}

#[cfg(test)]
pub fn danger_level(level: i32) -> String {
    level.to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn int_grouping() {
        let cases: &[(i64, &str)] = &[
            (0, "0"),
            (5, "5"),
            (42, "42"),
            (999, "999"),
            (1000, "1,000"),
            (12345, "12,345"),
            (176_000_000, "176,000,000"),
            (10_000_000_000, "10,000,000,000"),
            (-1234, "-1,234"),
            (-999, "-999"),
        ];
        for &(in_, want) in cases {
            assert_eq!(int(in_), want, "int({in_})");
        }
    }

    #[test]
    fn clock_hms() {
        let cases: &[(Duration, &str)] = &[
            (Duration::ZERO, "0:00:00"),
            (Duration::from_secs(42), "0:00:42"),
            (Duration::from_secs(90), "0:01:30"),
            (Duration::from_secs(3600 + 23 * 60 + 45), "1:23:45"),
            (Duration::from_secs(26 * 3600 + 60), "26:01:00"),
        ];
        for &(in_, want) in cases {
            assert_eq!(clock(in_), want);
        }
    }

    #[test]
    fn countdown_and_age() {
        let cases: &[(Duration, &str)] = &[
            (Duration::ZERO, ""),
            (Duration::from_secs(45), "45s"),
            (Duration::from_secs(120), "2m"),
            (Duration::from_secs(125), "2m 05s"),
            (Duration::from_secs(3 * 3600), "3h"),
            (Duration::from_secs(3 * 3600 + 7 * 60), "3h 07m"),
            (Duration::from_secs(50 * 3600), "2d 2h"),
            (Duration::from_secs(48 * 3600), "2d"),
        ];
        for &(in_, want) in cases {
            assert_eq!(countdown(in_), want, "countdown({in_:?})");
        }
        assert_eq!(age(Duration::from_secs(2)), "");
        assert_eq!(age(Duration::from_secs(5 * 60)), "5m ago");
    }

    #[test]
    fn other_formats() {
        assert_eq!(rate(0.0), "0");
        assert_eq!(rate(58_143_000.0), "58,143,000");
        assert_eq!(rate(1_234_567.9), "1,234,567");
        assert_eq!(rate(-1.0), "");
        assert_eq!(cash(1000), "$47,128");
        assert_eq!(position(1058, 1019, 2), "1058, 1019 (floor 2)");
        assert_eq!(position(1058, 1019, 0), "1058, 1019");
        assert_eq!(exp_progress(1000, 5000), "1,000 / 5,000");
        assert_eq!(exp_progress(1000, 0), "1,000");
        assert_eq!(danger_level(42), "42");
    }
}
