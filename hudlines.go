package main

import (
	"sort"
	"strings"
	"time"
)

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
		// df_dangerlevel is deliberately NOT shown. It is in the record, but the
		// game's own client never renders it, so its scale is unknown - there is no
		// way to tell whether 0 is safe or unmeasured, and "danger 0" on every row
		// is a number nobody can act on. It stays in the View for whenever its
		// meaning is established. See knowledge/player-record-and-signing.md.
	}
	if support := formatCountdown(v.BlockSupport); support != "" {
		parts = append(parts, "support "+support)
	}
	return head, strings.Join(parts, "  "), true
}

// threatLine is what is standing on your block, from the city event feed.
//
// This is the row the HUD exists for as much as any other: a block with six
// bandits on it is a different proposition from an empty one, and the game's own
// client does not tell you until you are looking at them. urgent picks the colour
// - an outpost attack is map-wide and gets the loud one.
//
// Nothing is rendered when the feed has no events for your block. That is the
// normal case for most of the map, and "nothing here" on every block would train
// you to stop reading the line.
func threatLine(v *View) (text string, urgent bool, show bool) {
	if !v.HaveData {
		return "", false, false
	}
	var parts []string
	if v.OutpostAttack {
		parts = append(parts, "OUTPOST ATTACK")
	}
	for _, e := range v.BlockEvents {
		label := e.Label()
		if len(e.Objectives) > 0 {
			label += " (" + strings.Join(e.Objectives, ", ") + ")"
		}
		parts = append(parts, label)
	}
	// Last cycle's, which the store fills only in Onslaught. Prefixed rather than
	// mixed in: a boss that may or may not still be standing there is a different
	// claim from one that is.
	for _, e := range v.BlockEventsPast {
		parts = append(parts, "last: "+e.Label())
	}
	if len(parts) == 0 {
		return "", false, false
	}
	return strings.Join(parts, "  "), v.OutpostAttack, true
}

// xpLine is the XP rate, and nothing else.
//
// When there is no rate the row disappears rather than explaining itself.
// "collecting samples" was on screen for the first thirty seconds of every run
// and after every window reset, which is a progress report on df-hud's internals
// on a HUD whose whole job is to be glanceable. The reason still exists in
// View.XPWhy, where -print-view and the tray tooltip can show it to whoever is
// actually debugging.
func xpLine(v *View) (text, cssClass string, show bool) {
	if !v.HaveData || !v.XPAvailable {
		return "", "", false
	}
	return "xp " + formatRate(v.XPPerHour), v.XPStability.CSSClass(), true
}

// challengeLines is the pinned challenges, one row each.
//
// The HUD shows only pinned ones: a board of a dozen would bury everything else,
// and the console window exists for the full list. Pins are matched by NAME
// because both the index and the end time rotate every cycle - a pin stored by
// either would silently follow a different challenge next week.
//
// When nothing is pinned it falls back to whatever is closest to completion among
// the ones you have actually started. That is the useful default: it answers
// "what can I finish right now?" without anyone configuring anything.
func challengeLines(v *View, cfg ChallengesWidgetConfig) []string {
	if len(v.Pinned) == 0 {
		if v.ChallengeStatus != "" {
			return []string{"challenges: " + v.ChallengeStatus}
		}
		return nil
	}
	lines := make([]string, 0, len(v.Pinned))
	for _, c := range v.Pinned {
		score, target := c.Progress()
		mark := ""
		if c.Complete() {
			mark = " done"
		}
		row := c.Name + "  " + formatInt(score) + "/" + formatInt(target) + mark
		if remaining := c.Remaining(v.Now); remaining > 0 && remaining < 24*time.Hour {
			// Only show the countdown when it is close enough to matter; "5d"
			// on every row is noise.
			row += "  " + formatCountdown(remaining)
		}
		lines = append(lines, row)
	}
	return lines
}

// pickPinned resolves which challenges the HUD shows.
func pickPinned(board []Challenge, pins []string, cfg ChallengesWidgetConfig) []Challenge {
	if len(board) == 0 {
		return nil
	}
	max := cfg.MaxShown
	if max < 1 {
		max = 1
	}

	var out []Challenge
	if len(pins) > 0 {
		// Pinned order follows the pin list, not the board, so the HUD layout is
		// stable and under the user's control.
		for _, name := range pins {
			for _, c := range board {
				if c.Name == name {
					if !cfg.ShowClan && c.Clan {
						continue
					}
					out = append(out, c)
					break
				}
			}
			if len(out) >= max {
				break
			}
		}
		return out
	}

	// Nothing pinned: the ones closest to done, among those already started.
	// Complete ones are dropped - they need no attention.
	var started []Challenge
	for _, c := range board {
		if !cfg.ShowClan && c.Clan {
			continue
		}
		if c.Started() && !c.Complete() {
			started = append(started, c)
		}
	}
	sort.SliceStable(started, func(i, j int) bool {
		return challengeFraction(started[i]) > challengeFraction(started[j])
	})
	if len(started) > max {
		started = started[:max]
	}
	return started
}

// challengeFraction is overall progress across every objective, for ranking.
func challengeFraction(c Challenge) float64 {
	score, target := c.Progress()
	if target <= 0 {
		return 0
	}
	if f := float64(score) / float64(target); f < 1 {
		return f
	}
	return 1
}

// sessionLine is the run clock: time in the inner city.
//
// show is false whenever there is no run - the game closed, or you are standing
// in an outpost. A clock that keeps counting while you shop reads as a broken
// clock, and one that counts the launcher's loading screen is worse: it is
// confidently wrong about the only thing it claims to measure.
func sessionLine(v *View) (string, bool) {
	if !v.GameRunning || !v.HasSession {
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
	if cfg.Widget.Block.Enabled {
		if text, _, ok := threatLine(v); ok {
			// Shares the block widget's order: it is information about the block
			// you are on, and it belongs next to the block's name.
			rows = append(rows, row{cfg.Widget.Block.Order, []string{text}})
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
	if cfg.Widget.Challenges.Enabled {
		if text := challengeLines(v, cfg.Widget.Challenges); len(text) > 0 {
			rows = append(rows, row{cfg.Widget.Challenges.Order, text})
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
