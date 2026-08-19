package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"
)

// The four forms captured live on 2026-08-16, plus the ones that must NOT be
// mistaken for a position.
func TestParsePresenceDetails(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, tc := range []struct {
		details string
		want    PresenceState
	}{
		{"Inner City 1054 x 986", PresenceState{HasPosition: true, X: 1054, Y: 986, Place: "Inner City"}},
		{"Inner City 1055 x 985", PresenceState{HasPosition: true, X: 1055, Y: 985, Place: "Inner City"}},
		// Inside a building: same block, labelled with the building.
		{"Hospital 1058 x 1016", PresenceState{HasPosition: true, X: 1058, Y: 1016, Place: "Hospital", Indoors: true}},
		{"Secronom Bunker", PresenceState{InOutpost: true, OutpostName: "Secronom Bunker"}},
		{"Nastya's Holdout", PresenceState{InOutpost: true, OutpostName: "Nastya's Holdout"}},
		{"Loading...", PresenceState{Loading: true}},
		{"", PresenceState{}},
		// Not an outpost we know, so it is reported as unparsed rather than
		// filed as one - the alternative is inventing an outpost from a string
		// the game changed the format of.
		{"Somewhere New", PresenceState{}},
		{"Inner City 1054", PresenceState{}},
		{"Inner City abc x def", PresenceState{}},
	} {
		got := parsePresenceDetails(tc.details, now)
		want := tc.want
		want.At, want.Details = now, tc.details
		if got != want {
			t.Errorf("parsePresenceDetails(%q):\n got %+v\nwant %+v", tc.details, got, want)
		}
	}
}

// The client publishes coordinates in the same space as df_positionx/y, so a
// parsed position must land on a real block rather than needing a transform.
func TestPresencePositionIsACityBlock(t *testing.T) {
	got := parsePresenceDetails("Inner City 1054 x 986", time.Unix(1000, 0))
	if !got.HasPosition {
		t.Fatal("expected a position")
	}
	if !theCity.IsBlock(got.X, got.Y) {
		t.Errorf("%d,%d is not a block in the city map - the coordinate spaces disagree", got.X, got.Y)
	}
}

func TestPresenceFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := writePresenceFrame(&buf, presenceOpFrame, map[string]string{"cmd": "SET_ACTIVITY"}); err != nil {
		t.Fatal(err)
	}
	// Little-endian opcode and length, which is the part a hand-rolled codec gets
	// wrong and no unit test above would notice.
	if op := binary.LittleEndian.Uint32(buf.Bytes()[0:4]); op != presenceOpFrame {
		t.Errorf("opcode = %d, want %d", op, presenceOpFrame)
	}
	if n := binary.LittleEndian.Uint32(buf.Bytes()[4:8]); int(n) != buf.Len()-8 {
		t.Errorf("length = %d, want %d", n, buf.Len()-8)
	}
	op, body, err := readPresenceFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if op != presenceOpFrame || string(body) != `{"cmd":"SET_ACTIVITY"}` {
		t.Errorf("round trip = %d %s", op, body)
	}
}

// A frame claiming a huge length must be refused rather than allocated.
func TestPresenceFrameRefusesAbsurdLength(t *testing.T) {
	var head [8]byte
	binary.LittleEndian.PutUint32(head[0:4], presenceOpFrame)
	binary.LittleEndian.PutUint32(head[4:8], 1<<30)
	if _, _, err := readPresenceFrame(bytes.NewReader(head[:])); err == nil {
		t.Error("expected a frame over the size limit to be refused")
	}
}

// SUBSCRIBE carries a literal null for args, which is what crashed the first
// capture script. It must be a no-op here, not a panic.
func TestPresenceHandlesNullArgs(t *testing.T) {
	p := newPresenceServer("")
	var got int
	p.SetOnState(func(PresenceState) { got++ })
	p.applyActivity(json.RawMessage(`null`))
	p.applyActivity(nil)
	p.applyActivity(json.RawMessage(`{"pid":1}`))
	if got != 0 {
		t.Errorf("onState fired %d times for frames carrying no activity", got)
	}
}

func TestPresenceAppliesActivity(t *testing.T) {
	p := newPresenceServer("")
	var got PresenceState
	p.SetOnState(func(s PresenceState) { got = s })
	p.applyActivity(json.RawMessage(`{"pid":42,"activity":{"details":"Inner City 1054 x 986","state":"Multiplayer"}}`))
	if !got.HasPosition || got.X != 1054 || got.Y != 986 {
		t.Errorf("got %+v, want position 1054,986", got)
	}
	if last, ok := p.Last(); !ok || last.X != 1054 {
		t.Errorf("Last() = %+v %v", last, ok)
	}
}

// The client's position must beat the poll's, since the poll is 15-25s stale
// while walking - that difference is the whole reason this exists.
func TestStorePrefersPresencePosition(t *testing.T) {
	s := newStore(nil)
	s.SetGame(GameState{Running: true, PID: 1, StartedAt: time.Unix(500, 0)})
	now := time.Unix(1000, 0)
	s.ApplyTick(Tick{At: now, Vars: map[string]string{
		"df_positionx": "1054", "df_positiony": "987", "df_level": "415",
	}})
	s.SetPresence(parsePresenceDetails("Inner City 1055 x 985", now))

	v := s.Derive(now)
	if v.PositionX != 1055 || v.PositionY != 985 {
		t.Errorf("position = %d,%d, want the client's 1055,985 rather than the poll's 1054,987",
			v.PositionX, v.PositionY)
	}
	if v.PositionSource != "presence" {
		t.Errorf("PositionSource = %q, want %q", v.PositionSource, "presence")
	}
}

// Silence is normal - the client only publishes on a change - but it cannot go
// on forever, or a dead socket leaves a stale block on screen.
func TestStoreFallsBackToPollWhenPresenceGoesStale(t *testing.T) {
	s := newStore(nil)
	s.SetGame(GameState{Running: true, PID: 1, StartedAt: time.Unix(500, 0)})
	at := time.Unix(1000, 0)
	s.ApplyTick(Tick{At: at, Vars: map[string]string{
		"df_positionx": "1054", "df_positiony": "987", "df_level": "415",
	}})
	s.SetPresence(parsePresenceDetails("Inner City 1055 x 985", at))

	v := s.Derive(at.Add(presenceMaxAge + time.Second))
	if v.PositionX != 1054 || v.PositionY != 987 {
		t.Errorf("position = %d,%d, want the poll's 1054,987 once the client has gone quiet",
			v.PositionX, v.PositionY)
	}
	if v.PositionSource != "" {
		t.Errorf("PositionSource = %q, want empty for a polled position", v.PositionSource)
	}
}

// The client's last word survives its own death, so a closed game must not leave
// the HUD certain of a position.
func TestStoreIgnoresPresenceWhileTheGameIsClosed(t *testing.T) {
	s := newStore(nil)
	at := time.Unix(1000, 0)
	s.SetPresence(parsePresenceDetails("Inner City 1055 x 985", at))
	s.ApplyTick(Tick{At: at, Vars: map[string]string{
		"df_positionx": "1054", "df_positiony": "987", "df_level": "415",
	}})
	if v := s.Derive(at); v.PositionX != 1054 || v.PositionY != 987 {
		t.Errorf("position = %d,%d, want the poll's while the game is not running", v.PositionX, v.PositionY)
	}
}

// The whole point: the block's events are looked up at the position the CLIENT
// reports, so the boss panel describes the block you are standing on rather than
// the one the server still thinks you are on.
func TestBlockEventsFollowThePresencePosition(t *testing.T) {
	s := newStore(nil)
	s.SetGame(GameState{Running: true, PID: 1, StartedAt: time.Unix(500, 0)})
	now := time.Unix(1000, 0)
	raw := `{
	  "0":{"event_id":"1","isoa":"0","locations":[["1055","985"]],"started":"1","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Titan","special_enemy_amount":"1","boss_num":"17",
	       "event_type":"","dfp_objectives":[],"start_time":"900","end_time":"5000"},
	  "bosshash":"abc","servertime":1000,"version":"1"}`
	m, err := parseBossMap([]byte(raw), now)
	if err != nil {
		t.Fatal(err)
	}
	s.SetBossMap(m)
	s.ApplyTick(Tick{At: now, Vars: map[string]string{
		"df_positionx": "1054", "df_positiony": "987", "df_level": "415",
	}})

	if v := s.Derive(now); len(v.BlockEvents) != 0 {
		t.Fatalf("the poll's block should be empty, got %d events", len(v.BlockEvents))
	}
	s.SetPresence(parsePresenceDetails("Inner City 1055 x 985", now))
	v := s.Derive(now)
	if len(v.BlockEvents) != 1 {
		t.Fatalf("got %d events at the client's block, want the Titan", len(v.BlockEvents))
	}
	if v.BlockEvents[0].Enemies[0] != "1 x Titan" {
		t.Errorf("event = %q", v.BlockEvents[0].Enemies[0])
	}
}

// An outpost is published by name with no coordinates, so the polled position
// stands while the fresher in-outpost fact is taken from the client.
func TestPresenceOutpostKeepsThePolledPosition(t *testing.T) {
	s := newStore(nil)
	s.SetGame(GameState{Running: true, PID: 1, StartedAt: time.Unix(500, 0)})
	now := time.Unix(1000, 0)
	s.ApplyTick(Tick{At: now, Vars: map[string]string{
		"df_positionx": "1054", "df_positiony": "987", "df_level": "415", "df_inoutpost": "0",
	}})
	s.SetPresence(parsePresenceDetails("Secronom Bunker", now))

	v := s.Derive(now)
	if !v.InOutpost {
		t.Error("InOutpost = false, want the client's word over df_inoutpost")
	}
	if v.OutpostName != "Secronom Bunker" {
		t.Errorf("OutpostName = %q", v.OutpostName)
	}
	if v.PositionX != 1054 || v.PositionY != 987 {
		t.Errorf("position = %d,%d, want the poll's kept when the client gives none",
			v.PositionX, v.PositionY)
	}
}

// "Loading..." is not a position and must not clear one.
func TestPresenceLoadingLeavesThePositionAlone(t *testing.T) {
	s := newStore(nil)
	s.SetGame(GameState{Running: true, PID: 1, StartedAt: time.Unix(500, 0)})
	now := time.Unix(1000, 0)
	s.ApplyTick(Tick{At: now, Vars: map[string]string{
		"df_positionx": "1054", "df_positiony": "987", "df_level": "415",
	}})
	s.SetPresence(parsePresenceDetails("Loading...", now))
	if v := s.Derive(now); v.PositionX != 1054 || v.PositionY != 987 {
		t.Errorf("position = %d,%d, want the poll's kept through a zone load", v.PositionX, v.PositionY)
	}
}

// Inside a building the game labels the coordinates with the building's name
// instead of "Inner City". Both were seen live; only the first was handled, so
// every minute spent looting fell back to the poll with the block sitting right
// there in the string.
func TestPresenceReadsThePositionInsideABuilding(t *testing.T) {
	for _, tc := range []struct {
		details string
		place   string
		indoors bool
	}{
		{"Inner City 1054 x 986", "Inner City", false},
		{"Hospital 1058 x 1016", "Hospital", true},
		{"Supermarket 1058 x 1016", "Supermarket", true},
		{"Fire Station 1000 x 1000", "Fire Station", true},
	} {
		got := parsePresenceDetails(tc.details, time.Unix(100, 0))
		if !got.HasPosition {
			t.Errorf("%q was not read as a position", tc.details)
			continue
		}
		if got.Place != tc.place || got.Indoors != tc.indoors {
			t.Errorf("%q gave place %q indoors %v, want %q %v",
				tc.details, got.Place, got.Indoors, tc.place, tc.indoors)
		}
		if got.InOutpost {
			t.Errorf("%q was read as an outpost", tc.details)
		}
	}
}

// A building is ON the block it stands on, so the coordinates are the same
// coordinates and the boss panel keeps working while you are inside.
func TestPresencePositionIndoorsIsTheBlockYouAreStandingOn(t *testing.T) {
	got := parsePresenceDetails("Hospital 1058 x 1016", time.Unix(100, 0))
	if got.X != 1058 || got.Y != 1016 {
		t.Fatalf("read %d,%d, want 1058,1016", got.X, got.Y)
	}
}

// The outpost names stay a closed set: a place with no coordinates is only an
// outpost if it is one of the seven, so a new form still surfaces as unparsed.
func TestPresenceStillRefusesAnUnknownPlaceWithNoCoordinates(t *testing.T) {
	got := parsePresenceDetails("Somewhere New", time.Unix(100, 0))
	if got.HasPosition || got.InOutpost || got.Loading {
		t.Fatalf("an unknown coordinate-less place was accepted: %+v", got)
	}
	out := parsePresenceDetails("Secronom Bunker", time.Unix(100, 0))
	if !out.InOutpost || out.OutpostName != "Secronom Bunker" {
		t.Fatalf("a known outpost stopped being recognised: %+v", out)
	}
}

func TestPresenceRefusesMalformedCoordinates(t *testing.T) {
	for _, bad := range []string{
		"1058 x 1016",           // no label at all
		"Hospital 1058 x",       // no y
		"Hospital x 1016",       // no x
		"Hospital 1058 by 1016", // not the separator
		"Hospital abc x 1016",   // x is not a number
		"Hospital 1058 x def",   // y is not a number
	} {
		if got := parsePresenceDetails(bad, time.Unix(100, 0)); got.HasPosition {
			t.Errorf("%q was accepted as a position (%d,%d)", bad, got.X, got.Y)
		}
	}
}

// A run persisted by a previous df-hud is restored by SetGame. Presence must not
// overwrite it with a fresh one, or restarting df-hud mid-run would throw away a
// clock that is already an hour old.
func TestPresenceDoesNotClobberARestoredRun(t *testing.T) {
	now := time.Unix(10000, 0)
	started := now.Add(-time.Hour)
	game := GameState{Running: true, PID: 1, StartedAt: now.Add(-2 * time.Hour)}

	s := newStore(nil)
	s.SetRunSeed(&RunState{StartedAt: started, GamePID: game.PID, GameStartedAt: game.StartedAt})
	// Presence can arrive before SetGame has consumed the seed.
	s.SetPresence(PresenceState{At: now, HasPosition: true, X: 1054, Y: 986})
	s.SetGame(game)

	if start, _ := s.Run(); !start.Equal(started) {
		t.Fatalf("run start is %v, want the restored %v", start, started)
	}
}

// A closed game already rejects presence outright; the clock must not move either.
func TestPresenceDoesNotStartARunWithTheGameClosed(t *testing.T) {
	s := newStore(nil)
	s.SetPresence(PresenceState{At: time.Unix(10000, 0), HasPosition: true, X: 1, Y: 2})
	if start, _ := s.Run(); !start.IsZero() {
		t.Fatalf("a closed game got a run clock: %v", start)
	}
}
