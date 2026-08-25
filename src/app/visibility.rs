//! Fail-open HUD visibility.
//!
//! Config and manual overlay gates win; then `hud.only_when_game_running`;
//! launcher titles; then `hud.follow_game_workspace`. Unknown placement shows.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use crate::config::Config;
use crate::game;
use crate::game::desktop::{Client, Match, Placement};
use crate::model::{GameState, Visibility};
use crate::wake::Notify;

const QUERY_ERROR_LOG_INTERVAL: Duration = Duration::from_secs(30);
type VisibilityHandler = Arc<dyn Fn(Visibility) + Send + Sync>;

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Rules {
    pub only_when_game_running: bool,
    pub follow_game_workspace: bool,
    pub config_enabled: bool,
    pub manual_enabled: bool,
}

pub fn decide(r: Rules, game: GameState, place: &Placement) -> (bool, String) {
    if !r.config_enabled {
        return (false, "the HUD is disabled in config".into());
    }
    if !r.manual_enabled {
        return (false, "hidden by hand".into());
    }
    if r.only_when_game_running && !game.running {
        return (false, "the game is not running".into());
    }
    if r.only_when_game_running && place.launcher_only {
        return (
            false,
            "the launcher is open but the game has not started".into(),
        );
    }
    if r.follow_game_workspace && place.known && !place.on_active_workspace {
        if place.minimized {
            return (false, "the game window is minimized".into());
        }
        if place.foreground_rule {
            return (false, "the game is not the foreground window".into());
        }
        let where_ = if place.workspace_name.is_empty() {
            place.workspace.to_string()
        } else {
            place.workspace_name.clone()
        };
        return (
            false,
            format!("the game is on workspace {where_}, which is not the one being shown"),
        );
    }
    (true, String::new())
}

pub trait Querier: Send + Sync {
    fn game_window(&self, pid: i32, m: &Match) -> Result<Placement, String>;
}

impl<T: Client + ?Sized> Querier for T {
    fn game_window(&self, pid: i32, m: &Match) -> Result<Placement, String> {
        Client::game_window(self, pid, m)
    }
}

impl Querier for Box<dyn Client> {
    fn game_window(&self, pid: i32, m: &Match) -> Result<Placement, String> {
        Client::game_window(&**self, pid, m)
    }
}

pub struct Watcher {
    game: Arc<game::Watcher>,
    cfg: Arc<Mutex<Config>>,
    query: Option<Arc<dyn Querier>>,
    poke: Notify,
    enabled: Mutex<bool>,
    state: Mutex<Visibility>,
    place: Mutex<Placement>,
    on_change: Mutex<Option<VisibilityHandler>>,
    window_session: Mutex<GameState>,
    window_seen: Mutex<bool>,
    last_query_error: Mutex<Option<Instant>>,
}

impl Watcher {
    pub fn new(
        game: Arc<game::Watcher>,
        cfg: Arc<Mutex<Config>>,
        query: Option<Arc<dyn Querier>>,
    ) -> Arc<Self> {
        Arc::new(Self {
            game,
            cfg,
            query,
            poke: Notify::new(),
            enabled: Mutex::new(true),
            state: Mutex::new(Visibility {
                visible: true,
                ..Visibility::default()
            }),
            place: Mutex::new(Placement::default()),
            on_change: Mutex::new(None),
            window_session: Mutex::new(GameState::default()),
            window_seen: Mutex::new(false),
            last_query_error: Mutex::new(None),
        })
    }

    pub fn set_on_change(&self, f: impl Fn(Visibility) + Send + Sync + 'static) {
        *self.on_change.lock().unwrap() = Some(Arc::new(f));
    }

    pub fn state(&self) -> Visibility {
        self.state.lock().unwrap().clone()
    }

    pub fn placement(&self) -> Placement {
        self.place.lock().unwrap().clone()
    }

    pub fn set_enabled(&self, on: bool) {
        let changed = {
            let mut e = self.enabled.lock().unwrap();
            let changed = *e != on;
            *e = on;
            changed
        };
        if changed {
            self.poke();
        }
    }

    #[cfg(test)]
    pub fn enabled(&self) -> bool {
        *self.enabled.lock().unwrap()
    }

    pub fn poke(&self) {
        self.poke.ping();
    }

    pub fn run(&self, stop: Arc<AtomicBool>) {
        self.refresh();
        while !stop.load(Ordering::SeqCst) {
            self.poke.wait_timeout(Duration::from_secs(2));
            if stop.load(Ordering::SeqCst) {
                break;
            }
            self.refresh();
        }
    }

    pub fn refresh(&self) {
        let cfg = self.cfg.lock().unwrap().clone();
        let game = self.game.state();
        let rules = Rules {
            only_when_game_running: cfg.hud.only_when_game_running,
            follow_game_workspace: cfg.hud.follow_game_workspace,
            config_enabled: cfg.hud.enabled,
            manual_enabled: *self.enabled.lock().unwrap(),
        };
        let session_changed = {
            let mut session = self.window_session.lock().unwrap();
            let mut seen = self.window_seen.lock().unwrap();
            if !game.running {
                let changed = session.running;
                *session = GameState::default();
                *seen = false;
                changed
            } else if !game.same_session(*session) {
                *session = game;
                *seen = false;
                true
            } else {
                false
            }
        };
        if session_changed {
            *self.place.lock().unwrap() = Placement::default();
            *self.last_query_error.lock().unwrap() = None;
        }
        let mut window_seen = *self.window_seen.lock().unwrap();
        let mut place = self.place.lock().unwrap().clone();
        let can_query = game.running && self.query.is_some();
        let mut query_failed_now = false;
        if can_query && let Some(q) = &self.query {
            let match_ = cfg.launcher_window_match();
            match q.game_window(game.pid, &match_) {
                Err(err) => {
                    query_failed_now = true;
                    let now = Instant::now();
                    let mut last = self.last_query_error.lock().unwrap();
                    if last
                        .map(|at| now.saturating_duration_since(at) >= QUERY_ERROR_LOG_INTERVAL)
                        .unwrap_or(true)
                    {
                        eprintln!(
                            "hud: cannot ask the desktop where the game's window is ({err}); \
                                 keeping the last placement and retrying"
                        );
                        *last = Some(now);
                    }
                }
                Ok(got) => {
                    if self.last_query_error.lock().unwrap().take().is_some() {
                        eprintln!("hud: desktop query recovered");
                    }
                    place = got;
                    if place.known {
                        window_seen = true;
                        if game.same_session(*self.window_session.lock().unwrap()) {
                            *self.window_seen.lock().unwrap() = true;
                        }
                    }
                }
            }
        }
        let (mut visible, mut reason) = decide(rules, game, &place);
        if rules.only_when_game_running
            && game.running
            && can_query
            && !query_failed_now
            && !window_seen
            && !place.launcher_only
        {
            visible = false;
            reason = "waiting for the game window to appear".into();
        }
        let next = Visibility {
            visible,
            reason,
            monitor: place.monitor.clone(),
        };
        let (prev, prev_place) = {
            let mut st = self.state.lock().unwrap();
            let mut pl = self.place.lock().unwrap();
            let prev = st.clone();
            let prev_place = pl.clone();
            *st = next.clone();
            *pl = place.clone();
            (prev, prev_place)
        };
        if prev_place.matched_by != place.matched_by && place.known {
            if place.foreground_rule {
                eprintln!(
                    "hud: the game's window is on {} (matched by {}, class {:?})",
                    place.monitor, place.matched_by, place.class
                );
            } else {
                eprintln!(
                    "hud: the game's window is on {} workspace {} (matched by {}, class {:?})",
                    place.monitor, place.workspace_name, place.matched_by, place.class
                );
            }
        }
        if prev == next {
            return;
        }
        match (next.visible, prev.visible) {
            (true, false) => eprintln!("hud: showing"),
            (false, true) => eprintln!("hud: hiding - {}", next.reason),
            (false, false) if prev.reason != next.reason => {
                eprintln!("hud: still hidden - {}", next.reason);
            }
            _ => {}
        }
        if let Some(f) = self.on_change.lock().unwrap().clone() {
            f(next);
        }
    }
}

pub fn spawn(watcher: Arc<Watcher>, stop: Arc<AtomicBool>) {
    std::thread::Builder::new()
        .name("df-hud-visibility".into())
        .spawn(move || watcher.run(stop))
        .expect("spawn visibility watcher");
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::game::desktop::Placement;
    use chrono::Utc;
    use std::sync::mpsc;

    fn all_rules() -> Rules {
        Rules {
            only_when_game_running: true,
            follow_game_workspace: true,
            config_enabled: true,
            manual_enabled: true,
        }
    }

    #[test]
    fn decide_hud_visible() {
        let running = GameState {
            running: true,
            pid: 10,
            started_at: Some(Utc::now()),
        };
        let on_screen = Placement {
            known: true,
            workspace_name: "13".into(),
            on_active_workspace: true,
            monitor: "DP-2".into(),
            ..Placement::default()
        };
        let elsewhere = Placement {
            known: true,
            workspace_name: "7".into(),
            on_active_workspace: false,
            monitor: "DP-2".into(),
            ..Placement::default()
        };
        let (visible, reason) = decide(all_rules(), running, &on_screen);
        assert!(visible, "{reason}");

        let (visible, reason) = decide(all_rules(), GameState::default(), &Placement::default());
        assert!(!visible);
        assert!(reason.contains("not running"), "{reason}");

        let (visible, reason) = decide(all_rules(), running, &elsewhere);
        assert!(!visible);
        assert!(reason.contains("workspace 7"), "{reason}");

        let mut alt = on_screen.clone();
        alt.foreground_rule = true;
        let (visible, reason) = decide(all_rules(), running, &alt);
        assert!(visible, "alt-tab should stay visible, got {reason}");

        let mut minimized = elsewhere.clone();
        minimized.foreground_rule = true;
        minimized.minimized = true;
        let (visible, reason) = decide(all_rules(), running, &minimized);
        assert!(
            !visible && reason.contains("minimized"),
            "{visible} {reason}"
        );

        let launcher = Placement {
            launcher_only: true,
            ..Placement::default()
        };
        let (visible, reason) = decide(all_rules(), running, &launcher);
        assert!(!visible);
        assert!(reason.contains("launcher"), "{reason}");

        let (visible, reason) = decide(all_rules(), running, &Placement::default());
        assert!(visible, "unknown must fail open, got {reason}");
    }

    #[test]
    fn decide_respects_disabled_rules() {
        let (visible, reason) = decide(
            Rules {
                config_enabled: true,
                manual_enabled: true,
                ..Rules::default()
            },
            GameState::default(),
            &Placement {
                known: true,
                workspace_name: "7".into(),
                ..Placement::default()
            },
        );
        assert!(visible, "{reason}");
    }

    #[test]
    fn decide_tray_override_wins() {
        let mut rules = all_rules();
        rules.manual_enabled = false;
        let (visible, reason) = decide(
            rules,
            GameState {
                running: true,
                ..GameState::default()
            },
            &Placement {
                known: true,
                on_active_workspace: true,
                ..Placement::default()
            },
        );
        assert!(!visible);
        assert!(reason.contains("by hand"), "{reason}");
    }

    #[test]
    fn decide_config_switch_wins_over_manual_toggle() {
        let mut rules = all_rules();
        rules.config_enabled = false;
        let (visible, reason) = decide(
            rules,
            GameState {
                running: true,
                ..GameState::default()
            },
            &Placement {
                known: true,
                on_active_workspace: true,
                ..Placement::default()
            },
        );
        assert!(!visible);
        assert!(reason.contains("config"), "{reason}");
    }

    #[test]
    fn decide_fails_open_on_unknown_window() {
        let running = GameState {
            running: true,
            pid: 10,
            ..GameState::default()
        };
        let (visible, reason) = decide(all_rules(), running, &Placement::default());
        assert!(visible, "{reason}");
        let partial = Placement {
            known: true,
            workspace: 3,
            ..Placement::default()
        };
        let (visible, _) = decide(all_rules(), running, &partial);
        assert!(!visible, "Known with a false on_active_workspace must hide");
    }

    struct FakeQuerier {
        place: Mutex<Placement>,
        err: Mutex<Option<String>>,
        calls: std::sync::atomic::AtomicI32,
    }

    impl FakeQuerier {
        fn new(place: Placement) -> Arc<Self> {
            Arc::new(Self {
                place: Mutex::new(place),
                err: Mutex::new(None),
                calls: std::sync::atomic::AtomicI32::new(0),
            })
        }
        fn set(&self, place: Placement, err: Option<String>) {
            *self.place.lock().unwrap() = place;
            *self.err.lock().unwrap() = err;
        }
        fn calls(&self) -> i32 {
            self.calls.load(Ordering::SeqCst)
        }
    }

    impl Querier for FakeQuerier {
        fn game_window(&self, _: i32, _: &Match) -> Result<Placement, String> {
            self.calls.fetch_add(1, Ordering::SeqCst);
            if let Some(e) = self.err.lock().unwrap().clone() {
                return Err(e);
            }
            Ok(self.place.lock().unwrap().clone())
        }
    }

    fn test_visibility(q: Arc<dyn Querier>) -> (Arc<Watcher>, Arc<game::Watcher>) {
        let game = game::Watcher::new("DeadFrontier.exe", Duration::from_secs(3600));
        let cfg = Config::default();
        (
            Watcher::new(game.clone(), Arc::new(Mutex::new(cfg)), Some(q)),
            game,
        )
    }

    #[test]
    fn watcher_publishes_changes() {
        let q = FakeQuerier::new(Placement {
            known: true,
            on_active_workspace: true,
            monitor: "DP-2".into(),
            ..Placement::default()
        });
        let (w, game) = test_visibility(q.clone());
        let (tx, rx) = mpsc::channel();
        w.set_on_change(move |s| {
            let _ = tx.send(s);
        });
        w.refresh();
        assert!(!w.state().visible);
        assert_eq!(q.calls(), 0);

        game.set_state_for_testing(GameState {
            running: true,
            pid: 42,
            started_at: Some(Utc::now()),
        });
        w.refresh();
        let state = w.state();
        assert!(state.visible, "{state:?}");
        assert_eq!(state.monitor, "DP-2");

        q.set(
            Placement {
                known: true,
                on_active_workspace: false,
                workspace_name: "4".into(),
                monitor: "DP-2".into(),
                ..Placement::default()
            },
            None,
        );
        w.refresh();
        assert!(!w.state().visible);
        assert!(rx.try_recv().is_ok());
    }

    #[test]
    fn watcher_waits_for_first_window_without_flashing() {
        let q = FakeQuerier::new(Placement::default());
        let (w, game) = test_visibility(q.clone());
        let session = GameState {
            running: true,
            pid: 42,
            started_at: Some(Utc::now()),
        };
        game.set_state_for_testing(session);
        w.refresh();
        let state = w.state();
        assert!(
            !state.visible && state.reason.contains("waiting"),
            "{state:?}"
        );

        q.set(
            Placement {
                launcher_only: true,
                ..Placement::default()
            },
            None,
        );
        w.refresh();
        let state = w.state();
        assert!(
            !state.visible && state.reason.contains("launcher"),
            "{state:?}"
        );

        q.set(
            Placement {
                known: true,
                on_active_workspace: true,
                monitor: "DP-2".into(),
                ..Placement::default()
            },
            None,
        );
        w.refresh();
        assert!(w.state().visible);

        q.set(Placement::default(), None);
        w.refresh();
        assert!(
            w.state().visible,
            "transient unknown hid an established window"
        );

        game.set_state_for_testing(GameState {
            running: true,
            pid: 43,
            started_at: Some(session.started_at.unwrap() + chrono::Duration::minutes(1)),
        });
        w.refresh();
        let state = w.state();
        assert!(
            !state.visible && state.reason.contains("waiting"),
            "{state:?}"
        );
    }

    #[test]
    fn watcher_retries_after_a_transient_failure() {
        let q = FakeQuerier::new(Placement::default());
        q.set(Placement::default(), Some("no such socket".into()));
        let (w, game) = test_visibility(q.clone());
        game.set_state_for_testing(GameState {
            running: true,
            pid: 42,
            started_at: Some(Utc::now()),
        });
        w.refresh();
        w.refresh();
        assert_eq!(q.calls(), 2);
        assert!(w.state().visible);
        q.set(
            Placement {
                known: true,
                on_active_workspace: true,
                monitor: "DP-2".into(),
                ..Placement::default()
            },
            None,
        );
        w.refresh();
        assert_eq!(q.calls(), 3);
        assert!(w.state().visible);
        assert_eq!(w.state().monitor, "DP-2");
        assert!(w.last_query_error.lock().unwrap().is_none());
    }

    #[test]
    fn watcher_keeps_same_session_placement_on_query_error() {
        let q = FakeQuerier::new(Placement {
            known: true,
            on_active_workspace: false,
            workspace_name: "4".into(),
            monitor: "DP-2".into(),
            ..Placement::default()
        });
        let (w, game) = test_visibility(q.clone());
        let session = GameState {
            running: true,
            pid: 42,
            started_at: Some(Utc::now()),
        };
        game.set_state_for_testing(session);
        w.refresh();
        assert!(!w.state().visible);
        assert_eq!(w.state().monitor, "DP-2");

        q.set(Placement::default(), Some("socket reset".into()));
        w.refresh();
        assert!(!w.state().visible);
        assert_eq!(w.state().monitor, "DP-2");

        game.set_state_for_testing(GameState {
            running: true,
            pid: 43,
            started_at: Some(session.started_at.unwrap() + chrono::Duration::seconds(1)),
        });
        w.refresh();
        assert!(w.state().visible, "new session must fail open");
        assert!(w.state().monitor.is_empty());
    }

    #[test]
    fn watcher_tray_toggle() {
        let q = FakeQuerier::new(Placement {
            known: true,
            on_active_workspace: true,
            ..Placement::default()
        });
        let (w, game) = test_visibility(q);
        game.set_state_for_testing(GameState {
            running: true,
            pid: 42,
            started_at: Some(Utc::now()),
        });
        w.refresh();
        assert!(w.state().visible);
        w.set_enabled(false);
        w.refresh();
        assert!(!w.state().visible);
        assert!(!w.enabled());
        w.set_enabled(true);
        w.refresh();
        assert!(w.state().visible);
    }

    #[test]
    fn watcher_config_switch_preserves_manual_preference() {
        let q = FakeQuerier::new(Placement {
            known: true,
            on_active_workspace: true,
            ..Placement::default()
        });
        let game = game::Watcher::new("DeadFrontier.exe", Duration::from_secs(3600));
        let cfg = Arc::new(Mutex::new(Config::default()));
        let w = Watcher::new(game.clone(), cfg.clone(), Some(q));
        game.set_state_for_testing(GameState {
            running: true,
            pid: 42,
            started_at: Some(Utc::now()),
        });

        w.refresh();
        assert!(w.state().visible);
        cfg.lock().unwrap().hud.enabled = false;
        w.refresh();
        let state = w.state();
        assert!(
            !state.visible && state.reason.contains("config"),
            "{state:?}"
        );
        assert!(
            w.enabled(),
            "config must not overwrite the manual preference"
        );

        cfg.lock().unwrap().hud.enabled = true;
        w.refresh();
        assert!(w.state().visible);

        w.set_enabled(false);
        w.refresh();
        cfg.lock().unwrap().hud.enabled = false;
        w.refresh();
        cfg.lock().unwrap().hud.enabled = true;
        w.refresh();
        assert!(!w.state().visible);
        assert!(!w.enabled());
    }
}
