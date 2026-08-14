package main

import (
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
	// Identifiers are assigned to the visible set in this order, so the nearest thing
	// is always 1 - the letters the feed happened to hand out are not what is drawn.
	rows := mapListLines(v, mapCfg())
	var order []string
	for _, r := range rows {
		if r.Marker != "" {
			order = append(order, r.Marker+" "+r.Text)
		}
	}
	want := []string{"1 near", "2 far", "3 unknown distance"}
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
	if rows[0].Marker != "1" || !strings.Contains(rows[0].Text, "Mega Wraith") {
		t.Errorf("first row = %#v, want the Onslaught event as 1", rows[0])
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
	// The nest is the second entry, so it is renumbered 2 whatever the feed called it.
	if rows[1].Marker != "2" || rows[1].Text != "3 x Evolved Longarms" {
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

	// Scale 1.0 is the map this shipped as: the full 59-block city at 20px a block,
	// with a 12pt key.
	if got := mapCellPx(cfg); got != 20 {
		t.Errorf("cell at scale 1 = %dpx, want 20", got)
	}
	if got := mapListPt(cfg); got != 12 {
		t.Errorf("key at scale 1 = %gpt, want 12", got)
	}

	// A bigger scale moves both, in step.
	cfg.Scale = 1.5
	bigCell, bigPt := mapCellPx(cfg), mapListPt(cfg)
	if bigCell != 30 || bigPt != 18 {
		t.Errorf("at scale 1.5: cell %dpx / key %gpt, want 30 and 18", bigCell, bigPt)
	}

	// So does cropping, because the scale is a budget for the window rather than a
	// size per block - which is the whole point of it.
	cfg.Scale, cfg.Radius = 1, 15
	if got := mapCellPx(cfg); got != 38 {
		t.Errorf("cell at radius 15 = %dpx, want 38 (1180 over 31 blocks)", got)
	}
	if got := mapListPt(cfg); got <= 12 {
		t.Errorf("key at radius 15 = %gpt, want more than the uncropped 12", got)
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

// The identifiers on the map must not collide with the letters the outposts are drawn
// with: an event marked D beside Dogg's Stockade's own D is two different things drawn
// the same way. Capitals otherwise, since they read better at cell size.
func TestMarkerCharsAvoidTheOutposts(t *testing.T) {
	taken := map[string]string{}
	for name, letter := range outpostLetters {
		taken[letter] = name
	}
	for i := 0; i < len(markerChars); i++ {
		c := string(markerChars[i])
		if name, clash := taken[c]; clash {
			t.Errorf("marker %q is also %s's letter on the map", c, name)
		}
	}
	if strings.Contains(markerChars, "I") {
		t.Error("I is too close to 1, which is the first marker of all")
	}
	// Digits first, then capitals: the common case should not be lowercase.
	if markerChars[0] != '1' {
		t.Errorf("the first marker is %q, want 1", markerChars[0])
	}
	if got := markerChars[9]; got < 'A' || got > 'Z' {
		t.Errorf("the tenth marker is %q, want a capital", string(got))
	}
	// And there are enough for a busy cycle: a live capture carried 31 active events.
	if len(markerChars) < 40 {
		t.Errorf("%d markers is not enough for a busy feed", len(markerChars))
	}
}
