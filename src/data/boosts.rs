//! Boost timers as they were bought. A pack covering several stats sets each
//! of them to the same expiry, so stats of one tier that share a deadline are
//! one boost, and only stats with different deadlines get separate timers.

use chrono::{DateTime, Utc};

use crate::model::{BoostGroup, BoostStat, BoostTier, Boosts, Deadline, Ns};

pub fn group(normal: Boosts, ex: Boosts, now: DateTime<Utc>) -> Vec<BoostGroup> {
    let mut out = tier(BoostTier::Normal, normal, now);
    out.extend(tier(BoostTier::Ex, ex, now));
    out
}

fn tier(tier: BoostTier, b: Boosts, now: DateTime<Utc>) -> Vec<BoostGroup> {
    let mut shared: Vec<(Deadline, Vec<BoostStat>)> = Vec::new();
    for (stat, deadline) in BoostStat::ALL.into_iter().zip([b.exp, b.damage, b.speed]) {
        if !deadline.active(now) {
            continue;
        }
        match shared.iter_mut().find(|(d, _)| *d == deadline) {
            Some((_, stats)) => stats.push(stat),
            None => shared.push((deadline, vec![stat])),
        }
    }
    let mut out: Vec<BoostGroup> = shared
        .into_iter()
        .map(|(deadline, stats)| BoostGroup {
            tier,
            stats,
            left: match deadline {
                Deadline::Forever => None,
                _ => Some(Ns::from_std(deadline.remaining(now))),
            },
        })
        .collect();
    out.sort_by_key(|g| g.left.map_or(i64::MAX, |n| n.0));
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;

    fn now() -> DateTime<Utc> {
        DateTime::from_timestamp(1_793_117_789, 0).unwrap()
    }

    fn at(secs: i64) -> Deadline {
        Deadline::At(now() + chrono::Duration::seconds(secs))
    }

    fn left(secs: u64) -> Option<Ns> {
        Some(Ns::from_std(Duration::from_secs(secs)))
    }

    fn all(d: Deadline) -> Boosts {
        Boosts {
            exp: d,
            damage: d,
            speed: d,
        }
    }

    #[test]
    fn ultra_boost_with_expired_ex_boosts_is_one_line() {
        let g = group(all(Deadline::Forever), all(at(-12_456_526)), now());
        assert_eq!(
            g,
            vec![BoostGroup {
                tier: BoostTier::Normal,
                stats: BoostStat::ALL.to_vec(),
                left: None,
            }]
        );
        assert!(g[0].ultra());
    }

    #[test]
    fn one_pack_is_one_timer_per_tier() {
        let g = group(all(at(3600)), all(at(600)), now());
        assert_eq!(
            g,
            vec![
                BoostGroup {
                    tier: BoostTier::Normal,
                    stats: BoostStat::ALL.to_vec(),
                    left: left(3600),
                },
                BoostGroup {
                    tier: BoostTier::Ex,
                    stats: BoostStat::ALL.to_vec(),
                    left: left(600),
                },
            ]
        );
        assert!(!g[0].ultra(), "timed, so not an ultra boost");
    }

    #[test]
    fn different_expiries_split_and_matching_ones_stay_together() {
        let normal = Boosts {
            exp: at(600),
            damage: at(3600),
            speed: at(600),
        };
        let g = group(normal, Boosts::default(), now());
        assert_eq!(
            g,
            vec![
                BoostGroup {
                    tier: BoostTier::Normal,
                    stats: vec![BoostStat::Exp, BoostStat::Speed],
                    left: left(600),
                },
                BoostGroup {
                    tier: BoostTier::Normal,
                    stats: vec![BoostStat::Damage],
                    left: left(3600),
                },
            ]
        );
    }

    #[test]
    fn equal_expiries_in_different_tiers_are_not_merged() {
        let normal = Boosts {
            exp: at(600),
            ..Boosts::default()
        };
        let g = group(normal, normal, now());
        assert_eq!(g.len(), 2);
        assert_eq!(g[0].tier, BoostTier::Normal);
        assert_eq!(g[1].tier, BoostTier::Ex);
    }

    #[test]
    fn ex_boosts_stack_under_an_ultra_boost() {
        let ex = Boosts {
            exp: at(600),
            damage: at(600),
            speed: at(60),
        };
        let g = group(all(Deadline::Forever), ex, now());
        assert_eq!(
            g,
            vec![
                BoostGroup {
                    tier: BoostTier::Normal,
                    stats: BoostStat::ALL.to_vec(),
                    left: None,
                },
                BoostGroup {
                    tier: BoostTier::Ex,
                    stats: vec![BoostStat::Speed],
                    left: left(60),
                },
                BoostGroup {
                    tier: BoostTier::Ex,
                    stats: vec![BoostStat::Exp, BoostStat::Damage],
                    left: left(600),
                },
            ]
        );
        assert!(g[0].ultra());
        assert!(!g[1].ultra() && !g[2].ultra());
    }

    #[test]
    fn nothing_active_is_nothing() {
        assert!(group(Boosts::default(), Boosts::default(), now()).is_empty());
        assert!(
            group(all(at(0)), all(at(-1)), now()).is_empty(),
            "expiry is exclusive"
        );
    }
}
