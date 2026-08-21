//go:build (linux || windows) && cgo && !nolayershell

package gtk

import (
	"df-hud/internal/hud/render"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

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
	cfg   SessionWidgetConfig
	label *gtk.Label
}

func newSessionWidget(cfg SessionWidgetConfig) *sessionWidget {
	return &sessionWidget{cfg: cfg, label: newHUDLabel()}
}

func (w *sessionWidget) Root() gtk.Widgetter { return w.label }

func (w *sessionWidget) Update(v *View) {
	text, show := render.SessionLine(v, w.cfg)
	w.label.SetVisible(show)
	if show {
		w.label.SetText(text)
	}
}
