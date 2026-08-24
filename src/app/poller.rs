//! Player-record and challenge-board pollers. The only code that talks to DF.

use chrono::Utc;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use crate::app::rategate::{Cancelled, Gate};
use crate::app::state;
use crate::app::store::Store;
use crate::config::Config;
use crate::data::challenges;
use crate::model::{PollerStatus, Tick};
use crate::net::creds::Store as Creds;
use crate::net::dfclient::{self, Client};
use crate::wake::Notify;

pub const MIN_REQUEST_GAP: Duration = Duration::from_secs(1);
const PAUSE_RECHECK: Duration = Duration::from_secs(2);

fn jitter_unit() -> f64 {
    static N: AtomicU64 = AtomicU64::new(1);
    let t = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos() as u64)
        .unwrap_or(0);
    let n = N.fetch_add(1, Ordering::Relaxed);
    let mix = t
        .wrapping_mul(0x9E3779B97F4A7C15)
        .wrapping_add(n.wrapping_mul(0xBF58476D1CE4E5B9));
    (mix >> 11) as f64 / ((1u64 << 53) as f64)
}

pub fn jittered(base: Duration, jitter: f64) -> Duration {
    if jitter <= 0.0 {
        return base;
    }
    let factor = 1.0 + (jitter_unit() * 2.0 - 1.0) * jitter;
    Duration::from_secs_f64((base.as_secs_f64() * factor).max(0.0))
}

pub struct PlayerPoller {
    client: Arc<Mutex<Client>>,
    creds: Arc<Creds>,
    store: Arc<Store>,
    cfg: Arc<Mutex<Config>>,
    gate: Arc<Gate>,
    stop: Arc<AtomicBool>,
    wake: Arc<Notify>,
    min_gap: Mutex<Duration>,
    game_running: Arc<AtomicBool>,
    status: Mutex<PollerStatus>,
    last_poll: Mutex<Option<Instant>>,
    on_tick: Mutex<Option<Arc<dyn Fn(Tick) + Send + Sync>>>,
}

impl PlayerPoller {
    pub fn new(
        client: Arc<Mutex<Client>>,
        creds: Arc<Creds>,
        store: Arc<Store>,
        cfg: Arc<Mutex<Config>>,
        gate: Arc<Gate>,
        stop: Arc<AtomicBool>,
        game_running: Arc<AtomicBool>,
    ) -> Arc<Self> {
        Arc::new(Self {
            client,
            creds,
            store,
            cfg,
            gate,
            stop,
            wake: Arc::new(Notify::new()),
            min_gap: Mutex::new(MIN_REQUEST_GAP),
            game_running,
            status: Mutex::new(PollerStatus::default()),
            last_poll: Mutex::new(None),
            on_tick: Mutex::new(None),
        })
    }

    pub fn set_on_tick(&self, fn_: impl Fn(Tick) + Send + Sync + 'static) {
        *self.on_tick.lock().unwrap() = Some(Arc::new(fn_));
    }

    #[cfg(test)]
    pub fn set_min_gap(&self, d: Duration) {
        *self.min_gap.lock().unwrap() = d;
    }

    pub fn wake(&self) {
        self.wake.ping();
    }

    pub fn resume(&self) {
        {
            let mut st = self.status.lock().unwrap();
            if st.stale {
                eprintln!("poller: credentials refreshed, resuming");
            }
            st.stale = false;
            st.failures = 0;
        }
        self.wake();
    }

    pub fn status(&self) -> PollerStatus {
        self.status.lock().unwrap().clone()
    }

    fn pause_reason(&self) -> Option<String> {
        if self.creds.get().is_none() {
            return Some("waiting for the browser bridge to deliver a session".into());
        }
        if self.status.lock().unwrap().stale {
            return Some(
                "credentials were rejected; open any Dead Frontier page to refresh them".into(),
            );
        }
        let cfg = self.cfg.lock().unwrap().clone();
        if cfg.poll.only_when_game_running && !self.game_running.load(Ordering::SeqCst) {
            return Some("the game is not running (poll.only_when_game_running)".into());
        }
        None
    }

    fn interval(&self) -> Duration {
        let cfg = self.cfg.lock().unwrap();
        if self.game_running.load(Ordering::SeqCst) {
            cfg.poll.active_interval.0
        } else {
            cfg.poll.idle_interval.0
        }
    }

    fn backoff(&self, n: i32) -> Duration {
        let (cap, jitter) = {
            let cfg = self.cfg.lock().unwrap();
            (cfg.poll.backoff_max.0, cfg.poll.jitter)
        };
        let mut d = self.interval();
        let mut i = 1;
        while i < n && d < cap {
            d *= 2;
            i += 1;
        }
        if d > cap {
            d = cap;
        }
        jittered(d, jitter)
    }

    pub fn run(self: Arc<Self>) {
        let mut next = Instant::now();
        let mut logged_pause = String::new();
        loop {
            if self.stop.load(Ordering::SeqCst) {
                return;
            }
            if let Some(reason) = self.pause_reason() {
                {
                    let mut st = self.status.lock().unwrap();
                    st.paused = true;
                    st.pause_reason = reason.clone();
                    st.next_attempt = None;
                }
                self.store.set_poller_status(self.status());
                if logged_pause != reason {
                    eprintln!("poller: paused - {reason}");
                    logged_pause = reason;
                }
                self.wake.wait_timeout(PAUSE_RECHECK);
                next = Instant::now();
                continue;
            }
            if !logged_pause.is_empty() {
                eprintln!("poller: resumed");
                logged_pause.clear();
                let mut st = self.status.lock().unwrap();
                st.paused = false;
                st.pause_reason.clear();
            }

            if let Some(last) = *self.last_poll.lock().unwrap() {
                let floor = last + *self.min_gap.lock().unwrap();
                if next < floor {
                    next = floor;
                }
            }
            {
                let mut st = self.status.lock().unwrap();
                st.next_attempt = Some(
                    Utc::now()
                        + chrono::Duration::from_std(
                            next.saturating_duration_since(Instant::now()),
                        )
                        .unwrap_or_default(),
                );
            }
            self.store.set_poller_status(self.status());

            let wait = next.saturating_duration_since(Instant::now());
            if wait > Duration::ZERO {
                self.wake.wait_timeout(wait);
                if self.stop.load(Ordering::SeqCst) {
                    return;
                }
                if let Some(last) = *self.last_poll.lock().unwrap() {
                    next = last + *self.min_gap.lock().unwrap();
                } else {
                    next = Instant::now();
                }
                continue;
            }

            let tick = self.poll_once(true);
            if self.stop.load(Ordering::SeqCst) {
                return;
            }
            if tick
                .err
                .as_deref()
                .is_some_and(|e| e.contains("credentials rejected"))
            {
                continue;
            }
            if tick.err.is_some() {
                next = Instant::now() + self.backoff(self.status().failures);
                continue;
            }
            let jitter = self.cfg.lock().unwrap().poll.jitter;
            next = Instant::now() + jittered(self.interval(), jitter);
        }
    }

    pub fn poll_once(&self, scheduled: bool) -> Tick {
        let Some((cr, _)) = self.creds.get() else {
            return Tick {
                at: Utc::now(),
                vars: Default::default(),
                err: Some("no credentials".into()),
                scheduled,
            };
        };
        {
            let mut st = self.status.lock().unwrap();
            st.last_attempt = Some(Utc::now());
            st.total_polls += 1;
        }
        *self.last_poll.lock().unwrap() = Some(Instant::now());
        if self.gate.wait(&self.stop).is_err() {
            return Tick {
                at: Utc::now(),
                vars: Default::default(),
                err: Some(Cancelled.to_string()),
                scheduled,
            };
        }
        let result = self.client.lock().unwrap().get_values(&cr.to_df());
        let tick = match result {
            Ok(vars) => Tick {
                at: Utc::now(),
                vars,
                err: None,
                scheduled,
            },
            Err(err) => Tick {
                at: Utc::now(),
                vars: Default::default(),
                err: Some(err.to_string()),
                scheduled,
            },
        };
        {
            let mut st = self.status.lock().unwrap();
            match &tick.err {
                None => {
                    st.failures = 0;
                    st.last_error.clear();
                    st.last_success = Some(tick.at);
                }
                Some(err)
                    if dfclient::is_stale(&DummyErr(err.clone()))
                        || err.contains("credentials rejected") =>
                {
                    st.stale = true;
                    st.last_error = err.clone();
                    st.total_failure += 1;
                    eprintln!(
                        "poller: the server rejected our credentials - polling STOPPED. \
                         Open any Dead Frontier page with the bridge userscript installed and it will resume by itself."
                    );
                }
                Some(err) => {
                    st.failures += 1;
                    st.last_error = err.clone();
                    st.total_failure += 1;
                    if st.failures == 1 || st.failures % 10 == 0 {
                        eprintln!("poller: {err} (backing off; will keep trying)");
                    }
                }
            }
        }
        self.store.set_poller_status(self.status());
        if let Some(fn_) = self.on_tick.lock().unwrap().clone() {
            fn_(tick.clone());
        }
        tick
    }
}

struct DummyErr(String);
impl std::fmt::Display for DummyErr {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.0)
    }
}
impl std::fmt::Debug for DummyErr {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.0)
    }
}
impl std::error::Error for DummyErr {}

pub struct ChallengePoller {
    client: Arc<Mutex<Client>>,
    creds: Arc<Creds>,
    store: Arc<Store>,
    persist: Arc<state::Store>,
    cfg: Arc<Mutex<Config>>,
    gate: Arc<Gate>,
    stop: Arc<AtomicBool>,
    wake: Arc<Notify>,
    game_running: Arc<AtomicBool>,
    stale: AtomicBool,
    failures: Mutex<i32>,
}

impl ChallengePoller {
    pub fn new(
        client: Arc<Mutex<Client>>,
        creds: Arc<Creds>,
        store: Arc<Store>,
        persist: Arc<state::Store>,
        cfg: Arc<Mutex<Config>>,
        gate: Arc<Gate>,
        stop: Arc<AtomicBool>,
        game_running: Arc<AtomicBool>,
    ) -> Arc<Self> {
        Arc::new(Self {
            client,
            creds,
            store,
            persist,
            cfg,
            gate,
            stop,
            wake: Arc::new(Notify::new()),
            game_running,
            stale: AtomicBool::new(false),
            failures: Mutex::new(0),
        })
    }

    pub fn wake(&self) {
        self.wake.ping();
    }

    pub fn resume(&self) {
        self.stale.store(false, Ordering::SeqCst);
        *self.failures.lock().unwrap() = 0;
        self.wake();
    }

    fn pause_reason(&self) -> Option<String> {
        let cfg = self.cfg.lock().unwrap().clone();
        if !cfg.widget.challenges.enabled {
            return Some("the challenge widget is disabled".into());
        }
        let Some((cr, salt)) = self.creds.get() else {
            return Some("waiting for the browser bridge to deliver a session".into());
        };
        if self.stale.load(Ordering::SeqCst) {
            return Some(
                "credentials were rejected; open any Dead Frontier page to refresh them".into(),
            );
        }
        if cfg.signing_salt(|| salt.clone()).is_empty() {
            return Some(
                "no signing salt yet - load the Outpost page with the bridge userscript \
                 (***+) or the the bridge userscript installed"
                    .into(),
            );
        }
        if cr.cookie.is_empty() {
            return Some(
                "no session cookie yet - the challenge board needs one; load any \
                 Dead Frontier page to send it"
                    .into(),
            );
        }
        if cfg.poll.only_when_game_running && !self.game_running.load(Ordering::SeqCst) {
            return Some("the game is not running (poll.only_when_game_running)".into());
        }
        match self.store.snapshot() {
            Some(s) if s.level > 0 => None,
            _ => Some(
                "waiting for the first player record (the level decides which challenges apply to you)"
                    .into(),
            ),
        }
    }

    pub fn run(self: Arc<Self>) {
        let mut next = Instant::now();
        let mut logged_pause = String::new();
        loop {
            if self.stop.load(Ordering::SeqCst) {
                return;
            }
            if let Some(reason) = self.pause_reason() {
                if logged_pause != reason {
                    eprintln!("challenges: paused - {reason}");
                    logged_pause = reason;
                    self.store.set_challenge_status(logged_pause.clone());
                }
                self.wake.wait_timeout(PAUSE_RECHECK);
                next = Instant::now();
                continue;
            }
            if !logged_pause.is_empty() {
                eprintln!("challenges: resumed");
                logged_pause.clear();
                self.store.set_challenge_status(String::new());
            }
            if let Some(floor) = self.gate.reserved() {
                if next < floor {
                    next = floor;
                }
            }
            let wait = next.saturating_duration_since(Instant::now());
            if wait > Duration::ZERO {
                self.wake.wait_timeout(wait);
                if self.stop.load(Ordering::SeqCst) {
                    return;
                }
                if let Some(floor) = self.gate.reserved() {
                    next = floor;
                }
                continue;
            }
            if let Err(err) = self.poll_once() {
                if err.contains("credentials rejected") {
                    continue;
                }
                let n = *self.failures.lock().unwrap();
                let cfg = self.cfg.lock().unwrap();
                let mut d = cfg.poll.effective_challenge_interval(true);
                let mut i = 1;
                while i < n && d < cfg.poll.backoff_max.0 {
                    d *= 2;
                    i += 1;
                }
                if d > cfg.poll.backoff_max.0 {
                    d = cfg.poll.backoff_max.0;
                }
                next = Instant::now() + jittered(d, cfg.poll.jitter);
                continue;
            }
            let cfg = self.cfg.lock().unwrap();
            let running = self.game_running.load(Ordering::SeqCst);
            next = Instant::now()
                + jittered(
                    cfg.poll.effective_challenge_interval(running),
                    cfg.poll.jitter,
                );
        }
    }

    fn poll_once(&self) -> Result<(), String> {
        let Some((cr, salt_stored)) = self.creds.get() else {
            return Err("no credentials".into());
        };
        let cfg = self.cfg.lock().unwrap().clone();
        let salt = cfg.signing_salt(|| salt_stored.clone());
        self.gate.wait(&self.stop).map_err(|e| e.to_string())?;
        let vars = {
            let mut client = self.client.lock().unwrap();
            client.cookie = cr.cookie.clone();
            client
                .load_challenge(&cr.to_df(), &salt)
                .map_err(|e| e.to_string())
        };
        match vars {
            Ok(vars) => {
                *self.failures.lock().unwrap() = 0;
                let (level, gold) = self
                    .store
                    .snapshot()
                    .map(|s| (s.level, s.gold_member))
                    .unwrap_or((0, false));
                let board = challenges::parse(&vars, level, gold);
                let board = self.persist.remember_challenge_board(board);
                self.store.set_challenges(board);
                self.store.set_challenge_status(String::new());
                Ok(())
            }
            Err(err) => {
                if err.contains("credentials rejected") {
                    self.stale.store(true, Ordering::SeqCst);
                } else {
                    *self.failures.lock().unwrap() += 1;
                }
                let n = *self.failures.lock().unwrap();
                if n == 1 || n % 10 == 0 || err.contains("credentials rejected") {
                    eprintln!("challenges: {err}");
                }
                Err(err)
            }
        }
    }
}

pub fn spawn<F>(name: &str, stop: Arc<AtomicBool>, f: F)
where
    F: FnOnce() + Send + 'static,
{
    let n = name.to_string();
    thread::Builder::new()
        .name(n.clone())
        .spawn(move || {
            f();
            let _ = stop;
        })
        .unwrap_or_else(|e| panic!("spawn {n}: {e}"));
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::net::creds::Credentials;
    use crate::net::dfclient::Client;
    use std::io::{BufRead, BufReader, Write};
    use std::net::TcpListener;
    use std::sync::Mutex as StdMutex;

    fn player_record() -> &'static str {
        "&df_level=415&df_exp=10000&df_positionx=1100&df_positiony=1100&df_positionz=0&df_tradezone=5&df_inoutpost=0&df_dangerlevel=3&df_cash=1000"
    }

    fn spawn_df(
        reply: impl Fn(usize) -> (u16, String) + Send + Sync + 'static,
    ) -> (String, Arc<StdMutex<usize>>) {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = listener.local_addr().unwrap();
        let hits = Arc::new(StdMutex::new(0usize));
        let hits2 = hits.clone();
        let reply = Arc::new(reply);
        thread::spawn(move || {
            for _ in 0..64 {
                let Ok((mut stream, _)) = listener.accept() else {
                    break;
                };
                let _ = stream.set_read_timeout(Some(Duration::from_secs(2)));
                let _ = stream.set_write_timeout(Some(Duration::from_secs(2)));
                let mut reader = BufReader::new(stream.try_clone().unwrap());
                let mut line = String::new();
                if reader.read_line(&mut line).is_err() {
                    continue;
                }
                let mut content_len = 0usize;
                loop {
                    let mut h = String::new();
                    if reader.read_line(&mut h).is_err() || h == "\r\n" || h == "\n" {
                        break;
                    }
                    if let Some(v) = h.to_ascii_lowercase().strip_prefix("content-length:") {
                        content_len = v.trim().parse().unwrap_or(0);
                    }
                }
                let mut body = vec![0; content_len];
                if content_len > 0 {
                    let _ = std::io::Read::read_exact(&mut reader, &mut body);
                }
                let n = {
                    let mut g = hits2.lock().unwrap();
                    *g += 1;
                    *g
                };
                let (status, body) = reply(n);
                let _ = write!(
                    stream,
                    "HTTP/1.1 {status} OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                    body.len()
                );
            }
        });
        (format!("http://{addr}"), hits)
    }

    fn test_poller(base: &str) -> (Arc<PlayerPoller>, Arc<AtomicBool>) {
        let mut cfg = Config::default();
        cfg.poll.active_interval = crate::config::Duration(Duration::from_millis(20));
        cfg.poll.idle_interval = crate::config::Duration(Duration::from_millis(20));
        cfg.poll.backoff_max = crate::config::Duration(Duration::from_millis(40));
        cfg.poll.jitter = 0.0;
        cfg.poll.only_when_game_running = false;
        cfg.df.timeout = crate::config::Duration(Duration::from_secs(2));
        let creds = Arc::new(Creds::new(""));
        creds
            .set(
                Credentials {
                    user_id: "1234567".into(),
                    password: "hash".into(),
                    sc: "sc".into(),
                    cookie: String::new(),
                },
                "salt",
            )
            .unwrap();
        let agent = ureq::AgentBuilder::new().timeout(cfg.df.timeout.0).build();
        let client = Client::with_agent(agent, base, "df-hud-test");
        client.disable_public_get_values();
        let stop = Arc::new(AtomicBool::new(false));
        let running = Arc::new(AtomicBool::new(true));
        let p = PlayerPoller::new(
            Arc::new(Mutex::new(client)),
            creds,
            Arc::new(Store::new(None)),
            Arc::new(Mutex::new(cfg)),
            Arc::new(Gate::new(Duration::from_millis(5))),
            stop.clone(),
            running,
        );
        p.set_min_gap(Duration::from_millis(20));
        (p, stop)
    }

    #[test]
    fn polls_and_reports_ticks() {
        let (base, hits) = spawn_df(|_| (200, player_record().into()));
        let (p, stop) = test_poller(&base);
        let ticks = Arc::new(StdMutex::new(Vec::new()));
        let slot = ticks.clone();
        p.set_on_tick(move |t| slot.lock().unwrap().push(t));
        let p2 = p.clone();
        let h = thread::spawn(move || p2.run());
        let start = Instant::now();
        while ticks.lock().unwrap().is_empty() && start.elapsed() < Duration::from_secs(2) {
            thread::sleep(Duration::from_millis(10));
        }
        stop.store(true, Ordering::SeqCst);
        p.wake();
        let _ = h.join();
        let got = ticks.lock().unwrap();
        assert!(!got.is_empty(), "no ticks");
        assert!(got[0].err.is_none(), "{:?}", got[0].err);
        assert_eq!(got[0].vars.get("df_level").map(String::as_str), Some("415"));
        assert!(got[0].scheduled);
        assert!(*hits.lock().unwrap() >= 1);
    }

    #[test]
    fn html_response_is_a_failure() {
        let (base, _) = spawn_df(|_| {
            (
                200,
                "<!DOCTYPE html><html><title>Just a moment...</title></html>".into(),
            )
        });
        let (p, _stop) = test_poller(&base);
        let tick = p.poll_once(false);
        assert!(
            tick.err.is_some(),
            "Cloudflare HTML must not be treated as data"
        );
        assert!(!tick.scheduled);
        let err = tick.err.unwrap();
        assert!(
            !err.contains("credentials rejected"),
            "HTML is not a credential problem: {err}"
        );
    }
}
