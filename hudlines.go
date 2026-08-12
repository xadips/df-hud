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

// outpostAttackLine is the map-wide siege, on its own row.
//
// Separate from threatLine, and it has to be. They answer different questions -
// "is the outpost under attack" is true everywhere on the map, while the threat
// line is what is standing where you are - and while they shared a row the
// attack's loud colour was applied to the whole thing, so six bandits on your
// block were painted the colour of an event happening somewhere else entirely.
func outpostAttackLine(v *View) (text string, show bool) {
	if !v.HaveData || !v.OutpostAttack {
		return "", false
	}
	return "OUTPOST ATTACK", true
}

// threatLine is what is standing on your block, from the city event feed.
//
// This is the row the HUD exists for as much as any other: a block with six
// bandits on it is a different proposition from an empty one, and the game's own
// client does not tell you until you are looking at them.
//
// Nothing is rendered when the feed has no events for your block. That is the
// normal case for most of the map, and "nothing here" on every block would train
// you to stop reading the line.
func threatLine(v *View) (text string, show bool) {
	if !v.HaveData {
		return "", false
	}
	var parts []string
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
		return "", false
	}
	return strings.Join(parts, "  "), true
}

// xpLine is the XP rate, and nothing else.
//
// When there is no rate the row disappears rather than explaining itself.
// "collecting samples" was on screen for the first thirty seconds of every run
// and after every window reset, which is a progress report on df-hud's internals
// on a HUD whose whole job is to be glanceable. The reason still exists in
// View.XPWhy, where -print-view and the tray tooltip can show it to whoever is
// actually debugging.
func xpLine(v *View, cfg XPWidgetConfig) (text, cssClass string, show bool) {
	if !v.HaveData || !v.XPAvailable {
		return "", "", false
	}
	return cfg.Prefix + formatRate(v.XPPerHour), v.XPStability.CSSClass(), true
}

// challengeCategory is which switch in the config decides whether a challenge is
// shown. Three sources, plus completion as a filter that cuts across all of them.
type challengeCategory int

const (
	// categoryRepeatable is a limited-time event challenge. The wire marks these
	// `repeatable`, verified against a live board: 1 on exactly the three Summer
	// ones and 0 on every daily, weekly and clan entry. The name is not used -
	// "Summer" is this event, not the concept.
	categoryRepeatable challengeCategory = iota
	categoryClan
	categoryPersonal
)

func categoryOf(c Challenge) challengeCategory {
	switch {
	case c.Clan:
		// Checked before Repeatable, because a repeatable clan challenge is still
		// the clan's business and belongs under the switch that says so.
		return categoryClan
	case c.Repeatable:
		return categoryRepeatable
	default:
		return categoryPersonal
	}
}

// showChallenge applies the config's switches to one challenge.
func showChallenge(c Challenge, cfg ChallengesWidgetConfig) bool {
	if c.Complete() && !cfg.ShowCompleted {
		return false
	}
	switch categoryOf(c) {
	case categoryRepeatable:
		return cfg.ShowRepeatable
	case categoryClan:
		return cfg.ShowClan
	default:
		return cfg.ShowPersonal
	}
}

// filterChallenges is the board, minus the categories that are switched off.
//
// The board's own order is kept. It arrives grouped - event, then weeklies, then
// dailies, then clan - and sorting it by progress or by deadline would reshuffle
// the rows under you as scores change, which on a HUD you read at a glance is
// worse than an order that is merely arbitrary but fixed.
func filterChallenges(board []Challenge, cfg ChallengesWidgetConfig) []Challenge {
	out := make([]Challenge, 0, len(board))
	for _, c := range board {
		if !showChallenge(c, cfg) {
			continue
		}
		out = append(out, c)
		if cfg.MaxShown > 0 && len(out) >= cfg.MaxShown {
			break
		}
	}
	return out
}

// challengeLines is the board, one row each.
//
// This used to show only pinned challenges, on the theory that a dozen rows would
// bury everything else. That was a consequence of every group sharing one corner:
// with the board in its own place on screen there is nothing to bury, so the whole
// thing is shown and the category switches decide what is worth a row.
func challengeLines(v *View, cfg ChallengesWidgetConfig) []string {
	shown := filterChallenges(v.Challenges, cfg)
	if len(shown) == 0 {
		// The status only replaces the board when there is no board at all. With
		// rows on screen it would be a second explanation of something already
		// visible, and with everything filtered out it would read as an error.
		if len(v.Challenges) == 0 && v.ChallengeStatus != "" {
			return []string{"challenges: " + v.ChallengeStatus}
		}
		return nil
	}
	lines := make([]string, 0, len(shown))
	for _, c := range shown {
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

// sessionLine is the run clock: time in the inner city.
//
// show is false whenever there is no run - the game closed, or you are standing
// in an outpost. A clock that keeps counting while you shop reads as a broken
// clock, and one that counts the launcher's loading screen is worse: it is
// confidently wrong about the only thing it claims to measure.
func sessionLine(v *View, cfg SessionWidgetConfig) (string, bool) {
	if !v.GameRunning || !v.HasSession {
		return "", false
	}
	return cfg.Prefix + formatClock(v.SessionTime), true
}

// hudLines is everything the HUD would render, in order. Used by -print-hud and
// by the tests, so what is asserted is exactly what is drawn.
func hudLines(v *View, cfg *Config) []string {
	var lines []string
	if v.Status != "" {
		lines = append(lines, v.Status)
	}

	// Groups now carry a position rather than a sort key, so "in order" means
	// reading order: down the screen, then across. That is only an approximation of
	// what the eye does with four groups in four corners, but it is a deterministic
	// one, and this exists so -print-hud and the tests see what is drawn.
	type row struct {
		place Placement
		text  []string
	}
	var rows []row

	if cfg.Widget.Block.Enabled {
		block := cfg.Widget.Block.Placement
		if head, sub, ok := blockLines(v, cfg.Widget.Block); ok {
			text := []string{head}
			if sub != "" {
				text = append(text, sub)
			}
			rows = append(rows, row{block, text})
		}
		// Both belong to the block group: they are information about where you are
		// standing. The attack goes first because it is the one that ends your run
		// if you ignore it.
		if text, ok := outpostAttackLine(v); ok {
			rows = append(rows, row{block, []string{text}})
		}
		if text, ok := threatLine(v); ok {
			rows = append(rows, row{block, []string{text}})
		}
	}
	if cfg.Widget.Session.Enabled {
		if text, ok := sessionLine(v, cfg.Widget.Session); ok {
			rows = append(rows, row{cfg.Widget.Session.Placement, []string{text}})
		}
	}
	if cfg.Widget.XP.Enabled {
		if text, _, ok := xpLine(v, cfg.Widget.XP); ok {
			rows = append(rows, row{cfg.Widget.XP.Placement, []string{text}})
		}
	}
	if cfg.Widget.Challenges.Enabled {
		if text := challengeLines(v, cfg.Widget.Challenges); len(text) > 0 {
			rows = append(rows, row{cfg.Widget.Challenges.Placement, text})
		}
	}

	// Stable, so the several rows that share the block group's position keep the
	// order they were added in rather than being shuffled against each other.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].place.Y != rows[j].place.Y {
			return rows[i].place.Y < rows[j].place.Y
		}
		return rows[i].place.X < rows[j].place.X
	})
	for _, r := range rows {
		lines = append(lines, r.text...)
	}
	return lines
}
