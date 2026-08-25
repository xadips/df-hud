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
type TickHandler = Arc<dyn Fn(Tick) + Send + Sync>;

/// Why the board is empty. These print beside it as `challenges: <reason>`, on
/// one line that clips rather than wraps, so they name the next move in the
/// words the docs use and leave the rest to docs/install.md.
const NEED_SCRIPT: &str = "no session yet - install the bridge script";
const SESSION_EXPIRED: &str = "session expired - open any Dead Frontier page";
const SCRIPT_TOO_OLD: &str = "bridge script sends no salt - update it";
const NEED_COOKIE: &str = "load any Dead Frontier page to send the session";
const BOARD_RETRY: &str = "could not load the board (retrying)";

#[derive(Clone)]
pub struct PollerRuntime {
    pub creds: Arc<Creds>,
    pub store: Arc<Store>,
    pub cfg: Arc<Mutex<Config>>,
    pub gate: Arc<Gate>,
    pub stop: Arc<AtomicBool>,
    pub shutdown: Arc<Notify>,
    pub game_running: Arc<AtomicBool>,
    pub session_stale: Arc<AtomicBool>,
}

fn jitter_unit() -> f64 {
    static N: AtomicU64 = AtomicU64::new(1);
    let t = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| d.as_nanos() as u64);
    let n = N.fetch_add(1, Ordering::Relaxed);
    let mix = t
        .wrapping_mul(0x9E37_79B9_7F4A_7C15)
        .wrapping_add(n.wrapping_mul(0xBF58_476D_1CE4_E5B9));
    (mix >> 11) as f64 / ((1u64 << 53) as f64)
}

pub fn jittered(base: Duration, jitter: f64) -> Duration {
    if jitter <= 0.0 {
        return base;
    }
    let factor = 1.0 + (jitter_unit() * 2.0 - 1.0) * jitter;
    Duration::from_secs_f64((base.as_secs_f64() * factor).max(0.0))
}

fn exponential_backoff(base: Duration, failures: i32, cap: Duration, jitter: f64) -> Duration {
    let mut d = base;
    let mut i = 1;
    while i < failures && d < cap {
        d *= 2;
        i += 1;
    }
    if d > cap {
        d = cap;
    }
    jittered(d, jitter)
}

enum Outcome {
    Stop,
    Stale,
    Err,
    Ok,
}

trait Schedule {
    fn stop(&self) -> &AtomicBool;
    fn wake(&self) -> &Notify;
    fn current_pause(&self) -> Option<String>;
    fn on_pause(&self, reason: &str, entered: bool);
    fn on_resume(&self);
    fn apply_floor(&self, next: &mut Instant);
    fn before_wait(&self, next: Instant);
    fn after_wake(&self, next: &mut Instant);
    fn poll(&self) -> Outcome;
    fn success_delay(&self) -> Duration;
    fn fail_delay(&self) -> Duration;
}

fn run_loop(s: &impl Schedule) {
    let mut next = Instant::now();
    let mut logged_pause = String::new();
    loop {
        if s.stop().load(Ordering::SeqCst) {
            return;
        }
        if let Some(reason) = s.current_pause() {
            let entered = logged_pause != reason;
            if entered {
                logged_pause.clone_from(&reason);
            }
            s.on_pause(&reason, entered);
            s.wake().wait_timeout(PAUSE_RECHECK);
            next = Instant::now();
            continue;
        }
        if !logged_pause.is_empty() {
            logged_pause.clear();
            s.on_resume();
        }
        s.apply_floor(&mut next);
        s.before_wait(next);
        let wait = next.saturating_duration_since(Instant::now());
        if wait > Duration::ZERO {
            s.wake().wait_timeout(wait);
            if s.stop().load(Ordering::SeqCst) {
                return;
            }
            s.after_wake(&mut next);
            continue;
        }
        match s.poll() {
            Outcome::Stop => return,
            Outcome::Stale => continue,
            Outcome::Err => next = Instant::now() + s.fail_delay(),
            Outcome::Ok => next = Instant::now() + s.success_delay(),
        }
    }
}

/// Who we are polling as. A bridge session can reach everything; a bare
/// `df.user_id` only reaches the public record.
enum Identity {
    Session(Box<crate::net::creds::Credentials>),
    PublicId(String),
}

pub struct PlayerPoller {
    client: Arc<Mutex<Client>>,
    creds: Arc<Creds>,
    store: Arc<Store>,
    cfg: Arc<Mutex<Config>>,
    gate: Arc<Gate>,
    stop: Arc<AtomicBool>,
    shutdown: Arc<Notify>,
    wake: Arc<Notify>,
    min_gap: Mutex<Duration>,
    game_running: Arc<AtomicBool>,
    session_stale: Arc<AtomicBool>,
    status: Mutex<PollerStatus>,
    last_poll: Mutex<Option<Instant>>,
    on_tick: Mutex<Option<TickHandler>>,
}

impl PlayerPoller {
    pub fn new(client: Arc<Mutex<Client>>, runtime: PollerRuntime) -> Arc<Self> {
        Arc::new(Self {
            client,
            creds: runtime.creds,
            store: runtime.store,
            cfg: runtime.cfg,
            gate: runtime.gate,
            stop: runtime.stop,
            shutdown: runtime.shutdown,
            wake: Arc::new(Notify::new()),
            min_gap: Mutex::new(MIN_REQUEST_GAP),
            game_running: runtime.game_running,
            session_stale: runtime.session_stale,
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

    pub fn replace_client(&self, client: Client) {
        *self.client.lock().unwrap() = client;
        self.wake();
    }

    pub fn resume(&self) {
        {
            let mut st = self.status.lock().unwrap();
            if st.stale || self.session_stale.load(Ordering::SeqCst) {
                eprintln!("poller: credentials refreshed, resuming");
            }
            st.stale = false;
            st.failures = 0;
        }
        self.session_stale.store(false, Ordering::SeqCst);
        self.wake();
    }

    pub fn status(&self) -> PollerStatus {
        self.status.lock().unwrap().clone()
    }

    /// The configured account id, when it is the only identity we have. Real
    /// bridge credentials always win, so this goes quiet the moment they land.
    fn public_only_id(&self) -> Option<String> {
        if self.creds.get().is_some() {
            return None;
        }
        let id = self.cfg.lock().unwrap().df.user_id.clone();
        (!id.is_empty()).then_some(id)
    }

    fn pause_reason(&self) -> Option<String> {
        if self.creds.get().is_none() && self.public_only_id().is_none() {
            return Some("waiting for the browser bridge to deliver a session".into());
        }
        {
            let mut st = self.status.lock().unwrap();
            if self.session_stale.load(Ordering::SeqCst) {
                st.stale = true;
            }
            if st.stale {
                return Some(
                    "credentials were rejected; open any Dead Frontier page to refresh them".into(),
                );
            }
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
        exponential_backoff(self.interval(), n, cap, jitter)
    }

    pub fn run(self: Arc<Self>) {
        run_loop(&*self);
    }

    pub fn poll_once(&self, scheduled: bool) -> Tick {
        let identity = match self.creds.get() {
            Some((cr, _)) => Identity::Session(Box::new(cr)),
            None => match self.public_only_id() {
                Some(id) => Identity::PublicId(id),
                None => {
                    return Tick {
                        at: Utc::now(),
                        vars: Default::default(),
                        err: Some("no credentials".into()),
                        scheduled,
                    }
                }
            },
        };
        {
            let mut st = self.status.lock().unwrap();
            st.last_attempt = Some(Utc::now());
            st.total_polls += 1;
        }
        *self.last_poll.lock().unwrap() = Some(Instant::now());
        if self.gate.wait(&self.stop, &self.shutdown).is_err() {
            return Tick {
                at: Utc::now(),
                vars: Default::default(),
                err: Some(Cancelled.to_string()),
                scheduled,
            };
        }
        let result = {
            let client = self.client.lock().unwrap();
            match &identity {
                Identity::Session(cr) => client.get_values(&cr.to_df()),
                Identity::PublicId(id) => client.get_values_public(id),
            }
        };
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
                    self.session_stale.store(true, Ordering::SeqCst);
                    st.last_error.clone_from(err);
                    st.total_failure += 1;
                    eprintln!(
                        "poller: the server rejected our credentials - polling STOPPED. \
                         Open any Dead Frontier page with the bridge userscript installed and it will resume by itself."
                    );
                }
                Some(err) => {
                    st.failures += 1;
                    st.last_error.clone_from(err);
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

impl Schedule for PlayerPoller {
    fn stop(&self) -> &AtomicBool {
        &self.stop
    }

    fn wake(&self) -> &Notify {
        &self.wake
    }

    fn current_pause(&self) -> Option<String> {
        self.pause_reason()
    }

    fn on_pause(&self, reason: &str, entered: bool) {
        {
            let mut st = self.status.lock().unwrap();
            st.paused = true;
            st.pause_reason = reason.to_string();
            st.next_attempt = None;
        }
        self.store.set_poller_status(self.status());
        if entered {
            eprintln!("poller: paused - {reason}");
        }
    }

    fn on_resume(&self) {
        eprintln!("poller: resumed");
        let mut st = self.status.lock().unwrap();
        st.paused = false;
        st.pause_reason.clear();
    }

    fn apply_floor(&self, next: &mut Instant) {
        if let Some(last) = *self.last_poll.lock().unwrap() {
            let floor = last + *self.min_gap.lock().unwrap();
            if *next < floor {
                *next = floor;
            }
        }
    }

    fn before_wait(&self, next: Instant) {
        {
            let mut st = self.status.lock().unwrap();
            st.next_attempt = Some(
                Utc::now()
                    + chrono::Duration::from_std(next.saturating_duration_since(Instant::now()))
                        .unwrap_or_default(),
            );
        }
        self.store.set_poller_status(self.status());
    }

    fn after_wake(&self, next: &mut Instant) {
        *next = match *self.last_poll.lock().unwrap() {
            Some(last) => last + *self.min_gap.lock().unwrap(),
            None => Instant::now(),
        };
    }

    fn poll(&self) -> Outcome {
        let tick = self.poll_once(true);
        if self.stop.load(Ordering::SeqCst) {
            return Outcome::Stop;
        }
        if tick
            .err
            .as_deref()
            .is_some_and(|e| e.contains("credentials rejected"))
        {
            Outcome::Stale
        } else if tick.err.is_some() {
            Outcome::Err
        } else {
            Outcome::Ok
        }
    }

    fn success_delay(&self) -> Duration {
        let jitter = self.cfg.lock().unwrap().poll.jitter;
        jittered(self.interval(), jitter)
    }

    fn fail_delay(&self) -> Duration {
        self.backoff(self.status().failures)
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
    shutdown: Arc<Notify>,
    wake: Arc<Notify>,
    game_running: Arc<AtomicBool>,
    session_stale: Arc<AtomicBool>,
    stale: AtomicBool,
    failures: Mutex<i32>,
}

impl ChallengePoller {
    pub fn new(
        client: Arc<Mutex<Client>>,
        persist: Arc<state::Store>,
        runtime: PollerRuntime,
    ) -> Arc<Self> {
        Arc::new(Self {
            client,
            creds: runtime.creds,
            store: runtime.store,
            persist,
            cfg: runtime.cfg,
            gate: runtime.gate,
            stop: runtime.stop,
            shutdown: runtime.shutdown,
            wake: Arc::new(Notify::new()),
            game_running: runtime.game_running,
            session_stale: runtime.session_stale,
            stale: AtomicBool::new(false),
            failures: Mutex::new(0),
        })
    }

    pub fn wake(&self) {
        self.wake.ping();
    }

    pub fn replace_client(&self, client: Client) {
        *self.client.lock().unwrap() = client;
        self.wake();
    }

    pub fn resume(&self) {
        self.stale.store(false, Ordering::SeqCst);
        self.session_stale.store(false, Ordering::SeqCst);
        *self.failures.lock().unwrap() = 0;
        self.wake();
    }

    #[cfg(test)]
    pub fn pause_reason_for_test(&self) -> Option<String> {
        self.pause_reason()
    }

    #[cfg(test)]
    pub fn enter_pause_for_test(&self) -> Option<String> {
        let reason = self.pause_reason()?;
        self.on_pause(&reason, true);
        Some(reason)
    }

    fn pause_reason(&self) -> Option<String> {
        let cfg = self.cfg.lock().unwrap().clone();
        if !cfg.widget.challenges.enabled {
            return Some("the challenge widget is disabled".into());
        }
        let Some((cr, salt)) = self.creds.get() else {
            return Some(NEED_SCRIPT.into());
        };
        if self.session_stale.load(Ordering::SeqCst) || self.stale.load(Ordering::SeqCst) {
            return Some(SESSION_EXPIRED.into());
        }
        if cfg.signing_salt(|| salt.clone()).is_empty() {
            return Some(SCRIPT_TOO_OLD.into());
        }
        if cr.cookie.is_empty() {
            return Some(NEED_COOKIE.into());
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
        run_loop(&*self);
    }

    fn poll_once(&self) -> Result<(), String> {
        let Some((cr, salt_stored)) = self.creds.get() else {
            return Err("no credentials".into());
        };
        let cfg = self.cfg.lock().unwrap().clone();
        let salt = cfg.signing_salt(|| salt_stored.clone());
        self.gate
            .wait(&self.stop, &self.shutdown)
            .map_err(|e| e.to_string())?;
        let vars = {
            let mut client = self.client.lock().unwrap();
            client.cookie.clone_from(&cr.cookie);
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
                    .map_or((0, false), |s| (s.level, s.gold_member));
                let board = challenges::parse(&vars, level, gold);
                let board = self.persist.remember_challenge_board(board);
                self.store.set_challenges(board);
                self.store.set_challenge_status(String::new());
                Ok(())
            }
            Err(err) => {
                if err.contains("credentials rejected") {
                    self.stale.store(true, Ordering::SeqCst);
                    self.session_stale.store(true, Ordering::SeqCst);
                } else {
                    *self.failures.lock().unwrap() += 1;
                    self.store.set_challenge_status(BOARD_RETRY.into());
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

impl Schedule for ChallengePoller {
    fn stop(&self) -> &AtomicBool {
        &self.stop
    }

    fn wake(&self) -> &Notify {
        &self.wake
    }

    fn current_pause(&self) -> Option<String> {
        self.pause_reason()
    }

    fn on_pause(&self, reason: &str, entered: bool) {
        if !entered {
            return;
        }
        eprintln!("challenges: paused - {reason}");
        self.store.set_challenge_status(reason.to_string());
        if !self.cfg.lock().unwrap().widget.challenges.enabled {
            self.store.clear_challenges();
        }
    }

    fn on_resume(&self) {
        eprintln!("challenges: resumed");
        self.store.set_challenge_status(String::new());
    }

    fn apply_floor(&self, next: &mut Instant) {
        if let Some(floor) = self.gate.reserved() {
            if *next < floor {
                *next = floor;
            }
        }
    }

    fn before_wait(&self, _next: Instant) {}

    fn after_wake(&self, next: &mut Instant) {
        if let Some(floor) = self.gate.reserved() {
            *next = floor;
        }
    }

    fn poll(&self) -> Outcome {
        match self.poll_once() {
            Err(err) if err.contains("credentials rejected") => Outcome::Stale,
            Err(_) => Outcome::Err,
            Ok(()) => Outcome::Ok,
        }
    }

    fn success_delay(&self) -> Duration {
        let cfg = self.cfg.lock().unwrap();
        let running = self.game_running.load(Ordering::SeqCst);
        jittered(
            cfg.poll.effective_challenge_interval(running),
            cfg.poll.jitter,
        )
    }

    fn fail_delay(&self) -> Duration {
        let n = *self.failures.lock().unwrap();
        let cfg = self.cfg.lock().unwrap();
        exponential_backoff(
            cfg.poll.effective_challenge_interval(true),
            n,
            cfg.poll.backoff_max.0,
            cfg.poll.jitter,
        )
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
    use std::collections::HashMap;
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

    fn test_poller(base: &str) -> (Arc<PlayerPoller>, Arc<AtomicBool>, Arc<Notify>) {
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
        let shutdown = Arc::new(Notify::new());
        let running = Arc::new(AtomicBool::new(true));
        let p = PlayerPoller::new(
            Arc::new(Mutex::new(client)),
            PollerRuntime {
                creds,
                store: Arc::new(Store::new(None)),
                cfg: Arc::new(Mutex::new(cfg)),
                gate: Arc::new(Gate::new(Duration::from_millis(5))),
                stop: stop.clone(),
                shutdown: shutdown.clone(),
                game_running: running,
                session_stale: Arc::new(AtomicBool::new(false)),
            },
        );
        p.set_min_gap(Duration::from_millis(20));
        (p, stop, shutdown)
    }

    /// What the unauthenticated `get_values.php?userID=` reply looks like. The
    /// public endpoint carries `id_member`, which is how a real record is told
    /// apart from a reply for an account that does not exist.
    fn public_record() -> &'static str {
        "&id_member=1234567&df_level=415&df_exp=10000&df_positionx=1100&df_positiony=1100&df_tradezone=5&df_inoutpost=0"
    }

    /// No bridge session, only `df.user_id`. The public endpoint is left
    /// enabled because it is the only way out.
    fn public_only_poller(base: &str, user_id: &str) -> Arc<PlayerPoller> {
        let mut cfg = Config::default();
        cfg.poll.only_when_game_running = false;
        cfg.df.timeout = crate::config::Duration(Duration::from_secs(2));
        cfg.df.user_id = user_id.into();
        let agent = ureq::AgentBuilder::new().timeout(cfg.df.timeout.0).build();
        let p = PlayerPoller::new(
            Arc::new(Mutex::new(Client::with_agent(agent, base, "df-hud-test"))),
            PollerRuntime {
                creds: Arc::new(Creds::new("")),
                store: Arc::new(Store::new(None)),
                cfg: Arc::new(Mutex::new(cfg)),
                gate: Arc::new(Gate::new(Duration::from_millis(5))),
                stop: Arc::new(AtomicBool::new(false)),
                shutdown: Arc::new(Notify::new()),
                game_running: Arc::new(AtomicBool::new(true)),
                session_stale: Arc::new(AtomicBool::new(false)),
            },
        );
        p.set_min_gap(Duration::from_millis(0));
        p
    }

    #[test]
    fn configured_user_id_polls_without_a_session() {
        let (base, hits) = spawn_df(|_| (200, public_record().into()));
        let p = public_only_poller(&base, "1234567");
        assert!(p.pause_reason().is_none(), "a user id is enough to poll");
        let tick = p.poll_once(false);
        assert!(tick.err.is_none(), "{:?}", tick.err);
        assert_eq!(tick.vars.get("df_level").map(String::as_str), Some("415"));
        assert_eq!(*hits.lock().unwrap(), 1);
    }

    #[test]
    fn a_session_wins_over_a_configured_user_id() {
        let (p, _stop, _) = test_poller("http://127.0.0.1:1");
        p.cfg.lock().unwrap().df.user_id = "7654321".into();
        assert!(
            p.public_only_id().is_none(),
            "bridge credentials must not be displaced by df.user_id"
        );
    }

    #[test]
    fn without_either_identity_the_poller_waits_for_the_bridge() {
        let p = public_only_poller("http://127.0.0.1:1", "");
        assert!(p.public_only_id().is_none());
        let reason = p.pause_reason().expect("paused");
        assert!(reason.contains("bridge"), "{reason}");
    }

    #[test]
    fn a_configured_user_id_with_no_record_is_an_error() {
        let (base, _) = spawn_df(|_| (200, "&df_level=&df_exp=".into()));
        let p = public_only_poller(&base, "1234567");
        let err = p.poll_once(false).err.expect("empty record must not pass");
        assert!(err.contains("df.user_id"), "{err}");
    }

    #[test]
    fn polls_and_reports_ticks() {
        let (base, hits) = spawn_df(|_| (200, player_record().into()));
        let (p, stop, shutdown) = test_poller(&base);
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
        shutdown.ping();
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
        let (p, _stop, _) = test_poller(&base);
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

    #[test]
    fn player_client_can_be_replaced_after_reload() {
        let (base, hits) = spawn_df(|_| (200, player_record().into()));
        let (p, _stop, _) = test_poller("http://127.0.0.1:1");
        let client = Client::new(&base, "df-hud-reloaded");
        client.disable_public_get_values();
        p.replace_client(client);
        let tick = p.poll_once(false);
        assert!(tick.err.is_none(), "{:?}", tick.err);
        assert_eq!(*hits.lock().unwrap(), 1);
    }

    fn test_challenge_poller(
        session_stale: Arc<AtomicBool>,
    ) -> (Arc<ChallengePoller>, Arc<Store>, Arc<Mutex<Config>>) {
        let mut cfg = Config::default();
        cfg.widget.challenges.enabled = true;
        cfg.poll.only_when_game_running = false;
        let creds = Arc::new(Creds::new(""));
        creds
            .set(
                Credentials {
                    user_id: "1234567".into(),
                    password: "hash".into(),
                    sc: "sc".into(),
                    cookie: "session=1".into(),
                },
                "salt",
            )
            .unwrap();
        let store = Arc::new(Store::new(None));
        store.apply_tick(Tick {
            at: Utc::now(),
            vars: HashMap::from([("df_level".into(), "415".into())]),
            err: None,
            scheduled: false,
        });
        let agent = ureq::AgentBuilder::new()
            .timeout(Duration::from_secs(2))
            .build();
        let cfg = Arc::new(Mutex::new(cfg));
        let p = ChallengePoller::new(
            Arc::new(Mutex::new(Client::with_agent(
                agent,
                "http://127.0.0.1:1",
                "df-hud-test",
            ))),
            Arc::new(state::Store::new("")),
            PollerRuntime {
                creds,
                store: store.clone(),
                cfg: cfg.clone(),
                gate: Arc::new(Gate::new(Duration::from_millis(5))),
                stop: Arc::new(AtomicBool::new(false)),
                shutdown: Arc::new(Notify::new()),
                game_running: Arc::new(AtomicBool::new(true)),
                session_stale,
            },
        );
        (p, store, cfg)
    }

    /// The board prints one of these on a single line that clips. Keeping them
    /// short is the whole reason they are named constants.
    #[test]
    fn challenge_reasons_fit_on_one_line() {
        for reason in [
            NEED_SCRIPT,
            SESSION_EXPIRED,
            SCRIPT_TOO_OLD,
            NEED_COOKIE,
            BOARD_RETRY,
        ] {
            let rendered = format!("challenges: {reason}");
            assert!(
                rendered.len() <= 64,
                "{} chars will clip: {rendered}",
                rendered.len()
            );
            assert!(
                !reason.contains("browser bridge") && !reason.contains("signing salt"),
                "say it the way docs/install.md does: {reason}"
            );
        }
    }

    #[test]
    fn challenge_pauses_when_player_session_is_stale() {
        let stale = Arc::new(AtomicBool::new(true));
        let (p, _, _) = test_challenge_poller(stale);
        let reason = p.pause_reason_for_test().expect("paused");
        assert!(reason.contains("session expired"), "{reason}");
    }

    #[test]
    fn challenge_clears_board_when_widget_disabled() {
        let (p, store, cfg) = test_challenge_poller(Arc::new(AtomicBool::new(false)));
        store.set_challenges(vec![crate::model::Challenge {
            name: "Travel".into(),
            ..crate::model::Challenge::default()
        }]);
        cfg.lock().unwrap().widget.challenges.enabled = false;
        let reason = p.enter_pause_for_test().expect("paused");
        assert!(reason.contains("disabled"), "{reason}");
        assert!(store.derive(Utc::now()).challenges.is_none());
    }

    #[test]
    fn challenge_client_can_be_replaced_after_reload() {
        let (base, hits) = spawn_df(|_| (200, "&df_challengename_1=Travel".into()));
        let (p, _, _) = test_challenge_poller(Arc::new(AtomicBool::new(false)));
        p.replace_client(Client::new(&base, "df-hud-reloaded"));
        assert!(p.poll_once().is_ok());
        assert_eq!(*hits.lock().unwrap(), 1);
    }
}
