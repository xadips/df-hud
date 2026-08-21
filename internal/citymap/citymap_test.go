package citymap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestDefaultMapLoads(t *testing.T) {
	m := Default()
	if m.OriginX != 1000 || m.OriginY != 981 || m.Width != 59 || m.Height != 55 {
		t.Errorf("geometry = origin %d,%d size %dx%d", m.OriginX, m.OriginY, m.Width, m.Height)
	}
	if got := m.Blocks(); got != 1716 {
		t.Errorf("Blocks = %d, want 1716", got)
	}
	shades := m.Shades()
	if len(shades) != 16 {
		t.Fatalf("Shades = %d, want 16", len(shades))
	}
	for i := 1; i < len(shades); i++ {
		if shades[i].Letter <= shades[i-1].Letter {
			t.Errorf("shade %d is out of order", i)
		}
	}
	if shades[0].Hex() == "#000000" {
		t.Error("darkest shade flattened to black")
	}
}

func TestMapKnowsOutposts(t *testing.T) {
	for _, outpost := range Outposts() {
		if !Default().IsBlock(outpost.X, outpost.Y) {
			t.Errorf("%s at %d,%d is not on the map", outpost.Name, outpost.X, outpost.Y)
		}
	}
}

func TestMapAgreesWithBossFeed(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "bossmap.json"))
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
		var event event
		if json.Unmarshal(rawEvent, &event) != nil {
			continue
		}
		for _, loc := range event.Locations {
			if len(loc) != 2 {
				continue
			}
			x, errX := strconv.Atoi(loc[0])
			y, errY := strconv.Atoi(loc[1])
			if errX != nil || errY != nil || x == 3000 && y == 3000 {
				continue
			}
			checked++
			if !Default().IsBlock(x, y) {
				t.Errorf("event %s at %d,%d is in a gap", key, x, y)
			}
		}
	}
	if checked < 100 {
		t.Fatalf("only checked %d feed locations", checked)
	}
}

func TestGapsAndConnectedness(t *testing.T) {
	m := Default()
	if !m.IsBlock(1016, 1020) || m.IsBlock(1013, 1020) || m.IsBlock(1058, 1018) {
		t.Error("known gap geometry changed")
	}
	if m.IsBlock(3000, 3000) {
		t.Error("Onslaught is not on the city map")
	}
	if _, ok := m.Shade(3000, 3000); ok {
		t.Error("Onslaught unexpectedly has a shade")
	}
	dist := m.WalkDistances(1000, 1000)
	for i, cell := range m.cells {
		if cell != '.' && dist[i] == unreachable {
			x, y := m.coordAt(i)
			t.Errorf("%d,%d is unreachable", x, y)
		}
	}
}

func TestRoutes(t *testing.T) {
	m := Default()
	walk, ok := m.Route(1016, 1021, 1015, 1024)
	if !ok || walk.Blocks != 8 || walk.Detour == 0 {
		t.Errorf("detour route = %+v, %v", walk, ok)
	}
	walk, ok = m.Route(1046, 999, 1051, 1000)
	if !ok || walk.Blocks != 6 || walk.Detour != 0 || walk.DX != 5 || walk.DY != 1 {
		t.Errorf("open route = %+v, %v", walk, ok)
	}
	for _, route := range [][4]int{
		{3000, 3000, 1000, 1000},
		{1013, 1020, 1000, 1000},
		{1000, 1000, 1013, 1020},
	} {
		if _, ok := m.Route(route[0], route[1], route[2], route[3]); ok {
			t.Errorf("invalid route succeeded: %v", route)
		}
	}
}

func TestDistrictDividers(t *testing.T) {
	m := Default()
	if len(m.DividersX) != 2 || len(m.DividersY) != 3 {
		t.Errorf("dividers = %v %v", m.DividersX, m.DividersY)
	}
	if !m.DividesColumn(1041, 1019) || m.DividesColumn(1041, 1020) {
		t.Error("vertical divider does not follow city edge")
	}
	if !m.DividesRow(1035, 1020) || m.DividesRow(1005, 1020) {
		t.Error("horizontal divider does not follow city edge")
	}
	for _, y := range []int{985, 995} {
		if m.IsBlock(1040, y) == m.IsBlock(1041, y) {
			t.Fatalf("test needs a one-sided boundary at y=%d", y)
		}
		if !m.DividesColumn(1041, y) {
			t.Errorf("one-sided boundary missing at y=%d", y)
		}
	}
}

func TestParseRejectsNonsense(t *testing.T) {
	for name, body := range map[string]string{
		"no size":        "origin 1 1\nmap\n..\n",
		"short row":      "origin 1 1\nsize 3 1\nshade a 010203 1\nmap\naa\n",
		"missing rows":   "origin 1 1\nsize 2 2\nshade a 010203 1\nmap\naa\n",
		"unknown shade":  "origin 1 1\nsize 2 1\nmap\nab\n",
		"unknown key":    "origin 1 1\nsize 2 1\nwibble 3\nmap\n..\n",
		"bad divider":    "origin 1 1\nsize 2 1\ndivider z 4\nmap\n..\n",
		"bad shade line": "origin 1 1\nsize 2 1\nshade aa 010203 1\nmap\n..\n",
	} {
		if _, err := parse(body); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
}

func TestLocationNames(t *testing.T) {
	if TradeZoneName(3) != "North Eastern" || TradeZoneShort(3) != "NE" {
		t.Error("trade zone names changed")
	}
	if TradeZoneName(99) != "" {
		t.Error("unknown trade zone has a name")
	}
	if OutpostName(1058, 1019) != "Ground Zero" || OutpostName(1040, 1000) != "" {
		t.Error("outpost lookup changed")
	}
}
