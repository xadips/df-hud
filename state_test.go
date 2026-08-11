package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := newStateStore(path)
	base := time.Now().Truncate(time.Second)

	s.Update(func(st *State) {
		st.Pins = []string{"Kill 500 zombies", "Scavenge 100 items"}
		st.ChallengeDone = map[string]bool{"Kill 500 zombies|2026-08-14": true}
		st.Grid = &GridTransform{OffsetX: 981, OffsetY: 981, Scale: 2, SolvedAt: base, Method: "helicopter"}
	})
	s.AppendXPSample(XPSample{At: base, Cumulative: 1000, Source: "df_exptotal"}, time.Minute)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	loaded := newStateStore(path)
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	got := loaded.Get()
	if len(got.Pins) != 2 || got.Pins[0] != "Kill 500 zombies" {
		t.Errorf("Pins = %v", got.Pins)
	}
	if !got.ChallengeDone["Kill 500 zombies|2026-08-14"] {
		t.Error("completion memory did not survive")
	}
	if got.Grid == nil || got.Grid.Scale != 2 || got.Grid.Method != "helicopter" {
		t.Errorf("Grid = %+v", got.Grid)
	}
	if len(got.XPSamples) != 1 || got.XPSamples[0].Cumulative != 1000 {
		t.Errorf("XPSamples = %v", got.XPSamples)
	}
}

func TestStateMissingFileIsAFreshStart(t *testing.T) {
	s := newStateStore(filepath.Join(t.TempDir(), "absent.json"))
	if err := s.Load(); err != nil {
		t.Errorf("a missing state file must not be an error: %v", err)
	}
	if got := s.Get(); len(got.XPSamples) != 0 || got.Grid != nil {
		t.Errorf("fresh state should be empty, got %+v", got)
	}
}

func TestStateCorruptFileIsQuarantined(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newStateStore(path)
	if err := s.Load(); err != nil {
		t.Errorf("a corrupt state file must not be fatal: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("the corrupt file should have been moved aside")
	}
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) != 1 {
		t.Errorf("expected one quarantined file, found %d", len(matches))
	}

	// An old schema is handled the same way.
	if err := os.WriteFile(path, []byte(`{"schema_version":0,"pins":["x"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s2 := newStateStore(path)
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	if got := s2.Get(); len(got.Pins) != 0 {
		t.Errorf("old-schema state should be discarded, got %v", got.Pins)
	}
}

func TestStateXPWindowTrims(t *testing.T) {
	s := newStateStore("")
	base := time.Now()
	// Ten samples a second apart, with a 5s window.
	for i := 0; i < 10; i++ {
		s.AppendXPSample(XPSample{
			At:         base.Add(time.Duration(i) * time.Second),
			Cumulative: int64(1000 + i*10),
			Source:     "df_exptotal",
		}, 5*time.Second)
	}
	got := s.Get().XPSamples
	if len(got) == 0 {
		t.Fatal("no samples kept")
	}
	newest := got[len(got)-1]
	if newest.Cumulative != 1090 {
		t.Errorf("newest sample = %d, want 1090", newest.Cumulative)
	}
	// Nothing older than the window may remain.
	cutoff := newest.At.Add(-5 * time.Second)
	for _, sample := range got {
		if sample.At.Before(cutoff) {
			t.Errorf("sample at %s is older than the window", sample.At)
		}
	}
	// And the window boundary itself is kept, so a 5s window at 1s spacing
	// holds 6 points rather than 5.
	if len(got) != 6 {
		t.Errorf("kept %d samples, want 6 (t-5s..t inclusive)", len(got))
	}
}

// TestStateXPSourceChangeResetsTheWindow is the one that prevents a spectacular
// wrong number: the two cumulative tiers differ by a large fixed constant, so a
// window spanning both would report a delta of hundreds of thousands of XP that
// nobody earned.
func TestStateXPSourceChangeResetsTheWindow(t *testing.T) {
	s := newStateStore("")
	base := time.Now()
	for i := 0; i < 3; i++ {
		s.AppendXPSample(XPSample{
			At:         base.Add(time.Duration(i) * time.Second),
			Cumulative: 10_000_000 + int64(i*100),
			Source:     "df_exptotal",
		}, time.Hour)
	}
	if got := len(s.Get().XPSamples); got != 3 {
		t.Fatalf("samples = %d, want 3", got)
	}

	// df_exptotal disappears and we fall back to the table, which is ~393k lower.
	s.AppendXPSample(XPSample{
		At:         base.Add(3 * time.Second),
		Cumulative: 999_000,
		Source:     "exp table reconstruction",
	}, time.Hour)

	got := s.Get().XPSamples
	if len(got) != 1 {
		t.Fatalf("samples = %d after a source change, want 1: the window must reset", len(got))
	}
	if got[0].Source != "exp table reconstruction" {
		t.Errorf("kept sample has source %q", got[0].Source)
	}
}

func TestStateResetXPWindow(t *testing.T) {
	s := newStateStore("")
	s.AppendXPSample(XPSample{At: time.Now(), Cumulative: 5, Source: "df_exptotal"}, time.Hour)
	s.ResetXPWindow("death")
	if got := len(s.Get().XPSamples); got != 0 {
		t.Errorf("samples = %d after a reset, want 0", got)
	}
	// Resetting an empty window is a no-op, not a log line per tick.
	s.ResetXPWindow("death")
}

func TestStateSaveIsDebounced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := newStateStore(path)
	now := time.Now()
	s.now = func() time.Time { return now }

	s.Update(func(st *State) { st.Pins = []string{"a"} })
	if err := s.MaybeSave(); err != nil {
		t.Fatal(err)
	}
	// The first MaybeSave writes, since lastSave is zero.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the first save should have happened: %v", err)
	}
	first, _ := os.Stat(path)

	// A change straight afterwards must not write again: the XP ring changes on
	// every poll, and this is what stops a disk write every ten seconds forever.
	s.Update(func(st *State) { st.Pins = []string{"a", "b"} })
	if err := s.MaybeSave(); err != nil {
		t.Fatal(err)
	}
	second, _ := os.Stat(path)
	if !first.ModTime().Equal(second.ModTime()) || first.Size() != second.Size() {
		t.Error("a save inside the debounce window should have been skipped")
	}

	// Past the interval, it writes.
	now = now.Add(stateSaveInterval + time.Second)
	if err := s.MaybeSave(); err != nil {
		t.Fatal(err)
	}
	third, _ := os.Stat(path)
	if third.Size() == second.Size() {
		t.Error("after the debounce elapsed the new state should have been written")
	}

	// And with nothing dirty, MaybeSave does nothing at all.
	now = now.Add(stateSaveInterval + time.Second)
	if err := s.MaybeSave(); err != nil {
		t.Fatal(err)
	}
	fourth, _ := os.Stat(path)
	if !third.ModTime().Equal(fourth.ModTime()) {
		t.Error("a clean state should not be rewritten")
	}
}

func TestStateGetReturnsACopy(t *testing.T) {
	s := newStateStore("")
	s.Update(func(st *State) {
		st.Pins = []string{"original"}
		st.ChallengeDone = map[string]bool{"x": true}
		st.Grid = &GridTransform{Scale: 1}
	})

	got := s.Get()
	got.Pins[0] = "mutated"
	got.ChallengeDone["x"] = false
	got.Grid.Scale = 99

	// The UI thread holds one of these while the poller writes; aliasing would
	// be a data race that only shows up under load.
	inside := s.Get()
	if inside.Pins[0] != "original" {
		t.Error("mutating the returned copy changed the store's slice")
	}
	if !inside.ChallengeDone["x"] {
		t.Error("mutating the returned copy changed the store's map")
	}
	if inside.Grid.Scale != 1 {
		t.Error("mutating the returned copy changed the store's grid")
	}
}

func TestGridTransformApply(t *testing.T) {
	// Nil is the normal state: the transform is unsolved, and a nil pointer says
	// so where a zero offset would be a false claim.
	var unsolved *GridTransform
	if _, _, ok := unsolved.Apply(1054, 1016, 39, 39); ok {
		t.Error("a nil transform must not resolve")
	}

	g := &GridTransform{OffsetX: 1000, OffsetY: 1000, Scale: 2}
	gx, gy, ok := g.Apply(1004, 1006, 39, 39)
	if !ok || gx != 3 || gy != 4 {
		t.Errorf("Apply = (%d,%d,%v), want (3,4,true)", gx, gy, ok)
	}
	// Off the grid must fail rather than clamp to an edge block that would then
	// be reported as your location.
	for _, xy := range [][2]int{{999, 1000}, {1000, 999}, {1100, 1000}, {1000, 1100}} {
		if _, _, ok := g.Apply(xy[0], xy[1], 39, 39); ok {
			t.Errorf("Apply%v should be off the grid", xy)
		}
	}
	// A zero scale is a broken transform, not a divide by zero.
	broken := &GridTransform{OffsetX: 1000, OffsetY: 1000, Scale: 0}
	if _, _, ok := broken.Apply(1004, 1006, 39, 39); ok {
		t.Error("a zero scale must not resolve")
	}
}
