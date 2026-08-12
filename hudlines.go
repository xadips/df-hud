package main

import "strings"

// What each widget says, as pure functions of the View.
//
// These live outside the GTK build tag on purpose. The widgets are thin - they
// call these and push the result into a label - so the decisions about what to
// show, what to omit and how to word it are all testable without a display.
// The alternative, a separate "what the HUD would render" helper for tests, tests
// a copy of the logic rather than the logic.

// blockLines is Block Info. show is false when there is nothing worth a row.
//
// The sub-line omits what it does not know rather than padding with placeholders,
// because an empty HUD row costs a line of screen and tells you nothing.
func blockLines(v *View, cfg BlockWidgetConfig) (head, sub string, show bool) {
	if !v.HaveData || !v.HasPosition {
		return "", "", false
	}

	switch {
	case v.InOutpost && v.OutpostName != "":
		head = v.OutpostName
	case v.InOutpost:
		// In an outpost the coordinates match one of the seven, so an unnamed
		// outpost means the table has gone out of date. Say the honest thing.
		head = "Outpost"
	default:
		head = formatPosition(v.PositionX, v.PositionY, v.PositionZ)
	}

	var parts []string
	if v.InOutpost && cfg.ShowCoords {
		parts = append(parts, formatPosition(v.PositionX, v.PositionY, v.PositionZ))
	}
	if !v.InOutpost {
		// The region name comes from df_tradezone via the game's own namer. The
		// neighbourhood name and the building would come from the catalog's grids,
		// but the position-to-grid transform is unsolved, so those lines are
		// simply absent rather than guessed at.
		if v.ZoneName != "" {
			parts = append(parts, v.ZoneName)
		}
		if v.HasDanger {
			parts = append(parts, "danger "+formatDangerLevel(v.DangerLevel))
		}
	}
	if support := formatCountdown(v.BlockSupport); support != "" {
		parts = append(parts, "support "+support)
	}
	return head, strings.Join(parts, "  "), true
}

// xpLine is the XP rate. It renders the reason when there is no rate yet, rather
// than an empty row: "collecting samples" tells you to wait, whereas a blank
// tells you nothing and looks like a bug.
func xpLine(v *View) (text, cssClass string, show bool) {
	if !v.HaveData {
		return "", "", false
	}
	if !v.XPAvailable {
		if v.XPWhy == "" {
			return "", "", false
		}
		return "xp " + v.XPWhy, "", true
	}
	return "xp " + formatRate(v.XPPerHour), v.XPStability.CSSClass(), true
}

// sessionLine is the game-client clock. show is false when the game is not
// running, since a frozen clock reads as a broken one.
func sessionLine(v *View) (string, bool) {
	if !v.GameRunning {
		return "", false
	}
	return formatClock(v.SessionTime), true
}

// hudLines is everything the HUD would render, in order. Used by -print-hud and
// by the tests, so what is asserted is exactly what is drawn.
func hudLines(v *View, cfg *Config) []string {
	var lines []string
	if v.Status != "" {
		lines = append(lines, v.Status)
	}

	type row struct {
		order int
		text  []string
	}
	var rows []row

	if cfg.Widget.Block.Enabled {
		if head, sub, ok := blockLines(v, cfg.Widget.Block); ok {
			text := []string{head}
			if sub != "" {
				text = append(text, sub)
			}
			rows = append(rows, row{cfg.Widget.Block.Order, text})
		}
	}
	if cfg.Widget.Session.Enabled {
		if text, ok := sessionLine(v); ok {
			rows = append(rows, row{cfg.Widget.Session.Order, []string{text}})
		}
	}
	if cfg.Widget.XP.Enabled {
		if text, _, ok := xpLine(v); ok {
			rows = append(rows, row{cfg.Widget.XP.Order, []string{text}})
		}
	}

	// Same ordering rule as buildWidgets, so the printed form matches the drawn
	// one even after someone reorders the config.
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].order < rows[j-1].order; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
	for _, r := range rows {
		lines = append(lines, r.text...)
	}
	return lines
}
