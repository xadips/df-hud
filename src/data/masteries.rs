//! Parse the Dead Frontier masteries page data from Flash key/value pairs.
//!
//! Wire format, per mastery `i` in `0..max_masteries`:
//! `mastery_{i}_{name,description,stat_level,stat_exp,scale_factor,start_point,bonuses}`
//! and per bonus `j` in `0..bonuses`: `mastery_{i}_bonuses_{j}_{name,scale,max,tooltip}`.
//! The arithmetic mirrors the game's own masteries.js.

use std::collections::HashMap;

use crate::data::repair_glued_pairs;
use crate::model::{Mastery, MasteryBonus};

pub fn parse(vars: &HashMap<String, String>) -> Vec<Mastery> {
    let mut vars = vars.clone();
    repair_glued_pairs(&mut vars);

    let mut fields: HashMap<i32, HashMap<String, String>> = HashMap::new();
    for (name, value) in &vars {
        let Some((index, field)) = parse_key(name) else {
            continue;
        };
        fields
            .entry(index)
            .or_default()
            .insert(field, value.clone());
    }

    let mut out = Vec::new();
    for (index, f) in fields {
        let name = f
            .get("name")
            .cloned()
            .unwrap_or_default()
            .trim()
            .to_string();
        if name.is_empty() {
            continue;
        }
        let level = atof(f.get("stat_level").map_or("", String::as_str)) as i32;
        let mut m = Mastery {
            index,
            name,
            desc: f
                .get("description")
                .cloned()
                .unwrap_or_default()
                .trim()
                .to_string(),
            level,
            exp: atof(f.get("stat_exp").map_or("", String::as_str)) as i64,
            next_exp: next_level_exp(
                atof(f.get("start_point").map_or("", String::as_str)),
                atof(f.get("scale_factor").map_or("", String::as_str)),
                level,
            ),
            ..Mastery::default()
        };
        let count = atof(f.get("bonuses").map_or("", String::as_str)) as i32;
        for j in 0..count {
            let get = |field: &str| {
                f.get(&format!("bonuses_{j}_{field}"))
                    .map_or("", String::as_str)
            };
            let scale = atof(get("scale")).abs();
            let max = atof(get("max")).abs();
            // masteries.js: (scale * level).toFixed(5), then clamp to max.
            let mut value = round5(scale * f64::from(m.level));
            if max != 0.0 && value > max {
                value = max;
            }
            m.bonuses.push(MasteryBonus {
                name: get("name").trim().to_string(),
                scale,
                max,
                value,
            });
        }
        out.push(m);
    }
    out.sort_by_key(|m| m.index);
    out
}

fn parse_key(name: &str) -> Option<(i32, String)> {
    let rest = name.strip_prefix("mastery_")?;
    let (idx, field) = rest.split_once('_')?;
    let index: i32 = idx.parse().ok()?;
    Some((index, field.to_string()))
}

/// `ceil(start_point * scale_factor^(level+1))`, what `exp` has to reach for
/// the next level. `exp` is progress within the level, not a lifetime total.
fn next_level_exp(start_point: f64, scale_factor: f64, level: i32) -> i64 {
    let next = start_point * scale_factor.powi(level + 1);
    if !next.is_finite() || next <= 0.0 {
        return 0;
    }
    next.ceil() as i64
}

fn atof(raw: &str) -> f64 {
    raw.trim().parse().unwrap_or(0.0)
}

fn round5(v: f64) -> f64 {
    (v * 100_000.0).round() / 100_000.0
}

#[cfg(test)]
mod tests {
    use super::*;

    fn response() -> HashMap<String, String> {
        let pairs = [
            ("max_masteries", "3"),
            ("mastery_0_name", "Looter"),
            ("mastery_0_description", "Loot anything."),
            ("mastery_0_stat_level", "204"),
            ("mastery_0_stat_exp", "37"),
            ("mastery_0_scale_factor", "1.0001"),
            ("mastery_0_start_point", "100"),
            ("mastery_0_bonuses", "2"),
            ("mastery_0_bonuses_0_name", "Item Find Chance"),
            ("mastery_0_bonuses_0_scale", "0.005"),
            ("mastery_0_bonuses_0_max", "5"),
            ("mastery_0_bonuses_0_tooltip", "Better loot."),
            ("mastery_0_bonuses_1_name", "Loot Speed"),
            ("mastery_0_bonuses_1_scale", "0.01"),
            ("mastery_0_bonuses_1_max", "5"),
            ("mastery_0_bonuses_1_tooltip", "Faster searching."),
            ("mastery_1_name", "Melee Expert"),
            ("mastery_1_description", "Kill with melee."),
            ("mastery_1_stat_level", "48"),
            ("mastery_1_stat_exp", "512"),
            ("mastery_1_scale_factor", "1.001"),
            ("mastery_1_start_point", "500"),
            ("mastery_1_bonuses", "1"),
            ("mastery_1_bonuses_0_name", "Melee Damage"),
            ("mastery_1_bonuses_0_scale", "0.1"),
            ("mastery_1_bonuses_0_max", "20"),
            // Glued pair: the count swallowed the next mastery's first field.
            ("mastery_2_bonuses", "1mastery_2_bonuses_0_name=Damage"),
            ("mastery_2_name", "Artisan"),
            ("mastery_2_stat_level", "400"),
            ("mastery_2_stat_exp", "0"),
            ("mastery_2_scale_factor", "1.0002"),
            ("mastery_2_start_point", "100"),
            ("mastery_2_bonuses_0_scale", "0.05"),
            ("mastery_2_bonuses_0_max", "20"),
        ];
        pairs
            .into_iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    #[test]
    fn parse_fixture() {
        let got = parse(&response());
        assert_eq!(got.len(), 3, "{got:?}");

        let looter = &got[0];
        assert_eq!(looter.name, "Looter");
        assert_eq!(looter.level, 204);
        assert_eq!(looter.exp, 37);
        // ceil(100 * 1.0001^205) = ceil(102.07...) = 103
        assert_eq!(looter.next_exp, 103);
        assert_eq!(looter.bonuses.len(), 2);
        assert_eq!(looter.bonuses[0].value, 1.02); // 0.005 * 204
        assert!(!looter.bonuses[0].capped());
        assert_eq!(looter.bonuses[1].value, 2.04); // 0.01 * 204
        assert!(!looter.mastered());

        let melee = &got[1];
        assert_eq!(melee.name, "Melee Expert");
        assert_eq!(melee.exp, 512);
        // ceil(500 * 1.001^49) = ceil(525.09...) = 526
        assert_eq!(melee.next_exp, 526);
        assert_eq!(melee.bonuses[0].value, 4.8);
        assert!(!melee.mastered());

        // 0.05 * 400 = 20, exactly at the 20 cap: mastered.
        let artisan = &got[2];
        assert_eq!(artisan.name, "Artisan");
        assert_eq!(artisan.bonuses.len(), 1, "glued count not repaired");
        assert_eq!(artisan.bonuses[0].name, "Damage");
        assert_eq!(artisan.bonuses[0].value, 20.0);
        assert!(artisan.bonuses[0].capped());
        assert!(artisan.mastered());
    }

    #[test]
    fn value_clamps_to_max() {
        let mut vars = response();
        vars.insert("mastery_1_stat_level".into(), "500".into());
        let got = parse(&vars);
        let melee = got.iter().find(|m| m.name == "Melee Expert").unwrap();
        // 0.1 * 500 = 50, clamped to the 20 cap.
        assert_eq!(melee.bonuses[0].value, 20.0);
        assert!(melee.mastered());
    }

    #[test]
    fn uncapped_bonus_is_never_mastered() {
        let mut vars = response();
        vars.insert("mastery_1_bonuses_0_max".into(), "0".into());
        let got = parse(&vars);
        let melee = got.iter().find(|m| m.name == "Melee Expert").unwrap();
        assert!(!melee.bonuses[0].capped());
        assert!(!melee.mastered());
    }

    #[test]
    fn no_bonuses_is_not_mastered() {
        assert!(!Mastery::default().mastered());
    }

    #[test]
    fn negative_scales_are_folded() {
        // The page shows Math.abs of both; a hunger *reduction* arrives negative.
        let mut vars = response();
        vars.insert("mastery_1_bonuses_0_scale".into(), "-0.1".into());
        vars.insert("mastery_1_bonuses_0_max".into(), "-20".into());
        let got = parse(&vars);
        let melee = got.iter().find(|m| m.name == "Melee Expert").unwrap();
        assert_eq!(melee.bonuses[0].scale, 0.1);
        assert_eq!(melee.bonuses[0].max, 20.0);
        assert_eq!(melee.bonuses[0].value, 4.8);
    }

    #[test]
    fn order_is_stable_and_nameless_rows_drop() {
        let mut vars = response();
        vars.insert("mastery_7_stat_level".into(), "3".into());
        for _ in 0..8 {
            let names: Vec<_> = parse(&vars).iter().map(|m| m.name.clone()).collect();
            assert_eq!(names, ["Looter", "Melee Expert", "Artisan"]);
        }
    }

    #[test]
    fn parse_empty() {
        assert!(parse(&HashMap::new()).is_empty());
        let only = HashMap::from([("max_masteries".into(), "11".into())]);
        assert!(parse(&only).is_empty());
    }
}
