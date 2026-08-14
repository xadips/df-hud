package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func allRules() visibilityRules {
	return visibilityRules{OnlyWhenGameRunning: true, FollowGameWorkspace: true, Enabled: true}
}

func TestDecideHUDVisible(t *testing.T) {
	running := GameState{Running: true, PID: 10, StartedAt: time.Now()}
	onScreen := windowPlacement{Known: true, WorkspaceName: "13", OnActiveWorkspace: true, Monitor: "DP-2"}
	elsewhere := windowPlacement{Known: true, WorkspaceName: "7", OnActiveWorkspace: false, Monitor: "DP-2"}

	if visible, reason := decideHUDVisible(allRules(), running, onScreen); !visible {
		t.Errorf("playing on the shown workspace must be visible, got %q", reason)
	}

	visible, reason := decideHUDVisible(allRules(), GameState{}, windowPlacement{})
	if visible {
		t.Error("the game is not running, so there is nothing to draw over")
	}
	if !strings.Contains(reason, "not running") {
		t.Errorf("reason = %q, want it to name the closed game", reason)
	}

	visible, reason = decideHUDVisible(allRules(), running, elsewhere)
	if visible {
		t.Error("the game's workspace is not the one being shown")
	}
	// The reason reaches a tray tooltip, so it has to name the workspace rather
	// than just say "hidden".
	if !strings.Contains(reason, "workspace 7") {
		t.Errorf("reason = %q, want the workspace named", reason)
	}
}

// Both rules can be turned off, and then neither hides anything.
func TestDecideHUDVisibleRespectsDisabledRules(t *testing.T) {
	closed := GameState{}
	elsewhere := windowPlacement{Known: true, WorkspaceName: "7"}

	rules := visibilityRules{Enabled: true}
	if visible, reason := decideHUDVisible(rules, closed, elsewhere); !visible {
		t.Errorf("with both rules off the HUD is always up, got %q", reason)
	}
}

// The tray override beats everything, including a perfectly good reason to show.
func TestDecideHUDVisibleTrayOverride(t *testing.T) {
	rules := allRules()
	rules.Enabled = false
	running := GameState{Running: true}
	onScreen := windowPlacement{Known: true, OnActiveWorkspace: true}

	visible, reason := decideHUDVisible(rules, running, onScreen)
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
	running := GameState{Running: true, PID: 10}
	if visible, reason := decideHUDVisible(allRules(), running, windowPlacement{}); !visible {
		t.Errorf("an unidentified window must not hide the HUD, got %q", reason)
	}
	// Known, but the compositor said nothing about the monitor, so
	// OnActiveWorkspace is a default rather than an observation. This one DOES
	// hide, because Known is the flag that says the answer is real - so the
	// placement code must never set Known without resolving the workspace.
	partial := windowPlacement{Known: true, Workspace: 3}
	if visible, _ := decideHUDVisible(allRules(), running, partial); visible {
		t.Error("Known means the answer is real; a false OnActiveWorkspace must be honoured")
	}
}

// fakeQuerier stands in for the compositor.
type fakeQuerier struct {
	mu    sync.Mutex
	place windowPlacement
	err   error
	calls int
}

func (f *fakeQuerier) GameWindow(context.Context, int, windowMatch) (windowPlacement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.place, f.err
}

func (f *fakeQuerier) set(place windowPlacement, err error) {
	f.mu.Lock()
	f.place, f.err = place, err
	f.mu.Unlock()
}

func (f *fakeQuerier) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func testVisibility(t *testing.T, q windowQuerier) (*visibilityWatcher, *GameWatcher) {
	t.Helper()
	game := newGameWatcher("DeadFrontier.exe", time.Hour)
	cfg := defaultConfig()
	return newVisibilityWatcher(game, func() *Config { return cfg }, q), game
}

func TestVisibilityWatcherPublishesChanges(t *testing.T) {
	q := &fakeQuerier{place: windowPlacement{Known: true, OnActiveWorkspace: true, Monitor: "DP-2"}}
	w, game := testVisibility(t, q)

	changes := make(chan hudVisibility, 8)
	w.SetOnChange(func(state hudVisibility) { changes <- state })

	// Nothing running: hidden, and no compositor query at all - there is no
	// window to place.
	w.refresh(context.Background())
	if state := w.State(); state.Visible {
		t.Errorf("state = %+v, want hidden with the game closed", state)
	}
	if q.callCount() != 0 {
		t.Errorf("queried the compositor %d times with the game closed", q.callCount())
	}

	game.mu.Lock()
	game.state = GameState{Running: true, PID: 42, StartedAt: time.Now()}
	game.mu.Unlock()

	w.refresh(context.Background())
	state := w.State()
	if !state.Visible {
		t.Fatalf("state = %+v, want visible while playing on the shown workspace", state)
	}
	if state.Monitor != "DP-2" {
		t.Errorf("Monitor = %q, want the game's monitor to travel with the decision", state.Monitor)
	}

	// Switching away from the game's workspace hides it.
	q.set(windowPlacement{Known: true, OnActiveWorkspace: false, WorkspaceName: "4", Monitor: "DP-2"}, nil)
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
	game.mu.Lock()
	game.state = GameState{Running: true, PID: 42, StartedAt: time.Now()}
	game.mu.Unlock()

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
	q := &fakeQuerier{place: windowPlacement{Known: true, OnActiveWorkspace: true}}
	w, game := testVisibility(t, q)
	game.mu.Lock()
	game.state = GameState{Running: true, PID: 42, StartedAt: time.Now()}
	game.mu.Unlock()

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
