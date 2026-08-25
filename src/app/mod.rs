//! Composition root: catalog, creds, pollers, bridge, presence, overlay handle.

pub mod autostart;
pub mod groups;
pub mod hotkeys;
pub mod poller;
pub mod rategate;
pub mod state;
pub mod store;
pub mod tray;
pub mod visibility;

use chrono::{DateTime, Utc};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::Duration;

use crate::config::Config;
use crate::data::{bossmap, catalog, xp};
use crate::game::{self, desktop, presence};
use crate::model::{RunState, Tick, XpSample, XpSource};
use crate::net::bridge::{self, Hooks};
use crate::net::creds::Store as Creds;
use crate::net::dfclient::Client;
use crate::wake::{Notify, Wake};

use groups::Groups;
use poller::{ChallengePoller, MIN_REQUEST_GAP, PlayerPoller, PollerRuntime};
use rategate::{Gate, sleep_cancellable};
use store::Store;

#[cfg(target_os = "linux")]
static HUP: AtomicBool = AtomicBool::new(false);

pub struct Handle {
    pub store: Arc<Store>,
    pub cfg: Arc<Mutex<Config>>,
    pub groups: Arc<Groups>,
    pub overlay_on: Arc<AtomicBool>,
    pub game_running: Arc<AtomicBool>,
    pub visible: Arc<AtomicBool>,
    pub wake: Arc<Wake>,
    pub ui: Arc<Notify>,
    shutdown: Arc<Notify>,
    bossmap_wake: Arc<Notify>,
    pub creds: Arc<Creds>,
    pub game: Arc<game::Watcher>,
    pub vis: Arc<visibility::Watcher>,
    active_address: Mutex<Option<String>>,
    persist: Arc<state::Store>,
    stop: Arc<AtomicBool>,
    player: Arc<PlayerPoller>,
    challenges: Arc<ChallengePoller>,
    last_run_start: Mutex<Option<DateTime<Utc>>>,
    presence: Option<Arc<presence::Control>>,
    pub gamekeys: Arc<crate::game::gamekeys::Keys>,
    config_err: Mutex<Option<String>>,
}

impl Handle {
    pub fn ping(&self) {
        self.wake_ui();
        self.player.wake();
        self.challenges.wake();
    }

    /// Player record, challenge board, and city map. Not the allstats catalog
    /// (that is a 24h table and already on disk after startup).
    fn refresh_feeds(&self) {
        self.ping();
        self.bossmap_wake.ping();
    }

    fn wake_ui(&self) {
        self.wake.ping();
        self.ui.ping();
    }

    pub fn resume_pollers(&self) {
        self.player.resume();
        self.challenges.resume();
        if let Some((_, _)) = self.creds.get()
            && let Some(at) = self.creds.updated_at()
        {
            self.store.set_credentials_at(at);
        }
        self.ping();
    }

    pub fn toggle_overlay(&self) -> bool {
        let next = !self.overlay_on.load(Ordering::SeqCst);
        self.overlay_on.store(next, Ordering::SeqCst);
        self.vis.set_enabled(next);
        self.wake_ui();
        next
    }

    pub fn request_stop(&self) {
        self.persist_run();
        if let Err(err) = self.persist.save() {
            eprintln!("state: could not save: {err}");
        }
        self.signal_stop();
    }

    fn signal_stop(&self) {
        self.stop.store(true, Ordering::SeqCst);
        self.shutdown.ping();
        self.game.poke();
        self.vis.poke();
        self.player.wake();
        self.challenges.wake();
        self.bossmap_wake.ping();
        self.wake_ui();
    }

    pub fn stopped(&self) -> bool {
        self.stop.load(Ordering::SeqCst)
    }

    pub fn toggle_group(&self, name: &str) -> Result<bool, String> {
        let hidden = self.groups.toggle(name)?;
        self.wake_ui();
        Ok(hidden)
    }

    pub fn restart_run(&self) {
        self.store.restart_run(Utc::now());
        self.wake_ui();
    }

    fn persist_run(&self) {
        persist_run(&self.store, &self.persist);
    }

    pub fn reset_xp(&self) {
        self.persist.reset_xp_window("reset by hand");
        self.wake_ui();
    }

    pub fn config_error(&self) -> Option<String> {
        self.config_err.lock().unwrap().clone()
    }

    pub fn set_config_error(&self, err: impl Into<String>) {
        *self.config_err.lock().unwrap() = Some(err.into());
    }

    pub fn clear_config_error(&self) {
        *self.config_err.lock().unwrap() = None;
    }

    pub fn note_config_watch(&self, watch: &crate::config::Watch, reloaded: bool) {
        if reloaded {
            self.clear_config_error();
        } else if let Some(err) = watch.error() {
            self.set_config_error(err);
        }
    }

    pub fn replace_config(&self, mut cfg: Config) -> Config {
        self.clear_config_error();
        let (ignored, rebuild_clients, reconfigure_game) = {
            let running = self.cfg.lock().unwrap();
            let ignored = cfg.reloadable_from(&running);
            let rebuild_clients = cfg.df.base_url != running.df.base_url
                || cfg.df.user_agent != running.df.user_agent
                || cfg.df.timeout != running.df.timeout;
            let reconfigure_game = cfg.game.process != running.game.process
                || cfg.game.scan_interval != running.game.scan_interval;
            (ignored, rebuild_clients, reconfigure_game)
        };
        if !ignored.is_empty() {
            eprintln!(
                "config: {} need a restart; running values kept",
                ignored.join(", ")
            );
        }
        if !cfg.bossmap.enabled {
            self.store.clear_boss_map();
        }
        if !cfg.widget.challenges.enabled {
            self.store.clear_challenges();
        }
        self.gamekeys.apply_config(&cfg.game_keys);
        self.store.set_xp_min_samples(cfg.widget.xp.min_samples);
        self.store
            .set_public_id_configured(!cfg.df.user_id.is_empty());
        *self.cfg.lock().unwrap() = cfg.clone();
        if rebuild_clients {
            self.player.replace_client(df_client(&cfg));
            self.challenges.replace_client(df_client(&cfg));
        }
        if reconfigure_game {
            self.game
                .reconfigure(&cfg.game.process, cfg.game.scan_interval.0);
        }
        self.game.poke();
        self.vis.poke();
        if cfg.bossmap.enabled {
            self.refresh_feeds();
        } else {
            self.ping();
        }
        cfg
    }

    pub fn set_fps_display(&self, on: bool) {
        if persist_tray(self, crate::config::TrayOption::FpsDisplay, on) {
            self.gamekeys.set_fps_display(on);
            self.cfg.lock().unwrap().game_keys.fps_display = on;
            self.wake_ui();
        }
    }

    pub fn set_dismiss_launcher(&self, on: bool) {
        if persist_tray(self, crate::config::TrayOption::DismissLauncher, on) {
            self.gamekeys.set_dismiss_launcher(on);
            self.cfg.lock().unwrap().game_keys.dismiss_launcher = on;
            self.wake_ui();
        }
    }

    pub fn has_presence(&self) -> bool {
        self.presence.is_some()
    }

    pub fn presence_bind_failed(&self) -> bool {
        self.presence.as_ref().is_some_and(|p| p.bind_failed())
    }

    pub fn presence_client_connected(&self) -> bool {
        self.store.presence_connected()
    }

    pub fn retry_presence(&self) -> bool {
        self.presence.as_ref().is_some_and(|p| p.retry())
    }

    pub fn active_address(&self) -> Option<String> {
        self.active_address.lock().unwrap().clone()
    }

    fn refresh_active_address(&self) {
        *self.active_address.lock().unwrap() = desktop::new_client().active_address();
    }

    pub fn config_file_path(&self) -> std::path::PathBuf {
        self.cfg
            .lock()
            .unwrap()
            .source_path()
            .map_or_else(crate::config::default_path, std::path::Path::to_path_buf)
    }

    pub fn reload_config(&self) {
        let path = self.config_file_path();
        match Config::load(&path) {
            Ok(cfg) => {
                eprintln!("reloaded {}", path.display());
                self.clear_config_error();
                self.replace_config(cfg);
            }
            Err(err) => {
                eprintln!("config reload failed: {err}");
                self.set_config_error(err.to_string());
            }
        }
    }

    pub fn open_config(&self) {
        let path = self.config_file_path();
        if let Err(err) = crate::config::write_defaults_if_missing(&path) {
            eprintln!("tray: could not write config file: {err}");
        }
        if let Err(err) = autostart::open_file(&path) {
            eprintln!("tray: could not open config file: {err}");
        }
    }

    pub fn open_log(&self) {
        let path = crate::config::default_log_path();
        if let Some(dir) = path.parent()
            && let Err(err) = std::fs::create_dir_all(dir)
        {
            eprintln!("tray: could not create log directory: {err}");
            return;
        }
        if !path.exists()
            && let Err(err) = std::fs::OpenOptions::new()
                .create(true)
                .append(true)
                .open(&path)
        {
            eprintln!("tray: could not create log file: {err}");
            return;
        }
        if let Err(err) = autostart::open_file(&path) {
            eprintln!("tray: could not open log file: {err}");
        }
    }

    pub fn start_on_login(&self) -> bool {
        autostart::enabled().unwrap_or(false)
    }

    pub fn set_start_on_login(&self, on: bool) {
        match autostart::set_enabled(on) {
            Ok(()) => {
                if cfg!(windows) {
                    eprintln!("tray: Windows startup enabled: {on}");
                }
            }
            Err(err) => eprintln!("tray: could not update Windows startup: {err}"),
        }
    }
}

impl Drop for Handle {
    fn drop(&mut self) {
        self.persist_run();
        if let Err(err) = self.persist.save() {
            eprintln!("state: could not save: {err}");
        }
        self.signal_stop();
    }
}

#[derive(Clone, Copy, Debug, Default)]
pub struct PrintOpts {
    pub hud: bool,
}

pub fn load_creds_and_catalog(
    cfg: &Config,
) -> Result<(Arc<Creds>, Option<catalog::Catalog>), Box<dyn std::error::Error>> {
    cfg.ensure_data_dir()?;
    let creds = Arc::new(Creds::new(cfg.credentials_path()));
    if let Err(err) = creds.load() {
        eprintln!("credentials: {err}");
    }
    let catalog = match catalog::ensure(
        &cfg.catalog_path(),
        &cfg.df.allstats_url,
        &cfg.df.user_agent,
        cfg.poll.catalog_interval.0,
        cfg.df.timeout.0,
        Utc::now(),
    ) {
        Ok(c) => Some(c),
        Err(err) => {
            eprintln!("catalog: unavailable ({err}); level thresholds will be missing");
            None
        }
    };
    Ok((creds, catalog))
}

pub fn df_client(cfg: &Config) -> Client {
    Client::with_agent(
        ureq::Agent::new_with_config(
            ureq::Agent::config_builder()
                .timeout_global(Some(cfg.df.timeout.0))
                .user_agent(cfg.df.user_agent.clone())
                .http_status_as_error(false)
                .build(),
        ),
        &cfg.df.base_url,
        &cfg.df.user_agent,
    )
}

pub fn start_with(
    cfg: Config,
    print: PrintOpts,
) -> Result<Arc<Handle>, Box<dyn std::error::Error>> {
    if let Err(err) = autostart::reconcile() {
        eprintln!("startup: could not refresh login launch entry: {err}");
    }
    let (creds, catalog) = load_creds_and_catalog(&cfg)?;
    if let Some(c) = &catalog {
        eprintln!("catalog: {}", c.summary());
    }
    let store = Arc::new(Store::new(catalog));
    store.set_public_id_configured(!cfg.df.user_id.is_empty());
    if let Some(at) = creds.updated_at() {
        store.set_credentials_at(at);
    }
    let persist = Arc::new(state::Store::new(cfg.state_path()));
    if let Err(err) = persist.load() {
        eprintln!("state: {err}");
    }
    let mut last_run_start = None;
    if let Some(run) = persist.get().run {
        last_run_start = Some(run.started_at);
        store.set_run_seed(Some(run));
    }
    store.set_xp_window(
        {
            let persist = persist.clone();
            move || persist.get().xp_samples
        },
        cfg.widget.xp.min_samples,
    );
    {
        let persist = persist.clone();
        let hud = store.clone();
        store.set_on_run_change(move || persist_run(&hud, &persist));
    }

    let player_client = Arc::new(Mutex::new(df_client(&cfg)));
    let challenge_client = Arc::new(Mutex::new(df_client(&cfg)));
    let session_stale = Arc::new(AtomicBool::new(false));
    let cfg = Arc::new(Mutex::new(cfg));
    let stop = Arc::new(AtomicBool::new(false));
    let game_running = Arc::new(AtomicBool::new(false));
    let visible = Arc::new(AtomicBool::new(true));
    let gate = Arc::new(Gate::new(MIN_REQUEST_GAP));
    let groups = Arc::new(Groups::new());
    let overlay_on = Arc::new(AtomicBool::new(true));
    let wake = Arc::new(Wake::new()?);
    let ui = Arc::new(Notify::new());
    let shutdown = Arc::new(Notify::new());
    let bossmap_wake = Arc::new(Notify::new());
    let presence = if cfg.lock().unwrap().presence.enabled {
        Some(presence::Control::new())
    } else {
        None
    };
    let gamekeys = crate::game::gamekeys::Keys::new(&cfg.lock().unwrap().game_keys);

    let (process, scan) = {
        let c = cfg.lock().unwrap();
        let process = if c.game.process.trim().is_empty() {
            game::DEFAULT_PROCESS.to_string()
        } else {
            c.game.process.clone()
        };
        (process, c.game.scan_interval.0)
    };
    let game = game::Watcher::new(&process, scan);
    let desktop_client = desktop::new_client();
    let active_address = desktop_client.active_address();
    let query = Some(Arc::new(desktop_client) as Arc<dyn visibility::Querier>);
    let vis = visibility::Watcher::new(game.clone(), cfg.clone(), query);

    let poller_runtime = PollerRuntime {
        creds: creds.clone(),
        store: store.clone(),
        cfg: cfg.clone(),
        gate: gate.clone(),
        stop: stop.clone(),
        shutdown: shutdown.clone(),
        game_running: game_running.clone(),
        session_stale,
    };
    let player = PlayerPoller::new(player_client, poller_runtime.clone());
    let challenges = ChallengePoller::new(challenge_client, persist.clone(), poller_runtime);

    let handle = Arc::new(Handle {
        store: store.clone(),
        cfg: cfg.clone(),
        groups: groups.clone(),
        overlay_on: overlay_on.clone(),
        game_running: game_running.clone(),
        visible: visible.clone(),
        wake: wake.clone(),
        ui: ui.clone(),
        shutdown: shutdown.clone(),
        bossmap_wake: bossmap_wake.clone(),
        creds: creds.clone(),
        game: game.clone(),
        vis: vis.clone(),
        active_address: Mutex::new(active_address),
        persist: persist.clone(),
        stop: stop.clone(),
        player: player.clone(),
        challenges: challenges.clone(),
        last_run_start: Mutex::new(last_run_start),
        presence: presence.clone(),
        gamekeys: gamekeys.clone(),
        config_err: Mutex::new(None),
    });

    {
        let handle = handle.clone();
        player.set_on_tick(move |tick: Tick| {
            let xp_window = {
                let cfg = handle.cfg.lock().unwrap();
                cfg.widget.xp.effective_window(cfg.poll.active_interval.0)
            };
            if ingest_player_tick(
                &handle.store,
                &handle.persist,
                xp_window,
                &handle.last_run_start,
                tick,
            ) {
                handle.challenges.wake();
            }
            handle.wake_ui();
        });
    }

    {
        let handle = handle.clone();
        let game = handle.game.clone();
        game.set_on_change(move |st| {
            handle.store.set_game(st);
            handle.game_running.store(st.running, Ordering::SeqCst);
            handle.vis.poke();
            if st.running {
                handle.refresh_feeds();
            } else {
                handle.ping();
            }
        });
    }
    {
        let handle = handle.clone();
        let vis = handle.vis.clone();
        vis.set_on_change(move |v| {
            handle.store.set_visibility(v.clone());
            handle.visible.store(v.visible, Ordering::SeqCst);
            handle.wake_ui();
        });
    }
    handle.vis.refresh();
    {
        let v = handle.vis.state();
        handle.store.set_visibility(v.clone());
        handle.visible.store(v.visible, Ordering::SeqCst);
    }

    game::spawn(handle.game.clone(), stop.clone());
    visibility::spawn(handle.vis.clone(), stop.clone());
    poller::spawn("df-hud-desktop", stop.clone(), {
        let handle = handle.clone();
        let stop = stop.clone();
        move || {
            let game = handle.game.clone();
            let vis = handle.vis.clone();
            let focus = handle.clone();
            desktop::watch_events(
                stop,
                move || game.poke(),
                move || vis.poke(),
                move || focus.refresh_active_address(),
            );
        }
    });
    tray::spawn(handle.clone(), stop.clone());
    hotkeys::spawn(handle.clone(), stop.clone());
    crate::game::gamekeys::spawn(handle.clone(), stop.clone());

    poller::spawn("df-hud-poller", stop.clone(), {
        let player = player.clone();
        move || player.run()
    });
    poller::spawn("df-hud-challenges", stop.clone(), {
        let challenges = challenges.clone();
        move || challenges.run()
    });
    poller::spawn("df-hud-state", stop.clone(), {
        let persist = persist.clone();
        let stop = stop.clone();
        move || {
            while !stop.load(Ordering::SeqCst) {
                thread::sleep(Duration::from_secs(1));
                if let Err(err) = persist.maybe_save() {
                    eprintln!("state: could not save: {err}");
                }
            }
        }
    });
    poller::spawn("df-hud-catalog", stop.clone(), {
        let store = store.clone();
        let cfg = cfg.clone();
        let stop = stop.clone();
        let shutdown = shutdown.clone();
        let gate = gate.clone();
        move || catalog_loop(store, cfg, stop, shutdown, gate)
    });
    poller::spawn("df-hud-bossmap", stop.clone(), {
        let store = store.clone();
        let cfg = cfg.clone();
        let stop = stop.clone();
        let shutdown = shutdown.clone();
        let poke = bossmap_wake.clone();
        let wake = wake.clone();
        let gate = gate.clone();
        move || bossmap_loop(store, cfg, stop, shutdown, poke, wake, gate)
    });

    {
        let cfg_now = cfg.lock().unwrap().clone();
        if cfg_now.presence.enabled {
            let store = store.clone();
            let wake = wake.clone();
            let stop = stop.clone();
            let path = if cfg_now.presence.socket.is_empty() {
                presence::default_socket()
            } else {
                cfg_now.presence.socket.clone()
            };
            poller::spawn("df-hud-presence", stop.clone(), move || {
                let store_state = store.clone();
                let wake_state = wake.clone();
                let store_conn = store.clone();
                let wake_conn = wake.clone();
                let control = presence.expect("presence enabled");
                presence::serve(
                    &path,
                    move |state| {
                        store_state.set_presence(state);
                        wake_state.ping();
                    },
                    move |connected| {
                        store_conn.set_presence_connected(connected);
                        wake_conn.ping();
                    },
                    control,
                    stop,
                );
            });
        }
        if cfg_now.bridge.enabled {
            let creds = creds.clone();
            let handle = handle.clone();
            let listen = cfg_now.bridge.listen.clone();
            match bridge::start(
                &listen,
                creds,
                Hooks {
                    on_credentials: Some(Arc::new({
                        let handle = handle.clone();
                        move || handle.resume_pollers()
                    })),
                    run_start: Some(Arc::new({
                        let handle = handle.clone();
                        move || handle.restart_run()
                    })),
                    xp_reset: Some(Arc::new({
                        let handle = handle.clone();
                        move || handle.reset_xp()
                    })),
                    overlay_toggle: Some(Arc::new({
                        let handle = handle.clone();
                        move || {
                            let _ = handle.toggle_overlay();
                        }
                    })),
                    widget_toggle: Some(Arc::new({
                        let handle = handle.clone();
                        move |g| handle.toggle_group(g).map(|_| true)
                    })),
                },
            ) {
                Ok(_) => {}
                Err(err) => eprintln!("bridge: {err}"),
            }
        }
    }

    if print.hud {
        let handle = handle.clone();
        poller::spawn("df-hud-print", stop.clone(), move || {
            while !handle.stopped() {
                let view = handle.store.derive(Utc::now());
                let cfg = handle.cfg.lock().unwrap().clone();
                print!(
                    "{}",
                    crate::overlay::present::format_hud(&view, &cfg, &handle.groups)
                );
                std::thread::sleep(Duration::from_secs(1));
            }
        });
    }

    #[cfg(target_os = "linux")]
    catch_sighup(handle.clone(), stop.clone());

    Ok(handle)
}

#[cfg(target_os = "linux")]
extern "C" fn on_sighup(_: libc::c_int) {
    HUP.store(true, Ordering::SeqCst);
}

#[cfg(target_os = "linux")]
fn catch_sighup(handle: Arc<Handle>, stop: Arc<AtomicBool>) {
    unsafe {
        libc::signal(libc::SIGHUP, on_sighup as *const () as libc::sighandler_t);
    }
    poller::spawn("df-hud-hup", stop, move || {
        while !handle.stopped() {
            if HUP.swap(false, Ordering::SeqCst) {
                eprintln!("config: SIGHUP");
                handle.reload_config();
            }
            thread::sleep(Duration::from_millis(200));
        }
    });
}

fn ingest_player_tick(
    store: &Store,
    persist: &state::Store,
    xp_window: Duration,
    last_run_start: &Mutex<Option<DateTime<Utc>>>,
    tick: Tick,
) -> bool {
    let applied = store.apply_tick(tick);
    if applied {
        write_xp_sample(store, persist, xp_window, last_run_start);
    }
    applied
}

fn write_xp_sample(
    store: &Store,
    persist: &state::Store,
    xp_window: Duration,
    last_run_start: &Mutex<Option<DateTime<Utc>>>,
) {
    let Some(snap) = store.snapshot() else {
        return;
    };
    if snap.xp_source == XpSource::None || snap.cumulative_xp <= 0 {
        return;
    }
    let (run_start, _) = store.run();
    {
        let mut last = last_run_start.lock().unwrap();
        if xp::run_reset(*last, run_start) {
            *last = run_start;
            drop(last);
            persist.reset_xp_window("a new run started");
        }
    }
    if let Some(prev) = store.previous_snapshot()
        && let Some(reason) = xp::window_reset(&prev, &snap, xp_window)
    {
        persist.reset_xp_window(reason);
    }
    persist.append_xp_sample(
        XpSample {
            at: snap.at,
            cumulative: snap.cumulative_xp,
            source: snap.xp_source.as_str().to_string(),
        },
        xp_window,
    );
}

fn persist_run(store: &Store, persist: &state::Store) {
    let (started, game) = store.run();
    persist.update(|st| {
        if started.is_none() || !game.running {
            st.run = None;
            return;
        }
        st.run = Some(RunState {
            started_at: started.unwrap(),
            game_pid: game.pid,
            game_started_at: game.started_at,
        });
    });
}

fn persist_tray(handle: &Handle, option: crate::config::TrayOption, on: bool) -> bool {
    let path = handle
        .cfg
        .lock()
        .unwrap()
        .source_path()
        .map_or_else(crate::config::default_path, std::path::Path::to_path_buf);
    if let Err(err) = crate::config::set_tray_option(&path, option, on) {
        eprintln!("config: could not persist {option:?} from tray: {err}");
        return false;
    }
    true
}

fn catalog_loop(
    store: Arc<Store>,
    cfg: Arc<Mutex<Config>>,
    stop: Arc<AtomicBool>,
    shutdown: Arc<Notify>,
    gate: Arc<Gate>,
) {
    loop {
        if stop.load(Ordering::SeqCst) {
            return;
        }
        let c = cfg.lock().unwrap().clone();
        if gate.wait(&stop, &shutdown).is_err() {
            return;
        }
        match catalog::ensure(
            &c.catalog_path(),
            &c.df.allstats_url,
            &c.df.user_agent,
            c.poll.catalog_interval.0,
            c.df.timeout.0,
            Utc::now(),
        ) {
            Ok(cat) => store.set_catalog(cat),
            Err(err) => eprintln!("catalog: {err}"),
        }
        let wait = c.poll.catalog_interval.0.max(Duration::from_secs(60));
        if sleep_cancellable(wait, &stop, &shutdown).is_err() {
            return;
        }
    }
}

fn bossmap_loop(
    store: Arc<Store>,
    cfg: Arc<Mutex<Config>>,
    stop: Arc<AtomicBool>,
    shutdown: Arc<Notify>,
    poke: Arc<Notify>,
    wake: Arc<Wake>,
    gate: Arc<Gate>,
) {
    loop {
        if stop.load(Ordering::SeqCst) {
            return;
        }
        let c = cfg.lock().unwrap().clone();
        if !c.bossmap.enabled {
            store.clear_boss_map();
            wake.ping();
            poke.wait_timeout(Duration::from_secs(2));
            continue;
        }
        if gate.wait(&stop, &shutdown).is_err() {
            return;
        }
        match bossmap::fetch(&c.bossmap.url, &c.df.user_agent, c.df.timeout.0, Utc::now()) {
            Ok(m) => {
                store.set_boss_map(m);
                wake.ping();
            }
            Err(err) => eprintln!("bossmap: {err}"),
        }
        let onslaught = store
            .effective_position(Utc::now())
            .is_some_and(|(x, y)| x == bossmap::ONSLAUGHT_COORD && y == bossmap::ONSLAUGHT_COORD);
        let wait = if onslaught {
            c.bossmap.onslaught_interval.0
        } else {
            c.bossmap.interval.0
        };
        poke.wait_timeout(wait.max(Duration::from_secs(5)));
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::data::xp;
    use crate::model::XpStability;
    use std::collections::HashMap;

    fn xp_tick(at: DateTime<Utc>, total: i64, err: Option<String>) -> Tick {
        Tick {
            at,
            vars: HashMap::from([
                ("df_level".into(), "415".into()),
                ("df_exp".into(), "1000".into()),
                ("df_exptotal".into(), total.to_string()),
            ]),
            err,
            scheduled: true,
        }
    }

    #[test]
    fn failed_ticks_do_not_append_xp_samples() {
        let store = Store::new(None);
        let persist = state::Store::new("");
        let cfg = Config::default();
        let xp_window = cfg.widget.xp.effective_window(cfg.poll.active_interval.0);
        let last_run = Mutex::new(None);
        let start = Utc::now();

        assert!(ingest_player_tick(
            &store,
            &persist,
            xp_window,
            &last_run,
            xp_tick(start, 1_000_000, None),
        ));
        assert!(ingest_player_tick(
            &store,
            &persist,
            xp_window,
            &last_run,
            xp_tick(start + chrono::Duration::seconds(10), 1_001_000, None),
        ));
        let after_good = persist.get().xp_samples;
        assert_eq!(after_good.len(), 2);
        let rate_before = xp::compute_rate(&after_good, 3, XpStability::Steady);

        for i in 1..=3 {
            assert!(!ingest_player_tick(
                &store,
                &persist,
                xp_window,
                &last_run,
                xp_tick(
                    start + chrono::Duration::seconds(10 + i),
                    1_001_000,
                    Some("boom".into()),
                ),
            ));
        }

        let after_fail = persist.get().xp_samples;
        assert_eq!(after_fail.len(), after_good.len());
        assert_eq!(after_fail, after_good);
        let rate_after = xp::compute_rate(&after_fail, 3, XpStability::Steady);
        assert_eq!(rate_after.available, rate_before.available);
        assert_eq!(rate_after.per_hour, rate_before.per_hour);
        assert_eq!(rate_after.provisional, rate_before.provisional);
        assert_eq!(store.missed_ticks(), 3);
    }
}
