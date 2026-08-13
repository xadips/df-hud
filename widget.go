//go:build linux && cgo && !nolayershell

package main

import "github.com/diamondburned/gotk4/pkg/gtk/v4"

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

// placedWidget is a widget and where it goes.
//
// Position replaced a sort key when the HUD stopped being one stack of rows in a
// corner. The name is not decoration: it becomes the group's CSS class, which is
// how a per-group font override reaches the labels.
type placedWidget struct {
	name  string
	place Placement
	w     Widget
}

// buildWidgets returns the enabled widgets with their placement.
func buildWidgets(cfg *Config) []placedWidget {
	var out []placedWidget
	if cfg.Widget.Block.Enabled {
		out = append(out, placedWidget{"block", cfg.Widget.Block.Placement, newBlockWidget(cfg.Widget.Block)})
	}
	if cfg.Widget.Bosses.Enabled {
		out = append(out, placedWidget{"bosses", cfg.Widget.Bosses.Placement,
			newBossesWidget(cfg.Widget.Bosses)})
	}
	if cfg.Widget.Session.Enabled {
		out = append(out, placedWidget{"session", cfg.Widget.Session.Placement,
			newSessionWidget(cfg.Widget.Session)})
	}
	if cfg.Widget.XP.Enabled {
		out = append(out, placedWidget{"xp", cfg.Widget.XP.Placement, newXPWidget(cfg.Widget.XP)})
	}
	if cfg.Widget.Map.Enabled {
		out = append(out, placedWidget{"map", cfg.Widget.Map.Placement, newMapWidget(cfg.Widget.Map)})
	}
	if cfg.Widget.Challenges.Enabled {
		out = append(out, placedWidget{"challenges", cfg.Widget.Challenges.Placement,
			newChallengeWidget(cfg.Widget.Challenges)})
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
