package xp

import (
	"testing"
	"time"

	"df-hud/internal/model"
)

func TestComputeRate(t *testing.T) {
	start := time.Unix(1_786_484_051, 0)
	samples := []Sample{
		{At: start, Cumulative: 1_000_000, Source: "df_exptotal"},
		{At: start.Add(10 * time.Second), Cumulative: 1_001_000, Source: "df_exptotal"},
		{At: start.Add(20 * time.Second), Cumulative: 1_002_000, Source: "df_exptotal"},
	}
	rate := ComputeRate(samples, 3, Steady)
	if !rate.Available || rate.PerHour != 360_000 || rate.Provisional {
		t.Fatalf("rate = %+v", rate)
	}
}

func TestComputeRateRejectsMixedSources(t *testing.T) {
	start := time.Unix(1_786_484_051, 0)
	rate := ComputeRate([]Sample{
		{At: start, Cumulative: 1_000, Source: "df_exptotal"},
		{At: start.Add(time.Second), Cumulative: 900, Source: "exp table reconstruction"},
	}, 2, Steady)
	if rate.Available || rate.Why != "XP source changed" {
		t.Fatalf("rate = %+v", rate)
	}
}

func TestWindowResetConditions(t *testing.T) {
	start := time.Unix(1_786_484_051, 0)
	prev := Snapshot{At: start, CumulativeXP: 1_000}
	next := Snapshot{At: start.Add(time.Second), CumulativeXP: 1_100}
	if reason := WindowReset(prev, next, time.Minute); reason != "" {
		t.Fatalf("ordinary step reset: %q", reason)
	}
	next.BoostExp = model.Deadline{Forever: true}
	if reason := WindowReset(prev, next, time.Minute); reason != "the XP boost changed" {
		t.Fatalf("boost reset reason = %q", reason)
	}
}
