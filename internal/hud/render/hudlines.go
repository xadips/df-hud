package render

import (
	"df-hud/internal/bossmap"
	"df-hud/internal/citymap"
	"df-hud/internal/config"
	hudformat "df-hud/internal/format"
	"df-hud/internal/model"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type (
	View                   = model.View
	CityEvent              = model.CityEvent
	CityEventKind          = model.CityEventKind
	CityMark               = model.CityMark
	Placement              = config.Placement
	Config                 = config.Config
	BlockWidgetConfig      = config.BlockWidgetConfig
	BossesWidgetConfig     = config.BossesWidgetConfig
	SessionWidgetConfig    = config.SessionWidgetConfig
	XPWidgetConfig         = config.XPWidgetConfig
	ChallengesWidgetConfig = config.ChallengesWidgetConfig
	MapWidgetConfig        = config.MapWidgetConfig
	Challenge              = model.Challenge
	Objective              = model.Objective
	BossMap                = bossmap.BossMap
	duration               = config.Duration
	cityWalk               = model.Walk
)

const (
	EventSpawn     = model.EventSpawn
	EventMission   = model.EventMission
	EventQRF       = model.EventQRF
	EventUnknown   = model.EventUnknown
	onslaughtCoord = bossmap.OnslaughtCoord
	qrfMarker      = bossmap.QRFMarker
	mapMinCell     = 6
	mapMaxCell     = 96
	mapBaseSize    = 1180
)

var (
	theCity  = citymap.Default()
	outposts = citymap.Outposts()
)

func formatInt(n int64) string               { return hudformat.Int(n) }
func formatClock(d time.Duration) string     { return hudformat.Clock(d) }
func formatCountdown(d time.Duration) string { return hudformat.Countdown(d) }
func formatRate(perHour float64) string      { return hudformat.Rate(perHour) }
func formatPosition(x, y, z int) string      { return hudformat.Position(x, y, z) }
func dailyMarker(enemies []string) string    { return bossmap.DailyMarker(enemies) }
func parseBossMap(data []byte, at time.Time) (*bossmap.BossMap, error) {
	return bossmap.Parse(data, at)
}
func defaultConfig() *Config { return config.Default() }

// What each widget says, as pure functions of the View.
//
// Outside the GTK build tag on purpose: the widgets just push these strings into
// labels, so every decision about wording and omission is testable without a
// display.

// blockLines is Block Info. show is false when there is nothing worth a row.
func blockLines(v *View, cfg BlockWidgetConfig) (head, sub string, show bool) {
	if !v.HaveData || !v.HasPosition {
		return "", "", false
	}

	switch {
	case v.InOutpost && v.OutpostName != "":
		head = v.OutpostName
	case v.InOutpost:
		// An unnamed outpost means the coordinate table has gone out of date.
		head = "Outpost"
	case cfg.ShowPosition:
		head = formatPosition(v.PositionX, v.PositionY, v.PositionZ)
	default:
		// The game prints coordinates under its own minimap, so with them off the
		// region takes the head instead - which also saves a row.
		head = v.ZoneName
	}

	var parts []string
	if v.InOutpost && cfg.ShowPosition {
		parts = append(parts, formatPosition(v.PositionX, v.PositionY, v.PositionZ))
	}
	if !v.InOutpost {
		// Neighbourhood and building would come from the catalog's grids, but the
		// position-to-grid transform is unsolved, so they are absent rather than
		// guessed at.
		if v.ZoneName != "" && head != v.ZoneName {
			parts = append(parts, v.ZoneName)
		}
		// df_dangerlevel is deliberately NOT shown. The game's own client never
		// renders it, so its scale is unknown - there is no telling whether 0 is
		// safe or unmeasured. It stays in the View for whenever that is
		// established. See knowledge/player-record-and-signing.md.
	}
	if support := formatCountdown(v.BlockSupport); support != "" {
		parts = append(parts, "support "+support)
	}
	return head, strings.Join(parts, "  "), true
}

// outpostAttackLine is the map-wide siege, on its own row.
//
// Separate from the threat rows because it is true everywhere on the map, and
// because while they shared a row its loud colour was applied to the bandits on
// your block too.
func outpostAttackLine(v *View) (text string, show bool) {
	if !v.HaveData || !v.OutpostAttack {
		return "", false
	}
	return "OUTPOST ATTACK", true
}

// Onslaught's prev/now/next panel, coloured like the onslaught_bosses userscript
// this is a port of. Classes rather than literal colours: restylable from
// hud.css, and they outrank a per-group colour. See style.go.
const (
	onslaughtPrevClass  = "onslaught-prev"
	onslaughtNowClass   = "onslaught-now"
	onslaughtNextClass  = "onslaught-next"
	onslaughtEmptyClass = "onslaught-empty"
)

// onslaughtHeaderTimer is the panel's countdown, m:ss like the userscript's own
// header clock. Not formatCountdown, which is coarse on purpose for things that
// expire over hours.
func onslaughtHeaderTimer(v *View) (text string, show bool) {
	if !v.HaveData || !v.HasOnslaughtCountdown {
		return "", false
	}
	return mmss(v.OnslaughtCountdown), true
}

func mmss(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

// onslaughtLabelClass is prev/now/next's own colour, always - never the row's.
// It is a separate widget from the content beside it because GTK's text-shadow
// is per-widget and cannot be turned off for part of a label's text. See
// widget_bosses.go.
const onslaughtLabelClass = "onslaught-label"

// onslaughtRow is one line of the panel. Label is empty on a continuation row.
type onslaughtRow struct {
	Label        string
	Content      string
	ContentClass string
}

// onslaughtPanel is the whole prev/now/next display, Onslaught only.
//
// Every section always gets a row, unlike threatLines: an empty prev answers
// "did anything just leave", which is not the same as no row at all.
func onslaughtPanel(v *View) ([]onslaughtRow, bool) {
	if !v.HaveData || v.PositionX != onslaughtCoord || v.PositionY != onslaughtCoord {
		return nil, false
	}
	var rows []onslaughtRow
	rows = append(rows, onslaughtSection("prev", v.BlockEventsPast, onslaughtPrevClass, "cleared")...)
	// The previous cycle's age. Onslaught skips slots often enough that "prev" is
	// regularly not the slot that just ended, so without this the row reads as
	// when it started rather than how long it has been gone.
	if len(v.BlockEventsPast) > 0 {
		if end := onslaughtCycleEnd(v.BlockEventsPast[0]); !end.IsZero() {
			// Negative means the bundle's last cycle is still running, so there is
			// no age to report rather than an age of zero.
			if since := v.Now.Sub(end); since >= 0 {
				text := "ended just now"
				if since >= time.Minute {
					text = fmt.Sprintf("ended %dm ago", int(since.Minutes()))
				}
				rows = append(rows, onslaughtRow{Content: text, ContentClass: onslaughtEmptyClass})
			}
		}
	}
	rows = append(rows, onslaughtSection("now", v.BlockEvents, onslaughtNowClass, "nothing this cycle")...)
	rows = append(rows, onslaughtSection("next", v.BlockEventsUpcoming, onslaughtNextClass, "not announced")...)
	return rows, true
}

// onslaughtCycleEnd is when the boss this panel DISPLAYS left, which is not
// e.End when the feed has bundled several cycles into one entry.
//
// Measured 2026-08-16: an entry reading start 00:15:01 / end 00:20:01 carried
// two names while the 00:25 cycle was a separate entry - so a bundle's window
// describes only its FIRST cycle, and the second name really ran 00:20-00:25.
// Ageing from e.End reported when the displayed boss STARTED, one cycle early
// per extra name. Cycle length comes from the entry's own window rather than
// assuming five minutes.
func onslaughtCycleEnd(e CityEvent) time.Time {
	if len(e.Enemies) < 2 || e.Start.IsZero() || e.End.IsZero() {
		return e.End
	}
	cycle := e.End.Sub(e.Start)
	if cycle <= 0 {
		return e.End
	}
	return e.End.Add(time.Duration(len(e.Enemies)-1) * cycle)
}

// onslaughtEventRows is eventRows keeping only the LAST enemy type.
//
// A joined list on an Onslaught event is the feed bundling separate cycles into
// one entry, last being most recent - unlike a real city nest, where eventRows
// rightly shows every type.
func onslaughtEventRows(e CityEvent) []string {
	if len(e.Enemies) > 1 {
		e.Enemies = e.Enemies[len(e.Enemies)-1:]
	}
	return eventRows(e, "")
}

// onslaughtSection is one of prev/now/next, its label on the first row only so a
// multi-row cycle reads as one block.
func onslaughtSection(label string, events []CityEvent, class, emptyText string) []onslaughtRow {
	var texts []string
	for _, e := range events {
		texts = append(texts, onslaughtEventRows(e)...)
	}
	if len(texts) == 0 {
		return []onslaughtRow{{Label: label, Content: emptyText, ContentClass: onslaughtEmptyClass}}
	}
	rows := make([]onslaughtRow, len(texts))
	for i, t := range texts {
		rows[i] = onslaughtRow{Content: t, ContentClass: class}
		if i == 0 {
			rows[i].Label = label
		}
	}
	return rows
}

// threatLines is what is standing on your block, ONE ROW PER ENEMY TYPE.
//
// Rows rather than a joined line because a live nest carried seven types at
// once - about 140 characters, which ran off the side of the screen and clipped
// the part naming what was actually dangerous.
//
// Silent when the feed has nothing for your block: that is most of the map, and
// "nothing here" everywhere would train you to stop reading it.
func threatLines(v *View) []string {
	if !v.HaveData {
		return nil
	}
	// Onslaught has its own panel, which is why BlockEventsPast/Upcoming never
	// appear here.
	var rows []string
	for _, e := range v.BlockEvents {
		rows = append(rows, eventRows(e, "")...)
	}
	return rows
}

// eventRows breaks one event into its rows. A plain spawn is nothing but its
// enemies, so a title would repeat them; a mission or QRF has a name worth its
// own row and carries enemies as well, not instead.
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

// nearestReportRange caps how far away an event is still worth a row, in block
// moves. Past a dozen it is permanent furniture rather than information.
const nearestReportRange = 12

// nearestLine is which way to walk when your own block is empty.
//
// The distance is the WALK, which can exceed what the directions add up to
// because the city has gaps - so when it does, the row says so ("5 up 2 left,
// 9 blocks").
//
// UP IS y DECREASING. Verified in game on 2026-08-13 by walking one block and
// watching the second coordinate fall; it had until then been inferred from
// DFProfiler's map being an HTML table. This is the one claim here that could
// send someone the wrong way.
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

// xpPending stands in until there are two samples to subtract. xpRough marks a
// rate computed from fewer than min_samples: correct arithmetic on thin evidence.
const (
	xpPending = "--"
	xpRough   = "~"
)

// xpLine is the XP rate, and nothing else.
//
// The number arrives as early as arithmetic allows and says how much to trust
// itself. Do not "fix" this by hiding the row until the window fills - every run
// start clears the window, so that blanked the row after every press of Start and
// was reported as a bug within the hour.
//
// Neither dashes nor a provisional rate carries a stability colour: amber and red
// mean "recent polls did not land", which is a different complaint from "there
// have not been many polls yet".
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

// challengeCategory is which config switch decides whether a challenge is shown.
type challengeCategory int

const (
	// categoryRepeatable is a limited-time event challenge. The wire marks these
	// `repeatable`, verified against a live board: 1 on exactly the three Summer
	// ones, 0 on every daily, weekly and clan entry.
	categoryRepeatable challengeCategory = iota
	categoryClan
	categoryPersonal
)

func categoryOf(c Challenge) challengeCategory {
	switch {
	case c.Clan:
		// Before Repeatable: a repeatable clan challenge is still the clan's.
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

// filterChallenges is the board minus the categories that are switched off.
//
// The board's own order is kept. Sorting by progress or deadline would reshuffle
// rows under you as scores change, which is worse than an order that is merely
// arbitrary but fixed.
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
func challengeLines(v *View, cfg ChallengesWidgetConfig) []challengeRow {
	shown := filterChallenges(v.Challenges, cfg)
	if len(shown) == 0 {
		// Only when there is no board at all: alongside rows it would explain
		// something already visible, and with everything filtered out it would
		// read as an error.
		if len(v.Challenges) == 0 && v.ChallengeStatus != "" {
			return []challengeRow{{Name: "challenges: " + v.ChallengeStatus}}
		}
		return nil
	}

	sections := groupByCategory(shown)
	// A heading needs something to tell apart.
	headings := cfg.ShowSections && len(sections) > 1

	rows := make([]challengeRow, 0, len(shown)+len(sections))
	for _, s := range sections {
		// Clan entries are all named "Weekly Challenge - <objective>", so five of
		// them put eighteen characters on screen five times. The prefix MOVES into
		// the heading - so only when there is a heading to move it to.
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

	// Three passes, because all three are properties of the board rather than of
	// any one row: the progress column, which rows begin a new challenge, and how
	// wide a heading's rule has to be.
	pad := 0
	for _, r := range rows {
		// Countdowns count towards the column too - they are the value on a row
		// with no progress of its own, and leaving them out put them in a column
		// of their own halfway across the board.
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

// challengeSection is one category's worth of the board, in the board's order.
type challengeSection struct {
	category   challengeCategory
	challenges []Challenge
}

// groupByCategory splits the board into sections, keeping the board's own order:
// sections appear in the order their first challenge does.
//
// Not a fixed order of my choosing - the board already arrives grouped, so the
// dividers describe what is on screen rather than rearranging it.
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

// sharedPrefix is the "<something> - " every challenge in a section begins with,
// or "" if they do not all share one.
//
// Cut at " - " rather than any common run of characters: "Kill Infected" and
// "Kill Dog Infected" share "Kill ", and stripping that leaves "Infected" and
// "Dog Infected", which is worse than the repetition.
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
		// Nothing may be left with an empty name - checked on the first as well,
		// since it is where the prefix came from, not an exception to it.
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

// challengeRow is one row of the board, kept in parts rather than pre-joined so
// the three can be styled differently. Text() is the plain form for -print-hud
// and the tests; Markup() is what the HUD draws.
type challengeRow struct {
	Name      string
	Objective string
	Progress  string
	Countdown string
	// Done draws the row struck through and green: still worth a row, but it
	// should read as settled rather than competing with the unfinished ones.
	Done bool
	// Urgent draws the row in the alarm colour. Never set together with Done,
	// since a finished challenge does not care what time it is.
	Urgent bool
	// Sub marks an objective belonging to the challenge above it.
	Sub bool
	// Heading marks a section divider, drawn as the name plus a rule Rule
	// characters wide. Text rather than a CSS border because a GTK label is only
	// as wide as its own content, so a border would stop at the end of the word.
	Heading bool
	// Rule is the width of the widest row on the board. Set board-wide, like Pad.
	Rule int
	// Gap asks for a few pixels above the row, turning nineteen unbroken rows
	// into visible groups without spending a blank row on each break.
	Gap bool
	// Pad is the column the progress figures line up in, in characters. Set
	// board-wide by challengeLines, since a column is a property of the set.
	Pad int
}

// label is everything before the progress figure.
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

// padding puts this row's progress in the shared column, two spaces minimum.
//
// Characters, not pixels - exact in a monospace font and approximate otherwise.
// It is also why the objective is not rendered a step smaller: a narrower
// character makes the same count of them a different width, and the column would
// drift on exactly the rows it is meant to line up.
func (r challengeRow) padding(label string) string {
	width := 2
	if extra := r.Pad - utf8.RuneCountInString(label); extra > 0 {
		width += extra
	}
	return strings.Repeat(" ", width)
}

// headingText is the name then a rule to the far side of the board, in
// box-drawing characters (one cell wide in any monospace font).
func (r challengeRow) headingText() string {
	head := "── " + r.Name + " "
	if fill := r.Rule - utf8.RuneCountInString(head); fill > 0 {
		return head + strings.Repeat("─", fill)
	}
	return head
}

// width is how wide the row draws, ignoring the "done" only the plain form adds.
func (r challengeRow) width() int {
	if r.Heading {
		// Headings are what the width is computed FOR, so they cannot contribute
		// to it - one long heading would otherwise pull every other one out.
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
		// Into the progress column, since on this row it is the value.
		b.WriteString(r.padding(label) + r.Countdown)
	}
	if r.Done {
		// In words, because plain text has no strikethrough.
		b.WriteString(" done")
	}
	return b.String()
}

// Markup is the drawn form, in Pango markup.
//
// Deliberately colour-free - weight, size, alpha and strikethrough only - so it
// composes with both the per-group color key and the state colours instead of
// fighting them. Progress is bold, objective and countdown dimmed, done struck
// through.
func (r challengeRow) Markup() string {
	var b strings.Builder
	if r.Heading {
		// Dimmer than an objective: furniture should be the least insistent thing
		// on the board while still being findable.
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
	// Padding goes outside the objective's span, so it is never rendered at a
	// different size from the padding on the row above.
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

// CSSClass is the row's colour, or empty for the group's normal one. A class
// rather than a colour so it outranks a per-group color key and can be restyled
// from hud.css.
func (r challengeRow) CSSClass() string {
	switch {
	case r.Done:
		return "done"
	case r.Urgent:
		return "expiring"
	}
	return ""
}

// Classes is every class the row wants, so the widget diffs one list.
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
// Not optional: Pango refuses to parse malformed markup and GTK answers with an
// EMPTY label, so one challenge named with an ampersand would silently blank its
// row. The board's text comes from the game.
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
// The objective is the actionable half - "First Strike  0/7" does not say what
// the seven are - and the name is kept as well because objectives collide: the
// live board carries both "Summer Loot" and "Weekly Challenge - Loot Anything",
// each with the objective "Loot Anything".
func challengeRows(c Challenge, now time.Time, cfg ChallengesWidgetConfig) []challengeRow {
	remaining := c.Remaining(now)
	countdown := ""
	if remaining > 0 && remaining < 24*time.Hour {
		// "5d" on every row is noise.
		countdown = formatCountdown(remaining)
	}
	// Not "complete AND close": Done wins, so the two are never both set.
	urgent := !c.Complete() && cfg.UrgentWithin.Duration > 0 &&
		remaining > 0 && remaining <= cfg.UrgentWithin.Duration

	// Objectives go UNDER the challenge always, not only when there are several,
	// so a weekly with four and a daily with one look like the same kind of
	// thing. The exception is a name that already says what the objective is -
	// every clan entry - where the extra row would be the name again with a
	// number on it.
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
			// The deadline belongs to the challenge, so it colours the whole
			// group - except an objective already finished.
			Urgent: urgent && !o.Done(),
			Sub:    true,
		})
	}
	return rows
}

// nameCoversObjective reports whether the challenge's name already says what the
// objective is. Case-insensitive substring is all the real board needs: clan
// entries are named "Weekly Challenge - <objective>" exactly.
func nameCoversObjective(name, objective string) bool {
	if objective == "" {
		return true
	}
	return strings.Contains(strings.ToLower(name), strings.ToLower(objective))
}

// sessionLine is the run clock: time in the inner city.
//
// show is false whenever there is no run. A clock that keeps counting while you
// shop reads as broken, and one that counts the launcher's loading screen is
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

	// Groups carry a position rather than a sort key, so "in order" means reading
	// order: down the screen, then across. An approximation of what the eye does
	// with four groups in four corners, but a deterministic one.
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
		// The attack first, because it is the one that ends your run if ignored.
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

	// Stable, so rows sharing the block group's position keep the order they were
	// added in.
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

// outpostLetters is each outpost's identifier on the map, taken from DFProfiler's
// own legend so anyone who knows their map can read this one.
var outpostLetters = map[string]string{
	"Nastya's Holdout": "N",
	"Dogg's Stockade":  "D",
	"Precinct 13":      "P",
	"Fort Pastor":      "F",
	"Secronom Bunker":  "S",
	"Valcrest":         "C",
	"Ground Zero":      "Z",
}

// mapCellPx is the size of one block in pixels.
//
// widget.map.scale is a budget for the LONGEST SIDE rather than a size per block,
// which is what makes a cropped map bigger rather than merely smaller: 1180
// pixels over 31 blocks instead of 59 gives 38px cells instead of 20.
//
// Here rather than in the widget because the key's font is derived from it too,
// and the two have to agree.
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

// mapListPt is the key's font size in points, derived from the block size so one
// scale sizes the whole group. 0 means "leave it to the stylesheet", which is the
// answer when font_size pinned it by hand.
//
// Reached through groupStyles, so it lands in the same CSS every other group's
// font_size does. 0.65pt per pixel of cell puts a 20px map's key at 13pt; it sits
// a little above the 0.54 the markers themselves use, because a marker only has
// to be told apart from twenty others while the key is a column you read while
// something walks towards you. Rounded to a tenth, which is what reaches the
// stylesheet anyway (%.1fpt). The 8..30 bounds are sanity, not taste.
func mapListPt(cfg MapWidgetConfig) float64 {
	if cfg.FontSize > 0 {
		return 0
	}
	pt := math.Round(0.65*float64(mapCellPx(cfg))*10) / 10
	return math.Min(math.Max(pt, 8), 30)
}

// mapWindow is the part of the city the map draws: an origin in block coordinates
// and a size in blocks.
type mapWindow struct{ X, Y, W, H int }

func (w mapWindow) contains(x, y int) bool {
	return x >= w.X && y >= w.Y && x < w.X+w.W && y < w.Y+w.H
}

// mapWindowFor is which blocks to draw, given the config and where you are.
//
// The window is CLAMPED into the city rather than allowed to hang off the edge,
// which keeps its size constant. That matters more than keeping you centred: the
// group is centred on the monitor, so a window that shrank near the city's edge
// would make the whole map jump sideways as you walked.
//
// Falls back to the whole city when there is no centre to crop around - no
// position yet, or Onslaught, whose 3000,3000 is not a place on this grid.
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

// mapWindowSize is the drawn size in blocks without needing to know where the
// player is, for the widget's size request. Clamping is what makes that possible.
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

// mapRow is one line of the key beside the map. Marker and Timer are set on an
// entry's first row and empty on its continuations.
type mapRow struct {
	Marker string
	// Color is the category's colour, the same one that rings this event's cells,
	// so a chip in the key and a ring on the grid are one lookup.
	Color string
	Timer string
	Text  string
	Sub   bool
}

// mapListLines is the key beside the map: which marker is what, and how long is
// left. A letter in a cell is only meaningful next to a list, and the list is
// also the only place a countdown will fit.
//
// ONE ENTRY PER EVENT, not per marked block. The feed puts the same bandit pack
// on a dozen blocks at once - 185 marks from 30 events in one live capture - so a
// row each made the list "+173 more".
//
// Entries are ordered by their nearest block, but the distance is not written:
// the map is where you see where something is.
func mapListLines(v *View, cfg MapWidgetConfig) []mapRow { return mapFrameFor(v, cfg).Rows }

// mapFrame is one frame of the map: which blocks to draw, what to draw on them,
// and the key that explains it. One function produces all three because the
// identifier on a cell and the identifier in the key are the same lookup.
type mapFrame struct {
	Window mapWindow
	// Marks is every visible location, each carrying this frame's identifier. An
	// event on six blocks appears six times, all with the same character.
	Marks []CityMark
	Rows  []mapRow
}

// mapFrameFor works out what the map shows.
func mapFrameFor(v *View, cfg MapWidgetConfig) mapFrame {
	frame := mapFrame{Window: mapWindowFor(v, cfg)}
	if len(v.CityMarks) == 0 {
		return frame
	}

	// Anything off the map is filtered out by ActiveMarks unless you are in
	// Onslaught. Where they do appear they come first, because then they are what
	// is in front of you.
	inOnslaught := v.HasPosition && v.PositionX == onslaughtCoord && v.PositionY == onslaughtCoord

	// A cropped map gets a cropped key. Off-map marks are exempt - they only
	// appear in Onslaught, and no window contains 3000,3000.
	visible := make([]CityMark, 0, len(v.CityMarks))
	for _, m := range v.CityMarks {
		if m.OffMap || frame.Window.contains(m.X, m.Y) {
			visible = append(visible, m)
		}
	}
	if len(visible) == 0 {
		return frame
	}

	// Collapse to the nearest block per event, keeping the feed's order for the
	// ones that cannot be compared.
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

	// Identifiers are NOT renumbered nearest-first. They are the game's own event
	// slots, ranked once in bossmap.go - B4, N7, M2 - so one means the same block
	// all cycle, and the same block DFProfiler's map calls B4. Renumbering read
	// beautifully and could not be said out loud to another player, because it
	// changed as you walked. The key is still sorted nearest first, which costs
	// nothing: the order of a row is not the name of it.
	frame.Marks = append(frame.Marks, visible...)

	for i, m := range entries {
		if cfg.MaxListed > 0 && i == cfg.MaxListed {
			// Said rather than silently dropped: a list that stops without saying
			// so reads as "that is everything".
			frame.Rows = append(frame.Rows, mapRow{Text: fmt.Sprintf("+%d more", len(entries)-i)})
			break
		}
		names := m.Enemies
		if len(names) == 0 {
			// A mission or a QRF, whose name is the thing worth saying.
			names = []string{m.Label}
		}
		frame.Rows = append(frame.Rows, mapRow{
			Marker: m.Marker, Color: cityMarkInk(m).Hex(),
			Timer: mapTimer(m), Text: names[0],
		})
		for _, enemy := range names[1:] {
			frame.Rows = append(frame.Rows, mapRow{Text: enemy, Sub: true})
		}
	}
	return frame
}

// closer orders two marks by how far they are to walk: reachable before not,
// on-map before off, and neither when there is nothing to choose between them
// (which leaves the feed's own order in place).
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
// Onslaught is a real coordinate but not a place on this map, so a row saying
// nothing about it would look like somewhere you could walk.
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

// mapListMarkup is the same list, styled: the marker in its own colour so the eye
// can tie a row to a cell, the countdown dimmed, the name plain.
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
			// A chip, not just a coloured glyph: a thin one-character glyph over
			// whatever the game is showing was unreadable, and this is the one
			// character on the row that has to be legible. Dark text on a bright
			// chip rather than the reverse, since every colour in that palette is
			// bright by design.
			b.WriteString(`<span background="` + r.Color + `" foreground="` +
				mapMarkerInk + `"><b>` + escapeMarkup(r.Marker) + "</b></span> ")
			if r.Timer != "" {
				b.WriteString(`<span alpha="78%">` + escapeMarkup(r.Timer) + "</span>  ")
			}
			b.WriteString(escapeMarkup(r.Text))
		case r.Sub:
			// Indented past the marker and countdown, so a nest reads as one thing.
			b.WriteString("        " + escapeMarkup(r.Text))
		default:
			b.WriteString(`<span alpha="60%">` + escapeMarkup(r.Text) + "</span>")
		}
		out = append(out, b.String())
	}
	return strings.Join(out, "\n")
}

// mapMarkerInk is the letter's colour on top of the category-coloured chip.
// Near-black rather than black: pure black against a bright chip is harsher than
// it needs to be at this size.
const mapMarkerInk = "#101010"

// What sort of thing a mark is, for colour. The feed does not carry this - it
// carries a kind and a list of enemy names, and the difference between a boss, a
// nest and a bandit pack is in those names and their count.
//
// It matters because those are different decisions: a bandit pack is a fight you
// pick for the loot, a nest is somewhere to avoid unless you came for it, a
// mission is not a fight at all.
type markCategory int

const (
	markBoss markCategory = iota
	markNest
	markBandits
	markMission
	markQRF
	markOther
)

// markColor is a colour in both forms this needs: hex for Pango and floats for
// cairo. One table, so the ring on the map and the chip in the key cannot drift.
type markColor struct{ R, G, B uint8 }

func (c markColor) Hex() string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

func (c markColor) Floats() (float64, float64, float64) {
	return float64(c.R) / 255, float64(c.G) / 255, float64(c.B) / 255
}

// The palette. Bright enough to read as a ring over any of the map's sixteen
// shades, and far enough apart to tell at a glance:
//
//	nest     magenta - the one you most need to recognise before walking in
//	boss     red
//	bandits  amber   - the HUD's own threat colour
//	mission  blue    - not a fight
//	qrf      green
//	other    grey    - described in a way we do not recognise
var markColors = map[markCategory]markColor{
	markNest:    {0xf0, 0x5c, 0xff},
	markBoss:    {0xff, 0x55, 0x55},
	markBandits: {0xff, 0xd1, 0x66},
	markMission: {0x55, 0xa8, 0xff},
	markQRF:     {0x5c, 0xe6, 0x5c},
	markOther:   {0xc0, 0xc0, 0xc0},
}

// dailyColor is ONE colour for every daily, whichever it is today, because the
// question is not "which boss" - the initials say that - but "is today's event
// here".
var dailyColor = markColor{0xff, 0x8a, 0x00}

func (c markCategory) Color() markColor {
	if col, ok := markColors[c]; ok {
		return col
	}
	return markColors[markOther]
}

// markInk is the colour a mark is drawn in: the daily's own when today's boss is
// standing there, the category's otherwise.
func cityMarkInk(m CityMark) markColor {
	if cityMarkIsDaily(m) {
		return dailyColor
	}
	return cityMarkCategory(m).Color()
}

// ringed reports whether this mark gets a ring around its block.
//
// Only where a ring earns it. Bandits, bosses and nests do not: their identifier
// already says which of the three they are, so the ring repeated the letter in
// colour and boxed two thirds of the map. What is left is what a ring is for -
// "this one is different": today's daily, a mission, and a QRF.
func cityMarkRinged(m CityMark) bool {
	if cityMarkIsDaily(m) {
		return true
	}
	switch cityMarkCategory(m) {
	case markMission, markQRF:
		return true
	}
	return false
}

// Category classifies one mark. A nest is a spawn carrying MORE THAN ONE enemy
// type, which is what the game means by one. Bandits are recognised by name
// because the feed gives no other handle - they arrive as "6 x Bandits" in the
// same field a boss does.
func cityMarkCategory(m CityMark) markCategory { return markCategoryOf(m.Kind, m.Enemies) }

// IsDaily is a different question from what sort of place this is: a nest can
// contain the daily, and then it is both.
func cityMarkIsDaily(m CityMark) bool { return dailyMarker(m.Enemies) != "" }

// markCategoryOf is shared by the mark and the event it came from, so the
// identifier assigned in bossmap.go and the colour chosen here cannot disagree.
func markCategoryOf(kind CityEventKind, enemies []string) markCategory {
	switch kind {
	case EventMission:
		return markMission
	case EventQRF:
		return markQRF
	case EventUnknown:
		return markOther
	}
	if len(enemies) > 1 {
		return markNest
	}
	if len(enemies) == 1 && strings.Contains(strings.ToLower(enemies[0]), "bandit") {
		return markBandits
	}
	return markBoss
}

// Exported presentation API consumed by the GTK package and command diagnostics.
type (
	OnslaughtRow = onslaughtRow
	ChallengeRow = challengeRow
	MapFrame     = mapFrame
	MapWindow    = mapWindow
	MarkColor    = markColor
)

const (
	OnslaughtPrevClass  = onslaughtPrevClass
	OnslaughtNowClass   = onslaughtNowClass
	OnslaughtNextClass  = onslaughtNextClass
	OnslaughtEmptyClass = onslaughtEmptyClass
	OnslaughtLabelClass = onslaughtLabelClass
)

func BlockLines(v *View, cfg BlockWidgetConfig) (string, string, bool) {
	return blockLines(v, cfg)
}
func OutpostAttackLine(v *View) (string, bool) { return outpostAttackLine(v) }
func OnslaughtHeaderTimer(v *View) (string, bool) {
	return onslaughtHeaderTimer(v)
}
func OnslaughtPanel(v *View) ([]OnslaughtRow, bool) { return onslaughtPanel(v) }
func ThreatLines(v *View) []string                  { return threatLines(v) }
func NearestLine(v *View, cfg BossesWidgetConfig) (string, bool) {
	return nearestLine(v, cfg)
}
func XPLine(v *View, cfg XPWidgetConfig) (string, string, bool) { return xpLine(v, cfg) }
func SessionLine(v *View, cfg SessionWidgetConfig) (string, bool) {
	return sessionLine(v, cfg)
}
func ChallengeLines(v *View, cfg ChallengesWidgetConfig) []ChallengeRow {
	return challengeLines(v, cfg)
}
func HUDLines(v *View, cfg *Config) []string { return hudLines(v, cfg) }
func MapCellPx(cfg MapWidgetConfig) int      { return mapCellPx(cfg) }
func MapWindowSize(cfg MapWidgetConfig) (int, int) {
	return mapWindowSize(cfg)
}
func MapFrameFor(v *View, cfg MapWidgetConfig) MapFrame { return mapFrameFor(v, cfg) }
func MapListMarkup(v *View, cfg MapWidgetConfig) string { return mapListMarkup(v, cfg) }
func CityMarkRinged(m CityMark) bool                    { return cityMarkRinged(m) }
func CityMarkInk(m CityMark) MarkColor                  { return cityMarkInk(m) }
func City() *citymap.Map                                { return theCity }
func Outposts() []citymap.Outpost                       { return outposts }
func OutpostLetter(name string) string                  { return outpostLetters[name] }

func (w mapWindow) Contains(x, y int) bool { return w.contains(x, y) }
