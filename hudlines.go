package main

import (
	"fmt"
	"math"
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
// UP IS y DECREASING. Verified in the game on 2026-08-13 by walking one block and
// watching the second coordinate fall - it had until then been inferred from
// DFProfiler's map being an HTML table whose rows are y, which was the one claim in
// this file that could have sent someone the wrong way. The coordinates stay beside
// the words anyway, since they are also what the game's own readout shows.
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

	sections := groupByCategory(shown)
	// A heading is only worth a row when there is something to tell apart. With one
	// section it would be a label on the only thing on screen.
	headings := cfg.ShowSections && len(sections) > 1

	rows := make([]challengeRow, 0, len(shown)+len(sections))
	for _, s := range sections {
		// The clan entries are all named "Weekly Challenge - <objective>", so five of
		// them put those eighteen characters on screen five times and push every
		// figure on the board right by as much. The prefix MOVES into the heading -
		// so only when there is a heading to move it to, or it would not be said at
		// all and "weekly" is not nothing.
		prefix := ""
		if headings {
			prefix = sharedPrefix(s.challenges)
			label := s.category.label()
			if prefix != "" {
				label += " - " + strings.TrimSuffix(strings.TrimSpace(prefix), "-")
			}
			rows = append(rows, challengeRow{Heading: true, Name: strings.TrimSpace(label)})
		}
		for _, c := range s.challenges {
			if prefix != "" {
				c.Name = strings.TrimSpace(strings.TrimPrefix(c.Name, prefix))
			}
			rows = append(rows, challengeRows(c, v.Now, cfg)...)
		}
	}

	// Three passes over the finished set, because all of these are properties of the
	// board rather than of any one row: where the progress column falls, which rows
	// begin a new challenge under another one, and how wide the rule on a heading
	// has to be to reach the far side of the widest row.
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
	board := 0
	for i := range rows {
		rows[i].Pad = pad
		// Not the first: a margin above row zero would push the whole group down
		// from the y it was configured at.
		rows[i].Gap = i > 0 && !rows[i].Sub
		if w := rows[i].width(); w > board {
			board = w
		}
	}
	for i := range rows {
		if rows[i].Heading {
			rows[i].Rule = board
		}
	}
	return rows
}

// challengeSection is one category's worth of the board, in the board's own order.
type challengeSection struct {
	category   challengeCategory
	challenges []Challenge
}

// groupByCategory splits the board into sections, keeping the board's own order:
// sections appear in the order their first challenge does, and challenges keep their
// places within a section.
//
// Not a fixed order of my choosing, tempting as "your own first" is. The board
// arrives grouped already - event, then weeklies, then dailies, then clan - so the
// dividers describe what is on screen rather than rearranging it, and nothing moves
// under someone who has learnt where their dailies sit.
func groupByCategory(board []Challenge) []challengeSection {
	var out []challengeSection
	at := map[challengeCategory]int{}
	for _, c := range board {
		k := categoryOf(c)
		if i, ok := at[k]; ok {
			out[i].challenges = append(out[i].challenges, c)
			continue
		}
		at[k] = len(out)
		out = append(out, challengeSection{category: k, challenges: []Challenge{c}})
	}
	return out
}

// sharedPrefix is the "<something> - " that every challenge in a section begins
// with, or "" if they do not all share one.
//
// Cut at " - " rather than at any common run of characters: two challenges called
// "Kill Infected" and "Kill Dog Infected" share "Kill " and stripping it would
// leave "Infected" and "Dog Infected", which is worse than the repetition. The
// separator makes the prefix a deliberate one rather than a coincidence.
//
// Two is the minimum. One challenge repeats nothing, and taking its prefix away
// would move information into a heading for no gain.
func sharedPrefix(challenges []Challenge) string {
	if len(challenges) < 2 {
		return ""
	}
	const sep = " - "
	i := strings.Index(challenges[0].Name, sep)
	if i <= 0 {
		return ""
	}
	prefix := challenges[0].Name[:i] + sep
	for _, c := range challenges {
		if !strings.HasPrefix(c.Name, prefix) {
			return ""
		}
		// And nothing may be left with an empty name. Checked on the first as well
		// as the rest: it is where the prefix came from, not an exception to it.
		if strings.TrimSpace(strings.TrimPrefix(c.Name, prefix)) == "" {
			return ""
		}
	}
	return prefix
}

func (c challengeCategory) label() string {
	switch c {
	case categoryRepeatable:
		return "event"
	case categoryClan:
		return "clan"
	default:
		return "yours"
	}
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
	// Heading marks a section divider - "yours", "clan", "event" - drawn as the
	// name followed by a rule out to Rule characters wide. A rule made of text
	// rather than a CSS border because a GTK label is only as wide as its own
	// content: a border-bottom would stop at the end of the word.
	Heading bool
	// Rule is how wide the widest row on the board is, so a heading's line reaches
	// the far side of it. Set across the whole board at once, like Pad.
	Rule int
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
	if r.Heading {
		return r.headingText()
	}
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

// headingText is a section divider: the name, then a line to the far side of the
// board. Box-drawing characters, which are one cell wide in any monospace font the
// HUD is likely to be set in, and a plain fallback is not worth carrying - the rule
// degrading to a row of dashes would be indistinguishable from what most people
// would draw by hand anyway.
func (r challengeRow) headingText() string {
	head := "── " + r.Name + " "
	if fill := r.Rule - utf8.RuneCountInString(head); fill > 0 {
		return head + strings.Repeat("─", fill)
	}
	return head
}

// width is how wide the row draws, in characters, ignoring the "done" that only the
// plain form adds. It is what a heading's rule is measured against.
func (r challengeRow) width() int {
	if r.Heading {
		// Headings are what the width is being computed FOR, so they cannot
		// contribute to it - a heading long enough to be the widest row would
		// otherwise pull every other heading out to match it.
		return 0
	}
	label := r.label()
	n := utf8.RuneCountInString(label)
	switch {
	case r.Progress != "":
		n += utf8.RuneCountInString(r.padding(label)) + utf8.RuneCountInString(r.Progress)
		if r.Countdown != "" {
			n += 2 + utf8.RuneCountInString(r.Countdown)
		}
	case r.Countdown != "":
		n += utf8.RuneCountInString(r.padding(label)) + utf8.RuneCountInString(r.Countdown)
	}
	return n
}

// Text is the plain form, for -print-hud and the tests.
func (r challengeRow) Text() string {
	if r.Heading {
		return r.headingText()
	}
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
	if r.Heading {
		// Dimmer than an objective: a divider is furniture, and it should be the
		// least insistent thing on the board while still being findable.
		return `<span alpha="50%">` + escapeMarkup(r.headingText()) + `</span>`
	}
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

// outpostLetters is the identifier each outpost is drawn with on the map, taken from
// DFProfiler's own legend so that anyone who knows their map can read this one.
var outpostLetters = map[string]string{
	"Nastya's Holdout": "N",
	"Dogg's Stockade":  "D",
	"Precinct 13":      "P",
	"Fort Pastor":      "F",
	"Secronom Bunker":  "S",
	"Valcrest":         "C",
	"Ground Zero":      "Z",
}

// mapCellPx is the size of one block in pixels, derived from widget.map.scale and how
// many blocks are on show.
//
// The scale is a budget for the longest side rather than a size per block, which is
// what makes a cropped map bigger rather than merely smaller: the same 1180 pixels
// spread over 31 blocks instead of 59 gives 38px cells instead of 20, so cutting the
// radius zooms in.
//
// Here rather than in the widget because the key's font is derived from it too, and
// the two have to agree - a map that scaled up while its key stayed at 12pt was the
// first version of this, and the reason there is one scale key and not two.
func mapCellPx(cfg MapWidgetConfig) int {
	scale := cfg.Scale
	if scale <= 0 {
		scale = 1
	}
	bw, bh := mapWindowSize(cfg)
	side := max(bw, bh)
	if side <= 0 {
		side = theCity.Width
	}
	return clampInt(int(scale*mapBaseSize)/side, mapMinCell, mapMaxCell)
}

// mapListPt is the key's font size in points, taken from the block size so that one
// scale sizes the whole group rather than just the grid. 0 means "leave it to the
// stylesheet", which is the answer when font_size pinned it by hand.
//
// This is reached through groupStyles, as the map group's derived font size, so it
// lands in the same CSS every other group's font_size does. Computing it and never
// applying it was the first version, and it looked exactly like the bug it was: the
// grid zoomed with the scale and the key stayed at whatever [hud] said.
//
// 0.65pt per pixel of cell puts a 20px map's key at 13pt. That factor was measured by
// looking at it, from both directions: 0.6 was small enough to squint at beside 28px
// blocks, 0.75 was bigger than every other group on the HUD. It stays a little ABOVE
// the marker it explains - the glyph in a cell is cell*0.72 pixels, which works out at
// 0.54pt per pixel - because the two are not the same job: a marker only has to be
// told apart from twenty others, while the key is a column you read while something
// walks towards you.
//
// Rounded to a tenth because that is what reaches the stylesheet anyway (%.1fpt), and
// a value that survives the trip is a value a test can state exactly.
//
// The bounds are sanity, not taste: under 8pt nothing is readable, and over 30pt the key
// is taller than the map it explains.
func mapListPt(cfg MapWidgetConfig) float64 {
	if cfg.FontSize > 0 {
		return 0
	}
	pt := math.Round(0.65*float64(mapCellPx(cfg))*10) / 10
	return math.Min(math.Max(pt, 8), 30)
}

// mapWindow is the part of the city the map draws: an origin in block coordinates
// and a size in blocks. The whole city unless widget.map.radius crops it.
type mapWindow struct{ X, Y, W, H int }

func (w mapWindow) contains(x, y int) bool {
	return x >= w.X && y >= w.Y && x < w.X+w.W && y < w.Y+w.H
}

// mapWindowFor is which blocks to draw, given the config and where the player is.
//
// A radius crops the map to a square around you - radius 15 is 31x31 blocks - which
// is the version worth having while playing: at the full 59x55 most of the city is
// somewhere you are not going, and the part you might walk to is a sixth of the
// picture.
//
// The window is CLAMPED into the city rather than allowed to hang off the edge. That
// keeps its size constant, which matters more than keeping you dead centre: the group
// is centred on the monitor, so a window that shrank near the city's edge would make
// the whole map jump sideways as you walked. Near an edge you are simply off-centre,
// which is what every map does.
//
// Falls back to the whole city when there is no centre to crop around - no position
// yet, or standing in Onslaught, whose 3000,3000 is not a place on this grid. A window
// around a coordinate that is not on the map would be a window around nothing.
func mapWindowFor(v *View, cfg MapWidgetConfig) mapWindow {
	whole := mapWindow{theCity.OriginX, theCity.OriginY, theCity.Width, theCity.Height}
	if cfg.Radius <= 0 || !v.HasPosition || !theCity.IsBlock(v.PositionX, v.PositionY) {
		return whole
	}
	side := 2*cfg.Radius + 1
	if side >= theCity.Width && side >= theCity.Height {
		return whole
	}
	win := mapWindow{W: min(side, theCity.Width), H: min(side, theCity.Height)}
	win.X = clampInt(v.PositionX-cfg.Radius, theCity.OriginX, theCity.OriginX+theCity.Width-win.W)
	win.Y = clampInt(v.PositionY-cfg.Radius, theCity.OriginY, theCity.OriginY+theCity.Height-win.H)
	return win
}

// mapWindowSize is the drawn size in blocks without needing to know where the player
// is, for the widget's size request. Clamping is what makes that possible: the window
// is the same size wherever you stand.
func mapWindowSize(cfg MapWidgetConfig) (w, h int) {
	if cfg.Radius <= 0 {
		return theCity.Width, theCity.Height
	}
	side := 2*cfg.Radius + 1
	return min(side, theCity.Width), min(side, theCity.Height)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// mapRow is one line of the key beside the map.
//
// Marker and Timer are set on an entry's first row and empty on its continuations,
// which are indented under it. Three fields rather than one string because each is
// styled differently, and because the column they line up in only works if the
// renderer knows where one ends and the next begins.
type mapRow struct {
	Marker string
	// Color is the category's colour, as hex - the same one that rings this event's
	// cells on the map, so a chip in the key and a ring on the grid are one lookup.
	Color string
	Timer string
	Text  string
	Sub   bool
}

// mapListLines is the key beside the map: which marker is what, and how long is left.
//
// It exists because the markers on the grid cannot say what they are. A letter in a
// cell is only meaningful next to a list, and the list is also the only place a
// countdown will fit.
//
// ONE ENTRY PER EVENT, not per marked block. The feed puts the same bandit pack on a
// dozen blocks at once - 185 marks from 30 events in one live capture - so a row each
// made the list "+173 more" and told you nothing, with the same enemies and the same
// countdown repeated a dozen times.
//
// Entries are still ORDERED by the nearest of their blocks, so the top of the list is
// what you could reach, but the distance itself is not written: the map is where you
// see where something is, and a number of blocks beside every row was a column of
// figures nobody reads.
//
// One line per event when there is one enemy type, which is most of them. A nest
// carries up to seven, and those get a row each - as a joined label it is 140
// characters and runs off the side of the screen, taking the dangerous part with it.
func mapListLines(v *View, cfg MapWidgetConfig) []mapRow { return mapFrameFor(v, cfg).Rows }

// mapFrame is one frame of the map: which blocks to draw, what to draw on them, and
// the key that explains it.
//
// The three come from one function because they have to agree exactly. The identifier
// on a cell and the identifier in the key are the same lookup, and they are assigned
// HERE rather than upstream in the feed - see below for why that matters.
type mapFrame struct {
	Window mapWindow
	// Marks is every visible location, each carrying this frame's identifier. An event
	// on six blocks appears six times, all with the same character.
	Marks []CityMark
	Rows  []mapRow
}

// mapFrameFor works out what the map shows.
//
// IDENTIFIERS ARE ASSIGNED TO WHAT IS VISIBLE, in the order the key lists them, so the
// nearest thing is always 1. Two reasons, and the second is why it changed:
//
//   - a cropped map showed a sparse scatter of whatever characters the feed's own
//     order had given those events - G, K, Q, V - which reads as arbitrary, because it
//     is. Numbering the visible set means the key runs 1, 2, 3 down the page.
//   - the feed carries about thirty active events, and nine digits plus the capitals
//     that are not already an outpost's letter is twenty-seven. The tail fell to
//     lowercase, so a busy cycle drew a lowercase c beside Camp Valcrest's C. Only a
//     handful are ever visible at once, so numbering the visible set never gets there.
//
// The cost is that a boss's character changes as you walk and the order shifts. That is
// the right trade: the character is a lookup within one glance at one frame, not a name
// for the boss.
func mapFrameFor(v *View, cfg MapWidgetConfig) mapFrame {
	frame := mapFrame{Window: mapWindowFor(v, cfg)}
	if len(v.CityMarks) == 0 {
		return frame
	}

	// Anything off the map has already been filtered out by ActiveMarks unless you
	// are standing in Onslaught - see there for why. Where they do appear they come
	// first, because then they are what is in front of you.
	inOnslaught := v.HasPosition && v.PositionX == onslaughtCoord && v.PositionY == onslaughtCoord

	// The key describes the map that is drawn, so a cropped map gets a cropped key:
	// an event fifty blocks away is not on screen, and listing it would be a row about
	// somewhere you cannot see. Off-map marks are exempt - they are only ever present
	// when you are in Onslaught, and no window contains 3000,3000.
	visible := make([]CityMark, 0, len(v.CityMarks))
	for _, m := range v.CityMarks {
		if m.OffMap || frame.Window.contains(m.X, m.Y) {
			visible = append(visible, m)
		}
	}
	if len(visible) == 0 {
		return frame
	}

	// Collapse to the nearest block per event, keeping the feed's order for the ones
	// that cannot be compared.
	order := make([]string, 0, len(visible))
	best := map[string]CityMark{}
	for _, m := range visible {
		prev, seen := best[m.Marker]
		if !seen {
			order = append(order, m.Marker)
			best[m.Marker] = m
			continue
		}
		if closer(m, prev) {
			best[m.Marker] = m
		}
	}
	entries := make([]CityMark, 0, len(order))
	for _, marker := range order {
		entries = append(entries, best[marker])
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if inOnslaught && entries[i].OffMap != entries[j].OffMap {
			return entries[i].OffMap
		}
		return closer(entries[i], entries[j])
	})

	// Renumber, then relabel every visible location of each event with its new
	// character. The map and the key are now the same numbering by construction.
	assigned := make(map[string]string, len(entries))
	for i := range entries {
		char := "?"
		if i < len(markerChars) {
			char = string(markerChars[i])
		}
		assigned[entries[i].Marker] = char
		entries[i].Marker = char
	}
	for _, m := range visible {
		m.Marker = assigned[m.Marker]
		frame.Marks = append(frame.Marks, m)
	}

	for i, m := range entries {
		if cfg.MaxListed > 0 && i == cfg.MaxListed {
			// Said rather than silently dropped: a list that stops without saying so
			// reads as "that is everything", which is the one thing it is not.
			frame.Rows = append(frame.Rows, mapRow{Text: fmt.Sprintf("+%d more", len(entries)-i)})
			break
		}
		names := m.Enemies
		if len(names) == 0 {
			// A mission or a QRF, whose name is the thing worth saying.
			names = []string{m.Label}
		}
		frame.Rows = append(frame.Rows, mapRow{
			Marker: m.Marker, Color: m.Category().Color().Hex(),
			Timer: mapTimer(m), Text: names[0],
		})
		for _, enemy := range names[1:] {
			frame.Rows = append(frame.Rows, mapRow{Text: enemy, Sub: true})
		}
	}
	return frame
}

// closer orders two marks by how far they are to walk: reachable before not,
// on-map before off, and neither if there is nothing to choose between them (which
// leaves the feed's own order in place).
func closer(a, b CityMark) bool {
	if a.OffMap != b.OffMap {
		return b.OffMap
	}
	if a.Reachable != b.Reachable {
		return a.Reachable
	}
	if a.Reachable && b.Reachable {
		return a.Walk.Blocks < b.Walk.Blocks
	}
	return false
}

// mapTimer is how long is left, or where it is when that is the surprising part:
// Onslaught is a real coordinate in the same space but not a place on this map, so a
// row that said nothing about it would look like somewhere you could walk.
func mapTimer(m CityMark) string {
	timer := ""
	if m.EndsIn > 0 {
		timer = formatCountdown(m.EndsIn)
	}
	if m.OffMap {
		if timer == "" {
			return "Onslaught"
		}
		return timer + " Onslaught"
	}
	return timer
}

// mapListMarkup is the same list, styled: the marker in its own colour so the eye can
// tie a row to a cell on the grid, the countdown dimmed, the name plain.
//
// The marker needs a colour of its own rather than just bold. It is the only thing on
// the row that also appears on the map, and a bold letter in a column of bold letters
// is not something you can find at a glance while a boss walks towards you.
func mapListMarkup(v *View, cfg MapWidgetConfig) string {
	rows := mapListLines(v, cfg)
	if len(rows) == 0 {
		return ""
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		var b strings.Builder
		switch {
		case r.Marker != "":
			// A chip, not just a coloured glyph. A thin one-character glyph over
			// whatever the game happens to be showing was unreadable - and this is
			// the one character on the row that has to be legible, since it is what
			// ties the row to a cell on the grid.
			//
			// The chip carries the category's colour, the same colour that rings its
			// cells on the map, so the two are one lookup: see a magenta ring, find
			// the magenta chip. Dark text on a bright chip rather than the reverse,
			// because every colour in that palette is bright by design.
			b.WriteString(`<span background="` + r.Color + `" foreground="` +
				mapMarkerInk + `"><b>` + escapeMarkup(r.Marker) + "</b></span> ")
			if r.Timer != "" {
				b.WriteString(`<span alpha="78%">` + escapeMarkup(r.Timer) + "</span>  ")
			}
			b.WriteString(escapeMarkup(r.Text))
		case r.Sub:
			// Indented to the width of the marker and the countdown, so a nest reads
			// as one thing rather than as several unrelated rows.
			b.WriteString("        " + escapeMarkup(r.Text))
		default:
			b.WriteString(`<span alpha="60%">` + escapeMarkup(r.Text) + "</span>")
		}
		out = append(out, b.String())
	}
	return strings.Join(out, "\n")
}

// mapMarkerColor is the identifier's colour, in the list and on the grid. Cyan
// because every other colour on this HUD already means something - amber is a
// threat, red is urgent, green is done - and an identifier means none of those.
// mapMarkerInk is the letter's own colour, on top of the category-coloured chip.
// Near-black rather than black: pure black against a bright chip is harsher than it
// needs to be at this size.
const mapMarkerInk = "#101010"

// What sort of thing a mark is, for colour. The feed does not carry this: it carries
// a kind (spawn, mission, QRF) and a list of enemy names, and the distinction between
// a boss, a nest of them and a bandit pack is in those names and their count.
//
// It matters because those four are different decisions. A bandit pack is a fight you
// pick for the loot; a single boss is a fight you pick for the challenge; a nest is
// somewhere to avoid unless you came for it; a mission is not a fight at all. One
// colour for all of them made the map say "something is here" and nothing else.
type markCategory int

const (
	markBoss markCategory = iota
	markNest
	markBandits
	markMission
	markQRF
	markOther
)

// markColor is a colour in both the forms this needs it: hex for Pango markup and
// floats for cairo. Kept as one table so the ring on the map and the chip in the key
// cannot drift apart - they are the same claim about the same event.
type markColor struct{ R, G, B uint8 }

func (c markColor) Hex() string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

func (c markColor) Floats() (float64, float64, float64) {
	return float64(c.R) / 255, float64(c.G) / 255, float64(c.B) / 255
}

// The palette. Bright enough to read as a ring over any of the map's sixteen shades,
// and far enough apart to tell at a glance:
//
//	nest     magenta - the one you most need to recognise before walking in
//	boss     red     - a single one, which is the ordinary case
//	bandits  amber   - the same amber the HUD uses for a threat on your own block
//	mission  blue    - not a fight
//	qrf      green   - a timed event of its own kind
//	other    grey    - an event the feed described in a way we do not recognise
var markColors = map[markCategory]markColor{
	markNest:    {0xf0, 0x5c, 0xff},
	markBoss:    {0xff, 0x55, 0x55},
	markBandits: {0xff, 0xd1, 0x66},
	markMission: {0x55, 0xa8, 0xff},
	markQRF:     {0x5c, 0xe6, 0x5c},
	markOther:   {0xc0, 0xc0, 0xc0},
}

func (c markCategory) Color() markColor {
	if col, ok := markColors[c]; ok {
		return col
	}
	return markColors[markOther]
}

// Category classifies one mark.
//
// A nest is a spawn carrying MORE THAN ONE enemy type, which is what the game means
// by one: a block with several kinds of boss standing on it. Bandits are recognised
// by name because the feed gives no other handle on them - they arrive as
// "6 x Bandits" in the same field a boss does.
func (m CityMark) Category() markCategory {
	switch m.Kind {
	case EventMission:
		return markMission
	case EventQRF:
		return markQRF
	case EventUnknown:
		return markOther
	}
	if len(m.Enemies) > 1 {
		return markNest
	}
	if len(m.Enemies) == 1 && strings.Contains(strings.ToLower(m.Enemies[0]), "bandit") {
		return markBandits
	}
	return markBoss
}
