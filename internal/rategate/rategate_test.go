package rategate

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestGateSpacesSequentialRequests(t *testing.T) {
	gate := New(40 * time.Millisecond)
	var times []time.Time
	for range 4 {
		if err := gate.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
		times = append(times, time.Now())
	}
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap < 35*time.Millisecond {
			t.Errorf("gap %d = %s, want at least 35ms", i, gap)
		}
	}
}

func TestGateSpacesConcurrentCallers(t *testing.T) {
	const min = 20 * time.Millisecond
	gate := New(min)
	var mu sync.Mutex
	var times []time.Time
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := gate.Wait(context.Background()); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			times = append(times, time.Now())
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(times) != 4 {
		t.Fatalf("got %d releases", len(times))
	}
	if spread := latest(times).Sub(earliest(times)); spread < 2*min {
		t.Errorf("spread %s is too short", spread)
	}
}

func TestGateRespectsContext(t *testing.T) {
	gate := New(time.Hour)
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := gate.Wait(ctx); err == nil {
		t.Error("cancelled wait returned nil")
	}
}

func TestReservedSlotsAreSpaced(t *testing.T) {
	gate := New(time.Millisecond)
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := gate.Reserved()
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gap := gate.Reserved().Sub(first); gap < time.Millisecond {
		t.Errorf("reserved slots are %s apart", gap)
	}
}

func earliest(times []time.Time) time.Time {
	v := times[0]
	for _, tm := range times[1:] {
		if tm.Before(v) {
			v = tm
		}
	}
	return v
}

func latest(times []time.Time) time.Time {
	v := times[0]
	for _, tm := range times[1:] {
		if tm.After(v) {
			v = tm
		}
	}
	return v
}
