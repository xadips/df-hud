package tray

import (
	"context"
	hudformat "df-hud/internal/format"
	"df-hud/internal/model"
	"log"
	"runtime"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
)

// The system tray item.
//
// Once the HUD hides itself when the game is not running, df-hud has no presence
// on screen at all for most of the day: a background process with no window to
// close, no way to tell whether it is alive, and no way to stop it short of
// finding its PID. The tray icon is the fix, and it is where the things that are
// awkward on a click-through overlay live - quitting, hiding the overlay by hand,
// and a one-line status.
//
// The protocol is StatusNotifierItem over D-Bus, which is what waybar's "tray"
// module implements, so the icon lands in the same place as Steam's and Discord's.
//
// fyne.io/systray (Apache-2.0) does the SNI and com.canonical.dbusmenu plumbing.
// It is a pure D-Bus implementation on Linux - checked before adding it, because
// the widely used alternative drags in GTK3, and GTK3 and GTK4 in one process is
// an immediate crash rather than a subtle problem.

// trayActions is everything the menu can do. Injected as functions so this file
// knows nothing about the app wiring, and so the menu can be tested without a
// D-Bus session.
type Actions struct {
	// SetOverlayEnabled is the manual show/hide override.
	SetOverlayEnabled func(bool)
	OverlayEnabled    func() bool
	// The three feature checkboxes below are persisted to config.toml by the app;
	// these accessors expose their effective live state to the menu.
	SetChallengesHidden func(bool)
	ChallengesHidden    func() bool
	// SetFPSDisplay presses the game's own FPS key. Nil when the feature is not
	// wired, which is what hides the item rather than showing a dead one.
	SetFPSDisplay func(bool)
	FPSDisplay    func() bool
	// SetDismissLauncher presses Play on the launcher dialog. On the menu because
	// that dialog is where the game's input bindings are changed, so leaving it up
	// has to be one click away.
	SetDismissLauncher func(bool)
	DismissLauncher    func() bool

	// ResetXPRate throws away the rate window and starts a fresh average.
	ResetXPRate func()
	// RestartRunClock starts the run clock from now.
	RestartRunClock func()
	// RetryPresence retries the Discord IPC bind after the user closes whichever
	// Discord client owned it first. Nil when presence capture is disabled.
	RetryPresence      func() bool
	PresenceBindFailed func() bool
	ReloadConfig       func()
	Quit               func()
	Version            string

	// View and Visibility supply the icon state and the tooltip.
	View       func() *model.View
	Visibility func() model.Visibility
}

type (
	trayActions   = Actions
	View          = model.View
	hudVisibility = model.Visibility
)

// trayFace is what is currently displayed, so the D-Bus properties are only
// written when something actually changes. Setting them every second would emit
// a NewIcon and a NewToolTip signal every second to every tray host on the bus.
type trayFace struct {
	icon    trayIconState
	tooltip string
}

// trayTooltip is the status panel, one fact per line.
//
// Multi-line rather than one long string: a tray tooltip is rendered as a
// floating label, so a single line grows until it is a stripe across the screen
// and nothing in it stands out. The first line still carries the primary state on
// its own, in case a host shows only one.
//
// Newlines are the only formatting used. Some hosts (waybar among them) pass
// tooltip text through as Pango markup, so anything else would mean escaping
// every value on the way in and would render as literal &amp; on the hosts that
// do not.
func trayTooltip(v *model.View, vis model.Visibility) string {
	var primary string
	switch {
	case v == nil:
		primary = "starting"
	case !v.GameRunning:
		primary = "the game is not running"
	case v.HasSession:
		primary = "in the city " + formatClock(v.SessionTime)
	default:
		// The client is up but no run has started: the launcher, the loading
		// screen, or standing in an outpost. Saying so is the whole explanation
		// for a HUD with no clock on it.
		primary = "client up " + formatClock(v.ClientUptime) + ", not in the city"
	}
	lines := []string{"df-hud: " + primary}

	if v != nil {
		// HaveData is checked as well as XPAvailable because the rate window
		// survives a restart: without it, a rate computed from samples persisted
		// before the last shutdown would be shown as if it were current, hours
		// after the run that earned it. The HUD's own xp row gates on the same
		// flag.
		// The unit is spelled out here because, unlike the HUD's own row, there is
		// no configured prefix saying what the number is per.
		if v.HaveData && v.XPAvailable {
			if rate := formatRate(v.XPPerHour); rate != "" {
				lines = append(lines, "xp "+rate+"/hr")
			}
		}
		if v.Status != "" {
			lines = append(lines, v.Status)
		}
		// Only worth explaining while the game is running: with it closed, the
		// reason the overlay is hidden is already the first line.
		if v.GameRunning && !vis.Visible && vis.Reason != "" {
			lines = append(lines, "overlay hidden: "+vis.Reason)
		}
	}
	return strings.Join(lines, "\n")
}

func trayTooltipWithVersion(v *model.View, vis model.Visibility, version string) string {
	text := trayTooltip(v, vis)
	if version != "" {
		text += "\nversion " + version
	}
	return text
}

// trayIconActiveFor decides which of the two icons to show: the game running is
// the one bit of state visible from across the room.
func trayIconActiveFor(v *model.View) bool { return v != nil && v.GameRunning }

type trayIconState uint8

const (
	trayIconIdleState trayIconState = iota
	trayIconActiveState
	trayIconErrorState
)

func trayIconStateFor(v *model.View, presenceBindFailed bool) trayIconState {
	if presenceBindFailed {
		return trayIconErrorState
	}
	if trayIconActiveFor(v) {
		return trayIconActiveState
	}
	return trayIconIdleState
}

type trayItem struct {
	actions trayActions
	icons   map[trayIconState][]byte

	mu   sync.Mutex
	face trayFace
	// overlay and board are the checkboxes, kept so they can be synced from the
	// outside. They have to be: the same overrides are reachable from keybinds, and
	// a checkbox that disagrees with what it controls is worse than no checkbox.
	overlay *systray.MenuItem
	board   *systray.MenuItem
	fps     *systray.MenuItem
	skipLnc *systray.MenuItem
}

// runTray blocks until ctx ends. Safe to run in a goroutine: the Linux backend
// is D-Bus only, so unlike GTK it has no main-thread requirement.
func Run(ctx context.Context, actions Actions) {
	t := &trayItem{
		actions: actions,
		icons: map[trayIconState][]byte{
			trayIconActiveState: trayIconBytes(trayIconActive, trayIconSize),
			trayIconIdleState:   trayIconBytes(trayIconIdle, trayIconSize),
			trayIconErrorState:  trayIconBytes(trayIconError, trayIconSize),
		},
	}

	if runtime.GOOS == "windows" {
		t.runWindows(ctx)
		return
	}

	// Linux's SNI backend bakes these into its initial property set.
	systray.SetTitle("df-hud")
	systray.SetIcon(t.icons[trayIconIdleState])
	ready := make(chan struct{})
	onReady := func() {
		t.buildMenu()
		close(ready)
	}
	start, end := systray.RunWithExternalLoop(onReady, nil)
	start()
	defer end()
	select {
	case <-ctx.Done():
		return
	case <-ready:
	}
	startTrayPlatformMaintenance(ctx)
	t.refreshLoop(ctx)
}

// runWindows keeps tray window creation and GetMessage on the same locked OS
// thread. RunWithExternalLoop starts the message loop in another goroutine,
// whose OS thread is not the one that owns the HWND; whether it happened to work
// then depended on scheduler placement and produced the observed one-click-only
// tray.
func (t *trayItem) runWindows(ctx context.Context) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	ready := make(chan struct{})
	onReady := func() {
		systray.SetTitle("df-hud")
		systray.SetIcon(t.icons[trayIconIdleState])
		t.buildMenu()
		close(ready)
	}
	go func() {
		<-ctx.Done()
		systray.Quit()
	}()
	go func() {
		<-ready
		startTrayPlatformMaintenance(ctx)
		t.refreshLoop(ctx)
	}()
	systray.Run(onReady, nil)
}

func (t *trayItem) refreshLoop(ctx context.Context) {
	// One second, matching the HUD's own tick. The properties are only written
	// when the rendered text changes, so this is a comparison per second rather
	// than bus traffic per second.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	t.refresh()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.refresh()
		}
	}
}

// buildMenu runs on systray's ready callback, which is the only point at which
// the menu is guaranteed to be exported. Each item gets its own goroutine because
// systray delivers clicks on a channel per item.
func (t *trayItem) buildMenu() {
	enabled := true
	if t.actions.OverlayEnabled != nil {
		enabled = t.actions.OverlayEnabled()
	}

	overlay := systray.AddMenuItemCheckbox("Show overlay",
		"Draw the HUD over the game. The other visibility rules still apply.", enabled)
	boardShown := true
	if t.actions.ChallengesHidden != nil {
		boardShown = !t.actions.ChallengesHidden()
	}
	board := systray.AddMenuItemCheckbox("Show challenges",
		"Hide the challenge board without turning it off in the config", boardShown)
	// The game's FPS readout is off at every launch and nothing in the game
	// remembers otherwise, so this is the only switch there is for it. Only
	// offered when it is wired: an item that does nothing is worse than none.
	var fps *systray.MenuItem
	if t.actions.SetFPSDisplay != nil && t.actions.FPSDisplay != nil {
		fps = systray.AddMenuItemCheckbox("FPS display on launch",
			"Press the game's FPS key once each time the client starts", t.actions.FPSDisplay())
	}
	// Unticking this is how you reach the launcher's Input tab, so the label says
	// what it does to the dialog rather than naming the dialog.
	var skipLnc *systray.MenuItem
	if t.actions.SetDismissLauncher != nil && t.actions.DismissLauncher != nil {
		skipLnc = systray.AddMenuItemCheckbox("Skip the launcher",
			"Press Play on the configuration dialog. Untick to reach the Input tab.",
			t.actions.DismissLauncher())
	}
	systray.AddSeparator()

	// A challenge reward is a single lump of XP that no amount of killing
	// produced, so it lands in the window and inflates the average for as long as
	// the window is wide. The rate is not wrong - that XP really was earned - but
	// it stops answering "how fast am I killing", which is the question being
	// asked. Discarding the window is the honest fix, and it has to be manual
	// because df-hud cannot see where a lump came from.
	resetXP := systray.AddMenuItem("Reset xp/hr",
		"Start the average again from now, after a challenge reward or a lull")
	// Paired with the xp reset: both are "that number is not measuring what I am
	// doing, start again". The run clock needs one because nothing in the player
	// record marks the moment the client takes control, so its start is inferred
	// from activity and can be late.
	restartRun := systray.AddMenuItem("Restart run clock",
		"Time this run from now, when it started before you did")
	var retryPresence *systray.MenuItem
	if t.actions.RetryPresence != nil {
		retryPresence = systray.AddMenuItem("Retry Discord IPC bind",
			"Bind presence capture again after closing Discord or Vesktop")
	}
	reload := systray.AddMenuItem("Reload config", "Re-read config.toml")
	systray.AddSeparator()
	if t.actions.Version != "" {
		versionItem := systray.AddMenuItem("df-hud "+t.actions.Version, "")
		versionItem.Disable()
	}
	quit := systray.AddMenuItem("Quit df-hud", "Stop df-hud")

	t.mu.Lock()
	t.overlay, t.board, t.fps, t.skipLnc = overlay, board, fps, skipLnc
	t.mu.Unlock()

	if skipLnc != nil {
		go func() {
			for range skipLnc.ClickedCh {
				t.actions.SetDismissLauncher(!t.actions.DismissLauncher())
			}
		}()
	}

	if fps != nil {
		go func() {
			for range fps.ClickedCh {
				t.actions.SetFPSDisplay(!t.actions.FPSDisplay())
			}
		}()
	}

	go func() {
		for range board.ClickedCh {
			if t.actions.SetChallengesHidden != nil && t.actions.ChallengesHidden != nil {
				t.actions.SetChallengesHidden(!t.actions.ChallengesHidden())
			}
		}
	}()

	go func() {
		for range overlay.ClickedCh {
			// The override itself is the source of truth, not the checkbox: it is
			// also reachable from a keybind, so reading the tick back would be one
			// of two copies of the same boolean. The refresh loop syncs the tick.
			if t.actions.SetOverlayEnabled != nil && t.actions.OverlayEnabled != nil {
				t.actions.SetOverlayEnabled(!t.actions.OverlayEnabled())
			}
		}
	}()
	go func() {
		for range resetXP.ClickedCh {
			log.Print("tray: xp/hr window reset")
			if t.actions.ResetXPRate != nil {
				t.actions.ResetXPRate()
			}
		}
	}()
	go func() {
		for range restartRun.ClickedCh {
			log.Print("tray: run clock restarted")
			if t.actions.RestartRunClock != nil {
				t.actions.RestartRunClock()
			}
		}
	}()
	if retryPresence != nil {
		go func() {
			for range retryPresence.ClickedCh {
				if t.actions.RetryPresence() {
					log.Print("tray: Discord IPC bind retry requested")
				} else {
					log.Print("tray: Discord IPC bind is already active or retry is pending")
				}
			}
		}()
	}
	go func() {
		for range reload.ClickedCh {
			if t.actions.ReloadConfig != nil {
				t.actions.ReloadConfig()
			}
		}
	}()
	go func() {
		for range quit.ClickedCh {
			log.Print("tray: quit requested")
			if t.actions.Quit != nil {
				t.actions.Quit()
			}
		}
	}()
}

func (t *trayItem) refresh() {
	var view *model.View
	if t.actions.View != nil {
		view = t.actions.View()
	}
	vis := model.Visibility{Visible: true}
	if t.actions.Visibility != nil {
		vis = t.actions.Visibility()
	}
	bindFailed := t.actions.PresenceBindFailed != nil && t.actions.PresenceBindFailed()
	tooltip := trayTooltipWithVersion(view, vis, t.actions.Version)
	if bindFailed {
		tooltip += "\nDiscord IPC unavailable - close Discord and retry the bind"
	}
	next := trayFace{icon: trayIconStateFor(view, bindFailed), tooltip: tooltip}

	t.mu.Lock()
	prev := t.face
	t.face = next
	t.mu.Unlock()

	if prev.icon != next.icon {
		systray.SetIcon(t.icons[next.icon])
	}
	if prev.tooltip != next.tooltip {
		systray.SetTooltip(next.tooltip)
	}

	// Sync the ticks to the real overrides, which keybinds can also change.
	t.mu.Lock()
	overlay, board, fps, skipLnc := t.overlay, t.board, t.fps, t.skipLnc
	t.mu.Unlock()
	if overlay != nil && t.actions.OverlayEnabled != nil {
		syncCheckbox(overlay, t.actions.OverlayEnabled())
	}
	if board != nil && t.actions.ChallengesHidden != nil {
		syncCheckbox(board, !t.actions.ChallengesHidden())
	}
	if fps != nil && t.actions.FPSDisplay != nil {
		syncCheckbox(fps, t.actions.FPSDisplay())
	}
	if skipLnc != nil && t.actions.DismissLauncher != nil {
		syncCheckbox(skipLnc, t.actions.DismissLauncher())
	}
}

func formatClock(d time.Duration) string { return hudformat.Clock(d) }
func formatRate(rate float64) string     { return hudformat.Rate(rate) }

func syncCheckbox(item *systray.MenuItem, want bool) {
	if want == item.Checked() {
		return
	}
	if want {
		item.Check()
	} else {
		item.Uncheck()
	}
}
