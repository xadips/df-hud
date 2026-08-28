//! Parse the Dead Frontier challenge board from Flash key/value pairs.

use chrono::{DateTime, TimeZone, Utc};
use std::collections::HashMap;

use crate::data::{TIME_OFFSET, repair_glued_pairs};
use crate::model::{Challenge, Objective};

pub fn parse(vars: &HashMap<String, String>, level: i32, gold: bool) -> Vec<Challenge> {
    let mut vars = vars.clone();
    repair_glued_pairs(&mut vars);

    let mut fields: HashMap<(bool, i32), HashMap<String, String>> = HashMap::new();
    for (name, value) in &vars {
        let Some((clan, index, field)) = parse_key(name) else {
            continue;
        };
        fields
            .entry((clan, index))
            .or_default()
            .insert(field, value.clone());
    }

    let mut out = Vec::new();
    for ((clan, index), f) in fields {
        let mut c = Challenge {
            index,
            clan,
            id: f.get("challenge_id").cloned().unwrap_or_default(),
            name: f
                .get("name")
                .cloned()
                .unwrap_or_default()
                .trim()
                .to_string(),
            desc: f
                .get("description")
                .cloned()
                .unwrap_or_default()
                .trim()
                .to_string(),
            start: challenge_time(f.get("start_time").map_or("", String::as_str)),
            end: challenge_time(f.get("end_time").map_or("", String::as_str)),
            min_level: atoi(f.get("min_level").map_or("", String::as_str)),
            max_level: atoi(f.get("max_level").map_or("", String::as_str)),
            repeatable: f.get("repeatable").map(String::as_str) == Some("1"),
            reward_cash: atoi64(f.get("reward_cash").map_or("", String::as_str)),
            reward_credits: atoi64(f.get("reward_credits").map_or("", String::as_str)),
            reward_points: atoi64(f.get("reward_points").map_or("", String::as_str)),
            reward_items: f
                .get("reward_items")
                .cloned()
                .unwrap_or_default()
                .trim()
                .to_string(),
            reward_special: f
                .get("reward_special")
                .cloned()
                .unwrap_or_default()
                .trim()
                .to_string(),
            ..Challenge::default()
        };
        let exp = atoi64(f.get("reward_exp").map_or("", String::as_str));
        if exp > 0 && level > 0 {
            c.reward_exp = exp * i64::from(level);
            if gold {
                c.reward_exp *= 2;
            }
        }
        let count = atoi(f.get("objectives").map_or("", String::as_str));
        for j in 1..=count {
            let suffix = j.to_string();
            let mut o = Objective {
                name: f
                    .get(&format!("objectives_{suffix}_name"))
                    .cloned()
                    .unwrap_or_default()
                    .trim()
                    .to_string(),
                target: atoi64(
                    f.get(&format!("objectives_{suffix}_target"))
                        .map_or("", String::as_str),
                ),
                ..Objective::default()
            };
            if let Some(raw) = f.get(&format!("objective_{suffix}_player_score")) {
                o.score = atoi64(raw);
                o.has_score = true;
            }
            c.objectives.push(o);
        }
        if c.eligible(level) {
            out.push(c);
        }
    }
    out.sort_by(|a, b| match (a.clan, b.clan) {
        (false, true) => std::cmp::Ordering::Less,
        (true, false) => std::cmp::Ordering::Greater,
        _ => a.index.cmp(&b.index),
    });
    out
}

fn parse_key(name: &str) -> Option<(bool, i32, String)> {
    let rest = name.strip_prefix("challenge_")?;
    let (clan, rest) = if let Some(r) = rest.strip_prefix("clan_") {
        (true, r)
    } else {
        (false, rest)
    };
    let (idx, field) = rest.split_once('_')?;
    let index: i32 = idx.parse().ok()?;
    Some((clan, index, field.to_string()))
}

fn challenge_time(raw: &str) -> DateTime<Utc> {
    let v: i64 = raw.trim().parse().unwrap_or(0);
    if v <= 0 {
        return DateTime::<Utc>::UNIX_EPOCH;
    }
    Utc.timestamp_opt(v + TIME_OFFSET, 0)
        .single()
        .unwrap_or(DateTime::<Utc>::UNIX_EPOCH)
}

fn atoi(raw: &str) -> i32 {
    raw.trim().parse().unwrap_or(0)
}

fn atoi64(raw: &str) -> i64 {
    raw.trim().parse().unwrap_or(0)
}

pub fn cycle_key(c: &Challenge) -> String {
    let day = if c.end.timestamp() > 0 {
        c.end.format("%Y-%m-%d").to_string()
    } else {
        String::new()
    };
    format!("{}|{day}", c.name)
}

/// Overlay sticky completion. Returns cycle keys that just finished so the
/// persist map can latch them. A later board that un-completes the same cycle
/// (clan-size target recompute) stays done.
pub fn apply_sticky(board: &mut [Challenge], done: &HashMap<String, bool>) -> Vec<String> {
    let mut newly = Vec::new();
    for c in board.iter_mut() {
        let key = cycle_key(c);
        let live = c.live_complete();
        let was = done.get(&key).copied().unwrap_or(false);
        if live && !was {
            newly.push(key.clone());
        }
        c.remembered = live || was;
    }
    newly
}

#[cfg(test)]
mod tests {
    use super::*;

    fn response() -> HashMap<String, String> {
        let pairs = [
            ("challenge_0_challenge_id", "8017"),
            ("challenge_0_name", "Summer Death"),
            ("challenge_0_start_time", "584880000"),
            ("challenge_0_end_time", "587299200"),
            ("challenge_0_min_level", "1"),
            ("challenge_0_max_level", "415"),
            ("challenge_0_objectives", "1"),
            ("challenge_0_objectives_1_name", "Kill Regular Infected"),
            ("challenge_0_objectives_1_target", "100"),
            ("challenge_0_objective_1_player_score", "55"),
            ("challenge_0_repeatable", "1"),
            ("challenge_0_reward_special", "summerticket|10"),
            ("challenge_10_name", "Hidden"),
            ("challenge_10_min_level", "1"),
            ("challenge_10_max_level", "44"),
            ("challenge_10_objectives", "1"),
            ("challenge_10_objectives_1_target", "50"),
            ("challenge_11_name", "What Doc Ordered"),
            ("challenge_11_min_level", "45"),
            ("challenge_11_max_level", "74"),
            ("challenge_11_objectives", "1"),
            ("challenge_11_objectives_1_target", "5"),
            ("challenge_11_objective_1_player_score", "2"),
            ("challenge_11_reward_exp", "2500"),
            ("challenge_clan_3_name", "Weekly Challenge - Travel Blocks"),
            ("challenge_clan_3_end_time", "586953931"),
            ("challenge_clan_3_objectives", "1"),
            ("challenge_clan_3_objectives_1_name", "Travel Blocks"),
            ("challenge_clan_3_objectives_1_target", "360"),
            ("challenge_clan_3_objective_1_player_score", "366"),
            ("challenge_clan_3_reward_points", "20"),
            ("max_challenges", "15challenge_clan_0_challenge_id=210"),
        ];
        pairs
            .into_iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    #[test]
    fn repair_glued_pairs_cases() {
        let mut vars = HashMap::from([(
            "max_challenges".into(),
            "15challenge_clan_0_challenge_id=210".into(),
        )]);
        repair_glued_pairs(&mut vars);
        assert_eq!(vars.get("max_challenges").unwrap(), "15");
        assert_eq!(vars.get("challenge_clan_0_challenge_id").unwrap(), "210");

        let mut vars = HashMap::from([("armour".into(), "hazardResistance=0.25".into())]);
        repair_glued_pairs(&mut vars);
        assert_eq!(vars.get("armour").unwrap(), "hazardResistance=0.25");

        let mut vars = HashMap::from([
            (
                "max_challenges".into(),
                "15challenge_clan_0_challenge_id=210".into(),
            ),
            ("challenge_clan_0_challenge_id".into(), "999".into()),
        ]);
        repair_glued_pairs(&mut vars);
        assert_eq!(vars.get("challenge_clan_0_challenge_id").unwrap(), "999");
    }

    #[test]
    fn parse_fixture() {
        let got = parse(&response(), 415, false);
        assert_eq!(got.len(), 4, "{got:?}");
        assert_eq!(got[0].name, "Summer Death");
        assert_eq!(got[3].name, "Weekly Challenge - Travel Blocks");
        assert_eq!(got[0].objectives[0].score, 55);
        assert!(got[0].objectives[0].has_score);
        assert!(!got[0].objectives[0].done());
        assert!(got[0].repeatable);
        assert_eq!(got[0].reward_special, "summerticket|10");
        let want = Utc.timestamp_opt(587299200 + TIME_OFFSET, 0).unwrap();
        assert_eq!(got[0].end, want);
        assert_eq!(got[1].name, "What Doc Ordered");
        assert!(got.iter().all(|c| c.name != "Hidden"));
        assert_eq!(got[1].reward_exp, 2500 * 415);
        assert!(got[3].complete());
        assert_eq!(got[3].reward_points, 20);
    }

    #[test]
    fn parse_order_is_stable() {
        let mut first = None;
        for _ in 0..8 {
            let names: Vec<_> = parse(&response(), 415, false)
                .into_iter()
                .map(|c| c.name)
                .collect();
            if let Some(first) = &first {
                assert_eq!(first, &names);
            } else {
                first = Some(names);
            }
        }
        let mut seen_clan = false;
        for c in parse(&response(), 415, false) {
            if c.clan {
                seen_clan = true;
            } else if seen_clan {
                panic!("personal challenge appeared after clan challenge");
            }
        }
    }

    #[test]
    fn parse_gold_reward() {
        let got = parse(&response(), 100, true);
        for c in got {
            if c.name == "What Doc Ordered" {
                assert_eq!(c.reward_exp, 2500 * 100 * 2);
            }
        }
    }

    #[test]
    fn helpers() {
        let c = Challenge {
            objectives: vec![
                Objective {
                    target: 10,
                    score: 4,
                    has_score: true,
                    ..Objective::default()
                },
                Objective {
                    target: 20,
                    score: 20,
                    has_score: true,
                    ..Objective::default()
                },
            ],
            ..Challenge::default()
        };
        let (score, target) = c.progress();
        assert_eq!(
            (score, target, c.complete(), c.started()),
            (24, 30, false, true)
        );
        assert!(!Challenge::default().complete());
        assert!(
            !Objective {
                score: 5,
                ..Objective::default()
            }
            .done()
        );
        let ended = Challenge {
            end: Utc::now() - chrono::Duration::hours(1),
            ..Challenge::default()
        };
        assert_eq!(ended.remaining(Utc::now()), std::time::Duration::ZERO);
        assert_eq!(
            Challenge::default().remaining(Utc::now()),
            std::time::Duration::ZERO
        );
        assert!(
            Challenge {
                clan: true,
                ..Challenge::default()
            }
            .eligible(1)
        );
        assert!(Challenge::default().eligible(1));
        assert!(
            !Challenge {
                min_level: 10,
                max_level: 20,
                ..Challenge::default()
            }
            .eligible(1)
        );
        assert!(
            Challenge {
                min_level: 10,
                max_level: 20,
                objectives: vec![Objective {
                    has_score: true,
                    ..Objective::default()
                }],
                ..Challenge::default()
            }
            .eligible(1)
        );
    }

    #[test]
    fn cycle_key_ignores_index_and_seconds() {
        let end = Utc.timestamp_opt(586_953_931 + TIME_OFFSET, 0).unwrap();
        let a = Challenge {
            index: 3,
            name: "Travel".into(),
            end,
            ..Challenge::default()
        };
        let b = Challenge {
            index: 1,
            name: "Travel".into(),
            end: end + chrono::Duration::seconds(11),
            ..Challenge::default()
        };
        assert_eq!(cycle_key(&a), cycle_key(&b));
        assert_ne!(
            cycle_key(&a),
            cycle_key(&Challenge {
                name: "Travel".into(),
                end: end + chrono::Duration::days(7),
                ..Challenge::default()
            })
        );
        assert_ne!(
            cycle_key(&a),
            cycle_key(&Challenge {
                name: "Other".into(),
                end,
                ..Challenge::default()
            })
        );
    }

    #[test]
    fn sticky_completion_latches_a_cycle() {
        let end = Utc.timestamp_opt(586_953_931 + TIME_OFFSET, 0).unwrap();
        let mut board = vec![Challenge {
            name: "Travel".into(),
            end,
            objectives: vec![Objective {
                target: 500,
                score: 500,
                has_score: true,
                ..Objective::default()
            }],
            ..Challenge::default()
        }];
        let newly = apply_sticky(&mut board, &HashMap::new());
        assert_eq!(newly, vec![cycle_key(&board[0])]);
        assert!(board[0].complete());

        let mut later = vec![Challenge {
            name: "Travel".into(),
            end,
            objectives: vec![Objective {
                target: 800,
                score: 500,
                has_score: true,
                ..Objective::default()
            }],
            ..Challenge::default()
        }];
        let done = HashMap::from([(cycle_key(&later[0]), true)]);
        assert!(apply_sticky(&mut later, &done).is_empty());
        assert!(later[0].complete());
        assert!(!later[0].live_complete());
    }

    #[test]
    fn parse_empty() {
        assert!(parse(&HashMap::new(), 415, false).is_empty());
        let only = HashMap::from([("max_challenges".into(), "15".into())]);
        assert!(parse(&only, 415, false).is_empty());
    }
}
