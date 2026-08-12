package main

import (
	"context"
	"log"
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
type trayActions struct {
	// SetOverlayEnabled is the manual show/hide override.
	SetOverlayEnabled func(bool)
	OverlayEnabled    func() bool
	// ResetXPRate throws away the rate window and starts a fresh average.
	ResetXPRate func()
	// RestartRunClock starts the run clock from now.
	RestartRunClock func()
	ReloadConfig    func()
	Quit            func()

	// View and Visibility supply the icon state and the tooltip.
	View       func() *View
	Visibility func() hudVisibility
}

// trayFace is what is currently displayed, so the D-Bus properties are only
// written when something actually changes. Setting them every second would emit
// a NewIcon and a NewToolTip signal every second to every tray host on the bus.
type trayFace struct {
	active  bool
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
func trayTooltip(v *View, vis hudVisibility) string {
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

// trayIconActiveFor decides which of the two icons to show: the game running is
// the one bit of state visible from across the room.
func trayIconActiveFor(v *View) bool { return v != nil && v.GameRunning }

type trayItem struct {
	actions trayActions
	icons   map[bool][]byte

	mu   sync.Mutex
	face trayFace
	// overlay is the checkbox, kept so it can be synced from the outside. It has
	// to be: the same override is reachable from a keybind now, and a checkbox
	// that disagrees with what it controls is worse than no checkbox.
	overlay *systray.MenuItem
}

// runTray blocks until ctx ends. Safe to run in a goroutine: the Linux backend
// is D-Bus only, so unlike GTK it has no main-thread requirement.
func runTray(ctx context.Context, actions trayActions) {
	t := &trayItem{
		actions: actions,
		icons: map[bool][]byte{
			true:  trayIconPNG(trayIconActive, trayIconSize),
			false: trayIconPNG(trayIconIdle, trayIconSize),
		},
	}

	// Set before the bus connection exists on purpose. systray bakes the current
	// title and icon into the initial property set, so doing it here means the
	// item is never published with a missing icon - and the title doubles as the
	// SNI Id, which is what a tray host uses to remember per-application
	// settings.
	systray.SetTitle("df-hud")
	systray.SetIcon(t.icons[false])

	start, end := systray.RunWithExternalLoop(t.buildMenu, nil)
	start()
	defer end()

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
	systray.AddSeparator()
	reload := systray.AddMenuItem("Reload config", "Re-read config.toml")
	quit := systray.AddMenuItem("Quit df-hud", "Stop df-hud")

	t.mu.Lock()
	t.overlay = overlay
	t.mu.Unlock()

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
	var view *View
	if t.actions.View != nil {
		view = t.actions.View()
	}
	vis := hudVisibility{Visible: true}
	if t.actions.Visibility != nil {
		vis = t.actions.Visibility()
	}
	next := trayFace{active: trayIconActiveFor(view), tooltip: trayTooltip(view, vis)}

	t.mu.Lock()
	prev := t.face
	t.face = next
	t.mu.Unlock()

	if prev.active != next.active {
		systray.SetIcon(t.icons[next.active])
	}
	if prev.tooltip != next.tooltip {
		systray.SetTooltip(next.tooltip)
	}

	// Sync the tick to the real override, which a keybind can also change.
	t.mu.Lock()
	overlay := t.overlay
	t.mu.Unlock()
	if overlay == nil || t.actions.OverlayEnabled == nil {
		return
	}
	if enabled := t.actions.OverlayEnabled(); enabled != overlay.Checked() {
		if enabled {
			overlay.Check()
		} else {
			overlay.Uncheck()
		}
	}
}
