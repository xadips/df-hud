package main

import (
	"context"
	"df-hud/internal/autostart"
	"df-hud/internal/bossmap"
	"df-hud/internal/bridge"
	"df-hud/internal/catalog"
	"df-hud/internal/config"
	"df-hud/internal/creds"
	"df-hud/internal/desktop"
	"df-hud/internal/dfclient"
	"df-hud/internal/game"
	"df-hud/internal/gamekeys"
	"df-hud/internal/hotkeys"
	"df-hud/internal/hud/groups"
	hudgtk "df-hud/internal/hud/gtk"
	"df-hud/internal/hud/render"
	"df-hud/internal/model"
	"df-hud/internal/poller"
	"df-hud/internal/presence"
	"df-hud/internal/rategate"
	"df-hud/internal/state"
	"df-hud/internal/store"
	"df-hud/internal/tray"
	"df-hud/internal/visibility"
	"df-hud/internal/xp"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// app is the composition root: bridge -> credentials -> pollers -> store, with
// the game watcher gating network work and the catalog feeding derived values.
type app struct {
	cfg     atomic.Pointer[config.Config]
	cfgPath string

	creds      *creds.Store
	catalog    *catalog.Catalog
	client     *dfclient.Client
	game       *game.GameWatcher
	visibility *visibility.Watcher
	bosses     *bossmap.BossPoller
	presence   *presence.PresenceServer
	gamekeys   *gamekeys.Keys
	hotkeys    *hotkeys.Hotkeys
	poller     *poller.Poller
	challenges *poller.ChallengePoller
	gate       *rategate.Gate
	store      *store.Store
	state      *state.Store

	bridgeSrv *http.Server
	watcher   *config.Watcher
	groups    *groups.Groups

	lastRunStart time.Time
	runPersistMu sync.Mutex

	reloadMu       sync.Mutex
	onConfigReload func(*config.Config)
}

func prepareConfig(cfg *config.Config) error {
	if err := cfg.EnsureDataDir(); err != nil {
		return err
	}
	if cfg.HUD.Enabled && cfg.HUD.HidesUnderFullscreen() {
		log.Printf("config: hud.layer = %q sits BELOW fullscreen windows, so the HUD will be "+
			"invisible while the game is fullscreen; use \"overlay\"", cfg.HUD.Layer)
	}
	if cfg.HUD.Enabled && !cfg.HUD.ClickThrough {
		log.Print("config: hud.click_through is off and the HUD surface covers the whole " +
			"monitor, so it will swallow EVERY pointer event - the game will not respond " +
			"to the mouse. Turn it back on unless you are debugging the surface.")
	}
	return nil
}

func newApp(ctx context.Context, cfg *config.Config, cfgPath string, withBridge bool) (*app, error) {
	a := &app{cfgPath: cfgPath}
	a.cfg.Store(cfg)

	a.creds = creds.NewStore(cfg.CredentialsPath())
	if err := a.creds.Load(); err != nil {
		log.Printf("credentials: %v", err)
	}

	a.state = state.NewStore(cfg.StatePath())
	if err := a.state.Load(); err != nil {
		log.Printf("state: %v", err)
	}

	catalogCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cat, err := catalog.Ensure(catalogCtx, &http.Client{Timeout: cfg.DF.Timeout.Duration},
		cfg.CatalogPath(), cfg.DF.AllstatsURL, cfg.DF.UserAgent, cfg.Poll.CatalogInterval.Duration)
	if err != nil {
		log.Printf("catalog: unavailable (%v); level thresholds will be missing", err)
	} else {
		a.catalog = cat
		log.Printf("catalog: %s", cat.Summary())
	}

	a.store = store.New(a.catalog)
	a.store.SetXPWindow(func() []model.XPSample { return a.state.Get().XPSamples }, cfg.Widget.XP.MinSamples)
	a.client = &dfclient.Client{
		HTTP:      &http.Client{Timeout: cfg.DF.Timeout.Duration},
		BaseURL:   cfg.DF.BaseURL,
		UserAgent: cfg.DF.UserAgent,
	}
	a.game = game.NewWatcher(cfg.Game.Process, cfg.Game.ScanInterval.Duration)
	desktopClient := desktop.NewClient()
	a.visibility = visibility.NewWatcher(a.game, a.Config, desktopClient)

	if run := a.state.Get().Run; run != nil {
		a.store.SetRunSeed(run)
		a.lastRunStart = run.StartedAt
	}
	a.store.SetOnRunChange(a.persistRun)

	a.groups = groups.New()
	a.gate = rategate.New(poller.MinRequestGap)
	a.poller = poller.New(a.client, a.creds, a.game, a.Config)
	a.poller.SetGate(a.gate)
	a.challenges = poller.NewChallenge(a.client, a.creds, a.game, a.gate, a.Config,
		func() (int, bool) {
			if snap, ok := a.store.Snapshot(); ok {
				return snap.Level, snap.GoldMember
			}
			return 0, false
		})
	a.bosses = bossmap.NewPoller(&http.Client{Timeout: cfg.DF.Timeout.Duration}, a.game, a.Config,
		func() bool {
			x, y, ok := a.store.EffectivePosition(time.Now())
			return ok && x == bossmap.OnslaughtCoord && y == bossmap.OnslaughtCoord
		})
	a.bosses.SetOnMap(a.store.SetBossMap)

	if cfg.Presence.Enabled {
		a.presence = presence.NewServer(cfg.Presence.SocketPath())
		a.presence.SetOnState(func(p model.PresenceState) {
			a.store.SetPresence(p)
			if p.HasPosition {
				a.bosses.Wake()
			}
		})
		a.presence.SetOnConnectionChange(a.store.SetPresenceConnected)
	}

	a.gamekeys = gamekeys.New(a.Config, a.game.State, a.visibility.Placement, desktopClient)
	a.gamekeys.SetActive(desktopClient.ActiveAddress)
	a.gamekeys.SetReady(func() bool { return a.store.ClientInWorld(time.Now()) })
	a.hotkeys = hotkeys.New(
		func() hotkeys.Config {
			cfg := a.Config().Hotkeys
			return hotkeys.Config{
				Enabled: cfg.Enabled, Map: cfg.Map, Challenges: cfg.Challenges,
				RunStart: cfg.RunStart, XPReset: cfg.XPReset, Overlay: cfg.Overlay,
			}
		},
		func() bool {
			placement := a.visibility.Placement()
			if !desktop.CanStartRun(placement) {
				return false
			}
			// The placement watcher reacts quickly, but a global key must be
			// released even faster when alt-tab moves focus elsewhere. Compare
			// the live foreground HWND as the final gate so a key typed into the
			// next application is never consumed on stale placement state.
			active, ok := desktopClient.ActiveAddress(context.Background())
			return ok && active == placement.Address
		},
		hotkeys.Actions{
			ToggleMap:        func() { _ = a.toggleWidget("map") },
			ToggleChallenges: func() { _ = a.toggleWidget("challenges") },
			StartRun:         func() { a.store.RestartRun(time.Now(), "a native keybind") },
			ResetXP:          a.resetXPRate,
			ToggleOverlay:    a.toggleOverlay,
		},
	)

	a.challenges.SetOnBoard(func(board []model.Challenge) {
		a.store.SetChallenges(board, time.Now())
		a.store.SetChallengeStatus("")
	})
	a.game.SetOnChange(func(g model.GameState) {
		a.store.SetGame(g)
		// Presence can beat the process scan at startup. Replay only an activity
		// emitted during this process, otherwise a quiet client would not be
		// recognized until its next block or party change.
		if g.Running && a.presence != nil && a.presence.Connected() {
			if p, ok := a.presence.Last(); ok && !p.At.Before(g.StartedAt) {
				a.store.SetPresence(p)
			}
		}
		a.poller.Wake()
		a.challenges.Wake()
		a.bosses.Wake()
		a.visibility.Poke()
	})

	levelSeen := false
	var lastBlock [2]int
	a.poller.SetOnTick(func(tick model.Tick) {
		applied := a.store.ApplyTick(tick)
		a.store.SetPollerStatus(a.poller.Status())
		snap, haveSnap := a.store.Snapshot()
		if applied && haveSnap && snap.HasPosition && !snap.InOutpost && !snap.Dead &&
			desktop.CanStartRun(a.visibility.Placement()) {
			a.store.StartRunIfIdle(tick.At, "the foreground game window entered the city")
		}
		a.recordXPSample()
		if !levelSeen && haveSnap && snap.Level > 0 {
			levelSeen = true
			a.challenges.Wake()
		}
		if haveSnap && snap.HasPosition {
			block := [2]int{snap.PositionX, snap.PositionY}
			if block != lastBlock {
				lastBlock = block
				a.bosses.Wake()
			}
		}
	})

	if cfg.Bridge.Enabled && withBridge {
		bridge.Version = version
		bs, srv, err := bridge.Start(cfg.Bridge.Listen, a.creds, func() {
			a.store.SetCredentialsAt(a.creds.UpdatedAt())
			a.poller.Resume()
			a.challenges.Resume()
		})
		if err != nil {
			return nil, err
		}
		bs.SetRunStart(func() { a.store.RestartRun(time.Now(), "a compositor keybind") })
		bs.SetXPReset(a.resetXPRate)
		bs.SetOverlayToggle(a.toggleOverlay)
		bs.SetWidgetToggle(a.toggleWidget)
		a.bridgeSrv = srv
	}
	if !a.creds.UpdatedAt().IsZero() {
		a.store.SetCredentialsAt(a.creds.UpdatedAt())
	}
	if _, _, ok := a.creds.Get(); !ok {
		log.Print("no session yet: install the the bridge userscript userscript and open any " +
			"logged-in Dead Frontier page (the bridge userscript)")
	}

	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		log.Printf("config: could not create %s: %v", filepath.Dir(cfgPath), err)
	}
	if watcher, err := config.NewWatcher(cfgPath); err != nil {
		log.Printf("config: not watching for changes (%v)", err)
	} else {
		a.watcher = watcher
	}
	return a, nil
}

func (a *app) Config() *config.Config { return a.cfg.Load() }

func (a *app) SetOnConfigReload(fn func(*config.Config)) {
	a.reloadMu.Lock()
	a.onConfigReload = fn
	a.reloadMu.Unlock()
}

func (a *app) Close() {
	if a.bridgeSrv != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = a.bridgeSrv.Shutdown(shutdownCtx)
	}
	a.persistRun()
	if err := a.state.Save(); err != nil {
		log.Printf("state: could not save: %v", err)
	}
}

type runOptions struct {
	printView bool
	printHUD  bool
	hud       bool
	quit      func()
}

func (a *app) run(ctx context.Context, opts runOptions) {
	go a.game.Run(ctx)
	go a.visibility.Run(ctx)
	go desktop.WatchEvents(ctx, a.game.Poke, a.visibility.Poke)
	go a.poller.Run(ctx)
	go a.challenges.Run(ctx)
	go a.bosses.Run(ctx)
	if a.presence != nil {
		go a.presence.Run(ctx)
	}
	go a.gamekeys.Run(ctx)
	go a.hotkeys.Run(ctx)
	go a.reportChallengeStatus(ctx)
	if a.watcher != nil {
		go a.watcher.Run(ctx, a.Config, a.applyReload)
	}
	go watchReloadSignal(ctx, a.reloadConfig)
	if opts.printView || opts.printHUD {
		go a.printLoop(ctx, opts)
	}
	if a.Config().Tray.Enabled {
		// tray.Run locks its own OS thread on Windows and uses the external
		// D-Bus loop on Linux, so it remains safe to launch here.
		var retryPresence func() bool
		var presenceBindFailed func() bool
		if a.presence != nil {
			retryPresence = a.presence.Retry
			presenceBindFailed = a.presence.BindFailed
		}
		actions := tray.Actions{
			SetOverlayEnabled:   a.visibility.SetEnabled,
			OverlayEnabled:      a.visibility.Enabled,
			SetChallengesHidden: a.setChallengesHidden,
			ChallengesHidden: func() bool {
				return !a.Config().Widget.Challenges.Enabled || a.groups.Hidden("challenges")
			},
			SetFPSDisplay:      a.setFPSDisplay,
			FPSDisplay:         a.gamekeys.FPSDisplay,
			SetDismissLauncher: a.setDismissLauncher,
			DismissLauncher:    a.gamekeys.DismissLauncher,
			ResetXPRate:        a.resetXPRate,
			RestartRunClock:    func() { a.store.RestartRun(time.Now(), "the tray menu") },
			RetryPresence:      retryPresence,
			PresenceBindFailed: presenceBindFailed,
			ReloadConfig:       a.reloadConfig,
			Quit:               opts.quit,
			Version:            version,
			View:               func() *model.View { return a.store.Derive(time.Now()) },
			Visibility:         a.visibility.State,
		}
		if autostart.Available() {
			actions.SetStartOnLogin = autostart.SetEnabled
			actions.StartOnLogin = func() bool {
				enabled, _ := autostart.Enabled()
				return enabled
			}
		}
		go tray.Run(ctx, actions)
	}

	log.Printf("polling budget: about %.0f requests/hour while playing, %.0f idle",
		a.Config().RequestsPerHour(1), a.Config().RequestsPerHour(0))

	if opts.hud {
		if err := hudgtk.Run(ctx, hudgtk.Dependencies{
			Config:             a.Config,
			Derive:             a.store.Derive,
			MaybeSave:          a.state.MaybeSave,
			Visibility:         a.visibility.State,
			OnVisibilityChange: a.visibility.SetOnChange,
			GroupHidden:        a.groups.Hidden,
			OnGroupsChange:     a.groups.SetOnChange,
			OnConfigReload:     a.SetOnConfigReload,
		}); err != nil {
			log.Fatalf("hud: %v", err)
		}
		log.Print("shutting down")
		return
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Print("shutting down")
			return
		case <-ticker.C:
			if err := a.state.MaybeSave(); err != nil {
				log.Printf("state: could not save: %v", err)
			}
		}
	}
}

func (a *app) reportChallengeStatus(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, ok := a.store.Challenges(); ok {
				a.store.SetChallengeStatus("")
				continue
			}
			if reason := a.challenges.PauseReason(); reason != "" {
				a.store.SetChallengeStatus(reason)
				continue
			}
			if failures, _, _ := a.challenges.Status(); failures > 0 {
				a.store.SetChallengeStatus("board unavailable (retrying)")
				continue
			}
			a.store.SetChallengeStatus("")
		}
	}
}

func (a *app) printLoop(ctx context.Context, opts runOptions) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			view := a.store.Derive(time.Now())
			if opts.printView {
				printViewJSON(view)
			}
			if opts.printHUD {
				lines := render.HUDLines(view, a.Config())
				if len(lines) == 0 {
					fmt.Println("[hud empty]")
					continue
				}
				fmt.Println(strings.Join(lines, "\n") + "\n---")
			}
		}
	}
}

func (a *app) applyReload(next *config.Config, _ []string) {
	prev := a.Config()
	a.cfg.Store(next)
	a.gamekeys.ApplyConfig(next.GameKeys)
	if prev.Widget.Challenges.Enabled != next.Widget.Challenges.Enabled {
		a.groups.Set("challenges", !next.Widget.Challenges.Enabled)
	}
	a.poller.Wake()
	a.visibility.Poke()
	a.bosses.Wake()
	a.reloadMu.Lock()
	fn := a.onConfigReload
	a.reloadMu.Unlock()
	if fn != nil {
		fn(next)
	}
	log.Print("config: reloaded")
}

func (a *app) reloadConfig() {
	next, frozen, err := config.Reload(a.cfgPath, a.Config())
	if err != nil {
		log.Printf("config: reload rejected, keeping the running config: %v", err)
		return
	}
	a.applyReload(next, frozen)
}

func (a *app) resetXPRate() {
	a.state.ResetXPWindow("reset by hand")
}

func (a *app) setFPSDisplay(on bool) {
	if a.persistTrayOption(config.TrayFPSDisplay, on) {
		a.gamekeys.SetFPSDisplay(on)
	}
}

func (a *app) setDismissLauncher(on bool) {
	if a.persistTrayOption(config.TrayDismissLauncher, on) {
		a.gamekeys.SetDismissLauncher(on)
	}
}

func (a *app) setChallengesHidden(hidden bool) {
	if a.persistTrayOption(config.TrayShowChallenges, !hidden) {
		a.groups.Set("challenges", hidden)
	}
}

func (a *app) persistTrayOption(option config.TrayOption, enabled bool) bool {
	if err := config.SetTrayOption(a.cfgPath, option, enabled); err != nil {
		log.Printf("config: could not persist %s from tray: %v", option, err)
		return false
	}
	return true
}

func (a *app) toggleOverlay() {
	a.visibility.SetEnabled(!a.visibility.Enabled())
}

func (a *app) toggleWidget(group string) error {
	if !groups.Known(group) {
		return fmt.Errorf("no widget group %q", group)
	}
	hidden := a.groups.Toggle(group)
	shown := "shown"
	if hidden {
		shown = "hidden"
	}
	log.Printf("hud: %s %s by hand", group, shown)
	return nil
}

func (a *app) persistRun() {
	a.runPersistMu.Lock()
	defer a.runPersistMu.Unlock()

	started, runningGame := a.store.Run()
	a.state.Update(func(st *state.State) {
		if started.IsZero() || !runningGame.Running {
			st.Run = nil
			return
		}
		st.Run = &model.RunState{
			StartedAt:     started,
			GamePID:       runningGame.PID,
			GameStartedAt: runningGame.StartedAt,
		}
	})
}

func (a *app) recordXPSample() {
	snap, ok := a.store.Snapshot()
	if !ok || snap.XPSource == model.XPSourceNone || snap.CumulativeXP <= 0 {
		return
	}
	cfg := a.Config()
	window := cfg.Widget.XP.EffectiveWindow(cfg.Poll.ActiveInterval.Duration)

	if runStart, _ := a.store.Run(); xp.RunReset(a.lastRunStart, runStart) {
		a.lastRunStart = runStart
		a.state.ResetXPWindow("a new run started")
	}
	if prev, had := a.store.PreviousSnapshot(); had {
		if reason := xp.WindowReset(prev, snap, window); reason != "" {
			a.state.ResetXPWindow(reason)
		}
	}
	a.state.AppendXPSample(model.XPSample{
		At:         snap.At,
		Cumulative: snap.CumulativeXP,
		Source:     snap.XPSource.String(),
	}, window)
}
