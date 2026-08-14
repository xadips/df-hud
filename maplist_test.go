package main

import (
	"sort"
	"strings"
	"testing"
	"time"
)

func mapCfg() MapWidgetConfig {
	return MapWidgetConfig{Enabled: true, Scale: 1, Opacity: 1, ShowList: true, MaxListed: 20}
}

// mark is one location of one event, as ActiveMarks would produce it.
func mark(marker string, blocks int, reachable bool, ends time.Duration, enemies ...string) CityMark {
	return CityMark{
		Marker: marker, Label: strings.Join(enemies, " + "), Enemies: enemies,
		X: 1020, Y: 1000, EndsIn: ends,
		Walk: cityWalk{Blocks: blocks}, Reachable: reachable,
	}
}

// One key per event, not per block. The feed puts the same bandit pack on a dozen
// blocks at once - 185 marks from 30 events in a live capture - and a row each meant
// the same enemies and the same countdown twelve times over, then "+173 more".
func TestMapListCollapsesAnEventToOneEntry(t *testing.T) {
	v := &View{CityMarks: []CityMark{
		mark("1", 9, true, time.Minute, "6 x Bandits"),
		mark("1", 3, true, time.Minute, "6 x Bandits"),
		mark("1", 14, true, time.Minute, "6 x Bandits"),
	}}
	rows := mapListLines(v, mapCfg())
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want one entry for one event: %#v", len(rows), rows)
	}
	if rows[0].Marker != "1" || !strings.Contains(rows[0].Text, "Bandits") {
		t.Errorf("row = %#v", rows[0])
	}
	// And it reports the block you would actually walk to, which is the nearest.
	if !strings.Contains(rows[0].Timer, "1m") {
		t.Errorf("timer = %q, want the countdown", rows[0].Timer)
	}
}

// Ordered by the walk to the nearest of an event's blocks, so the top of the list is
// what you can reach.
func TestMapListOrdersByTheWalk(t *testing.T) {
	v := &View{CityMarks: []CityMark{
		mark("a", 12, true, time.Minute, "far"),
		mark("b", 2, true, time.Minute, "near"),
		mark("c", 0, false, time.Minute, "unknown distance"),
	}}
	// The ROWS are ordered nearest first; the identifiers are not touched. They are
	// the game's own slots now (B4, N7), so renumbering them by distance would give
	// the same block a different name every time you walked.
	rows := mapListLines(v, mapCfg())
	var order []string
	for _, r := range rows {
		if r.Marker != "" {
			order = append(order, r.Marker+" "+r.Text)
		}
	}
	want := []string{"b near", "a far", "c unknown distance"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func listMarkers(rows []mapRow) []string {
	var out []string
	for _, r := range rows {
		if r.Marker != "" {
			out = append(out, r.Marker)
		}
	}
	return out
}

// Off-map events are filtered upstream in ActiveMarks (see its test), so the key only
// has to put them first when they are there at all - which is when you are standing
// in Onslaught, where they are what is in front of you.
func TestMapListPutsOnslaughtFirstWhenYouAreInIt(t *testing.T) {
	off := mark("z", 0, false, 4*time.Minute, "3 x Mega Wraith")
	off.OffMap = true
	off.X, off.Y = onslaughtCoord, onslaughtCoord
	v := &View{HasPosition: true, PositionX: onslaughtCoord, PositionY: onslaughtCoord,
		CityMarks: []CityMark{mark("a", 3, true, time.Minute, "6 x Bandits"), off}}
	rows := mapListLines(v, mapCfg())
	if len(rows) != 2 {
		t.Fatalf("rows = %#v, want both events", rows)
	}
	if rows[0].Marker != "z" || !strings.Contains(rows[0].Text, "Mega Wraith") {
		t.Errorf("first row = %#v, want the Onslaught event at the top", rows[0])
	}
}

// A single enemy type is one line. A nest is the headline plus a row each, because
// seven of them joined is 140 characters and runs off the side of the screen - taking
// the part that says what is dangerous with it.
func TestMapListSplitsANest(t *testing.T) {
	nest := mark("4", 7, true, 30*time.Second,
		"3 x Evolved Longarms", "1 x Irradiated Wraith", "1 x Mega Mother")
	v := &View{CityMarks: []CityMark{mark("1", 1, true, time.Minute, "6 x Bandits"), nest}}
	rows := mapListLines(v, mapCfg())
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 1 for the pack and 3 for the nest: %#v", len(rows), rows)
	}
	if rows[0].Marker == "" || rows[0].Sub {
		t.Errorf("the single pack should be one headline row: %#v", rows[0])
	}
	// The nest is the second entry and keeps its own identifier.
	if rows[1].Marker != "4" || rows[1].Text != "3 x Evolved Longarms" {
		t.Errorf("the nest's first type belongs on its headline: %#v", rows[1])
	}
	for _, r := range rows[2:] {
		if r.Marker != "" || !r.Sub {
			t.Errorf("the rest of the nest should be indented continuations: %#v", r)
		}
	}
}

// A mission has no enemy list, and its name is the thing worth saying.
func TestMapListNamesAMission(t *testing.T) {
	m := CityMark{Marker: "7", Label: "mission: The Clue", Kind: EventMission,
		X: 1002, Y: 1000, Walk: cityWalk{Blocks: 4}, Reachable: true}
	rows := mapListLines(&View{CityMarks: []CityMark{m}}, mapCfg())
	if len(rows) != 1 || !strings.Contains(rows[0].Text, "The Clue") {
		t.Errorf("rows = %#v", rows)
	}
}

// What is dropped is counted. A list that stops without saying so reads as "that is
// everything", which is the one thing it is not.
func TestMapListSaysWhatItDropped(t *testing.T) {
	cfg := mapCfg()
	cfg.MaxListed = 2
	v := &View{CityMarks: []CityMark{
		mark("1", 1, true, 0, "a"),
		mark("2", 2, true, 0, "b"),
		mark("3", 3, true, 0, "c"),
		mark("4", 4, true, 0, "d"),
	}}
	rows := mapListLines(v, cfg)
	last := rows[len(rows)-1]
	if last.Marker != "" || last.Text != "+2 more" {
		t.Errorf("last row = %#v, want a count of what was dropped", last)
	}
}

// When it IS shown, the row says Onslaught: with only a countdown on it, it would
// look like somewhere you could walk.
func TestMapListMarksOnslaught(t *testing.T) {
	off := mark("z", 0, false, 4*time.Minute, "3 x Mega Wraith")
	off.OffMap = true
	v := &View{HasPosition: true, PositionX: onslaughtCoord, PositionY: onslaughtCoord,
		CityMarks: []CityMark{off}}
	rows := mapListLines(v, mapCfg())
	if len(rows) == 0 || !strings.Contains(rows[0].Timer, "Onslaught") {
		t.Errorf("rows = %#v", rows)
	}
}

// The markup is where the identifier gets its chip and its colour, and everything
// from the feed has to be escaped before it reaches Pango - a malformed span makes
// GTK render an EMPTY label, so one boss named with an ampersand would blank the
// whole key.
func TestMapListMarkupEscapesAndStyles(t *testing.T) {
	v := &View{CityMarks: []CityMark{mark("1", 2, true, time.Minute, "6 x Bandits & friends")}}
	got := mapListMarkup(v, mapCfg())
	if !strings.Contains(got, "&amp;") {
		t.Errorf("the ampersand was not escaped: %q", got)
	}
	if strings.Contains(got, "& ") {
		t.Errorf("raw ampersand left in the markup: %q", got)
	}
	if !strings.Contains(got, markBandits.Color().Hex()) || !strings.Contains(got, mapMarkerInk) {
		t.Errorf("the identifier lost its chip or its ink: %q", got)
	}
}

// The chip's colour is the category's, and it is the same colour that rings the
// event's cells on the map - see it on the grid, find it in the key. Four different
// decisions, four colours: a nest is somewhere to avoid unless you came for it, a
// single boss is a fight you pick, a bandit pack is loot, a mission is not a fight.
func TestMarkCategories(t *testing.T) {
	cases := []struct {
		name string
		in   CityMark
		want markCategory
	}{
		{"a single boss", mark("1", 1, true, 0, "1 x Flaming Charred Titan"), markBoss},
		{"a nest", mark("2", 1, true, 0, "3 x Evolved Longarms", "1 x Mega Mother"), markNest},
		{"bandits", mark("3", 1, true, 0, "6 x Bandits"), markBandits},
		{"bandits, other case", mark("4", 1, true, 0, "8 x BANDIT LEADER"), markBandits},
		{"a mission", CityMark{Kind: EventMission, Label: "mission: The Clue"}, markMission},
		{"a QRF", CityMark{Kind: EventQRF, Label: "QRF Extermination Mission"}, markQRF},
		{"something unrecognised", CityMark{Kind: EventUnknown}, markOther},
		// A nest of bandits is still a nest: several types on one block is the thing
		// worth recognising before walking in, whatever they are.
		{"bandits and a boss", mark("5", 1, true, 0, "6 x Bandits", "1 x Mega Wraith"), markNest},
	}
	seen := map[string]string{}
	for _, tc := range cases {
		if got := tc.in.Category(); got != tc.want {
			t.Errorf("%s: category = %v, want %v", tc.name, got, tc.want)
		}
		// And no two categories share a colour, or the distinction is not visible.
		hex := tc.want.Color().Hex()
		if other, clash := seen[hex]; clash && other != tc.want.Color().Hex() {
			t.Errorf("%s: colour %s is used by more than one category", tc.name, hex)
		}
		seen[hex] = hex
	}
	colours := map[string]markCategory{}
	for category := range markColors {
		hex := category.Color().Hex()
		if prev, clash := colours[hex]; clash {
			t.Errorf("categories %v and %v are both %s", prev, category, hex)
		}
		colours[hex] = category
	}
}

func TestMapListEmpty(t *testing.T) {
	if rows := mapListLines(&View{}, mapCfg()); rows != nil {
		t.Errorf("rows = %#v, want nothing with no events", rows)
	}
	if got := mapListMarkup(&View{}, mapCfg()); got != "" {
		t.Errorf("markup = %q, want empty", got)
	}
}

// A radius crops the map to a square around you. Clamped into the city rather than
// hanging off the edge, so the window's SIZE never changes - the group is centred on
// the monitor, and a window that shrank near a boundary would make the whole map jump
// sideways as you walked.
func TestMapWindow(t *testing.T) {
	cfg := mapCfg()
	at := func(x, y int) *View { return &View{HasPosition: true, PositionX: x, PositionY: y} }

	// No radius is the whole city.
	if got := mapWindowFor(at(1020, 1000), cfg); got.W != theCity.Width || got.H != theCity.Height {
		t.Errorf("radius 0 = %+v, want the whole city", got)
	}

	cfg.Radius = 5
	// Well inside the map: centred on the player.
	got := mapWindowFor(at(1020, 1000), cfg)
	if got.W != 11 || got.H != 11 {
		t.Errorf("window = %+v, want 11x11 for radius 5", got)
	}
	if got.X != 1015 || got.Y != 995 {
		t.Errorf("window origin = %d,%d; want the player centred at 1015,995", got.X, got.Y)
	}
	if !got.contains(1020, 1000) || got.contains(1026, 1000) {
		t.Errorf("window %+v contains the wrong blocks", got)
	}

	// Every edge clamps, and the size is the same at all of them.
	for _, tc := range []struct{ x, y, wantX, wantY int }{
		{1000, 1000, 1000, 995},      // west edge
		{1058, 1000, 1058 - 10, 995}, // east edge
		{1020, 981, 1015, 981},       // north edge
		// 1013 rather than 1020: the southern strip is two blocks wide down there,
		// and a position that is not a block has nothing to centre on.
		{1013, 1035, 1008, 1025},                      // south edge
		{theCity.OriginX, theCity.OriginY, 1000, 981}, // the corner
	} {
		got := mapWindowFor(at(tc.x, tc.y), cfg)
		if got.W != 11 || got.H != 11 {
			t.Errorf("at %d,%d the window is %dx%d, want 11x11", tc.x, tc.y, got.W, got.H)
		}
		if got.X != tc.wantX || got.Y != tc.wantY {
			t.Errorf("at %d,%d origin = %d,%d, want %d,%d", tc.x, tc.y, got.X, got.Y, tc.wantX, tc.wantY)
		}
	}

	// A radius larger than the city is the city, not a window hanging off both sides.
	cfg.Radius = 500
	if got := mapWindowFor(at(1020, 1000), cfg); got.W != theCity.Width || got.H != theCity.Height {
		t.Errorf("an oversized radius = %+v, want the whole city", got)
	}

	// Nothing to crop around: the whole city rather than a window around nowhere.
	cfg.Radius = 5
	for _, v := range []*View{
		{},
		at(onslaughtCoord, onslaughtCoord),
		at(1013, 1020), // a gap
	} {
		if got := mapWindowFor(v, cfg); got.W != theCity.Width {
			t.Errorf("with no usable position the window = %+v, want the whole city", got)
		}
	}

	// The size is knowable without a position, which is what lets the widget ask for
	// it once at construction.
	cfg.Radius = 7
	if w, h := mapWindowSize(cfg); w != 15 || h != 15 {
		t.Errorf("mapWindowSize = %dx%d, want 15x15", w, h)
	}
}

// One scale key, moving the blocks and the key beside them together. The whole reason
// there is one and not two is that they used to drift: the map scaled up and its key
// stayed at 12pt.
func TestMapScaleSizesTheGridAndTheKeyTogether(t *testing.T) {
	cfg := mapCfg()

	// Scale 1.0 is the full 59-block city at 20px a block, with a 13pt key.
	if got := mapCellPx(cfg); got != 20 {
		t.Errorf("cell at scale 1 = %dpx, want 20", got)
	}
	if got := mapListPt(cfg); got != 13 {
		t.Errorf("key at scale 1 = %gpt, want 13", got)
	}

	// A bigger scale moves both, in step.
	cfg.Scale = 1.5
	bigCell, bigPt := mapCellPx(cfg), mapListPt(cfg)
	if bigCell != 30 || bigPt != 19.5 {
		t.Errorf("at scale 1.5: cell %dpx / key %gpt, want 30 and 19.5", bigCell, bigPt)
	}

	// So does cropping, because the scale is a budget for the window rather than a
	// size per block - which is the whole point of it. This is the one the key used
	// to miss: the grid zoomed and the writing beside it did not.
	cfg.Scale, cfg.Radius = 1, 15
	if got := mapCellPx(cfg); got != 38 {
		t.Errorf("cell at radius 15 = %dpx, want 38 (1180 over 31 blocks)", got)
	}
	if got := mapListPt(cfg); got != 24.7 {
		t.Errorf("key at radius 15 = %gpt, want 24.7, up from the uncropped 13", got)
	}

	// Both ends clamp. A tight radius divides the budget by very few blocks, and a
	// tiny scale would draw a coloured smear with unreadable markers in it.
	cfg.Radius = 1
	if got := mapCellPx(cfg); got != mapMaxCell {
		t.Errorf("cell at radius 1 = %dpx, want the %dpx ceiling", got, mapMaxCell)
	}
	cfg.Scale, cfg.Radius = 0.05, 0
	if got := mapCellPx(cfg); got != mapMinCell {
		t.Errorf("cell at scale 0.05 = %dpx, want the %dpx floor", got, mapMinCell)
	}
	// And the key has its own bounds, since a 6px cell implies 3.6pt.
	if got := mapListPt(cfg); got != 8 {
		t.Errorf("key at the floor = %gpt, want 8", got)
	}

	// font_size still pins the key's text while the map moves. 0 means "leave it to
	// the stylesheet", which is what pinning it does.
	cfg.Scale = 1
	cfg.FontSize = 14
	if got := mapListPt(cfg); got != 0 {
		t.Errorf("key with font_size set = %gpt, want 0 so the stylesheet wins", got)
	}
}

// The key describes the map that is drawn, so a cropped map gets a cropped key.
func TestMapListCropsWithTheMap(t *testing.T) {
	cfg := mapCfg()
	cfg.Radius = 5

	near := mark("1", 2, true, time.Minute, "6 x Bandits")
	near.X, near.Y = 1022, 1000
	far := mark("2", 30, true, time.Minute, "1 x Mega Wraith")
	far.X, far.Y = 1050, 1000
	v := &View{HasPosition: true, PositionX: 1020, PositionY: 1000,
		CityMarks: []CityMark{near, far}}

	rows := mapListLines(v, cfg)
	if len(rows) != 1 || !strings.Contains(rows[0].Text, "Bandits") {
		t.Errorf("cropped key = %#v, want only the event inside the window", rows)
	}
	// And without the crop, both.
	cfg.Radius = 0
	if got := listMarkers(mapListLines(v, cfg)); len(got) != 2 {
		t.Errorf("uncropped key = %v, want both events", got)
	}
}

// The identifiers, against the real capture. This is the whole scheme in one test,
// and it is against a fixture rather than hand-built events on purpose: the claim
// being made is about what the FEED contains - that boss_num ascends with difficulty
// within a type, and that a mission's slot is its outpost - so events written here to
// suit the code would prove nothing.
func TestEventMarkersFollowTheFeedsOwnSlots(t *testing.T) {
	m := loadFixtureBossMap(t)
	marks := m.ActiveMarks(fixtureNow(t, m), [2]int{1020, 1000}, nil)

	// One identifier per event, whatever it is called, at every one of its blocks.
	got := map[string]string{} // marker -> what is standing there
	for _, mk := range marks {
		if mk.OffMap {
			continue
		}
		if was, seen := got[mk.Marker]; seen && was != strings.Join(mk.Enemies, " + ") {
			t.Errorf("marker %q is on two different events: %q and %q",
				mk.Marker, was, strings.Join(mk.Enemies, " + "))
		}
		got[mk.Marker] = strings.Join(mk.Enemies, " + ")
	}

	// No event identifier is a bare letter, which is what keeps them apart from the
	// outposts: those are drawn as single letters, and Nastya's N beside a nest's N
	// would be two different things drawn the same way. A prefix plus a number is
	// always two characters, and Δ is not a letter at all.
	for marker := range got {
		if len([]rune(marker)) < 2 && marker != qrfMarker {
			t.Errorf("marker %q is a bare letter, which is what an outpost is drawn as", marker)
		}
	}

	// The bandit camps, ascending with the endgame. In this capture they sit at slots
	// 1, 2, 3, 14, 16 carrying 1, 2, 2, 4 and 6 bandits, so the numbers must come out
	// 1..5 in that order - which is what makes B4 mean the same camp as it does on
	// their map.
	for marker, want := range map[string]string{
		"B1": "1 x Bandits", "B2": "2 x Bandits", "B3": "2 x Bandits",
		"B4": "4 x Bandits", "B5": "6 x Bandits",
	} {
		if got[marker] != want {
			t.Errorf("%s = %q, want %q", marker, got[marker], want)
		}
	}
	if _, over := got["B6"]; over {
		t.Errorf("B6 exists but the capture has five camps: %v", got["B6"])
	}

	// Missions are one per outpost, numbered by the slot the game gives them rather
	// than by anything about the player. M1 is Nastya's, whose mission is at 1002,1000.
	for _, mk := range marks {
		if mk.Kind == EventMission && mk.X == 1002 && mk.Y == 1000 && mk.Marker != "M1" {
			t.Errorf("Nastya's mission is %q, want M1", mk.Marker)
		}
		if mk.Kind == EventMission && mk.X == 1047 && mk.Y == 987 && mk.Marker != "M5" {
			t.Errorf("Secronom's mission is %q, want M5", mk.Marker)
		}
	}

	// Nests are multi-type spawns, numbered the same way. The capture has eight,
	// slots 4 to 11.
	for _, want := range []string{"N1", "N8"} {
		if got[want] == "" {
			t.Errorf("%s is missing; markers were %v", want, sortedKeys(got))
		}
	}
	if _, over := got["N9"]; over {
		t.Error("N9 exists but the capture has eight nests")
	}

	// Single-type bosses take I. Seven of them here: eight in the capture, less the
	// Devil Hound, which is the daily and takes its initials instead.
	if got["I1"] == "" || got["I7"] == "" {
		t.Errorf("inner city bosses should run I1..I7, got %v", sortedKeys(got))
	}
	if _, over := got["I8"]; over {
		t.Error("I8 exists but the capture has seven plain bosses and one daily")
	}

	// Both QRFs are triangles, and numbered BECAUSE there are two of them.
	qrf := 0
	for _, mk := range marks {
		if mk.Kind == EventQRF {
			qrf++
			if !strings.HasPrefix(mk.Marker, qrfMarker) {
				t.Errorf("a QRF is marked %q, want a %s", mk.Marker, qrfMarker)
			}
			if mk.Marker == qrfMarker {
				t.Errorf("with two QRFs up, %q needs a number", mk.Marker)
			}
		}
	}
	if qrf == 0 {
		t.Error("the capture has two QRF events and neither was marked")
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Today's daily takes its own initials, and a nest that merely contains it does not.
//
// The capture is a Devil Hound day: one standing alone at 1017,1024, and one inside a
// four-type nest at 1019,1021. Those are two different things to a player - a boss you
// can kill and a nest you probably cannot - so they must not be drawn the same way.
func TestDailyBossMarkers(t *testing.T) {
	m := loadFixtureBossMap(t)
	marks := m.ActiveMarks(fixtureNow(t, m), [2]int{1020, 1000}, nil)

	var alone, inNest *CityMark
	for i, mk := range marks {
		switch {
		case mk.X == 1017 && mk.Y == 1024:
			alone = &marks[i]
		case mk.X == 1019 && mk.Y == 1021:
			inNest = &marks[i]
		}
	}
	if alone == nil || inNest == nil {
		t.Fatal("the fixture should have a lone Devil Hound and one in a nest")
	}
	if alone.Marker != "DH" {
		t.Errorf("the lone Devil Hound is %q, want DH", alone.Marker)
	}
	if !strings.HasPrefix(inNest.Marker, "N") {
		t.Errorf("the nest containing one is %q, want an N number", inNest.Marker)
	}
	// Both are today's boss, so both are ringed in the daily colour - that is what
	// says "the daily is here" before you have read anything.
	for _, mk := range []*CityMark{alone, inNest} {
		if !mk.IsDaily() || !mk.ringed() {
			t.Errorf("%q at %d,%d should be flagged as the daily", mk.Marker, mk.X, mk.Y)
		}
		if mk.markInk() != dailyColor {
			t.Errorf("%q is not in the daily colour", mk.Marker)
		}
	}

	// The daily is out of the numbering it would otherwise have taken, so the plain
	// bosses stay 1..n with no gap where it was.
	for _, mk := range marks {
		if mk.Marker == "I0" {
			t.Error("the boss numbering starts at 1")
		}
	}
}

// A name is not a substring match away from another name. "hound" would file every
// Flaming Flesh Hound as the Devil Hound, and those are a world apart to walk to.
func TestDailyMarkerDoesNotOvermatch(t *testing.T) {
	for _, enemies := range [][]string{
		{"1 x Flaming Flesh Hound"},
		{"4 x Flaming Rumblers"},
		{"6 x Bandits"},
		{"1 x Mother"},
	} {
		if got := dailyMarker(enemies); got != "" {
			t.Errorf("%v was filed as the daily %q", enemies, got)
		}
	}
	for enemies, want := range map[string]string{
		"1 x Devil Hound":         "DH",
		"1 x Charred Devil Hound": "DH",
		"2 x Volatile Leaper":     "VL",
		"1 x Behemoth":            "BH",
		"8 x Bandits":             "LB",
		"12 x Bandits":            "LB",
	} {
		if got := dailyMarker([]string{enemies}); got != want {
			t.Errorf("dailyMarker(%q) = %q, want %q", enemies, got, want)
		}
	}
	// Seven bandits is still one of the standing camps, not the legendary pack.
	if got := dailyMarker([]string{"7 x Bandits"}); got != "" {
		t.Errorf("7 x Bandits = %q, want a plain camp", got)
	}
}
