package store

import (
	"errors"
	"testing"
	"time"

	"df-hud/internal/model"
)

func xpSamples(start time.Time, n int, spacing time.Duration, base, step int64) []XPSample {
	out := make([]XPSample, 0, n)
	for i := range n {
		out = append(out, XPSample{
			At:         start.Add(time.Duration(i) * spacing),
			Cumulative: base + int64(i)*step,
			Source:     "df_exptotal",
		})
	}
	return out
}

func TestStoreStabilityCarriesMissForTwoTicks(t *testing.T) {
	s := New(nil)
	now := time.Now()
	ok := func() { s.ApplyTick(Tick{At: now, Vars: realPlayerRecord(), Scheduled: true}) }
	miss := func() { s.ApplyTick(Tick{At: now, Err: errors.New("no data"), Scheduled: true}) }

	ok()
	if s.Stability() != model.XPSteady {
		t.Fatal("initial stability is not steady")
	}
	miss()
	if s.Stability() != model.XPShaky {
		t.Fatal("single miss is not shaky")
	}
	ok()
	if s.Stability() != model.XPShaky {
		t.Fatal("first success did not carry single miss")
	}
	ok()
	if s.Stability() != model.XPSteady {
		t.Fatal("second success did not clear miss")
	}
	miss()
	miss()
	if s.Stability() != model.XPUnstable {
		t.Fatal("two misses are not unstable")
	}
	ok()
	if s.Stability() != model.XPUnstable {
		t.Fatal("first success did not carry two misses")
	}
	ok()
	if s.Stability() != model.XPSteady {
		t.Fatal("second success did not clear misses")
	}
	s.ApplyTick(Tick{At: now, Err: errors.New("manual failure"), Scheduled: false})
	if s.Stability() != model.XPSteady {
		t.Fatal("unscheduled failure changed stability")
	}
}

func TestStoreDeriveIncludesRate(t *testing.T) {
	s := New(nil)
	start := time.Unix(1_786_484_051, 0)
	samples := xpSamples(start, 4, 10*time.Second, 1_000_000, 1000)
	s.SetXPWindow(func() []XPSample { return samples }, 3)
	s.SetCredentialsAt(start)
	s.ApplyTick(Tick{At: start, Vars: realPlayerRecord(), Scheduled: true})
	view := s.Derive(start.Add(time.Second))
	if !view.XPAvailable || view.XPPerHour != 360_000 || view.XPStability != model.XPSteady {
		t.Fatalf("derived rate = %+v", view)
	}

	bare := New(nil)
	bare.ApplyTick(Tick{At: start, Vars: realPlayerRecord(), Scheduled: true})
	if bare.Derive(start).XPAvailable {
		t.Fatal("store without a sample window exposed a rate")
	}
}
