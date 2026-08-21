package main

import (
	"testing"
	"time"

	"df-hud/internal/model"
	"df-hud/internal/state"
	"df-hud/internal/store"
)

func TestRunTransitionsPersistWithoutWaitingForPoll(t *testing.T) {
	a := &app{store: store.New(nil), state: state.NewStore("")}
	a.store.SetOnRunChange(a.persistRun)

	game := model.GameState{
		Running: true, PID: 42, StartedAt: time.Unix(9000, 0),
	}
	a.store.SetGame(game)
	started := time.Unix(10000, 0)
	a.store.SetPresence(model.PresenceState{
		At: started, HasPosition: true, X: 1054, Y: 986,
	})
	run := a.state.Get().Run
	if run == nil || !run.StartedAt.Equal(started) || !run.Matches(game) {
		t.Fatalf("presence start was not persisted immediately: %+v", run)
	}

	a.store.SetPresence(model.PresenceState{
		At: started.Add(time.Minute), InOutpost: true, OutpostName: "Secronom Bunker",
	})
	if run := a.state.Get().Run; run == nil || !run.StartedAt.Equal(started) {
		t.Fatalf("outpost block changed persisted run: %+v", run)
	}
	a.store.SetGame(model.GameState{})
	if run := a.state.Get().Run; run != nil {
		t.Fatalf("process close left persisted run %+v", run)
	}
}
