//! Runtime show/hide state for HUD widget groups.

use std::collections::HashMap;
use std::sync::Mutex;

pub const TOGGLEABLE: &[&str] = &[
    "block",
    "bosses",
    "session",
    "xp",
    "challenges",
    "masteries",
    "map",
    "keybinds",
];

pub fn hidden_at_start() -> &'static [&'static str] {
    &["map"]
}

pub fn known(name: &str) -> bool {
    TOGGLEABLE.contains(&name)
}

pub struct Groups {
    hidden: Mutex<HashMap<String, bool>>,
}

impl Default for Groups {
    fn default() -> Self {
        Self::new()
    }
}

impl Groups {
    pub fn new() -> Self {
        let mut hidden = HashMap::new();
        for name in hidden_at_start() {
            hidden.insert((*name).to_string(), true);
        }
        Self {
            hidden: Mutex::new(hidden),
        }
    }

    pub fn hidden(&self, name: &str) -> bool {
        *self.hidden.lock().unwrap().get(name).unwrap_or(&false)
    }

    pub fn shown(&self, name: &str) -> bool {
        !self.hidden(name)
    }

    pub fn toggle(&self, name: &str) -> Result<bool, String> {
        if !known(name) {
            return Err(format!("unknown group {name:?}"));
        }
        let mut g = self.hidden.lock().unwrap();
        let now = !g.get(name).copied().unwrap_or(false);
        g.insert(name.to_string(), now);
        Ok(now)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn map_starts_hidden() {
        let g = Groups::new();
        assert!(g.hidden("map"));
        assert!(!g.hidden("block"));
        assert!(!g.hidden("keybinds"));
        assert!(!g.toggle("map").unwrap());
        assert!(!g.hidden("map"));
        assert!(g.toggle("nope").is_err());
    }
}
