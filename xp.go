package main

import "time"

// The XP rate. Pure functions over the sample ring, so every timeline that
// matters - a level-up, a death, a boost starting, a dropped poll, a clock jump -
// can be tested without a network or a GUI.
//
// The rate itself is the easy part. What takes care is knowing when NOT to show
// one: a number that is confidently wrong is worse than a blank, because you
// cannot tell it is wrong by looking at it.

// xpStability is how much to trust the current rate, driven by whether recent
// scheduled polls actually produced samples.
type xpStability int

const (
	xpSteady   xpStability = iota // every recent poll landed
	xpShaky                       // one poll missed
	xpUnstable                    // two or more missed
)

func (s xpStability) String() string {
	switch s {
	case xpShaky:
		return "shaky"
	case xpUnstable:
		return "unstable"
	}
	return "steady"
}

// CSSClass is the colour: default text for steady, amber for shaky, red for
// unstable. Same vocabulary as the status line, so one glance reads the same way
// everywhere on the HUD.
func (s xpStability) CSSClass() string {
	switch s {
	case xpShaky:
		return "shaky"
	case xpUnstable:
		return "unstable"
	}
	return ""
}

// XPRate is the computed rate plus enough context to explain itself.
type XPRate struct {
	Available bool
	PerHour   float64
	Gained    int64
	Span      time.Duration
	Samples   int
	Stability xpStability

	// Why is set when Available is false, so the HUD can say what it is waiting
	// for instead of showing an unexplained blank.
	Why string
}

// computeXPRate turns the sample window into a rate.
//
// It uses the oldest and newest samples rather than a least-squares fit on
// purpose: cumulative XP is a step function (a kill lands a lump), so a fit
// would smooth over exactly the lumps that make up the total, and the endpoints
// already give the exact XP earned across the window - which is the definition
// of the average rate.
func computeXPRate(samples []XPSample, minSamples int, stability xpStability) XPRate {
	if minSamples < 2 {
		minSamples = 2
	}
	if len(samples) < minSamples {
		return XPRate{
			Samples: len(samples),
			Why:     "collecting samples",
		}
	}

	oldest, newest := samples[0], samples[len(samples)-1]

	// A source change should already have cleared the window (see
	// stateStore.AppendXPSample); if one slipped through, the two tiers differ by
	// a large constant and the delta would be nonsense.
	if oldest.Source != newest.Source {
		return XPRate{Samples: len(samples), Why: "XP source changed"}
	}

	span := newest.At.Sub(oldest.At)
	if span <= 0 {
		// Equal or reversed timestamps: a clock jump, or samples recorded inside
		// the same second. Either way there is no rate to compute.
		return XPRate{Samples: len(samples), Why: "no elapsed time"}
	}

	gained := newest.Cumulative - oldest.Cumulative
	if gained < 0 {
		// Cumulative XP going backwards means a death or a reset that the caller
		// should have caught. Refuse rather than render a negative rate.
		return XPRate{Samples: len(samples), Why: "XP went backwards"}
	}

	return XPRate{
		Available: true,
		PerHour:   float64(gained) / span.Seconds() * 3600,
		Gained:    gained,
		Span:      span,
		Samples:   len(samples),
		Stability: stability,
	}
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
func xpWindowReset(prev, next Snapshot, window time.Duration) string {
	if prev.At.IsZero() {
		return ""
	}
	if next.At.Before(prev.At) {
		return "the clock went backwards"
	}
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
