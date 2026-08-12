package main

import (
	"strings"
	"testing"
	"time"
)

func board() []Challenge {
	end := time.Now().Add(5 * 24 * time.Hour)
	soon := time.Now().Add(20 * time.Minute)
	return []Challenge{
		{Name: "Summer Death", End: end, Objectives: []Objective{
			{Name: "Kill Regular Infected", Target: 100, Score: 55, HasScore: true},
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

// Pins are matched by NAME because the index and the end time both rotate every
// cycle - a pin stored by either would silently follow a different challenge next
// week.
func TestPickPinnedFollowsTheNames(t *testing.T) {
	cfg := ChallengesWidgetConfig{MaxShown: 3, ShowClan: true}
	got := pickPinned(board(), []string{"Nearly There", "Summer Death"}, cfg)

	if len(got) != 2 {
		t.Fatalf("got %d pinned, want 2", len(got))
	}
	// Pin order wins over board order, so the HUD layout stays where it was put.
	if got[0].Name != "Nearly There" || got[1].Name != "Summer Death" {
		t.Errorf("order = %q, %q; want the pin list's order", got[0].Name, got[1].Name)
	}

	// A pin naming something no longer on the board is skipped, not an error: a
	// cycle can retire a challenge while the pin remains.
	got = pickPinned(board(), []string{"Long Gone", "Summer Death"}, cfg)
	if len(got) != 1 || got[0].Name != "Summer Death" {
		t.Errorf("a stale pin should be skipped, got %+v", got)
	}
}

func TestPickPinnedRespectsMaxShown(t *testing.T) {
	cfg := ChallengesWidgetConfig{MaxShown: 2, ShowClan: true}
	got := pickPinned(board(), []string{"Summer Death", "Nearly There", "Already Done"}, cfg)
	if len(got) != 2 {
		t.Errorf("got %d rows, want max_shown of 2", len(got))
	}
	// A max of zero is nonsense; one row is the floor rather than a panic.
	got = pickPinned(board(), []string{"Summer Death"}, ChallengesWidgetConfig{MaxShown: 0})
	if len(got) != 1 {
		t.Errorf("got %d rows, want 1", len(got))
	}
}

// The default with nothing pinned answers "what can I finish right now?" without
// anyone configuring anything.
func TestPickPinnedFallsBackToClosestToDone(t *testing.T) {
	cfg := ChallengesWidgetConfig{MaxShown: 2, ShowClan: true}
	got := pickPinned(board(), nil, cfg)

	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	// Nearly There (95%) then the clan challenge (98%)... ranked by fraction, so
	// the clan one leads.
	if got[0].Name != "Weekly Challenge - Kill Infected" {
		t.Errorf("first row = %q, want the highest fraction", got[0].Name)
	}
	if got[1].Name != "Nearly There" {
		t.Errorf("second row = %q", got[1].Name)
	}

	names := got[0].Name + " " + got[1].Name
	// Untouched challenges are noise on a HUD, and completed ones need no
	// attention.
	if strings.Contains(names, "Untouched") {
		t.Error("an unstarted challenge should not be auto-shown")
	}
	if strings.Contains(names, "Already Done") {
		t.Error("a completed challenge should not be auto-shown")
	}
}

func TestPickPinnedCanHideClanChallenges(t *testing.T) {
	cfg := ChallengesWidgetConfig{MaxShown: 5, ShowClan: false}

	if got := pickPinned(board(), nil, cfg); len(got) > 0 {
		for _, c := range got {
			if c.Clan {
				t.Error("show_clan=false must exclude clan challenges from the fallback")
			}
		}
	}
	// And from explicit pins too, otherwise the setting only half works.
	got := pickPinned(board(), []string{"Weekly Challenge - Kill Infected", "Summer Death"}, cfg)
	for _, c := range got {
		if c.Clan {
			t.Error("show_clan=false must exclude a clan challenge even when pinned")
		}
	}
}

func TestChallengeLines(t *testing.T) {
	cfg := ChallengesWidgetConfig{MaxShown: 3, ShowClan: true}
	v := &View{
		Now:    time.Now(),
		Pinned: pickPinned(board(), []string{"Summer Death", "Nearly There", "Already Done"}, cfg),
	}
	lines := challengeLines(v, cfg)
	if len(lines) != 3 {
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
	if !strings.Contains(lines[1], "m") {
		t.Errorf("a deadline within the day should be shown: %q", lines[1])
	}
	if !strings.Contains(lines[2], "done") {
		t.Errorf("a completed challenge should say so: %q", lines[2])
	}
}

// An empty board with a reason must explain itself: "no challenges" alone would
// never point anyone at the missing salt or cookie.
func TestChallengeLinesExplainsAnEmptyBoard(t *testing.T) {
	cfg := ChallengesWidgetConfig{MaxShown: 3, ShowClan: true}

	lines := challengeLines(&View{Now: time.Now(), ChallengeStatus: "no signing salt yet"}, cfg)
	if len(lines) != 1 || !strings.Contains(lines[0], "no signing salt") {
		t.Errorf("lines = %v, want the reason", lines)
	}
	// With no board and no reason, the widget stays quiet rather than showing a
	// bare label.
	if lines := challengeLines(&View{Now: time.Now()}, cfg); len(lines) != 0 {
		t.Errorf("lines = %v, want none", lines)
	}
}

func TestChallengeFractionCaps(t *testing.T) {
	over := Challenge{Objectives: []Objective{{Target: 100, Score: 250, HasScore: true}}}
	if f := challengeFraction(over); f != 1 {
		t.Errorf("fraction = %v, want it capped at 1", f)
	}
	if f := challengeFraction(Challenge{}); f != 0 {
		t.Errorf("fraction = %v for an empty challenge, want 0", f)
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
	s.SetPinned(pickPinned(b, []string{"Summer Death"}, ChallengesWidgetConfig{MaxShown: 3, ShowClan: true}))

	v := s.Derive(now)
	if v.ChallengesTotal != len(b) {
		t.Errorf("ChallengesTotal = %d, want %d", v.ChallengesTotal, len(b))
	}
	if v.ChallengesDone != 1 {
		t.Errorf("ChallengesDone = %d, want 1", v.ChallengesDone)
	}
	if len(v.Pinned) != 1 || v.Pinned[0].Name != "Summer Death" {
		t.Errorf("Pinned = %+v", v.Pinned)
	}
	if got, ok := s.Challenges(); !ok || len(got) != len(b) {
		t.Error("the full board should be available for the console window")
	}
}
