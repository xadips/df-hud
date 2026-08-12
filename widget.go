//go:build linux && cgo && !nolayershell

package main

import (
	"sort"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// The widget seam. Adding a HUD element means one new widget_*.go implementing
// this interface plus one entry in buildWidgets and one config table - nothing
// else in df-hud changes.
//
// Update receives a *View: plain data, already derived, with every
// time-dependent value computed. A widget therefore never issues a request,
// never reads the store, never locks, and never sees the wire format. That is
// what makes the traffic budget a property of the poller alone.
type Widget interface {
	// Root is the GTK widget to pack into the HUD.
	Root() gtk.Widgetter
	// Update renders one frame. Called on the GTK main thread roughly once a
	// second, so it must not block.
	Update(v *View)
}

// buildWidgets returns the enabled widgets in display order. Order is a sort
// key rather than an index, so the config's gaps of ten are deliberate: a widget
// can be moved by editing one number without renumbering the rest.
func buildWidgets(cfg *Config) []Widget {
	type entry struct {
		order int
		name  string
		w     Widget
	}
	var entries []entry

	if cfg.Widget.Block.Enabled {
		entries = append(entries, entry{cfg.Widget.Block.Order, "block", newBlockWidget(cfg.Widget.Block)})
	}
	if cfg.Widget.Session.Enabled {
		entries = append(entries, entry{cfg.Widget.Session.Order, "session", newSessionWidget()})
	}
	if cfg.Widget.XP.Enabled {
		entries = append(entries, entry{cfg.Widget.XP.Order, "xp", newXPWidget()})
	}

	// Ties keep their declaration order, so an unset order does something
	// predictable rather than arbitrary.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].order < entries[j].order })

	out := make([]Widget, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.w)
	}
	return out
}

// newHUDLabel is the standard HUD text row: left aligned, markup enabled so a
// widget can colour part of a line, and no wrapping - a HUD line that reflows
// makes the whole surface jump around as values change width.
func newHUDLabel() *gtk.Label {
	label := gtk.NewLabel("")
	label.SetUseMarkup(true)
	label.SetXAlign(0)
	label.SetWrap(false)
	label.SetSingleLineMode(true)
	return label
}
