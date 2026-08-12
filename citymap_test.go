package main

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

// The committed map is the thing every route and the whole console grid is built
// on, and it came out of someone else's stylesheet, so it is checked against
// independent evidence rather than trusted.

func TestCityMapLoads(t *testing.T) {
	m := theCity
	if m.OriginX != 1000 || m.OriginY != 981 {
		t.Errorf("origin is %d,%d, want 1000,981", m.OriginX, m.OriginY)
	}
	if m.Width != 59 || m.Height != 55 {
		t.Errorf("size is %dx%d, want 59x55", m.Width, m.Height)
	}
	if got := m.Blocks(); got != 1716 {
		t.Errorf("%d blocks, want 1716 - if the game changed the city, regenerate and say so in the commit", got)
	}
	if len(m.Shades()) != 16 {
		t.Errorf("%d shades, want 16", len(m.Shades()))
	}
	// Dark to light, so a legend reads as a gradient.
	shades := m.Shades()
	for i := 1; i < len(shades); i++ {
		if shades[i].Letter <= shades[i-1].Letter {
			t.Errorf("shade %d is out of order", i)
		}
	}
	if got := shades[0].Hex(); got == "#000000" {
		t.Errorf("shade %c flattens to black, which would be invisible", shades[0].Letter)
	}
}

func TestCityMapKnowsTheOutposts(t *testing.T) {
	// Every outpost the game itself lists (newoutpost.js, ported into block.go)
	// must be a block. An offset error of even one would fail this.
	for _, o := range outposts {
		if !theCity.IsBlock(o.X, o.Y) {
			t.Errorf("%s at %d,%d is not on the map", o.Name, o.X, o.Y)
		}
	}
}

// Every event in the recorded feed lands on a block. This is the check that the
// gaps are real: 201 locations chosen by the game, none of them in a hole.
func TestCityMapAgreesWithTheFeed(t *testing.T) {
	raw, err := os.ReadFile("testdata/bossmap.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	type event struct {
		Locations [][]string `json:"locations"`
	}
	checked := 0
	for key, rawEvent := range doc {
		var e event
		if err := json.Unmarshal(rawEvent, &e); err != nil {
			continue // bosshash, servertime, version
		}
		for _, loc := range e.Locations {
			if len(loc) != 2 {
				continue
			}
			x, err := strconv.Atoi(loc[0])
			if err != nil {
				continue
			}
			y, err := strconv.Atoi(loc[1])
			if err != nil {
				continue
			}
			if x == onslaughtCoord && y == onslaughtCoord {
				continue // Onslaught is a real coordinate but not a place on this map
			}
			checked++
			if !theCity.IsBlock(x, y) {
				t.Errorf("event %s is at %d,%d, which the map calls a gap", key, x, y)
			}
		}
	}
	if checked < 100 {
		t.Fatalf("only checked %d locations; the fixture should have around 200", checked)
	}
}

func TestCityMapGapsAreNotBlocks(t *testing.T) {
	// The southern strip is two blocks wide where it leaves the city: 1016,1020 is
	// in it and 1013,1020 is not, which is exactly the shape a straight-line
	// distance gets wrong.
	if !theCity.IsBlock(1016, 1020) {
		t.Error("1016,1020 should be a block")
	}
	if theCity.IsBlock(1013, 1020) {
		t.Error("1013,1020 should be a gap")
	}
	// And the block directly north of Ground Zero is a gap, so the only way out of
	// that outpost is west. This is the kind of thing subtraction cannot know.
	if theCity.IsBlock(1058, 1018) {
		t.Error("1058,1018 should be a gap")
	}
	// Outside the bounding box entirely, which is where Onslaught sits.
	if theCity.IsBlock(onslaughtCoord, onslaughtCoord) {
		t.Error("3000,3000 is not on the city map")
	}
	if _, ok := theCity.Shade(onslaughtCoord, onslaughtCoord); ok {
		t.Error("3000,3000 should have no shade")
	}
}

// The whole city is walkable from Nastya's Holdout. If a change to the map ever
// islands part of it, routes to that part would silently stop being offered.
func TestCityIsOneConnectedPlace(t *testing.T) {
	dist := theCity.walkDistances(1000, 1000)
	stranded := 0
	for i, c := range theCity.cells {
		if c == '.' {
			continue
		}
		if dist[i] == unreachable {
			stranded++
			if stranded < 5 {
				x, y := theCity.coordAt(i)
				t.Errorf("%d,%d cannot be walked to from Nastya's Holdout", x, y)
			}
		}
	}
	if stranded > 0 {
		t.Errorf("%d blocks are cut off", stranded)
	}
}

func TestCityRouteGoesAroundGaps(t *testing.T) {
	// Down the southern strip, which is not straight: four blocks apart by
	// subtraction, eight to walk.
	walk, ok := theCity.Route(1016, 1021, 1015, 1024)
	if !ok {
		t.Fatal("no route down the southern strip")
	}
	if walk.Blocks != 8 {
		t.Errorf("walk is %d blocks, want 8", walk.Blocks)
	}
	straight := abs(walk.DX) + abs(walk.DY)
	if walk.Blocks < straight {
		t.Errorf("walk of %d is shorter than the straight line of %d, which is impossible",
			walk.Blocks, straight)
	}
	if walk.Detour != walk.Blocks-straight {
		t.Errorf("detour %d does not match %d - %d", walk.Detour, walk.Blocks, straight)
	}
	if walk.Detour == 0 {
		t.Errorf("expected a detour into the strip, got a straight walk of %d", walk.Blocks)
	}
}

func TestCityRouteWithinOpenGround(t *testing.T) {
	// Two blocks with clear ground between them: the walk should be exactly the
	// coordinate difference, or the search is finding phantom obstacles.
	walk, ok := theCity.Route(1046, 999, 1051, 1000)
	if !ok {
		t.Fatal("no route across open ground")
	}
	if walk.Blocks != 6 || walk.Detour != 0 {
		t.Errorf("walk is %d blocks with detour %d, want 6 and 0", walk.Blocks, walk.Detour)
	}
	if walk.DX != 5 || walk.DY != 1 {
		t.Errorf("deltas are %d,%d, want 5,1", walk.DX, walk.DY)
	}
}

func TestCityRouteFromNowhere(t *testing.T) {
	// From Onslaught, and from a gap: no route rather than a wrong one.
	if _, ok := theCity.Route(onslaughtCoord, onslaughtCoord, 1000, 1000); ok {
		t.Error("routed out of Onslaught")
	}
	if _, ok := theCity.Route(1013, 1020, 1000, 1000); ok {
		t.Error("routed out of a gap")
	}
	if _, ok := theCity.Route(1000, 1000, 1013, 1020); ok {
		t.Error("routed into a gap")
	}
}

func TestCityMapDistrictDividers(t *testing.T) {
	// Nine districts on the main map: two vertical lines and, above the southern
	// strip, two horizontal ones. The strip's own line makes three.
	if len(theCity.DividersX) != 2 {
		t.Errorf("%d vertical dividers, want 2", len(theCity.DividersX))
	}
	if len(theCity.DividersY) != 3 {
		t.Errorf("%d horizontal dividers, want 3", len(theCity.DividersY))
	}
}

func TestParseCityMapRejectsNonsense(t *testing.T) {
	// The file is embedded, so a bad one is a build-time mistake - but it is parsed
	// with a real parser and every branch of it should refuse rather than guess.
	for name, body := range map[string]string{
		"no size":        "origin 1 1\nmap\n..\n",
		"short row":      "origin 1 1\nsize 3 1\nshade a 010203 1\nmap\naa\n",
		"missing rows":   "origin 1 1\nsize 2 2\nshade a 010203 1\nmap\naa\n",
		"unknown shade":  "origin 1 1\nsize 2 1\nmap\nab\n",
		"unknown key":    "origin 1 1\nsize 2 1\nwibble 3\nmap\n..\n",
		"bad divider":    "origin 1 1\nsize 2 1\ndivider z 4\nmap\n..\n",
		"bad shade line": "origin 1 1\nsize 2 1\nshade aa 010203 1\nmap\n..\n",
	} {
		if _, err := parseCityMap(body); err == nil {
			t.Errorf("%s: parsed without complaint", name)
		}
	}
}
