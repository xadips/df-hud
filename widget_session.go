//go:build linux && cgo && !nolayershell

package main

import "github.com/diamondburned/gotk4/pkg/gtk/v4"

// The session clock: time since the game client launched.
//
// It reads the game process's own start time from /proc, which is why it is
// correct even if df-hud starts an hour into a session or is restarted
// mid-run - there is no accumulator to lose and no state to persist. Closing the
// game hides the row; relaunching starts a new clock, because a new process is a
// new session.
//
// It updates without any network activity: View.SessionTime is derived from the
// current time on every 1s tick, the same way the game's own timeKeeper loop
// moves its clocks.
type sessionWidget struct {
	label *gtk.Label
}

func newSessionWidget() *sessionWidget {
	return &sessionWidget{label: newHUDLabel()}
}

func (w *sessionWidget) Root() gtk.Widgetter { return w.label }

func (w *sessionWidget) Update(v *View) {
	if !v.GameRunning {
		w.label.SetVisible(false)
		return
	}
	w.label.SetVisible(true)
	w.label.SetText(formatClock(v.SessionTime))
}
