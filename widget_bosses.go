//go:build linux && cgo && !nolayershell

package main

import "github.com/diamondburned/gotk4/pkg/gtk/v4"

// What the city event feed says is standing where you are, and whether the outpost
// is under attack.
//
// Split out of Block Info, which it used to be part of. Two reasons, and the
// second one is the real one:
//
//   - they answer different questions. Block Info is where you are; this is what is
//     there with you, and it comes from somebody else's server rather than the
//     game's own record.
//   - Block Info is two short rows that change when you walk. This is a list that
//     is empty on most of the map and seven rows long on a boss nest. Sharing a
//     group meant anything below moved up and down as you travelled.
//
// Rows are created on demand and reused, never destroyed: this updates every
// second forever, and a block with fewer bosses than the last one should hide a
// row rather than rebuild the tree.
type bossesWidget struct {
	cfg BossesWidgetConfig
	box *gtk.Box
	// attack is its own label with the loud class fixed on it. An outpost attack is
	// map-wide and time-limited, so it is a different claim from "there is a boss
	// here" and must not share a colour with one.
	attack *gtk.Label

	// Onslaught's own header (title + countdown) and its prev/now/next rows -
	// see onslaughtPanel. Built once and toggled, not rebuilt on crossing in
	// and out: most of a session is not spent in Onslaught, but the widgets
	// are cheap and a toggle is simpler than tearing the tree down each time.
	onslaughtHeader *gtk.Box
	onslaughtTitle  *gtk.Label
	onslaughtTimer  *gtk.Label
	onslaughtRows   []*onslaughtRowWidget

	threats []*gtk.Label
	// nearest is which way to walk when this block is empty. Its own label because
	// it is the opposite case from the threat rows: they are never both up.
	nearest *gtk.Label
}

// onslaughtRowWidget is one prev/now/next row: two labels side by side rather
// than one with a mixed-colour markup span, because GTK's text-shadow cannot
// be turned off for only part of a label's text, and the "prev"/"now"/"next"
// word wants no shadow the same as the rest of this panel while the content
// beside it keeps its own colour. See onslaughtLabelClass in hudlines.go.
type onslaughtRowWidget struct {
	box     *gtk.Box
	label   *gtk.Label
	content *gtk.Label
}

func newOnslaughtRowWidget() *onslaughtRowWidget {
	rw := &onslaughtRowWidget{
		box:     gtk.NewBox(gtk.OrientationHorizontal, 6),
		label:   newHUDLabel(),
		content: newHUDLabel(),
	}
	rw.label.AddCSSClass(onslaughtLabelClass)
	// Fixed width so a continuation row's content (no label word) still lines
	// up under the boss name on the row above it, the way the userscript's own
	// flex:0 0 30px label column does.
	rw.label.SetWidthChars(5)
	rw.box.Append(rw.label)
	rw.box.Append(rw.content)
	return rw
}

// onslaughtContentClasses is every CSS class an onslaught row's content can
// carry. Update removes all of them before adding the current one, since
// which colour applies to a given row index changes as a cycle turns over - a
// row that was "next" a moment ago must not still carry that class once the
// same slot becomes "now".
var onslaughtContentClasses = []string{onslaughtPrevClass, onslaughtNowClass, onslaughtNextClass, onslaughtEmptyClass}

func newBossesWidget(cfg BossesWidgetConfig) *bossesWidget {
	w := &bossesWidget{
		cfg:     cfg,
		box:     gtk.NewBox(gtk.OrientationVertical, 0),
		attack:  newHUDLabel(),
		nearest: newHUDLabel(),
	}
	w.attack.AddCSSClass("threat")
	w.attack.AddCSSClass("urgent")
	w.box.Append(w.attack)

	w.onslaughtHeader = gtk.NewBox(gtk.OrientationHorizontal, 6)
	w.onslaughtTitle = newHUDLabel()
	w.onslaughtTitle.SetText("Onslaught Cycles")
	w.onslaughtTitle.AddCSSClass("onslaught-timer") // same white as the countdown beside it
	// Expands to push the timer to the far edge, the way the userscript's own
	// header (justify-content: space-between) lays out.
	w.onslaughtTitle.SetHExpand(true)
	w.onslaughtTimer = newHUDLabel()
	w.onslaughtTimer.AddCSSClass("onslaught-timer")
	w.onslaughtTimer.SetHAlign(gtk.AlignEnd)
	w.onslaughtHeader.Append(w.onslaughtTitle)
	w.onslaughtHeader.Append(w.onslaughtTimer)
	w.box.Append(w.onslaughtHeader)

	// Above the threat rows in the box, since those are created on demand later.
	// That is not a layout decision: the two are never up at the same time, because
	// this one only exists for a block with nothing on it.
	w.box.Append(w.nearest)
	return w
}

func (w *bossesWidget) Root() gtk.Widgetter { return w.box }

func (w *bossesWidget) Update(v *View) {
	attack, showAttack := outpostAttackLine(v)
	timer, showTimer := onslaughtHeaderTimer(v)
	onslaughtRows, inOnslaught := onslaughtPanel(v)
	nearest, showNearest := nearestLine(v, w.cfg)

	// Onslaught's "now" row already covers what threatLines would otherwise
	// repeat unlabelled and uncoloured - see onslaughtPanel's own doc comment.
	var threats []string
	if !inOnslaught {
		threats = threatLines(v)
	}

	// With nothing to say at all the whole group goes away rather than leaving an
	// empty box behind.
	if !showAttack && !inOnslaught && len(threats) == 0 && !showNearest {
		w.box.SetVisible(false)
		return
	}
	w.box.SetVisible(true)

	w.attack.SetVisible(showAttack)
	if showAttack {
		w.attack.SetText(attack)
	}

	w.onslaughtHeader.SetVisible(inOnslaught)
	if inOnslaught {
		w.onslaughtTimer.SetVisible(showTimer)
		if showTimer {
			w.onslaughtTimer.SetText(timer)
		}
	}

	for len(w.onslaughtRows) < len(onslaughtRows) {
		rw := newOnslaughtRowWidget()
		w.box.Append(rw.box)
		w.onslaughtRows = append(w.onslaughtRows, rw)
	}
	for i, rw := range w.onslaughtRows {
		if i >= len(onslaughtRows) {
			rw.box.SetVisible(false)
			continue
		}
		r := onslaughtRows[i]
		rw.label.SetText(r.Label) // empty on a continuation row - no second "prev"
		for _, c := range onslaughtContentClasses {
			rw.content.RemoveCSSClass(c)
		}
		rw.content.AddCSSClass(r.ContentClass)
		rw.content.SetText(r.Content)
		rw.box.SetVisible(true)
	}

	w.nearest.SetVisible(showNearest)
	if showNearest {
		w.nearest.SetText(nearest)
	}

	for len(w.threats) < len(threats) {
		label := newHUDLabel()
		label.AddCSSClass("threat")
		w.box.Append(label)
		w.threats = append(w.threats, label)
	}
	for i, label := range w.threats {
		if i >= len(threats) {
			label.SetVisible(false)
			continue
		}
		label.SetText(threats[i])
		label.SetVisible(true)
	}
}
