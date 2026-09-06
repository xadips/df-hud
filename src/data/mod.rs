pub mod bossmap;
pub mod catalog;
pub mod challenges;
pub mod citymap;
pub mod masteries;
pub mod xp;

use std::borrow::Cow;
use std::collections::HashMap;
use std::hash::Hash;

use crate::net::dfclient::Vars;

/// Seconds to add to Dead Frontier compact timestamps (`df_servertime`,
/// challenge start/end) to get Unix time. Live: `df_servertime = 586484051`
/// at unix `1786484051`.
pub const TIME_OFFSET: i64 = 1_200_000_000;

/// One board row's `field → value` bag. Borrows the reply; only the pairs
/// [`group_indexed`] had to repair are owned.
pub(crate) type Fields<'a> = HashMap<Cow<'a, str>, Cow<'a, str>>;

/// The field's raw value, `""` when the row lacks it.
pub(crate) fn field<'a>(f: &'a Fields<'_>, name: &str) -> &'a str {
    f.get(name).map_or("", |v| v.as_ref())
}

/// Group a reply's `prefix_{index}_{field}` pairs by whatever `key` extracts,
/// dropping the pairs it rejects (counts, unrelated vars). Works on borrows,
/// so the caller does not copy the whole reply to repair its glued pairs.
///
/// The Flash replies sometimes glue two pairs together:
/// `max_challenges=15challenge_clan_0_challenge_id=210`. The number is split
/// off the front and the swallowed pair reinstated, without touching values
/// that legitimately contain `=` (item stats strings). A pair the reply also
/// carries on its own wins over the reinstated copy.
pub(crate) fn group_indexed<'a, K: Eq + Hash>(
    vars: &'a Vars,
    key: impl Fn(&str) -> Option<(K, &str)>,
) -> HashMap<K, Fields<'a>> {
    let mut groups: HashMap<K, Fields<'a>> = HashMap::new();
    for (name, value) in vars {
        let Some((numeric, glued_key, glued_value)) = split_glued(value) else {
            if let Some((index, field)) = key(name) {
                groups
                    .entry(index)
                    .or_default()
                    .insert(Cow::Borrowed(field), Cow::Borrowed(value.as_str()));
            }
            continue;
        };
        if let Some((index, field)) = key(name) {
            groups
                .entry(index)
                .or_default()
                .insert(Cow::Borrowed(field), Cow::Owned(numeric.to_string()));
        }
        if vars.contains_key(glued_key) {
            continue;
        }
        if let Some((index, field)) = key(glued_key) {
            groups
                .entry(index)
                .or_default()
                .entry(Cow::Owned(field.to_string()))
                .or_insert(Cow::Owned(glued_value.to_string()));
        }
    }
    groups
}

/// `(number, swallowed key, swallowed value)` when `value` is a glued pair.
fn split_glued(value: &str) -> Option<(&str, &str, &str)> {
    let (left, glued_value) = value.split_once('=')?;
    let digits_end = left
        .bytes()
        .position(|b| !b.is_ascii_digit())
        .unwrap_or(left.len());
    let (numeric, glued_key) = left.split_at(digits_end);
    if numeric.is_empty() {
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
    Some((numeric, glued_key, glued_value))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn key(name: &str) -> Option<(i32, &str)> {
        let (idx, field) = name.strip_prefix("row_")?.split_once('_')?;
        Some((idx.parse().ok()?, field))
    }

    fn vars(pairs: &[(&str, &str)]) -> Vars {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    #[test]
    fn split_glued_cases() {
        assert_eq!(
            split_glued("15challenge_clan_0_challenge_id=210"),
            Some(("15", "challenge_clan_0_challenge_id", "210"))
        );
        assert_eq!(split_glued("hazardResistance=0.25"), None, "item stats");
        assert_eq!(split_glued("abc_def=5"), None, "no number in front");
        assert_eq!(split_glued("15"), None);
        assert_eq!(split_glued("15=3"), None);
    }

    #[test]
    fn groups_by_index_and_repairs_glued_pairs() {
        let v = vars(&[
            ("max_rows", "2row_1_id=210"),
            ("row_0_name", "first"),
            ("row_0_stats", "hazardResistance=0.25"),
            ("row_1_name", "second"),
            ("row_2_count", "3row_2_kind=x"),
            ("row_2_kind", "kept"),
            ("unrelated", "1"),
        ]);
        let g = group_indexed(&v, key);
        assert_eq!(g.len(), 3, "{g:?}");
        assert_eq!(field(&g[&0], "name"), "first");
        assert_eq!(field(&g[&0], "stats"), "hazardResistance=0.25");
        assert_eq!(field(&g[&0], "missing"), "");
        assert_eq!(field(&g[&1], "id"), "210", "reinstated from the glue");
        assert_eq!(field(&g[&2], "count"), "3", "number split off the front");
        assert_eq!(field(&g[&2], "kind"), "kept", "the reply's own pair wins");
    }

    #[test]
    fn empty_reply_groups_nothing() {
        assert!(group_indexed(&Vars::new(), key).is_empty());
        let only = vars(&[("max_rows", "15")]);
        assert!(group_indexed(&only, key).is_empty());
    }
}
