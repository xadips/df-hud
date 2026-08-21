package gamekeys

import (
	"context"
	"df-hud/internal/config"
	"df-hud/internal/desktop"
	"df-hud/internal/model"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// Pressing a key in the game's own window, once per launch.
//
// There is exactly one thing this is for: the client's FPS readout starts OFF
// every single launch. It is not a setting that got lost - the game keeps its
// settings in the Wine registry under Software\Creaky Corpse\Dead Frontier, and
// that key has entries for the FPS CAP, vsync, quality, brightness and the HUD
// outlines, but none for whether the readout is showing. There is nothing to
// preset, so the only way to have it on is to press the key.
//
// SilverOverlays does the same thing, and has to focus the game's window first
// because it types with pynput, which goes wherever focus is. Addressing the
// window instead means nothing is focused and nothing is stolen - see
// hyprClient.SendKey.
//
// Deliberately narrow. This is not a macro facility: it sends ONE configured key
// ONCE per game process, it never repeats, and it does nothing at all while the
// launcher is the only window open.

// Sender is the compositor, as an interface so the driver can be tested
// without Hyprland.
type Sender interface {
	SendKey(ctx context.Context, key, address string) error
}

// Keys fires the configured key when the game's own window has been up long
// enough.
type Keys struct {
	cfg   func() *config.Config
	state func() model.GameState
	place func() desktop.Placement
	send  Sender

	// active is the focused window's address. A key sent to an unfocused window
	// is the likeliest reason one silently does nothing - SilverOverlays calls
	// activate() first for the same reason. This waits for focus rather than
	// stealing it. Nil means "assume focused".
	active func(context.Context) (string, bool)

	// ready reports that the client is in the world rather than on a loading
	// screen. Nil, or true, means send anyway.
	ready func() bool

	// enabled and dismiss are the live copies behind the tray checkboxes. The app
	// persists tray changes to config.toml and ApplyConfig updates them on reload.
	enabled atomic.Bool
	dismiss atomic.Bool

	mu sync.Mutex
	// pid is the game process the fields below describe. Everything resets when
	// it changes, which is what makes these once per LAUNCH rather than once per
	// df-hud lifetime.
	pid    int
	seenAt time.Time
	sent   bool
	// The launcher dialog is a separate one-shot with its own clock: it happens
	// before the game window exists, so it cannot share the other one's.
	launcherSeenAt time.Time
	launcherSent   bool
}

func New(cfg func() *config.Config, state func() model.GameState, place func() desktop.Placement, send Sender) *Keys {
	k := &Keys{cfg: cfg, state: state, place: place, send: send}
	k.enabled.Store(cfg().GameKeys.FPSDisplay)
	k.dismiss.Store(cfg().GameKeys.DismissLauncher)
	return k
}

// SetFPSDisplay is the tray menu's switch.
//
// Turning it on mid-session sends the key straight away, because that is what
// ticking a box called "FPS display" is asking for. Turning it off sends nothing
// - the game's own key is a toggle and df-hud does not track its state, so
// "off" can only mean "do not do this at the next launch". Ticking it twice in
// one session does not send twice, which is what stops that difference from
// turning into a toggle that fights the player.
func (k *Keys) SetFPSDisplay(on bool) { k.enabled.Store(on) }

func (k *Keys) FPSDisplay() bool { return k.enabled.Load() }

// ApplyConfig makes live config reloads authoritative over the two tray-backed
// switches. Tray clicks persist these fields before changing the runtime state.
func (k *Keys) ApplyConfig(cfg config.GameKeysConfig) {
	k.enabled.Store(cfg.FPSDisplay)
	k.dismiss.Store(cfg.DismissLauncher)
}

// KeysTick is the floor on how long the launcher dialog stays on screen: it
// is noticed on one tick and dismissed on a later one. Reads cached state only;
// the compositor is asked just before a key is sent. SilverOverlays uses 100ms.
const KeysTick = 200 * time.Millisecond

// Run drives the check until ctx ends. A ticker rather than an event
// subscription: the trigger is "the window has existed for N, and is focused,
// and the client is in the world", which is a clock question with two gates.
func (k *Keys) Run(ctx context.Context) {
	ticker := time.NewTicker(KeysTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			k.Tick(ctx, now)
		}
	}
}

// tick is the whole decision, taking the clock so it can be tested without
// waiting for one.
func (k *Keys) Tick(ctx context.Context, now time.Time) {
	game := model.GameState{}
	if k.state != nil {
		game = k.state()
	}
	if !game.Running {
		k.forget()
		return
	}
	cfg := k.cfg()
	place := k.place()

	// The launcher dialog, while it is the only thing open. Pressing its default
	// button is the whole feature; see GameKeysConfig.DismissLauncher for why it
	// is dismissed rather than stopped from appearing.
	if place.LauncherOnly && place.LauncherAddress != "" {
		k.dismissLauncher(ctx, cfg, game.PID, place.LauncherAddress, now)
		return
	}

	// The launcher is not the game. place.Known is false both while only the
	// configuration dialogs are open and while the real window is not mapped yet,
	// and either way there is nothing to send a key to - so this doubles as the
	// "wait until the client is actually up" gate.
	if !place.Known || place.Address == "" {
		return
	}

	k.mu.Lock()
	if k.pid != game.PID {
		k.pid, k.seenAt, k.sent = game.PID, now, false
		k.launcherSeenAt, k.launcherSent = time.Time{}, false
	}
	if k.seenAt.IsZero() {
		// First sight of this launch's WINDOW. Separate from the pid check above
		// because dismissing the launcher claims the pid while the game window
		// does not exist yet - and without this, that would leave the delay
		// measured from the zero time, so the key would fire on the first tick
		// after the window appeared instead of fps_delay later.
		//
		// The delay has to run from here rather than from the process starting:
		// the process starts when the LAUNCHER opens, which can sit on screen
		// indefinitely.
		k.seenAt = now
	}
	due := !k.sent && k.enabled.Load() && !now.Before(k.seenAt.Add(cfg.GameKeys.FPSDelay.Duration))
	k.mu.Unlock()
	if !due {
		return
	}
	// The two gates below are WAITS, not failures: nothing is claimed, so the
	// next tick tries again. A key pressed at the loading screen or into another
	// window is simply lost, and losing it silently is what made this
	// intermittent.
	if !k.canSend(ctx, place.Address) {
		return
	}

	k.mu.Lock()
	// Claimed before the send, not after: a send that fails has still had its one
	// attempt, and retrying against a refusing compositor would be a keypress
	// storm aimed at the game.
	k.sent = true
	k.mu.Unlock()

	key := cfg.GameKeys.FPSKey
	if err := k.send.SendKey(ctx, key, place.Address); err != nil {
		log.Printf("game keys: could not send %q to the game window: %v", key, err)
		return
	}
	log.Printf("game keys: sent %q to turn the game's FPS display on", key)
}

// SetDismissLauncher is the tray menu's switch, and the reason it is on the menu
// at all: the dialog is where input bindings are changed, so "let it stay up this
// time" has to be reachable without editing a file. Unticking it before a launch
// is the whole workflow.
func (k *Keys) SetDismissLauncher(on bool) { k.dismiss.Store(on) }

func (k *Keys) DismissLauncher() bool { return k.dismiss.Load() }

// dismissLauncher presses the dialog's default button, once per launch.
func (k *Keys) dismissLauncher(ctx context.Context, cfg *config.Config, pid int, address string, now time.Time) {
	if !k.dismiss.Load() {
		return
	}
	k.mu.Lock()
	if k.pid != pid {
		k.pid, k.seenAt, k.sent = pid, time.Time{}, false
		k.launcherSeenAt, k.launcherSent = now, false
	}
	if k.launcherSeenAt.IsZero() {
		k.launcherSeenAt = now
	}
	due := !k.launcherSent && !now.Before(k.launcherSeenAt.Add(cfg.GameKeys.LauncherDelay.Duration))
	k.mu.Unlock()
	if !due {
		return
	}
	// Focus only. The client-in-world gate does not apply: the launcher IS what
	// stands between here and the world, so waiting for the world would deadlock.
	if k.active != nil {
		if addr, ok := k.active(ctx); !ok || addr != address {
			return
		}
	}

	k.mu.Lock()
	k.launcherSent = true
	k.mu.Unlock()

	key := cfg.GameKeys.LauncherKey
	if err := k.send.SendKey(ctx, key, address); err != nil {
		log.Printf("game keys: could not dismiss the launcher: %v", err)
		return
	}
	log.Printf("game keys: pressed %q on the launcher dialog", key)
}

// canSend is the two conditions that decide whether a key would actually land:
// the client is past its loading screen, and the window it is aimed at is the
// focused one.
func (k *Keys) canSend(ctx context.Context, address string) bool {
	if k.ready != nil && !k.ready() {
		return false
	}
	if k.active == nil {
		return true
	}
	addr, ok := k.active(ctx)
	return ok && addr == address
}

// forget re-arms for the next launch.
func (k *Keys) forget() {
	k.mu.Lock()
	k.pid, k.seenAt, k.sent = 0, time.Time{}, false
	k.launcherSeenAt, k.launcherSent = time.Time{}, false
	k.mu.Unlock()
}

func (k *Keys) SetActive(fn func(context.Context) (string, bool)) { k.active = fn }
func (k *Keys) SetReady(fn func() bool)                           { k.ready = fn }
