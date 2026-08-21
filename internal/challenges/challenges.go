// Package challenges parses and models the Dead Frontier challenge board.
package challenges

import (
	"df-hud/internal/model"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const dfTimeOffset = 1_200_000_000

var challengeKeyRe = regexp.MustCompile(`^challenge_(clan_)?(\d+)_(.+)$`)
var gluedValueRe = regexp.MustCompile(`^(\d*)([a-z][a-z0-9_]*)=(.*)$`)

// repairGluedPairs recovers fields lost to a missing "&" in the server output.
func repairGluedPairs(vars map[string]string) {
	for key, value := range vars {
		if !strings.Contains(value, "=") {
			continue
		}
		m := gluedValueRe.FindStringSubmatch(value)
		if m == nil {
			continue
		}
		numeric, gluedKey, gluedValue := m[1], m[2], m[3]
		if numeric == "" {
			continue
		}
		vars[key] = numeric
		if _, exists := vars[gluedKey]; !exists {
			vars[gluedKey] = gluedValue
		}
	}
}

type Objective = model.Objective
type Challenge = model.Challenge

// Parse builds a stable, filtered challenge board from response fields.
func Parse(vars map[string]string, level int, goldMember bool) []Challenge {
	repairGluedPairs(vars)

	type key struct {
		clan  bool
		index int
	}
	fields := map[key]map[string]string{}
	for name, value := range vars {
		m := challengeKeyRe.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		index, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		k := key{clan: m[1] != "", index: index}
		if fields[k] == nil {
			fields[k] = map[string]string{}
		}
		fields[k][m[3]] = value
	}

	out := make([]Challenge, 0, len(fields))
	for k, f := range fields {
		c := Challenge{
			Index: k.index,
			Clan:  k.clan,
			ID:    f["challenge_id"],
			Name:  strings.TrimSpace(f["name"]),
			Desc:  strings.TrimSpace(f["description"]),
		}
		c.Start = challengeTime(f["start_time"])
		c.End = challengeTime(f["end_time"])
		c.MinLevel = atoiOr(f["min_level"], 0)
		c.MaxLevel = atoiOr(f["max_level"], 0)
		c.Repeatable = f["repeatable"] == "1"
		c.RewardCash = atoi64Or(f["reward_cash"], 0)
		c.RewardCredits = atoi64Or(f["reward_credits"], 0)
		c.RewardPoints = atoi64Or(f["reward_points"], 0)
		c.RewardItems = strings.TrimSpace(f["reward_items"])
		c.RewardSpecial = strings.TrimSpace(f["reward_special"])

		if exp := atoi64Or(f["reward_exp"], 0); exp > 0 && level > 0 {
			c.RewardExp = exp * int64(level)
			if goldMember {
				c.RewardExp *= 2
			}
		}

		count := atoiOr(f["objectives"], 0)
		for j := 1; j <= count; j++ {
			suffix := strconv.Itoa(j)
			o := Objective{
				Name:   strings.TrimSpace(f["objectives_"+suffix+"_name"]),
				Target: atoi64Or(f["objectives_"+suffix+"_target"], 0),
			}
			if raw, ok := f["objective_"+suffix+"_player_score"]; ok {
				o.Score, o.HasScore = atoi64Or(raw, 0), true
			}
			c.Objectives = append(c.Objectives, o)
		}
		if c.Eligible(level) {
			out = append(out, c)
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Clan != out[j].Clan {
			return !out[i].Clan
		}
		return out[i].Index < out[j].Index
	})
	return out
}

func challengeTime(raw string) time.Time {
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || v <= 0 {
		return time.Time{}
	}
	return time.Unix(v+dfTimeOffset, 0)
}

func atoiOr(raw string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return v
}

func atoi64Or(raw string, fallback int64) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return fallback
	}
	return v
}

// CycleKey identifies a challenge cycle despite index and second-level drift.
func CycleKey(c Challenge) string {
	day := ""
	if !c.End.IsZero() {
		day = c.End.UTC().Format("2006-01-02")
	}
	return c.Name + "|" + day
}
