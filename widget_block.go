//go:build linux && cgo && !nolayershell

package main

import "github.com/diamondburned/gotk4/pkg/gtk/v4"

// Block Info, priority #1. What it can say today, and why:
//
//   - Where you are, from df_positionx/y - server-supplied, so no OCR anywhere.
//   - Which outpost, when the coordinates match one of the seven (the table is
//     the game's own, cross-checked against DFProfiler).
//   - Which region, from df_tradezone via the game's own tradezoneNamer.
//   - The block support countdown, from df_block_support_until.
//   - What is standing on the block: bosses, bandits, missions and QRF events,
//     from the city event feed (see bossmap.go). This is the one line that comes
//     from outside the game's own API, and the only way to know before you can
//     see them.
//
// What it cannot say yet: the neighbourhood name and the building on your block.
// Those need the position-to-grid transform, which is unsolved - see
// knowledge/allstats-map-and-xp.md. The widget degrades to the above rather than
// guessing, and the extra lines appear once a transform exists.
type blockWidget struct {
	cfg  BlockWidgetConfig
	box  *gtk.Box
	head *gtk.Label
	sub  *gtk.Label
	// attack is separate from the threat rows because it is a different claim with
	// a different colour: the attack is map-wide, the threats are your own block.
	// Sharing a label meant the attack's colour was applied to both.
	attack *gtk.Label
	// threats is one label per enemy type, grown on demand. A boss nest can carry
	// seven, and they arrive and leave as you move.
	threats []*gtk.Label
}

func newBlockWidget(cfg BlockWidgetConfig) *blockWidget {
	w := &blockWidget{
		cfg:    cfg,
		box:    gtk.NewBox(gtk.OrientationVertical, 0),
		head:   newHUDLabel(),
		sub:    newHUDLabel(),
		attack: newHUDLabel(),
	}
	w.attack.AddCSSClass("threat")
	// The loud colour is permanent on this label, since it only ever says one
	// thing. Nothing toggles it, which is what stops the two kinds of row sharing a
	// piece of state.
	w.attack.AddCSSClass("urgent")
	w.box.Append(w.head)
	w.box.Append(w.sub)
	w.box.Append(w.attack)
	return w
}

func (w *blockWidget) Root() gtk.Widgetter { return w.box }

func (w *blockWidget) Update(v *View) {
	head, sub, show := blockLines(v, w.cfg)
	if !show {
		w.box.SetVisible(false)
		return
	}
	w.box.SetVisible(true)
	w.head.SetText(head)
	w.sub.SetText(sub)
	// An empty label would otherwise occupy a row and say nothing.
	w.sub.SetVisible(sub != "")

	attack, showAttack := outpostAttackLine(v)
	w.attack.SetVisible(showAttack)
	if showAttack {
		w.attack.SetText(attack)
	}

	// One row per enemy type, because a boss nest can carry seven at once and a
	// joined line runs off the side of the screen. Rows are created on demand and
	// then reused: this runs every second forever, so surplus rows are hidden
	// rather than destroyed, and a block with fewer bosses than the last one does
	// not rebuild the tree.
	threats := threatLines(v)
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
