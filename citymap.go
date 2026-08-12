package main

import (
	"bufio"
	"embed"
	"fmt"
	"strconv"
	"strings"
)

// The city's shape, and what it is for.
//
// Two questions need it. "Which way is the boss" was answered with subtraction -
// four blocks up, one left - and that is wrong often enough to send someone the
// wrong way, because the city is not a full rectangle. 1716 of the 3245
// coordinates in its bounding box are blocks; the rest are gaps you cannot cross
// and have to walk around, so the real distance can be longer than the difference
// in coordinates and the direction to set off in is not always the direction the
// boss is in. The second is the map itself: a grid you can look at is the whole
// point of the boss map, and it cannot be drawn without knowing which cells exist.
//
// The data is committed rather than fetched: see tools/citymapgen and
// knowledge/city-map.md.

//go:embed citymap.txt
var cityMapFS embed.FS

// cityShade is one of the colours the map is painted in. df-hud keeps them because
// they are the difficulty gradient outward from Nastya's Holdout, and reproducing
// them is what makes the console map recognisable to someone who already knows the
// real one. Whether a shade corresponds exactly to df_dangerlevel is NOT known -
// see knowledge/city-map.md - so nothing is derived from them, they are only drawn.
type cityShade struct {
	Letter  byte
	R, G, B uint8
	Alpha   float64
}

// Hex is the shade as GTK CSS wants it, flattened onto black rather than drawn with
// its alpha: the console map has its own background and compositing the two would
// wash the gradient out.
func (s cityShade) Hex() string {
	f := func(c uint8) uint8 { return uint8(float64(c)*s.Alpha + 0.5) }
	return fmt.Sprintf("#%02x%02x%02x", f(s.R), f(s.G), f(s.B))
}

type cityMap struct {
	OriginX, OriginY int
	Width, Height    int

	// cells is row-major, one byte per coordinate: the shade's letter, or '.' for
	// a gap. Kept as the raw bytes because that is also the on-disk form, so a
	// mistake in parsing shows up as a visibly wrong map rather than as a subtly
	// wrong number.
	cells []byte

	shades map[byte]cityShade

	// DividersX and DividersY are the district boundaries, each "the line runs
	// before this coordinate".
	DividersX, DividersY []int
}

// theCity is parsed once at startup. A failure here is a build problem, not a
// runtime one - the file is embedded - so it panics rather than degrading.
var theCity = mustLoadCityMap()

func mustLoadCityMap() *cityMap {
	b, err := cityMapFS.ReadFile("citymap.txt")
	if err != nil {
		panic("citymap.txt is not embedded: " + err.Error())
	}
	m, err := parseCityMap(string(b))
	if err != nil {
		panic("citymap.txt: " + err.Error())
	}
	return m
}

func parseCityMap(s string) (*cityMap, error) {
	m := &cityMap{shades: map[byte]cityShade{}}
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024)
	inMap := false
	var rows []string
	for sc.Scan() {
		line := sc.Text()
		if inMap {
			if line == "" {
				continue
			}
			rows = append(rows, line)
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
			m.shades[letter] = cityShade{Letter: letter, R: r, G: g, B: b, Alpha: a}
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
		for x := 0; x < len(row); x++ {
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

// index is the cell offset for a city coordinate, or -1 for anywhere outside the
// map's bounding box. Onslaught's 3000,3000 lands here, which is correct: it is a
// real coordinate but not a place on this map, so it has no neighbours and no route.
func (m *cityMap) index(x, y int) int {
	cx, cy := x-m.OriginX, y-m.OriginY
	if cx < 0 || cy < 0 || cx >= m.Width || cy >= m.Height {
		return -1
	}
	return cy*m.Width + cx
}

func (m *cityMap) coordAt(i int) (x, y int) {
	return m.OriginX + i%m.Width, m.OriginY + i/m.Width
}

// IsBlock reports whether a coordinate is somewhere you can stand.
func (m *cityMap) IsBlock(x, y int) bool {
	i := m.index(x, y)
	return i >= 0 && m.cells[i] != '.'
}

// Shade is how the map paints a block.
func (m *cityMap) Shade(x, y int) (cityShade, bool) {
	i := m.index(x, y)
	if i < 0 || m.cells[i] == '.' {
		return cityShade{}, false
	}
	s, ok := m.shades[m.cells[i]]
	return s, ok
}

// Shades is every shade the map uses, dark to light, for a legend.
func (m *cityMap) Shades() []cityShade {
	out := make([]cityShade, 0, len(m.shades))
	seen := map[byte]bool{}
	for _, c := range m.cells {
		if c == '.' || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, m.shades[c])
	}
	// The file lists shades dark to light; sort back into that order rather than
	// first-appearance order, which is a scan of the top row.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Letter < out[j-1].Letter; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Blocks is the number of real blocks, for tests and diagnostics.
func (m *cityMap) Blocks() int {
	n := 0
	for _, c := range m.cells {
		if c != '.' {
			n++
		}
	}
	return n
}

// unreachable marks a cell no walk from the origin reached. Distances are in blocks
// and the map is 59x55, so a value this large cannot be confused with a real one.
const unreachable int32 = -1

// walkDistances is how many block moves it takes to reach every block, from one
// block, going around the gaps. A plain breadth-first search: moves are the four
// compass directions, every move costs the same, and the returned slice is indexed
// the same way as cells.
//
// Two adjacent blocks are assumed to be connected. That is not something any feed
// states, and it is the one assumption left in here now that the gaps are known -
// but it is the assumption the in-game map's own layout implies, and a wrong edge
// costs a block or two of estimate rather than a wrong direction.
func (m *cityMap) walkDistances(fromX, fromY int) []int32 {
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

// cityWalk is a route between two blocks.
type cityWalk struct {
	// Blocks is how far it actually is, walking around the gaps.
	Blocks int
	// DX and DY are the difference in coordinates, which is what a player sights
	// down and reads as "up and to the left". Negative is left and up.
	DX, DY int
	// Detour is how much longer the walk is than the difference in coordinates
	// suggests - zero when you can go straight there.
	Detour int
}

// Route is how to get from one block to another.
func (m *cityMap) Route(fromX, fromY, toX, toY int) (cityWalk, bool) {
	dist := m.walkDistances(fromX, fromY)
	return m.routeFrom(dist, fromX, fromY, toX, toY)
}

func (m *cityMap) routeFrom(dist []int32, fromX, fromY, toX, toY int) (cityWalk, bool) {
	j := m.index(toX, toY)
	if j < 0 || dist[j] == unreachable {
		return cityWalk{}, false
	}
	dx, dy := toX-fromX, toY-fromY
	straight := abs(dx) + abs(dy)
	return cityWalk{
		Blocks: int(dist[j]),
		DX:     dx,
		DY:     dy,
		Detour: int(dist[j]) - straight,
	}, true
}
