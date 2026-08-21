package bossmap

import (
	"df-hud/internal/citymap"
	"df-hud/internal/config"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testdata/bossmap.json is a real capture. It is safe to keep: the feed is public
// and carries no account data at all, which is also why this feature needs no
// credentials.
// fixtureNow is the instant the capture was taken, which is what its events'
// start and end times are relative to.
func fixtureNow(t *testing.T, m *BossMap) time.Time {
	t.Helper()
	return m.FetchedAt
}

func loadFixtureBossMap(t *testing.T) *BossMap {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "bossmap.json"))
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

	events := m.At(1058, 1016, fixtureNow(t, m))
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
	if events := m.At(1234, 1234, fixtureNow(t, m)); len(events) != 0 {
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
	events := m.At(1029, 1006, fixtureNow(t, m))
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
	for _, e := range m.At(1058, 1016, fixtureNow(t, m)) {
		if e.Onslaught {
			t.Error("an Onslaught cycle surfaced on a city block")
		}
	}
	// In Onslaught, they are exactly what you want to know.
	if events := m.At(onslaughtCoord, onslaughtCoord, fixtureNow(t, m)); len(events) == 0 {
		t.Error("expected the Onslaught cycle at 3000, 3000")
	}
}

// Onslaught shifts every five minutes and the cycles overlap, so last cycle's
// boss is routinely still standing there. Out in the city the cycle is an hour and
// the previous boss has gone, so the same information would send you nowhere.
func TestBossMapPreviousCycle(t *testing.T) {
	m := loadFixtureBossMap(t)

	past := m.AtEnded(onslaughtCoord, onslaughtCoord, fixtureNow(t, m))
	if len(past) == 0 {
		t.Fatal("the capture has an ended Onslaught cycle; it must be kept, not dropped")
	}
	now := fixtureNow(t, m)
	for _, e := range past {
		if e.ActiveAt(now) {
			t.Error("an ended event must not also be active")
		}
	}
	// And the current cycle is still separate from it.
	for _, e := range m.At(onslaughtCoord, onslaughtCoord, now) {
		if e.EndedRecentlyAt(now, bossMapPastWindow) {
			t.Error("At must return only the live cycle")
		}
	}
}

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

	at := time.Unix(350, 0)
	m, err := parseBossMap([]byte(raw), at)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Events) != 2 {
		t.Fatalf("got %d events, want both kept", len(m.Events))
	}
	if got := m.At(1000, 1000, at); len(got) != 1 || got[0].Label() != "1 x Mother" {
		t.Errorf("got %+v, want only the live event", got)
	}
	if got := m.AtEnded(1000, 1000, at); len(got) != 1 || got[0].Label() != "9 x Titan" {
		t.Errorf("got %+v, want the previous cycle available separately", got)
	}
}

// Onslaught skips slots often enough that bossMapPastWindow can span more than
// one finished cycle. Mixing an older cycle's spawns into "the previous one"
// with no way to tell them apart is worse than only ever showing the nearest,
// so AtEnded narrows to whichever event(s) ended most recently.
func TestAtEndedNarrowsToTheMostRecentCycle(t *testing.T) {
	raw := `{
	  "0":{"event_id":"older","isoa":"0","locations":[["1000","1000"]],"started":"1","ended":"1",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Bear","special_enemy_amount":"1","boss_num":"1",
	       "event_type":"","dfp_objectives":[],"start_time":"1","end_time":"100"},
	  "1":{"event_id":"newer","isoa":"0","locations":[["1000","1000"]],"started":"1","ended":"1",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Titan","special_enemy_amount":"1","boss_num":"2",
	       "event_type":"","dfp_objectives":[],"start_time":"101","end_time":"200"},
	  "bosshash":"abc","servertime":250,"version":"1"}`

	m, err := parseBossMap([]byte(raw), time.Unix(250, 0))
	if err != nil {
		t.Fatal(err)
	}
	got := m.AtEnded(1000, 1000, time.Unix(250, 0))
	if len(got) != 1 || got[0].Label() != "1 x Titan" {
		t.Errorf("got %+v, want only the more recently ended cycle", got)
	}
}

// The reverse of the above: two events ending at the SAME instant (Onslaught
// spawning several enemy types in one slot) must both survive the narrowing,
// not just the first one seen.
func TestAtEndedKeepsTheWholeGroupWhenTied(t *testing.T) {
	raw := `{
	  "0":{"event_id":"a","isoa":"0","locations":[["1000","1000"]],"started":"1","ended":"1",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Bear","special_enemy_amount":"1","boss_num":"1",
	       "event_type":"","dfp_objectives":[],"start_time":"1","end_time":"200"},
	  "1":{"event_id":"b","isoa":"0","locations":[["1000","1000"]],"started":"1","ended":"1",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Titan","special_enemy_amount":"1","boss_num":"2",
	       "event_type":"","dfp_objectives":[],"start_time":"1","end_time":"200"},
	  "bosshash":"abc","servertime":250,"version":"1"}`

	m, err := parseBossMap([]byte(raw), time.Unix(250, 0))
	if err != nil {
		t.Fatal(err)
	}
	got := m.AtEnded(1000, 1000, time.Unix(250, 0))
	if len(got) != 2 {
		t.Errorf("got %+v, want both events that ended at the same instant", got)
	}
}

// The forward-looking mirror of AtEnded: the feed carries the next cycle
// before it starts (see NextBoundary), and out of two upcoming events,
// AtUpcoming reports only the one arriving soonest.
func TestAtUpcomingReturnsTheSoonestCycle(t *testing.T) {
	raw := `{
	  "0":{"event_id":"later","isoa":"0","locations":[["1000","1000"]],"started":"0","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Bear","special_enemy_amount":"1","boss_num":"1",
	       "event_type":"","dfp_objectives":[],"start_time":"500","end_time":"800"},
	  "1":{"event_id":"sooner","isoa":"0","locations":[["1000","1000"]],"started":"0","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Titan","special_enemy_amount":"1","boss_num":"2",
	       "event_type":"","dfp_objectives":[],"start_time":"400","end_time":"700"},
	  "bosshash":"abc","servertime":100,"version":"1"}`

	m, err := parseBossMap([]byte(raw), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	got := m.AtUpcoming(1000, 1000, time.Unix(100, 0))
	if len(got) != 1 || got[0].Label() != "1 x Titan" {
		t.Errorf("got %+v, want only the soonest-starting cycle", got)
	}
	// And it must not also show up as active or ended.
	for _, e := range got {
		if e.ActiveAt(time.Unix(100, 0)) || e.EndedRecentlyAt(time.Unix(100, 0), bossMapPastWindow) {
			t.Errorf("an upcoming event must not also read as active or ended: %+v", e)
		}
	}
}

// BlockBoundary is what drives the Onslaught countdown: the current cycle's
// own end while something is up, the next one's start once it isn't - both
// scoped to one block, unlike NextBoundary which schedules polling across the
// whole map and would happily answer with a city boundary instead.
func TestBlockBoundaryIsTheNearestStartOrEnd(t *testing.T) {
	raw := `{
	  "0":{"event_id":"active","isoa":"0","locations":[["1000","1000"]],"started":"1","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Titan","special_enemy_amount":"1","boss_num":"1",
	       "event_type":"","dfp_objectives":[],"start_time":"1","end_time":"200"},
	  "bosshash":"abc","servertime":100,"version":"1"}`
	m, err := parseBossMap([]byte(raw), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if b := m.BlockBoundary(1000, 1000, time.Unix(100, 0)); !b.Equal(time.Unix(200, 0)) {
		t.Errorf("BlockBoundary = %s, want the active event's own end", b)
	}
	// A block with nothing at all: zero, not a boundary from somewhere else.
	if b := m.BlockBoundary(2000, 2000, time.Unix(100, 0)); !b.IsZero() {
		t.Errorf("BlockBoundary = %s, want zero for an empty block", b)
	}
}

// The reported bug: the countdown ticked out and restarted at 5:00, but the
// panel kept last cycle's boss as "now" for seconds afterwards.
//
// The cause was two clocks. BlockBoundary compares against the local clock
// while At/AtEnded/AtUpcoming went through the feed's servertime, which is not
// a clock at all - it is how stale their data is, measured 19s and then 53s
// behind within 14 minutes. So the rows waited out the staleness after the
// countdown had already rolled over.
//
// Pinned with a feed 53s stale, back-to-back cycles, and no give at all: one
// second before the boundary and exactly on it. Everything shifts on the same
// instant or this fails.
func TestOnslaughtCyclesShiftWhenTheCountdownRollsOver(t *testing.T) {
	raw := `{
	  "0":{"event_id":"a","isoa":"0","locations":[["3000","3000"]],"started":"1","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"3 x Mega Mother","special_enemy_amount":"3","boss_num":"1",
	       "event_type":"","dfp_objectives":[],"start_time":"1000","end_time":"1300"},
	  "1":{"event_id":"b","isoa":"0","locations":[["3000","3000"]],"started":"0","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"3 x Eldritch Horror","special_enemy_amount":"3","boss_num":"17",
	       "event_type":"","dfp_objectives":[],"start_time":"1300","end_time":"1600"},
	  "bosshash":"abc","servertime":1147,"version":"1"}`
	// fetchedAt 1200 against a servertime of 1147: the 53s measured live.
	m, err := parseBossMap([]byte(raw), time.Unix(1200, 0))
	if err != nil {
		t.Fatal(err)
	}

	cycles := func(now time.Time) (prev, cur, next string, countdown time.Duration) {
		names := func(events []CityEvent) string {
			var out []string
			for _, e := range events {
				out = append(out, e.Enemies...)
			}
			return strings.Join(out, "+")
		}
		return names(m.AtEnded(onslaughtCoord, onslaughtCoord, now)),
			names(m.At(onslaughtCoord, onslaughtCoord, now)),
			names(m.AtUpcoming(onslaughtCoord, onslaughtCoord, now)),
			m.BlockBoundary(onslaughtCoord, onslaughtCoord, now).Sub(now)
	}

	prev, cur, next, left := cycles(time.Unix(1299, 0))
	if prev != "" || cur != "3 x Mega Mother" || next != "3 x Eldritch Horror" {
		t.Errorf("a second before the boundary: prev=%q now=%q next=%q", prev, cur, next)
	}
	if left != time.Second {
		t.Errorf("countdown a second out = %s, want 1s", left)
	}

	// The instant it rolls over: the list moves along by one, and "next" empties
	// because the cycle after this one is not published until ~100s before it.
	prev, cur, next, left = cycles(time.Unix(1300, 0))
	if prev != "3 x Mega Mother" {
		t.Errorf("prev = %q, want the cycle that just ended", prev)
	}
	if cur != "3 x Eldritch Horror" {
		t.Errorf("now = %q, want what was next a second ago - the whole bug", cur)
	}
	if next != "" {
		t.Errorf("next = %q, want nothing until the feed publishes the cycle after", next)
	}
	if left != 300*time.Second {
		t.Errorf("countdown at the boundary = %s, want the new cycle's full 5 minutes", left)
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

	at := time.Unix(350, 0)
	m, err := parseBossMap([]byte(raw), at)
	if err != nil {
		t.Fatal(err)
	}
	if !m.OutpostAttack {
		t.Error("isoa=1 means every outpost is under attack, which is worth knowing anywhere")
	}
	// The attack is map-wide, so it must not be filed as an event on the block its
	// location happens to name.
	if got := m.At(1000, 1000, at); len(got) != 0 {
		t.Errorf("got %+v, want the outpost attack kept out of the block index", got)
	}
}

// A feed that changed shape must be an error rather than a silent "nothing here":
// is already in hand well before the switch.
func TestBossMapCrossesABoundaryWithoutRefetching(t *testing.T) {
	// Fetched at :59:00. One cycle ends at :00:00, the next begins there.
	fetched := time.Date(2026, 8, 12, 13, 59, 0, 0, time.UTC)
	turnover := time.Date(2026, 8, 12, 14, 0, 0, 0, time.UTC)
	raw := `{
	  "0":{"event_id":"now","isoa":"0","locations":[["1058","1016"]],"started":"1","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"6 x Bandits","special_enemy_amount":"6","boss_num":"1",
	       "event_type":"","dfp_objectives":[],
	       "start_time":"` + unixStr(turnover.Add(-time.Hour)) + `",
	       "end_time":"` + unixStr(turnover) + `"},
	  "1":{"event_id":"next","isoa":"0","locations":[["1058","1016"]],"started":"0","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"2 x Titan","special_enemy_amount":"2","boss_num":"2",
	       "event_type":"","dfp_objectives":[],
	       "start_time":"` + unixStr(turnover) + `",
	       "end_time":"` + unixStr(turnover.Add(time.Hour)) + `"},
	  "bosshash":"abc","servertime":` + unixStr(fetched) + `,"version":"1"}`

	m, err := parseBossMap([]byte(raw), fetched)
	if err != nil {
		t.Fatal(err)
	}

	// Before the changeover: this cycle only. The next one is in the map but must
	// not be presented as though it were here.
	got := m.At(1058, 1016, fetched)
	if len(got) != 1 || got[0].Label() != "6 x Bandits" {
		t.Fatalf("before the turnover: %+v", got)
	}

	// One second after, with no new fetch at all.
	got = m.At(1058, 1016, turnover.Add(time.Second))
	if len(got) != 1 || got[0].Label() != "2 x Titan" {
		t.Fatalf("after the turnover: %+v, want the new cycle from the cached map", got)
	}
	// And the one that just finished is available as the previous cycle.
	past := m.AtEnded(1058, 1016, turnover.Add(time.Second))
	if len(past) != 1 || past[0].Label() != "6 x Bandits" {
		t.Errorf("previous cycle = %+v", past)
	}
	// But not forever: an event that finished long ago is not news.
	if past := m.AtEnded(1058, 1016, turnover.Add(30*time.Minute)); len(past) != 0 {
		t.Errorf("still reporting a cycle that ended half an hour ago: %+v", past)
	}

	// The boundary is what the schedule is built from.
	if b := m.NextBoundary(fetched, false); !b.Equal(turnover) {
		t.Errorf("NextBoundary = %s, want the turnover at %s", b, turnover)
	}
	// Past it, the next boundary is the following one rather than nothing.
	if b := m.NextBoundary(turnover.Add(time.Second), false); !b.Equal(turnover.Add(time.Hour)) {
		t.Errorf("NextBoundary after the turnover = %s", b)
	}
}

// unixStr writes an instant the way the feed does: unix seconds, as a string.
func unixStr(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

// Onslaught cycles are five minutes long and always in the feed, so counting them
// unconditionally would drag the whole schedule down to five minutes for a player
// out in the city who cannot see them.
func TestBossMapNextBoundaryIgnoresOnslaughtInTheCity(t *testing.T) {
	m := loadFixtureBossMap(t)
	now := fixtureNow(t, m)

	city := m.NextBoundary(now, false)
	both := m.NextBoundary(now, true)
	if city.IsZero() || both.IsZero() {
		t.Fatalf("boundaries: city %s, both %s", city, both)
	}
	if !both.Before(city) {
		t.Errorf("with Onslaught counted the next boundary should be sooner: %s vs %s", both, city)
	}
}

func TestBossPollerNextDelayClamps(t *testing.T) {
	cfg := config.Default()
	m := loadFixtureBossMap(t)
	now := fixtureNow(t, m)

	p := NewPoller(nil, nil, func() *config.Config { return cfg }, func() bool { return false })
	// No map yet: the heartbeat, which is what catches the once-a-day random
	// spawns that no boundary predicts.
	if got := p.nextDelay(now); got < 4*time.Minute || got > 6*time.Minute {
		t.Errorf("with no map, delay = %s, want about the 5m heartbeat", got)
	}

	p.current = m
	// A boundary sooner than the heartbeat wins.
	if got := p.nextDelay(now); got > 5*time.Minute+30*time.Second {
		t.Errorf("delay = %s, want no more than the heartbeat", got)
	}
	// And the minimum interval is never breached, however close the boundary is.
	late := m.NextBoundary(now, false).Add(-time.Second)
	if got := p.nextDelay(late); got < cfg.BossMap.Interval.Duration-time.Second {
		t.Errorf("delay = %s, want at least the %s minimum", got, cfg.BossMap.Interval)
	}
}

// This is the fix for "next never shows up": reacting to NextBoundary alone
// means waking up when the active cycle ENDS, which on a back-to-back
// schedule is also when the next one STARTS - too late to ever have seen it
// as upcoming. Waking up bossMapPublishWindow before the horizon instead lands
// inside the window the feed is expected to already carry it in.
func TestBossPollerWakesEarlyForOnslaughtsPublishWindow(t *testing.T) {
	cfg := config.Default()
	cfg.Poll.Jitter = 0 // deterministic
	// Raised well clear of the wake being measured, so the heartbeat cannot be
	// what caps the delay: this test is about where the aim lands, and the
	// tighter Onslaught heartbeat is TestBossPollerUsesOnslaughtsOwnIntervals.
	cfg.BossMap.OnslaughtMaxInterval = config.Duration{Duration: 10 * time.Minute}

	// now=100, cycle ends at 450: the raw boundary (450+slack-100=355s) is
	// past that and so would not lower the delay on its own - only the
	// horizon-minus-publish-window (450-100-100=250s) should.
	raw := `{
	  "0":{"event_id":"1","isoa":"0","locations":[["3000","3000"]],"started":"1","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Titan","special_enemy_amount":"1","boss_num":"1",
	       "event_type":"","dfp_objectives":[],"start_time":"1","end_time":"450"},
	  "bosshash":"abc","servertime":100,"version":"1"}`
	m, err := parseBossMap([]byte(raw), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}

	p := NewPoller(nil, nil, func() *config.Config { return cfg }, func() bool { return true })
	p.current = m
	if got := p.nextDelay(time.Unix(100, 0)); got != 250*time.Second {
		t.Errorf("delay = %s, want 250s (the horizon minus the publish window)", got)
	}

	// Jitter on this wake may only ever push it LATER. Early is the failure
	// direction - the feed has not published yet - so a symmetric spread would
	// be spending the very margin the aim depends on. Repeated because it is a
	// claim about a random draw.
	cfg.Poll.Jitter = 0.10
	for i := 0; i < 200; i++ {
		got := p.nextDelay(time.Unix(100, 0))
		if got < 250*time.Second {
			t.Fatalf("delay = %s on draw %d, want never below the 250s aim - jitter must not pull an aimed wake earlier", got, i)
		}
		if got > 275*time.Second {
			t.Fatalf("delay = %s on draw %d, want at most 250s +10%%", got, i)
		}
	}
	cfg.Poll.Jitter = 0

	// Out in the city, the same map's Onslaught horizon must not matter at all -
	// counting it would drag every player's schedule down to Onslaught's cadence
	// just because the feed always carries its cycles.
	pCity := NewPoller(nil, nil, func() *config.Config { return cfg }, func() bool { return false })
	pCity.current = m
	if got := pCity.nextDelay(time.Unix(100, 0)); got != cfg.BossMap.MaxInterval.Duration {
		t.Errorf("delay = %s, want the plain max interval (%s), Onslaught ignored",
			got, cfg.BossMap.MaxInterval.Duration)
	}
}

// Onslaught's cycle is 300s where the city's is 3600s, so it gets its own floor
// and heartbeat: the city's five-minute heartbeat is a WHOLE Onslaught cycle and
// can miss a turnover outright.
func TestBossPollerUsesOnslaughtsOwnIntervals(t *testing.T) {
	cfg := config.Default()
	cfg.Poll.Jitter = 0
	now := time.Unix(100, 0)

	// No map at all, so nothing schedules the fetch and the heartbeat is the
	// entire answer - which is the number being asserted.
	inOnslaught := true
	p := NewPoller(nil, nil, func() *config.Config { return cfg }, func() bool { return inOnslaught })
	if got := p.nextDelay(now); got != cfg.BossMap.OnslaughtMaxInterval.Duration {
		t.Errorf("in Onslaught delay = %s, want the Onslaught heartbeat (%s)",
			got, cfg.BossMap.OnslaughtMaxInterval)
	}
	inOnslaught = false
	if got := p.nextDelay(now); got != cfg.BossMap.MaxInterval.Duration {
		t.Errorf("in the city delay = %s, want the city heartbeat (%s)",
			got, cfg.BossMap.MaxInterval)
	}

	// And the floor moves with it. A boundary two seconds out would otherwise
	// schedule a fetch two seconds out; the floor is what stops that, and in
	// Onslaught it is the tighter one.
	raw := `{
	  "0":{"event_id":"1","isoa":"0","locations":[["3000","3000"]],"started":"1","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Titan","special_enemy_amount":"1","boss_num":"1",
	       "event_type":"","dfp_objectives":[],"start_time":"1","end_time":"102"},
	  "bosshash":"abc","servertime":100,"version":"1"}`
	m, err := parseBossMap([]byte(raw), now)
	if err != nil {
		t.Fatal(err)
	}
	p.current = m
	inOnslaught = true
	if got := p.nextDelay(now); got != cfg.BossMap.OnslaughtInterval.Duration {
		t.Errorf("delay = %s, want the Onslaught floor (%s)", got, cfg.BossMap.OnslaughtInterval)
	}
}

// The floors protect somebody else's server, so a config below them is a startup
// error rather than a value quietly raised.
func TestOnslaughtIntervalRespectsTheSharedFloor(t *testing.T) {
	cfg := config.Default()
	cfg.BossMap.OnslaughtInterval = config.Duration{Duration: 15*time.Second - time.Second}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "bossmap.onslaught_interval") {
		t.Errorf("validate() = %v, want it to reject anything under the %s floor", err, 15*time.Second)
	}

	// And the floor itself is allowed - it is a floor, not a value to stay clear
	// of. Onslaught's 300s cycle is the reason it sits below dfprofiler's own
	// 30s page rate at all.
	cfg = config.Default()
	cfg.BossMap.OnslaughtInterval = config.Duration{Duration: 15 * time.Second}
	if err := cfg.Validate(); err != nil {
		t.Errorf("validate() = %v, want the floor value itself to be accepted", err)
	}

	cfg = config.Default()
	cfg.BossMap.OnslaughtMaxInterval = config.Duration{Duration: 15 * time.Second}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "onslaught_max_interval") {
		t.Errorf("validate() = %v, want it to reject a heartbeat below its own floor", err)
	}
}

// A wake has to move the deadline, or it does nothing at all: the loop re-arms a
// timer for the same instant and the poke is swallowed. That was the bug that
// stopped "arriving on a new block refetches the map" from ever happening.
func TestBossPollerWakeBringsTheNextFetchForward(t *testing.T) {
	cfg := config.Default()
	cfg.Poll.Jitter = 0
	now := time.Unix(10000, 0)

	p := NewPoller(nil, nil, func() *config.Config { return cfg }, func() bool { return false })

	// With no map yet there is nothing to be polite about: fetch now.
	if got := p.earliestFetch(now); !got.Equal(now) {
		t.Errorf("earliestFetch with no map = %s, want %s", got, now)
	}

	// With a fetch 10s ago and a 1m floor, the wake waits out the remaining 50s
	// rather than firing immediately.
	raw := `{
	  "0":{"event_id":"1","isoa":"0","locations":[["1058","1016"]],"started":"1","ended":"0",
	       "reward_cash":"0","reward_exp":"0","need_briefing":"0","title":"","briefing":"",
	       "special_enemy_type":"1 x Titan","special_enemy_amount":"1","boss_num":"17",
	       "event_type":"","dfp_objectives":[],"start_time":"1","end_time":"99999"},
	  "bosshash":"abc","servertime":9990,"version":"1"}`
	m, err := parseBossMap([]byte(raw), now.Add(-10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	p.current = m
	want := now.Add(50 * time.Second)
	if got := p.earliestFetch(now); !got.Equal(want) {
		t.Errorf("earliestFetch = %s, want %s (the interval floor still applies to a poke)", got, want)
	}

	// Once the floor has passed, a wake fetches at once.
	if got := p.earliestFetch(now.Add(2 * time.Minute)); !got.Equal(now.Add(2 * time.Minute)) {
		t.Errorf("earliestFetch past the floor = %s, want the given time", got)
	}
}

// Which way to walk when your own block is empty, which is most blocks.
//
// nearestFixture is the pair the store uses: every active event as a mark, with one
// shared walk-distance table, then the closest of them.
func nearestFixture(t *testing.T, m *BossMap, now time.Time, x, y int) (CityMark, bool) {
	t.Helper()
	if !citymap.Default().IsBlock(x, y) {
		t.Fatalf("%d,%d is a gap, so nobody can be standing there to ask", x, y)
	}
	dist := citymap.Default().WalkDistances(x, y)
	return NearestMark(m.ActiveMarks(now, [2]int{x, y}, dist))
}

func TestNearestMark(t *testing.T) {
	m := loadFixtureBossMap(t)
	now := fixtureNow(t, m)

	// The bandit pack in the capture is on 1058,1016. From two blocks up the same
	// column it should be what comes back, with the deltas pointing at it.
	//
	// 1058,1014 rather than 1058,1018, which this test used to walk from: that is a
	// gap, so it is not somewhere a player can be standing to ask the question.
	mark, ok := nearestFixture(t, m, now, 1058, 1014)
	if !ok {
		t.Fatal("expected an event somewhere on the map")
	}
	if mark.Walk.DX != 0 || mark.Walk.DY != 2 {
		// dy positive means the target is at a larger y, which is down the screen.
		t.Errorf("deltas = %d, %d; want 0, 2 towards 1058,1016", mark.Walk.DX, mark.Walk.DY)
	}
	if mark.Walk.Blocks != 2 || mark.Walk.Detour != 0 {
		t.Errorf("walk = %d blocks, detour %d; that column is unbroken",
			mark.Walk.Blocks, mark.Walk.Detour)
	}
	if !strings.Contains(mark.Label, "Bandits") {
		t.Errorf("nearest = %q, want the bandit pack two blocks up", mark.Label)
	}
	if mark.X != 1058 || mark.Y != 1016 {
		t.Errorf("mark is at %d,%d, want 1058,1016", mark.X, mark.Y)
	}

	// Something on your own block is not "nearest": the caller already has it from
	// At, and reporting it as somewhere to walk would be nonsense.
	if got, ok := nearestFixture(t, m, now, 1058, 1016); ok && got.Walk.Blocks == 0 {
		t.Error("an event on your own block must not be reported as the nearest one")
	}
}

// The nearest event is the nearest to WALK to, which is not always the one with the
// smallest coordinate difference.
func TestNearestMarkPrefersTheShorterWalk(t *testing.T) {
	m := loadFixtureBossMap(t)
	now := fixtureNow(t, m)

	from := [2]int{1013, 1024} // inside the southern strip
	dist := citymap.Default().WalkDistances(from[0], from[1])
	marks := m.ActiveMarks(now, from, dist)
	best, ok := NearestMark(marks)
	if !ok {
		t.Skip("nothing active within reach of the strip in this capture")
	}
	straight := abs(best.Walk.DX) + abs(best.Walk.DY)
	if best.Walk.Blocks < straight {
		t.Fatalf("walk of %d blocks beats the straight line of %d, which cannot happen",
			best.Walk.Blocks, straight)
	}
	// And nothing else on the map is a shorter walk than what was chosen.
	for _, m := range marks {
		if m.Reachable && m.Walk.Blocks > 0 && m.Walk.Blocks < best.Walk.Blocks {
			t.Errorf("chose %s at %d blocks, but %s is %d blocks away",
				best.Label, best.Walk.Blocks, m.Label, m.Walk.Blocks)
		}
	}
}

// Onslaught's cycles sit on 3000,3000, which is not a place you can walk to from
// the city, so out in the city they are left out of the marks entirely - they would
// be events you can do nothing about, several every cycle, and they would consume
// identifiers, leaving gaps in the letters drawn on the map. Standing in Onslaught
// they are the only events that concern you, so there they appear.
func TestActiveMarksSkipsOnslaughtUnlessYouAreInIt(t *testing.T) {
	m := loadFixtureBossMap(t)
	now := fixtureNow(t, m)

	from := [2]int{1048, 1010} // a block in the city
	for _, mark := range m.ActiveMarks(now, from, citymap.Default().WalkDistances(from[0], from[1])) {
		if mark.OffMap {
			t.Errorf("%s at %d,%d is off the map and should not be marked from the city",
				mark.Label, mark.X, mark.Y)
		}
	}

	inside := [2]int{onslaughtCoord, onslaughtCoord}
	offMap := 0
	for _, mark := range m.ActiveMarks(now, inside, nil) {
		if mark.OffMap {
			offMap++
			if mark.Reachable {
				t.Errorf("%s is off the map and cannot be walked to", mark.Label)
			}
		}
	}
	if offMap == 0 {
		t.Fatal("standing in Onslaught, its own cycles should be marked (the capture has them)")
	}
}

// And the nearest event is never one of them, which is what that filter protects:
// counted as walkable they would report the nearest boss as two thousand blocks away.
func TestNearestMarkIgnoresOnslaught(t *testing.T) {
	m := loadFixtureBossMap(t)
	now := fixtureNow(t, m)

	from := [2]int{1048, 1010}
	marks := m.ActiveMarks(now, from, citymap.Default().WalkDistances(from[0], from[1]))
	best, ok := NearestMark(marks)
	if !ok {
		t.Fatal("expected an event")
	}
	if abs(best.Walk.DX) > 100 || abs(best.Walk.DY) > 100 {
		t.Errorf("deltas = %d, %d; nothing on the city map is that far",
			best.Walk.DX, best.Walk.DY)
	}
}

// One identifier per event, the same at every block that event occupies, and no two
// events sharing one. That is what makes the marker on the map and the row in the key
// mean the same thing. Which identifier it is - B4, N7, DH - is
// TestEventMarkersFollowTheFeedsOwnSlots.
func TestActiveMarksNumberEventsInOrder(t *testing.T) {
	m := loadFixtureBossMap(t)
	now := fixtureNow(t, m)
	marks := m.ActiveMarks(now, [2]int{}, nil)
	if len(marks) == 0 {
		t.Fatal("the capture should have active events")
	}
	byMarker := map[string]string{}
	for _, mark := range marks {
		if label, seen := byMarker[mark.Marker]; seen && label != mark.Label {
			t.Errorf("marker %q is used by both %q and %q", mark.Marker, label, mark.Label)
		}
		byMarker[mark.Marker] = mark.Label
		if mark.Reachable {
			t.Error("with no distance table nothing can be reachable")
		}
	}
	// Every event got one, and none fell through to the "we do not recognise this"
	// marker - which would mean the feed grew a category this does not classify.
	for _, mark := range marks {
		if mark.Marker == "" || mark.Marker == "?" {
			t.Errorf("event %q at %d,%d has no identifier (%q)",
				mark.Label, mark.X, mark.Y, mark.Marker)
		}
	}
}
