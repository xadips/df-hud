package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeProc builds a /proc tree on disk. Every game-detection rule can then be
// tested without a running game, and the awkward cases (a process named
// "(evil) (name)", a command line that merely mentions the game) can be
// constructed on purpose rather than waited for.
type fakeProc struct {
	root  string
	btime int64
}

func newFakeProc(t *testing.T) *fakeProc {
	t.Helper()
	p := &fakeProc{root: t.TempDir(), btime: 1786429751}
	statLine := fmt.Sprintf("cpu  1 2 3\nbtime %d\nprocesses 4242\n", p.btime)
	if err := os.WriteFile(filepath.Join(p.root, "stat"), []byte(statLine), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// addProcess writes a process entry. comm is the name in parentheses in
// stat, argv0 is the first command-line element, and startTicks is the
// process start time in clock ticks since boot.
func (p *fakeProc) addProcess(t *testing.T, pid int, comm, argv0 string, startTicks int64) {
	t.Helper()
	dir := filepath.Join(p.root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// cmdline is NUL-separated and NUL-terminated.
	cmdline := argv0 + "\x00--some-flag\x00"
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644); err != nil {
		t.Fatal(err)
	}
	// Fields 1 and 2, then fields 3..22 where 22 is starttime. Everything
	// between is filler, which is exactly how the real file reads.
	fields := make([]string, 0, 24)
	fields = append(fields, strconv.Itoa(pid), "("+comm+")")
	for i := 3; i <= 21; i++ {
		fields = append(fields, "0")
	}
	fields = append(fields, strconv.FormatInt(startTicks, 10), "trailing", "fields")
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(strings.Join(fields, " ")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (p *fakeProc) scanner(exe string) *procScanner {
	return &procScanner{procRoot: p.root, exeName: exe, selfPID: 999999}
}

// startedAt is when a process with the given start ticks actually launched.
func (p *fakeProc) startedAt(ticks int64) time.Time {
	return time.Unix(p.btime, 0).Add(time.Duration(ticks) * time.Second / userHZ)
}

func TestProcScanFindsTheGame(t *testing.T) {
	p := newFakeProc(t)
	p.addProcess(t, 100, "systemd", "/usr/lib/systemd/systemd", 50)
	p.addProcess(t, 200, "DeadFrontier.e", `Z:\home\player\Program Files\DeadFrontier.exe`, 360_000)

	got, err := p.scanner("DeadFrontier.exe").scan()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Running || got.PID != 200 {
		t.Fatalf("scan = %+v, want the game at pid 200", got)
	}
	// The whole point: the start time comes from the kernel, not from now.
	want := p.startedAt(360_000)
	if !got.StartedAt.Equal(want) {
		t.Errorf("StartedAt = %s, want %s (btime + ticks/USER_HZ)", got.StartedAt, want)
	}
}

// A Windows-style argv[0] must still resolve, since filepath.Base does not
// split on backslashes when running on Linux.
func TestProcScanHandlesWindowsPaths(t *testing.T) {
	for _, argv0 := range []string{
		`C:\Program Files (x86)\Dead Frontier\DeadFrontier.exe`,
		`Z:\home\player\games\DeadFrontier.exe`,
		"/home/player/.steam/.../Dead Frontier/DeadFrontier.exe",
		"DeadFrontier.exe",
		"deadfrontier.exe", // case differs between Proton and the filesystem
	} {
		p := newFakeProc(t)
		p.addProcess(t, 300, "DeadFrontier.e", argv0, 1000)
		got, err := p.scanner("DeadFrontier.exe").scan()
		if err != nil {
			t.Fatal(err)
		}
		if !got.Running {
			t.Errorf("argv0 %q was not detected", argv0)
		}
	}
}

// TestProcScanIgnoresMentionsInOtherCommandLines is the false-positive guard.
// Matching anywhere in the command line would make a grep, an editor or df-hud's
// own arguments look like a running game and start a phantom session clock.
func TestProcScanIgnoresMentionsInOtherCommandLines(t *testing.T) {
	p := newFakeProc(t)
	p.addProcess(t, 400, "grep", "/usr/bin/grep", 100)
	p.addProcess(t, 401, "nvim", "/usr/bin/nvim", 200)
	p.addProcess(t, 402, "df-hud", "/home/player/Programming/df-hud/df-hud", 300)
	// A command line that mentions the exe as an ARGUMENT, not as argv[0].
	dir := filepath.Join(p.root, "403")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"),
		[]byte("/usr/bin/grep\x00DeadFrontier.exe\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"),
		[]byte("403 (grep) "+strings.Repeat("0 ", 19)+"5000 x y\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := p.scanner("DeadFrontier.exe").scan()
	if err != nil {
		t.Fatal(err)
	}
	if got.Running {
		t.Errorf("scan = %+v, but nothing here is the game", got)
	}
}

// Proton and Unity spawn helpers sharing the executable name; the session is the
// one that started first.
func TestProcScanPrefersTheOldestMatch(t *testing.T) {
	p := newFakeProc(t)
	p.addProcess(t, 500, "DeadFrontier.e", "/games/DeadFrontier.exe", 900_000) // child
	p.addProcess(t, 501, "DeadFrontier.e", "/games/DeadFrontier.exe", 360_000) // the real one
	p.addProcess(t, 502, "DeadFrontier.e", "/games/DeadFrontier.exe", 500_000)

	got, err := p.scanner("DeadFrontier.exe").scan()
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != 501 {
		t.Errorf("PID = %d, want 501 (the earliest start time)", got.PID)
	}
}

func TestProcScanSkipsSelf(t *testing.T) {
	p := newFakeProc(t)
	p.addProcess(t, 600, "DeadFrontier.e", "/games/DeadFrontier.exe", 1000)
	s := p.scanner("DeadFrontier.exe")
	s.selfPID = 600
	got, err := s.scan()
	if err != nil {
		t.Fatal(err)
	}
	if got.Running {
		t.Error("df-hud must never detect itself as the game")
	}
}

// A process whose comm contains spaces and parentheses breaks any parser that
// splits /proc/<pid>/stat on whitespace from the left.
func TestProcessStartTimeSurvivesAWeirdProcessName(t *testing.T) {
	p := newFakeProc(t)
	p.addProcess(t, 700, "evil) (name with spaces", "/games/DeadFrontier.exe", 12_345)

	got, err := p.scanner("DeadFrontier.exe").scan()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Running {
		t.Fatal("the process should still be found")
	}
	if want := p.startedAt(12_345); !got.StartedAt.Equal(want) {
		t.Errorf("StartedAt = %s, want %s: the comm field was mis-parsed", got.StartedAt, want)
	}
}

func TestProcScanNoGameRunning(t *testing.T) {
	p := newFakeProc(t)
	p.addProcess(t, 800, "firefox", "/usr/lib/firefox/firefox", 100)
	got, err := p.scanner("DeadFrontier.exe").scan()
	if err != nil {
		t.Fatal(err)
	}
	if got.Running || got.PID != 0 || !got.StartedAt.IsZero() {
		t.Errorf("scan = %+v, want the zero value", got)
	}
}

func TestBootTimeErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := bootTime(dir); err == nil {
		t.Error("a missing /proc/stat must be an error")
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte("cpu 1 2 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := bootTime(dir); err == nil {
		t.Error("a /proc/stat with no btime line must be an error")
	}
}

// The real /proc must parse, since a fake tree could agree with a wrong parser.
func TestBootTimeAgainstTheRealProc(t *testing.T) {
	boot, err := bootTime("/proc")
	if err != nil {
		t.Skipf("no usable /proc here: %v", err)
	}
	if boot.After(time.Now()) || time.Since(boot) > 365*24*time.Hour {
		t.Errorf("boot time %s is not plausible", boot)
	}
	// This process must appear to have started after boot and not in the future.
	started, err := processStartTime("/proc", os.Getpid(), boot)
	if err != nil {
		t.Fatal(err)
	}
	if started.Before(boot) || started.After(time.Now().Add(time.Second)) {
		t.Errorf("this test process appears to have started at %s, outside boot..now", started)
	}
}

func TestGameStateElapsedAndSession(t *testing.T) {
	now := time.Now()
	running := GameState{Running: true, PID: 42, StartedAt: now.Add(-90 * time.Minute)}

	if got := running.Elapsed(now); got != 90*time.Minute {
		t.Errorf("Elapsed = %s, want 1h30m", got)
	}
	// Not running means no clock, whatever the stored start time.
	stopped := running
	stopped.Running = false
	if got := stopped.Elapsed(now); got != 0 {
		t.Errorf("Elapsed with the game closed = %s, want 0", got)
	}
	// A clock jump backwards must not produce a negative duration.
	if got := running.Elapsed(running.StartedAt.Add(-time.Hour)); got != 0 {
		t.Errorf("Elapsed before the start time = %s, want 0", got)
	}

	if !running.SameSession(running) {
		t.Error("a state must be the same session as itself")
	}
	// PIDs get recycled, so the start time is part of the identity.
	recycled := GameState{Running: true, PID: 42, StartedAt: now.Add(-time.Minute)}
	if running.SameSession(recycled) {
		t.Error("the same PID with a different start time is a different session")
	}
	if running.SameSession(stopped) {
		t.Error("a stopped game is not the same session as a running one")
	}
}

func TestGameWatcherReportsChangesOnce(t *testing.T) {
	p := newFakeProc(t)
	p.addProcess(t, 900, "DeadFrontier.e", "/games/DeadFrontier.exe", 360_000)

	w := newGameWatcher("DeadFrontier.exe", 10*time.Millisecond)
	w.scanner = p.scanner("DeadFrontier.exe")
	changes := make(chan GameState, 8)
	w.SetOnChange(func(s GameState) { changes <- s })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// Detected once...
	select {
	case got := <-changes:
		if !got.Running || got.PID != 900 {
			t.Fatalf("first change = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the game was never detected")
	}
	// ...and not again while nothing changes, even though scans keep happening.
	select {
	case got := <-changes:
		t.Errorf("an unchanged scan fired a change: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}

	// Closing the game is a change.
	if err := os.RemoveAll(filepath.Join(p.root, "900")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-changes:
		if got.Running {
			t.Errorf("expected the game to be reported closed, got %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the game closing was not reported")
	}
	if state := w.State(); state.Running {
		t.Errorf("State() = %+v, want not running", state)
	}
}

// A relaunch is a new session even though the game is running both before and
// after, because the clock has to restart.
func TestGameWatcherDetectsRelaunch(t *testing.T) {
	p := newFakeProc(t)
	p.addProcess(t, 910, "DeadFrontier.e", "/games/DeadFrontier.exe", 100_000)

	w := newGameWatcher("DeadFrontier.exe", time.Hour) // ticker never fires; Poke drives it
	w.scanner = p.scanner("DeadFrontier.exe")
	changes := make(chan GameState, 8)
	w.SetOnChange(func(s GameState) { changes <- s })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	first := <-changes
	if first.PID != 910 {
		t.Fatalf("first = %+v", first)
	}

	if err := os.RemoveAll(filepath.Join(p.root, "910")); err != nil {
		t.Fatal(err)
	}
	p.addProcess(t, 911, "DeadFrontier.e", "/games/DeadFrontier.exe", 500_000)
	w.Poke()

	select {
	case got := <-changes:
		if !got.Running || got.PID != 911 {
			t.Errorf("relaunch = %+v, want pid 911 running", got)
		}
		if got.StartedAt.Equal(first.StartedAt) {
			t.Error("the relaunched session must carry a new start time")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the relaunch was not reported")
	}
}

// Poke coalesces, so a burst of compositor events cannot queue up a backlog of
// scans.
func TestGameWatcherPokeCoalesces(t *testing.T) {
	w := newGameWatcher("DeadFrontier.exe", time.Hour)
	for i := 0; i < 100; i++ {
		w.Poke()
	}
	if got := len(w.rescan); got != 1 {
		t.Errorf("queued rescans = %d, want 1", got)
	}
}

func TestHyprEventSocketNeedsTheSignature(t *testing.T) {
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	if _, err := hyprEventSocket(); err == nil {
		t.Error("without HYPRLAND_INSTANCE_SIGNATURE this must fail, not guess a path")
	}
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "nonexistent-signature")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if _, err := hyprEventSocket(); err == nil {
		t.Error("a signature with no socket on disk must fail")
	}
}
