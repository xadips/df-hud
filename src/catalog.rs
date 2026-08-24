//! XP table from the public allstats feed. Keys are `exp_lvlN`.

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::io::Read;
use std::path::Path;
use std::time::Duration;

use crate::dfclient;

const SCHEMA_VERSION: i32 = 1;
const MAX_BODY: u64 = 8 << 20;
const MIN_BODY: usize = 64 << 10;
const LEVEL_CAP: i32 = 10000;

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Catalog {
    pub schema_version: i32,
    pub fetched_at: DateTime<Utc>,
    pub max_level: i32,
    pub exp_to_reach: Vec<i64>,
    #[serde(skip)]
    cum_exp: Vec<i64>,
}

impl Catalog {
    fn finish(&mut self) {
        self.cum_exp = vec![0; self.exp_to_reach.len()];
        let mut total = 0i64;
        for level in 2..self.exp_to_reach.len() {
            total += self.exp_to_reach[level];
            self.cum_exp[level] = total;
        }
    }

    pub fn exp_needed(&self, level: i32) -> Option<i64> {
        let i = (level as usize).checked_add(1)?;
        if level < 1 || i >= self.exp_to_reach.len() {
            return None;
        }
        Some(self.exp_to_reach[i])
    }

    pub fn cumulative_xp(&self, level: i32, exp_in_level: i64) -> Option<i64> {
        if level < 1 || (level as usize) >= self.cum_exp.len() {
            return None;
        }
        Some(self.cum_exp[level as usize] + exp_in_level)
    }

    pub fn summary(&self) -> String {
        format!("levels 2..{}", self.max_level)
    }
}

pub fn parse(vars: &HashMap<String, String>, fetched_at: DateTime<Utc>) -> Result<Catalog, String> {
    let mut exp = HashMap::new();
    let mut max_level = 0i32;
    for (key, raw) in vars {
        let Some(rest) = key.strip_prefix("exp_lvl") else {
            continue;
        };
        if rest.is_empty() || !rest.bytes().all(|b| b.is_ascii_digit()) {
            continue;
        }
        let level: i32 = rest
            .parse()
            .map_err(|_| format!("catalog: {key} is not a level"))?;
        if !(1..=LEVEL_CAP).contains(&level) {
            continue;
        }
        let v: i64 = raw
            .parse()
            .map_err(|_| format!("catalog: {key} = {raw:?} is not a number"))?;
        if v < 0 {
            return Err(format!("catalog: {key} = {v} is negative"));
        }
        if level > max_level {
            max_level = level;
        }
        exp.insert(level, v);
    }
    if max_level < 2 {
        return Err("catalog: the feed carries no exp_lvl table".into());
    }
    let mut exp_to_reach = vec![0i64; (max_level as usize) + 1];
    for level in 2..=max_level {
        let Some(v) = exp.get(&level) else {
            return Err(format!(
                "catalog: exp_lvl{level} is missing, so the XP table has a hole \
                 and cumulative totals past level {} would be wrong",
                level - 1
            ));
        };
        exp_to_reach[level as usize] = *v;
    }
    let mut c = Catalog {
        schema_version: SCHEMA_VERSION,
        fetched_at,
        max_level,
        exp_to_reach,
        cum_exp: Vec::new(),
    };
    c.finish();
    Ok(c)
}

fn fetch(
    url: &str,
    user_agent: &str,
    timeout: Duration,
    fetched_at: DateTime<Utc>,
) -> Result<Catalog, String> {
    let agent = ureq::AgentBuilder::new().timeout(timeout).build();
    let resp = agent
        .get(url)
        .set("User-Agent", user_agent)
        .call()
        .map_err(|e| format!("catalog: {e}"))?;
    if resp.status() != 200 {
        return Err(format!("catalog: HTTP {}", resp.status()));
    }
    let mut raw = Vec::new();
    resp.into_reader()
        .take(MAX_BODY)
        .read_to_end(&mut raw)
        .map_err(|e| format!("catalog: reading body: {e}"))?;
    let body = String::from_utf8_lossy(&raw);
    if dfclient::looks_like_html(&body) {
        return Err(
            "catalog: got an HTML page instead of the feed (Cloudflare or an outage)".into(),
        );
    }
    if raw.len() < MIN_BODY {
        return Err(format!(
            "catalog: body is only {} bytes, far short of the ~1MB feed; \
             treating it as truncated rather than parsing a partial table",
            raw.len()
        ));
    }
    let vars = dfclient::parse_flash(&body).map_err(|e| format!("catalog: {e}"))?;
    parse(&vars, fetched_at)
}

fn load_file(path: &Path) -> Result<Option<Catalog>, String> {
    let data = match std::fs::read(path) {
        Ok(d) => d,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(e.to_string()),
    };
    match serde_json::from_slice::<Catalog>(&data) {
        Ok(mut c) if c.schema_version == SCHEMA_VERSION && c.exp_to_reach.len() >= 3 => {
            c.finish();
            Ok(Some(c))
        }
        _ => {
            let aside = format!("{}.corrupt-{}", path.display(), Utc::now().timestamp());
            std::fs::rename(path, &aside)
                .map_err(|e| format!("catalog cache unusable and could not be moved aside: {e}"))?;
            Ok(None)
        }
    }
}

fn save_file(path: &Path, c: &Catalog) -> Result<(), String> {
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir).map_err(|e| e.to_string())?;
    }
    let data = serde_json::to_vec(c).map_err(|e| e.to_string())?;
    let tmp = path.with_extension("json.tmp");
    std::fs::write(&tmp, data).map_err(|e| e.to_string())?;
    std::fs::rename(&tmp, path).map_err(|e| e.to_string())
}

pub fn ensure(
    path: &Path,
    feed_url: &str,
    user_agent: &str,
    max_age: Duration,
    timeout: Duration,
    now: DateTime<Utc>,
) -> Result<Catalog, String> {
    let cached = load_file(path)?;
    if let Some(c) = cached.as_ref() {
        if now >= c.fetched_at {
            let age = (now - c.fetched_at).to_std().unwrap_or(Duration::ZERO);
            if age < max_age {
                return Ok(c.clone());
            }
        }
    }
    match fetch(feed_url, user_agent, timeout, now) {
        Ok(fresh) => {
            let _ = save_file(path, &fresh);
            Ok(fresh)
        }
        Err(err) => {
            if let Some(c) = cached {
                Ok(c)
            } else {
                Err(err)
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::dfclient::parse_flash;

    fn load_fixture() -> Catalog {
        let raw = std::fs::read_to_string(
            Path::new(env!("CARGO_MANIFEST_DIR")).join("testdata/allstats.txt"),
        )
        .unwrap();
        let vars = parse_flash(&raw).unwrap();
        parse(&vars, Utc::now()).unwrap()
    }

    #[test]
    fn parses_the_real_feed() {
        let c = load_fixture();
        assert_eq!(c.max_level, 415);
        assert_eq!(c.exp_to_reach[2], 125);
        assert_eq!(c.exp_to_reach[415], 176_000_000);
        assert_eq!(c.exp_to_reach.len(), 416);
    }

    #[test]
    fn cumulative_xp_is_continuous_across_level_up() {
        let c = load_fixture();
        for level in [1, 2, 5, 50, 200, 414] {
            let needed = c.exp_needed(level).unwrap();
            let before = c.cumulative_xp(level, needed - 1).unwrap();
            let after = c.cumulative_xp(level + 1, 0).unwrap();
            assert_eq!(after - before, 1, "level {level}");
        }
        let mut prev = -1i64;
        for level in 1..=c.max_level {
            let got = c.cumulative_xp(level, 0).unwrap();
            assert!(got > prev, "level {level}");
            prev = got;
        }
    }

    #[test]
    fn exp_needed_at_the_cap() {
        let c = load_fixture();
        assert!(c.exp_needed(415).is_none());
        assert_eq!(c.exp_needed(414).unwrap(), 176_000_000);
    }

    #[test]
    fn hole_in_the_table_is_an_error() {
        let mut vars = HashMap::new();
        vars.insert("exp_lvl2".into(), "10".into());
        vars.insert("exp_lvl4".into(), "40".into());
        assert!(parse(&vars, Utc::now()).unwrap_err().contains("exp_lvl3"));
    }
}
