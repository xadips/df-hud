package xp

import (
	"df-hud/internal/model"
	"strconv"
	"time"
)

// The XP rate. Pure functions over the sample ring, so every timeline that
// matters - a level-up, a death, a boost starting, a dropped poll, a clock jump -
// can be tested without a network or a GUI.
//
// The rate itself is the easy part. What takes care is saying how much to trust
// one: a number that is confidently wrong is worse than a marked estimate, because
// you cannot tell it is wrong by looking at it. Hence Provisional and Stability,
// which are the two ways the rate qualifies itself - too few samples, and samples
// that did not arrive.

type Sample = model.XPSample
type Snapshot = model.Snapshot
type Stability = model.XPStability
type Rate = model.XPRate

const (
	Steady   = model.XPSteady
	Shaky    = model.XPShaky
	Unstable = model.XPUnstable
)

// ComputeRate turns the sample window into a rate.
//
// It uses the oldest and newest samples rather than a least-squares fit on
// purpose: cumulative XP is a step function (a kill lands a lump), so a fit
// would smooth over exactly the lumps that make up the total, and the endpoints
// already give the exact XP earned across the window - which is the definition
// of the average rate.
// minSamples is the threshold for a rate being TRUSTED, not for it being shown:
// below it the rate is still returned, with Provisional set. Two samples is the
// arithmetic floor, since one point spans no time at all.
func ComputeRate(samples []Sample, minSamples int, stability Stability) Rate {
	if minSamples < 2 {
		minSamples = 2
	}
	if len(samples) < 2 {
		return Rate{
			Samples: len(samples),
			Why:     "collecting samples",
		}
	}
	provisional := len(samples) < minSamples

	oldest, newest := samples[0], samples[len(samples)-1]

	// A source change should already have cleared the window (see
	// stateStore.AppendXPSample); if one slipped through, the two tiers differ by
	// a large constant and the delta would be nonsense.
	if oldest.Source != newest.Source {
		return Rate{Samples: len(samples), Why: "XP source changed"}
	}

	span := newest.At.Sub(oldest.At)
	if span <= 0 {
		// Equal or reversed timestamps: a clock jump, or samples recorded inside
		// the same second. Either way there is no rate to compute.
		return Rate{Samples: len(samples), Why: "no elapsed time"}
	}

	gained := newest.Cumulative - oldest.Cumulative
	if gained < 0 {
		// Cumulative XP going backwards means a death or a reset that the caller
		// should have caught. Refuse rather than render a negative rate.
		return Rate{Samples: len(samples), Why: "XP went backwards"}
	}

	rate := Rate{
		Available:   true,
		PerHour:     float64(gained) / span.Seconds() * 3600,
		Gained:      gained,
		Span:        span,
		Samples:     len(samples),
		Stability:   stability,
		Provisional: provisional,
	}
	if provisional {
		// Kept for -print-view and the tray, so "why does that number have a tilde
		// on it" has an answer without reading the source.
		rate.Why = "provisional: " + strconv.Itoa(len(samples)) + " of " +
			strconv.Itoa(minSamples) + " samples"
	}
	return rate
}

// xpRunReset reports whether the rate window must be discarded because a new run
// began, given the previously known run start and the current one.
//
// A run boundary matters to the rate for the same reason a death does: the
// samples either side describe different activity. The window collected while the
// launcher sat on screen contains no XP at all, so averaging across the boundary
// under-reports the run's real rate for as long as the window is wide.
//
// A run ENDING is deliberately not a reset. The rate the run earned is still the
// last true thing the widget can say, and blanking it the moment you step into an
// outpost would throw the number away exactly when you want to read it.
func RunReset(prev, next time.Time) bool {
	return !next.IsZero() && !next.Equal(prev)
}

// xpWindowReset reports why the rate window must be discarded when moving from
// one snapshot to the next, or "" to keep it.
//
// Each of these makes the samples either side incomparable, so averaging across
// them would produce a rate that describes no real period:
//
//   - a boost starting or ending changes the earn rate outright
//   - cumulative XP falling means a death or a server-side correction
//   - the clock jumping backwards (suspend, NTP step) would give a negative or
//     absurd span
//   - a gap much longer than the window means the samples either side describe
//     different activity entirely; an alt-tab of an hour should not average with
//     the fight before it
func WindowReset(prev, next Snapshot, window time.Duration) string {
	if prev.At.IsZero() {
		return ""
	}
	if next.At.Before(prev.At) {
		return "the clock went backwards"
	}
	// Note what is NOT here: df_inoutpost changing. A new run does need the window
	// discarded - otherwise the flat samples from the launcher and the loading
	// screen are averaged in and the first minutes of a run read as a fraction of
	// the real rate - but that field turned out not to mean what its name suggests
	// (it was already "0" at the launcher), so a change in it is not evidence of
	// anything. The run boundary is decided by the store, from movement, and the
	// reset is driven by xpRunReset below.
	if prev.BoostExp != next.BoostExp {
		return "the XP boost changed"
	}
	if next.CumulativeXP < prev.CumulativeXP {
		return "cumulative XP fell (death or correction)"
	}
	// Two windows' worth of silence: generous enough not to fire on a slow poll,
	// tight enough that a long absence does not average into the next run.
	if gap := next.At.Sub(prev.At); window > 0 && gap > 2*window {
		return "a long gap between samples"
	}
	return ""
}
