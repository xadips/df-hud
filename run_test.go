package main

import (
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// The run clock, which is what the session widget shows.
//
// The behaviour these pin is the reason it is not the client's uptime: launching
// the game means a launcher, a Launch button, a loading screen and then a Start
// button, so the process can be minutes old before any playing happens.

// cityRecord is the player record out in the inner city, which is the state that
// starts a run.
func cityRecord() map[string]string {
	vars := realPlayerRecord()
	vars["df_inoutpost"] = "0"
	vars["df_tradezone"] = "5"
	return vars
}

func runningGame(started time.Time) GameState {
	return GameState{Running: true, PID: 4242, StartedAt: started}
}

// movedTo is a city record at a given block.
func movedTo(x, y int) map[string]string {
	vars := cityRecord()
	vars["df_positionx"] = strconv.Itoa(x)
	vars["df_positiony"] = strconv.Itoa(y)
	return vars
}

// The case that was reported live: the launcher was open, df_inoutpost was
// already "0", and the clock started minutes before Start was pressed. Nothing
// but movement distinguishes a loading screen from playing.
func TestRunClockDoesNotStartWhileTheLauncherSits(t *testing.T) {
	s := newStore(nil)
	launch := time.Now().Add(-10 * time.Minute)
	s.SetGame(runningGame(launch))
	s.SetCredentialsAt(launch)

	// Polls during the launcher and the loading screen. The record already reads
	// as out of an outpost - that is exactly what made df_inoutpost useless here -
	// but the position never changes, because nothing is being played.
	for _, minute := range []int{1, 2, 3, 4} {
		at := launch.Add(time.Duration(minute) * time.Minute)
		s.ApplyTick(Tick{At: at, Vars: movedTo(1058, 1019), Scheduled: true})
	}

	v := s.Derive(launch.Add(4 * time.Minute))
	if v.HasSession {
		t.Errorf("the clock started while the launcher sat still, at %s", v.SessionTime)
	}
	if v.ClientUptime != 4*time.Minute {
		t.Errorf("ClientUptime = %s, want 4m; the process clock still works", v.ClientUptime)
	}

	// Start pressed, and the first step taken.
	moved := launch.Add(5 * time.Minute)
	s.ApplyTick(Tick{At: moved, Vars: movedTo(1058, 1018), Scheduled: true})

	v = s.Derive(moved.Add(90 * time.Second))
	if !v.HasSession {
		t.Fatal("moving is what starts the run")
	}
	if v.SessionTime != 90*time.Second {
		t.Errorf("SessionTime = %s, want 90s from the first step, not %s of client uptime",
			v.SessionTime, v.ClientUptime)
	}

	// Standing still afterwards does not stop it: you are in the city either way.
	s.ApplyTick(Tick{At: moved.Add(2 * time.Minute), Vars: movedTo(1058, 1018), Scheduled: true})
	if v := s.Derive(moved.Add(2 * time.Minute)); !v.HasSession || v.SessionTime != 2*time.Minute {
		t.Errorf("SessionTime = %s (has=%v), want the clock to keep running while stationary",
			v.SessionTime, v.HasSession)
	}
}

// Entering a building changes only the floor index, and that is still movement.
func TestRunClockStartsOnAFloorChange(t *testing.T) {
	s := newStore(nil)
	start := time.Now()
	s.SetGame(runningGame(start.Add(-time.Minute)))

	s.ApplyTick(Tick{At: start, Vars: movedTo(1058, 1016), Scheduled: true})
	inside := cityRecord()
	inside["df_positionx"], inside["df_positiony"] = "1058", "1016"
	inside["df_positionz"] = "1"
	s.ApplyTick(Tick{At: start.Add(10 * time.Second), Vars: inside, Scheduled: true})

	if v := s.Derive(start.Add(10 * time.Second)); !v.HasSession {
		t.Error("stepping inside a building is movement too")
	}
}

func TestRunClockEndsInAnOutpost(t *testing.T) {
	s := newStore(nil)
	start := time.Now().Add(-time.Hour)
	s.SetGame(runningGame(start))

	s.ApplyTick(Tick{At: start, Vars: movedTo(1058, 1016), Scheduled: true})
	s.ApplyTick(Tick{At: start.Add(10 * time.Second), Vars: movedTo(1058, 1015), Scheduled: true})
	if v := s.Derive(start.Add(time.Minute)); !v.HasSession {
		t.Fatal("the run should be running")
	}

	// Back to an outpost to bank the XP: the row disappears rather than counting
	// time spent shopping. df_inoutpost is trusted for the END of a run only,
	// where being wrong stops a clock early instead of inventing playing time.
	s.ApplyTick(Tick{At: start.Add(20 * time.Minute), Vars: realPlayerRecord(), Scheduled: true})
	if v := s.Derive(start.Add(21 * time.Minute)); v.HasSession {
		t.Errorf("HasSession = true in an outpost, SessionTime = %s", v.SessionTime)
	}

	// And a new trip out is a new run, not a resumption of the old one. Stepping
	// out of the outpost block is itself the first movement, so the clock starts
	// at that poll.
	second := start.Add(25 * time.Minute)
	s.ApplyTick(Tick{At: second, Vars: movedTo(1058, 1016), Scheduled: true})
	if v := s.Derive(second.Add(2 * time.Second)); !v.HasSession || v.SessionTime != 2*time.Second {
		t.Errorf("SessionTime = %s (has=%v), want a fresh run timed from the first step",
			v.SessionTime, v.HasSession)
	}
}

func TestRunClockClearsOnGameExitAndRelaunch(t *testing.T) {
	s := newStore(nil)
	start := time.Now().Add(-30 * time.Minute)
	s.SetGame(runningGame(start))
	s.ApplyTick(Tick{At: start, Vars: movedTo(1058, 1016), Scheduled: true})
	s.ApplyTick(Tick{At: start.Add(time.Second), Vars: movedTo(1058, 1015), Scheduled: true})
	if v := s.Derive(start.Add(time.Minute)); !v.HasSession {
		t.Fatal("the run should be running")
	}

	s.SetGame(GameState{})
	if v := s.Derive(time.Now()); v.HasSession {
		t.Error("a closed game cannot have a run in progress")
	}

	// A relaunch is a different process, so a run from the previous one must not
	// be resurrected by the next poll.
	s.SetGame(runningGame(start))
	s.ApplyTick(Tick{At: start, Vars: movedTo(1058, 1016), Scheduled: true})
	s.ApplyTick(Tick{At: start.Add(time.Second), Vars: movedTo(1058, 1015), Scheduled: true})
	s.SetGame(GameState{Running: true, PID: 9999, StartedAt: time.Now()})
	if v := s.Derive(time.Now()); v.HasSession {
		t.Error("a relaunched game starts a new run, not the old one")
	}
}

// Restarting df-hud mid-run must not reset the player's clock: that was a free
// property of reading the process start time, and it has to be paid for
// explicitly now that the run is observed rather than derived.
func TestRunClockResumesFromPersistedState(t *testing.T) {
	launch := time.Now().Add(-2 * time.Hour)
	game := runningGame(launch)
	seed := &RunState{
		StartedAt:     time.Now().Add(-40 * time.Minute),
		GamePID:       game.PID,
		GameStartedAt: game.StartedAt,
	}

	s := newStore(nil)
	s.SetRunSeed(seed)
	s.SetGame(game)

	v := s.Derive(time.Now())
	if !v.HasSession {
		t.Fatal("the persisted run should have been restored")
	}
	if v.SessionTime < 39*time.Minute || v.SessionTime > 41*time.Minute {
		t.Errorf("SessionTime = %s, want about 40m from the persisted start", v.SessionTime)
	}
}

func TestRunSeedIgnoredForADifferentGame(t *testing.T) {
	seed := &RunState{
		StartedAt:     time.Now().Add(-40 * time.Minute),
		GamePID:       4242,
		GameStartedAt: time.Now().Add(-2 * time.Hour),
	}

	// Same PID, different start time: a recycled PID, and resuming here would
	// hand the player a stranger's clock.
	s := newStore(nil)
	s.SetRunSeed(seed)
	s.SetGame(GameState{Running: true, PID: 4242, StartedAt: time.Now()})
	if v := s.Derive(time.Now()); v.HasSession {
		t.Error("a recycled pid must not resume the run")
	}

	if seed.Matches(GameState{}) {
		t.Error("a closed game matches nothing")
	}
	if (*RunState)(nil).Matches(GameState{Running: true, PID: 4242}) {
		t.Error("no persisted run means no match")
	}
}

// The XP window has to be reset by the same event that starts the clock.
// Otherwise the samples collected while the launcher sat on screen - earning
// nothing - are averaged into the start of the run, and the first minutes read as
// a fraction of the real rate.
func TestXPRunReset(t *testing.T) {
	var none time.Time
	first := time.Now()

	if !xpRunReset(none, first) {
		t.Error("the first run of a session must reset the window")
	}
	if xpRunReset(first, first) {
		t.Error("the same run must not reset the window on every poll")
	}
	if !xpRunReset(first, first.Add(time.Hour)) {
		t.Error("a second run must reset the window")
	}
	// A run ending is not a reset: the rate it earned is the last true thing the
	// widget can say, and blanking it as you step into an outpost throws the
	// number away exactly when you want to read it.
	if xpRunReset(first, none) {
		t.Error("a run ending must not reset the window")
	}
}

// df_inoutpost must not drive the window any more: it was already "0" at the
// launcher, so a change in it is not evidence about anything.
func TestXPWindowIgnoresInOutpost(t *testing.T) {
	outpost := Snapshot{At: time.Now(), InOutpost: true, CumulativeXP: 1000}
	city := Snapshot{At: outpost.At.Add(10 * time.Second), InOutpost: false, CumulativeXP: 1000}

	if reason := xpWindowReset(outpost, city, 5*time.Minute); reason != "" {
		t.Errorf("df_inoutpost changing reset the window (%q); it is not a run boundary", reason)
	}
	// The reasons that remain are the ones about the samples themselves.
	died := Snapshot{At: city.At.Add(10 * time.Second), CumulativeXP: 500}
	if reason := xpWindowReset(city, died, 5*time.Minute); reason == "" {
		t.Error("cumulative XP falling must still reset the window")
	}
	later := Snapshot{At: city.At.Add(10 * time.Second), CumulativeXP: 5000}
	if reason := xpWindowReset(city, later, 5*time.Minute); reason != "" {
		t.Errorf("a normal poll reset the window: %q", reason)
	}
}

func TestPersistedRunRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"
	game := runningGame(time.Now().Add(-time.Hour))
	started := time.Now().Add(-15 * time.Minute)

	s := newStateStore(path)
	s.Update(func(st *State) {
		st.Run = &RunState{StartedAt: started, GamePID: game.PID, GameStartedAt: game.StartedAt}
	})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	reloaded := newStateStore(path)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	run := reloaded.Get().Run
	if run == nil {
		t.Fatal("the run did not survive a save and load")
	}
	if !run.Matches(game) {
		t.Errorf("run = %+v, want it to still match the game it was recorded for", run)
	}
	if !run.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %s, want %s", run.StartedAt, started)
	}
}

// Killing things without leaving the block is still playing, and it is the case
// movement alone misses. It also insures the clock against df_positionx/y being
// updated less often than it appears to be - 1623 consecutive polls of an
// unchanged position were observed in one live session.
func TestRunClockStartsOnEarnedXP(t *testing.T) {
	s := newStore(nil)
	launch := time.Now().Add(-5 * time.Minute)
	s.SetGame(runningGame(launch))

	still := movedTo(1058, 1016)
	s.ApplyTick(Tick{At: launch, Vars: still, Scheduled: true})
	if v := s.Derive(launch); v.HasSession {
		t.Fatal("one poll is not evidence of anything")
	}

	// Same block, more XP.
	killing := movedTo(1058, 1016)
	killing["df_exptotal"] = "23480999999"
	at := launch.Add(10 * time.Second)
	s.ApplyTick(Tick{At: at, Vars: killing, Scheduled: true})

	v := s.Derive(at.Add(time.Minute))
	if !v.HasSession {
		t.Fatal("earning XP is proof of play")
	}
	if v.SessionTime != time.Minute {
		t.Errorf("SessionTime = %s, want 1m from the poll that proved it", v.SessionTime)
	}
}

// XP going DOWN, or the tier changing, is not evidence of play: the two
// cumulative tiers differ by a large constant, so a tier change would look like
// earning hundreds of thousands of XP out of nowhere.
func TestRunClockIgnoresXPGoingBackwards(t *testing.T) {
	s := newStore(nil)
	start := time.Now()
	s.SetGame(runningGame(start.Add(-time.Minute)))

	high := movedTo(1058, 1016)
	high["df_exptotal"] = "23480999999"
	s.ApplyTick(Tick{At: start, Vars: high, Scheduled: true})

	low := movedTo(1058, 1016)
	low["df_exptotal"] = "23480000000"
	s.ApplyTick(Tick{At: start.Add(10 * time.Second), Vars: low, Scheduled: true})

	if v := s.Derive(start.Add(time.Minute)); v.HasSession {
		t.Error("cumulative XP falling is a death or a correction, not a run starting")
	}
}

// The signal that actually fires at the START of a run, rather than at the first
// sign of activity within one. Both of the other two arrive late: a whole loot run
// can happen inside one block, and killing is not what you do first.
//
// It is the EDGE of df_inoutpost, never its value. The value is what started the
// clock at the launcher, where the field was already 0 and stayed 0.
func TestRunClockStartsOnLeavingAnOutpost(t *testing.T) {
	s := newStore(nil)
	launch := time.Now().Add(-5 * time.Minute)
	s.SetGame(runningGame(launch))

	// In an outpost, banking. Several polls, no clock.
	for i := 0; i < 3; i++ {
		s.ApplyTick(Tick{At: launch.Add(time.Duration(i) * 10 * time.Second),
			Vars: realPlayerRecord(), Scheduled: true})
	}
	if v := s.Derive(launch.Add(30 * time.Second)); v.HasSession {
		t.Fatal("an outpost is not a run")
	}

	// Start pressed: the server takes you out of the outpost. Same block, no XP
	// earned yet - neither of the other two signals could fire here.
	out := launch.Add(40 * time.Second)
	sameBlock := cityRecord()
	sameBlock["df_positionx"] = realPlayerRecord()["df_positionx"]
	sameBlock["df_positiony"] = realPlayerRecord()["df_positiony"]
	s.ApplyTick(Tick{At: out, Vars: sameBlock, Scheduled: true})

	v := s.Derive(out.Add(3 * time.Second))
	if !v.HasSession {
		t.Fatal("leaving the outpost is the start of the run")
	}
	if v.SessionTime != 3*time.Second {
		t.Errorf("SessionTime = %s, want 3s from the transition", v.SessionTime)
	}
}

// The launcher case, which is what the value-based version got wrong: the record
// already reads 0 and never changes, so there is no edge and nothing else moves.
func TestRunClockIgnoresAnAlreadyZeroOutpostFlag(t *testing.T) {
	s := newStore(nil)
	launch := time.Now().Add(-3 * time.Minute)
	s.SetGame(runningGame(launch))

	// Four polls of a launcher sitting there: out of an outpost as far as the
	// record is concerned, but nothing moving and no XP.
	for i := 0; i < 4; i++ {
		s.ApplyTick(Tick{At: launch.Add(time.Duration(i) * 20 * time.Second),
			Vars: movedTo(1058, 1019), Scheduled: true})
	}
	if v := s.Derive(launch.Add(80 * time.Second)); v.HasSession {
		t.Errorf("the clock started at the launcher again, at %s", v.SessionTime)
	}
}

// The calibration line is bounded: a click storm before the run starts must not
// fill the log, and the first click must not be swallowed by a zero-value clock.
func TestLogMissedClick(t *testing.T) {
	var last atomic.Int64
	now := time.Date(2026, 8, 12, 20, 0, 0, 0, time.UTC)

	if !logMissedClick(&last, now, 5*time.Second) {
		t.Fatal("the first click must print; a zero last-time means nothing has been said yet")
	}
	if logMissedClick(&last, now.Add(4*time.Second), 5*time.Second) {
		t.Error("a click inside the interval must be dropped")
	}
	if !logMissedClick(&last, now.Add(5*time.Second), 5*time.Second) {
		t.Error("a click at the interval must print again")
	}
}
