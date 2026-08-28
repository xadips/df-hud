pub mod bossmap;
pub mod catalog;
pub mod challenges;
pub mod citymap;
pub mod masteries;
pub mod xp;

use std::collections::HashMap;

/// Seconds to add to Dead Frontier compact timestamps (`df_servertime`,
/// challenge start/end) to get Unix time. Live: `df_servertime = 586484051`
/// at unix `1786484051`.
pub const TIME_OFFSET: i64 = 1_200_000_000;

/// The Flash replies sometimes glue two pairs together:
/// `max_challenges=15challenge_clan_0_challenge_id=210`. Split the number off
/// the front and reinstate the swallowed pair, without touching values that
/// legitimately contain `=` (item stats strings).
pub(crate) fn repair_glued_pairs(vars: &mut HashMap<String, String>) {
    let keys: Vec<String> = vars.keys().cloned().collect();
    for key in keys {
        let Some(value) = vars.get(&key).cloned() else {
            continue;
        };
        if !value.contains('=') {
            continue;
        }
        let Some((numeric, glued_key, glued_value)) = split_glued(&value) else {
            continue;
        };
        if numeric.is_empty() {
            continue;
        }
        vars.insert(key, numeric);
        vars.entry(glued_key).or_insert(glued_value);
    }
}

fn split_glued(value: &str) -> Option<(String, String, String)> {
    let eq = value.find('=')?;
    let (left, right) = value.split_at(eq);
    let glued_value = right[1..].to_string();
    let digits_end = left
        .bytes()
        .position(|b| !b.is_ascii_digit())
        .unwrap_or(left.len());
    let numeric = left[..digits_end].to_string();
    let glued_key = &left[digits_end..];
    if glued_key.is_empty() {
        return None;
    }
    let mut cs = glued_key.chars();
    let first = cs.next()?;
    if !first.is_ascii_lowercase() {
        return None;
    }
    if !cs.all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '_') {
        return None;
    }
    Some((numeric, glued_key.to_string(), glued_value))
}
