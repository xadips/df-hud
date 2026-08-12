package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Persistent state: the things that must survive a restart because they cannot
// be recovered from the server. The XP window is the main one - restart df-hud
// mid-run and without this the rate goes blank for a minute for no reason the
// player can see.
//
// Deliberately NOT persisted: the last player snapshot. It would let the HUD
// show numbers immediately at startup, but they would be yesterday's numbers
// rendered as if current, and the first poll is seconds away anyway. Data that
// is wrong is worse than data that is briefly absent.
const stateSchemaVersion = 1

// stateSaveInterval debounces writes. The XP ring changes on every poll, so
// saving per tick would be a disk write every ten seconds forever; the loss
// from a crash is at most this much history.
const stateSaveInterval = 30 * time.Second

// XPSample is one point in the rate window.
type XPSample struct {
	At         time.Time `json:"at"`
	Cumulative int64     `json:"cumulative"`

	// Source is which cumulative tier produced this number, stored as text so
	// the file stays readable and an enum renumbering cannot silently change
	// its meaning.
	//
	// It is here because the two tiers differ by a large constant (a fixed
	// ~393k for the captured account - see knowledge/allstats-map-and-xp.md).
	// If df_exptotal stopped being sent mid-session and we fell back to the
	// table, a window mixing both would compute a huge bogus delta. The rate
	// therefore resets when the source changes rather than averaging across it.
	Source string `json:"source"`
}

type State struct {
	SchemaVersion int       `json:"schema_version"`
	SavedAt       time.Time `json:"saved_at"`

	// XPSamples is the rate window, oldest first.
	XPSamples []XPSample `json:"xp_samples,omitempty"`

	// Pins are challenge names shown on the HUD. By name, because both the
	// index and the end time rotate every cycle, so neither identifies a
	// challenge across cycles.
	Pins []string `json:"pins,omitempty"`

	// PinsSeeded records that the config's pin list has been applied once.
	// Without it, unpinning everything would be indistinguishable from a first
	// boot and the config would silently re-seed on the next start - quietly
	// undoing a deliberate choice.
	PinsSeeded bool `json:"pins_seeded,omitempty"`

	// ChallengeDone is sticky per-cycle completion memory, keyed by
	// name + cycle end. The game's own board can un-complete a finished
	// challenge when targets recompute from clan size, so completion has to be
	// remembered rather than re-derived.
	ChallengeDone map[string]bool `json:"challenge_done,omitempty"`

	// Grid is the solved position-to-block-grid transform, nil until
	// calibrated. Nil is the normal state today: see
	// knowledge/allstats-map-and-xp.md.
	Grid *GridTransform `json:"grid,omitempty"`
}

// GridTransform maps a player position onto the catalog's 1-based block grid.
// Unsolved as of writing, which is why it is a nil pointer rather than a zero
// value: a zero offset would silently mean "the grid starts at position 0",
// which is a claim, not an absence.
type GridTransform struct {
	OffsetX int `json:"offset_x"`
	OffsetY int `json:"offset_y"`
	// Scale is position units per grid cell, at least 1.
	Scale int `json:"scale"`
	// SolvedAt and Method record where this came from, so a bad calibration can
	// be told apart from a good one later.
	SolvedAt time.Time `json:"solved_at"`
	Method   string    `json:"method"`
}

// Apply converts a position to grid indices. ok is false when there is no
// transform or the result falls outside the grid.
func (g *GridTransform) Apply(x, y, cols, rows int) (gx, gy int, ok bool) {
	if g == nil || g.Scale < 1 {
		return 0, 0, false
	}
	// The negative case has to be rejected BEFORE dividing. Go truncates integer
	// division toward zero, so a position one unit before the origin gives
	// -1/2 == 0 and lands on cell 1 - reporting the grid's corner as your
	// location instead of admitting you are off the map.
	dx, dy := x-g.OffsetX, y-g.OffsetY
	if dx < 0 || dy < 0 {
		return 0, 0, false
	}
	gx = dx/g.Scale + 1
	gy = dy/g.Scale + 1
	if gx > cols || gy > rows {
		return 0, 0, false
	}
	return gx, gy, true
}

// stateStore owns the file. Concurrency-safe, and saves are debounced.
type stateStore struct {
	mu       sync.Mutex
	path     string
	state    State
	dirty    bool
	lastSave time.Time
	now      func() time.Time
}

func newStateStore(path string) *stateStore {
	return &stateStore{
		path:  path,
		state: State{SchemaVersion: stateSchemaVersion},
		now:   time.Now,
	}
}

// Load reads the file. A missing file is a first run, not an error. A corrupt or
// old-schema file is quarantined and treated as a first run, so df-hud never
// crash-loops on its own state - the same discipline as the catalog cache and
// df-allstats-watcher's state.json.
func (s *stateStore) Load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil || st.SchemaVersion != stateSchemaVersion {
		aside := fmt.Sprintf("%s.corrupt-%d", s.path, s.now().Unix())
		if renameErr := os.Rename(s.path, aside); renameErr != nil {
			return fmt.Errorf("state file unusable and could not be moved aside: %w", renameErr)
		}
		log.Printf("state: file unusable, moved to %s, starting fresh", aside)
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = st
	return nil
}

// Update mutates the state under lock and marks it for saving. The callback
// keeps every mutation in one place, so there is no way to change state without
// the dirty flag being set.
func (s *stateStore) Update(fn func(*State)) {
	s.mu.Lock()
	fn(&s.state)
	s.state.SchemaVersion = stateSchemaVersion
	s.dirty = true
	s.mu.Unlock()
}

// Get returns a copy. A copy rather than a pointer because the caller is the UI
// thread and the writer is the poller.
func (s *stateStore) Get() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.clone()
}

func (st State) clone() State {
	out := st
	out.XPSamples = append([]XPSample(nil), st.XPSamples...)
	out.Pins = append([]string(nil), st.Pins...)
	if st.ChallengeDone != nil {
		out.ChallengeDone = make(map[string]bool, len(st.ChallengeDone))
		for k, v := range st.ChallengeDone {
			out.ChallengeDone[k] = v
		}
	}
	if st.Grid != nil {
		grid := *st.Grid
		out.Grid = &grid
	}
	return out
}

// MaybeSave writes if something changed and the debounce has elapsed.
func (s *stateStore) MaybeSave() error {
	s.mu.Lock()
	if !s.dirty || s.now().Sub(s.lastSave) < stateSaveInterval {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	return s.Save()
}

// Save writes unconditionally. Called on shutdown so the last window is kept.
func (s *stateStore) Save() error {
	s.mu.Lock()
	if s.path == "" {
		s.dirty = false
		s.mu.Unlock()
		return nil // memory-only, used by tests
	}
	s.state.SavedAt = s.now()
	snapshot := s.state.clone()
	s.mu.Unlock()

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return err
	}

	s.mu.Lock()
	s.dirty = false
	s.lastSave = s.now()
	s.mu.Unlock()
	return nil
}

// AppendXPSample adds a point and drops everything older than window. A change
// of source clears the window first: the two cumulative tiers differ by a large
// constant, so a delta spanning both would be nonsense.
func (s *stateStore) AppendXPSample(sample XPSample, window time.Duration) {
	s.Update(func(st *State) {
		if n := len(st.XPSamples); n > 0 && st.XPSamples[n-1].Source != sample.Source {
			log.Printf("state: cumulative XP source changed from %s to %s; resetting the rate window",
				st.XPSamples[n-1].Source, sample.Source)
			st.XPSamples = nil
		}
		st.XPSamples = append(st.XPSamples, sample)
		cutoff := sample.At.Add(-window)
		keep := 0
		for i, existing := range st.XPSamples {
			if !existing.At.Before(cutoff) {
				keep = i
				break
			}
			keep = i + 1
		}
		if keep > 0 {
			st.XPSamples = append([]XPSample(nil), st.XPSamples[keep:]...)
		}
	})
}

// ResetXPWindow drops the rate history, for a death, a boost change, or a clock
// jump - anything that makes the samples either side incomparable.
func (s *stateStore) ResetXPWindow(reason string) {
	s.Update(func(st *State) {
		if len(st.XPSamples) == 0 {
			return
		}
		log.Printf("state: resetting the XP rate window (%s)", reason)
		st.XPSamples = nil
	})
}
