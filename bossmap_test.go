package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// testdata/bossmap.json is a real capture. It is safe to keep: the feed is public
// and carries no account data at all, which is also why this feature needs no
// credentials.
func loadFixtureBossMap(t *testing.T) *BossMap {
	t.Helper()
	data, err := os.ReadFile("testdata/bossmap.json")
	if err != nil {
		t.Fatal(err)
	}
	// Parsed at the fixture's own server time, so "active" means what it meant
	// when the capture was taken rather than drifting as the file ages.
	m, err := parseBossMap(data, fixtureBossMapTime(t, data))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func fixtureBossMapTime(t *testing.T, data []byte) time.Time {
	t.Helper()
	var top struct {
		ServerTime int64 `json:"servertime"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	return time.Unix(top.ServerTime, 0)
}

func TestParseBossMapFixture(t *testing.T) {
	m := loadFixtureBossMap(t)

	if len(m.Events) == 0 {
		t.Fatal("no events parsed")
	}
	if m.Hash == "" {
		t.Error("bosshash should be kept: it is the feed's own change marker")
	}
	if m.ServerTime.IsZero() {
		t.Error("servertime should be kept: it is the clock that decides what is active")
	}
	// The three top-level scalars sit at the same level as the events and must not
	// become events themselves.
	for _, e := range m.Events {
		if e.ID == "" {
			t.Errorf("an event with no id got through: %+v", e)
		}
	}
}

// The block the player was actually standing on when this was captured, which is
// the whole point of the feature: the game's own client does not tell you there
// are six bandits there until you can see them.
func TestBossMapAtTheBlockTheFeedSaysBandits(t *testing.T) {
	m := loadFixtureBossMap(t)

	events := m.At(1058, 1016)
	if len(events) == 0 {
		t.Fatal("expected an event on 1058, 1016")
	}
	var found bool
	for _, e := range events {
		if e.Kind == EventSpawn && strings.Contains(e.Label(), "Bandits") {
			found = true
			if e.Label() != "6 x Bandits" {
				t.Errorf("label = %q, want the feed's own wording", e.Label())
			}
		}
	}
	if !found {
		for _, e := range events {
			t.Logf("got %s: %q", e.Kind, e.Label())
		}
		t.Error("expected the bandit pack on this block")
	}

	// A block with nothing on it is the normal case and must come back empty
	// rather than as an error or a placeholder.
	if events := m.At(1234, 1234); len(events) != 0 {
		t.Errorf("expected nothing on an empty block, got %+v", events)
	}
}

// A mission also carries a special_enemy_type, so classifying on that first
// would file every mission as a plain spawn. This is the branch order from
// bossmap.js.
func TestBossMapClassifiesMissions(t *testing.T) {
	m := loadFixtureBossMap(t)

	var mission, qrf, spawn int
	for _, e := range m.Events {
		switch e.Kind {
		case EventMission:
			mission++
			if e.Title == "" {
				t.Error("a mission should carry its title")
			}
		case EventQRF:
			qrf++
		case EventSpawn:
			spawn++
		}
	}
	if mission == 0 || qrf == 0 || spawn == 0 {
		t.Errorf("kinds: %d missions, %d qrf, %d spawns; the capture has all three",
			mission, qrf, spawn)
	}

	// "Red Inferno" is a mission with three flaming titans and a kill objective.
	events := m.At(1029, 1006)
	if len(events) == 0 {
		t.Fatal("expected the mission at 1029, 1006")
	}
	e := events[0]
	if e.Kind != EventMission {
		t.Errorf("kind = %s, want mission (it has a briefing)", e.Kind)
	}
	if e.Label() != "mission: Red Inferno" {
		t.Errorf("label = %q", e.Label())
	}
	if len(e.Objectives) == 0 || !strings.Contains(e.Objectives[0], "Flaming Titans") {
		t.Errorf("objectives = %v, want the mission's task list", e.Objectives)
	}
	if !strings.Contains(e.Objectives[0], ": 3") {
		t.Errorf("objectives = %v, want the amount, formatted as their page formats it", e.Objectives)
	}
}

// Several enemy types on one block arrive as one <br />-joined string.
func TestSplitEnemyTypes(t *testing.T) {
	got := splitEnemyTypes("2 x Flaming Zombie<br />1 x Riot Shield Guy")
	if len(got) != 2 || got[0] != "2 x Flaming Zombie" || got[1] != "1 x Riot Shield Guy" {
		t.Errorf("got %q", got)
	}
	// "0" is the feed's way of saying there is no special enemy, and it must not
	// become an enemy called "0".
	if got := splitEnemyTypes("0"); got != nil {
		t.Errorf("got %q, want nothing", got)
	}
	if got := splitEnemyTypes(""); got != nil {
		t.Errorf("got %q, want nothing", got)
	}
}

// Onslaught events sit on 3000,3000, which is where the player record puts you
// when you are IN Onslaught - so they are indexed like any other block and
// surface exactly when you are there.
func TestBossMapOnslaught(t *testing.T) {
	m := loadFixtureBossMap(t)

	var onslaught int
	for _, e := range m.Events {
		if e.Onslaught {
			onslaught++
		}
	}
	if onslaught == 0 {
		t.Fatal("the capture contains Onslaught cycles; they must be kept")
	}
	// Out in the city you must never be told about them.
	for _, e := range m.At(1058, 1016) {
		if e.Onslaught {
			t.Error("an Onslaught cycle surfaced on a city block")
		}
	}
	// In Onslaught, they are exactly what you want to know.
	if events := m.At(onslaughtCoord, onslaughtCoord); len(events) == 0 {
		t.Error("expected the Onslaught cycle at 3000, 3000")
	}
}

// Onslaught shifts every five minutes and the cycles overlap, so last cycle's
// boss is routinely still standing there. Out in the city the cycle is an hour and
// the previous boss has gone, so the same information would send you nowhere.
func TestBossMapPreviousCycle(t *testing.T) {
	m := loadFixtureBossMap(t)

	past := m.AtEnded(onslaughtCoord, onslaughtCoord)
	if len(past) == 0 {
		t.Fatal("the capture has an ended Onslaught cycle; it must be kept, not dropped")
	}
	for _, e := range past {
		if e.Active {
			t.Error("an ended event must not also be active")
		}
	}
	// And the current cycle is still separate from it.
	for _, e := range m.At(onslaughtCoord, onslaughtCoord) {
		if e.Ended {
			t.Error("At must return only the live cycle")
		}
	}
}

func TestThreatLineMarksThePreviousCycle(t *testing.T) {
	v := &View{
		HaveData: true, HasPosition: true,
		PositionX: onslaughtCoord, PositionY: onslaughtCoord,
		BlockEvents:     []CityEvent{{Kind: EventSpawn, Enemies: []string{"3 x Charred Giant Spider"}, Active: true}},
		BlockEventsPast: []CityEvent{{Kind: EventSpawn, Enemies: []string{"3 x Irradiated Wraith"}, Ended: true}},
	}
	text, _, show := threatLine(v)
	if !show {
		t.Fatal("expected a threat line")
	}
	if !strings.Contains(text, "3 x Charred Giant Spider") {
		t.Errorf("threatLine = %q, want the current cycle", text)
	}
	// Prefixed, because "might still be there" is a different claim from "is".
	if !strings.Contains(text, "last: 3 x Irradiated Wraith") {
		t.Errorf("threatLine = %q, want the previous cycle marked as such", text)
	}
}

// Last cycle's events are kept, but never reported as being on your block: their
// own page hides them by default, and out in the city that boss has gone.
func TestParseBossMapKeepsEndedEventsOutOfAt(t *testing.T) {
	raw := `{
	  "0":{"event_id":"1","isoa":"0","locations":[["1000","1000"]],"started":"1","ended":"1",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"9 x Titan","special_enemy_amount":"9","boss_num":"1",
	       "event_type":"","dfp_objectives":[],"start_time":"100","end_time":"200"},
	  "1":{"event_id":"2","isoa":"0","locations":[["1000","1000"]],"started":"1","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Mother","special_enemy_amount":"1","boss_num":"2",
	       "event_type":"","dfp_objectives":[],"start_time":"300","end_time":"400"},
	  "bosshash":"abc","servertime":350,"version":"1"}`

	m, err := parseBossMap([]byte(raw), time.Unix(350, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Events) != 2 {
		t.Fatalf("got %d events, want both kept", len(m.Events))
	}
	if got := m.At(1000, 1000); len(got) != 1 || got[0].Label() != "1 x Mother" {
		t.Errorf("got %+v, want only the live event", got)
	}
	if got := m.AtEnded(1000, 1000); len(got) != 1 || got[0].Label() != "9 x Titan" {
		t.Errorf("got %+v, want the previous cycle available separately", got)
	}
}

func TestParseBossMapOutpostAttack(t *testing.T) {
	raw := `{
	  "0":{"event_id":"1","isoa":"1","locations":[["1000","1000"]],"started":"1","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"0","special_enemy_amount":"0","boss_num":"0",
	       "event_type":"","dfp_objectives":[],"start_time":"100","end_time":"900"},
	  "1":{"event_id":"2","isoa":"0","locations":[["1005","1005"]],"started":"1","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"3 x Bandits","special_enemy_amount":"3","boss_num":"1",
	       "event_type":"","dfp_objectives":[],"start_time":"100","end_time":"900"},
	  "bosshash":"abc","servertime":350,"version":"1"}`

	m, err := parseBossMap([]byte(raw), time.Unix(350, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !m.OutpostAttack {
		t.Error("isoa=1 means every outpost is under attack, which is worth knowing anywhere")
	}
	// The attack is map-wide, so it must not be filed as an event on the block its
	// location happens to name.
	if got := m.At(1000, 1000); len(got) != 0 {
		t.Errorf("got %+v, want the outpost attack kept out of the block index", got)
	}
}

// A feed that changed shape must be an error rather than a silent "nothing here":
// no error and no data is the one outcome with no way to diagnose it.
func TestParseBossMapRejectsAnEmptyFeed(t *testing.T) {
	if _, err := parseBossMap([]byte(`{"bosshash":"x","servertime":1,"version":"1"}`), time.Now()); err == nil {
		t.Error("an event-less response should be an error")
	}
	if _, err := parseBossMap([]byte(`not json`), time.Now()); err == nil {
		t.Error("non-JSON should be an error")
	}
}

func TestThreatLine(t *testing.T) {
	m := loadFixtureBossMap(t)
	v := &View{HaveData: true, HasPosition: true, PositionX: 1058, PositionY: 1016}
	v.BlockEvents = m.At(1058, 1016)

	text, urgent, show := threatLine(v)
	if !show || !strings.Contains(text, "6 x Bandits") {
		t.Errorf("threatLine = %q, %v", text, show)
	}
	if urgent {
		t.Error("a bandit pack is a warning, not the map-wide alarm")
	}

	// An empty block says nothing at all. "nothing here" on every block would
	// train you to stop reading the line.
	if _, _, show := threatLine(&View{HaveData: true, HasPosition: true}); show {
		t.Error("an empty block must not take a row")
	}

	// The map-wide alarm shows wherever you are standing, and gets the loud colour.
	attack := &View{HaveData: true, OutpostAttack: true}
	text, urgent, show = threatLine(attack)
	if !show || !urgent || !strings.Contains(text, "OUTPOST ATTACK") {
		t.Errorf("threatLine = %q, urgent=%v", text, urgent)
	}
}
