package main

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The challenge board, from hotrods/load_challenge.
//
// Two things about this endpoint are worth knowing before reading further.
//
// It is HASHED, so it needs the signing salt: digest = MD5(SKeyGen + every
// parameter value in order). And unlike get_values it also needs the browser
// SESSION COOKIE - without one it redirects to the site's front page even with a
// correct signature and correct parameters. That was established by elimination:
// the salt matches md5.js, the parameters and their order match challenge.js
// exactly, the path is right (a bare POST there answers "Invalid action" rather
// than 404), and adding Referer and X-Requested-With changed nothing.
//
// The field names have three traps, all confirmed against a live response rather
// than inferred:
//
//  1. The count and the per-objective fields disagree about plurality:
//     challenge_N_objectives is a COUNT, challenge_N_objectives_J_name and
//     _target are plural, but the progress field is SINGULAR -
//     challenge_N_objective_J_player_score.
//  2. An absent player_score means no progress recorded, i.e. zero - but it is
//     also what the game uses to decide eligibility, so absent and zero are not
//     interchangeable.
//  3. The response has a missing "&": max_challenges runs straight into the next
//     field as "max_challenges=15challenge_clan_0_challenge_id=210". See
//     repairGluedPairs.

// challengeKeyRe matches both the personal and clan prefixes, capturing the
// "clan_" marker and the index.
var challengeKeyRe = regexp.MustCompile(`^challenge_(clan_)?(\d+)_(.+)$`)

// gluedValueRe matches a value that has swallowed the following key=value pair
// because the server omitted an "&": a numeric prefix, then something that looks
// like a field name, then "=".
var gluedValueRe = regexp.MustCompile(`^(\d*)([a-z][a-z0-9_]*)=(.*)$`)

// repairGluedPairs recovers fields lost to a missing "&" in the server's output.
//
// The live response contains exactly one, between max_challenges and the first
// clan challenge, so parseFlash sees a single segment and splits it on the first
// "=": max_challenges gets the value "15challenge_clan_0_challenge_id=210" and
// challenge_clan_0_challenge_id disappears entirely.
//
// The repair is deliberately narrow and lives HERE rather than in parseFlash,
// because values legitimately containing "=" exist elsewhere: the public
// allstats feed carries armour16_unique_parameters=hazardResistance=0.25, where
// the whole "hazardResistance=0.25" is the value. A general repair would corrupt
// that. This one only fires on a value shaped like <digits><field_name>=<rest>,
// and never overwrites a key that is already present.
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
			continue // no leading number: not the shape of this bug
		}
		vars[key] = numeric
		if _, exists := vars[gluedKey]; !exists {
			vars[gluedKey] = gluedValue
		}
	}
}

// Objective is one requirement within a challenge.
type Objective struct {
	Name   string
	Target int64
	Score  int64
	// HasScore distinguishes "no progress recorded" from "recorded as zero". The
	// game itself uses exactly this distinction to decide whether an
	// out-of-level-band challenge is still yours (see Eligible).
	HasScore bool
}

func (o Objective) Done() bool { return o.Target > 0 && o.Score >= o.Target }

// Fraction is progress in 0..1, capped so an overshoot does not render past the
// end of a progress bar.
func (o Objective) Fraction() float64 {
	if o.Target <= 0 {
		return 0
	}
	if f := float64(o.Score) / float64(o.Target); f < 1 {
		return f
	}
	return 1
}

// Challenge is one entry on the board.
type Challenge struct {
	Index int
	ID    string
	Name  string
	Desc  string
	Clan  bool

	Start time.Time
	End   time.Time

	Objectives []Objective

	MinLevel, MaxLevel int
	Repeatable         bool

	// RewardExp is already scaled: the server sends a per-level multiplier, and
	// the game multiplies by your level and doubles it for gold members
	// (challenge.js:279-283). Storing the raw multiplier would guarantee someone
	// later displays it as if it were XP.
	RewardExp     int64
	RewardCash    int64
	RewardCredits int64
	RewardPoints  int64 // clan challenges pay points instead
	RewardItems   string
	RewardSpecial string
}

// Complete is true when every objective is met. A challenge with no objectives
// is never complete, since there is nothing to have done.
func (c Challenge) Complete() bool {
	if len(c.Objectives) == 0 {
		return false
	}
	for _, o := range c.Objectives {
		if !o.Done() {
			return false
		}
	}
	return true
}

// Progress sums across objectives, for a single-line summary. Individual
// objectives are still available for the detailed view.
func (c Challenge) Progress() (score, target int64) {
	for _, o := range c.Objectives {
		score += o.Score
		target += o.Target
	}
	return score, target
}

// Started reports whether any progress exists at all, which is what makes a
// challenge worth showing on a HUD before you have engaged with it.
func (c Challenge) Started() bool {
	for _, o := range c.Objectives {
		if o.Score > 0 {
			return true
		}
	}
	return false
}

// Remaining is the countdown to the cycle's end.
func (c Challenge) Remaining(now time.Time) time.Duration {
	if c.End.IsZero() {
		return 0
	}
	if d := c.End.Sub(now); d > 0 {
		return d
	}
	return 0
}

// Eligible ports the game's own filter (challenge.js:284-297): a challenge
// outside your level band is hidden UNLESS one of its objectives already has a
// recorded score, because a challenge you have made progress on stays yours even
// after you outlevel it.
//
// Clan challenges carry no level band at all, so they are always eligible.
func (c Challenge) Eligible(level int) bool {
	if c.Clan || c.MinLevel == 0 && c.MaxLevel == 0 {
		return true
	}
	if level >= c.MinLevel && level <= c.MaxLevel {
		return true
	}
	for _, o := range c.Objectives {
		if o.HasScore {
			return true
		}
	}
	return false
}

// parseChallenges builds the board.
//
// It discovers indices from the keys present rather than trusting
// max_challenges. That is not just defensive: max_challenges is precisely the
// field the server's missing "&" corrupts, so trusting it would mean parsing
// zero challenges. Deriving the set from the data is also immune to the count
// and the data disagreeing after a game update.
func parseChallenges(vars map[string]string, level int, goldMember bool) []Challenge {
	repairGluedPairs(vars)

	type key struct {
		clan  bool
		index int
	}
	// fields[key][subkey] = value
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
		// Challenge timestamps use the compact epoch, the same as df_servertime:
		// challenge.js does d.setUTCSeconds(end_time + 1200000000).
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

		// The stored value is per level, doubled for gold members.
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
				// Plural for the definition...
				Name:   strings.TrimSpace(f["objectives_"+suffix+"_name"]),
				Target: atoi64Or(f["objectives_"+suffix+"_target"], 0),
			}
			// ...and SINGULAR for the progress. Not a typo: confirmed against a
			// live response.
			if raw, ok := f["objective_"+suffix+"_player_score"]; ok {
				o.Score, o.HasScore = atoi64Or(raw, 0), true
			}
			c.Objectives = append(c.Objectives, o)
		}

		if !c.Eligible(level) {
			continue
		}
		out = append(out, c)
	}

	// Personal first, then clan, each in the server's own order. Stable output
	// matters because the console window renders a list and pins are matched by
	// name; a set-iteration order would reshuffle the display every poll.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Clan != out[j].Clan {
			return !out[i].Clan
		}
		return out[i].Index < out[j].Index
	})
	return out
}

// challengeTime decodes a challenge timestamp: the compact epoch, unix minus
// 1.2e9, the same encoding df_servertime uses.
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

// challengeCycleKey identifies a challenge's cycle, for the sticky completion
// memory. Name plus the day the cycle ends: the index rotates, the end time
// shifts by seconds between polls, but "this week's Travel Blocks" is stable.
//
// Completion has to be remembered rather than recomputed because clan targets
// are derived from clan size, so a finished challenge can un-complete when
// somebody joins - the game's own board shows this.
func challengeCycleKey(c Challenge) string {
	day := ""
	if !c.End.IsZero() {
		day = c.End.UTC().Format("2006-01-02")
	}
	return c.Name + "|" + day
}
