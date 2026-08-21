package game

import (
	"context"
	"df-hud/internal/model"
	"log"
	"strings"
	"sync"
	"time"
)

// Game detection gates polling and drives the session clock. The platform
// scanner is the source of truth rather than the desktop window list: a process
// can exist before it maps a window, and only the process API gives the actual
// launch time when df-hud starts in the middle of a session.

// defaultGameProcess is the Proton/Steam client's executable name, verified
// against the local install:
// steamapps/compatdata/3818286641/pfx/drive_c/Program Files (x86)/Dead Frontier/DeadFrontier.exe
const defaultGameProcess = "DeadFrontier.exe"

// DefaultProcess is the executable name used when none is configured.
const DefaultProcess = defaultGameProcess

// GameState remains as a compatibility alias for callers of the game service.
type GameState = model.GameState

// baseName splits on both separators: argv[0] for a Proton process is often a
// Windows-style path (C:\Program Files\...\DeadFrontier.exe), which
// filepath.Base would not split on Linux. Keeping this platform-neutral also
// makes diagnostics consistent with native Windows process names.
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// processScanner is implemented with /proc on Linux and Toolhelp/process-time
// APIs on Windows.
type processScanner interface {
	scan() (GameState, error)
}

// GameWatcher publishes GameState changes. Read the current value with State()
// or subscribe with OnChange; both are safe from any goroutine.
type GameWatcher struct {
	scanner  processScanner
	interval time.Duration

	mu       sync.RWMutex
	state    GameState
	onChange func(GameState)

	// rescan is the fast path: Hyprland window events push to it so detection
	// does not wait out the ticker.
	rescan chan struct{}
}

func newGameWatcher(exeName string, interval time.Duration) *GameWatcher {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &GameWatcher{
		scanner:  newProcessScanner(exeName),
		interval: interval,
		rescan:   make(chan struct{}, 1),
	}
}

// NewWatcher creates a platform process watcher.
func NewWatcher(exeName string, interval time.Duration) *GameWatcher {
	return newGameWatcher(exeName, interval)
}

// Scan performs one authoritative platform process scan.
func Scan(exeName string) (GameState, error) { return newProcessScanner(exeName).scan() }

// ScanDescription describes the matching rule used by Scan.
func ScanDescription(exeName string) string { return processScanDescription(exeName) }

// ScanError formats a platform-specific process scan failure.
func ScanError(err error) string { return processScanError(err) }

// SimilarProcesses returns diagnostic candidates for a differently named game
// executable.
func SimilarProcesses(needle string) []string { return similarProcesses(needle) }

func (w *GameWatcher) State() GameState {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.state
}

// Scan performs one scan with this watcher's configured scanner.
func (w *GameWatcher) Scan() (GameState, error) { return w.scanner.scan() }

// SetStateForTesting seeds watcher state without a platform scan.
func (w *GameWatcher) SetStateForTesting(state GameState) {
	w.mu.Lock()
	w.state = state
	w.mu.Unlock()
}

// SetOnChange registers the change callback. Called with the new state whenever
// the game starts, stops, or is relaunched - never on an unchanged scan.
func (w *GameWatcher) SetOnChange(fn func(GameState)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onChange = fn
}

// Poke requests an immediate rescan, coalescing multiple requests.
func (w *GameWatcher) Poke() {
	select {
	case w.rescan <- struct{}{}:
	default:
	}
}

// Run scans until ctx is done. It scans once immediately, so State() is
// meaningful as soon as Run has started rather than one interval later.
func (w *GameWatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	w.scanOnce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.scanOnce()
		case <-w.rescan:
			w.scanOnce()
		}
	}
}

func (w *GameWatcher) scanOnce() {
	next, err := w.scanner.scan()
	if err != nil {
		// A process API error is not worth spamming about; treat it as "no
		// information" and keep the previous state rather than declaring the
		// game closed on a transient failure.
		return
	}
	w.mu.Lock()
	prev, changed := w.state, false
	// The session comparison only applies when both sides are running.
	// SameSession requires it by definition, so !SameSession is trivially true
	// while the game is closed - and reporting a change on every scan of a
	// steady "not running" state made the poller wake constantly, which pulled
	// the idle cadence down to the minimum request gap. Found live: 5s polling
	// against a configured 30s.
	if next.Running != prev.Running || (next.Running && !next.SameSession(prev)) {
		w.state, changed = next, true
	}
	fn := w.onChange
	w.mu.Unlock()

	if !changed {
		return
	}
	switch {
	case next.Running && !prev.Running:
		log.Printf("game: DeadFrontier.exe running (pid %d, started %s)",
			next.PID, next.StartedAt.Format(time.TimeOnly))
	case !next.Running && prev.Running:
		log.Printf("game: closed after %s", prev.Elapsed(time.Now()).Round(time.Second))
	case next.Running:
		log.Printf("game: relaunched (pid %d)", next.PID)
	}
	if fn != nil {
		fn(next)
	}
}
