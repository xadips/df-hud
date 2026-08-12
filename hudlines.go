package main

import (
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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
	case cfg.ShowPosition:
		head = formatPosition(v.PositionX, v.PositionY, v.PositionZ)
	default:
		// The game prints your coordinates under its own minimap, so repeating them
		// an inch away is not information. The region is not shown anywhere in the
		// game, so with the coordinates off it takes the head instead of the row
		// below - which also saves a row.
		head = v.ZoneName
	}

	var parts []string
	if v.InOutpost && cfg.ShowPosition {
		parts = append(parts, formatPosition(v.PositionX, v.PositionY, v.PositionZ))
	}
	if !v.InOutpost {
		// The region name comes from df_tradezone via the game's own namer. The
		// neighbourhood name and the building would come from the catalog's grids,
		// but the position-to-grid transform is unsolved, so those lines are
		// simply absent rather than guessed at.
		if v.ZoneName != "" && head != v.ZoneName {
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

// threatLines is what is standing on your block, from the city event feed, ONE
// ROW PER ENEMY TYPE.
//
// This is the part of the HUD the whole bossmap exists for: a block with six
// bandits on it is a different proposition from an empty one, and the game's own
// client does not tell you until you are looking at them.
//
// It returns rows rather than a joined line because a boss nest is not rare. A
// live one carried seven types at once:
//
//	1 x Evolved Longarms / 1 x Irradiated Titan / 1 x Irradiated Mother /
//	1 x Irradiated Giant Spider / 2 x Mega Wraith / 1 x Charred Mother /
//	1 x Charred Giant Spider
//
// which as a single string is around 140 characters. It cannot be read at a
// glance, and at this group's position it ran off the side of the screen, so the
// tail of it - the part naming what is actually dangerous - was the part that got
// clipped.
//
// Nothing is rendered when the feed has no events for your block. That is the
// normal case for most of the map, and "nothing here" on every block would train
// you to stop reading it.
func threatLines(v *View) []string {
	if !v.HaveData {
		return nil
	}
	var rows []string
	for _, e := range v.BlockEvents {
		rows = append(rows, eventRows(e, "")...)
	}
	// Last cycle's, which the store fills only in Onslaught. Prefixed rather than
	// mixed in: a boss that may or may not still be standing there is a different
	// claim from one that is.
	for _, e := range v.BlockEventsPast {
		rows = append(rows, eventRows(e, "last: ")...)
	}
	return rows
}

// eventRows breaks one event into its rows.
//
// A plain spawn is nothing but its enemy list, so its rows ARE the enemies and a
// title would repeat them. A mission or a QRF has a name worth its own row, and
// then any enemies it brings underneath - a mission also carries a
// special_enemy_type, so those two are not alternatives.
func eventRows(e CityEvent, prefix string) []string {
	var rows []string
	if e.Kind != EventSpawn || len(e.Enemies) == 0 {
		rows = append(rows, prefix+e.Label())
	}
	for _, enemy := range e.Enemies {
		rows = append(rows, prefix+enemy)
	}
	if len(e.Objectives) > 0 {
		rows = append(rows, prefix+"("+strings.Join(e.Objectives, ", ")+")")
	}
	return rows
}

// nearestReportRange caps how far away an event is still worth reporting, in block
// moves. Past a dozen blocks it is not somewhere you are about to walk, and the row
// would be permanent furniture rather than information.
const nearestReportRange = 12

// nearestLine is which way to walk when your own block is empty.
//
// The direction is given in blocks and then repeated as the target block's own
// coordinates. Both, on purpose: the words are what you read at a glance, and the
// coordinates are what the game itself shows you, so they are both the actionable
// form and the way to catch this being wrong.
//
// The distance is the WALK and can be longer than the directions add up to, because
// the city has gaps you cannot cross. When it is longer the row says so - "5 up
// 2 left, 9 blocks" - since that difference is the whole reason the direct line is
// not the route, and hiding it would be the same lie as before, told more precisely.
//
// UP IS y DECREASING. That is inferred rather than verified - DFProfiler's map is
// an HTML table whose rows are y, so the smallest y renders topmost (bossmap.js) -
// and it is the one claim here that could send someone the wrong way. The
// coordinates beside it are the check: walk one block and see which number moves.
func nearestLine(v *View, cfg BossesWidgetConfig) (string, bool) {
	if !cfg.ShowNearest || !v.HaveData || !v.HasNearest {
		return "", false
	}
	if v.NearestDistanceInBlocks > nearestReportRange {
		return "", false
	}
	var parts []string
	switch {
	case v.NearestDY < 0:
		parts = append(parts, strconv.Itoa(-v.NearestDY)+" up")
	case v.NearestDY > 0:
		parts = append(parts, strconv.Itoa(v.NearestDY)+" down")
	}
	switch {
	case v.NearestDX < 0:
		parts = append(parts, strconv.Itoa(-v.NearestDX)+" left")
	case v.NearestDX > 0:
		parts = append(parts, strconv.Itoa(v.NearestDX)+" right")
	}
	if len(parts) == 0 {
		return "", false
	}
	line := "nearest " + strings.Join(parts, " ")
	if v.NearestDetour > 0 {
		line += ", " + strconv.Itoa(v.NearestDistanceInBlocks) + " blocks"
	}
	return line + "  " + strconv.Itoa(v.NearestX) + ", " + strconv.Itoa(v.NearestY), true
}

// xpPending stands in for the rate until there are two samples to subtract, which
// is one poll interval. xpRough marks a rate computed from fewer samples than
// min_samples: correct arithmetic on thin evidence.
const (
	xpPending = "--"
	xpRough   = "~"
)

// xpLine is the XP rate, and nothing else.
//
// A rate appears as soon as there are two samples to subtract, marked with a tilde
// until the window holds min_samples. That is the third arrangement of this row and
// the first that behaves; the other two are worth recording because both looked
// reasonable:
//
//   - "collecting samples" in place of the number was a progress report on
//     df-hud's internals, on a HUD whose whole job is to be glanceable.
//   - hiding the row until the window filled, which replaced it, and which reads as
//     the HUD being broken. Every run start clears the window, so this happened
//     after every single press of Start: the row vanished for half a minute and
//     then came back. It was reported as a bug within the hour, and rightly - a
//     blank where a number lives looks nothing like a number that is not ready.
//
// So the number arrives as early as arithmetic allows and says how much to trust
// itself. Only the first interval of a run has nothing at all to show, and that
// gets dashes to hold the row's place.
//
// Neither dashes nor a provisional rate carries a stability colour: the amber and
// red mean "recent polls did not land", which is a different complaint from "there
// have not been many polls yet", and one colour cannot say both.
func xpLine(v *View, cfg XPWidgetConfig) (text, cssClass string, show bool) {
	if !v.HaveData {
		return "", "", false
	}
	if !v.XPAvailable {
		return cfg.Prefix + xpPending, "", true
	}
	if v.XPProvisional {
		return cfg.Prefix + xpRough + formatRate(v.XPPerHour), "", true
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
func challengeLines(v *View, cfg ChallengesWidgetConfig) []challengeRow {
	shown := filterChallenges(v.Challenges, cfg)
	if len(shown) == 0 {
		// The status only replaces the board when there is no board at all. With
		// rows on screen it would be a second explanation of something already
		// visible, and with everything filtered out it would read as an error.
		if len(v.Challenges) == 0 && v.ChallengeStatus != "" {
			return []challengeRow{{Name: "challenges: " + v.ChallengeStatus}}
		}
		return nil
	}
	rows := make([]challengeRow, 0, len(shown))
	for _, c := range shown {
		rows = append(rows, challengeRows(c, v.Now, cfg)...)
	}

	// Two passes over the finished set, because both of these are properties of the
	// board rather than of any one challenge: where the progress column falls, and
	// which rows begin a new challenge under another one.
	pad := 0
	for _, r := range rows {
		// Countdowns count towards the column as well as progress figures. They are
		// the value on a challenge row that has no progress of its own, and leaving
		// them out put them in a column of their own halfway across the board.
		if r.Progress == "" && r.Countdown == "" {
			continue
		}
		if width := utf8.RuneCountInString(r.label()); width > pad {
			pad = width
		}
	}
	for i := range rows {
		rows[i].Pad = pad
		// Not the first: a margin above row zero would push the whole group down
		// from the y it was configured at.
		rows[i].Gap = i > 0 && !rows[i].Sub
	}
	return rows
}

// challengeRow is one row of the board, kept in parts rather than pre-joined.
//
// The parts exist because the three of them answer different questions and want
// to look different: the name says which challenge, the objective says what to
// do, the progress says how far. Flattened into one string they were a wall of
// same-weight text - and with a weekly carrying four objectives, an unreadable
// one.
//
// Text() is the plain form for -print-hud and the tests; Markup() is what the HUD
// draws. Keeping both off one structure is what stops the two forms drifting.
type challengeRow struct {
	Name      string
	Objective string
	Progress  string
	Countdown string
	// Done draws the row struck through and green. A finished challenge is still
	// worth a row - it is how you know not to chase it - but it should read as
	// settled rather than competing with the ones that are not.
	Done bool
	// Urgent draws the row in the alarm colour: unfinished, and close enough to its
	// deadline that it is now or never. Never set together with Done, since a
	// finished challenge does not care what time it is.
	Urgent bool
	// Sub marks an objective belonging to the challenge above it, which is indented
	// and dimmed so the grouping is visible.
	Sub bool
	// Gap asks for a few pixels above the row. Set on every challenge except the
	// first, which turns nineteen unbroken rows into visible groups without
	// spending a whole blank row on each break.
	Gap bool
	// Pad is the column the progress figures line up in, measured in characters
	// from the start of the row. Set across the whole board at once by
	// challengeLines, since a column is a property of the set rather than of a row.
	Pad int
}

// label is everything before the progress figure: the indent, and the name and
// objective in whichever combination this row carries.
func (r challengeRow) label() string {
	var b strings.Builder
	if r.Sub {
		b.WriteString("  ")
	}
	b.WriteString(r.Name)
	if r.Objective != "" {
		if r.Name != "" {
			b.WriteString(": ")
		}
		b.WriteString(r.Objective)
	}
	return b.String()
}

// padding is the spaces that put this row's progress in the shared column. Two
// spaces minimum, so the longest label still has a gap after it.
//
// Characters, not pixels, which is exact in the monospace font the HUD defaults to
// and approximate in anything else. It is also why the objective is no longer
// rendered a step smaller: a narrower character makes the same count of them a
// different width, and the column would drift on exactly the rows it is meant to
// line up.
func (r challengeRow) padding(label string) string {
	width := 2
	if extra := r.Pad - utf8.RuneCountInString(label); extra > 0 {
		width += extra
	}
	return strings.Repeat(" ", width)
}

// Text is the plain form, for -print-hud and the tests.
func (r challengeRow) Text() string {
	var b strings.Builder
	label := r.label()
	b.WriteString(label)
	switch {
	case r.Progress != "":
		b.WriteString(r.padding(label) + r.Progress)
		if r.Countdown != "" {
			b.WriteString("  " + r.Countdown)
		}
	case r.Countdown != "":
		// Into the same column the progress figures use, since on this row it is the
		// value.
		b.WriteString(r.padding(label) + r.Countdown)
	}
	if r.Done {
		// Said in words here because plain text has no strikethrough to say it with.
		b.WriteString(" done")
	}
	return b.String()
}

// Markup is the drawn form, in Pango markup.
//
// Everything here is deliberately colour-free: weight, size, alpha and
// strikethrough only. A hardcoded colour would fight both the per-group color key
// and the state colours, so the hierarchy is built out of the attributes that
// compose with whatever colour the text already has.
//
//	progress   bold, because it is what you scan the board for
//	name       plain
//	objective  dimmed: subordinate to the challenge it belongs to
//	countdown  dimmed, same reason
//	done       struck through, and the whole row dimmed
//
// The objective was also a step smaller until the progress figures were lined up
// into a column. The two cannot both be had: the column is built out of padding
// characters, and a narrower character makes the same count of them a different
// width. Dimming carries the hierarchy on its own.
func (r challengeRow) Markup() string {
	var b strings.Builder
	if r.Sub {
		b.WriteString("  ")
	}
	if r.Done {
		b.WriteString(`<span alpha="60%"><s>`)
	}
	b.WriteString(escapeMarkup(r.Name))
	if r.Objective != "" {
		if r.Name != "" {
			b.WriteString(": ")
		}
		b.WriteString(`<span alpha="78%">` + escapeMarkup(r.Objective) + `</span>`)
	}
	// The padding goes outside the objective's span, so it is never rendered at a
	// different size from the padding on the row above it.
	countdown := `<span alpha="70%">` + escapeMarkup(r.Countdown) + `</span>`
	switch {
	case r.Progress != "":
		b.WriteString(r.padding(r.label()) + "<b>" + escapeMarkup(r.Progress) + "</b>")
		if r.Countdown != "" {
			b.WriteString("  " + countdown)
		}
	case r.Countdown != "":
		b.WriteString(r.padding(r.label()) + countdown)
	}
	if r.Done {
		// No "done" in words: the strikethrough is the word.
		b.WriteString(`</s></span>`)
	}
	return b.String()
}

// CSSClass is the row's colour, or empty for the group's normal one.
//
// A class rather than a colour in the markup, for two reasons: the built-in sheet
// scopes state colours so they outrank a per-group color key (which is exactly
// what these are), and a class can be restyled from hud.css without touching Go.
func (r challengeRow) CSSClass() string {
	switch {
	case r.Done:
		return "done"
	case r.Urgent:
		return "expiring"
	}
	return ""
}

// Classes is every class the row wants, colour and spacing together, so the widget
// can diff one list instead of tracking each kind separately.
func (r challengeRow) Classes() []string {
	var out []string
	if c := r.CSSClass(); c != "" {
		out = append(out, c)
	}
	if r.Gap {
		out = append(out, "board-gap")
	}
	return out
}

// escapeMarkup makes text safe to interpolate into Pango markup.
//
// Not optional. Pango refuses to parse a label whose markup is malformed, and GTK
// answers with a warning and an EMPTY label - so one challenge named with an
// ampersand would silently blank its row. The board's text comes from the game, so
// it is not ours to trust.
func escapeMarkup(s string) string {
	return markupEscaper.Replace(s)
}

// & first, or the escapes would be escaped again.
var markupEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&#39;",
)

// challengeRows renders one challenge as a GROUP: the challenge on one row, its
// objectives indented underneath.
//
// "First Strike  0/7" was the first form, and it does not say what the seven are.
// The objective is the actionable half - "Kill Any Boss" - and a progress figure
// without it is a number you cannot act on.
//
// The name is kept as well as the objective, because dropping it collides: the
// live board carries both "Summer Loot" and "Weekly Challenge - Loot Anything",
// and the objective of each is "Loot Anything". Two identical rows with different
// numbers is worse than one long row.
func challengeRows(c Challenge, now time.Time, cfg ChallengesWidgetConfig) []challengeRow {
	remaining := c.Remaining(now)
	countdown := ""
	if remaining > 0 && remaining < 24*time.Hour {
		// Only when it is close enough to matter; "5d" on every row is noise.
		countdown = formatCountdown(remaining)
	}
	// Not "complete AND close": a finished challenge does not care what time it is,
	// so Done wins and the two flags are never both set.
	urgent := !c.Complete() && cfg.UrgentWithin.Duration > 0 &&
		remaining > 0 && remaining <= cfg.UrgentWithin.Duration

	// One row for the challenge, then its objectives UNDER it, indented. Always,
	// not only when there are several: a weekly with four objectives and a daily
	// with one should look like the same kind of thing, and an objective on the
	// same line as its own challenge reads as part of the name.
	//
	// The exception is a challenge whose name already says what the objective is,
	// which is how every clan entry reads ("Weekly Challenge - Kill Infected"
	// against an objective of "Kill Infected"). There the objective row would be
	// the name again with a number on it, so the number joins the name instead and
	// the challenge stays one row.
	if len(c.Objectives) == 1 && nameCoversObjective(c.Name, c.Objectives[0].Name) {
		score, target := c.Progress()
		return []challengeRow{{
			Name:      c.Name,
			Progress:  formatInt(score) + "/" + formatInt(target),
			Countdown: countdown,
			Done:      c.Complete(),
			Urgent:    urgent,
		}}
	}

	rows := make([]challengeRow, 0, len(c.Objectives)+1)
	rows = append(rows, challengeRow{
		Name: c.Name, Countdown: countdown, Done: c.Complete(), Urgent: urgent,
	})
	for _, o := range c.Objectives {
		rows = append(rows, challengeRow{
			Objective: o.Name,
			Progress:  formatInt(o.Score) + "/" + formatInt(o.Target),
			Done:      o.Done(),
			// The deadline belongs to the challenge, so it colours the whole group -
			// except an objective already finished, which is green on its own account.
			Urgent: urgent && !o.Done(),
			Sub:    true,
		})
	}
	return rows
}

// nameCoversObjective reports whether the challenge's name already says what the
// objective is. Case-insensitive substring, which is all the real board needs:
// the clan entries are named "Weekly Challenge - <objective>" exactly.
func nameCoversObjective(name, objective string) bool {
	if objective == "" {
		return true
	}
	return strings.Contains(strings.ToLower(name), strings.ToLower(objective))
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
	}
	if cfg.Widget.Bosses.Enabled {
		// The attack goes first because it is the one that ends your run if you
		// ignore it.
		bosses := cfg.Widget.Bosses.Placement
		if text, ok := outpostAttackLine(v); ok {
			rows = append(rows, row{bosses, []string{text}})
		}
		if threats := threatLines(v); len(threats) > 0 {
			rows = append(rows, row{bosses, threats})
		}
		if text, ok := nearestLine(v, cfg.Widget.Bosses); ok {
			rows = append(rows, row{bosses, []string{text}})
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
		if board := challengeLines(v, cfg.Widget.Challenges); len(board) > 0 {
			text := make([]string, 0, len(board))
			for _, r := range board {
				text = append(text, r.Text())
			}
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
