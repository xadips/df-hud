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
	if !strings.Contains(lines[0], "Summer Death") || !strings.Contains(lines[0], "55/100") {
		t.Errorf("line = %q, want the name and progress", lines[0])
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
