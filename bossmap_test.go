package main

import (
	"encoding/json"
	"os"
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
		if e.EndedRecentlyAt(now) {
			t.Error("At must return only the live cycle")
		}
	}
}

// The panel always shows all three sections, in the order the cycles
// themselves happen in - prev, now, next - each carrying the class its colour
// comes from.
func TestOnslaughtPanelOrdersPrevNowNext(t *testing.T) {
	v := &View{
		HaveData: true, HasPosition: true,
		PositionX: onslaughtCoord, PositionY: onslaughtCoord,
		BlockEventsPast:     []CityEvent{{Kind: EventSpawn, Enemies: []string{"3 x Irradiated Wraith"}}},
		BlockEvents:         []CityEvent{{Kind: EventSpawn, Enemies: []string{"3 x Charred Giant Spider"}}},
		BlockEventsUpcoming: []CityEvent{{Kind: EventSpawn, Enemies: []string{"2 x Titan"}}},
	}
	rows, ok := onslaughtPanel(v)
	if !ok {
		t.Fatal("onslaughtPanel should apply while standing in Onslaught")
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %+v, want exactly prev/now/next, one line each", rows)
	}
	want := []struct{ label, content, class string }{
		{"prev", "3 x Irradiated Wraith", onslaughtPrevClass},
		{"now", "3 x Charred Giant Spider", onslaughtNowClass},
		{"next", "2 x Titan", onslaughtNextClass},
	}
	for i, w := range want {
		if rows[i].Label != w.label {
			t.Errorf("row %d label = %q, want %q", i, rows[i].Label, w.label)
		}
		if rows[i].Content != w.content {
			t.Errorf("row %d content = %q, want %q", i, rows[i].Content, w.content)
		}
		if rows[i].ContentClass != w.class {
			t.Errorf("row %d class = %q, want %q", i, rows[i].ContentClass, w.class)
		}
	}
}

// Confirmed live: an Onslaught event listing several enemy types is not a
// real multi-type nest, it is the feed bundling two different cycles' own
// single bosses into one entry, with the one listed last being current - so
// only that one is shown, not both.
func TestOnslaughtPanelKeepsOnlyTheLastBundledName(t *testing.T) {
	v := &View{
		HaveData: true, HasPosition: true,
		PositionX: onslaughtCoord, PositionY: onslaughtCoord,
		BlockEventsPast: []CityEvent{{
			Kind:    EventSpawn,
			Enemies: []string{"3 x Irradiated Giant Spider", "3 x Mega Giant Spider"},
		}},
	}
	rows, ok := onslaughtPanel(v)
	if !ok {
		t.Fatal("onslaughtPanel should apply while standing in Onslaught")
	}
	// now/next still get their own placeholder rows - only the bundling
	// within prev's own event is what collapses.
	prev := rows[0]
	if prev.Content != "3 x Mega Giant Spider" {
		t.Errorf("prev content = %q, want only the LAST-listed (more recent) name", prev.Content)
	}
}

// Every section always gets a row, even with nothing in it - an empty prev or
// next IS the answer to a question, not the absence of one.
func TestOnslaughtPanelShowsPlaceholdersWhenEmpty(t *testing.T) {
	v := &View{
		HaveData: true, HasPosition: true,
		PositionX: onslaughtCoord, PositionY: onslaughtCoord,
	}
	rows, ok := onslaughtPanel(v)
	if !ok {
		t.Fatal("onslaughtPanel should apply while standing in Onslaught")
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %+v, want a placeholder row for each of prev/now/next", rows)
	}
	for _, want := range []string{"cleared", "nothing this cycle", "not announced"} {
		found := false
		for _, r := range rows {
			if r.Content == want {
				found = true
				if r.ContentClass != onslaughtEmptyClass {
					t.Errorf("row %q has class %q, want %q", r.Content, r.ContentClass, onslaughtEmptyClass)
				}
			}
		}
		if !found {
			t.Errorf("rows = %+v, want one of them to say %q", rows, want)
		}
	}
}

// Onslaught skips slots often enough that "prev" is regularly not the slot
// that just ended, so its age is shown rather than left to be misread as when
// it started - the same reason the userscript this is ported from added it.
func TestOnslaughtPanelShowsAgeOfThePreviousCycle(t *testing.T) {
	now := time.Unix(10000, 0)
	v := &View{
		HaveData: true, HasPosition: true, Now: now,
		PositionX: onslaughtCoord, PositionY: onslaughtCoord,
		BlockEventsPast: []CityEvent{{
			Kind: EventSpawn, Enemies: []string{"1 x Titan"},
			End: now.Add(-3 * time.Minute),
		}},
	}
	rows, ok := onslaughtPanel(v)
	if !ok {
		t.Fatal("onslaughtPanel should apply while standing in Onslaught")
	}
	found := false
	for _, r := range rows {
		if r.Content == "ended 3m ago" {
			found = true
			if r.ContentClass != onslaughtEmptyClass {
				t.Errorf("age row class = %q, want %q (a fact, not a threat)", r.ContentClass, onslaughtEmptyClass)
			}
		}
	}
	if !found {
		t.Errorf("rows = %+v, want an \"ended 3m ago\" row", rows)
	}
}

// A bundled entry's window covers only the FIRST cycle in it, so the age of
// the boss actually displayed (the last one listed) is that many cycles later.
// The live capture this reproduces: start 00:15:01, end 00:20:01, two names,
// read at 00:28:51 - the second boss ran 00:20-00:25 and is 3m gone, not 8m.
func TestOnslaughtPanelAgesTheDisplayedBossNotTheBundle(t *testing.T) {
	start := time.Unix(1786828501, 0) // 00:15:01
	now := start.Add(13*time.Minute + 50*time.Second)
	v := &View{
		HaveData: true, HasPosition: true, Now: now,
		PositionX: onslaughtCoord, PositionY: onslaughtCoord,
		BlockEventsPast: []CityEvent{{
			Kind:  EventSpawn,
			Start: start,
			End:   start.Add(5 * time.Minute),
			// The one displayed is "3 x Irradiated Mother", whose own cycle
			// is 00:20-00:25.
			Enemies: []string{"3 x Mega Giant Spider", "3 x Irradiated Mother"},
		}},
	}
	rows, ok := onslaughtPanel(v)
	if !ok {
		t.Fatal("onslaughtPanel should apply while standing in Onslaught")
	}
	var ages []string
	for _, r := range rows {
		if strings.HasPrefix(r.Content, "ended ") {
			ages = append(ages, r.Content)
		}
	}
	if len(ages) != 1 || ages[0] != "ended 3m ago" {
		t.Errorf("age rows = %v, want exactly [\"ended 3m ago\"] - 8m is the bundle's own end, which is when this boss STARTED", ages)
	}
}

// A bundle whose last cycle has not finished yet has no age to report - the
// shifted end is in the future, and "ended just now" would be a false claim.
func TestOnslaughtPanelOmitsTheAgeWhenTheBundleIsStillRunning(t *testing.T) {
	start := time.Unix(1786828501, 0)
	v := &View{
		HaveData: true, HasPosition: true, Now: start.Add(7 * time.Minute),
		PositionX: onslaughtCoord, PositionY: onslaughtCoord,
		BlockEventsPast: []CityEvent{{
			Kind:    EventSpawn,
			Start:   start,
			End:     start.Add(5 * time.Minute),
			Enemies: []string{"3 x Mega Giant Spider", "3 x Irradiated Mother"},
		}},
	}
	rows, _ := onslaughtPanel(v)
	for _, r := range rows {
		if strings.HasPrefix(r.Content, "ended ") {
			t.Errorf("rows carry %q, want no age row while the displayed cycle is still running", r.Content)
		}
	}
}

// Onslaught only - a city block's own prev/next fields are always empty
// anyway (the store never fills them there), but the position guard is what
// keeps this from ever firing for one by accident.
func TestOnslaughtPanelOnlyAppliesInOnslaught(t *testing.T) {
	v := &View{
		HaveData: true, HasPosition: true, PositionX: 1058, PositionY: 1016,
		BlockEvents: []CityEvent{{Kind: EventSpawn, Enemies: []string{"6 x Bandits"}}},
	}
	if _, ok := onslaughtPanel(v); ok {
		t.Error("onslaughtPanel must not apply outside Onslaught")
	}
}

func TestOnslaughtHeaderTimer(t *testing.T) {
	text, show := onslaughtHeaderTimer(&View{HaveData: true, HasOnslaughtCountdown: true, OnslaughtCountdown: 3*time.Minute + 59*time.Second})
	if !show || text != "3:59" {
		t.Errorf("onslaughtHeaderTimer = %q, %v, want \"3:59\"", text, show)
	}
	// No countdown known (not in Onslaught, or no boundary yet): no row.
	if _, show := onslaughtHeaderTimer(&View{HaveData: true}); show {
		t.Error("no countdown must not take a row")
	}
	// No data at all means no claim either way, same as the other lines.
	if _, show := onslaughtHeaderTimer(&View{HasOnslaughtCountdown: true, OnslaughtCountdown: time.Minute}); show {
		t.Error("HaveData=false must suppress the row")
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
		if e.ActiveAt(time.Unix(100, 0)) || e.EndedRecentlyAt(time.Unix(100, 0)) {
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
// no error and no data is the one outcome with no way to diagnose it.
func TestParseBossMapRejectsAnEmptyFeed(t *testing.T) {
	if _, err := parseBossMap([]byte(`{"bosshash":"x","servertime":1,"version":"1"}`), time.Now()); err == nil {
		t.Error("an event-less response should be an error")
	}
	if _, err := parseBossMap([]byte(`not json`), time.Now()); err == nil {
		t.Error("non-JSON should be an error")
	}
}

func TestThreatLines(t *testing.T) {
	m := loadFixtureBossMap(t)
	v := &View{HaveData: true, HasPosition: true, PositionX: 1058, PositionY: 1016}
	v.BlockEvents = m.At(1058, 1016, fixtureNow(t, m))

	rows := threatLines(v)
	if len(rows) != 1 || rows[0] != "6 x Bandits" {
		t.Errorf("rows = %v, want one row naming the bandits", rows)
	}

	// An empty block says nothing at all. "nothing here" on every block would
	// train you to stop reading it.
	if rows := threatLines(&View{HaveData: true, HasPosition: true}); len(rows) != 0 {
		t.Errorf("rows = %v, want none on an empty block", rows)
	}
}

// A boss nest gets a row per type. A live one carried seven at once, which as a
// single line was about 140 characters - unreadable at a glance, and clipped at
// the block group's position, so the tail naming the worst of it was exactly the
// part that disappeared.
func TestThreatLinesOneRowPerEnemyType(t *testing.T) {
	nest := []string{
		"1 x Evolved Longarms",
		"1 x Irradiated Titan",
		"1 x Irradiated Mother",
		"1 x Irradiated Giant Spider",
		"2 x Mega Wraith",
		"1 x Charred Mother",
		"1 x Charred Giant Spider",
	}
	v := &View{
		HaveData: true, HasPosition: true,
		BlockEvents: []CityEvent{{Kind: EventSpawn, Enemies: nest}},
	}

	rows := threatLines(v)
	if len(rows) != len(nest) {
		t.Fatalf("got %d rows for %d enemy types: %v", len(rows), len(nest), rows)
	}
	for i, want := range nest {
		if rows[i] != want {
			t.Errorf("row %d = %q, want %q", i, rows[i], want)
		}
	}
	// A plain spawn is nothing but its enemies, so no row repeats them as a title.
	for _, r := range rows {
		if strings.Contains(r, " + ") {
			t.Errorf("row %q still joins types together", r)
		}
	}
}

// A mission or a QRF has a name worth its own row, and its enemies go underneath:
// a mission also carries a special_enemy_type, so those are not alternatives.
func TestThreatLinesTitleThenEnemies(t *testing.T) {
	v := &View{
		HaveData: true, HasPosition: true,
		BlockEvents: []CityEvent{{
			Kind:       EventMission,
			Title:      "Retrieve the Samples",
			Enemies:    []string{"2 x Titan"},
			Objectives: []string{"0/3 samples"},
		}},
	}

	rows := threatLines(v)
	if len(rows) != 3 {
		t.Fatalf("rows = %v, want the title, the enemies and the objectives", rows)
	}
	if !strings.Contains(rows[0], "Retrieve the Samples") {
		t.Errorf("rows = %v, want the mission named first", rows)
	}
	if rows[1] != "2 x Titan" {
		t.Errorf("rows = %v, want the mission's enemies on their own row", rows)
	}
	if !strings.Contains(rows[2], "0/3 samples") {
		t.Errorf("rows = %v, want the objectives last", rows)
	}
}

// The map-wide alarm is its own row, and this is the property that matters: it
// must not appear in, or recolour, what is standing on your own block. While they
// shared a label an outpost attack painted a bandit pack the colour of an event
// happening on the other side of the map.
func TestOutpostAttackIsItsOwnLine(t *testing.T) {
	m := loadFixtureBossMap(t)
	v := &View{HaveData: true, HasPosition: true, PositionX: 1058, PositionY: 1016, OutpostAttack: true}
	v.BlockEvents = m.At(1058, 1016, fixtureNow(t, m))

	attack, show := outpostAttackLine(v)
	if !show || !strings.Contains(attack, "OUTPOST ATTACK") {
		t.Errorf("outpostAttackLine = %q, %v", attack, show)
	}

	threats := threatLines(v)
	if len(threats) == 0 || !strings.Contains(threats[0], "6 x Bandits") {
		t.Fatalf("threatLines = %v", threats)
	}
	for _, r := range threats {
		if strings.Contains(r, "OUTPOST") {
			t.Errorf("threatLines = %v, want the attack on its own row", threats)
		}
	}

	// And with no attack the row is absent rather than empty.
	if _, show := outpostAttackLine(&View{HaveData: true}); show {
		t.Error("no attack must not take a row")
	}
	// No data at all means no claim either way.
	if _, show := outpostAttackLine(&View{OutpostAttack: true}); show {
		t.Error("without data there is nothing to report")
	}
}

// The point of deriving state from the clock: one fetch stays correct through a
// changeover, with no request at the moment it matters.
//
// This is what was observed live - the feed carries the next cycle before it
// starts (appearing around :59 for a cycle that begins at :00) and keeps the
// previous one for minutes afterwards - so the information needed to switch over
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
	cfg := defaultConfig()
	m := loadFixtureBossMap(t)
	now := fixtureNow(t, m)

	p := newBossPoller(nil, nil, func() *Config { return cfg }, func() bool { return false })
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
	cfg := defaultConfig()
	cfg.Poll.Jitter = 0 // deterministic
	// Raised well clear of the wake being measured, so the heartbeat cannot be
	// what caps the delay: this test is about where the aim lands, and the
	// tighter Onslaught heartbeat is TestBossPollerUsesOnslaughtsOwnIntervals.
	cfg.BossMap.OnslaughtMaxInterval = duration{10 * time.Minute}

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

	p := newBossPoller(nil, nil, func() *Config { return cfg }, func() bool { return true })
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
	pCity := newBossPoller(nil, nil, func() *Config { return cfg }, func() bool { return false })
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
	cfg := defaultConfig()
	cfg.Poll.Jitter = 0
	now := time.Unix(100, 0)

	// No map at all, so nothing schedules the fetch and the heartbeat is the
	// entire answer - which is the number being asserted.
	inOnslaught := true
	p := newBossPoller(nil, nil, func() *Config { return cfg }, func() bool { return inOnslaught })
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
	cfg := defaultConfig()
	cfg.BossMap.OnslaughtInterval = duration{floorBossMapInterval - time.Second}
	err := cfg.validate()
	if err == nil || !strings.Contains(err.Error(), "bossmap.onslaught_interval") {
		t.Errorf("validate() = %v, want it to reject anything under the %s floor", err, floorBossMapInterval)
	}

	// And the floor itself is allowed - it is a floor, not a value to stay clear
	// of. Onslaught's 300s cycle is the reason it sits below dfprofiler's own
	// 30s page rate at all.
	cfg = defaultConfig()
	cfg.BossMap.OnslaughtInterval = duration{floorBossMapInterval}
	if err := cfg.validate(); err != nil {
		t.Errorf("validate() = %v, want the floor value itself to be accepted", err)
	}

	cfg = defaultConfig()
	cfg.BossMap.OnslaughtMaxInterval = duration{15 * time.Second}
	err = cfg.validate()
	if err == nil || !strings.Contains(err.Error(), "onslaught_max_interval") {
		t.Errorf("validate() = %v, want it to reject a heartbeat below its own floor", err)
	}
}

// A wake has to move the deadline, or it does nothing at all: the loop re-arms a
// timer for the same instant and the poke is swallowed. That was the bug that
// stopped "arriving on a new block refetches the map" from ever happening.
func TestBossPollerWakeBringsTheNextFetchForward(t *testing.T) {
	cfg := defaultConfig()
	cfg.Poll.Jitter = 0
	now := time.Unix(10000, 0)

	p := newBossPoller(nil, nil, func() *Config { return cfg }, func() bool { return false })

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
	if !theCity.IsBlock(x, y) {
		t.Fatalf("%d,%d is a gap, so nobody can be standing there to ask", x, y)
	}
	dist := theCity.walkDistances(x, y)
	return nearestMark(m.ActiveMarks(now, [2]int{x, y}, dist))
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
	dist := theCity.walkDistances(from[0], from[1])
	marks := m.ActiveMarks(now, from, dist)
	best, ok := nearestMark(marks)
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
	for _, mark := range m.ActiveMarks(now, from, theCity.walkDistances(from[0], from[1])) {
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
	marks := m.ActiveMarks(now, from, theCity.walkDistances(from[0], from[1]))
	best, ok := nearestMark(marks)
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

func TestNearestLine(t *testing.T) {
	cfg := defaultConfig().Widget.Bosses
	v := &View{
		HaveData: true, HasPosition: true, PositionX: 1058, PositionY: 1020,
		HasNearest: true, NearestDX: -1, NearestDY: -4,
		NearestX: 1057, NearestY: 1016, NearestDistanceInBlocks: 5,
	}

	text, ok := nearestLine(v, cfg)
	if !ok {
		t.Fatal("expected a nearest row")
	}
	// Vertical first, then horizontal, then the block itself - the words for
	// glancing at and the coordinates for acting on, since the game shows yours in
	// the same form.
	if text != "nearest 4 up 1 left  1057, 1016" {
		t.Errorf("nearestLine = %q", text)
	}

	// The other quadrant.
	v.NearestDX, v.NearestDY = 3, 2
	v.NearestX, v.NearestY = 1061, 1022
	if text, _ := nearestLine(v, cfg); text != "nearest 2 down 3 right  1061, 1022" {
		t.Errorf("nearestLine = %q", text)
	}

	// Too far to be somewhere you are about to walk.
	v.NearestDistanceInBlocks = nearestReportRange + 1
	if _, ok := nearestLine(v, cfg); ok {
		t.Error("an event a long way off must not take a permanent row")
	}

	// And it is a switch.
	v.NearestDistanceInBlocks = 5
	cfg.ShowNearest = false
	if _, ok := nearestLine(v, cfg); ok {
		t.Error("show_nearest = false must silence it")
	}
}
