package challenges

import (
	"testing"
	"time"
)

func response() map[string]string {
	return map[string]string{
		"challenge_0_challenge_id":             "8017",
		"challenge_0_name":                     "Summer Death",
		"challenge_0_start_time":               "584880000",
		"challenge_0_end_time":                 "587299200",
		"challenge_0_min_level":                "1",
		"challenge_0_max_level":                "415",
		"challenge_0_objectives":               "1",
		"challenge_0_objectives_1_name":        "Kill Regular Infected",
		"challenge_0_objectives_1_target":      "100",
		"challenge_0_objective_1_player_score": "55",
		"challenge_0_repeatable":               "1",
		"challenge_0_reward_special":           "summerticket|10",

		"challenge_10_name":                "Hidden",
		"challenge_10_min_level":           "1",
		"challenge_10_max_level":           "44",
		"challenge_10_objectives":          "1",
		"challenge_10_objectives_1_target": "50",

		"challenge_11_name":                     "What Doc Ordered",
		"challenge_11_min_level":                "45",
		"challenge_11_max_level":                "74",
		"challenge_11_objectives":               "1",
		"challenge_11_objectives_1_target":      "5",
		"challenge_11_objective_1_player_score": "2",
		"challenge_11_reward_exp":               "2500",

		"challenge_clan_3_name":                     "Weekly Challenge - Travel Blocks",
		"challenge_clan_3_end_time":                 "586953931",
		"challenge_clan_3_objectives":               "1",
		"challenge_clan_3_objectives_1_name":        "Travel Blocks",
		"challenge_clan_3_objectives_1_target":      "360",
		"challenge_clan_3_objective_1_player_score": "366",
		"challenge_clan_3_reward_points":            "20",
		"max_challenges":                            "15challenge_clan_0_challenge_id=210",
	}
}

func TestRepairGluedPairs(t *testing.T) {
	vars := map[string]string{"max_challenges": "15challenge_clan_0_challenge_id=210"}
	repairGluedPairs(vars)
	if vars["max_challenges"] != "15" || vars["challenge_clan_0_challenge_id"] != "210" {
		t.Errorf("repair = %#v", vars)
	}
	vars = map[string]string{"armour": "hazardResistance=0.25"}
	repairGluedPairs(vars)
	if vars["armour"] != "hazardResistance=0.25" {
		t.Errorf("legitimate value changed: %#v", vars)
	}
	vars = map[string]string{
		"max_challenges":                "15challenge_clan_0_challenge_id=210",
		"challenge_clan_0_challenge_id": "999",
	}
	repairGluedPairs(vars)
	if vars["challenge_clan_0_challenge_id"] != "999" {
		t.Error("repair clobbered an existing field")
	}
}

func TestParse(t *testing.T) {
	got := Parse(response(), 415, false)
	if len(got) != 4 {
		t.Fatalf("got %d challenges, want 4", len(got))
	}
	if got[0].Name != "Summer Death" || got[3].Name != "Weekly Challenge - Travel Blocks" {
		t.Errorf("unstable order: %#v", got)
	}
	if got[0].Objectives[0].Score != 55 || !got[0].Objectives[0].HasScore {
		t.Errorf("objective progress = %#v", got[0].Objectives[0])
	}
	if got[0].Objectives[0].Done() || !got[0].Repeatable ||
		got[0].RewardSpecial != "summerticket|10" {
		t.Errorf("personal challenge fields = %#v", got[0])
	}
	if want := time.Unix(587299200+dfTimeOffset, 0); !got[0].End.Equal(want) {
		t.Errorf("end = %s, want %s", got[0].End, want)
	}
	if got[1].Name != "What Doc Ordered" {
		t.Errorf("challenge with recorded progress was filtered: %#v", got)
	}
	for _, challenge := range got {
		if challenge.Name == "Hidden" {
			t.Error("out-of-band challenge without progress was not filtered")
		}
	}
	if got[1].RewardExp != 2500*415 {
		t.Errorf("RewardExp = %d", got[1].RewardExp)
	}
	if !got[3].Complete() || got[3].RewardPoints != 20 {
		t.Errorf("clan challenge = %#v", got[3])
	}
}

func TestParseOrderIsStable(t *testing.T) {
	var first []string
	for run := 0; run < 8; run++ {
		got := Parse(response(), 415, false)
		names := make([]string, len(got))
		for i, challenge := range got {
			names[i] = challenge.Name
		}
		if run == 0 {
			first = names
			continue
		}
		for i := range names {
			if names[i] != first[i] {
				t.Fatalf("run %d differs at %d: %q vs %q", run, i, names[i], first[i])
			}
		}
	}
	seenClan := false
	for _, challenge := range Parse(response(), 415, false) {
		if challenge.Clan {
			seenClan = true
		} else if seenClan {
			t.Error("personal challenge appeared after clan challenge")
		}
	}
}

func TestParseGoldReward(t *testing.T) {
	got := Parse(response(), 100, true)
	for _, c := range got {
		if c.Name == "What Doc Ordered" && c.RewardExp != 2500*100*2 {
			t.Errorf("gold RewardExp = %d", c.RewardExp)
		}
	}
}

func TestChallengeHelpers(t *testing.T) {
	c := Challenge{Objectives: []Objective{
		{Target: 10, Score: 4, HasScore: true},
		{Target: 20, Score: 20, HasScore: true},
	}}
	score, target := c.Progress()
	if score != 24 || target != 30 || c.Complete() || !c.Started() {
		t.Errorf("helpers disagree: %d/%d complete=%v started=%v", score, target, c.Complete(), c.Started())
	}
	if (Challenge{}).Complete() || (Objective{Score: 5}).Done() {
		t.Error("empty targets cannot be complete")
	}
	if got := (Objective{Target: 10, Score: 12}).Fraction(); got != 1 {
		t.Errorf("Fraction = %v", got)
	}
	if got := (Challenge{End: time.Now().Add(-time.Hour)}).Remaining(time.Now()); got != 0 {
		t.Errorf("Remaining = %s", got)
	}
	if got := (Challenge{}).Remaining(time.Now()); got != 0 {
		t.Errorf("empty Remaining = %s", got)
	}
	if !(Challenge{Clan: true}).Eligible(1) ||
		!(Challenge{}).Eligible(1) ||
		(Challenge{MinLevel: 10, MaxLevel: 20}).Eligible(1) ||
		!(Challenge{MinLevel: 10, MaxLevel: 20, Objectives: []Objective{{HasScore: true}}}).Eligible(1) {
		t.Error("eligibility rules changed")
	}
}

func TestCycleKey(t *testing.T) {
	end := time.Unix(586953931+dfTimeOffset, 0)
	a := Challenge{Index: 3, Name: "Travel", End: end}
	b := Challenge{Index: 1, Name: "Travel", End: end.Add(11 * time.Second)}
	if CycleKey(a) != CycleKey(b) {
		t.Error("index or seconds changed the cycle key")
	}
	if CycleKey(a) == CycleKey(Challenge{Name: "Travel", End: end.Add(7 * 24 * time.Hour)}) {
		t.Error("next week shared a cycle key")
	}
	if CycleKey(a) == CycleKey(Challenge{Name: "Other", End: end}) {
		t.Error("different challenges shared a cycle key")
	}
}

func TestParseEmpty(t *testing.T) {
	if got := Parse(map[string]string{}, 415, false); len(got) != 0 {
		t.Errorf("empty response returned %d challenges", len(got))
	}
	if got := Parse(map[string]string{"max_challenges": "15"}, 415, false); len(got) != 0 {
		t.Errorf("counts-only response returned %d challenges", len(got))
	}
}
