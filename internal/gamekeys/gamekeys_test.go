package gamekeys

import (
	"context"
	"df-hud/internal/config"
	"df-hud/internal/desktop"
	"df-hud/internal/model"
	"errors"
	"testing"
	"time"
)

// fakeSender records what would have been sent to the compositor.
type fakeSender struct {
	sent []struct{ key, address string }
	err  error
}

func (f *fakeSender) SendKey(_ context.Context, key, address string) error {
	f.sent = append(f.sent, struct{ key, address string }{key, address})
	return f.err
}

// keysHarness wires a Keys against values a test can move.
type keysHarness struct {
	cfg   *config.Config
	game  model.GameState
	place desktop.Placement
	send  *fakeSender
	keys  *Keys
}

func newKeysHarness(t *testing.T) *keysHarness {
	t.Helper()
	cfg := config.Default()
	cfg.GameKeys.FPSDisplay = true
	cfg.GameKeys.FPSKey = "y"
	cfg.GameKeys.FPSDelay = config.Duration{Duration: 5 * time.Second}

	h := &keysHarness{cfg: cfg, send: &fakeSender{}}
	h.keys = New(
		func() *config.Config { return h.cfg },
		func() model.GameState { return h.game },
		func() desktop.Placement { return h.place },
		h.send,
	)
	return h
}

// running puts a game and its mapped window in place.
func (h *keysHarness) running(pid int, address string) {
	h.game = model.GameState{Running: true, PID: pid}
	h.place = desktop.Placement{Known: true, Address: address}
}

func (h *keysHarness) tick(now time.Time) { h.keys.Tick(context.Background(), now) }

func TestFPSKeyIsSentOnceTheWindowHasBeenUpForTheDelay(t *testing.T) {
	h := newKeysHarness(t)
	start := time.Unix(1000, 0)
	h.running(42, "0xabc")

	h.tick(start)
	if len(h.send.sent) != 0 {
		t.Fatalf("sent immediately; the delay should have held it: %v", h.send.sent)
	}
	h.tick(start.Add(4 * time.Second))
	if len(h.send.sent) != 0 {
		t.Fatalf("sent before the delay elapsed: %v", h.send.sent)
	}

	h.tick(start.Add(5 * time.Second))
	if len(h.send.sent) != 1 {
		t.Fatalf("want one send at the delay, got %v", h.send.sent)
	}
	if got := h.send.sent[0]; got.key != "y" || got.address != "0xabc" {
		t.Fatalf("sent %+v, want key y to 0xabc", got)
	}
}

func TestFPSKeyIsSentOnlyOncePerLaunch(t *testing.T) {
	h := newKeysHarness(t)
	start := time.Unix(1000, 0)
	h.running(42, "0xabc")
	h.tick(start)

	for i := 0; i < 30; i++ {
		h.tick(start.Add(time.Duration(5+i) * time.Second))
	}
	if len(h.send.sent) != 1 {
		t.Fatalf("want exactly one send across a whole session, got %d", len(h.send.sent))
	}
}

// The launcher is the same process with the same class, so the ONLY thing that
// separates it from the game is that findGameWindow refuses to match it - which
// reaches here as Known false. Sending the key then would type into a settings
// dialog.
func TestFPSKeyWaitsOutTheLauncher(t *testing.T) {
	h := newKeysHarness(t)
	start := time.Unix(1000, 0)

	// The process is up, but every window carrying its class was ignored by title.
	h.game = model.GameState{Running: true, PID: 42}
	h.place = desktop.Placement{LauncherOnly: true}
	for i := 0; i < 60; i++ {
		h.tick(start.Add(time.Duration(i) * time.Second))
	}
	if len(h.send.sent) != 0 {
		t.Fatalf("sent while only the launcher was open: %v", h.send.sent)
	}

	// The game's own window finally maps, a minute in. The delay must be measured
	// from HERE, not from when the process started.
	windowUp := start.Add(60 * time.Second)
	h.running(42, "0xabc")
	h.tick(windowUp)
	if len(h.send.sent) != 0 {
		t.Fatalf("sent the instant the window appeared, ignoring the delay: %v", h.send.sent)
	}
	h.tick(windowUp.Add(5 * time.Second))
	if len(h.send.sent) != 1 {
		t.Fatalf("want one send five seconds after the window appeared, got %v", h.send.sent)
	}
}

func TestFPSKeyReArmsForTheNextLaunch(t *testing.T) {
	h := newKeysHarness(t)
	start := time.Unix(1000, 0)
	h.running(42, "0xabc")
	h.tick(start)
	h.tick(start.Add(5 * time.Second))

	// The game closes.
	h.game = model.GameState{}
	h.place = desktop.Placement{}
	h.tick(start.Add(10 * time.Second))

	// And comes back as a new process.
	second := start.Add(20 * time.Second)
	h.running(99, "0xdef")
	h.tick(second)
	h.tick(second.Add(5 * time.Second))

	if len(h.send.sent) != 2 {
		t.Fatalf("want a send per launch, got %v", h.send.sent)
	}
	if h.send.sent[1].address != "0xdef" {
		t.Fatalf("second send went to %q, want the new window 0xdef", h.send.sent[1].address)
	}
}

// A new process id with no gap: df-hud may not see a tick while the game is
// down, so the pid change alone has to re-arm it.
func TestFPSKeyReArmsWhenThePIDChangesWithoutAGap(t *testing.T) {
	h := newKeysHarness(t)
	start := time.Unix(1000, 0)
	h.running(42, "0xabc")
	h.tick(start)
	h.tick(start.Add(5 * time.Second))

	second := start.Add(6 * time.Second)
	h.running(99, "0xdef")
	h.tick(second)
	h.tick(second.Add(5 * time.Second))

	if len(h.send.sent) != 2 {
		t.Fatalf("want a send for the new process, got %v", h.send.sent)
	}
}

func TestFPSKeySendsNothingWhenOff(t *testing.T) {
	h := newKeysHarness(t)
	h.keys.SetFPSDisplay(false)
	start := time.Unix(1000, 0)
	h.running(42, "0xabc")
	for i := 0; i < 20; i++ {
		h.tick(start.Add(time.Duration(i) * time.Second))
	}
	if len(h.send.sent) != 0 {
		t.Fatalf("sent with the toggle off: %v", h.send.sent)
	}
}

// Ticking the box mid-session is asking for the readout NOW, so it fires without
// waiting for a relaunch. Ticking it twice must not fire twice: the game's key is
// a toggle, so a second send would turn the readout back off.
func TestFPSKeyFiresWhenTurnedOnMidSessionButNotTwice(t *testing.T) {
	h := newKeysHarness(t)
	h.keys.SetFPSDisplay(false)
	start := time.Unix(1000, 0)
	h.running(42, "0xabc")
	h.tick(start)
	h.tick(start.Add(30 * time.Second))
	if len(h.send.sent) != 0 {
		t.Fatalf("sent while off: %v", h.send.sent)
	}

	h.keys.SetFPSDisplay(true)
	h.tick(start.Add(31 * time.Second))
	if len(h.send.sent) != 1 {
		t.Fatalf("want one send when switched on mid-session, got %v", h.send.sent)
	}

	h.keys.SetFPSDisplay(false)
	h.tick(start.Add(32 * time.Second))
	h.keys.SetFPSDisplay(true)
	for i := 0; i < 10; i++ {
		h.tick(start.Add(time.Duration(33+i) * time.Second))
	}
	if len(h.send.sent) != 1 {
		t.Fatalf("off-and-on again sent a second time, which would toggle the readout back off: %v",
			h.send.sent)
	}
}

// A refusing compositor must not turn into a keypress every second aimed at the
// game.
func TestFPSKeyDoesNotRetryAFailedSend(t *testing.T) {
	h := newKeysHarness(t)
	h.send.err = errors.New("window not found")
	start := time.Unix(1000, 0)
	h.running(42, "0xabc")
	h.tick(start)
	for i := 0; i < 20; i++ {
		h.tick(start.Add(time.Duration(5+i) * time.Second))
	}
	if len(h.send.sent) != 1 {
		t.Fatalf("want one attempt after a failure, got %d", len(h.send.sent))
	}
}

func TestFPSKeyHonoursAReloadedDelay(t *testing.T) {
	h := newKeysHarness(t)
	h.cfg.GameKeys.FPSDelay = config.Duration{Duration: time.Minute}
	start := time.Unix(1000, 0)
	h.running(42, "0xabc")
	h.tick(start)
	h.tick(start.Add(30 * time.Second))
	if len(h.send.sent) != 0 {
		t.Fatalf("ignored the longer delay: %v", h.send.sent)
	}
	h.tick(start.Add(time.Minute))
	if len(h.send.sent) != 1 {
		t.Fatalf("want one send at the configured minute, got %v", h.send.sent)
	}
}

func TestConfigRejectsAnFPSKeyThatIsNotAKeyName(t *testing.T) {
	cfg := config.Default()
	cfg.GameKeys.FPSDisplay = true
	cfg.GameKeys.FPSKey = `y"}--`
	if err := cfg.Validate(); err == nil {
		t.Fatal("accepted an fps_key that cannot be a keysym")
	}
}

// Off is the shipped default, because synthesising input into the game is worth
// opting into rather than discovering.
func TestFPSDisplayIsOffByDefault(t *testing.T) {
	cfg := config.Default()
	if cfg.GameKeys.FPSDisplay {
		t.Fatal("fps_display ships on; it synthesises input and should be opt-in")
	}
	if cfg.GameKeys.FPSKey != "y" {
		t.Fatalf("default fps_key is %q, want the game's own binding y", cfg.GameKeys.FPSKey)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the defaults do not validate: %v", err)
	}
}

// An empty key is only an error when the feature is on: an unused key nobody set
// should not stop df-hud starting.
func TestConfigAllowsAnEmptyFPSKeyWhileTheFeatureIsOff(t *testing.T) {
	cfg := config.Default()
	cfg.GameKeys.FPSDisplay = false
	cfg.GameKeys.FPSKey = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("an unused fps_key blocked startup: %v", err)
	}
}

// launcherUp puts only the configuration dialog on screen: the process is up,
// but every window carrying its class was ignored by title.
func (h *keysHarness) launcherUp(pid int, address string) {
	h.game = model.GameState{Running: true, PID: pid}
	h.place = desktop.Placement{LauncherOnly: true, LauncherAddress: address}
}

func TestTheLauncherIsDismissedOnceItHasBeenUpForTheDelay(t *testing.T) {
	h := newKeysHarness(t)
	h.keys.SetDismissLauncher(true)
	start := time.Unix(1000, 0)
	h.launcherUp(42, "0x111")

	h.tick(start)
	if len(h.send.sent) != 0 {
		t.Fatalf("pressed before the dialog had settled: %v", h.send.sent)
	}
	h.tick(start.Add(time.Second))
	if len(h.send.sent) != 1 {
		t.Fatalf("want one press at launcher_delay, got %v", h.send.sent)
	}
	if got := h.send.sent[0]; got.key != "Return" || got.address != "0x111" {
		t.Fatalf("sent %+v, want Return to the dialog at 0x111", got)
	}
}

func TestTheLauncherIsLeftAloneWhenTheOptionIsOff(t *testing.T) {
	h := newKeysHarness(t)
	h.keys.SetDismissLauncher(false)
	start := time.Unix(1000, 0)
	h.launcherUp(42, "0x111")
	for i := 0; i < 30; i++ {
		h.tick(start.Add(time.Duration(i) * time.Second))
	}
	if len(h.send.sent) != 0 {
		t.Fatalf("pressed Play without being asked to: %v", h.send.sent)
	}
}

// Only the dialog with the Play button on it has an address. The 215x78 "Input
// Configuration" key-capture box is LauncherOnly too, and Return there means
// something else entirely.
func TestNothingIsPressedWithoutTheDialogsAddress(t *testing.T) {
	h := newKeysHarness(t)
	h.keys.SetDismissLauncher(true)
	start := time.Unix(1000, 0)
	h.game = model.GameState{Running: true, PID: 42}
	h.place = desktop.Placement{LauncherOnly: true}
	for i := 0; i < 10; i++ {
		h.tick(start.Add(time.Duration(i) * time.Second))
	}
	if len(h.send.sent) != 0 {
		t.Fatalf("pressed a key at a window it could not name: %v", h.send.sent)
	}
}

func TestTheLauncherIsPressedOnlyOnce(t *testing.T) {
	h := newKeysHarness(t)
	h.keys.SetDismissLauncher(true)
	start := time.Unix(1000, 0)
	h.launcherUp(42, "0x111")
	for i := 0; i < 20; i++ {
		h.tick(start.Add(time.Duration(i) * time.Second))
	}
	if len(h.send.sent) != 1 {
		t.Fatalf("want one press however long the dialog stays up, got %d", len(h.send.sent))
	}
}

// The two one-shots share a process id but not a clock. Dismissing the launcher
// claims the pid before the game window exists, and the FPS delay must still be
// measured from the moment that window appears.
func TestTheFPSDelayStillRunsFromTheWindowAfterDismissingTheLauncher(t *testing.T) {
	h := newKeysHarness(t)
	h.keys.SetDismissLauncher(true)
	start := time.Unix(1000, 0)

	h.launcherUp(42, "0x111")
	h.tick(start)
	h.tick(start.Add(time.Second)) // the dialog is dismissed here
	if len(h.send.sent) != 1 {
		t.Fatalf("expected the launcher press first, got %v", h.send.sent)
	}

	// The dialog closes and the game's own window maps, same process.
	windowUp := start.Add(3 * time.Second)
	h.running(42, "0xabc")
	h.tick(windowUp)
	if len(h.send.sent) != 1 {
		t.Fatalf("the FPS key fired the instant the window appeared: %v", h.send.sent)
	}
	h.tick(windowUp.Add(4 * time.Second))
	if len(h.send.sent) != 1 {
		t.Fatalf("the FPS key fired before fps_delay elapsed: %v", h.send.sent)
	}

	h.tick(windowUp.Add(5 * time.Second))
	if len(h.send.sent) != 2 {
		t.Fatalf("want the FPS key five seconds after the window, got %v", h.send.sent)
	}
	if got := h.send.sent[1]; got.key != "y" || got.address != "0xabc" {
		t.Fatalf("second send was %+v, want y to the game window", got)
	}
}

func TestBothOneShotsReArmForTheNextLaunch(t *testing.T) {
	h := newKeysHarness(t)
	h.keys.SetDismissLauncher(true)
	start := time.Unix(1000, 0)

	h.launcherUp(42, "0x111")
	h.tick(start)
	h.tick(start.Add(time.Second))
	h.running(42, "0xabc")
	h.tick(start.Add(2 * time.Second))
	h.tick(start.Add(7 * time.Second))
	if len(h.send.sent) != 2 {
		t.Fatalf("first launch sent %v, want two", h.send.sent)
	}

	h.game = model.GameState{}
	h.place = desktop.Placement{}
	h.tick(start.Add(10 * time.Second))

	second := start.Add(20 * time.Second)
	h.launcherUp(99, "0x222")
	h.tick(second)
	h.tick(second.Add(time.Second))
	h.running(99, "0xdef")
	h.tick(second.Add(2 * time.Second))
	h.tick(second.Add(7 * time.Second))
	if len(h.send.sent) != 4 {
		t.Fatalf("second launch did not re-arm both: %v", h.send.sent)
	}
}

// The dialog has to be one of the windows the HUD refuses to draw over, or
// df-hud thinks the launcher IS the game and never sees a launcher at all.
func TestConfigRejectsALauncherTitleTheHUDWouldTreatAsTheGame(t *testing.T) {
	cfg := config.Default()
	cfg.GameKeys.DismissLauncher = true
	cfg.GameKeys.LauncherTitle = "Dead Frontier"
	if err := cfg.Validate(); err == nil {
		t.Fatal("accepted a launcher_title that game.window_title_ignore does not cover")
	}
}

func TestTheDefaultLauncherTitleIsCoveredByTheIgnoreList(t *testing.T) {
	cfg := config.Default()
	cfg.GameKeys.DismissLauncher = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the shipped defaults do not validate with dismissal on: %v", err)
	}
	if cfg.GameKeys.DismissLauncher != true {
		t.Fatal("test setup")
	}
	// And the narrower title must NOT match the key-capture box.
	m := cfg.LauncherWindowMatch()
	if !m.IsLauncherDialog("Dead Frontier Configuration") {
		t.Error("the dialog itself is not matched")
	}
	if m.IsLauncherDialog("Input Configuration") {
		t.Error("the Input Configuration key-capture box is matched, and Return there means something else")
	}
}

func TestDismissLauncherIsOffByDefault(t *testing.T) {
	if config.Default().GameKeys.DismissLauncher {
		t.Fatal("dismiss_launcher ships on; it synthesises input and should be opt-in")
	}
}

// The tray switch is the authority once df-hud is running, so the config value
// only seeds it. Worth pinning: the two got out of step during development and
// every launcher test silently stopped exercising the send.
func TestTheConfigSeedsTheTraySwitches(t *testing.T) {
	cfg := config.Default()
	cfg.GameKeys.FPSDisplay = true
	cfg.GameKeys.DismissLauncher = true
	k := New(func() *config.Config { return cfg },
		func() model.GameState { return model.GameState{} },
		func() desktop.Placement { return desktop.Placement{} },
		&fakeSender{})
	if !k.FPSDisplay() || !k.DismissLauncher() {
		t.Fatal("the tray switches did not start from the config")
	}
}

// A key sent to a window that does not have focus is the likeliest reason one
// silently does nothing - SilverOverlays calls activate() before every press for
// the same reason. Here it WAITS for focus instead of taking it, so nothing is
// claimed and the next tick tries again.
func TestFPSKeyWaitsForTheGameWindowToBeFocused(t *testing.T) {
	h := newKeysHarness(t)
	focused := "0xsomethingelse"
	h.keys.active = func(context.Context) (string, bool) { return focused, true }

	start := time.Unix(1000, 0)
	h.running(42, "0xabc")
	for i := 0; i < 20; i++ {
		h.tick(start.Add(time.Duration(i) * time.Second))
	}
	if len(h.send.sent) != 0 {
		t.Fatalf("sent to a window that was not focused: %v", h.send.sent)
	}

	focused = "0xabc"
	h.tick(start.Add(21 * time.Second))
	if len(h.send.sent) != 1 {
		t.Fatalf("want one send once the window took focus, got %v", h.send.sent)
	}
}

// A compositor that cannot answer must not disable the feature.
func TestFPSKeySendsWhenFocusCannotBeDetermined(t *testing.T) {
	h := newKeysHarness(t)
	h.keys.active = nil
	start := time.Unix(1000, 0)
	h.running(42, "0xabc")
	h.tick(start)
	h.tick(start.Add(5 * time.Second))
	if len(h.send.sent) != 1 {
		t.Fatalf("want one send with no focus source, got %v", h.send.sent)
	}
}

// A key pressed at the loading screen is discarded by the client, which is what
// made this intermittent. Waiting costs nothing; the tick comes round again.
func TestFPSKeyWaitsForTheClientToLeaveTheLoadingScreen(t *testing.T) {
	h := newKeysHarness(t)
	inWorld := false
	h.keys.ready = func() bool { return inWorld }

	start := time.Unix(1000, 0)
	h.running(42, "0xabc")
	for i := 0; i < 20; i++ {
		h.tick(start.Add(time.Duration(i) * time.Second))
	}
	if len(h.send.sent) != 0 {
		t.Fatalf("sent while the client was still loading: %v", h.send.sent)
	}

	inWorld = true
	h.tick(start.Add(21 * time.Second))
	if len(h.send.sent) != 1 {
		t.Fatalf("want one send once the client was in the world, got %v", h.send.sent)
	}
}

// The launcher gate is focus ONLY. Waiting for the client to be in the world
// there would deadlock: the dialog is what stands between here and the world.
func TestTheLauncherIsNotGatedOnTheClientBeingInTheWorld(t *testing.T) {
	h := newKeysHarness(t)
	h.keys.SetDismissLauncher(true)
	h.keys.ready = func() bool { return false }
	h.keys.active = func(context.Context) (string, bool) { return "0x111", true }

	start := time.Unix(1000, 0)
	h.launcherUp(42, "0x111")
	h.tick(start)
	h.tick(start.Add(time.Second))
	if len(h.send.sent) != 1 {
		t.Fatalf("the launcher waited on a world it cannot reach: %v", h.send.sent)
	}
}

func TestTheLauncherWaitsForItsOwnDialogToBeFocused(t *testing.T) {
	h := newKeysHarness(t)
	h.keys.SetDismissLauncher(true)
	focused := "0xelsewhere"
	h.keys.active = func(context.Context) (string, bool) { return focused, true }

	start := time.Unix(1000, 0)
	h.launcherUp(42, "0x111")
	for i := 0; i < 10; i++ {
		h.tick(start.Add(time.Duration(i) * time.Second))
	}
	if len(h.send.sent) != 0 {
		t.Fatalf("pressed Play on an unfocused dialog: %v", h.send.sent)
	}
	focused = "0x111"
	h.tick(start.Add(11 * time.Second))
	if len(h.send.sent) != 1 {
		t.Fatalf("want one press once the dialog had focus, got %v", h.send.sent)
	}
}

// Waiting must not consume the one attempt: a gate that blocks has to leave the
// key unsent, or the feature silently never fires.
func TestAGateDoesNotConsumeTheAttempt(t *testing.T) {
	h := newKeysHarness(t)
	h.keys.ready = func() bool { return false }
	start := time.Unix(1000, 0)
	h.running(42, "0xabc")
	for i := 0; i < 30; i++ {
		h.tick(start.Add(time.Duration(i) * time.Second))
	}
	h.keys.ready = func() bool { return true }
	h.tick(start.Add(31 * time.Second))
	if len(h.send.sent) != 1 {
		t.Fatalf("the attempt was consumed while gated: %v", h.send.sent)
	}
}
