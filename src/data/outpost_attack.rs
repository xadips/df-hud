//! The outpost attack schedule: every outpost at once, 20 minutes, every
//! three hours on the game's US Eastern clock (01, 04, ... 22 local).

use chrono::{DateTime, Datelike, Duration, NaiveDate, Timelike, Utc, Weekday};

pub const DURATION: Duration = Duration::minutes(20);

/// UTC hour of the US daylight-saving switch: 02:00 local, on standard time
/// in March and on daylight time in November.
const DST_START_UTC_HOUR: u32 = 7;
const DST_END_UTC_HOUR: u32 = 6;

fn nth_sunday(year: i32, month: u32, n: u8) -> NaiveDate {
    NaiveDate::from_weekday_of_month_opt(year, month, Weekday::Sun, n).unwrap_or(NaiveDate::MIN)
}

/// The US rule in force since 2007.
fn eastern_offset_hours(t: DateTime<Utc>) -> i64 {
    let year = t.year();
    let start = nth_sunday(year, 3, 2)
        .and_hms_opt(DST_START_UTC_HOUR, 0, 0)
        .unwrap_or_default()
        .and_utc();
    let end = nth_sunday(year, 11, 1)
        .and_hms_opt(DST_END_UTC_HOUR, 0, 0)
        .unwrap_or_default()
        .and_utc();
    if t >= start && t < end { -4 } else { -5 }
}

fn is_slot(t: DateTime<Utc>) -> bool {
    let local = t + Duration::hours(eastern_offset_hours(t));
    local.hour() % 3 == 1
}

fn hour_floor(t: DateTime<Utc>) -> DateTime<Utc> {
    t.with_minute(0)
        .and_then(|t| t.with_second(0))
        .and_then(|t| t.with_nanosecond(0))
        .unwrap_or(t)
}

/// The first scheduled start strictly after `now`. On the night the clocks go
/// back, local 01:00 happens twice and both count, so that night has one
/// slot the game may not run.
pub fn next_start(now: DateTime<Utc>) -> DateTime<Utc> {
    let mut t = hour_floor(now) + Duration::hours(1);
    for _ in 0..4 {
        if is_slot(t) {
            return t;
        }
        t += Duration::hours(1);
    }
    t
}

/// When the scheduled attack covering `now` ends, if one does.
pub fn live_end(now: DateTime<Utc>) -> Option<DateTime<Utc>> {
    let start = hour_floor(now);
    (is_slot(start) && now < start + DURATION).then_some(start + DURATION)
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::TimeZone;

    fn utc(y: i32, mo: u32, d: u32, h: u32, mi: u32) -> DateTime<Utc> {
        Utc.with_ymd_and_hms(y, mo, d, h, mi, 0).unwrap()
    }

    #[test]
    fn live_capture_is_a_slot() {
        // The feed carried 15:00-15:20 UTC on 2026-11-19.
        assert_eq!(
            next_start(utc(2026, 11, 19, 14, 30)),
            utc(2026, 11, 19, 15, 0)
        );
        assert_eq!(
            live_end(utc(2026, 11, 19, 15, 5)),
            Some(utc(2026, 11, 19, 15, 20))
        );
        assert_eq!(live_end(utc(2026, 11, 19, 15, 20)), None);
        assert_eq!(
            next_start(utc(2026, 11, 19, 15, 24)),
            utc(2026, 11, 19, 18, 0)
        );
    }

    #[test]
    fn winter_and_summer_tables() {
        let winter: Vec<u32> = (0..24)
            .filter(|&h| is_slot(utc(2026, 1, 15, h, 0)))
            .collect();
        assert_eq!(winter, [0, 3, 6, 9, 12, 15, 18, 21]);
        let summer: Vec<u32> = (0..24)
            .filter(|&h| is_slot(utc(2026, 7, 15, h, 0)))
            .collect();
        assert_eq!(summer, [2, 5, 8, 11, 14, 17, 20, 23]);
    }

    #[test]
    fn spring_forward() {
        // 2026-03-08 is the second Sunday; 07:00 UTC is 02:00 EST -> 03:00 EDT.
        assert_eq!(next_start(utc(2026, 3, 8, 6, 1)), utc(2026, 3, 8, 8, 0));
        assert_eq!(next_start(utc(2026, 3, 8, 3, 0)), utc(2026, 3, 8, 6, 0));
    }

    #[test]
    fn fall_back() {
        // 2026-11-01 is the first Sunday; 06:00 UTC is 02:00 EDT -> 01:00 EST.
        assert_eq!(next_start(utc(2026, 11, 1, 2, 30)), utc(2026, 11, 1, 5, 0));
        assert_eq!(next_start(utc(2026, 11, 1, 5, 30)), utc(2026, 11, 1, 6, 0));
        assert_eq!(next_start(utc(2026, 11, 1, 6, 30)), utc(2026, 11, 1, 9, 0));
    }

    #[test]
    fn next_start_is_strictly_after() {
        assert_eq!(
            next_start(utc(2026, 11, 19, 15, 0)),
            utc(2026, 11, 19, 18, 0)
        );
        let just_before = utc(2026, 11, 19, 15, 0) - Duration::nanoseconds(1);
        assert_eq!(next_start(just_before), utc(2026, 11, 19, 15, 0));
    }

    #[test]
    fn live_end_edges() {
        let start = utc(2026, 11, 19, 15, 0);
        assert_eq!(live_end(start), Some(start + DURATION));
        assert_eq!(live_end(start - Duration::seconds(1)), None);
        // Switch days: 06:10 UTC is 01:10 EST after fall-back; 07:10 UTC on
        // spring-forward day is 03:10 EDT.
        assert!(live_end(utc(2026, 11, 1, 6, 10)).is_some());
        assert!(live_end(utc(2026, 3, 8, 7, 10)).is_none());
    }

    #[test]
    fn switch_instants_and_latest_dates() {
        let second = Duration::seconds(1);
        assert_eq!(eastern_offset_hours(utc(2026, 3, 8, 7, 0) - second), -5);
        assert_eq!(eastern_offset_hours(utc(2026, 3, 8, 7, 0)), -4);
        assert_eq!(eastern_offset_hours(utc(2026, 11, 1, 6, 0) - second), -4);
        assert_eq!(eastern_offset_hours(utc(2026, 11, 1, 6, 0)), -5);
        // 2027 has the latest possible switches: March 14 and November 7.
        assert_eq!(eastern_offset_hours(utc(2027, 3, 13, 12, 0)), -5);
        assert_eq!(eastern_offset_hours(utc(2027, 3, 14, 12, 0)), -4);
        assert_eq!(eastern_offset_hours(utc(2027, 11, 6, 12, 0)), -4);
        assert_eq!(eastern_offset_hours(utc(2027, 11, 7, 12, 0)), -5);
    }

    #[test]
    fn next_start_is_always_a_later_slot() {
        let mut t = utc(2026, 1, 1, 0, 0);
        while t < utc(2028, 1, 1, 0, 0) {
            let n = next_start(t);
            assert!(n > t && is_slot(n) && n - t <= Duration::hours(3), "{t}");
            t += Duration::minutes(7);
        }
    }
}
