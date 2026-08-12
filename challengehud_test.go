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

	lines := challengeLines(v, cfg)
	if len(lines) != 6 {
		t.Fatalf("lines = %v", lines)
	}
	// The OBJECTIVE, not just the name. "First Strike 0/7" does not say what the
	// seven are, which makes the number one you cannot act on.
	if !strings.Contains(lines[0], "Summer Death") || !strings.Contains(lines[0], "55/100") {
		t.Errorf("line = %q, want the name and progress", lines[0])
	}
	if !strings.Contains(lines[0], "Kill Regular Infected") {
		t.Errorf("line = %q, want the objective named", lines[0])
	}
	// A countdown appears only when it is close enough to matter; "5d" on every
	// row is noise.
	if strings.Contains(lines[0], "5d") {
		t.Errorf("a far-off deadline should not be shown: %q", lines[0])
	}
	if !strings.Contains(lines[3], "m") {
		t.Errorf("a deadline within the day should be shown: %q", lines[3])
	}
	if !strings.Contains(lines[1], "done") {
		t.Errorf("a completed challenge should say so: %q", lines[1])
	}
}

// The objective is the actionable half of a challenge, so it is on the row. The
// name stays too, because dropping it collides - the live board carries both
// "Summer Loot" and "Weekly Challenge - Loot Anything", each with an objective of
// "Loot Anything", and two identical rows with different numbers is worse than one
// long row.
func TestChallengeRowsNameTheObjective(t *testing.T) {
	now := time.Now()

	first := Challenge{
		Name: "First Strike", End: now.Add(20 * time.Hour),
		Objectives: []Objective{{Name: "Kill Any Boss", Target: 7}},
	}
	rows := challengeRows(first, now)
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want one", rows)
	}
	if !strings.Contains(rows[0], "First Strike") || !strings.Contains(rows[0], "Kill Any Boss") {
		t.Errorf("row = %q, want the challenge and the objective", rows[0])
	}
	if !strings.Contains(rows[0], "0/7") {
		t.Errorf("row = %q, want the progress", rows[0])
	}

	// When the name already contains the objective - which is exactly how the clan
	// board reads - saying it twice is noise.
	clan := Challenge{
		Name: "Weekly Challenge - Kill Infected", Clan: true, End: now.Add(4 * 24 * time.Hour),
		Objectives: []Objective{{Name: "Kill Infected", Target: 162401, Score: 162423, HasScore: true}},
	}
	rows = challengeRows(clan, now)
	if strings.Count(strings.ToLower(rows[0]), "kill infected") != 1 {
		t.Errorf("row = %q, want the objective named once", rows[0])
	}
	if !strings.Contains(rows[0], "done") {
		t.Errorf("row = %q, want the completion mark", rows[0])
	}
}

// Two objectives summed into one figure is not something you can act on either, so
// they get a row each under the challenge's name.
func TestChallengeRowsSplitsMultipleObjectives(t *testing.T) {
	now := time.Now()
	c := Challenge{
		Name: "Two Jobs", End: now.Add(3 * time.Hour),
		Objectives: []Objective{
			{Name: "Kill Dogs", Target: 100, Score: 100, HasScore: true},
			{Name: "Loot Food", Target: 25, Score: 4, HasScore: true},
		},
	}

	rows := challengeRows(c, now)
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

	lines := challengeLines(&View{Now: time.Now(), ChallengeStatus: "no signing salt yet"}, cfg)
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
