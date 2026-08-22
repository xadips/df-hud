//go:build linux && cgo && !nolayershell

package gtk

import (
	"df-hud/internal/hud/render"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// The challenge tracker, priority #2.
//
// Rows are created lazily and reused rather than rebuilt each frame: this
// updates every second forever, and tearing down and re-adding GTK widgets that
// often is both wasteful and visible as flicker. The row count changes rarely -
// only when a cycle rolls over or a category is switched off - so surplus rows are
// hidden instead of destroyed.
//
// Each row is drawn as Pango markup rather than plain text, which is what lets one
// label hold three levels of hierarchy: bold progress, plain name, dimmed and
// smaller objective. A weekly with four objectives becomes a group - the challenge
// heading its own row with the objectives indented under it - instead of a very
// long line.
type challengeWidget struct {
	cfg  ChallengesWidgetConfig
	box  *gtk.Box
	rows []*gtk.Label
	// applied is the classes currently on each row, joined, tracked so they are only
	// swapped when they change: GTK recomputes style on every add and remove, and
	// this runs every second forever.
	applied []string
}

func newChallengeWidget(cfg ChallengesWidgetConfig) *challengeWidget {
	return &challengeWidget{cfg: cfg, box: gtk.NewBox(gtk.OrientationVertical, 0)}
}

func (w *challengeWidget) Root() gtk.Widgetter { return w.box }

func (w *challengeWidget) Update(v *View) {
	lines := render.ChallengeLines(v, w.cfg)
	if len(lines) == 0 {
		w.box.SetVisible(false)
		return
	}
	w.box.SetVisible(true)

	for len(w.rows) < len(lines) {
		label := newHUDLabel()
		w.box.Append(label)
		w.rows = append(w.rows, label)
		w.applied = append(w.applied, "")
	}
	for i, label := range w.rows {
		if i >= len(lines) {
			label.SetVisible(false)
			continue
		}
		// SetMarkup rather than SetText, and not just because of the styling:
		// gtk_label_set_text turns use-markup back OFF, so a label updated with
		// SetText would silently render the tags as literal text.
		label.SetMarkup(lines[i].Markup())
		label.SetVisible(true)

		// Green for finished, red for nearly out of time, and a margin above each
		// challenge after the first. Classes rather than markup: the built-in sheet
		// scopes the colours so they outrank a per-group colour the way every other
		// state colour does, and a margin cannot be expressed in markup at all.
		classes := lines[i].Classes()
		if want := strings.Join(classes, " "); want != w.applied[i] {
			for _, old := range strings.Fields(w.applied[i]) {
				label.RemoveCSSClass(old)
			}
			for _, class := range classes {
				label.AddCSSClass(class)
			}
			w.applied[i] = want
		}
	}
}
