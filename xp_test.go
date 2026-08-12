package main

import (
	"errors"
	"testing"
	"time"
)

// errNoData stands in for a transport failure in the stability timelines.
var errNoData = errors.New("no data")

// ring builds a sample window: n samples, spacing apart, each gaining perSample.
func ring(start time.Time, n int, spacing time.Duration, base, perSample int64) []XPSample {
	out := make([]XPSample, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, XPSample{
			At:         start.Add(time.Duration(i) * spacing),
			Cumulative: base + int64(i)*perSample,
			Source:     "df_exptotal",
		})
	}
	return out
}

func TestComputeXPRate(t *testing.T) {
	start := time.Unix(1786484051, 0)
	// 4 samples 10s apart, 1000 XP each step: 3000 XP over 30s = 360,000/hr.
	rate := computeXPRate(ring(start, 4, 10*time.Second, 1_000_000, 1000), 3, xpSteady)
	if !rate.Available {
		t.Fatalf("rate should be available: %+v", rate)
	}
	if rate.PerHour != 360_000 {
		t.Errorf("PerHour = %.0f, want 360000", rate.PerHour)
	}
	if rate.Gained != 3000 {
		t.Errorf("Gained = %d, want 3000", rate.Gained)
	}
	if rate.Span != 30*time.Second {
		t.Errorf("Span = %s, want 30s", rate.Span)
	}
	if rate.Samples != 4 {
		t.Errorf("Samples = %d, want 4", rate.Samples)
	}
}

// Below min_samples the rate must be blank rather than extrapolated: two points a
// second apart imply an hourly figure that is pure noise.
func TestComputeXPRateNeedsMinSamples(t *testing.T) {
	start := time.Unix(1786484051, 0)
	for n := 0; n < 3; n++ {
		rate := computeXPRate(ring(start, n, 10*time.Second, 1000, 100), 3, xpSteady)
		if rate.Available {
			t.Errorf("%d samples should not produce a rate", n)
		}
		if rate.Why == "" {
			t.Errorf("%d samples: the widget needs a reason to display", n)
		}
	}
	if rate := computeXPRate(ring(start, 3, 10*time.Second, 1000, 100), 3, xpSteady); !rate.Available {
		t.Error("exactly min_samples should produce a rate")
	}
	// min_samples below 2 is meaningless and must be floored, not honoured.
	if rate := computeXPRate(ring(start, 1, time.Second, 1000, 100), 0, xpSteady); rate.Available {
		t.Error("one sample can never give a rate, whatever min_samples says")
	}
}

// A window spanning both cumulative tiers would compute a delta of hundreds of
// thousands of XP that nobody earned. AppendXPSample resets on a source change;
// this is the second line of defence.
func TestComputeXPRateRefusesMixedSources(t *testing.T) {
	start := time.Unix(1786484051, 0)
	samples := ring(start, 4, 10*time.Second, 24_690_000_000, 1000)
	samples[len(samples)-1].Source = "exp table reconstruction"
	samples[len(samples)-1].Cumulative -= 393_591 // the real offset between tiers

	rate := computeXPRate(samples, 3, xpSteady)
	if rate.Available {
		t.Errorf("a window spanning two XP sources must not produce a rate, got %.0f/hr", rate.PerHour)
	}
	if rate.Why != "XP source changed" {
		t.Errorf("Why = %q", rate.Why)
	}
}

func TestComputeXPRateRefusesNonsense(t *testing.T) {
	start := time.Unix(1786484051, 0)

	// Every sample in the same instant: no span, so no rate.
	flat := ring(start, 4, 0, 1000, 100)
	if rate := computeXPRate(flat, 3, xpSteady); rate.Available {
		t.Error("a zero span must not produce a rate")
	}

	// Cumulative XP falling: a death the caller failed to catch. A negative rate
	// is never the right thing to draw.
	falling := ring(start, 4, 10*time.Second, 1_000_000, 1000)
	falling[len(falling)-1].Cumulative = 500
	rate := computeXPRate(falling, 3, xpSteady)
	if rate.Available {
		t.Errorf("falling XP must not produce a rate, got %.0f/hr", rate.PerHour)
	}
	if rate.Why != "XP went backwards" {
		t.Errorf("Why = %q", rate.Why)
	}

	// Reversed timestamps, i.e. a clock step backwards mid-window.
	reversed := ring(start, 4, 10*time.Second, 1_000_000, 1000)
	reversed[0].At = start.Add(time.Hour)
	if rate := computeXPRate(reversed, 3, xpSteady); rate.Available {
		t.Error("a reversed window must not produce a rate")
	}
}

// A genuinely idle window is a real answer: zero XP per hour, not "unavailable".
func TestComputeXPRateZeroIsAValidRate(t *testing.T) {
	start := time.Unix(1786484051, 0)
	rate := computeXPRate(ring(start, 4, 10*time.Second, 1_000_000, 0), 3, xpSteady)
	if !rate.Available {
		t.Fatal("standing still is a rate of zero, not an absence of data")
	}
	if rate.PerHour != 0 {
		t.Errorf("PerHour = %.0f, want 0", rate.PerHour)
	}
	// And it renders as an explicit zero: a bare label reads as broken.
	if got := formatRate(0); got != "0" {
		t.Errorf("formatRate(0) = %q, want 0", got)
	}
}

func TestXPWindowResetConditions(t *testing.T) {
	base := time.Unix(1786484051, 0)
	window := 30 * time.Second

	prev := Snapshot{At: base, CumulativeXP: 1_000_000}
	next := Snapshot{At: base.Add(10 * time.Second), CumulativeXP: 1_001_000}

	if reason := xpWindowReset(prev, next, window); reason != "" {
		t.Errorf("an ordinary step should not reset the window, got %q", reason)
	}

	// A first snapshot has nothing to compare against.
	if reason := xpWindowReset(Snapshot{}, next, window); reason != "" {
		t.Errorf("no previous snapshot should not reset, got %q", reason)
	}

	cases := map[string]Snapshot{
		"the clock went backwards": {
			At: base.Add(-time.Minute), CumulativeXP: 1_001_000,
		},
		"cumulative XP fell (death or correction)": {
			At: base.Add(10 * time.Second), CumulativeXP: 900_000,
		},
		"a long gap between samples": {
			At: base.Add(5 * time.Minute), CumulativeXP: 1_001_000,
		},
	}
	for want, snap := range cases {
		if got := xpWindowReset(prev, snap, window); got != want {
			t.Errorf("expected reset %q, got %q", want, got)
		}
	}

	// A boost starting or ending changes the earn rate outright, so the samples
	// either side are not comparable.
	boosted := next
	boosted.BoostExp = dfDeadline{At: base.Add(time.Hour)}
	if got := xpWindowReset(prev, boosted, window); got != "the XP boost changed" {
		t.Errorf("a boost starting should reset the window, got %q", got)
	}
	// And ending.
	prevBoosted := prev
	prevBoosted.BoostExp = dfDeadline{Forever: true}
	if got := xpWindowReset(prevBoosted, next, window); got != "the XP boost changed" {
		t.Errorf("a boost ending should reset the window, got %q", got)
	}
	// An unchanged permanent boost must NOT reset every poll - the captured
	// account has one, so getting this wrong would blank the rate forever.
	stillBoosted := next
	stillBoosted.BoostExp = dfDeadline{Forever: true}
	if got := xpWindowReset(prevBoosted, stillBoosted, window); got != "" {
		t.Errorf("an unchanged boost must not reset the window, got %q", got)
	}
}

// TestStoreStabilityCarriesAMissForTwoTicks is the whole point of the amber
// state: a single missed poll that flashes for a fraction of a second and
// vanishes before you look up from the game is not a signal.
func TestStoreStabilityCarriesAMissForTwoTicks(t *testing.T) {
	s := newStore(nil)
	now := time.Now()
	ok := func() { s.ApplyTick(Tick{At: now, Vars: realPlayerRecord(), Scheduled: true}) }
	miss := func() { s.ApplyTick(Tick{At: now, Err: errNoData, Scheduled: true}) }

	ok()
	if got := s.Stability(); got != xpSteady {
		t.Fatalf("stability = %v, want steady", got)
	}

	miss()
	if got := s.Stability(); got != xpShaky {
		t.Errorf("during a miss: %v, want shaky", got)
	}

	ok() // first success after the miss still carries it
	if got := s.Stability(); got != xpShaky {
		t.Errorf("first tick after a miss: %v, want shaky (the miss stays visible)", got)
	}

	ok() // second success clears it
	if got := s.Stability(); got != xpSteady {
		t.Errorf("second tick after a miss: %v, want steady", got)
	}

	// Two misses in a row is the red state, and it survives one success too.
	miss()
	miss()
	if got := s.Stability(); got != xpUnstable {
		t.Errorf("two misses: %v, want unstable", got)
	}
	ok()
	if got := s.Stability(); got != xpUnstable {
		t.Errorf("first tick after two misses: %v, want unstable", got)
	}
	ok()
	if got := s.Stability(); got != xpSteady {
		t.Errorf("second tick after two misses: %v, want steady", got)
	}

	// An unscheduled failure (-once, a manual refresh) is not a missed tick.
	s.ApplyTick(Tick{At: now, Err: errNoData, Scheduled: false})
	if got := s.Stability(); got != xpSteady {
		t.Errorf("an unscheduled failure changed stability to %v", got)
	}
}

func TestStoreDeriveIncludesTheRate(t *testing.T) {
	s := newStore(nil)
	start := time.Unix(1786484051, 0)
	samples := ring(start, 4, 10*time.Second, 1_000_000, 1000)
	s.SetXPWindow(func() []XPSample { return samples }, 3)
	s.SetCredentialsAt(start)
	s.ApplyTick(Tick{At: start, Vars: realPlayerRecord(), Scheduled: true})

	v := s.Derive(start.Add(time.Second))
	if !v.XPAvailable {
		t.Fatalf("the view should carry a rate: %+v", v.XPWhy)
	}
	if v.XPPerHour != 360_000 {
		t.Errorf("XPPerHour = %.0f, want 360000", v.XPPerHour)
	}
	if v.XPStability != xpSteady {
		t.Errorf("XPStability = %v", v.XPStability)
	}

	// Without a window wired in, the XP fields stay blank rather than lying.
	bare := newStore(nil)
	bare.ApplyTick(Tick{At: start, Vars: realPlayerRecord(), Scheduled: true})
	if bare.Derive(start).XPAvailable {
		t.Error("with no sample window the rate must be unavailable")
	}
}

func TestXPLine(t *testing.T) {
	cfg := defaultConfig().Widget.XP
	// A real rate, with the stability colour. Grouped in full rather than compact:
	// at high level the digits below the millions are where the difference between
	// a good run and an ordinary one lives.
	v := &View{HaveData: true, XPAvailable: true, XPPerHour: 1_234_567, XPStability: xpShaky}
	text, class, show := xpLine(v, cfg)
	if !show || text != "Xp/Hr: 1,234,567" {
		t.Errorf("xpLine text = %q", text)
	}
	if class != "shaky" {
		t.Errorf("class = %q, want shaky", class)
	}

	// No rate yet: no row. The reason used to be rendered here, which put
	// "collecting samples" on screen for the first thirty seconds of every run and
	// after every reset - a progress report on df-hud's internals, on a HUD whose
	// job is to be glanceable. It stays in View.XPWhy for -print-view and the tray.
	if _, _, show := xpLine(&View{HaveData: true, XPWhy: "collecting samples"}, cfg); show {
		t.Error("a rate that is not available yet must not take a row")
	}

	// No data at all: no row.
	if _, _, show := xpLine(&View{}, cfg); show {
		t.Error("without data the xp row must be hidden")
	}
}

func TestStabilityCSSClasses(t *testing.T) {
	if xpSteady.CSSClass() != "" {
		t.Error("steady should use the default text colour")
	}
	if xpShaky.CSSClass() != "shaky" || xpUnstable.CSSClass() != "unstable" {
		t.Error("shaky and unstable need their own classes")
	}
	if xpSteady.String() != "steady" || xpShaky.String() != "shaky" || xpUnstable.String() != "unstable" {
		t.Error("stability should stringify for logs")
	}
}
