package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const namespace = "df-hud"

// version is reported by /healthz, -version and the User-Agent. A var rather
// than a const so `make` can stamp the commit into it (-ldflags -X only works on
// variables), which is what makes "which build is running" answerable for a
// service. A plain `go build` still gives the honest dev default.
var version = "0.1.0-dev"

func main() {
	// Time only, no date: this log is read while something is happening, and
	// journald stamps its own copy anyway.
	log.SetFlags(log.Ltime)

	var (
		configPath  = flag.String("config", defaultConfigPath(), "path to config.toml")
		once        = flag.Bool("once", false, "poll once, print the view, and exit")
		printView   = flag.Bool("print-view", false, "print the derived view as JSON on every update")
		showVersion = flag.Bool("version", false, "print the version and exit")
		checkConfig = flag.Bool("check-config", false, "validate the config and exit")
		checkGame   = flag.Bool("check-game", false, "report whether the game client is detected, and exit")
		dumpFields  = flag.Bool("dump-fields", false, "with -once, print the player record for diagnostics (credentials withheld)")
		dumpChals   = flag.Bool("dump-challenges", false, "fetch the challenge board once, print it, and exit")
		headless    = flag.Bool("headless", false, "run without the HUD window")
		printHUD    = flag.Bool("print-hud", false, "print the HUD's text lines on every update")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("df-hud %s\n", version)
		return
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *checkConfig {
		fmt.Printf("config ok (%s)\n", describeConfigSource(cfg, *configPath))
		fmt.Printf("request budget: about %.0f/hour while playing, %.0f/hour idle\n",
			cfg.RequestsPerHour(1), cfg.RequestsPerHour(0))
		return
	}
	if *checkGame {
		reportGameDetection(cfg)
		return
	}
	log.Printf("df-hud %s starting (%s)", version, describeConfigSource(cfg, *configPath))

	if err := cfg.EnsureDataDir(); err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.HUD.Enabled && cfg.HUD.HidesUnderFullscreen() {
		// Worth saying out loud: this looks exactly like df-hud being broken.
		log.Printf("config: hud.layer = %q sits BELOW fullscreen windows, so the HUD will be "+
			"invisible while the game is fullscreen; use \"overlay\"", cfg.HUD.Layer)
	}
	if cfg.HUD.Enabled && !cfg.HUD.ClickThrough {
		// The surface covers the whole monitor, so an input-consuming one eats
		// every click on the screen. The symptom is the game not responding at
		// all, which nobody would trace to a HUD setting.
		log.Print("config: hud.click_through is off and the HUD surface covers the whole " +
			"monitor, so it will swallow EVERY pointer event - the game will not respond " +
			"to the mouse. Turn it back on unless you are debugging the surface.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The one-shot diagnostics use the credentials already on disk, so they must
	// not need the bridge port - otherwise they cannot run while the daemon is
	// up, which is exactly when you reach for them.
	oneShot := *once || *dumpChals
	app, err := newApp(ctx, cfg, *configPath, !oneShot)
	if err != nil {
		log.Fatalf("startup: %v", err)
	}
	defer app.Close()

	if *dumpChals {
		app.dumpChallenges(ctx, *dumpFields)
		return
	}
	if *once {
		app.runOnce(ctx, *dumpFields)
		return
	}
	app.run(ctx, runOptions{
		printView: *printView,
		printHUD:  *printHUD,
		// The HUD is the point of the program, so it is on unless the config
		// disables it or the flag does.
		hud: cfg.HUD.Enabled && !*headless,
		// stop cancels the context, which is the same path SIGINT takes, so
		// quitting from the tray shuts down exactly like Ctrl-C.
		quit: stop,
	})
}

// reportGameDetection answers "does df-hud see my game?" without starting
// anything. Worth its own flag because the failure is silent: a process name
// that does not match means df-hud never polls, which looks like the HUD being
// broken rather than a one-word config problem.
func reportGameDetection(cfg *Config) {
	scanner := newProcScanner(cfg.Game.Process)
	fmt.Printf("looking for a process whose argv[0] basename is %q\n", cfg.Game.Process)

	state, err := scanner.scan()
	if err != nil {
		fmt.Printf("could not scan /proc: %v\n", err)
		return
	}
	if state.Running {
		fmt.Printf("FOUND: pid %d, launched %s (%s ago)\n",
			state.PID, state.StartedAt.Format(time.DateTime),
			state.Elapsed(time.Now()).Round(time.Second))
		reportWindowDetection(cfg, state)
		return
	}

	fmt.Println("NOT FOUND - the game does not appear to be running.")
	// If it is running under another name, saying so is far more useful than
	// "not found", since the fix is one config line.
	if candidates := similarProcesses(scanner.procRoot, "frontier"); len(candidates) > 0 {
		fmt.Println("\nProcesses with a similar name, in case the executable is named differently:")
		for _, c := range candidates {
			fmt.Println("  " + c)
		}
		fmt.Println("\nIf one of those is the game, set game.process to its basename.")
	} else {
		fmt.Println("Nothing similar is running either. Start the game and run this again.")
	}
	if cfg.Poll.OnlyWhenGameRunning {
		fmt.Println("\nNote: poll.only_when_game_running is on, so df-hud will not poll at all " +
			"until the game is detected.")
	}
}

// reportWindowDetection answers the other half: whether the COMPOSITOR's view of
// the game can be found. Its own silent failure - an unmatched window makes
// follow_game_workspace do nothing and monitor = "auto" unable to follow. The
// window classes are printed on a miss because that is the whole answer.
func reportWindowDetection(cfg *Config, game GameState) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client := hyprClient{}
	place, err := client.GameWindow(ctx, game.PID, cfg.Game.WindowMatch())
	if err != nil {
		fmt.Printf("\nThe compositor could not be asked where the window is (%v).\n"+
			"The HUD will still work; it just shows on every workspace.\n", err)
		return
	}
	if place.Known {
		shown := "no - the HUD will be hidden while it stays there"
		if place.OnActiveWorkspace {
			shown = "yes"
		}
		fmt.Printf("\nWINDOW: class %q on monitor %s, workspace %s (matched by %s)\n",
			place.Class, place.Monitor, place.WorkspaceName, place.MatchedBy)
		fmt.Printf("        that workspace is being shown: %s\n", shown)
		return
	}

	fmt.Printf("\nWINDOW NOT MATCHED: no window with pid %d, and none whose class looks like %q.\n",
		game.PID, cfg.Game.WindowMatch().Class)
	if ignore := cfg.Game.WindowTitleIgnore; len(ignore) > 0 {
		fmt.Printf("        (a title containing %q is skipped as the launcher: game.window_title_ignore)\n",
			strings.Join(ignore, ", "))
	}
	if windows, err := client.Windows(ctx); err == nil {
		fmt.Println("\nWindows the compositor knows about:")
		for _, w := range windows {
			title := w.Title
			if len(title) > 40 {
				title = title[:40] + "..."
			}
			fmt.Printf("  class %-32s pid %-8d %s\n", w.Class, w.PID, title)
		}
		fmt.Println("\nIf one of those is the game, set game.window_class to its class.")
	}
	fmt.Println("\nUntil then the HUD shows on every workspace, and monitor = \"auto\" " +
		"leaves the choice to the compositor.")
}

// similarProcesses looks for the substring anywhere in a command line - the
// opposite of the strict argv[0] match the scanner uses, and appropriate here
// because this is a diagnostic rather than a decision.
func similarProcesses(procRoot, needle string) []string {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	var out []string
	self, parent := os.Getpid(), os.Getppid()
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == self || pid == parent {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		line := strings.ReplaceAll(string(raw), "\x00", " ")
		lower := strings.ToLower(line)
		if !strings.Contains(lower, needle) {
			continue
		}
		// Anything mentioning df-hud is our own tooling - the shell that invoked
		// this check names the game in its own command line - and the game's
		// command line will never mention us.
		if strings.Contains(lower, "df-hud") {
			continue
		}
		if len(line) > 120 {
			line = line[:120] + "..."
		}
		out = append(out, fmt.Sprintf("pid %d: %s", pid, strings.TrimSpace(line)))
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func describeConfigSource(cfg *Config, path string) string {
	if cfg.path == "" {
		return "built-in defaults, no config file at " + path
	}
	return "config " + cfg.path
}

// app is the wiring, assembled here and nowhere else so the data flow reads in
// one place: bridge -> credentials -> poller -> store, with the game watcher
// gating the poller and the catalog feeding derived values.
type app struct {
	// cfg is swapped wholesale on reload from the watcher's goroutine while every
	// poller reads it once per iteration from its own. Atomic because the previous
	// plain field was a data race that only stayed quiet because nothing had
	// reloaded a config under load.
	cfg     atomic.Pointer[Config]
	cfgPath string

	creds      *credStore
	catalog    *Catalog
	client     *Client
	game       *GameWatcher
	visibility *visibilityWatcher
	bosses     *BossPoller
	presence   *PresenceServer
	gamekeys   *gameKeys
	poller     *Poller
	challenges *ChallengePoller
	gate       *rateGate
	store      *Store
	state      *stateStore

	bridge    *bridgeServer
	bridgeSrv *http.Server
	watcher   *configWatcher

	// groups is the runtime show/hide for individual widget groups, from a keybind
	// or the tray. Not config, and not persisted - see groupToggles.
	groups *groupToggles

	// lastRunStart is only touched from the poller's tick callback, which is a
	// single goroutine, so it needs no lock.
	lastRunStart time.Time

	reloadMu       sync.Mutex
	onConfigReload func(*Config)
}

func newApp(ctx context.Context, cfg *Config, cfgPath string, withBridge bool) (*app, error) {
	a := &app{cfgPath: cfgPath}
	a.cfg.Store(cfg)

	a.creds = newCredStore(cfg.CredentialsPath())
	if err := a.creds.Load(); err != nil {
		// A bad credentials file is not fatal: the bridge can replace it.
		log.Printf("credentials: %v", err)
	}

	a.state = newStateStore(cfg.StatePath())
	if err := a.state.Load(); err != nil {
		log.Printf("state: %v", err)
	}

	// The catalog is public data, so this needs no credentials and can happen
	// before the bridge has ever run.
	catalogCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	catalog, err := ensureCatalog(catalogCtx, &http.Client{Timeout: cfg.DF.Timeout.Duration},
		cfg.CatalogPath(), cfg.DF.AllstatsURL, cfg.DF.UserAgent, cfg.Poll.CatalogInterval.Duration)
	if err != nil {
		// Without it there is no XP table, but df_exptotal and the position still
		// work, so this is a degradation rather than a stop.
		log.Printf("catalog: unavailable (%v); level thresholds will be missing", err)
	} else {
		a.catalog = catalog
		log.Printf("catalog: %s", catalog.Summary())
	}

	a.store = newStore(a.catalog)
	a.store.SetXPWindow(func() []XPSample { return a.state.Get().XPSamples }, cfg.Widget.XP.MinSamples)
	a.client = &Client{
		HTTP:      &http.Client{Timeout: cfg.DF.Timeout.Duration},
		BaseURL:   cfg.DF.BaseURL,
		UserAgent: cfg.DF.UserAgent,
	}
	a.game = newGameWatcher(cfg.Game.Process, cfg.Game.ScanInterval.Duration)
	a.visibility = newVisibilityWatcher(a.game, a.Config, hyprClient{})
	// A run persisted by a previous df-hud process. Restored only if the game it
	// belongs to is still the one running, so restarting df-hud mid-run keeps the
	// clock instead of resetting it.
	if run := a.state.Get().Run; run != nil {
		a.store.SetRunSeed(run)
		// Seeded here too, or a resumed run looks brand new to recordXPSample
		// and it discards the rate window just restored from disk.
		a.lastRunStart = run.StartedAt
	}
	// One gate for every scheduler, so the minimum spacing is a property of
	// df-hud's traffic rather than of any one poller's.
	a.groups = newGroupToggles()
	a.gate = newRateGate(minRequestGap)
	a.poller = newPoller(a.client, a.creds, a.game, a.Config)
	a.poller.gate = a.gate
	a.challenges = newChallengePoller(a.client, a.creds, a.game, a.gate, a.Config,
		func() (int, bool) {
			if snap, ok := a.store.Snapshot(); ok {
				return snap.Level, snap.GoldMember
			}
			return 0, false
		})
	// A different server with a different budget, so its own client and scheduler
	// rather than sharing the game's rate gate: coupling them would let one starve
	// the other for no benefit.
	a.bosses = newBossPoller(&http.Client{Timeout: cfg.DF.Timeout.Duration}, a.game, a.Config,
		func() bool {
			snap, ok := a.store.Snapshot()
			return ok && snap.PositionX == onslaughtCoord && snap.PositionY == onslaughtCoord
		})
	a.bosses.SetOnMap(func(m *BossMap) { a.store.SetBossMap(m) })

	// Where you are, straight from the game client. Not a poller: the client
	// pushes, so there is nothing to schedule and nothing to be polite about.
	if cfg.Presence.Enabled {
		a.presence = newPresenceServer(cfg.Presence.SocketPath())
		a.presence.SetOnState(func(p PresenceState) {
			a.store.SetPresence(p)
			// A new block is the moment the event map matters, and the client
			// says so 15-25s before the poll would.
			if p.HasPosition {
				a.bosses.Wake()
			}
		})
	}

	// The game's FPS readout, which starts off at every launch because nothing in
	// the game remembers it. Reads the window the visibility watcher has already
	// resolved, so it costs no extra compositor queries.
	a.gamekeys = newGameKeys(a.Config, a.game.State, a.visibility.Placement, hyprClient{})
	a.gamekeys.active = hyprClient{}.ActiveAddress
	// Not while the client is still loading: a key pressed at the loading screen
	// is simply discarded, which is what made the FPS key intermittent.
	a.gamekeys.ready = func() bool { return a.store.ClientInWorld(time.Now()) }

	a.challenges.SetOnBoard(func(board []Challenge) {
		a.store.SetChallenges(board, time.Now())
		a.store.SetChallengeStatus("")
	})

	// The game starting or stopping changes the cadence, so tell the poller
	// rather than making it discover the change on its next tick. It also decides
	// whether the HUD is on screen at all.
	a.game.SetOnChange(func(g GameState) {
		a.store.SetGame(g)
		a.poller.Wake()
		a.challenges.Wake()
		a.bosses.Wake()
		a.visibility.Poke()
	})
	// levelSeen fires the one event the challenge board waits for. Only on the
	// transition: waking that scheduler when it is not paused makes it poll again
	// as soon as the gate allows, pulling the board's cadence down to the minimum
	// request gap.
	levelSeen := false
	var lastBlock [2]int
	a.poller.SetOnTick(func(tick Tick) {
		a.store.ApplyTick(tick)
		a.store.SetPollerStatus(a.poller.Status())
		a.recordXPSample()
		a.persistRun()
		if !levelSeen {
			if snap, ok := a.store.Snapshot(); ok && snap.Level > 0 {
				levelSeen = true
				a.challenges.Wake()
			}
		}
		// Arriving on a new block is the moment the event map matters. Subject
		// to the minimum interval, so walking cannot turn into a burst.
		if snap, ok := a.store.Snapshot(); ok && snap.HasPosition {
			block := [2]int{snap.PositionX, snap.PositionY}
			if block != lastBlock {
				lastBlock = block
				a.bosses.Wake()
			}
		}
	})

	if cfg.Bridge.Enabled && withBridge {
		bs, srv, err := startBridge(cfg.Bridge.Listen, a.creds, func() {
			// New credentials: clear the stale stop and poll as soon as the
			// request gap allows.
			a.store.SetCredentialsAt(a.creds.UpdatedAt())
			a.poller.Resume()
			a.challenges.Resume()
		})
		if err != nil {
			return nil, err
		}
		// The same two corrections the tray offers, on the loopback listener so a
		// keybind can reach them without taking a hand off the mouse.
		bs.runStart = func() { a.store.RestartRun(time.Now(), "a keybind") }
		bs.xpReset = a.resetXPRate
		bs.overlayToggle = func() { a.visibility.SetEnabled(!a.visibility.Enabled()) }
		bs.widgetToggle = func(group string) error {
			if !knownGroup(group) {
				return fmt.Errorf("no widget group %q", group)
			}
			hidden := a.groups.Toggle(group)
			state := "shown"
			if hidden {
				state = "hidden"
			}
			log.Printf("hud: %s %s by hand", group, state)
			return nil
		}
		a.bridge, a.bridgeSrv = bs, srv
	}
	if !a.creds.UpdatedAt().IsZero() {
		a.store.SetCredentialsAt(a.creds.UpdatedAt())
	}
	if _, _, ok := a.creds.Get(); !ok {
		log.Print("no session yet: install the the bridge userscript userscript and open any " +
			"logged-in Dead Frontier page (the bridge userscript)")
	}

	// Created even with no config file in it, so the watcher has something to
	// watch and a config dropped in later takes effect without a restart.
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		log.Printf("config: could not create %s: %v", filepath.Dir(cfgPath), err)
	}
	if watcher, err := newConfigWatcher(cfgPath); err != nil {
		log.Printf("config: not watching for changes (%v)", err)
	} else {
		a.watcher = watcher
	}
	return a, nil
}

// Config returns the live config, which every component reads per iteration so a
// hot reload takes effect without a restart.
func (a *app) Config() *Config { return a.cfg.Load() }

// SetOnConfigReload registers the HUD's re-styling hook. Guarded because it is
// set from the GTK thread while the config watcher may already be running.
func (a *app) SetOnConfigReload(fn func(*Config)) {
	a.reloadMu.Lock()
	a.onConfigReload = fn
	a.reloadMu.Unlock()
}

func (a *app) Close() {
	if a.bridgeSrv != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		a.bridgeSrv.Shutdown(shutdownCtx)
	}
	// Save unconditionally: the debounce exists to spare the disk during a run,
	// not to lose the last window on the way out.
	if err := a.state.Save(); err != nil {
		log.Printf("state: could not save: %v", err)
	}
}

// runOnce is -once: one poll, print what was derived, exit. The first thing to
// reach for when asking "is the whole chain working?".
func (a *app) runOnce(ctx context.Context, dumpFields bool) {
	tick := a.poller.Once(ctx)
	if tick.Err != nil {
		log.Fatalf("poll failed: %v", tick.Err)
	}
	if dumpFields {
		dumpRecordFields(tick.Vars)
	}
	a.store.ApplyTick(tick)
	a.store.SetGame(a.game.State())
	a.store.SetPollerStatus(a.poller.Status())

	// A one-off scan rather than the watcher, since nothing is running yet.
	if state, err := a.game.scanner.scan(); err == nil {
		a.store.SetGame(state)
	}
	printViewJSON(a.store.Derive(time.Now()))
}

type runOptions struct {
	printView bool
	printHUD  bool
	hud       bool
	quit      func()
}

// dumpChallenges fetches the challenge board once and prints it. This is a
// hashed endpoint, so it also proves the signing salt is right end to end -
// a wrong salt comes back as a rejection rather than as data.
func (a *app) dumpChallenges(ctx context.Context, raw bool) {
	cr, _, ok := a.creds.Get()
	if !ok {
		log.Fatal("no credentials yet: load a Dead Frontier page with the bridge userscript")
	}
	// The cookie travels with the request for endpoints that check the site
	// session rather than only the credential triple.
	a.client.Cookie = cr.Cookie
	salt := a.Config().SigningSalt(a.creds)
	if salt == "" {
		log.Fatal("no signing salt: the bridge has not reported one, and df.skeygen is empty. " +
			"Load the Outpost home page with the bridge userscript or the the bridge userscript installed.")
	}

	reqCtx, cancel := context.WithTimeout(ctx, a.Config().DF.Timeout.Duration)
	defer cancel()
	vars, err := a.client.LoadChallenge(reqCtx, cr, salt)
	if err != nil {
		log.Fatalf("load_challenge failed: %v", err)
	}
	if raw {
		dumpRecordFields(vars)
		return
	}

	// Reward XP is a per-level multiplier, doubled for gold members, so poll once
	// for those two facts rather than printing rewards at the wrong value.
	if _, ok := a.store.Snapshot(); !ok {
		if tick := a.poller.Once(ctx); tick.Err == nil {
			a.store.ApplyTick(tick)
		} else {
			log.Printf("could not read your level (%v); reward XP will be omitted", tick.Err)
		}
	}
	level, gold := 0, false
	if snap, ok := a.store.Snapshot(); ok {
		level, gold = snap.Level, snap.GoldMember
	}
	board := parseChallenges(vars, level, gold)
	fmt.Printf("%d challenges (level %d)\n", len(board), level)
	for _, c := range board {
		kind := "personal"
		if c.Clan {
			kind = "clan"
		}
		status := " "
		if c.Complete() {
			status = "x"
		}
		fmt.Printf("\n[%s] %-8s %s\n", status, kind, c.Name)
		if remaining := c.Remaining(time.Now()); remaining > 0 {
			fmt.Printf("        ends in %s\n", formatCountdown(remaining))
		}
		for _, o := range c.Objectives {
			fmt.Printf("        %-28s %s / %s  (%.0f%%)\n",
				o.Name, formatInt(o.Score), formatInt(o.Target), o.Fraction()*100)
		}
		switch {
		case c.RewardPoints > 0:
			fmt.Printf("        reward: %d clan points\n", c.RewardPoints)
		case c.RewardExp > 0:
			fmt.Printf("        reward: %s xp\n", formatInt(c.RewardExp))
		}
		if c.RewardSpecial != "" {
			fmt.Printf("        reward: %s\n", c.RewardSpecial)
		}
	}
}

func (a *app) run(ctx context.Context, opts runOptions) {
	// Everything runs in its own goroutine and communicates only through the
	// store, because GTK then takes the main thread and never gives it back.
	go a.game.Run(ctx)
	go a.visibility.Run(ctx)
	// One event stream, two subscribers. Neither parses the payload: an event is
	// only ever a hint to go and re-read the authoritative source.
	go watchHyprEvents(ctx, func(name string) {
		if hyprWindowEvents[name] {
			a.game.Poke()
		}
		if hyprPlacementEvents[name] {
			a.visibility.Poke()
		}
	})
	go a.poller.Run(ctx)
	go a.challenges.Run(ctx)
	go a.bosses.Run(ctx)
	if a.presence != nil {
		go a.presence.Run(ctx)
	}
	go a.gamekeys.Run(ctx)
	go a.reportChallengeStatus(ctx)
	if a.watcher != nil {
		go a.watcher.Run(ctx, a.Config, a.applyReload)
	}
	go a.reloadOnSIGHUP(ctx)
	if opts.printView || opts.printHUD {
		go a.printLoop(ctx, opts)
	}
	if a.Config().Tray.Enabled {
		// The tray is what gives df-hud a presence when the HUD is hidden, so it
		// runs whether or not there is a HUD at all - including under -headless.
		go runTray(ctx, trayActions{
			SetOverlayEnabled:   a.visibility.SetEnabled,
			OverlayEnabled:      a.visibility.Enabled,
			SetChallengesHidden: func(hidden bool) { a.groups.Set("challenges", hidden) },
			ChallengesHidden:    func() bool { return a.groups.Hidden("challenges") },
			SetFPSDisplay:       a.gamekeys.SetFPSDisplay,
			FPSDisplay:          a.gamekeys.FPSDisplay,
			SetDismissLauncher:  a.gamekeys.SetDismissLauncher,
			DismissLauncher:     a.gamekeys.DismissLauncher,
			ResetXPRate:         a.resetXPRate,
			RestartRunClock:     func() { a.store.RestartRun(time.Now(), "the tray menu") },
			ReloadConfig:        a.reloadConfig,
			Quit:                opts.quit,
			View:                func() *View { return a.store.Derive(time.Now()) },
			Visibility:          a.visibility.State,
		})
	}

	log.Printf("polling budget: about %.0f requests/hour while playing, %.0f idle",
		a.Config().RequestsPerHour(1), a.Config().RequestsPerHour(0))

	if opts.hud {
		// Blocks until the window closes or the context ends. The HUD runs its
		// own 1s derive tick, so nothing else needs to.
		if err := runUI(ctx, a); err != nil {
			log.Fatalf("hud: %v", err)
		}
		log.Print("shutting down")
		return
	}

	// Headless: keep the 1s cadence so clocks and the debounced state save still
	// happen, just with nothing to draw.
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

// reportChallengeStatus keeps the widget's explanation current: a pause reason is
// only useful if it reaches the screen.
func (a *app) reportChallengeStatus(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, ok := a.store.Challenges(); ok {
				a.store.SetChallengeStatus("") // a board is showing; nothing to explain
				continue
			}
			if reason := a.challenges.pauseReason(); reason != "" {
				a.store.SetChallengeStatus(reason)
				continue
			}
			if failures, _, lastErr := a.challenges.Status(); failures > 0 {
				_ = lastErr
				a.store.SetChallengeStatus("board unavailable (retrying)")
				continue
			}
			// Nothing wrong and no board yet: the first fetch is in flight.
			// Clearing matters as much as setting - without it the last pause
			// reason outlives the pause, and the HUD went on saying "the game is
			// not running" for a whole session with the game plainly running.
			a.store.SetChallengeStatus("")
		}
	}
}

// printLoop is the diagnostic output, kept off the HUD's own tick so that
// printing cannot slow down or interleave with rendering.
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
				lines := hudLines(view, a.Config())
				if len(lines) == 0 {
					fmt.Println("[hud empty]")
					continue
				}
				fmt.Println(strings.Join(lines, "\n") + "\n---")
			}
		}
	}
}

// applyReload swaps in a reloaded config. Only the fields the running components
// read per iteration change; the rest were already frozen by reloadableFrom.
func (a *app) applyReload(next *Config, _ []string) {
	a.cfg.Store(next)
	a.poller.Wake()
	// The visibility rules are config, so a reload can change whether the HUD
	// should be on screen right now.
	a.visibility.Poke()
	a.bosses.Wake()
	// The HUD's appearance is config too: without this, font size, colours and
	// margins would silently need a restart while everything else reloaded.
	a.reloadMu.Lock()
	fn := a.onConfigReload
	a.reloadMu.Unlock()
	if fn != nil {
		fn(next)
	}
	log.Print("config: reloaded")
}

// reloadOnSIGHUP is `systemctl --user reload df-hud`, and the same by hand.
//
// It has to be HANDLED rather than left alone: the default disposition for
// SIGHUP is to terminate, so an ExecReload of kill -HUP would kill the HUD - and
// a "reload" that loses the run clock and the XP window is not a reload.
func (a *app) reloadOnSIGHUP(ctx context.Context) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			log.Print("config: SIGHUP")
			a.reloadConfig()
		}
	}
}

// reloadConfig re-reads the file on demand, for the tray menu and SIGHUP. The
// watcher already reloads on every save; this is for when a save produced no event,
// which happens on some network and bind-mounted home directories.
func (a *app) reloadConfig() {
	next, frozen, err := reloadFrom(a.cfgPath, a.Config())
	if err != nil {
		log.Printf("config: reload rejected, keeping the running config: %v", err)
		return
	}
	a.applyReload(next, frozen)
}

// resetXPRate throws the rate window away, the tray's answer to a challenge
// reward: a lump of XP no killing produced, which inflates the average for as
// long as the window is wide. The record reports a total, not a source, so the
// judgement has to be the player's.
func (a *app) resetXPRate() {
	a.state.ResetXPWindow("reset from the tray menu")
}

// persistRun stores the run clock's start, so a df-hud restart mid-run resumes
// the clock instead of showing zero for a run that is an hour old.
func (a *app) persistRun() {
	start, game := a.store.Run()
	a.state.Update(func(st *State) {
		if start.IsZero() || !game.Running {
			st.Run = nil
			return
		}
		st.Run = &RunState{StartedAt: start, GamePID: game.PID, GameStartedAt: game.StartedAt}
	})
}

// recordXPSample feeds the rate window. It lives here rather than in the store
// because it is the one place that knows both the snapshot and the persistent
// state.
func (a *app) recordXPSample() {
	snap, ok := a.store.Snapshot()
	if !ok || snap.XPSource == xpSourceNone || snap.CumulativeXP <= 0 {
		return
	}
	cfg := a.Config()
	window := cfg.Widget.XP.EffectiveWindow(cfg.Poll.ActiveInterval.Duration)

	// A new run starts a new average: the samples before it were collected while
	// the launcher was on screen earning nothing, so averaging across makes the
	// first minutes read as a fraction of the real rate.
	if runStart, _ := a.store.Run(); xpRunReset(a.lastRunStart, runStart) {
		a.lastRunStart = runStart
		a.state.ResetXPWindow("a new run started")
	}

	// Discard the window when the samples either side stop being comparable - a
	// boost, a death, a clock jump, a long absence. Averaging across any of those
	// produces a rate that describes no real period.
	if prev, had := a.store.PreviousSnapshot(); had {
		if reason := xpWindowReset(prev, snap, window); reason != "" {
			a.state.ResetXPWindow(reason)
		}
	}

	a.state.AppendXPSample(XPSample{
		At:         snap.At,
		Cumulative: snap.CumulativeXP,
		Source:     snap.XPSource.String(),
	}, window)
}

// withheldFieldPattern names what must never be printed. The rest - level, cash,
// kills, position - is what DFProfiler publishes publicly for any account, so
// printing it on the player's own machine costs nothing and answers most "why is
// this widget empty?" questions. df_session3d is withheld because its meaning is
// unverified and a field named "session" gets the benefit of the doubt.
var withheldFieldPattern = regexp.MustCompile(`(?i)pass|token|cookie|auth|secretkey|^sc$|session`)

// dumpRecordFields prints the raw player record for diagnostics.
func dumpRecordFields(vars map[string]string) {
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Printf("%d fields returned:\n", len(names))
	for _, name := range names {
		value := vars[name]
		if withheldFieldPattern.MatchString(name) {
			value = "[withheld]"
		}
		fmt.Printf("  %s = %s\n", name, value)
	}
}

func printViewJSON(v *View) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		log.Printf("view: %v", err)
		return
	}
	fmt.Fprintln(os.Stdout, string(data))
}
