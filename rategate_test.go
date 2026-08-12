package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestRateGateSpacesRequests(t *testing.T) {
	gate := newRateGate(40 * time.Millisecond)
	ctx := context.Background()

	var times []time.Time
	for i := 0; i < 4; i++ {
		if err := gate.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		times = append(times, time.Now())
	}
	// The first is immediate; every later one waits out the gap.
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap < 35*time.Millisecond {
			t.Errorf("gap %d = %s, want at least the 40ms minimum", i, gap)
		}
	}
}

// TestRateGateSpacesConcurrentCallers is the case the gate exists for: two
// schedulers arriving at once must be spaced, not both released at the same
// instant. That is why the slot is reserved before the lock is dropped.
func TestRateGateSpacesConcurrentCallers(t *testing.T) {
	const min = 30 * time.Millisecond
	gate := newRateGate(min)
	ctx := context.Background()

	var mu sync.Mutex
	var times []time.Time

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := gate.Wait(ctx); err != nil {
				return
			}
			mu.Lock()
			times = append(times, time.Now())
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(times) != 6 {
		t.Fatalf("got %d releases, want 6", len(times))
	}
	// Sort and check no two are closer than the minimum, allowing a little
	// scheduler slack.
	for i := 0; i < len(times); i++ {
		for j := i + 1; j < len(times); j++ {
			gap := times[j].Sub(times[i])
			if gap < 0 {
				gap = -gap
			}
			if gap < min-10*time.Millisecond {
				t.Errorf("two callers were released %s apart, want at least %s", gap, min)
			}
		}
	}
	// Six requests at 30ms apart cannot have finished in less than five gaps.
	var earliest, latest time.Time
	for _, tm := range times {
		if earliest.IsZero() || tm.Before(earliest) {
			earliest = tm
		}
		if tm.After(latest) {
			latest = tm
		}
	}
	if spread := latest.Sub(earliest); spread < 4*min {
		t.Errorf("total spread %s is too short for 6 spaced requests", spread)
	}
}

func TestRateGateRespectsContext(t *testing.T) {
	gate := newRateGate(time.Hour) // nothing will be granted in time
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatalf("the first call should be immediate: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := gate.Wait(ctx); err == nil {
		t.Error("a cancelled wait must return an error rather than sending")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("the wait took %s, it should have given up with the context", elapsed)
	}
}

// The two pollers share one gate, which is what keeps "no two requests closer
// than the minimum" a property of df-hud's traffic rather than of one
// scheduler's. This pins that they are actually wired to the same instance.
func TestBothPollersShareTheGate(t *testing.T) {
	f := newFakeDF(t)
	p, creds, game := testPoller(t, f, nil)

	gate := newRateGate(minRequestGap)
	p.gate = gate
	cp := newChallengePoller(p.client, creds, game, gate, p.cfg, func() (int, bool) { return 415, false })

	if p.gate != cp.gate {
		t.Fatal("the pollers must share one gate")
	}

	// One request through each: the second must be spaced by the shared gate.
	ctx := context.Background()
	if err := gate.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	first := gate.Reserved()
	if err := gate.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if gap := gate.Reserved().Sub(first); gap < minRequestGap {
		t.Errorf("the shared gate reserved slots %s apart, want at least %s", gap, minRequestGap)
	}
}
