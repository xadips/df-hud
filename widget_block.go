//go:build linux && cgo && !nolayershell

package main

import (
	"strings"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// Block Info, priority #1. What it can say today, and why:
//
//   - Where you are, from df_positionx/y - server-supplied, so no OCR anywhere.
//   - Which outpost, when the coordinates match one of the seven (the table is
//     the game's own, cross-checked against DFProfiler).
//   - Which region, from df_tradezone via the game's own tradezoneNamer.
//   - The danger level as a bare number, since the client never renders it and
//     its scale is unknown.
//   - The block support countdown, from df_block_support_until.
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
	if !v.HaveData || !v.HasPosition {
		w.box.SetVisible(false)
		return
	}
	w.box.SetVisible(true)

	// Headline: the most specific name available for where you are.
	switch {
	case v.InOutpost && v.OutpostName != "":
		w.head.SetText(v.OutpostName)
	case v.InOutpost:
		w.head.SetText("Outpost")
	default:
		w.head.SetText(formatPosition(v.PositionX, v.PositionY, v.PositionZ))
	}

	// Sub-line: the details worth having, omitted rather than padded when
	// unknown. An empty label is hidden so the HUD does not carry a blank row.
	var parts []string
	if v.InOutpost && w.cfg.ShowCoords {
		parts = append(parts, formatPosition(v.PositionX, v.PositionY, v.PositionZ))
	}
	if !v.InOutpost && v.ZoneName != "" {
		parts = append(parts, v.ZoneName)
	}
	if !v.InOutpost && v.HasDanger {
		parts = append(parts, "danger "+formatDangerLevel(v.DangerLevel))
	}
	if support := formatCountdown(v.BlockSupport); support != "" {
		parts = append(parts, "support "+support)
	}

	sub := strings.Join(parts, "  ")
	w.sub.SetText(sub)
	w.sub.SetVisible(sub != "")
}
