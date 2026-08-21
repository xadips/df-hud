package store

import (
	"df-hud/internal/bossmap"
	"df-hud/internal/presence"
	"testing"
	"time"
)

func runningStore(at time.Time) *Store {
	s := New(nil)
	s.SetGame(GameState{Running: true, PID: 1, StartedAt: at.Add(-time.Minute)})
	return s
}

func polledPosition(s *Store, at time.Time) {
	s.ApplyTick(Tick{At: at, Vars: map[string]string{
		"df_positionx": "1054", "df_positiony": "987", "df_level": "415",
	}})
}

func TestStorePrefersFreshPresencePosition(t *testing.T) {
	now := time.Unix(1000, 0)
	s := runningStore(now)
	polledPosition(s, now)
	s.SetPresence(presence.ParseDetails("Inner City 1055 x 985", now))
	view := s.Derive(now)
	if view.PositionX != 1055 || view.PositionY != 985 || view.PositionSource != "presence" {
		t.Fatalf("position = %d,%d from %q", view.PositionX, view.PositionY, view.PositionSource)
	}

	view = s.Derive(now.Add(presenceMaxAge + time.Second))
	if view.PositionX != 1054 || view.PositionY != 987 || view.PositionSource != "" {
		t.Fatalf("stale presence did not fall back to poll: %+v", view)
	}
}

func TestStoreRejectsPresenceWhileGameClosed(t *testing.T) {
	now := time.Unix(1000, 0)
	s := New(nil)
	s.SetPresence(presence.ParseDetails("Inner City 1055 x 985", now))
	polledPosition(s, now)
	view := s.Derive(now)
	if view.PositionX != 1054 || view.PositionY != 987 {
		t.Fatalf("closed game used presence: %+v", view)
	}
}

func TestBlockEventsFollowPresencePosition(t *testing.T) {
	now := time.Unix(1000, 0)
	s := runningStore(now)
	raw := `{
	  "0":{"event_id":"1","isoa":"0","locations":[["1055","985"]],"started":"1","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Titan","special_enemy_amount":"1","boss_num":"17",
	       "event_type":"","dfp_objectives":[],"start_time":"900","end_time":"5000"},
	  "bosshash":"abc","servertime":1000,"version":"1"}`
	eventMap, err := bossmap.Parse([]byte(raw), now)
	if err != nil {
		t.Fatal(err)
	}
	s.SetBossMap(eventMap)
	polledPosition(s, now)
	if events := s.Derive(now).BlockEvents; len(events) != 0 {
		t.Fatalf("polled block has %d events", len(events))
	}
	s.SetPresence(presence.ParseDetails("Inner City 1055 x 985", now))
	if events := s.Derive(now).BlockEvents; len(events) != 1 || events[0].Enemies[0] != "1 x Titan" {
		t.Fatalf("presence block events = %+v", events)
	}
}

func TestPresenceOutpostAndLoadingKeepPolledPosition(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, details := range []string{"Secronom Bunker", "Loading..."} {
		s := runningStore(now)
		polledPosition(s, now)
		s.SetPresence(presence.ParseDetails(details, now))
		view := s.Derive(now)
		if view.PositionX != 1054 || view.PositionY != 987 {
			t.Errorf("%q cleared polled position: %+v", details, view)
		}
		if details == "Secronom Bunker" && (!view.InOutpost || view.OutpostName != details) {
			t.Errorf("outpost presence not applied: %+v", view)
		}
	}
}

func TestPresenceMaintainsSessionAcrossOutpostBlocks(t *testing.T) {
	now := time.Unix(10000, 0)
	s := runningStore(now)
	s.SetPresence(PresenceState{At: now, HasPosition: true, X: 1054, Y: 986})
	if started, _ := s.Run(); !started.Equal(now) {
		t.Fatalf("presence run started at %v", started)
	}
	s.SetPresence(PresenceState{At: now.Add(time.Minute), Loading: true})
	if started, _ := s.Run(); !started.Equal(now) {
		t.Fatalf("loading changed run start to %v", started)
	}
	s.SetPresence(PresenceState{At: now.Add(2 * time.Minute), InOutpost: true, OutpostName: "Secronom Bunker"})
	if started, _ := s.Run(); !started.Equal(now) {
		t.Fatalf("outpost block changed run start to %v", started)
	}
	late := now.Add(3 * time.Minute)
	s.SetPresence(presence.ParseDetails("Supermarket 1058 x 1016", late))
	if started, _ := s.Run(); !started.Equal(now) {
		t.Fatalf("later city activity changed run start to %v", started)
	}

	// Returning to the website is the process closing, not an outpost block label.
	s.SetGame(GameState{})
	if started, _ := s.Run(); !started.IsZero() {
		t.Fatalf("closed game retained run at %v", started)
	}
	nextGame := GameState{Running: true, PID: 2, StartedAt: now.Add(4 * time.Minute)}
	s.SetGame(nextGame)
	next := now.Add(5 * time.Minute)
	s.SetPresence(presence.ParseDetails("Supermarket 1058 x 1016", next))
	if started, _ := s.Run(); !started.Equal(next) {
		t.Fatalf("next launch started at %v, want %v", started, next)
	}
}

func TestOutpostBlockStartsGameSession(t *testing.T) {
	now := time.Unix(10000, 0)
	s := runningStore(now)
	s.SetPresence(presence.ParseDetails("Secronom Bunker", now))
	if started, _ := s.Run(); !started.Equal(now) {
		t.Fatalf("outpost block started session at %v, want %v", started, now)
	}
}

func TestPresenceRunClockIgnoresConflictingPollsAndPartyRefreshes(t *testing.T) {
	now := time.Unix(10000, 0)
	s := runningStore(now)
	s.SetPresence(PresenceState{At: now, HasPosition: true, X: 1054, Y: 986})

	// The player record can lag behind the client and still claim the character
	// is in an outpost. It must not clear a clock whose boundary came from IPC,
	// even after the position presence would be considered stale for rendering.
	s.ApplyTick(Tick{
		At: now.Add(presenceMaxAge + time.Minute), Vars: realPlayerRecord(), Scheduled: true,
	})
	if started, _ := s.Run(); !started.Equal(now) {
		t.Fatalf("conflicting poll changed run start to %v, want %v", started, now)
	}

	// Multiplayer join/leave changes cause another SET_ACTIVITY with the same
	// details. Reapplying that city state is not a new run boundary.
	refresh := now.Add(presenceMaxAge + 2*time.Minute)
	s.SetPresence(PresenceState{At: refresh, HasPosition: true, X: 1054, Y: 986})
	if started, _ := s.Run(); !started.Equal(now) {
		t.Fatalf("party refresh changed run start to %v, want %v", started, now)
	}
}

func TestDeathStillEndsPresenceOwnedRun(t *testing.T) {
	now := time.Unix(10000, 0)
	s := runningStore(now)
	s.SetPresence(PresenceState{At: now, HasPosition: true, X: 1054, Y: 986})

	dead := cityRecord()
	dead["df_dead"] = "1"
	s.ApplyTick(Tick{At: now.Add(time.Minute), Vars: dead, Scheduled: true})
	if started, _ := s.Run(); !started.IsZero() {
		t.Fatalf("death left presence-owned run active at %v", started)
	}
	s.SetPresence(PresenceState{
		At: now.Add(2 * time.Minute), HasPosition: true, X: 1054, Y: 986,
	})
	if started, _ := s.Run(); !started.IsZero() {
		t.Fatalf("late activity restarted dead session at %v", started)
	}
	if s.RestartRun(now.Add(3*time.Minute), "manual correction after death") {
		t.Fatal("manual correction restarted a terminal game process")
	}
}

func TestPresenceDisconnectRestoresPollFallback(t *testing.T) {
	now := time.Unix(10000, 0)
	s := runningStore(now)
	s.SetPresence(PresenceState{At: now, Loading: true})

	s.ApplyTick(Tick{At: now.Add(time.Second), Vars: movedTo(1058, 1016), Scheduled: true})
	s.ApplyTick(Tick{At: now.Add(2 * time.Second), Vars: movedTo(1058, 1015), Scheduled: true})
	if started, _ := s.Run(); !started.IsZero() {
		t.Fatalf("poll started while connected presence said loading: %v", started)
	}

	s.SetPresenceConnected(false)
	s.ApplyTick(Tick{At: now.Add(3 * time.Second), Vars: movedTo(1058, 1014), Scheduled: true})
	if started, _ := s.Run(); !started.Equal(now.Add(3 * time.Second)) {
		t.Fatalf("poll fallback started at %v after disconnect", started)
	}
}

func TestPresenceDoesNotBleedIntoRelaunchedGame(t *testing.T) {
	now := time.Unix(10000, 0)
	s := runningStore(now)
	polledPosition(s, now)
	s.SetPresence(presence.ParseDetails("Inner City 1055 x 985", now))

	s.SetGame(GameState{Running: true, PID: 2, StartedAt: now.Add(time.Second)})
	view := s.Derive(now.Add(2 * time.Second))
	if view.HasPosition || view.PositionSource != "" {
		t.Fatalf("new game used previous process presence: %+v", view)
	}
}

func TestEffectivePositionUsesPresenceAndRejectsOutpost(t *testing.T) {
	now := time.Unix(10000, 0)
	s := runningStore(now)
	polledPosition(s, now)
	s.SetPresence(presence.ParseDetails("Inner City 3000 x 3000", now))
	if x, y, ok := s.EffectivePosition(now); !ok || x != 3000 || y != 3000 {
		t.Fatalf("effective position = %d,%d %v", x, y, ok)
	}
	s.SetPresence(presence.ParseDetails("Secronom Bunker", now.Add(time.Second)))
	if x, y, ok := s.EffectivePosition(now.Add(time.Second)); ok {
		t.Fatalf("outpost retained effective city position %d,%d", x, y)
	}
}

func TestPresenceDoesNotClobberRestoredRun(t *testing.T) {
	now := time.Unix(10000, 0)
	started := now.Add(-time.Hour)
	game := GameState{Running: true, PID: 1, StartedAt: now.Add(-2 * time.Hour)}
	s := New(nil)
	s.SetRunSeed(&RunState{StartedAt: started, GamePID: game.PID, GameStartedAt: game.StartedAt})
	s.SetPresence(PresenceState{At: now, HasPosition: true, X: 1054, Y: 986})
	s.SetGame(game)
	if got, _ := s.Run(); !got.Equal(started) {
		t.Fatalf("run start = %v, want restored %v", got, started)
	}
}

func TestPresenceCannotStartRunWithGameClosed(t *testing.T) {
	s := New(nil)
	s.SetPresence(PresenceState{At: time.Unix(10000, 0), HasPosition: true, X: 1, Y: 2})
	if started, _ := s.Run(); !started.IsZero() {
		t.Fatalf("closed game got run %v", started)
	}
}
