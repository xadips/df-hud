//! XP/hr from the sample window. Oldest and newest endpoints, not a fit.

use chrono::{DateTime, Utc};
use std::time::Duration;

use crate::model::{Snapshot, XpRate, XpSample, XpStability};

pub fn compute_rate(samples: &[XpSample], mut min_samples: i32, stability: XpStability) -> XpRate {
    if min_samples < 2 {
        min_samples = 2;
    }
    if samples.len() < 2 {
        return XpRate {
            samples: samples.len() as i32,
            why: "collecting samples".into(),
            ..XpRate::default()
        };
    }
    let provisional = (samples.len() as i32) < min_samples;
    let oldest = &samples[0];
    let newest = &samples[samples.len() - 1];
    if oldest.source != newest.source {
        return XpRate {
            samples: samples.len() as i32,
            why: "XP source changed".into(),
            ..XpRate::default()
        };
    }
    let span = newest.at - oldest.at;
    if span <= chrono::Duration::zero() {
        return XpRate {
            samples: samples.len() as i32,
            why: "no elapsed time".into(),
            ..XpRate::default()
        };
    }
    let gained = newest.cumulative - oldest.cumulative;
    if gained < 0 {
        return XpRate {
            samples: samples.len() as i32,
            why: "XP went backwards".into(),
            ..XpRate::default()
        };
    }
    let secs = span.num_milliseconds() as f64 / 1000.0;
    let mut rate = XpRate {
        available: true,
        per_hour: gained as f64 / secs * 3600.0,
        gained,
        span: span.to_std().unwrap_or_default(),
        samples: samples.len() as i32,
        stability,
        provisional,
        why: String::new(),
    };
    if provisional {
        rate.why = format!("provisional: {} of {} samples", samples.len(), min_samples);
    }
    rate
}

/// Whether the rate window must be discarded because a new run began.
///
/// A run ending is not a reset: the last rate is still the last true thing
/// the widget can say, and blanking it in an outpost throws the number away
/// exactly when you want to read it.
pub fn run_reset(prev: Option<DateTime<Utc>>, next: Option<DateTime<Utc>>) -> bool {
    next.is_some() && next != prev
}

/// Why the rate window must be discarded when moving from one snapshot to the
/// next, or `None` to keep it.
///
/// Each of these makes the samples either side incomparable: a boost starting
/// or ending, cumulative XP falling, the clock jumping backwards, or a gap
/// much longer than the window. A change in `in_outpost` is not a reason; that
/// field does not mean what its name suggests.
pub fn window_reset(prev: &Snapshot, next: &Snapshot, window: Duration) -> Option<&'static str> {
    if prev.at.timestamp() == 0 {
        return None;
    }
    if next.at < prev.at {
        return Some("the clock went backwards");
    }
    if prev.boost_exp != next.boost_exp {
        return Some("the XP boost changed");
    }
    if next.cumulative_xp < prev.cumulative_xp {
        return Some("cumulative XP fell (death or correction)");
    }
    if !window.is_zero() {
        let gap = next.at.signed_duration_since(prev.at);
        if let Ok(limit) = chrono::Duration::from_std(window.saturating_mul(2))
            && gap > limit
        {
            return Some("a long gap between samples");
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::{TimeZone, Utc};

    use crate::model::{Deadline, XpStability};

    fn sample_ring(
        start: DateTime<Utc>,
        n: usize,
        spacing: chrono::Duration,
        base: i64,
        per_sample: i64,
    ) -> Vec<XpSample> {
        (0..n)
            .map(|i| XpSample {
                at: start + spacing * i as i32,
                cumulative: base + i as i64 * per_sample,
                source: "df_exptotal".into(),
            })
            .collect()
    }

    #[test]
    fn rate_from_three_samples() {
        let start = Utc.timestamp_opt(1_786_484_051, 0).unwrap();
        let samples = vec![
            XpSample {
                at: start,
                cumulative: 1_000_000,
                source: "df_exptotal".into(),
            },
            XpSample {
                at: start + chrono::Duration::seconds(10),
                cumulative: 1_001_000,
                source: "df_exptotal".into(),
            },
            XpSample {
                at: start + chrono::Duration::seconds(20),
                cumulative: 1_002_000,
                source: "df_exptotal".into(),
            },
        ];
        let rate = compute_rate(&samples, 3, XpStability::Steady);
        assert!(rate.available);
        assert_eq!(rate.per_hour, 360_000.0);
        assert_eq!(rate.gained, 2_000);
        assert!(!rate.provisional);
    }

    #[test]
    fn compute_rate_details() {
        let start = Utc.timestamp_opt(1_786_484_051, 0).unwrap();
        let rate = compute_rate(
            &sample_ring(start, 4, chrono::Duration::seconds(10), 1_000_000, 1000),
            3,
            XpStability::Steady,
        );
        assert!(rate.available);
        assert_eq!(rate.per_hour, 360_000.0);
        assert_eq!(rate.gained, 3000);
        assert_eq!(rate.span, std::time::Duration::from_secs(30));
        assert_eq!(rate.samples, 4);
    }

    #[test]
    fn compute_rate_rejects_mixed_sources() {
        let start = Utc.timestamp_opt(1_786_484_051, 0).unwrap();
        let rate = compute_rate(
            &[
                XpSample {
                    at: start,
                    cumulative: 1_000,
                    source: "df_exptotal".into(),
                },
                XpSample {
                    at: start + chrono::Duration::seconds(1),
                    cumulative: 900,
                    source: "exp table reconstruction".into(),
                },
            ],
            2,
            XpStability::Steady,
        );
        assert!(!rate.available);
        assert_eq!(rate.why, "XP source changed");
    }

    #[test]
    fn window_reset_conditions() {
        let start = Utc.timestamp_opt(1_786_484_051, 0).unwrap();
        let prev = Snapshot {
            at: start,
            cumulative_xp: 1_000,
            ..Snapshot::default()
        };
        let next = Snapshot {
            at: start + chrono::Duration::seconds(1),
            cumulative_xp: 1_100,
            ..Snapshot::default()
        };
        assert_eq!(
            window_reset(&prev, &next, Duration::from_secs(60)),
            None,
            "ordinary step reset"
        );
        let boosted = Snapshot {
            boost_exp: Deadline {
                forever: true,
                ..Deadline::default()
            },
            ..next
        };
        assert_eq!(
            window_reset(&prev, &boosted, Duration::from_secs(60)),
            Some("the XP boost changed")
        );
    }

    #[test]
    fn window_reset_all_conditions() {
        let base = Utc.timestamp_opt(1_786_484_051, 0).unwrap();
        let window = Duration::from_secs(30);
        let prev = Snapshot {
            at: base,
            cumulative_xp: 1_000_000,
            ..Snapshot::default()
        };
        let next = Snapshot {
            at: base + chrono::Duration::seconds(10),
            cumulative_xp: 1_001_000,
            ..Snapshot::default()
        };
        assert_eq!(
            window_reset(&Snapshot::default(), &next, window),
            None,
            "first snapshot reset"
        );
        for (want, snap) in [
            (
                "the clock went backwards",
                Snapshot {
                    at: base - chrono::Duration::minutes(1),
                    cumulative_xp: 1_001_000,
                    ..Snapshot::default()
                },
            ),
            (
                "cumulative XP fell (death or correction)",
                Snapshot {
                    at: base + chrono::Duration::seconds(10),
                    cumulative_xp: 900_000,
                    ..Snapshot::default()
                },
            ),
            (
                "a long gap between samples",
                Snapshot {
                    at: base + chrono::Duration::minutes(5),
                    cumulative_xp: 1_001_000,
                    ..Snapshot::default()
                },
            ),
        ] {
            assert_eq!(window_reset(&prev, &snap, window), Some(want));
        }
        let boosted = Snapshot {
            boost_exp: Deadline {
                forever: true,
                ..Deadline::default()
            },
            ..next.clone()
        };
        assert_eq!(
            window_reset(&prev, &boosted, window),
            Some("the XP boost changed")
        );
        let prev_boosted = Snapshot {
            boost_exp: Deadline {
                forever: true,
                ..Deadline::default()
            },
            ..prev.clone()
        };
        assert_eq!(
            window_reset(&prev_boosted, &next, window),
            Some("the XP boost changed")
        );
        assert_eq!(
            window_reset(&prev_boosted, &boosted, window),
            None,
            "unchanged boost reset"
        );
    }

    #[test]
    fn window_reset_ignores_outpost_flag() {
        let now = Utc::now();
        let outpost = Snapshot {
            at: now,
            in_outpost: true,
            cumulative_xp: 1000,
            ..Snapshot::default()
        };
        let city = Snapshot {
            at: now + chrono::Duration::seconds(10),
            in_outpost: false,
            cumulative_xp: 1000,
            ..Snapshot::default()
        };
        assert_eq!(
            window_reset(&outpost, &city, Duration::from_secs(5 * 60)),
            None
        );
    }

    #[test]
    fn run_reset_boundaries() {
        let none = None;
        let first = Some(Utc::now());
        assert!(run_reset(none, first));
        assert!(!run_reset(first, first));
        assert!(run_reset(
            first,
            first.map(|t| t + chrono::Duration::hours(1))
        ));
        assert!(!run_reset(first, none));
    }
}
