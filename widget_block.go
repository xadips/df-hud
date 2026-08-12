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
	cfg    BlockWidgetConfig
	box    *gtk.Box
	head   *gtk.Label
	sub    *gtk.Label
	threat *gtk.Label
}

func newBlockWidget(cfg BlockWidgetConfig) *blockWidget {
	w := &blockWidget{
		cfg:    cfg,
		box:    gtk.NewBox(gtk.OrientationVertical, 0),
		head:   newHUDLabel(),
		sub:    newHUDLabel(),
		threat: newHUDLabel(),
	}
	w.threat.AddCSSClass("threat")
	w.box.Append(w.head)
	w.box.Append(w.sub)
	w.box.Append(w.threat)
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

	threat, urgent, showThreat := threatLine(v)
	w.threat.SetVisible(showThreat)
	if showThreat {
		w.threat.SetText(threat)
		// An outpost attack is map-wide and time-limited, so it gets the loud
		// colour; anything on your own block gets the ordinary warning one.
		if urgent {
			w.threat.AddCSSClass("urgent")
		} else {
			w.threat.RemoveCSSClass("urgent")
		}
	}
}
