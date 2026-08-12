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
	attack  *gtk.Label
	threats []*gtk.Label
	// nearest is which way to walk when this block is empty. Its own label because
	// it is the opposite case from the threat rows: they are never both up.
	nearest *gtk.Label
}

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
	// Above the threat rows in the box, since those are created on demand later.
	// That is not a layout decision: the two are never up at the same time, because
	// this one only exists for a block with nothing on it.
	w.box.Append(w.nearest)
	return w
}

func (w *bossesWidget) Root() gtk.Widgetter { return w.box }

func (w *bossesWidget) Update(v *View) {
	attack, showAttack := outpostAttackLine(v)
	threats := threatLines(v)
	nearest, showNearest := nearestLine(v, w.cfg)

	// With nothing to say at all the whole group goes away rather than leaving an
	// empty box behind.
	if !showAttack && len(threats) == 0 && !showNearest {
		w.box.SetVisible(false)
		return
	}
	w.box.SetVisible(true)

	w.nearest.SetVisible(showNearest)
	if showNearest {
		w.nearest.SetText(nearest)
	}

	w.attack.SetVisible(showAttack)
	if showAttack {
		w.attack.SetText(attack)
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
