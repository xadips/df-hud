package main

import (
	"testing"
	"time"
)

// liveChallengeResponse reproduces the shape of a real load_challenge response,
// including every quirk found in it. Hand-built rather than captured so each
// quirk is isolated deliberately - and so the repo carries no personal progress
// data.
//
// The exact values for challenge 0 and clan challenge 3 are from the live board.
func liveChallengeResponse() map[string]string {
	return map[string]string{
		// A personal challenge in range, with progress. Note the plural
		// "objectives_1_target" against the singular "objective_1_player_score".
		"challenge_0_challenge_id":             "8017",
		"challenge_0_name":                     "Summer Death",
		"challenge_0_description":              "We need you to thin out the horde this summer.",
		"challenge_0_start_time":               "584880000",
		"challenge_0_end_time":                 "587299200",
		"challenge_0_min_level":                "1",
		"challenge_0_max_level":                "415",
		"challenge_0_objectives":               "1",
		"challenge_0_objectives_1_name":        "Kill Regular Infected",
		"challenge_0_objectives_1_target":      "100",
		"challenge_0_objective_1_player_score": "55",
		"challenge_0_repeatable":               "1",
		"challenge_0_reward_cash":              "0",
		"challenge_0_reward_credits":           "0",
		"challenge_0_reward_exp":               "0",
		"challenge_0_reward_items":             "",
		"challenge_0_reward_special":           "summerticket|10",

		// Out of the level band with NO recorded score: the game hides this.
		"challenge_10_challenge_id":        "9001",
		"challenge_10_name":                "Scrap Metal Shortage",
		"challenge_10_min_level":           "1",
		"challenge_10_max_level":           "44",
		"challenge_10_objectives":          "2",
		"challenge_10_objectives_1_name":   "Scavenge Scrap",
		"challenge_10_objectives_1_target": "50",
		"challenge_10_objectives_2_name":   "Deliver Scrap",
		"challenge_10_objectives_2_target": "10",
		"challenge_10_end_time":            "587299200",
		"challenge_10_reward_exp":          "1000",
		"challenge_10_repeatable":          "0",

		// Out of the level band but WITH a score: stays yours.
		"challenge_11_challenge_id":             "9002",
		"challenge_11_name":                     "What Doc Ordered",
		"challenge_11_min_level":                "45",
		"challenge_11_max_level":                "74",
		"challenge_11_objectives":               "1",
		"challenge_11_objectives_1_name":        "Deliver Medicine",
		"challenge_11_objectives_1_target":      "5",
		"challenge_11_objective_1_player_score": "2",
		"challenge_11_end_time":                 "587299200",
		"challenge_11_reward_exp":               "2500",

		// A completed one, to pin the boundary condition.
		"challenge_12_challenge_id":             "9003",
		"challenge_12_name":                     "Get Some",
		"challenge_12_min_level":                "1",
		"challenge_12_max_level":                "415",
		"challenge_12_objectives":               "1",
		"challenge_12_objectives_1_name":        "Kill Anything",
		"challenge_12_objectives_1_target":      "10",
		"challenge_12_objective_1_player_score": "10",
		"challenge_12_end_time":                 "587299200",

		// A clan challenge: reward_points, no level band, and its challenge_id is
		// the field the server's missing "&" swallows.
		"challenge_clan_3_name":                     "Weekly Challenge - Travel Blocks",
		"challenge_clan_3_description":              "The objective for this challenge just came in!",
		"challenge_clan_3_start_time":               "586349131",
		"challenge_clan_3_end_time":                 "586953931",
		"challenge_clan_3_objectives":               "1",
		"challenge_clan_3_objectives_1_name":        "Travel Blocks",
		"challenge_clan_3_objectives_1_target":      "360",
		"challenge_clan_3_objective_1_player_score": "366",
		"challenge_clan_3_reward_points":            "20",

		// THE GLUE BUG, exactly as the server sends it.
		"max_challenges":      "15challenge_clan_0_challenge_id=210",
		"max_clan_challenges": "5",
	}
}

// TestRepairGluedPairs covers the server sending "max_challenges=15" and the next
// field with no "&" between them, which costs one field entirely and makes
// max_challenges unparseable.
func TestRepairGluedPairs(t *testing.T) {
	vars := map[string]string{
		"max_challenges": "15challenge_clan_0_challenge_id=210",
	}
	repairGluedPairs(vars)

	if got := vars["max_challenges"]; got != "15" {
		t.Errorf("max_challenges = %q, want the numeric prefix 15", got)
	}
	if got := vars["challenge_clan_0_challenge_id"]; got != "210" {
		t.Errorf("the swallowed field was not recovered: %q", got)
	}
}

// The repair must not touch values that legitimately contain "=". The public
// allstats feed has them, and corrupting those would be a worse bug than the one
// being fixed.
func TestRepairGluedPairsLeavesLegitimateValuesAlone(t *testing.T) {
	vars := map[string]string{
		"armour16_unique_parameters": "hazardResistance=0.25",
		"challenge_0_description":    "Kill 10 = ten zombies",
		"some_key":                   "plain",
	}
	before := map[string]string{}
	for k, v := range vars {
		before[k] = v
	}
	repairGluedPairs(vars)

	for k, want := range before {
		if got := vars[k]; got != want {
			t.Errorf("%s changed from %q to %q", k, want, got)
		}
	}
	if len(vars) != len(before) {
		t.Errorf("the repair invented %d key(s)", len(vars)-len(before))
	}
}

// An existing key must never be overwritten by a recovered one.
func TestRepairGluedPairsDoesNotClobber(t *testing.T) {
	vars := map[string]string{
		"max_challenges":                "15challenge_clan_0_challenge_id=210",
		"challenge_clan_0_challenge_id": "999",
	}
	repairGluedPairs(vars)
	if got := vars["challenge_clan_0_challenge_id"]; got != "999" {
		t.Errorf("an existing field was clobbered: %q", got)
	}
}

func TestParseChallenges(t *testing.T) {
	const level = 415
	got := parseChallenges(liveChallengeResponse(), level, false)

	byName := map[string]Challenge{}
	for _, c := range got {
		byName[c.Name] = c
	}

	// In range with progress.
	summer, ok := byName["Summer Death"]
	if !ok {
		t.Fatal("Summer Death should be on the board")
	}
	if len(summer.Objectives) != 1 {
		t.Fatalf("objectives = %d, want 1", len(summer.Objectives))
	}
	o := summer.Objectives[0]
	if o.Name != "Kill Regular Infected" || o.Target != 100 {
		t.Errorf("objective definition = %+v (plural keys)", o)
	}
	if o.Score != 55 || !o.HasScore {
		t.Errorf("objective progress = %d (singular key), has=%v", o.Score, o.HasScore)
	}
	if o.Done() {
		t.Error("55/100 is not done")
	}
	if !summer.Repeatable {
		t.Error("repeatable=1 should parse")
	}
	if summer.RewardSpecial != "summerticket|10" {
		t.Errorf("RewardSpecial = %q", summer.RewardSpecial)
	}
	// Compact epoch: unix minus 1.2e9, as challenge.js decodes it.
	if want := time.Unix(587299200+dfTimeOffset, 0); !summer.End.Equal(want) {
		t.Errorf("End = %s, want %s", summer.End, want)
	}

	// The level filter, ported from the game.
	if _, present := byName["Scrap Metal Shortage"]; present {
		t.Error("a challenge outside the level band with no score must be hidden")
	}
	if _, present := byName["What Doc Ordered"]; !present {
		t.Error("a challenge outside the level band WITH a score stays yours")
	}

	// Completion at the boundary.
	if done := byName["Get Some"]; !done.Complete() {
		t.Error("10/10 should be complete")
	}
	if summer.Complete() {
		t.Error("55/100 should not be complete")
	}

	// The clan challenge: points, no level band, over target.
	clan, ok := byName["Weekly Challenge - Travel Blocks"]
	if !ok {
		t.Fatal("the clan challenge should be on the board")
	}
	if !clan.Clan {
		t.Error("Clan should be true")
	}
	if clan.RewardPoints != 20 {
		t.Errorf("RewardPoints = %d, want 20", clan.RewardPoints)
	}
	if !clan.Complete() {
		t.Error("366/360 should be complete")
	}
	if f := clan.Objectives[0].Fraction(); f != 1 {
		t.Errorf("Fraction = %v, want it capped at 1 for an overshoot", f)
	}
	// The glue bug's victim, recovered.
	if clanZeroID := parseChallenges(liveChallengeResponse(), level, false); len(clanZeroID) == 0 {
		t.Error("expected challenges")
	}
}

// reward_exp is a per-level multiplier, not XP. Storing it raw would guarantee
// somebody later renders "2500" as the reward.
func TestParseChallengesScalesRewardExp(t *testing.T) {
	vars := liveChallengeResponse()
	got := parseChallenges(vars, 100, false)
	var doc Challenge
	for _, c := range got {
		if c.Name == "What Doc Ordered" {
			doc = c
		}
	}
	if doc.RewardExp != 2500*100 {
		t.Errorf("RewardExp = %d, want 2500 x level 100", doc.RewardExp)
	}

	// Gold membership doubles it (challenge.js:279-283).
	got = parseChallenges(liveChallengeResponse(), 100, true)
	for _, c := range got {
		if c.Name == "What Doc Ordered" && c.RewardExp != 2500*100*2 {
			t.Errorf("gold RewardExp = %d, want double", c.RewardExp)
		}
	}
}

// Ordering has to be stable: the console renders a list and pins match by name,
// so map iteration order would reshuffle the display on every poll.
func TestParseChallengesOrderIsStable(t *testing.T) {
	var first []string
	for run := 0; run < 8; run++ {
		got := parseChallenges(liveChallengeResponse(), 415, false)
		names := make([]string, 0, len(got))
		for _, c := range got {
			names = append(names, c.Name)
		}
		if run == 0 {
			first = names
			continue
		}
		if len(names) != len(first) {
			t.Fatalf("run %d returned %d challenges, first run returned %d", run, len(names), len(first))
		}
		for i := range names {
			if names[i] != first[i] {
				t.Fatalf("run %d order differs at %d: %q vs %q", run, i, names[i], first[i])
			}
		}
	}
	// Personal before clan.
	got := parseChallenges(liveChallengeResponse(), 415, false)
	seenClan := false
	for _, c := range got {
		if c.Clan {
			seenClan = true
		} else if seenClan {
			t.Error("a personal challenge appeared after a clan one")
		}
	}
}

func TestChallengeProgressAndHelpers(t *testing.T) {
	c := Challenge{Objectives: []Objective{
		{Name: "a", Target: 10, Score: 4, HasScore: true},
		{Name: "b", Target: 20, Score: 20, HasScore: true},
	}}
	score, target := c.Progress()
	if score != 24 || target != 30 {
		t.Errorf("Progress = %d/%d, want 24/30", score, target)
	}
	if c.Complete() {
		t.Error("one objective short of done is not complete")
	}
	if !c.Started() {
		t.Error("Started should be true with any progress")
	}

	// No objectives is not "complete": there is nothing to have done.
	if (Challenge{}).Complete() {
		t.Error("an empty challenge must not report complete")
	}
	// A zero target cannot be met, so it must not read as done.
	if (Objective{Target: 0, Score: 5}).Done() {
		t.Error("a zero target must not count as done")
	}

	// The countdown never goes negative.
	past := Challenge{End: time.Now().Add(-time.Hour)}
	if got := past.Remaining(time.Now()); got != 0 {
		t.Errorf("Remaining = %s for an expired challenge, want 0", got)
	}
	if got := (Challenge{}).Remaining(time.Now()); got != 0 {
		t.Errorf("Remaining = %s with no end time, want 0", got)
	}
}

// The cycle key has to survive what rotates. The index and the exact end time
// both move; the name plus the ending day does not.
func TestChallengeCycleKey(t *testing.T) {
	end := time.Unix(586953931+dfTimeOffset, 0)
	a := Challenge{Index: 3, Name: "Weekly Challenge - Travel Blocks", End: end}
	b := Challenge{Index: 1, Name: "Weekly Challenge - Travel Blocks", End: end.Add(11 * time.Second)}
	if challengeCycleKey(a) != challengeCycleKey(b) {
		t.Errorf("the key changed across a reindex and a few seconds of drift:\n %s\n %s",
			challengeCycleKey(a), challengeCycleKey(b))
	}

	// Next week is a different cycle.
	next := Challenge{Index: 3, Name: "Weekly Challenge - Travel Blocks", End: end.Add(7 * 24 * time.Hour)}
	if challengeCycleKey(a) == challengeCycleKey(next) {
		t.Error("next week's cycle must get its own key")
	}
	// And a different challenge is different even in the same cycle.
	other := Challenge{Index: 3, Name: "Weekly Challenge - Complete Challenges", End: end}
	if challengeCycleKey(a) == challengeCycleKey(other) {
		t.Error("two challenges ending together must not share a key")
	}
}

func TestParseChallengesEmptyResponse(t *testing.T) {
	if got := parseChallenges(map[string]string{}, 415, false); len(got) != 0 {
		t.Errorf("an empty response should yield no challenges, got %d", len(got))
	}
	// A response with only the counts and no challenge data must not invent any.
	if got := parseChallenges(map[string]string{"max_challenges": "15"}, 415, false); len(got) != 0 {
		t.Errorf("counts alone should yield no challenges, got %d", len(got))
	}
}
