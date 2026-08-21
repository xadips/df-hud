// Package citymap provides the embedded city geometry, routing, shading, and
// location naming tables.
package citymap

import (
	"bufio"
	"embed"
	"fmt"
	"strconv"
	"strings"
)

//go:embed citymap.txt
var cityMapFS embed.FS

// Shade is one of the colours used to paint the city map.
type Shade struct {
	Letter  byte
	R, G, B uint8
	Alpha   float64
}

// Hex returns the shade flattened onto black as GTK CSS.
func (s Shade) Hex() string {
	f := func(c uint8) uint8 { return uint8(float64(c)*s.Alpha + 0.5) }
	return fmt.Sprintf("#%02x%02x%02x", f(s.R), f(s.G), f(s.B))
}

// Map is the city's walkable geometry and visual metadata.
type Map struct {
	OriginX, OriginY int
	Width, Height    int
	DividersX        []int
	DividersY        []int

	cells  []byte
	shades map[byte]Shade
}

var defaultMap = mustLoad()

// Default returns the immutable embedded city map.
func Default() *Map { return defaultMap }

func mustLoad() *Map {
	b, err := cityMapFS.ReadFile("citymap.txt")
	if err != nil {
		panic("citymap.txt is not embedded: " + err.Error())
	}
	m, err := parse(string(b))
	if err != nil {
		panic("citymap.txt: " + err.Error())
	}
	return m
}

func parse(s string) (*Map, error) {
	m := &Map{shades: map[byte]Shade{}}
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024)
	inMap := false
	var rows []string
	for sc.Scan() {
		line := sc.Text()
		if inMap {
			if line != "" {
				rows = append(rows, line)
			}
			continue
		}
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "map":
			inMap = true
		case "origin":
			if len(fields) != 3 {
				return nil, fmt.Errorf("origin wants two numbers, got %q", line)
			}
			var err error
			if m.OriginX, err = strconv.Atoi(fields[1]); err != nil {
				return nil, err
			}
			if m.OriginY, err = strconv.Atoi(fields[2]); err != nil {
				return nil, err
			}
		case "size":
			if len(fields) != 3 {
				return nil, fmt.Errorf("size wants two numbers, got %q", line)
			}
			var err error
			if m.Width, err = strconv.Atoi(fields[1]); err != nil {
				return nil, err
			}
			if m.Height, err = strconv.Atoi(fields[2]); err != nil {
				return nil, err
			}
		case "divider":
			if len(fields) != 3 {
				return nil, fmt.Errorf("divider wants an axis and a number, got %q", line)
			}
			n, err := strconv.Atoi(fields[2])
			if err != nil {
				return nil, err
			}
			switch fields[1] {
			case "x":
				m.DividersX = append(m.DividersX, n)
			case "y":
				m.DividersY = append(m.DividersY, n)
			default:
				return nil, fmt.Errorf("divider axis %q is not x or y", fields[1])
			}
		case "shade":
			if len(fields) != 4 || len(fields[1]) != 1 {
				return nil, fmt.Errorf("shade wants a letter, rrggbb and an alpha, got %q", line)
			}
			var r, g, b uint8
			if _, err := fmt.Sscanf(fields[2], "%02x%02x%02x", &r, &g, &b); err != nil {
				return nil, fmt.Errorf("shade %s: %w", fields[1], err)
			}
			a, err := strconv.ParseFloat(fields[3], 64)
			if err != nil {
				return nil, fmt.Errorf("shade %s: %w", fields[1], err)
			}
			letter := fields[1][0]
			m.shades[letter] = Shade{Letter: letter, R: r, G: g, B: b, Alpha: a}
		default:
			return nil, fmt.Errorf("unknown keyword %q", fields[0])
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if m.Width <= 0 || m.Height <= 0 {
		return nil, fmt.Errorf("size is missing")
	}
	if len(rows) != m.Height {
		return nil, fmt.Errorf("map has %d rows, size says %d", len(rows), m.Height)
	}
	m.cells = make([]byte, 0, m.Width*m.Height)
	for y, row := range rows {
		if len(row) != m.Width {
			return nil, fmt.Errorf("row %d is %d wide, size says %d", y, len(row), m.Width)
		}
		for x := range len(row) {
			c := row[x]
			if c != '.' {
				if _, ok := m.shades[c]; !ok {
					return nil, fmt.Errorf("row %d column %d uses shade %q, which is not declared", y, x, string(c))
				}
			}
			m.cells = append(m.cells, c)
		}
	}
	return m, nil
}

func (m *Map) index(x, y int) int {
	cx, cy := x-m.OriginX, y-m.OriginY
	if cx < 0 || cy < 0 || cx >= m.Width || cy >= m.Height {
		return -1
	}
	return cy*m.Width + cx
}

func (m *Map) coordAt(i int) (x, y int) {
	return m.OriginX + i%m.Width, m.OriginY + i/m.Width
}

// IsBlock reports whether a coordinate is somewhere a player can stand.
func (m *Map) IsBlock(x, y int) bool {
	i := m.index(x, y)
	return i >= 0 && m.cells[i] != '.'
}

// DividesColumn reports whether a district line has blocks on either side.
func (m *Map) DividesColumn(x, y int) bool {
	return m.IsBlock(x-1, y) || m.IsBlock(x, y)
}

// DividesRow reports whether a district line has blocks on either side.
func (m *Map) DividesRow(x, y int) bool {
	return m.IsBlock(x, y-1) || m.IsBlock(x, y)
}

// Shade returns the block's map shade.
func (m *Map) Shade(x, y int) (Shade, bool) {
	i := m.index(x, y)
	if i < 0 || m.cells[i] == '.' {
		return Shade{}, false
	}
	s, ok := m.shades[m.cells[i]]
	return s, ok
}

// Shades returns every map shade, dark to light.
func (m *Map) Shades() []Shade {
	out := make([]Shade, 0, len(m.shades))
	seen := map[byte]bool{}
	for _, c := range m.cells {
		if c == '.' || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, m.shades[c])
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Letter < out[j-1].Letter; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Blocks returns the number of real blocks.
func (m *Map) Blocks() int {
	n := 0
	for _, c := range m.cells {
		if c != '.' {
			n++
		}
	}
	return n
}

const unreachable int32 = -1

// WalkDistances computes block distances from one coordinate. The result is an
// opaque routing table accepted by RouteFrom.
func (m *Map) WalkDistances(fromX, fromY int) []int32 {
	dist := make([]int32, len(m.cells))
	for i := range dist {
		dist[i] = unreachable
	}
	start := m.index(fromX, fromY)
	if start < 0 || m.cells[start] == '.' {
		return dist
	}
	dist[start] = 0
	queue := make([]int32, 1, len(m.cells))
	queue[0] = int32(start)
	for head := 0; head < len(queue); head++ {
		i := int(queue[head])
		x, y := m.coordAt(i)
		for _, n := range [4][2]int{{x + 1, y}, {x - 1, y}, {x, y + 1}, {x, y - 1}} {
			j := m.index(n[0], n[1])
			if j < 0 || m.cells[j] == '.' || dist[j] != unreachable {
				continue
			}
			dist[j] = dist[i] + 1
			queue = append(queue, int32(j))
		}
	}
	return dist
}

// Walk is a route between two city blocks.
type Walk struct {
	Blocks int
	DX, DY int
	Detour int
}

// Route computes a walk between two blocks.
func (m *Map) Route(fromX, fromY, toX, toY int) (Walk, bool) {
	return m.RouteFrom(m.WalkDistances(fromX, fromY), fromX, fromY, toX, toY)
}

// RouteFrom reuses a WalkDistances table to route to a destination.
func (m *Map) RouteFrom(dist []int32, fromX, fromY, toX, toY int) (Walk, bool) {
	j := m.index(toX, toY)
	if j < 0 || j >= len(dist) || dist[j] == unreachable {
		return Walk{}, false
	}
	dx, dy := toX-fromX, toY-fromY
	straight := abs(dx) + abs(dy)
	return Walk{
		Blocks: int(dist[j]),
		DX:     dx,
		DY:     dy,
		Detour: int(dist[j]) - straight,
	}, true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// TradeZoneName returns the game's full trade-zone name.
func TradeZoneName(zone int) string {
	switch zone {
	case 1:
		return "North Western"
	case 2:
		return "Northern"
	case 3:
		return "North Eastern"
	case 4:
		return "Western"
	case 5:
		return "Central"
	case 6:
		return "Eastern"
	case 7:
		return "South Western"
	case 8:
		return "Southern"
	case 9:
		return "South Eastern"
	case 10:
		return "Wastelands"
	case 21:
		return "Outpost"
	case 22:
		return "Valcrest"
	}
	return ""
}

// TradeZoneShort returns the game's compact trade-zone name.
func TradeZoneShort(zone int) string {
	switch zone {
	case 1:
		return "NW"
	case 2:
		return "North"
	case 3:
		return "NE"
	case 4:
		return "West"
	case 5:
		return "Central"
	case 6:
		return "East"
	case 7:
		return "SW"
	case 8:
		return "South"
	case 9:
		return "SE"
	case 10:
		return "Wastelands"
	case 21:
		return "Outpost"
	case 22:
		return "Valcrest"
	}
	return ""
}

// Outpost is one fixed city outpost.
type Outpost struct {
	X, Y int
	Name string
	Slug string
}

var outposts = [...]Outpost{
	{1000, 1000, "Nastya's Holdout", "nastya"},
	{1005, 985, "Dogg's Stockade", "doggs"},
	{1012, 1019, "Precinct 13", "precinct"},
	{1029, 1003, "Fort Pastor", "fort"},
	{1054, 987, "Secronom Bunker", "bunker"},
	{1032, 985, "Valcrest", "valcrest"},
	{1058, 1019, "Ground Zero", "groundzero"},
}

// Outposts returns the fixed outpost table.
func Outposts() []Outpost { return append([]Outpost(nil), outposts[:]...) }

// OutpostName returns the outpost at a coordinate, or empty.
func OutpostName(x, y int) string {
	for _, outpost := range outposts {
		if outpost.X == x && outpost.Y == y {
			return outpost.Name
		}
	}
	return ""
}
