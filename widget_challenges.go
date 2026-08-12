//go:build linux && cgo && !nolayershell

package main

import "github.com/diamondburned/gotk4/pkg/gtk/v4"

// The challenge tracker, priority #2.
//
// Rows are created lazily and reused rather than rebuilt each frame: this
// updates every second forever, and tearing down and re-adding GTK widgets that
// often is both wasteful and visible as flicker. The row count changes rarely -
// only when pins change or a cycle rolls over - so surplus rows are hidden
// instead of destroyed.
type challengeWidget struct {
	cfg  ChallengesWidgetConfig
	box  *gtk.Box
	rows []*gtk.Label
}

func newChallengeWidget(cfg ChallengesWidgetConfig) *challengeWidget {
	return &challengeWidget{cfg: cfg, box: gtk.NewBox(gtk.OrientationVertical, 0)}
}

func (w *challengeWidget) Root() gtk.Widgetter { return w.box }

func (w *challengeWidget) Update(v *View) {
	lines := challengeLines(v, w.cfg)
	if len(lines) == 0 {
		w.box.SetVisible(false)
		return
	}
	w.box.SetVisible(true)

	for len(w.rows) < len(lines) {
		label := newHUDLabel()
		w.box.Append(label)
		w.rows = append(w.rows, label)
	}
	for i, label := range w.rows {
		if i < len(lines) {
			label.SetText(lines[i])
			label.SetVisible(true)
		} else {
			label.SetVisible(false)
		}
	}
}
