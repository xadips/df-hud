//go:build linux && cgo && !nolayershell

package gtk

import (
	"df-hud/internal/hud/render"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// Block Info, priority #1. What it can say today, and why:
//
//   - Where you are, from df_positionx/y - server-supplied, so no OCR anywhere.
//   - Which outpost, when the coordinates match one of the seven (the table is
//     the game's own, cross-checked against DFProfiler).
//   - Which region, from df_tradezone via the game's own tradezoneNamer.
//   - The block support countdown, from df_block_support_until.
//
// What is standing on the block - bosses, bandits, missions, QRF events - used to
// be here too. It is its own widget now (widget_bosses.go): it comes from somebody
// else's server rather than the game's record, and it is a list that can be seven
// rows long, so sharing a group meant the position line moved as you travelled.
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
}

func newBlockWidget(cfg BlockWidgetConfig) *blockWidget {
	w := &blockWidget{
		cfg:  cfg,
		box:  gtk.NewBox(gtk.OrientationVertical, 0),
		head: newHUDLabel(),
		sub:  newHUDLabel(),
	}
	w.box.Append(w.head)
	w.box.Append(w.sub)
	return w
}

func (w *blockWidget) Root() gtk.Widgetter { return w.box }

func (w *blockWidget) Update(v *View) {
	head, sub, show := render.BlockLines(v, w.cfg)
	if !show {
		w.box.SetVisible(false)
		return
	}
	w.box.SetVisible(true)
	w.head.SetText(head)
	w.sub.SetText(sub)
	// An empty label would otherwise occupy a row and say nothing.
	w.sub.SetVisible(sub != "")
}
