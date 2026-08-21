package store

import (
	"strconv"
	"testing"
	"time"
)

func cityRecord() map[string]string {
	vars := realPlayerRecord()
	vars["df_inoutpost"] = "0"
	vars["df_tradezone"] = "5"
	return vars
}

func runningGame(started time.Time) GameState {
	return GameState{Running: true, PID: 4242, StartedAt: started}
}

func movedTo(x, y int) map[string]string {
	vars := cityRecord()
	vars["df_positionx"] = strconv.Itoa(x)
	vars["df_positiony"] = strconv.Itoa(y)
	return vars
}

func TestRunClockDoesNotStartWhileLauncherSits(t *testing.T) {
	s := New(nil)
	launch := time.Now().Add(-10 * time.Minute)
	s.SetGame(runningGame(launch))
	for _, minute := range []int{1, 2, 3, 4} {
		at := launch.Add(time.Duration(minute) * time.Minute)
		s.ApplyTick(Tick{At: at, Vars: movedTo(1058, 1019), Scheduled: true})
	}
	if view := s.Derive(launch.Add(4 * time.Minute)); view.HasSession {
		t.Fatalf("clock started while launcher sat still: %s", view.SessionTime)
	}

	moved := launch.Add(5 * time.Minute)
	s.ApplyTick(Tick{At: moved, Vars: movedTo(1058, 1018), Scheduled: true})
	view := s.Derive(moved.Add(90 * time.Second))
	if !view.HasSession || view.SessionTime != 90*time.Second {
		t.Fatalf("session = %s (has=%v), want 90s from first movement", view.SessionTime, view.HasSession)
	}
}

func TestStartRunIfIdleDoesNotReplaceExistingEvidence(t *testing.T) {
	s := New(nil)
	if s.StartRunIfIdle(time.Now(), "no game") {
		t.Fatal("closed game started a run")
	}
	s.SetGame(runningGame(time.Now().Add(-time.Minute)))
	first := time.Now()
	if !s.StartRunIfIdle(first, "foreground game window") {
		t.Fatal("foreground game should start a run")
	}
	if s.StartRunIfIdle(first.Add(time.Minute), "duplicate poll") {
		t.Fatal("later evidence replaced the run")
	}
	if started, _ := s.Run(); !started.Equal(first) {
		t.Fatalf("run start = %s, want %s", started, first)
	}
}

func TestRunClockStartsOnFloorChange(t *testing.T) {
	s := New(nil)
	start := time.Now()
	s.SetGame(runningGame(start.Add(-time.Minute)))
	s.ApplyTick(Tick{At: start, Vars: movedTo(1058, 1016), Scheduled: true})
	inside := cityRecord()
	inside["df_positionx"], inside["df_positiony"], inside["df_positionz"] = "1058", "1016", "1"
	s.ApplyTick(Tick{At: start.Add(10 * time.Second), Vars: inside, Scheduled: true})
	if !s.Derive(start.Add(10 * time.Second)).HasSession {
		t.Error("floor movement did not start the run")
	}
}

func TestRunClockEndsInOutpostAndRestarts(t *testing.T) {
	s := New(nil)
	start := time.Now().Add(-time.Hour)
	s.SetGame(runningGame(start))
	s.ApplyTick(Tick{At: start, Vars: movedTo(1058, 1016), Scheduled: true})
	s.ApplyTick(Tick{At: start.Add(10 * time.Second), Vars: movedTo(1058, 1015), Scheduled: true})
	s.ApplyTick(Tick{At: start.Add(20 * time.Minute), Vars: realPlayerRecord(), Scheduled: true})
	if s.Derive(start.Add(21 * time.Minute)).HasSession {
		t.Fatal("outpost left a run active")
	}
	second := start.Add(25 * time.Minute)
	s.ApplyTick(Tick{At: second, Vars: movedTo(1058, 1016), Scheduled: true})
	if view := s.Derive(second.Add(2 * time.Second)); !view.HasSession || view.SessionTime != 2*time.Second {
		t.Fatalf("new run = %s (has=%v), want 2s", view.SessionTime, view.HasSession)
	}
}

func TestRunClockClearsOnExitAndRelaunch(t *testing.T) {
	s := New(nil)
	start := time.Now().Add(-30 * time.Minute)
	s.SetGame(runningGame(start))
	s.ApplyTick(Tick{At: start, Vars: movedTo(1058, 1016), Scheduled: true})
	s.ApplyTick(Tick{At: start.Add(time.Second), Vars: movedTo(1058, 1015), Scheduled: true})
	s.SetGame(GameState{})
	if s.Derive(time.Now()).HasSession {
		t.Fatal("closed game retained a run")
	}

	s.SetGame(runningGame(start))
	s.ApplyTick(Tick{At: start, Vars: movedTo(1058, 1016), Scheduled: true})
	s.ApplyTick(Tick{At: start.Add(time.Second), Vars: movedTo(1058, 1015), Scheduled: true})
	s.SetGame(GameState{Running: true, PID: 9999, StartedAt: time.Now()})
	if s.Derive(time.Now()).HasSession {
		t.Fatal("relaunched game retained the old run")
	}
}

func TestRunSeedMatchesOnlySameGame(t *testing.T) {
	launch := time.Now().Add(-2 * time.Hour)
	game := runningGame(launch)
	seed := &RunState{
		StartedAt:     time.Now().Add(-40 * time.Minute),
		GamePID:       game.PID,
		GameStartedAt: game.StartedAt,
	}
	s := New(nil)
	s.SetRunSeed(seed)
	s.SetGame(game)
	if view := s.Derive(time.Now()); !view.HasSession || view.SessionTime < 39*time.Minute {
		t.Fatalf("persisted run was not restored: %+v", view)
	}

	other := New(nil)
	other.SetRunSeed(seed)
	other.SetGame(GameState{Running: true, PID: game.PID, StartedAt: time.Now()})
	if other.Derive(time.Now()).HasSession {
		t.Fatal("recycled pid restored another game's run")
	}
	if seed.Matches(GameState{}) || (*RunState)(nil).Matches(game) {
		t.Fatal("invalid run seed matched")
	}
}

func TestRunClockIgnoresXPGoingBackwards(t *testing.T) {
	s := New(nil)
	start := time.Now()
	s.SetGame(runningGame(start.Add(-time.Minute)))
	high := movedTo(1058, 1016)
	high["df_exptotal"] = "23480999999"
	s.ApplyTick(Tick{At: start, Vars: high, Scheduled: true})
	low := movedTo(1058, 1016)
	low["df_exptotal"] = "23480000000"
	s.ApplyTick(Tick{At: start.Add(10 * time.Second), Vars: low, Scheduled: true})
	if s.Derive(start.Add(time.Minute)).HasSession {
		t.Fatal("falling XP started a run")
	}
}

func TestRunClockStartsOnlyOnLeavingOutpostEdge(t *testing.T) {
	s := New(nil)
	launch := time.Now().Add(-5 * time.Minute)
	s.SetGame(runningGame(launch))
	for i := range 3 {
		s.ApplyTick(Tick{
			At: launch.Add(time.Duration(i) * 10 * time.Second), Vars: realPlayerRecord(), Scheduled: true,
		})
	}
	out := launch.Add(40 * time.Second)
	sameBlock := cityRecord()
	sameBlock["df_positionx"] = realPlayerRecord()["df_positionx"]
	sameBlock["df_positiony"] = realPlayerRecord()["df_positiony"]
	s.ApplyTick(Tick{At: out, Vars: sameBlock, Scheduled: true})
	if view := s.Derive(out.Add(3 * time.Second)); !view.HasSession || view.SessionTime != 3*time.Second {
		t.Fatalf("outpost edge session = %s (has=%v), want 3s", view.SessionTime, view.HasSession)
	}

	launcher := New(nil)
	launcher.SetGame(runningGame(launch))
	for i := range 4 {
		launcher.ApplyTick(Tick{
			At: launch.Add(time.Duration(i) * 20 * time.Second), Vars: movedTo(1058, 1019), Scheduled: true,
		})
	}
	if launcher.Derive(launch.Add(80 * time.Second)).HasSession {
		t.Fatal("already-zero outpost flag started a launcher run")
	}
}
