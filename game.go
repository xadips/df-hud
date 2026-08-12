package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Game detection, for two jobs: gating the poller (only_when_game_running, and
// active vs idle cadence) and driving the session clock.
//
// /proc is the source of truth, not the compositor. Three reasons:
//
//  1. The session clock needs the moment the game LAUNCHED, not the moment
//     df-hud noticed it. /proc/<pid>/stat carries the process start time, so
//     the reading is correct even if df-hud starts an hour into a session or is
//     restarted mid-run. No compositor event can tell you that.
//  2. A window is not the same thing as a running game: Proton games spend a
//     while with no mapped window at startup, and window classes for Steam
//     titles vary (the exe name, steam_app_<id>, or something else under
//     gamescope). Matching on a class would be guessing.
//  3. It keeps df-hud working on any compositor. Hyprland is then a pure
//     latency optimisation, not a dependency.
//
// So Hyprland's event stream is subscribed to only as a "something changed,
// rescan now" hint: any window open or close triggers an immediate scan, which
// makes detection near-instant instead of up to one scan interval late. If the
// socket is missing or the compositor is something else, the ticker alone still
// does the job.

// defaultGameProcess is the Proton/Steam client's executable name, verified
// against the local install:
// steamapps/compatdata/3818286641/pfx/drive_c/Program Files (x86)/Dead Frontier/DeadFrontier.exe
const defaultGameProcess = "DeadFrontier.exe"

// userHZ is the kernel's clock-tick rate, which /proc/<pid>/stat reports the
// process start time in. It is 100 on every mainstream Linux build (confirmed
// here with getconf CLK_TCK) and there is no way to read it without cgo, so it
// is a constant with this note attached rather than a silent assumption.
const userHZ = 100

// GameState is what the rest of df-hud consumes.
type GameState struct {
	Running bool
	PID     int
	// StartedAt is the process start time, so elapsed session time is
	// independent of when df-hud itself started.
	StartedAt time.Time
}

// Elapsed is the session clock: time since the game launched, zero when it is
// not running.
func (g GameState) Elapsed(now time.Time) time.Duration {
	if !g.Running || g.StartedAt.IsZero() {
		return 0
	}
	if d := now.Sub(g.StartedAt); d > 0 {
		return d
	}
	return 0
}

// SameSession reports whether two observations are of the same game process. A
// different PID or a different start time means a relaunch, which resets the
// clock. Comparing the start time as well as the PID matters because PIDs are
// recycled.
func (g GameState) SameSession(other GameState) bool {
	return g.Running && other.Running && g.PID == other.PID && g.StartedAt.Equal(other.StartedAt)
}

// procScanner finds the game in /proc. procRoot is a field so tests can point
// it at a fake tree instead of needing a real game running.
type procScanner struct {
	procRoot string
	exeName  string
	selfPID  int
}

func newProcScanner(exeName string) *procScanner {
	if exeName == "" {
		exeName = defaultGameProcess
	}
	return &procScanner{procRoot: "/proc", exeName: exeName, selfPID: os.Getpid()}
}

// scan returns the oldest process whose argv[0] basename matches the game
// executable.
//
// Matching argv[0] only, rather than searching the whole command line, is
// deliberate: a command line containing "deadfrontier" for any other reason -
// a grep, an editor, a log tail, df-hud's own arguments - would otherwise be
// detected as the game and start a phantom session clock.
//
// Oldest wins because Proton and Unity both spawn helper processes that share
// the executable name; the first one to start is the session.
func (s *procScanner) scan() (GameState, error) {
	entries, err := os.ReadDir(s.procRoot)
	if err != nil {
		return GameState{}, err
	}
	boot, err := bootTime(s.procRoot)
	if err != nil {
		return GameState{}, err
	}

	var best GameState
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == s.selfPID {
			continue
		}
		argv0, err := processArgv0(s.procRoot, pid)
		if err != nil || argv0 == "" {
			continue // the process exited between ReadDir and now; normal
		}
		if !strings.EqualFold(baseName(argv0), s.exeName) {
			continue
		}
		started, err := processStartTime(s.procRoot, pid, boot)
		if err != nil {
			continue
		}
		if !best.Running || started.Before(best.StartedAt) {
			best = GameState{Running: true, PID: pid, StartedAt: started}
		}
	}
	return best, nil
}

// baseName splits on both separators: argv[0] for a Proton process is often a
// Windows-style path (C:\Program Files\...\DeadFrontier.exe), which
// filepath.Base would not split on Linux.
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

func processArgv0(procRoot string, pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return "", err
	}
	// cmdline is NUL-separated; argv[0] is everything up to the first NUL.
	argv0, _, _ := strings.Cut(string(data), "\x00")
	return argv0, nil
}

// processStartTime converts field 22 of /proc/<pid>/stat (start time in clock
// ticks since boot) into a wall-clock instant.
func processStartTime(procRoot string, pid int, boot time.Time) (time.Time, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return time.Time{}, err
	}
	// Field 2 is the executable name in parentheses and may itself contain
	// spaces and parentheses, so everything before the LAST ')' has to go.
	// Splitting on whitespace from the start is the classic way to get this
	// wrong for a process called "(evil) (name)".
	close := strings.LastIndex(string(data), ")")
	if close < 0 || close+2 >= len(data) {
		return time.Time{}, fmt.Errorf("proc: %d: malformed stat line", pid)
	}
	fields := strings.Fields(string(data[close+2:]))
	// The remaining fields begin at field 3 (state), so starttime (field 22)
	// sits at index 19.
	const startTimeIndex = 19
	if len(fields) <= startTimeIndex {
		return time.Time{}, fmt.Errorf("proc: %d: stat has %d fields after comm, want more than %d",
			pid, len(fields), startTimeIndex)
	}
	ticks, err := strconv.ParseInt(fields[startTimeIndex], 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("proc: %d: start time %q: %w", pid, fields[startTimeIndex], err)
	}
	return boot.Add(time.Duration(ticks) * time.Second / userHZ), nil
}

// bootTime reads btime from /proc/stat, the unix time the kernel booted.
func bootTime(procRoot string) (time.Time, error) {
	f, err := os.Open(filepath.Join(procRoot, "stat"))
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "btime ") {
			continue
		}
		secs, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("proc: btime %q: %w", line, err)
		}
		return time.Unix(secs, 0), nil
	}
	if err := scanner.Err(); err != nil {
		return time.Time{}, err
	}
	return time.Time{}, errors.New("proc: /proc/stat has no btime line")
}

// GameWatcher publishes GameState changes. Read the current value with State()
// or subscribe with OnChange; both are safe from any goroutine.
type GameWatcher struct {
	scanner  *procScanner
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
		scanner:  newProcScanner(exeName),
		interval: interval,
		rescan:   make(chan struct{}, 1),
	}
}

func (w *GameWatcher) State() GameState {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.state
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
		// A /proc read error is not worth spamming about; treat it as "no
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

// hyprEventSocket locates Hyprland's event stream. Hyprland moved this from
// /tmp/hypr to $XDG_RUNTIME_DIR/hypr, so both are checked - the runtime dir
// first, since that is where current versions put it.
func hyprEventSocket() (string, error) {
	sig := os.Getenv("HYPRLAND_INSTANCE_SIGNATURE")
	if sig == "" {
		return "", errors.New("HYPRLAND_INSTANCE_SIGNATURE is unset (not running under Hyprland)")
	}
	candidates := []string{}
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		candidates = append(candidates, filepath.Join(runtimeDir, "hypr", sig, ".socket2.sock"))
	}
	candidates = append(candidates, filepath.Join("/tmp", "hypr", sig, ".socket2.sock"))
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no Hyprland event socket found (looked in %s)", strings.Join(candidates, ", "))
}

// watchHyprWindowEvents pokes the watcher whenever a window opens or closes, so
// the game is noticed immediately rather than on the next tick. It deliberately
// does NOT try to identify the game by window class: /proc decides, this is
// only a hint. Failure is logged once and then ignored - Hyprland is optional.
func watchHyprWindowEvents(ctx context.Context, poke func()) {
	path, err := hyprEventSocket()
	if err != nil {
		log.Printf("game: no Hyprland event stream (%v); falling back to periodic scans", err)
		return
	}

	backoff := time.Second
	for ctx.Err() == nil {
		if err := streamHyprEvents(ctx, path, poke); err != nil && ctx.Err() == nil {
			log.Printf("game: Hyprland event stream ended (%v); retrying in %s", err, backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func streamHyprEvents(ctx context.Context, path string, poke func()) error {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		conn.Close() // unblocks the scanner below on shutdown
	}()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		// Lines are EVENT>>payload. Only the window lifecycle matters, and only
		// as a trigger, so the payload is never parsed.
		name, _, ok := strings.Cut(scanner.Text(), ">>")
		if !ok {
			continue
		}
		switch name {
		case "openwindow", "closewindow", "fullscreen":
			poke()
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("socket closed")
}
