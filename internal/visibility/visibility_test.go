package visibility

import (
	"context"
	"df-hud/internal/config"
	"df-hud/internal/desktop"
	"df-hud/internal/game"
	"df-hud/internal/model"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func allRules() Rules {
	return Rules{OnlyWhenGameRunning: true, FollowGameWorkspace: true, Enabled: true}
}

func TestDecideHUDVisible(t *testing.T) {
	running := model.GameState{Running: true, PID: 10, StartedAt: time.Now()}
	onScreen := desktop.Placement{Known: true, WorkspaceName: "13", OnActiveWorkspace: true, Monitor: "DP-2"}
	elsewhere := desktop.Placement{Known: true, WorkspaceName: "7", OnActiveWorkspace: false, Monitor: "DP-2"}

	if visible, reason := Decide(allRules(), running, onScreen); !visible {
		t.Errorf("playing on the shown workspace must be visible, got %q", reason)
	}

	visible, reason := Decide(allRules(), model.GameState{}, desktop.Placement{})
	if visible {
		t.Error("the game is not running, so there is nothing to draw over")
	}
	if !strings.Contains(reason, "not running") {
		t.Errorf("reason = %q, want it to name the closed game", reason)
	}

	visible, reason = Decide(allRules(), running, elsewhere)
	if visible {
		t.Error("the game's workspace is not the one being shown")
	}
	// The reason reaches a tray tooltip, so it has to name the workspace rather
	// than just say "hidden".
	if !strings.Contains(reason, "workspace 7") {
		t.Errorf("reason = %q, want the workspace named", reason)
	}

	// Windows has no workspace relationship to follow. The same configured rule
	// instead follows focus, so alt-tabbing away hides the overlay.
	foreground := elsewhere
	foreground.ForegroundRule = true
	visible, reason = Decide(allRules(), running, foreground)
	if visible || !strings.Contains(reason, "foreground") {
		t.Errorf("Windows foreground decision = %v, %q; want hidden with a focus reason", visible, reason)
	}

	// THE LAUNCHER. /proc says the game is running, because the launcher is the same
	// executable, so only the compositor can say otherwise - and when it does, the
	// answer is to hide rather than to fail open. Getting this wrong put the HUD on
	// EVERY workspace: excluding the launcher's window made the lookup unknown, and
	// unknown means "ask again later, keep showing".
	launcher := desktop.Placement{LauncherOnly: true}
	visible, reason = Decide(allRules(), running, launcher)
	if visible {
		t.Error("the launcher is not the game, so there is nothing to draw over yet")
	}
	if !strings.Contains(reason, "launcher") {
		t.Errorf("reason = %q, want it to name the launcher", reason)
	}

	// And an unknown placement that is NOT the launcher still fails open, which is
	// the case that keeps a wrongly hidden HUD from looking like a broken one.
	if visible, reason := Decide(allRules(), running, desktop.Placement{}); !visible {
		t.Errorf("an unanswerable lookup must fail open, got %q", reason)
	}
}

// Both rules can be turned off, and then neither hides anything.
func TestDecideHUDVisibleRespectsDisabledRules(t *testing.T) {
	closed := model.GameState{}
	elsewhere := desktop.Placement{Known: true, WorkspaceName: "7"}

	rules := Rules{Enabled: true}
	if visible, reason := Decide(rules, closed, elsewhere); !visible {
		t.Errorf("with both rules off the HUD is always up, got %q", reason)
	}
}

// The tray override beats everything, including a perfectly good reason to show.
func TestDecideHUDVisibleTrayOverride(t *testing.T) {
	rules := allRules()
	rules.Enabled = false
	running := model.GameState{Running: true}
	onScreen := desktop.Placement{Known: true, OnActiveWorkspace: true}

	visible, reason := Decide(rules, running, onScreen)
	if visible {
		t.Error("the tray toggle must win")
	}
	// Deliberately not naming the tray: the same switch is on a keybind, and
	// naming one would send someone looking in the wrong place.
	if !strings.Contains(reason, "by hand") {
		t.Errorf("reason = %q, want it to say the override is manual", reason)
	}
}

// The fail-open rule, which is the most important behaviour in this file: an
// unanswerable question must not hide the HUD. A HUD that is wrongly visible is
// an annoyance; one that is wrongly invisible looks exactly like df-hud being
// broken.
func TestDecideHUDVisibleFailsOpenOnAnUnknownWindow(t *testing.T) {
	running := model.GameState{Running: true, PID: 10}
	if visible, reason := Decide(allRules(), running, desktop.Placement{}); !visible {
		t.Errorf("an unidentified window must not hide the HUD, got %q", reason)
	}
	// Known, but the compositor said nothing about the monitor, so
	// OnActiveWorkspace is a default rather than an observation. This one DOES
	// hide, because Known is the flag that says the answer is real - so the
	// placement code must never set Known without resolving the workspace.
	partial := desktop.Placement{Known: true, Workspace: 3}
	if visible, _ := Decide(allRules(), running, partial); visible {
		t.Error("Known means the answer is real; a false OnActiveWorkspace must be honoured")
	}
}

// fakeQuerier stands in for the compositor.
type fakeQuerier struct {
	mu    sync.Mutex
	place desktop.Placement
	err   error
	calls int
}

func (f *fakeQuerier) GameWindow(context.Context, int, desktop.Match) (desktop.Placement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.place, f.err
}

func (f *fakeQuerier) set(place desktop.Placement, err error) {
	f.mu.Lock()
	f.place, f.err = place, err
	f.mu.Unlock()
}

func (f *fakeQuerier) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func testVisibility(t *testing.T, q Querier) (*Watcher, *game.GameWatcher) {
	t.Helper()
	game := game.NewWatcher("DeadFrontier.exe", time.Hour)
	cfg := config.Default()
	return NewWatcher(game, func() *config.Config { return cfg }, q), game
}

func TestVisibilityWatcherPublishesChanges(t *testing.T) {
	q := &fakeQuerier{place: desktop.Placement{Known: true, OnActiveWorkspace: true, Monitor: "DP-2"}}
	w, game := testVisibility(t, q)

	changes := make(chan Visibility, 8)
	w.SetOnChange(func(state Visibility) { changes <- state })

	// Nothing running: hidden, and no compositor query at all - there is no
	// window to place.
	w.refresh(context.Background())
	if state := w.State(); state.Visible {
		t.Errorf("state = %+v, want hidden with the game closed", state)
	}
	if q.callCount() != 0 {
		t.Errorf("queried the compositor %d times with the game closed", q.callCount())
	}

	game.SetStateForTesting(model.GameState{Running: true, PID: 42, StartedAt: time.Now()})

	w.refresh(context.Background())
	state := w.State()
	if !state.Visible {
		t.Fatalf("state = %+v, want visible while playing on the shown workspace", state)
	}
	if state.Monitor != "DP-2" {
		t.Errorf("Monitor = %q, want the game's monitor to travel with the decision", state.Monitor)
	}

	// Switching away from the game's workspace hides it.
	q.set(desktop.Placement{Known: true, OnActiveWorkspace: false, WorkspaceName: "4", Monitor: "DP-2"}, nil)
	w.refresh(context.Background())
	if w.State().Visible {
		t.Error("want hidden once the game's workspace is not the one being shown")
	}

	// Only transitions are published, which is what lets the UI treat every
	// callback as a real change.
	select {
	case <-changes:
	default:
		t.Error("no change was published")
	}
}

// A compositor that cannot answer is asked once, not twice a second forever.
func TestVisibilityWatcherStopsAskingAfterAFailure(t *testing.T) {
	q := &fakeQuerier{err: errors.New("no such socket")}
	w, game := testVisibility(t, q)
	game.SetStateForTesting(model.GameState{Running: true, PID: 42, StartedAt: time.Now()})

	for i := 0; i < 5; i++ {
		w.refresh(context.Background())
	}
	if q.callCount() != 1 {
		t.Errorf("asked %d times, want to give up after the first failure", q.callCount())
	}
	// And it fails open.
	if !w.State().Visible {
		t.Error("a compositor that cannot be asked must leave the HUD visible")
	}
}

func TestVisibilityWatcherTrayToggle(t *testing.T) {
	q := &fakeQuerier{place: desktop.Placement{Known: true, OnActiveWorkspace: true}}
	w, game := testVisibility(t, q)
	game.SetStateForTesting(model.GameState{Running: true, PID: 42, StartedAt: time.Now()})

	w.refresh(context.Background())
	if !w.State().Visible {
		t.Fatal("visible to begin with")
	}
	w.SetEnabled(false)
	w.refresh(context.Background())
	if w.State().Visible {
		t.Error("the tray toggle must hide it")
	}
	if w.Enabled() {
		t.Error("Enabled should report the override")
	}
	w.SetEnabled(true)
	w.refresh(context.Background())
	if !w.State().Visible {
		t.Error("re-enabling must bring it back")
	}
}

// Poke coalesces, so an event storm on the compositor socket cannot queue up a
// backlog of refreshes.
func TestVisibilityWatcherPokeCoalesces(t *testing.T) {
	w, _ := testVisibility(t, &fakeQuerier{})
	for i := 0; i < 50; i++ {
		w.Poke()
	}
	if got := len(w.wake); got != 1 {
		t.Errorf("queued wakes = %d, want 1", got)
	}
}
