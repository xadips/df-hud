package xp

import (
	"strings"
	"testing"
	"time"

	"df-hud/internal/model"
)

func sampleRing(start time.Time, n int, spacing time.Duration, base, perSample int64) []Sample {
	out := make([]Sample, 0, n)
	for i := range n {
		out = append(out, Sample{
			At:         start.Add(time.Duration(i) * spacing),
			Cumulative: base + int64(i)*perSample,
			Source:     "df_exptotal",
		})
	}
	return out
}

func TestComputeRateDetails(t *testing.T) {
	start := time.Unix(1_786_484_051, 0)
	rate := ComputeRate(sampleRing(start, 4, 10*time.Second, 1_000_000, 1000), 3, Steady)
	if !rate.Available || rate.PerHour != 360_000 || rate.Gained != 3000 ||
		rate.Span != 30*time.Second || rate.Samples != 4 {
		t.Fatalf("rate = %+v", rate)
	}
}

func TestComputeRateNeedsTwoSamplesAndMarksProvisional(t *testing.T) {
	start := time.Unix(1_786_484_051, 0)
	for n := 0; n < 2; n++ {
		if rate := ComputeRate(sampleRing(start, n, 10*time.Second, 1000, 100), 3, Steady); rate.Available || rate.Why == "" {
			t.Errorf("%d samples produced %+v", n, rate)
		}
	}
	rate := ComputeRate(sampleRing(start, 2, 10*time.Second, 1000, 100), 3, Steady)
	if !rate.Available || !rate.Provisional || !strings.Contains(rate.Why, "2 of 3") {
		t.Fatalf("provisional rate = %+v", rate)
	}
	if rate := ComputeRate(sampleRing(start, 3, 10*time.Second, 1000, 100), 3, Steady); !rate.Available || rate.Provisional {
		t.Fatalf("full rate = %+v", rate)
	}
	if ComputeRate(sampleRing(start, 1, time.Second, 1000, 100), 0, Steady).Available {
		t.Fatal("one sample produced a rate")
	}
}

func TestComputeRateRefusesNonsense(t *testing.T) {
	start := time.Unix(1_786_484_051, 0)
	if ComputeRate(sampleRing(start, 4, 0, 1000, 100), 3, Steady).Available {
		t.Error("zero-span samples produced a rate")
	}
	falling := sampleRing(start, 4, 10*time.Second, 1_000_000, 1000)
	falling[len(falling)-1].Cumulative = 500
	if rate := ComputeRate(falling, 3, Steady); rate.Available || rate.Why != "XP went backwards" {
		t.Errorf("falling rate = %+v", rate)
	}
	reversed := sampleRing(start, 4, 10*time.Second, 1_000_000, 1000)
	reversed[0].At = start.Add(time.Hour)
	if ComputeRate(reversed, 3, Steady).Available {
		t.Error("reversed timestamps produced a rate")
	}
}

func TestComputeRateZeroIsValid(t *testing.T) {
	rate := ComputeRate(sampleRing(time.Unix(100, 0), 4, 10*time.Second, 1000, 0), 3, Steady)
	if !rate.Available || rate.PerHour != 0 {
		t.Fatalf("idle rate = %+v", rate)
	}
}

func TestWindowResetAllConditions(t *testing.T) {
	base := time.Unix(1_786_484_051, 0)
	window := 30 * time.Second
	prev := Snapshot{At: base, CumulativeXP: 1_000_000}
	next := Snapshot{At: base.Add(10 * time.Second), CumulativeXP: 1_001_000}
	if reason := WindowReset(Snapshot{}, next, window); reason != "" {
		t.Errorf("first snapshot reset: %q", reason)
	}
	for want, snap := range map[string]Snapshot{
		"the clock went backwards":                 {At: base.Add(-time.Minute), CumulativeXP: 1_001_000},
		"cumulative XP fell (death or correction)": {At: base.Add(10 * time.Second), CumulativeXP: 900_000},
		"a long gap between samples":               {At: base.Add(5 * time.Minute), CumulativeXP: 1_001_000},
	} {
		if got := WindowReset(prev, snap, window); got != want {
			t.Errorf("reset = %q, want %q", got, want)
		}
	}
	boosted := next
	boosted.BoostExp = model.Deadline{Forever: true}
	if got := WindowReset(prev, boosted, window); got != "the XP boost changed" {
		t.Errorf("boost start = %q", got)
	}
	prev.BoostExp = model.Deadline{Forever: true}
	if got := WindowReset(prev, next, window); got != "the XP boost changed" {
		t.Errorf("boost end = %q", got)
	}
	next.BoostExp = model.Deadline{Forever: true}
	if got := WindowReset(prev, next, window); got != "" {
		t.Errorf("unchanged boost reset = %q", got)
	}
}

func TestWindowResetIgnoresOutpostFlag(t *testing.T) {
	outpost := Snapshot{At: time.Now(), InOutpost: true, CumulativeXP: 1000}
	city := Snapshot{At: outpost.At.Add(10 * time.Second), InOutpost: false, CumulativeXP: 1000}
	if reason := WindowReset(outpost, city, 5*time.Minute); reason != "" {
		t.Fatalf("outpost flag reset window: %q", reason)
	}
}

func TestRunReset(t *testing.T) {
	var none time.Time
	first := time.Now()
	if !RunReset(none, first) || RunReset(first, first) ||
		!RunReset(first, first.Add(time.Hour)) || RunReset(first, none) {
		t.Fatal("run reset boundaries changed")
	}
}

func TestStabilityNamesAndClasses(t *testing.T) {
	if Steady.String() != "steady" || Shaky.String() != "shaky" || Unstable.String() != "unstable" {
		t.Fatal("stability names changed")
	}
	if Steady.CSSClass() != "" || Shaky.CSSClass() != "shaky" || Unstable.CSSClass() != "unstable" {
		t.Fatal("stability CSS classes changed")
	}
}
