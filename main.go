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
	"syscall"
	"time"
)

const namespace = "df-hud"

// version is reported by /healthz, -version and the User-Agent. It stays a dev
// string until there is a release process, rather than claiming a version
// number nothing produces.
const version = "0.1.0-dev"

func main() {
	log.SetFlags(0)

	var (
		configPath  = flag.String("config", defaultConfigPath(), "path to config.toml")
		once        = flag.Bool("once", false, "poll once, print the view, and exit")
		printView   = flag.Bool("print-view", false, "print the derived view as JSON on every update")
		showVersion = flag.Bool("version", false, "print the version and exit")
		checkConfig = flag.Bool("check-config", false, "validate the config and exit")
		checkGame   = flag.Bool("check-game", false, "report whether the game client is detected, and exit")
		dumpFields  = flag.Bool("dump-fields", false, "with -once, print the player record for diagnostics (credentials withheld)")
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// -once is a diagnostic: it polls with the credentials already on disk, so it
	// must not need the bridge port. Without this it cannot run at all while the
	// daemon is up, which is exactly when you reach for it.
	app, err := newApp(ctx, cfg, *configPath, !*once)
	if err != nil {
		log.Fatalf("startup: %v", err)
	}
	defer app.Close()

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
	})
}

// reportGameDetection answers "does df-hud see my game?" without starting
// anything. Worth its own flag because the failure is silent: with
// only_when_game_running set, a process name that does not match means df-hud
// simply never polls, and the session clock never appears - which looks like the
// HUD being broken rather than like a one-word config problem.
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

// app is the wiring. Everything is assembled here and nowhere else, so the data
// flow is readable in one place: bridge -> credentials -> poller -> store, with
// the game watcher gating the poller and the catalog feeding derived values.
type app struct {
	cfg     *Config
	cfgPath string

	creds   *credStore
	catalog *Catalog
	client  *Client
	game    *GameWatcher
	poller  *Poller
	store   *Store
	state   *stateStore

	bridge    *bridgeServer
	bridgeSrv *http.Server
	watcher   *configWatcher
}

func newApp(ctx context.Context, cfg *Config, cfgPath string, withBridge bool) (*app, error) {
	a := &app{cfg: cfg, cfgPath: cfgPath}

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
		// Without it there is no XP table and no map grids, but df_exptotal and
		// the position still work, so this is a degradation rather than a stop.
		log.Printf("catalog: unavailable (%v); level thresholds and neighbourhood names will be missing", err)
	} else {
		a.catalog = catalog
		log.Printf("catalog: %s", catalog.Summary())
		if shape := catalog.UnexpectedShape(); shape != "" {
			log.Printf("catalog: %s", shape)
		}
	}

	a.store = newStore(a.catalog)
	a.client = &Client{
		HTTP:      &http.Client{Timeout: cfg.DF.Timeout.Duration},
		BaseURL:   cfg.DF.BaseURL,
		UserAgent: cfg.DF.UserAgent,
	}
	a.game = newGameWatcher(cfg.Game.Process, cfg.Game.ScanInterval.Duration)
	a.poller = newPoller(a.client, a.creds, a.game, a.Config)

	// The game starting or stopping changes the cadence, so tell the poller
	// rather than making it discover the change on its next tick.
	a.game.SetOnChange(func(g GameState) {
		a.store.SetGame(g)
		a.poller.Wake()
	})
	a.poller.SetOnTick(func(tick Tick) {
		a.store.ApplyTick(tick)
		a.store.SetPollerStatus(a.poller.Status())
		a.recordXPSample()
	})

	if cfg.Bridge.Enabled && withBridge {
		bs, srv, err := startBridge(cfg.Bridge.Listen, a.creds, func() {
			// New credentials: clear the stale stop and poll as soon as the
			// request gap allows.
			a.store.SetCredentialsAt(a.creds.UpdatedAt())
			a.poller.Resume()
		})
		if err != nil {
			return nil, err
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

	// Create the config directory even with no config file in it, so the
	// watcher has something to watch and a config dropped in later takes effect
	// without a restart. Watching a directory that does not exist is what the
	// "no such file or directory" case would otherwise be.
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

// Config returns the live config, which the poller reads per iteration so a hot
// reload takes effect without a restart.
func (a *app) Config() *Config { return a.cfg }

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

// runOnce is the -once mode: one poll, print what was derived, exit. The first
// thing to reach for when asking "is the whole chain working?".
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
}

func (a *app) run(ctx context.Context, opts runOptions) {
	// Everything runs in its own goroutine and communicates only through the
	// store, which matters because GTK then takes the main thread and never
	// gives it back.
	go a.game.Run(ctx)
	go watchHyprWindowEvents(ctx, a.game.Poke)
	go a.poller.Run(ctx)
	if a.watcher != nil {
		go a.watcher.Run(ctx, a.Config, a.applyReload)
	}
	if opts.printView || opts.printHUD {
		go a.printLoop(ctx, opts)
	}

	log.Printf("polling budget: about %.0f requests/hour while playing, %.0f idle",
		a.cfg.RequestsPerHour(1), a.cfg.RequestsPerHour(0))

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
	a.cfg = next
	a.poller.Wake()
	log.Print("config: reloaded")
}

// recordXPSample feeds the rate window. It lives here rather than in the store
// because it is the one place that knows both the snapshot and the persistent
// state.
func (a *app) recordXPSample() {
	snap, ok := a.store.Snapshot()
	if !ok || snap.XPSource == xpSourceNone || snap.CumulativeXP <= 0 {
		return
	}
	window := a.cfg.Widget.XP.EffectiveWindow(a.cfg.Poll.ActiveInterval.Duration)
	a.state.AppendXPSample(XPSample{
		At:         snap.At,
		Cumulative: snap.CumulativeXP,
		Source:     snap.XPSource.String(),
	}, window)
}

// withheldFieldPattern names the parts of the player record that must never be
// printed. The rest of it - level, cash, kills, position - is the same data
// DFProfiler publishes publicly for any account, so printing it on the player's
// own machine costs nothing and answers most "why is this widget empty?"
// questions. Credentials are a different matter, and df_session3d is withheld
// because its meaning is unverified and a field with "session" in the name gets
// the benefit of the doubt.
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
