//! Composition root: catalog, creds, pollers, bridge, presence, overlay handle.

pub mod autostart;
pub mod groups;
pub mod hotkeys;
pub mod poller;
pub mod rategate;
pub mod state;
pub mod store;
pub mod tray;
pub mod updates;
pub mod visibility;

use crate::wake::lock;
use chrono::{DateTime, Utc};
use std::sync::atomic::{AtomicBool, AtomicI64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

#[cfg(unix)]
use std::os::fd::RawFd;
#[cfg(target_os = "linux")]
use std::sync::OnceLock;
#[cfg(target_os = "linux")]
use std::sync::atomic::AtomicI32;
#[cfg(windows)]
use windows_sys::Win32::Foundation::HANDLE;

use crate::config::{self, Config};
use crate::data::{bossmap, catalog, xp};
use crate::game::{self, desktop, presence};
use crate::model::{RunState, Tick, XpSample, XpSource};
use crate::net::bridge::{self, Hooks};
use crate::net::creds::Store as Creds;
use crate::net::dfclient::Client;
use crate::wake::{Notify, Wake};

use groups::{Group, Groups};
use poller::{ChallengePoller, MIN_REQUEST_GAP, MasteryPoller, PlayerPoller, PollerRuntime};
use rategate::Gate;
use store::Store;

#[cfg(target_os = "linux")]
static HUP: AtomicBool = AtomicBool::new(false);
/// Write end of the wake pipe, for the SIGHUP handler.
#[cfg(target_os = "linux")]
static HUP_FD: AtomicI32 = AtomicI32::new(-1);

/// The overlay's wake channel. Same surface as [`Wake`]; on Linux `take`
/// also services SIGHUP, which the handler turns into one byte on this
/// pipe, so a reload costs no polling thread.
pub struct AppWake {
    inner: Arc<Wake>,
    #[cfg(target_os = "linux")]
    on_hup: OnceLock<Box<dyn Fn() + Send + Sync>>,
}

impl AppWake {
    fn new(inner: Arc<Wake>) -> Self {
        Self {
            inner,
            #[cfg(target_os = "linux")]
            on_hup: OnceLock::new(),
        }
    }

    pub fn ping(&self) {
        self.inner.ping();
    }

    /// Blocks until something pinged (or SIGHUP interrupted the wait). The
    /// caller then runs [`take`](Self::take).
    pub fn wait(&self) {
        self.inner.wait();
    }

    /// Drains pending wakeups. Called by the surface loop after its poll
    /// returns, which is where a SIGHUP lands.
    pub fn take(&self) {
        self.inner.take();
        #[cfg(target_os = "linux")]
        if HUP.swap(false, Ordering::SeqCst)
            && let Some(on_hup) = self.on_hup.get()
        {
            on_hup();
        }
    }

    #[cfg(unix)]
    pub fn read_fd(&self) -> RawFd {
        self.inner.read_fd()
    }

    #[cfg(windows)]
    pub fn event_handle(&self) -> HANDLE {
        self.inner.event_handle()
    }
}

pub struct Handle {
    pub store: Arc<Store>,
    pub cfg: Arc<config::Shared>,
    pub groups: Arc<Groups>,
    pub overlay_on: Arc<AtomicBool>,
    pub game_running: Arc<AtomicBool>,
    pub visible: Arc<AtomicBool>,
    pub wake: AppWake,
    pub ui: Arc<Notify>,
    shutdown: Arc<Notify>,
    bossmap_wake: Arc<Notify>,
    pub creds: Arc<Creds>,
    pub game: Arc<game::Watcher>,
    pub vis: Arc<visibility::Watcher>,
    active_address: Mutex<Option<String>>,
    persist: Arc<state::Persist>,
    stop: Arc<AtomicBool>,
    player: Arc<PlayerPoller>,
    challenges: Arc<ChallengePoller>,
    masteries: Arc<MasteryPoller>,
    last_run_start: Mutex<Option<DateTime<Utc>>>,
    presence: Option<Arc<presence::Control>>,
    pub gamekeys: Arc<crate::game::gamekeys::Keys>,
    update_status: Mutex<updates::Status>,
    agent: SharedAgent,
}

/// The process's one HTTP agent: a connection pool plus TLS setup shared by
/// the three pollers, the feeds thread, the update check and the startup
/// catalog fetch. `replace_config` swaps in a new one when `[df]` timeout or
/// user agent change, so every holder re-reads this slot rather than keeping
/// its own clone.
type SharedAgent = Arc<Mutex<ureq::Agent>>;

fn agent_of(slot: &Mutex<ureq::Agent>) -> ureq::Agent {
    lock(slot).clone()
}

impl Handle {
    /// The running config. One `Arc` clone; never copies the `Config`.
    pub fn config(&self) -> Arc<Config> {
        self.cfg.get()
    }

    fn agent(&self) -> ureq::Agent {
        agent_of(&self.agent)
    }

    pub fn ping(&self) {
        self.wake_ui();
        self.player.wake();
        self.challenges.wake();
        self.masteries.wake();
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
        self.masteries.resume();
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
            error!("state: could not save: {err}");
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
        self.masteries.wake();
        self.bossmap_wake.ping();
        self.persist.poke();
        if let Some(p) = &self.presence {
            p.poke();
        }
        self.wake_ui();
    }

    pub fn stopped(&self) -> bool {
        self.stop.load(Ordering::SeqCst)
    }

    /// Flips a widget and returns whether it is now hidden.
    pub fn toggle_group(&self, g: Group) -> bool {
        let hidden = self.groups.toggle(g);
        self.wake_ui();
        hidden
    }

    pub fn restart_run(&self) {
        self.store.restart_run(Utc::now());
        self.wake_ui();
    }

    fn persist_run(&self) {
        persist_run(&self.store, &self.persist);
    }

    pub fn reset_xp(&self) {
        reset_xp_window(&self.store, &self.persist, "reset by hand");
        self.wake_ui();
    }

    pub fn config_error(&self) -> Option<String> {
        self.store.config_error()
    }

    pub fn set_config_error(&self, err: impl Into<String>) {
        self.store.set_config_error(err.into());
        self.wake_ui();
    }

    pub fn clear_config_error(&self) {
        self.store.set_config_error(String::new());
    }

    pub fn note_config_watch(&self, watch: &crate::config::Watch, reloaded: bool) {
        if reloaded {
            self.clear_config_error();
        } else if let Some(err) = watch.error() {
            self.set_config_error(err);
        }
    }

    pub fn replace_config(&self, mut cfg: Config) -> Arc<Config> {
        self.clear_config_error();
        let running = self.config();
        let ignored = cfg.reloadable_from(&running);
        let rebuild_clients = cfg.df.base_url != running.df.base_url
            || cfg.df.user_agent != running.df.user_agent
            || cfg.df.timeout != running.df.timeout;
        let reconfigure_game = cfg.game.process != running.game.process
            || cfg.game.scan_interval != running.game.scan_interval;
        if !ignored.is_empty() {
            warn!(
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
        let cfg = Arc::new(cfg);
        self.cfg.set(cfg.clone());
        if rebuild_clients {
            let agent = df_agent(&cfg);
            *lock(&self.agent) = agent.clone();
            self.player.replace_client(df_client(&agent, &cfg));
            self.challenges.replace_client(df_client(&agent, &cfg));
            self.masteries.replace_client(df_client(&agent, &cfg));
        }
        if reconfigure_game {
            self.game
                .reconfigure(&cfg.game.process, cfg.game.scan_interval.0);
        }
        self.game.poke();
        self.vis.poke();
        // The feeds thread re-reads `bossmap.enabled` and the intervals on a
        // poke, so a disable takes effect now rather than at the next deadline.
        self.refresh_feeds();
        cfg
    }

    pub fn set_fps_display(&self, on: bool) {
        if persist_tray(self, crate::config::TrayOption::FpsDisplay, on) {
            self.gamekeys.set_fps_display(on);
            self.cfg.update(|c| c.game_keys.fps_display = on);
            self.wake_ui();
        }
    }

    pub fn set_dismiss_launcher(&self, on: bool) {
        if persist_tray(self, crate::config::TrayOption::DismissLauncher, on) {
            self.gamekeys.set_dismiss_launcher(on);
            self.cfg.update(|c| c.game_keys.dismiss_launcher = on);
            self.wake_ui();
        }
    }

    /// The tray's onboarding path for the masteries widget: configs written
    /// before it existed never gain the section on their own, so the first
    /// click writes `[widget.masteries] enabled = true` into the file. Only
    /// ever called with `true`; turning it back off is a config edit.
    pub fn enable_masteries_widget(&self) {
        if persist_tray(self, crate::config::TrayOption::MasteriesWidget, true) {
            self.cfg.update(|c| c.widget.masteries.enabled = true);
            self.masteries.wake();
            self.wake_ui();
        }
    }

    pub fn masteries_widget_enabled(&self) -> bool {
        self.config().widget.masteries.enabled
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
        lock(&self.active_address).clone()
    }

    fn refresh_active_address(&self) {
        *lock(&self.active_address) = desktop::new_client().active_address();
    }

    pub fn config_file_path(&self) -> std::path::PathBuf {
        self.config()
            .source_path()
            .map_or_else(crate::config::default_path, std::path::Path::to_path_buf)
    }

    /// Tray and SIGHUP reload. The file watcher in the overlay loop is the
    /// other caller of `Config::reload`; both print the same lines.
    pub fn reload_config(&self) {
        match Config::reload(&self.config_file_path()) {
            Ok(cfg) => {
                self.replace_config(cfg);
            }
            Err(err) => self.set_config_error(err),
        }
    }

    pub fn open_config(&self) {
        let path = self.config_file_path();
        if let Err(err) = crate::config::write_defaults_if_missing_with_reference(
            &path,
            crate::overlay::seed_reference(),
        ) {
            error!("tray: could not write config file: {err}");
        }
        if let Err(err) = autostart::open_file(&path) {
            error!("tray: could not open config file: {err}");
        }
    }

    pub fn open_log(&self) {
        let path = crate::config::default_log_path();
        if let Some(dir) = path.parent()
            && let Err(err) = std::fs::create_dir_all(dir)
        {
            error!("tray: could not create log directory: {err}");
            return;
        }
        if !path.exists()
            && let Err(err) = std::fs::OpenOptions::new()
                .create(true)
                .append(true)
                .open(&path)
        {
            error!("tray: could not create log file: {err}");
            return;
        }
        if let Err(err) = autostart::open_file(&path) {
            error!("tray: could not open log file: {err}");
        }
    }

    pub fn update_status(&self) -> updates::Status {
        lock(&self.update_status).clone()
    }

    /// Probes GitHub off-thread and opens the release page when it is newer.
    pub fn check_updates(self: &Arc<Self>) {
        const CURRENT: &str = env!("CARGO_PKG_VERSION");
        {
            let mut status = lock(&self.update_status);
            if *status == updates::Status::Checking {
                return;
            }
            *status = updates::Status::Checking;
        }
        let handle = self.clone();
        let agent = self.agent();
        poller::spawn("df-hud-updates", self.stop.clone(), move || {
            let outcome = match updates::check_with(&agent, CURRENT) {
                Ok(updates::Check::Newer { version }) => {
                    info!(
                        "updates: {version} is out (running {CURRENT}); opening the release page"
                    );
                    updates::open_release_page();
                    updates::Status::Newer(version)
                }
                Ok(updates::Check::UpToDate) => {
                    info!("updates: {CURRENT} is the latest");
                    updates::Status::UpToDate
                }
                Err(err) => {
                    warn!("updates: check failed: {err}");
                    updates::Status::Failed
                }
            };
            *lock(&handle.update_status) = outcome;
        });
    }

    pub fn start_on_login(&self) -> bool {
        autostart::enabled().unwrap_or(false)
    }

    pub fn set_start_on_login(&self, on: bool) {
        match autostart::set_enabled(on) {
            Ok(()) => {
                if cfg!(windows) {
                    info!("tray: Windows startup enabled: {on}");
                }
            }
            Err(err) => error!("tray: could not update Windows startup: {err}"),
        }
    }
}

impl Drop for Handle {
    fn drop(&mut self) {
        self.persist_run();
        if let Err(err) = self.persist.save() {
            error!("state: could not save: {err}");
        }
        self.signal_stop();
    }
}

#[derive(Clone, Copy, Debug, Default)]
pub struct PrintOpts {
    pub hud: bool,
}

pub fn load_creds_and_catalog(
    agent: &ureq::Agent,
    cfg: &Config,
) -> Result<(Arc<Creds>, Option<catalog::Catalog>), Box<dyn std::error::Error>> {
    cfg.ensure_data_dir()?;
    let creds = Arc::new(Creds::new(cfg.credentials_path()));
    if let Err(err) = creds.load() {
        warn!("credentials: {err}");
    }
    let catalog = match catalog::ensure_with(
        agent,
        &cfg.catalog_path(),
        &cfg.df.allstats_url,
        &cfg.df.user_agent,
        cfg.poll.catalog_interval.0,
        cfg.df.timeout.0,
        Utc::now(),
    ) {
        Ok(c) => Some(c),
        Err(err) => {
            warn!("catalog: unavailable ({err}); level thresholds will be missing");
            None
        }
    };
    Ok((creds, catalog))
}

/// The one agent everything HTTP in the process shares. Built once per
/// config (see [`Handle::replace_config`]); callers clone the handle, never
/// the pool.
pub fn df_agent(cfg: &Config) -> ureq::Agent {
    ureq::Agent::new_with_config(
        ureq::Agent::config_builder()
            .timeout_global(Some(cfg.df.timeout.0))
            .user_agent(cfg.df.user_agent.clone())
            .http_status_as_error(false)
            .build(),
    )
}

pub fn df_client(agent: &ureq::Agent, cfg: &Config) -> Client {
    Client::with_agent(agent.clone(), &cfg.df.base_url, &cfg.df.user_agent)
}

pub fn start_with(
    cfg: Config,
    print: PrintOpts,
) -> Result<Arc<Handle>, Box<dyn std::error::Error>> {
    if let Err(err) = autostart::reconcile() {
        warn!("startup: could not refresh login launch entry: {err}");
    }
    let agent = df_agent(&cfg);
    let (creds, catalog) = load_creds_and_catalog(&agent, &cfg)?;
    if let Some(c) = &catalog {
        info!("catalog: {}", c.summary());
    }
    let store = Arc::new(Store::new(catalog));
    store.set_public_id_configured(!cfg.df.user_id.is_empty());
    if let Some(at) = creds.updated_at() {
        store.set_credentials_at(at);
    }
    let persist = Arc::new(state::Persist::new(cfg.state_path()));
    if let Err(err) = persist.load() {
        warn!("state: {err}");
    }
    let mut last_run_start = None;
    if let Some(run) = persist.get().run {
        last_run_start = Some(run.started_at);
        store.set_run_seed(Some(run));
    }
    store.set_xp_window(persist.get().xp_samples, cfg.widget.xp.min_samples);
    {
        let persist = persist.clone();
        let hud = store.clone();
        store.set_on_run_change(move || persist_run(&hud, &persist));
    }

    let player_client = Arc::new(Mutex::new(df_client(&agent, &cfg)));
    let challenge_client = Arc::new(Mutex::new(df_client(&agent, &cfg)));
    let mastery_client = Arc::new(Mutex::new(df_client(&agent, &cfg)));
    let agent: SharedAgent = Arc::new(Mutex::new(agent));
    let session_stale = Arc::new(AtomicBool::new(false));
    let shared = config::Shared::new(cfg);
    let cfg = shared.get();
    let stop = Arc::new(AtomicBool::new(false));
    let game_running = Arc::new(AtomicBool::new(false));
    let pending_launch_at = Arc::new(AtomicI64::new(0));
    let visible = Arc::new(AtomicBool::new(true));
    let gate = Arc::new(Gate::new(MIN_REQUEST_GAP));
    let groups = Arc::new(Groups::new());
    let overlay_on = Arc::new(AtomicBool::new(true));
    let wake = Arc::new(Wake::new()?);
    let ui = Arc::new(Notify::new());
    let shutdown = Arc::new(Notify::new());
    let bossmap_wake = Arc::new(Notify::new());
    let presence = if cfg.presence.enabled {
        Some(presence::Control::new()?)
    } else {
        None
    };
    let gamekeys = crate::game::gamekeys::Keys::new(&cfg.game_keys);

    let process = if cfg.game.process.trim().is_empty() {
        game::DEFAULT_PROCESS
    } else {
        cfg.game.process.as_str()
    };
    let game = game::Watcher::new(process, cfg.game.scan_interval.0);
    let desktop_client = desktop::new_client();
    let active_address = desktop_client.active_address();
    let query = Some(Arc::new(desktop_client) as Arc<dyn visibility::Querier>);
    let vis = visibility::Watcher::new(game.clone(), shared.clone(), query);

    let poller_runtime = PollerRuntime {
        creds: creds.clone(),
        store: store.clone(),
        cfg: shared.clone(),
        gate: gate.clone(),
        stop: stop.clone(),
        shutdown: shutdown.clone(),
        game_running: game_running.clone(),
        session_stale,
    };
    let player = PlayerPoller::new(player_client, poller_runtime.clone());
    let challenges =
        ChallengePoller::new(challenge_client, persist.clone(), poller_runtime.clone());
    let masteries = MasteryPoller::new(mastery_client, poller_runtime);

    let handle = Arc::new(Handle {
        store: store.clone(),
        cfg: shared.clone(),
        groups: groups.clone(),
        overlay_on: overlay_on.clone(),
        game_running: game_running.clone(),
        visible: visible.clone(),
        wake: AppWake::new(wake.clone()),
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
        masteries: masteries.clone(),
        last_run_start: Mutex::new(last_run_start),
        presence: presence.clone(),
        gamekeys: gamekeys.clone(),
        update_status: Mutex::new(updates::Status::Unchecked),
        agent: agent.clone(),
    });

    {
        let handle = handle.clone();
        player.set_on_tick(move |tick: Tick| {
            let xp_window = {
                let cfg = handle.config();
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
        let pending_launch_at = pending_launch_at.clone();
        game.set_on_change(move |st| {
            handle.store.set_game(st);
            handle.game_running.store(st.running, Ordering::SeqCst);
            pending_launch_at.store(0, Ordering::SeqCst);
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
    poller::spawn("df-hud-masteries", stop.clone(), {
        let masteries = masteries.clone();
        move || masteries.run()
    });
    poller::spawn("df-hud-state", stop.clone(), {
        let persist = persist.clone();
        let stop = stop.clone();
        move || persist.run_saver(&stop)
    });
    poller::spawn("df-hud-feeds", stop.clone(), {
        let store = store.clone();
        let cfg = shared.clone();
        let agent = agent.clone();
        let stop = stop.clone();
        let shutdown = shutdown.clone();
        let poke = bossmap_wake.clone();
        let wake = wake.clone();
        let gate = gate.clone();
        move || {
            let feeds = Feeds {
                store: &store,
                cfg: &cfg,
                agent: &agent,
                stop: &stop,
                shutdown: &shutdown,
                poke: &poke,
                wake: &wake,
                gate: &gate,
            };
            feeds_loop(&feeds);
        }
    });

    {
        if cfg.presence.enabled {
            let store = store.clone();
            let wake = wake.clone();
            let stop = stop.clone();
            let path = if cfg.presence.socket.is_empty() {
                presence::default_socket()
            } else {
                cfg.presence.socket.clone()
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
        if cfg.bridge.enabled {
            let creds = creds.clone();
            let handle = handle.clone();
            let listen = cfg.bridge.listen.clone();
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
                        move |g| {
                            handle.toggle_group(g);
                        }
                    })),
                },
                handle.game_running.clone(),
                pending_launch_at.clone(),
            ) {
                Ok(_) => {}
                Err(err) => error!("bridge: {err}"),
            }
        }
    }

    if print.hud {
        let handle = handle.clone();
        poller::spawn("df-hud-print", stop.clone(), move || {
            while !handle.stopped() {
                let view = handle.store.derive(Utc::now());
                let cfg = handle.config();
                print!(
                    "{}",
                    crate::overlay::present::format_hud(&view, &cfg, &handle.groups)
                );
                // 1 Hz by design; the shutdown ping ends the wait early.
                handle.shutdown.wait_timeout(Duration::from_secs(1));
            }
        });
    }

    #[cfg(target_os = "linux")]
    catch_sighup(&handle);

    Ok(handle)
}

#[cfg(target_os = "linux")]
extern "C" fn on_sighup(_: libc::c_int) {
    // Async-signal-safe only: an atomic store and one write(2). The byte
    // wakes the surface loop, whose `AppWake::take` runs the reload.
    HUP.store(true, Ordering::SeqCst);
    let fd = HUP_FD.load(Ordering::SeqCst);
    if fd >= 0 {
        let byte = 1u8;
        // SAFETY: write(2) is async-signal-safe; the pointer is to one live
        // byte on this frame and the length is 1. `fd` is the wake pipe's
        // write end, which `Wake::write_fd` promises stays open for the rest
        // of the process (the `Handle` owning it is never dropped once the
        // handler is installed). The end is non-blocking, and a dropped write
        // into a full pipe is fine: a wake is already pending.
        unsafe {
            libc::write(fd, std::ptr::from_ref(&byte).cast(), 1);
        }
    }
}

#[cfg(target_os = "linux")]
fn catch_sighup(handle: &Arc<Handle>) {
    let weak = Arc::downgrade(handle);
    let _ = handle.wake.on_hup.set(Box::new(move || {
        if let Some(h) = weak.upgrade() {
            info!("config: SIGHUP");
            h.reload_config();
        }
    }));
    HUP_FD.store(handle.wake.inner.write_fd(), Ordering::SeqCst);
    // SAFETY: `on_sighup` is an `extern "C" fn(c_int)`, the shape `signal`
    // expects behind `sighandler_t`, and it does only async-signal-safe work
    // (two atomics and one write(2)). SIGHUP is a valid, catchable signal,
    // and `HUP_FD` was stored above so the handler never sees a stale fd.
    unsafe {
        libc::signal(libc::SIGHUP, on_sighup as *const () as libc::sighandler_t);
    }
}

fn ingest_player_tick(
    store: &Store,
    persist: &state::Persist,
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
    persist: &state::Persist,
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
        let mut last = lock(last_run_start);
        if xp::run_reset(*last, run_start) {
            *last = run_start;
            drop(last);
            reset_xp_window(store, persist, "a new run started");
        }
    }
    if let Some(prev) = store.previous_snapshot()
        && let Some(reason) = xp::window_reset(&prev, &snap, xp_window)
    {
        reset_xp_window(store, persist, reason);
    }
    let sample = XpSample {
        at: snap.at,
        cumulative: snap.cumulative_xp,
        source: snap.xp_source.as_str().to_string(),
    };
    store.append_xp_sample(sample.clone(), xp_window);
    persist.append_xp_sample(sample, xp_window);
}

/// The HUD store and the state file hold the same ring; every write goes
/// to both so `derive` never has to read the persisted copy.
fn reset_xp_window(store: &Store, persist: &state::Persist, reason: &str) {
    store.reset_xp_window();
    persist.reset_xp_window(reason);
}

fn persist_run(store: &Store, persist: &state::Persist) {
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
    let path = handle.config_file_path();
    if let Err(err) = crate::config::set_tray_option(&path, option, on) {
        error!("config: could not persist {option:?} from tray: {err}");
        return false;
    }
    true
}

/// What the feeds thread borrows from `start_with`.
struct Feeds<'a> {
    store: &'a Store,
    cfg: &'a config::Shared,
    agent: &'a Mutex<ureq::Agent>,
    stop: &'a AtomicBool,
    shutdown: &'a Notify,
    poke: &'a Notify,
    wake: &'a Wake,
    gate: &'a Gate,
}

/// The two background feeds on one timer thread: the allstats catalog and the
/// city event map. Each keeps a deadline; the loop sleeps until the nearer
/// one. A `poke` (game start, config change, stop) pulls the city map
/// forward; with `bossmap.enabled = false` it has no deadline and the thread
/// sleeps until the next poke.
fn feeds_loop(f: &Feeds<'_>) {
    let mut catalog_due = Instant::now();
    let mut bossmap_due = Some(Instant::now());
    // `bosshash` of the map in the store; an unchanged feed is not re-parsed.
    let mut boss_hash: Option<String> = None;
    loop {
        if f.stop.load(Ordering::SeqCst) {
            return;
        }
        let c = f.cfg.get();
        // Re-read per pass so a `[df]` reload's new pool is picked up here
        // too. Timeout and status handling are set per request by the
        // `_with` helpers.
        let agent = agent_of(f.agent);
        if Instant::now() >= catalog_due {
            if f.gate.wait(f.stop, f.shutdown).is_err() {
                return;
            }
            refresh_catalog(&agent, f.store, &c);
            catalog_due = Instant::now() + c.poll.catalog_interval.0.max(Duration::from_secs(60));
        }
        if bossmap_due.is_some_and(|due| Instant::now() >= due) {
            bossmap_due = if c.bossmap.enabled {
                if f.gate.wait(f.stop, f.shutdown).is_err() {
                    return;
                }
                refresh_bossmap(&agent, f.store, f.wake, &c, &mut boss_hash);
                Some(Instant::now() + bossmap_interval(f.store, &c))
            } else {
                f.store.clear_boss_map();
                boss_hash = None;
                f.wake.ping();
                None
            };
        }
        let next = bossmap_due.map_or(catalog_due, |due| due.min(catalog_due));
        if f.poke
            .wait_pinged(next.saturating_duration_since(Instant::now()))
        {
            bossmap_due = Some(Instant::now());
        }
    }
}

fn refresh_catalog(agent: &ureq::Agent, store: &Store, c: &Config) {
    match catalog::ensure_with(
        agent,
        &c.catalog_path(),
        &c.df.allstats_url,
        &c.df.user_agent,
        c.poll.catalog_interval.0,
        c.df.timeout.0,
        Utc::now(),
    ) {
        Ok(cat) => store.set_catalog(cat),
        Err(err) => warn!("catalog: {err}"),
    }
}

fn refresh_bossmap(
    agent: &ureq::Agent,
    store: &Store,
    wake: &Wake,
    c: &Config,
    hash: &mut Option<String>,
) {
    match bossmap::fetch_if_changed_with(
        agent,
        &c.bossmap.url,
        &c.df.user_agent,
        c.df.timeout.0,
        hash.as_deref(),
    ) {
        Ok(Some(m)) => {
            *hash = Some(m.hash.clone());
            store.set_boss_map(m);
            wake.ping();
        }
        Ok(None) => {}
        Err(err) => warn!("bossmap: {err}"),
    }
}

/// Faster inside Onslaught, where the block roster changes by the minute.
fn bossmap_interval(store: &Store, c: &Config) -> Duration {
    let onslaught = store
        .effective_position(Utc::now())
        .is_some_and(|(x, y)| x == bossmap::ONSLAUGHT_COORD && y == bossmap::ONSLAUGHT_COORD);
    let wait = if onslaught {
        c.bossmap.onslaught_interval.0
    } else {
        c.bossmap.interval.0
    };
    wait.max(Duration::from_secs(5))
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
        let persist = state::Persist::new("");
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
        assert_eq!(
            store.xp_samples(),
            after_good,
            "the HUD ring mirrors the persisted one"
        );
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
        assert_eq!(store.xp_samples(), after_good);
        let view = store.derive(start + chrono::Duration::seconds(14));
        assert_eq!(view.xp_available, rate_before.per_hour.is_some());
        assert_eq!(view.xp_per_hour, rate_before.per_hour.unwrap_or(0.0));
        let rate_after = xp::compute_rate(&after_fail, 3, XpStability::Steady);
        assert_eq!(rate_after.per_hour, rate_before.per_hour);
        assert_eq!(rate_after.provisional, rate_before.provisional);
        assert_eq!(store.missed_ticks(), 3);
    }
}
