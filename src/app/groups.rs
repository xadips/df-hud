//! Runtime show/hide state for HUD widget groups.

use crate::wake::lock;
use std::fmt;
use std::str::FromStr;
use std::sync::Mutex;

/// A toggleable HUD widget. The names are what the hotkey actions, the
/// `/api/widget/<name>/toggle` route and the tray speak; parse them once at
/// that boundary with [`FromStr`].
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Group {
    Block,
    Bosses,
    Session,
    Xp,
    Challenges,
    Masteries,
    Map,
    Keybinds,
}

impl Group {
    pub const ALL: [Group; 8] = [
        Group::Block,
        Group::Bosses,
        Group::Session,
        Group::Xp,
        Group::Challenges,
        Group::Masteries,
        Group::Map,
        Group::Keybinds,
    ];

    pub fn as_str(self) -> &'static str {
        match self {
            Group::Block => "block",
            Group::Bosses => "bosses",
            Group::Session => "session",
            Group::Xp => "xp",
            Group::Challenges => "challenges",
            Group::Masteries => "masteries",
            Group::Map => "map",
            Group::Keybinds => "keybinds",
        }
    }

    /// `block, bosses, ...` for error messages.
    pub fn names() -> String {
        Self::ALL
            .iter()
            .map(|g| g.as_str())
            .collect::<Vec<_>>()
            .join(", ")
    }
}

impl fmt::Display for Group {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

impl FromStr for Group {
    type Err = String;

    fn from_str(name: &str) -> Result<Self, String> {
        Self::ALL
            .into_iter()
            .find(|g| g.as_str() == name)
            .ok_or_else(|| format!("unknown group {name:?}"))
    }
}

pub const HIDDEN_AT_START: &[Group] = &[Group::Map];

pub struct Groups {
    hidden: Mutex<[bool; Group::ALL.len()]>,
}

impl Default for Groups {
    fn default() -> Self {
        Self::new()
    }
}

impl Groups {
    pub fn new() -> Self {
        let mut hidden = [false; Group::ALL.len()];
        for g in HIDDEN_AT_START {
            hidden[*g as usize] = true;
        }
        Self {
            hidden: Mutex::new(hidden),
        }
    }

    pub fn hidden(&self, g: Group) -> bool {
        lock(&self.hidden)[g as usize]
    }

    pub fn shown(&self, g: Group) -> bool {
        !self.hidden(g)
    }

    /// Flips the group and returns whether it is now hidden.
    pub fn toggle(&self, g: Group) -> bool {
        let mut hidden = lock(&self.hidden);
        let now = !hidden[g as usize];
        hidden[g as usize] = now;
        now
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn map_starts_hidden() {
        let g = Groups::new();
        assert!(g.hidden(Group::Map));
        assert!(!g.hidden(Group::Block));
        assert!(!g.hidden(Group::Keybinds));
        assert!(!g.toggle(Group::Map));
        assert!(!g.hidden(Group::Map));
        assert!(g.toggle(Group::Map));
        assert!(g.shown(Group::Block));
    }

    #[test]
    fn names_round_trip() {
        for g in Group::ALL {
            assert_eq!(g.as_str().parse::<Group>(), Ok(g));
            assert_eq!(g.to_string(), g.as_str());
        }
        let err = "nope".parse::<Group>().unwrap_err();
        assert!(err.contains("nope"), "{err}");
        assert!(
            Group::names().starts_with("block, bosses"),
            "{}",
            Group::names()
        );
    }
}
