package main

import (
	"strings"
	"testing"
	"time"
)

// board mirrors the shape of a real one, verified against a live fetch: three
// event challenges the wire marks repeatable, ordinary dailies and weeklies, and
// the clan's own set. The completion states are spread so the filters have
// something to bite on.
func board() []Challenge {
	end := time.Now().Add(5 * 24 * time.Hour)
	soon := time.Now().Add(20 * time.Minute)
	return []Challenge{
		{Name: "Summer Death", Repeatable: true, End: end, Objectives: []Objective{
			{Name: "Kill Regular Infected", Target: 100, Score: 55, HasScore: true},
		}},
		{Name: "Summer Loot", Repeatable: true, End: end, Objectives: []Objective{
			{Name: "Loot Anything", Target: 10, Score: 10, HasScore: true},
		}},
		{Name: "Untouched", End: end, Objectives: []Objective{
			{Name: "Do Nothing", Target: 10},
		}},
		{Name: "Nearly There", End: soon, Objectives: []Objective{
			{Name: "Kill Dogs", Target: 100, Score: 95, HasScore: true},
		}},
		{Name: "Already Done", End: end, Objectives: []Objective{
			{Name: "Kill Anything", Target: 10, Score: 10, HasScore: true},
		}},
		{Name: "Weekly Challenge - Kill Infected", Clan: true, End: end, Objectives: []Objective{
			{Name: "Kill Infected", Target: 162401, Score: 159487, HasScore: true},
		}},
	}
}

// texts is the plain form of a rendered board, for assertions that care about
// wording rather than styling.
func texts(rows []challengeRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Text())
	}
	return out
}

func allCategories() ChallengesWidgetConfig {
	return ChallengesWidgetConfig{
		Enabled: true, ShowRepeatable: true, ShowClan: true, ShowPersonal: true, ShowCompleted: true,
	}
}

func names(cs []Challenge) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return strings.Join(out, " | ")
}

// The whole board, in the board's own order. Sorting by progress or deadline would
// reshuffle rows as scores change, which is worse on a glanceable HUD than an
// order that is merely arbitrary but fixed.
func TestFilterChallengesShowsEverythingByDefault(t *testing.T) {
	got := filterChallenges(board(), allCategories())
	if len(got) != 6 {
		t.Fatalf("got %d rows, want the whole board: %s", len(got), names(got))
	}
	if got[0].Name != "Summer Death" || got[5].Name != "Weekly Challenge - Kill Infected" {
		t.Errorf("order = %s, want the board's own", names(got))
	}
}

// Each category is its own switch. The event one keys off the wire's `repeatable`
// flag rather than the name: "Summer" is this event, not the concept.
func TestFilterChallengesByCategory(t *testing.T) {
	cfg := allCategories()
	cfg.ShowRepeatable = false
	got := filterChallenges(board(), cfg)
	if strings.Contains(names(got), "Summer") {
		t.Errorf("show_repeatable=false left event challenges in: %s", names(got))
	}
	if len(got) != 4 {
		t.Errorf("got %d rows, want the four non-event ones: %s", len(got), names(got))
	}

	cfg = allCategories()
	cfg.ShowClan = false
	if got := filterChallenges(board(), cfg); strings.Contains(names(got), "Weekly Challenge") {
		t.Errorf("show_clan=false left clan challenges in: %s", names(got))
	}

	cfg = allCategories()
	cfg.ShowPersonal = false
	got = filterChallenges(board(), cfg)
	if strings.Contains(names(got), "Nearly There") || strings.Contains(names(got), "Untouched") {
		t.Errorf("show_personal=false left ordinary challenges in: %s", names(got))
	}
	// The event and clan ones are untouched by it, which is the point of them being
	// separate switches.
	if !strings.Contains(names(got), "Summer Death") || !strings.Contains(names(got), "Weekly Challenge") {
		t.Errorf("show_personal=false took too much: %s", names(got))
	}
}

// Completion cuts across the three sources rather than being a fourth one.
func TestFilterChallengesCanHideCompleted(t *testing.T) {
	cfg := allCategories()
	cfg.ShowCompleted = false
	got := filterChallenges(board(), cfg)

	for _, c := range got {
		if c.Complete() {
			t.Errorf("show_completed=false left %q in", c.Name)
		}
	}
	// Summer Loot is complete AND an event challenge, so this also pins down that
	// completion is checked independently of the category.
	if strings.Contains(names(got), "Summer Loot") || strings.Contains(names(got), "Already Done") {
		t.Errorf("got %s", names(got))
	}
	if !strings.Contains(names(got), "Summer Death") {
		t.Error("an unfinished event challenge must survive show_completed=false")
	}
}

// Every switch off is an empty group rather than a fallback to something. A HUD
// that ignores the config and shows rows anyway is worse than an empty corner.
func TestFilterChallengesCanHideEverything(t *testing.T) {
	if got := filterChallenges(board(), ChallengesWidgetConfig{Enabled: true}); len(got) != 0 {
		t.Errorf("got %s, want nothing", names(got))
	}
}

// MaxShown is a cap, and 0 means no cap: the point of the group having its own
// place on screen is that the whole board fits.
func TestFilterChallengesMaxShown(t *testing.T) {
	cfg := allCategories()
	cfg.MaxShown = 2
	if got := filterChallenges(board(), cfg); len(got) != 2 {
		t.Errorf("got %d rows, want 2: %s", len(got), names(got))
	}
	cfg.MaxShown = 0
	if got := filterChallenges(board(), cfg); len(got) != 6 {
		t.Errorf("got %d rows, want no cap: %s", len(got), names(got))
	}
}

func TestChallengeLines(t *testing.T) {
	cfg := allCategories()
	v := &View{Now: time.Now(), Challenges: board()}

	lines := texts(challengeLines(v, cfg))
	// Six challenges: five as a name row plus an indented objective, and the clan
	// one as a single row because its name already says its objective.
	if len(lines) != 11 {
		t.Fatalf("lines = %#v", lines)
	}
	if lines[0] != "Summer Death" {
		t.Errorf("lines[0] = %q, want the challenge alone on its row", lines[0])
	}
	if !strings.HasPrefix(lines[1], "  Kill Regular Infected") || !strings.Contains(lines[1], "55/100") {
		t.Errorf("lines[1] = %q, want the objective indented with its progress", lines[1])
	}
	// A countdown appears only when it is close enough to matter; "5d" on every
	// row is noise. It sits on the challenge, not on the objective.
	for _, line := range lines {
		if strings.Contains(line, "5d") {
			t.Errorf("a far-off deadline should not be shown: %q", line)
		}
	}
	if !strings.Contains(lines[6], "Nearly There") || !strings.Contains(lines[6], "20m") {
		t.Errorf("lines[6] = %q, want the near deadline on the challenge row", lines[6])
	}
	// Completion is marked on the challenge and on the objective independently.
	if !strings.Contains(lines[2], "Summer Loot") || !strings.Contains(lines[2], "done") {
		t.Errorf("lines[2] = %q, want the finished challenge marked", lines[2])
	}
	if !strings.Contains(lines[3], "done") {
		t.Errorf("lines[3] = %q, want the finished objective marked", lines[3])
	}
	if !strings.HasPrefix(lines[10], "Weekly Challenge - Kill Infected") ||
		!strings.Contains(lines[10], "159,487/162,401") {
		t.Errorf("lines[10] = %q, want the clan challenge as one row", lines[10])
	}
}

// The objective is the actionable half of a challenge, so it is on the row. The
// name stays too, because dropping it collides - the live board carries both
// "Summer Loot" and "Weekly Challenge - Loot Anything", each with an objective of
// "Loot Anything", and two identical rows with different numbers is worse than one
// long row.
func TestChallengeRowsNameTheObjective(t *testing.T) {
	now := time.Now()
	cfg := allCategories()

	first := Challenge{
		Name: "First Strike", End: now.Add(20 * time.Hour),
		Objectives: []Objective{{Name: "Kill Any Boss", Target: 7}},
	}
	rows := texts(challengeRows(first, now, cfg))
	// The objective lands UNDER the challenge, indented: a weekly with four of them
	// and a daily with one should look like the same kind of thing, and an objective
	// on the same line as its challenge reads as part of the name.
	if len(rows) != 2 {
		t.Fatalf("rows = %v, want the challenge and its objective below it", rows)
	}
	if !strings.Contains(rows[0], "First Strike") || strings.Contains(rows[0], "Kill Any Boss") {
		t.Errorf("first row = %q, want the challenge alone", rows[0])
	}
	if !strings.HasPrefix(rows[1], "  ") {
		t.Errorf("second row = %q, want it indented under the challenge", rows[1])
	}
	if !strings.Contains(rows[1], "Kill Any Boss") || !strings.Contains(rows[1], "0/7") {
		t.Errorf("second row = %q, want the objective and its progress", rows[1])
	}

	// When the name already contains the objective - which is exactly how the clan
	// board reads - saying it twice is noise.
	clan := Challenge{
		Name: "Weekly Challenge - Kill Infected", Clan: true, End: now.Add(4 * 24 * time.Hour),
		Objectives: []Objective{{Name: "Kill Infected", Target: 162401, Score: 162423, HasScore: true}},
	}
	rows = texts(challengeRows(clan, now, cfg))
	// One row, not two: an objective row here would be the name again with a number
	// on it, so the number joins the name instead.
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want one row when the name already says the objective", rows)
	}
	if strings.Count(strings.ToLower(rows[0]), "kill infected") != 1 {
		t.Errorf("row = %q, want the objective named once", rows[0])
	}
	if !strings.Contains(rows[0], "162,423/162,401") {
		t.Errorf("row = %q, want the progress on the name row", rows[0])
	}
	if !strings.Contains(rows[0], "done") {
		t.Errorf("row = %q, want the completion mark", rows[0])
	}
}

// Two objectives summed into one figure is not something you can act on either, so
// they get a row each under the challenge's name.
func TestChallengeRowsSplitsMultipleObjectives(t *testing.T) {
	now := time.Now()
	cfg := allCategories()
	c := Challenge{
		Name: "Two Jobs", End: now.Add(3 * time.Hour),
		Objectives: []Objective{
			{Name: "Kill Dogs", Target: 100, Score: 100, HasScore: true},
			{Name: "Loot Food", Target: 25, Score: 4, HasScore: true},
		},
	}

	rows := texts(challengeRows(c, now, cfg))
	if len(rows) != 3 {
		t.Fatalf("rows = %v, want the name and a row per objective", rows)
	}
	if !strings.Contains(rows[0], "Two Jobs") || !strings.Contains(rows[0], "h") {
		t.Errorf("first row = %q, want the name and the countdown", rows[0])
	}
	if !strings.Contains(rows[1], "Kill Dogs") || !strings.Contains(rows[1], "100/100") {
		t.Errorf("row = %q", rows[1])
	}
	// Per-objective completion, since the challenge as a whole is not done.
	if !strings.Contains(rows[1], "done") {
		t.Errorf("row = %q, want the finished objective marked", rows[1])
	}
	if !strings.Contains(rows[2], "Loot Food") || strings.Contains(rows[2], "done") {
		t.Errorf("row = %q", rows[2])
	}
}

// An empty board with a reason must explain itself: "no challenges" alone would
// never point anyone at the missing salt or cookie.
func TestChallengeLinesExplainsAnEmptyBoard(t *testing.T) {
	cfg := allCategories()

	lines := texts(challengeLines(&View{Now: time.Now(), ChallengeStatus: "no signing salt yet"}, cfg))
	if len(lines) != 1 || !strings.Contains(lines[0], "no signing salt") {
		t.Errorf("lines = %v, want the reason", lines)
	}
	// With no board and no reason, the widget stays quiet rather than showing a
	// bare label.
	if lines := challengeLines(&View{Now: time.Now()}, cfg); len(lines) != 0 {
		t.Errorf("lines = %v, want none", lines)
	}
	// A board that exists but is entirely filtered out is NOT an error, so the
	// status must not appear: the rows are missing because they were asked to be.
	hidden := &View{Now: time.Now(), Challenges: board(), ChallengeStatus: "stale"}
	if lines := challengeLines(hidden, ChallengesWidgetConfig{Enabled: true}); len(lines) != 0 {
		t.Errorf("lines = %v, want silence rather than a status", lines)
	}
}

// The board is part of the view, so the console window and the HUD read the same
// data.
func TestStoreHoldsTheBoard(t *testing.T) {
	s := newStore(nil)
	now := time.Now()
	s.SetCredentialsAt(now)
	s.ApplyTick(Tick{At: now, Vars: realPlayerRecord(), Scheduled: true})

	b := board()
	s.SetChallenges(b, now)

	v := s.Derive(now)
	if v.ChallengesTotal != len(b) {
		t.Errorf("ChallengesTotal = %d, want %d", v.ChallengesTotal, len(b))
	}
	// Summer Loot and Already Done.
	if v.ChallengesDone != 2 {
		t.Errorf("ChallengesDone = %d, want 2", v.ChallengesDone)
	}
	if len(v.Challenges) != len(b) {
		t.Errorf("Challenges = %d rows, want the whole board", len(v.Challenges))
	}
	if got, ok := s.Challenges(); !ok || len(got) != len(b) {
		t.Error("the full board should be available for the console window")
	}
}

// The drawn form. Three levels of hierarchy in one label, so a weekly with four
// objectives reads as a group rather than as a wall of same-weight text.
func TestChallengeRowMarkupHierarchy(t *testing.T) {
	row := challengeRow{Name: "First Strike", Objective: "Kill Any Boss",
		Progress: "0/7", Countdown: "20h20m"}

	got := row.Markup()
	// Progress is bold because it is what you scan the board for.
	if !strings.Contains(got, "<b>0/7</b>") {
		t.Errorf("markup = %q, want the progress bold", got)
	}
	// The objective is subordinate to the challenge it belongs to, said with alpha.
	if !strings.Contains(got, `alpha="78%">Kill Any Boss`) {
		t.Errorf("markup = %q, want the objective dimmed", got)
	}
	// And NOT with size. A narrower character makes the same count of padding
	// characters a different width, which would drift the progress column on
	// exactly the rows it exists to line up.
	if strings.Contains(got, "size=") {
		t.Errorf("markup = %q, want no size change: it breaks the aligned column", got)
	}
	// Deliberately no colour anywhere: a hardcoded one would fight both the
	// per-group color key and the state colours.
	if strings.Contains(got, "foreground") || strings.Contains(got, "color") {
		t.Errorf("markup = %q, want no hardcoded colour", got)
	}
}

// A finished challenge is struck through rather than labelled, and the word goes
// away because the line through it says the same thing.
func TestChallengeRowMarkupStrikesCompleted(t *testing.T) {
	done := challengeRow{Name: "Summer Loot", Progress: "10/10", Done: true}

	got := done.Markup()
	if !strings.Contains(got, "<s>") || !strings.Contains(got, "</s>") {
		t.Errorf("markup = %q, want it struck through", got)
	}
	if strings.Contains(got, "done") {
		t.Errorf("markup = %q, want the strikethrough instead of the word", got)
	}
	// The plain form has no strikethrough to say it with, so there it stays a word.
	if !strings.Contains(done.Text(), "done") {
		t.Errorf("text = %q, want the word when there is no styling", done.Text())
	}
}

// Escaping is not optional. Pango refuses to parse malformed markup and GTK
// answers with a warning and an EMPTY label, so one challenge named with an
// ampersand would silently blank its own row - and the board's text comes from
// the game, not from us.
func TestChallengeRowMarkupEscapes(t *testing.T) {
	row := challengeRow{Name: "Fish & Chips <b>", Objective: `"Quotes" & 'more'`, Progress: "1/2"}

	got := row.Markup()
	if strings.Contains(got, "Fish & Chips") {
		t.Errorf("markup = %q, want the ampersand escaped", got)
	}
	if !strings.Contains(got, "Fish &amp; Chips") {
		t.Errorf("markup = %q, want &amp;", got)
	}
	// The name's own tag must arrive as text, not as markup.
	if !strings.Contains(got, "&lt;b&gt;") {
		t.Errorf("markup = %q, want the tag in the name neutralised", got)
	}
	// Our own tags survive.
	if !strings.Contains(got, "<b>1/2</b>") {
		t.Errorf("markup = %q, want our own tags intact", got)
	}
	// The plain form is never escaped: it goes to a terminal, not to Pango.
	if !strings.Contains(row.Text(), "Fish & Chips") {
		t.Errorf("text = %q, want it unescaped", row.Text())
	}
}

// An objective row is indented in both forms, so the grouping survives whether it
// is drawn or printed.
func TestChallengeRowSubIsIndented(t *testing.T) {
	sub := challengeRow{Objective: "Loot Food", Progress: "4/25", Sub: true}
	if !strings.HasPrefix(sub.Text(), "  ") {
		t.Errorf("text = %q, want it indented", sub.Text())
	}
	if !strings.HasPrefix(sub.Markup(), "  ") {
		t.Errorf("markup = %q, want it indented", sub.Markup())
	}
	// With no name there is no separator to leave dangling.
	if strings.Contains(sub.Text(), ": ") {
		t.Errorf("text = %q, want no dangling separator", sub.Text())
	}
}

// Green for finished, red for unfinished work that is nearly out of time. These are
// state colours, so they are CSS classes rather than markup: the built-in sheet
// scopes them to outrank a per-group color key, which is the whole point of them.
func TestChallengeRowColours(t *testing.T) {
	cfg := allCategories()
	cfg.UrgentWithin = duration{2 * time.Hour}
	now := time.Now()

	done := Challenge{Name: "Summer Loot", End: now.Add(5 * 24 * time.Hour),
		Objectives: []Objective{{Name: "Loot Anything", Target: 10, Score: 10, HasScore: true}}}
	if got := challengeRows(done, now, cfg)[0].CSSClass(); got != "done" {
		t.Errorf("a finished challenge = %q, want done", got)
	}

	soon := Challenge{Name: "Big Fancy Dinner", End: now.Add(90 * time.Minute),
		Objectives: []Objective{{Name: "Loot Food", Target: 25, Score: 8, HasScore: true}}}
	rows := challengeRows(soon, now, cfg)
	if got := rows[0].CSSClass(); got != "expiring" {
		t.Errorf("90 minutes left = %q, want expiring", got)
	}
	// The deadline belongs to the challenge, so it colours the objectives under it
	// too - otherwise the group would be half red.
	if got := rows[1].CSSClass(); got != "expiring" {
		t.Errorf("the objective row = %q, want expiring with its challenge", got)
	}

	// Comfortably ahead of the deadline: the group's own colour.
	later := Challenge{Name: "Who Let The Dogs Out?", End: now.Add(18 * time.Hour),
		Objectives: []Objective{{Name: "Kill Dog Infected", Target: 1000, Score: 316, HasScore: true}}}
	if got := challengeRows(later, now, cfg)[0].CSSClass(); got != "" {
		t.Errorf("18 hours left = %q, want no state colour", got)
	}

	// A finished challenge does not care what time it is, so the two are never both
	// set and green wins.
	finishedAndLate := Challenge{Name: "Just In Time", End: now.Add(10 * time.Minute),
		Objectives: []Objective{{Name: "Kill Anything", Target: 5, Score: 5, HasScore: true}}}
	row := challengeRows(finishedAndLate, now, cfg)[0]
	if row.Urgent {
		t.Error("a completed challenge must not also be urgent")
	}
	if got := row.CSSClass(); got != "done" {
		t.Errorf("class = %q, want done to win", got)
	}

	// Zero turns it off without touching the green.
	cfg.UrgentWithin = duration{0}
	if got := challengeRows(soon, now, cfg)[0].CSSClass(); got != "" {
		t.Errorf("with urgent_within off, class = %q, want none", got)
	}
	if got := challengeRows(done, now, cfg)[0].CSSClass(); got != "done" {
		t.Errorf("urgent_within must not affect completion, got %q", got)
	}
}

// The progress figures line up in one column across the whole board, which is why
// the column is computed over the finished set rather than per challenge.
func TestChallengeLinesAlignsProgress(t *testing.T) {
	cfg := allCategories()
	rows := challengeLines(&View{Now: time.Now(), Challenges: board()}, cfg)

	column := -1
	counted := 0
	for _, r := range rows {
		if r.Progress == "" {
			continue
		}
		counted++
		at := strings.Index(r.Text(), r.Progress)
		if at < 0 {
			t.Fatalf("row %q lost its progress", r.Text())
		}
		if column == -1 {
			column = at
			continue
		}
		if at != column {
			t.Errorf("progress starts at %d in %q, but at %d in an earlier row",
				at, r.Text(), column)
		}
	}
	if counted < 5 {
		t.Fatalf("only %d rows carried progress; the board should have more", counted)
	}
	// The longest label decides the column, and there is always a gap after it.
	longest := 0
	for _, r := range rows {
		if r.Progress == "" {
			continue
		}
		if n := len(r.Text()[:strings.Index(r.Text(), r.Progress)]); n > longest {
			longest = n
		}
	}
	if column != longest {
		t.Errorf("column = %d, want the widest label's width %d", column, longest)
	}
}

// Every challenge after the first asks for a margin above it, so nineteen rows read
// as groups. Not the first: a margin above row zero would push the whole group down
// from the y it was configured at.
func TestChallengeLinesGapsBetweenChallenges(t *testing.T) {
	cfg := allCategories()
	rows := challengeLines(&View{Now: time.Now(), Challenges: board()}, cfg)

	if rows[0].Gap {
		t.Error("the first row must not have a gap above it")
	}
	gaps := 0
	for i, r := range rows {
		if r.Sub && r.Gap {
			t.Errorf("row %d (%q) is an objective and must not break the group", i, r.Text())
		}
		if r.Gap {
			gaps++
		}
	}
	// Six challenges, so five breaks between them.
	if gaps != 5 {
		t.Errorf("got %d gaps, want one per challenge after the first", gaps)
	}
	// The class carries it, since a margin cannot be expressed in markup.
	for _, r := range rows {
		if r.Gap && !hasClass(r.Classes(), "board-gap") {
			t.Errorf("row %q wants a gap but does not ask for the class", r.Text())
		}
	}
}

func hasClass(all []string, want string) bool {
	for _, s := range all {
		if s == want {
			return true
		}
	}
	return false
}
