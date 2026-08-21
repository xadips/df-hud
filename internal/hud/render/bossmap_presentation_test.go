package render

import (
	"strings"
	"testing"
	"time"
)

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
