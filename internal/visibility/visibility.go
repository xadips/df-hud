package visibility

import (
	"context"
	"df-hud/internal/config"
	"df-hud/internal/desktop"
	"df-hud/internal/game"
	"df-hud/internal/model"
	"fmt"
	"log"
	"sync"
	"time"
)

// When the HUD should be on screen.
//
// This used to be "always", which is defensible for a click-through overlay of
// four short lines but wrong in practice: the HUD sat over the desktop all day
// showing a clock for a game that was not running, and it sat over every other
// workspace on the monitor because a layer surface has no concept of a workspace.
//
// Two rules fix that, both configurable and both able to say why they hid it:
//
//   - hud.only_when_game_running - no game, no HUD.
//   - hud.follow_game_workspace - the HUD is shown only while the game's own
//     workspace is the one being displayed. The layer-shell protocol has no
//     per-workspace concept at all, so this is emulated: ask the compositor which
//     workspace the game's window is on, compare it with what is active on that
//     monitor, and map the answer onto showing or hiding the surface.
//
// Both FAIL OPEN. If the compositor cannot be reached, or the game's window
// cannot be identified, the HUD is shown. A HUD that is wrongly visible is a
// cosmetic annoyance you can see and report; a HUD that is wrongly invisible is
// indistinguishable from df-hud being broken, and that is the failure that costs
// an evening.

// Rules is the config subset that decides this, extracted so the
// decision is a pure function.
type Rules struct {
	OnlyWhenGameRunning bool
	FollowGameWorkspace bool
	// Enabled is the manual override, from the tray menu or from a keybind hitting
	// /api/overlay/toggle. It is not a config key: it is the "hide it for a
	// moment" switch, and it beats every other rule.
	Enabled bool
}

// decideHUDVisible returns whether to draw the HUD, and when it should not, the
// reason in words fit for a log line and a tray tooltip.
func Decide(r Rules, game model.GameState, place desktop.Placement) (bool, string) {
	if !r.Enabled {
		// Not "from the tray": the same switch is on a keybind too, and naming one
		// of the two would send someone looking in the wrong place.
		return false, "hidden by hand"
	}
	if r.OnlyWhenGameRunning && !game.Running {
		return false, "the game is not running"
	}
	// The launcher is not the game, and /proc cannot tell: the configuration dialogs
	// are the same executable, so the process looks like a running game for as long
	// as they are open. The compositor CAN tell, and LauncherOnly is it saying so -
	// every window carrying the game's class or pid was one we ignore by title.
	//
	// This refines only_when_game_running with better evidence than a process name,
	// which is why it is gated on the same switch rather than on the workspace rule.
	if r.OnlyWhenGameRunning && place.LauncherOnly {
		return false, "the launcher is open but the game has not started"
	}
	// place.Known false means the question could not be answered, not that the
	// answer is no - see the fail-open note above.
	if r.FollowGameWorkspace && place.Known && !place.OnActiveWorkspace {
		if place.ForegroundRule {
			return false, "the game is not the foreground window"
		}
		where := place.WorkspaceName
		if where == "" {
			where = fmt.Sprint(place.Workspace)
		}
		return false, "the game is on workspace " + where + ", which is not the one being shown"
	}
	return true, ""
}

// Querier is the compositor lookup, an interface so the watcher can be
// tested without Hyprland.
type Querier interface {
	GameWindow(ctx context.Context, pid int, match desktop.Match) (desktop.Placement, error)
}

// Visibility is the published decision. Monitor travels with it because the
// same compositor query answers both questions, and because moving the surface to
// the game's monitor is only ever done at the moment it is shown.
type Visibility = model.Visibility

// Watcher recomputes the decision on compositor events and on a slow
// ticker, and publishes changes.
type Watcher struct {
	game    *game.GameWatcher
	cfg     func() *config.Config
	query   Querier
	timeout time.Duration

	wake chan struct{}

	mu       sync.RWMutex
	enabled  bool
	state    Visibility
	place    desktop.Placement
	onChange func(Visibility)
	// queryFailed stops a compositor that is not Hyprland from being asked twice
	// a second forever, and stops the log filling with the same failure.
	queryFailed bool
}

func NewWatcher(game *game.GameWatcher, cfg func() *config.Config, query Querier) *Watcher {
	return &Watcher{
		game:    game,
		cfg:     cfg,
		query:   query,
		timeout: 2 * time.Second,
		wake:    make(chan struct{}, 1),
		enabled: true,
		// Visible until proven otherwise, so a compositor that never answers
		// leaves a working HUD rather than a missing one.
		state: Visibility{Visible: true},
	}
}

func (w *Watcher) SetOnChange(fn func(Visibility)) {
	w.mu.Lock()
	w.onChange = fn
	w.mu.Unlock()
}

func (w *Watcher) State() Visibility {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.state
}

// Placement is the compositor's last word on the game's window, for -check-game
// and the tray tooltip.
func (w *Watcher) Placement() desktop.Placement {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.place
}

// SetEnabled is the tray menu's override.
func (w *Watcher) SetEnabled(on bool) {
	w.mu.Lock()
	changed := w.enabled != on
	w.enabled = on
	w.mu.Unlock()
	if changed {
		w.Poke()
	}
}

func (w *Watcher) Enabled() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.enabled
}

// Poke requests an immediate recompute, coalescing multiple requests.
func (w *Watcher) Poke() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run recomputes until ctx ends. The ticker is a backstop: compositor events
// drive the responsive path, because waiting up to a full tick to hide the HUD
// after switching workspace is exactly the lag that makes an overlay feel broken.
func (w *Watcher) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	w.refresh(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.refresh(ctx)
		case <-w.wake:
			w.refresh(ctx)
		}
	}
}

func (w *Watcher) refresh(ctx context.Context) {
	cfg := w.cfg()
	game := model.GameState{}
	if w.game != nil {
		game = w.game.State()
	}

	w.mu.RLock()
	rules := Rules{
		OnlyWhenGameRunning: cfg.HUD.OnlyWhenGameRunning,
		FollowGameWorkspace: cfg.HUD.FollowGameWorkspace,
		Enabled:             w.enabled,
	}
	failed := w.queryFailed
	w.mu.RUnlock()

	// Only worth asking the compositor while the game is running: with it closed
	// there is no window to place, and the rules above have already decided.
	place := desktop.Placement{}
	if game.Running && w.query != nil && !failed {
		queryCtx, cancel := context.WithTimeout(ctx, w.timeout)
		// launcherWindowMatch rather than the plain one, so the placement also
		// carries the launcher dialog's address. Nothing here uses it; gamekeys.go
		// does, and this is the only compositor query already running.
		match := cfg.LauncherWindowMatch()
		got, err := w.query.GameWindow(queryCtx, game.PID, desktop.Match{
			Class:         match.Class,
			IgnoreTitles:  match.IgnoreTitles,
			LauncherTitle: match.LauncherTitle,
		})
		cancel()
		switch {
		case err != nil:
			// One line, once. Then stop asking: on a compositor without this
			// socket the answer will not change, and the HUD stays visible
			// everywhere, which is the pre-existing behaviour.
			log.Printf("hud: cannot ask the desktop where the game's window is (%v); "+
				"window-following visibility is disabled", err)
			w.mu.Lock()
			w.queryFailed = true
			w.mu.Unlock()
		case !got.Known:
			// Not an error: the window may not be mapped yet during startup, or the
			// only windows there are belong to the launcher - which got.LauncherOnly
			// distinguishes, and which is the difference between failing open and
			// hiding.
			place = got
		default:
			place = got
		}
	}

	visible, reason := Decide(rules, game, place)
	next := Visibility{Visible: visible, Reason: reason, Monitor: place.Monitor}

	w.mu.Lock()
	prev, prevPlace := w.state, w.place
	w.state, w.place = next, place
	fn := w.onChange
	w.mu.Unlock()

	if prevPlace.MatchedBy != place.MatchedBy && place.Known {
		// Said once per identification change, because "how did it find the
		// window" is the first question when the workspace rule misbehaves.
		if place.ForegroundRule {
			log.Printf("hud: the game's window is on %s (matched by %s, class %q)",
				place.Monitor, place.MatchedBy, place.Class)
		} else {
			log.Printf("hud: the game's window is on %s workspace %s (matched by %s, class %q)",
				place.Monitor, place.WorkspaceName, place.MatchedBy, place.Class)
		}
	}
	if prev == next {
		return
	}
	switch {
	case next.Visible && !prev.Visible:
		log.Print("hud: showing")
	case !next.Visible && prev.Visible:
		log.Printf("hud: hiding - %s", next.Reason)
	case !next.Visible && prev.Reason != next.Reason:
		// Still hidden, hidden for a different reason. Worth a line: without it,
		// "hiding - the game is not running" is the last thing said even after the
		// game has started and the real reason has become the workspace - which
		// reads as the workspace rule not working at all.
		log.Printf("hud: still hidden - %s", next.Reason)
	}
	if fn != nil {
		fn(next)
	}
}
