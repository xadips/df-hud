//! Press one configured key in the game window, once per launch.
//!
//! The FPS readout starts off every launch and is not a saved setting, so the
//! only way to have it on is to press the game's own key. The launcher dialog
//! is a separate one-shot so the Input tab stays reachable by unticking the
//! tray box.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime};

use crate::config::GameKeys as GameKeysCfg;
use crate::desktop::{Client, Placement};
use crate::model::GameState;

pub struct Keys {
    fps: AtomicBool,
    dismiss: AtomicBool,
    inner: Mutex<Inner>,
}

#[derive(Default)]
struct Inner {
    pid: i32,
    seen_at: Option<SystemTime>,
    sent: bool,
    launcher_seen_at: Option<SystemTime>,
    launcher_sent: bool,
}

impl Keys {
    pub fn new(cfg: &GameKeysCfg) -> Arc<Self> {
        let k = Self {
            fps: AtomicBool::new(cfg.fps_display),
            dismiss: AtomicBool::new(cfg.dismiss_launcher),
            inner: Mutex::new(Inner::default()),
        };
        Arc::new(k)
    }

    pub fn set_fps_display(&self, on: bool) {
        self.fps.store(on, Ordering::SeqCst);
    }

    pub fn fps_display(&self) -> bool {
        self.fps.load(Ordering::SeqCst)
    }

    pub fn set_dismiss_launcher(&self, on: bool) {
        self.dismiss.store(on, Ordering::SeqCst);
    }

    pub fn dismiss_launcher(&self) -> bool {
        self.dismiss.load(Ordering::SeqCst)
    }

    pub fn apply_config(&self, cfg: &GameKeysCfg) {
        self.fps.store(cfg.fps_display, Ordering::SeqCst);
        self.dismiss.store(cfg.dismiss_launcher, Ordering::SeqCst);
    }

    pub fn tick(
        &self,
        now: SystemTime,
        cfg: &GameKeysCfg,
        game: GameState,
        place: &Placement,
        send: &dyn Sender,
        active: Option<&str>,
        ready: bool,
    ) {
        if !game.running {
            *self.inner.lock().unwrap() = Inner::default();
            return;
        }

        if place.launcher_only && !place.launcher_address.is_empty() {
            self.dismiss_launcher_tick(now, cfg, game.pid, &place.launcher_address, send, active);
            return;
        }
        if !place.known || place.address.is_empty() {
            return;
        }

        let due = {
            let mut g = self.inner.lock().unwrap();
            if g.pid != game.pid {
                g.pid = game.pid;
                g.seen_at = Some(now);
                g.sent = false;
                g.launcher_seen_at = None;
                g.launcher_sent = false;
            }
            if g.seen_at.is_none() {
                g.seen_at = Some(now);
            }
            !g.sent
                && self.fps.load(Ordering::SeqCst)
                && now
                    .duration_since(g.seen_at.unwrap())
                    .unwrap_or(Duration::ZERO)
                    >= cfg.fps_delay.0
        };
        if !due {
            return;
        }
        if !can_send(&place.address, active, ready) {
            return;
        }

        self.inner.lock().unwrap().sent = true;
        if let Err(err) = send.send_key(&cfg.fps_key, &place.address) {
            eprintln!(
                "game keys: could not send {:?} to the game window: {err}",
                cfg.fps_key
            );
            return;
        }
        eprintln!(
            "game keys: sent {:?} to turn the game's FPS display on",
            cfg.fps_key
        );
    }

    fn dismiss_launcher_tick(
        &self,
        now: SystemTime,
        cfg: &GameKeysCfg,
        pid: i32,
        address: &str,
        send: &dyn Sender,
        active: Option<&str>,
    ) {
        if !self.dismiss.load(Ordering::SeqCst) {
            return;
        }
        let due = {
            let mut g = self.inner.lock().unwrap();
            if g.pid != pid {
                g.pid = pid;
                g.seen_at = None;
                g.sent = false;
                g.launcher_seen_at = Some(now);
                g.launcher_sent = false;
            }
            if g.launcher_seen_at.is_none() {
                g.launcher_seen_at = Some(now);
            }
            !g.launcher_sent
                && now
                    .duration_since(g.launcher_seen_at.unwrap())
                    .unwrap_or(Duration::ZERO)
                    >= cfg.launcher_delay.0
        };
        if !due {
            return;
        }
        if !can_send(address, active, true) {
            return;
        }

        self.inner.lock().unwrap().launcher_sent = true;
        if let Err(err) = send.send_key(&cfg.launcher_key, address) {
            eprintln!("game keys: could not dismiss the launcher: {err}");
            return;
        }
        eprintln!(
            "game keys: pressed {:?} on the launcher dialog",
            cfg.launcher_key
        );
    }
}

fn can_send(address: &str, active: Option<&str>, ready: bool) -> bool {
    ready && active == Some(address)
}

pub trait Sender {
    fn send_key(&self, key: &str, address: &str) -> Result<(), String>;
}

impl Sender for dyn Client {
    fn send_key(&self, key: &str, address: &str) -> Result<(), String> {
        Client::send_key(self, key, address)
    }
}

impl Sender for Box<dyn Client> {
    fn send_key(&self, key: &str, address: &str) -> Result<(), String> {
        Client::send_key(self.as_ref(), key, address)
    }
}

pub fn spawn(handle: Arc<crate::app::Handle>, stop: Arc<std::sync::atomic::AtomicBool>) {
    std::thread::Builder::new()
        .name("df-hud-gamekeys".into())
        .spawn(move || {
            let send = crate::desktop::new_client();
            while !stop.load(Ordering::SeqCst) && !handle.stopped() {
                let cfg = handle.cfg.lock().unwrap().game_keys.clone();
                let game = handle.game.state();
                let place = handle.vis.placement();
                let ready = handle.store.client_in_world(chrono::Utc::now());
                let active = send.active_address();
                handle.gamekeys.tick(
                    SystemTime::now(),
                    &cfg,
                    game,
                    &place,
                    &send,
                    active.as_deref(),
                    ready,
                );
                std::thread::sleep(Duration::from_millis(200));
            }
        })
        .ok();
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::Duration as CfgDuration;
    use crate::config::GameKeys as GameKeysCfg;
    use std::sync::Mutex;
    use std::time::Duration;

    struct Fake {
        sent: Mutex<Vec<(String, String)>>,
        err: Mutex<Option<String>>,
    }

    impl Fake {
        fn new() -> Self {
            Self {
                sent: Mutex::new(Vec::new()),
                err: Mutex::new(None),
            }
        }
        fn count(&self) -> usize {
            self.sent.lock().unwrap().len()
        }
        fn last(&self) -> (String, String) {
            self.sent.lock().unwrap().last().cloned().unwrap()
        }
    }

    impl Sender for Fake {
        fn send_key(&self, key: &str, address: &str) -> Result<(), String> {
            self.sent
                .lock()
                .unwrap()
                .push((key.to_string(), address.to_string()));
            match self.err.lock().unwrap().as_ref() {
                Some(e) => Err(e.clone()),
                None => Ok(()),
            }
        }
    }

    fn cfg() -> GameKeysCfg {
        GameKeysCfg {
            fps_display: true,
            fps_key: "y".into(),
            fps_delay: CfgDuration(Duration::from_secs(5)),
            dismiss_launcher: false,
            launcher_key: "Return".into(),
            launcher_title: "Dead Frontier Configuration".into(),
            launcher_delay: CfgDuration(Duration::from_millis(500)),
        }
    }

    fn running(pid: i32, address: &str) -> (GameState, Placement) {
        (
            GameState {
                running: true,
                pid,
                ..GameState::default()
            },
            Placement {
                known: true,
                address: address.into(),
                ..Placement::default()
            },
        )
    }

    fn launcher(pid: i32, address: &str) -> (GameState, Placement) {
        (
            GameState {
                running: true,
                pid,
                ..GameState::default()
            },
            Placement {
                launcher_only: true,
                launcher_address: address.into(),
                ..Placement::default()
            },
        )
    }

    fn tick(
        k: &Keys,
        now: SystemTime,
        cfg: &GameKeysCfg,
        game: GameState,
        place: &Placement,
        send: &Fake,
    ) {
        let dest = if !place.address.is_empty() {
            Some(place.address.as_str())
        } else if !place.launcher_address.is_empty() {
            Some(place.launcher_address.as_str())
        } else {
            None
        };
        k.tick(now, cfg, game, place, send, dest, true);
    }

    #[test]
    fn fps_key_is_sent_once_the_window_has_been_up_for_the_delay() {
        let k = Keys::new(&cfg());
        let send = Fake::new();
        let start = SystemTime::UNIX_EPOCH + Duration::from_secs(1000);
        let (game, place) = running(42, "0xabc");
        tick(&k, start, &cfg(), game, &place, &send);
        assert_eq!(send.count(), 0);
        tick(
            &k,
            start + Duration::from_secs(4),
            &cfg(),
            game,
            &place,
            &send,
        );
        assert_eq!(send.count(), 0);
        tick(
            &k,
            start + Duration::from_secs(5),
            &cfg(),
            game,
            &place,
            &send,
        );
        assert_eq!(send.count(), 1);
        assert_eq!(send.last(), ("y".into(), "0xabc".into()));
    }

    #[test]
    fn fps_key_is_sent_only_once_per_launch() {
        let k = Keys::new(&cfg());
        let send = Fake::new();
        let start = SystemTime::UNIX_EPOCH + Duration::from_secs(1000);
        let (game, place) = running(42, "0xabc");
        tick(&k, start, &cfg(), game, &place, &send);
        for i in 0..30 {
            tick(
                &k,
                start + Duration::from_secs(5 + i),
                &cfg(),
                game,
                &place,
                &send,
            );
        }
        assert_eq!(send.count(), 1);
    }

    #[test]
    fn fps_key_waits_out_the_launcher() {
        let k = Keys::new(&cfg());
        let send = Fake::new();
        let start = SystemTime::UNIX_EPOCH + Duration::from_secs(1000);
        let (game, place) = launcher(42, "");
        for i in 0..60 {
            tick(
                &k,
                start + Duration::from_secs(i),
                &cfg(),
                game,
                &place,
                &send,
            );
        }
        assert_eq!(send.count(), 0);
        let window = start + Duration::from_secs(60);
        let (game, place) = running(42, "0xabc");
        tick(&k, window, &cfg(), game, &place, &send);
        assert_eq!(send.count(), 0);
        tick(
            &k,
            window + Duration::from_secs(5),
            &cfg(),
            game,
            &place,
            &send,
        );
        assert_eq!(send.count(), 1);
    }

    #[test]
    fn fps_key_rearms_when_the_pid_changes() {
        let k = Keys::new(&cfg());
        let send = Fake::new();
        let start = SystemTime::UNIX_EPOCH + Duration::from_secs(1000);
        let (game, place) = running(42, "0xabc");
        tick(&k, start, &cfg(), game, &place, &send);
        tick(
            &k,
            start + Duration::from_secs(5),
            &cfg(),
            game,
            &place,
            &send,
        );
        let second = start + Duration::from_secs(6);
        let (game, place) = running(99, "0xdef");
        tick(&k, second, &cfg(), game, &place, &send);
        tick(
            &k,
            second + Duration::from_secs(5),
            &cfg(),
            game,
            &place,
            &send,
        );
        assert_eq!(send.count(), 2);
        assert_eq!(send.last().1, "0xdef");
    }

    #[test]
    fn fps_key_sends_nothing_when_off() {
        let k = Keys::new(&cfg());
        k.set_fps_display(false);
        let send = Fake::new();
        let start = SystemTime::UNIX_EPOCH + Duration::from_secs(1000);
        let (game, place) = running(42, "0xabc");
        for i in 0..20 {
            tick(
                &k,
                start + Duration::from_secs(i),
                &cfg(),
                game,
                &place,
                &send,
            );
        }
        assert_eq!(send.count(), 0);
    }

    #[test]
    fn fps_key_fires_when_turned_on_mid_session_but_not_twice() {
        let k = Keys::new(&cfg());
        k.set_fps_display(false);
        let send = Fake::new();
        let start = SystemTime::UNIX_EPOCH + Duration::from_secs(1000);
        let (game, place) = running(42, "0xabc");
        tick(&k, start, &cfg(), game, &place, &send);
        tick(
            &k,
            start + Duration::from_secs(30),
            &cfg(),
            game,
            &place,
            &send,
        );
        assert_eq!(send.count(), 0);
        k.set_fps_display(true);
        tick(
            &k,
            start + Duration::from_secs(31),
            &cfg(),
            game,
            &place,
            &send,
        );
        assert_eq!(send.count(), 1);
        k.set_fps_display(false);
        tick(
            &k,
            start + Duration::from_secs(32),
            &cfg(),
            game,
            &place,
            &send,
        );
        k.set_fps_display(true);
        for i in 0..10 {
            tick(
                &k,
                start + Duration::from_secs(33 + i),
                &cfg(),
                game,
                &place,
                &send,
            );
        }
        assert_eq!(send.count(), 1);
    }

    #[test]
    fn fps_key_does_not_retry_a_failed_send() {
        let k = Keys::new(&cfg());
        let send = Fake::new();
        *send.err.lock().unwrap() = Some("window not found".into());
        let start = SystemTime::UNIX_EPOCH + Duration::from_secs(1000);
        let (game, place) = running(42, "0xabc");
        tick(&k, start, &cfg(), game, &place, &send);
        for i in 0..20 {
            tick(
                &k,
                start + Duration::from_secs(5 + i),
                &cfg(),
                game,
                &place,
                &send,
            );
        }
        assert_eq!(send.count(), 1);
    }

    #[test]
    fn launcher_is_dismissed_once_after_the_delay() {
        let mut cfg = cfg();
        cfg.dismiss_launcher = true;
        let k = Keys::new(&cfg);
        k.set_dismiss_launcher(true);
        let send = Fake::new();
        let start = SystemTime::UNIX_EPOCH + Duration::from_secs(1000);
        let (game, place) = launcher(42, "0x111");
        tick(&k, start, &cfg, game, &place, &send);
        assert_eq!(send.count(), 0);
        tick(
            &k,
            start + Duration::from_secs(1),
            &cfg,
            game,
            &place,
            &send,
        );
        assert_eq!(send.count(), 1);
        assert_eq!(send.last(), ("Return".into(), "0x111".into()));
        for i in 2..20 {
            tick(
                &k,
                start + Duration::from_secs(i),
                &cfg,
                game,
                &place,
                &send,
            );
        }
        assert_eq!(send.count(), 1);
    }

    #[test]
    fn launcher_is_left_alone_when_off() {
        let k = Keys::new(&cfg());
        let send = Fake::new();
        let start = SystemTime::UNIX_EPOCH + Duration::from_secs(1000);
        let (game, place) = launcher(42, "0x111");
        for i in 0..30 {
            tick(
                &k,
                start + Duration::from_secs(i),
                &cfg(),
                game,
                &place,
                &send,
            );
        }
        assert_eq!(send.count(), 0);
    }

    #[test]
    fn fps_delay_runs_from_the_window_after_dismissing_the_launcher() {
        let mut cfg = cfg();
        cfg.dismiss_launcher = true;
        let k = Keys::new(&cfg);
        k.set_dismiss_launcher(true);
        let send = Fake::new();
        let start = SystemTime::UNIX_EPOCH + Duration::from_secs(1000);
        let (game, place) = launcher(42, "0x111");
        tick(&k, start, &cfg, game, &place, &send);
        tick(
            &k,
            start + Duration::from_secs(1),
            &cfg,
            game,
            &place,
            &send,
        );
        assert_eq!(send.count(), 1);
        let window = start + Duration::from_secs(3);
        let (game, place) = running(42, "0xabc");
        tick(&k, window, &cfg, game, &place, &send);
        tick(
            &k,
            window + Duration::from_secs(4),
            &cfg,
            game,
            &place,
            &send,
        );
        assert_eq!(send.count(), 1);
        tick(
            &k,
            window + Duration::from_secs(5),
            &cfg,
            game,
            &place,
            &send,
        );
        assert_eq!(send.count(), 2);
        assert_eq!(send.last(), ("y".into(), "0xabc".into()));
    }

    #[test]
    fn fps_display_is_off_by_default() {
        let cfg = crate::config::Config::default();
        assert!(!cfg.game_keys.fps_display);
        assert_eq!(cfg.game_keys.fps_key, "y");
        assert!(!cfg.game_keys.dismiss_launcher);
    }
}
