//go:build (linux || windows) && cgo && !nolayershell

package gtk

import (
	"df-hud/internal/hud/render"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// The XP/hr readout.
//
// The colour carries information the number cannot: whether recent polls
// actually landed. A rate averaged over a window with a hole in it is still a
// number, and it still looks authoritative, so the amber and red states are how
// the widget admits it is on thin ice.
type xpWidget struct {
	cfg   XPWidgetConfig
	label *gtk.Label
	// applied is the CSS class currently on the label, tracked so the class is
	// only swapped when it changes - GTK recomputes style on every add/remove,
	// and this runs every second forever.
	applied string
}

func newXPWidget(cfg XPWidgetConfig) *xpWidget {
	return &xpWidget{cfg: cfg, label: newHUDLabel()}
}

func (w *xpWidget) Root() gtk.Widgetter { return w.label }

func (w *xpWidget) Update(v *View) {
	text, class, show := render.XPLine(v, w.cfg)
	w.label.SetVisible(show)
	if !show {
		return
	}
	w.label.SetText(text)
	if class != w.applied {
		if w.applied != "" {
			w.label.RemoveCSSClass(w.applied)
		}
		if class != "" {
			w.label.AddCSSClass(class)
		}
		w.applied = class
	}
}
