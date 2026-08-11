package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

// The public game data. get_allstats.php?printvars=1 is unauthenticated, about
// 1MB, and changes only when the game updates, so it is fetched at most once a
// day and cached on disk. Same feed df-allstats-watcher already polls.
//
// Two things in it matter to df-hud:
//
//   - exp_lvl<N>: the XP that must accumulate WITHIN level N-1 to reach level
//     N. Not an assumption - the game's own checkIfLevelUp (base.js:448-478)
//     compares df_exp against exp_lvl[level+1] and carries the remainder over
//     as the new df_exp on level-up. That is exactly what makes a cumulative
//     XP counter reconstructable, and therefore what makes XP/hr immune to the
//     level-up reset in df_exp.
//   - zones_<x>_<y>_name (a 13x13 grid of neighbourhood names) and
//     areas_<x>_<y>_* (a 39x39 grid of city blocks): the map data behind
//     Block Info.
//
// UNSOLVED, and deliberately not guessed at: how df_positionx/y map onto the
// areas grid. Observed positions are ~1000-centred - the outposts span x
// 1000..1058 and y 985..1019 (newoutpost.js:13-36) - while the grid is indexed
// 1..39. So there is an offset, and probably also a scale: 13 zones x 3 areas
// per zone = 39 cells covering what looks like a ~78-unit span hints at 2
// position units per cell. Nothing in the saved web client references zones_ or
// areas_ at all (only the standalone client does), so there is no code to port.
//
// Everything here therefore takes GRID indices, never positions. block.go owns
// the transform and treats it as unknown until calibrated, so Block Info v1
// works from coordinates, tradezone and danger level alone.
const catalogSchemaVersion = 1

// Observed feed geometry, used as sanity checks rather than as hard
// requirements: a game update could legitimately extend the map.
const (
	expectedZoneCols = 13
	expectedZoneRows = 13
	expectedAreaCols = 39
	expectedAreaRows = 39
	// catalogMaxBody caps the download. The real body is ~1MB; 8MB is room for
	// growth while still refusing to buffer something pathological.
	catalogMaxBody = 8 << 20
	// catalogMinBody rejects a truncated response before it becomes a half
	// catalog with a plausible-looking XP table.
	catalogMinBody = 64 << 10
)

// Area is one city block. Field semantics come from a census of the live feed:
//
//   - type is present on 958 of 1521 cells and its value is ALWAYS "street",
//     so it is a flag, not an enumeration.
//   - building is present on 1014 cells, and every street cell has one, so a
//     street and a building are not alternatives: the building is what you can
//     enter from that street.
//   - building_direction (north/south/east/west) is which way the entrance
//     faces.
//   - building_size is present on all 1521 cells and is "0" everywhere, so it
//     carries no information and is not stored. If a future feed gives it real
//     values, that is the moment to add it.
//   - helicopter appears exactly once in the whole feed, at areas_25_26. It is
//     kept because a single uniquely-identifiable landmark is the strongest
//     available anchor for calibrating the position-to-grid transform.
type Area struct {
	Street     bool   `json:"street,omitempty"`
	Building   string `json:"building,omitempty"`
	Direction  string `json:"direction,omitempty"`
	Helicopter bool   `json:"helicopter,omitempty"`
}

// Known reports whether the feed said anything about this block. 507 of the
// 1521 cells are blank, so "no data" is a normal answer, not an error.
func (a Area) Known() bool {
	return a.Street || a.Building != "" || a.Helicopter
}

type Catalog struct {
	SchemaVersion int       `json:"schema_version"`
	FetchedAt     time.Time `json:"fetched_at"`

	// MaxLevel is the highest level the feed has a threshold for (415 today,
	// which matches the hard cap in checkIfLevelUp).
	MaxLevel int `json:"max_level"`
	// ExpToReach is indexed by level: ExpToReach[n] is the feed's exp_lvl<n>,
	// the XP needed within level n-1 to reach level n. Entries 0 and 1 are
	// unused, so the index is the level and no off-by-one arithmetic is needed
	// at the call sites.
	ExpToReach []int64 `json:"exp_to_reach"`

	ZoneCols  int      `json:"zone_cols"`
	ZoneRows  int      `json:"zone_rows"`
	ZoneNames []string `json:"zone_names"` // row-major, index = (y-1)*ZoneCols + (x-1)

	AreaCols int    `json:"area_cols"`
	AreaRows int    `json:"area_rows"`
	Areas    []Area `json:"areas"` // row-major, index = (y-1)*AreaCols + (x-1)

	// cumExp[n] is the total XP earned since level 1 by the moment you reach
	// level n, i.e. the prefix sum of ExpToReach. Derived, not persisted:
	// storing it would double the file to save a loop over 415 integers.
	cumExp []int64
}

var (
	expKeyRe   = regexp.MustCompile(`^exp_lvl(\d+)$`)
	zoneKeyRe  = regexp.MustCompile(`^zones_(\d+)_(\d+)_name$`)
	areaKeyRe  = regexp.MustCompile(`^areas_(\d+)_(\d+)_(.+)$`)
	catalogMax = 10000 // sanity bound on grid indices and level numbers
)

// parseCatalog builds a catalog from an already-parsed feed. It takes the map
// rather than the raw body so the same code serves the network path, the disk
// path and the test fixture.
func parseCatalog(vars map[string]string, fetchedAt time.Time) (*Catalog, error) {
	c := &Catalog{SchemaVersion: catalogSchemaVersion, FetchedAt: fetchedAt}

	// --- XP table ---
	exp := map[int]int64{}
	for key, raw := range vars {
		m := expKeyRe.FindStringSubmatch(key)
		if m == nil {
			continue
		}
		level, err := strconv.Atoi(m[1])
		if err != nil || level < 1 || level > catalogMax {
			continue
		}
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("catalog: %s = %q is not a number", key, raw)
		}
		if v < 0 {
			return nil, fmt.Errorf("catalog: %s = %d is negative", key, v)
		}
		if level > c.MaxLevel {
			c.MaxLevel = level
		}
		exp[level] = v
	}
	if c.MaxLevel < 2 {
		return nil, errors.New("catalog: the feed carries no exp_lvl table")
	}
	// A hole in the table would silently corrupt every cumulative sum past it,
	// which would show up as an XP rate that is quietly wrong rather than
	// obviously broken. Refuse instead.
	c.ExpToReach = make([]int64, c.MaxLevel+1)
	for level := 2; level <= c.MaxLevel; level++ {
		v, ok := exp[level]
		if !ok {
			return nil, fmt.Errorf("catalog: exp_lvl%d is missing, so the XP table has a hole "+
				"and cumulative totals past level %d would be wrong", level, level-1)
		}
		c.ExpToReach[level] = v
	}

	// --- neighbourhood names ---
	zoneNames := map[[2]int]string{}
	maxZX, maxZY := 0, 0
	for key, raw := range vars {
		m := zoneKeyRe.FindStringSubmatch(key)
		if m == nil {
			continue
		}
		x, y, ok := gridIndices(m[1], m[2])
		if !ok {
			continue
		}
		zoneNames[[2]int{x, y}] = raw
		maxZX, maxZY = max(maxZX, x), max(maxZY, y)
	}
	if len(zoneNames) > 0 {
		c.ZoneCols, c.ZoneRows = maxZX, maxZY
		c.ZoneNames = make([]string, maxZX*maxZY)
		for xy, name := range zoneNames {
			c.ZoneNames[(xy[1]-1)*maxZX+(xy[0]-1)] = name
		}
	}

	// --- city blocks ---
	areas := map[[2]int]Area{}
	maxAX, maxAY := 0, 0
	for key, raw := range vars {
		m := areaKeyRe.FindStringSubmatch(key)
		if m == nil {
			continue
		}
		x, y, ok := gridIndices(m[1], m[2])
		if !ok {
			continue
		}
		xy := [2]int{x, y}
		a := areas[xy]
		switch m[3] {
		case "type":
			// Always "street" in the live feed; treat anything else as a
			// street too rather than dropping a block we do not recognise.
			a.Street = true
		case "building":
			a.Building = raw
		case "building_direction":
			a.Direction = raw
		case "helicopter":
			a.Helicopter = raw != "" && raw != "0"
		case "building_size":
			// Always "0"; see the Area doc comment.
		default:
			// A new subkey from a game update. Ignored on purpose: an unknown
			// field in somebody else's feed is not our error to raise.
		}
		areas[xy] = a
		maxAX, maxAY = max(maxAX, x), max(maxAY, y)
	}
	if len(areas) > 0 {
		c.AreaCols, c.AreaRows = maxAX, maxAY
		c.Areas = make([]Area, maxAX*maxAY)
		for xy, a := range areas {
			c.Areas[(xy[1]-1)*maxAX+(xy[0]-1)] = a
		}
	}

	c.finish()
	return c, nil
}

// gridIndices parses a 1-based coordinate pair, rejecting anything absurd so a
// malformed key cannot allocate a huge grid.
func gridIndices(sx, sy string) (int, int, bool) {
	x, err1 := strconv.Atoi(sx)
	y, err2 := strconv.Atoi(sy)
	if err1 != nil || err2 != nil || x < 1 || y < 1 || x > catalogMax || y > catalogMax {
		return 0, 0, false
	}
	return x, y, true
}

// finish computes the derived prefix sums. Must be called after parsing and
// after loading from disk, since cumExp is not persisted.
func (c *Catalog) finish() {
	c.cumExp = make([]int64, len(c.ExpToReach))
	var total int64
	for level := 2; level < len(c.ExpToReach); level++ {
		total += c.ExpToReach[level]
		c.cumExp[level] = total
	}
}

// ExpNeeded is the threshold shown as "df_exp / needed" in the game's own
// sidebar (base.js:1660): the XP required to leave the given level.
func (c *Catalog) ExpNeeded(level int) (int64, bool) {
	if level < 1 || level+1 >= len(c.ExpToReach) {
		return 0, false // level 415 is the cap and has nothing above it
	}
	return c.ExpToReach[level+1], true
}

// CumulativeXP is total XP earned since level 1, the counter XP/hr differences.
// It exists because df_exp resets to the carry-over on every level-up
// (checkIfLevelUp writes currentExp - neededExp), so a naive delta goes
// negative at each boundary. This sum is continuous across level-ups by
// construction: the XP that vanishes from df_exp reappears in the prefix sum.
func (c *Catalog) CumulativeXP(level int, expInLevel int64) (int64, bool) {
	if level < 1 || level >= len(c.cumExp) {
		return 0, false
	}
	return c.cumExp[level] + expInLevel, true
}

func (c *Catalog) ZoneName(x, y int) (string, bool) {
	if c.ZoneCols == 0 || x < 1 || y < 1 || x > c.ZoneCols || y > c.ZoneRows {
		return "", false
	}
	name := c.ZoneNames[(y-1)*c.ZoneCols+(x-1)]
	return name, name != ""
}

func (c *Catalog) AreaAt(x, y int) (Area, bool) {
	if c.AreaCols == 0 || x < 1 || y < 1 || x > c.AreaCols || y > c.AreaRows {
		return Area{}, false
	}
	a := c.Areas[(y-1)*c.AreaCols+(x-1)]
	return a, a.Known()
}

// Helicopter returns the grid position of the crash site, the feed's only
// unique landmark. Kept as an anchor for solving the position-to-grid
// transform: one identifiable point on both sides of the mapping is worth more
// than any number of statistical hits.
func (c *Catalog) Helicopter() (x, y int, ok bool) {
	for i, a := range c.Areas {
		if a.Helicopter {
			return i%c.AreaCols + 1, i/c.AreaCols + 1, true
		}
	}
	return 0, 0, false
}

// Summary is one log line at startup, and the fastest way to notice that a
// game update changed the feed's shape.
func (c *Catalog) Summary() string {
	s := fmt.Sprintf("levels 2..%d", c.MaxLevel)
	if c.ZoneCols > 0 {
		s += fmt.Sprintf(", %dx%d neighbourhoods", c.ZoneCols, c.ZoneRows)
	}
	if c.AreaCols > 0 {
		known := 0
		for _, a := range c.Areas {
			if a.Known() {
				known++
			}
		}
		s += fmt.Sprintf(", %dx%d blocks (%d described)", c.AreaCols, c.AreaRows, known)
	}
	return s
}

// UnexpectedShape reports differences from the geometry df-hud was written
// against. Not an error: a game update may legitimately extend the map, and a
// HUD that refuses to start because the city grew is worse than one that says
// so. Block Info degrades to coordinates on its own.
func (c *Catalog) UnexpectedShape() string {
	if c.ZoneCols == 0 && c.AreaCols == 0 {
		return "the feed carried no map grids at all"
	}
	if c.ZoneCols != expectedZoneCols || c.ZoneRows != expectedZoneRows ||
		c.AreaCols != expectedAreaCols || c.AreaRows != expectedAreaRows {
		return fmt.Sprintf("map geometry changed: neighbourhoods %dx%d (was %dx%d), blocks %dx%d (was %dx%d)",
			c.ZoneCols, c.ZoneRows, expectedZoneCols, expectedZoneRows,
			c.AreaCols, c.AreaRows, expectedAreaCols, expectedAreaRows)
	}
	return ""
}

// fetchCatalog downloads and parses the feed. GET, no credentials: this
// endpoint is public, which is the whole reason Block Info needs no login.
func fetchCatalog(ctx context.Context, hc *http.Client, feedURL, userAgent string) (*Catalog, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog: HTTP %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, catalogMaxBody))
	if err != nil {
		return nil, fmt.Errorf("catalog: reading body: %w", err)
	}
	body := string(raw)
	if looksLikeHTML(body) {
		return nil, errors.New("catalog: got an HTML page instead of the feed (Cloudflare or an outage)")
	}
	if len(raw) < catalogMinBody {
		return nil, fmt.Errorf("catalog: body is only %d bytes, far short of the ~1MB feed; "+
			"treating it as truncated rather than parsing a partial table", len(raw))
	}
	vars, err := parseFlash(body)
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	return parseCatalog(vars, time.Now())
}

// loadCatalogFile reads the disk cache. A missing file returns (nil, nil): that
// is a first run, not a failure. A corrupt or old-schema file is quarantined
// and also returns (nil, nil), so df-hud re-fetches instead of crash-looping -
// the same discipline as state.json.
func loadCatalogFile(path string) (*Catalog, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil || c.SchemaVersion != catalogSchemaVersion || len(c.ExpToReach) < 3 {
		aside := fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
		if renameErr := os.Rename(path, aside); renameErr != nil {
			return nil, fmt.Errorf("catalog cache unusable and could not be moved aside: %w", renameErr)
		}
		log.Printf("catalog: cache unusable, moved to %s, will re-fetch", aside)
		return nil, nil
	}
	c.finish()
	return &c, nil
}

func saveCatalogFile(path string, c *Catalog) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// ensureCatalog returns a usable catalog, in order of preference:
//
//  1. a cached copy younger than maxAge - no network at all
//  2. a fresh download, written to the cache
//  3. the stale cache, if the download failed
//
// Step 3 is the point. This data changes only when the game updates, so a
// week-old XP table is still correct, and an outage or a Cloudflare
// interstitial should not cost you the HUD.
func ensureCatalog(ctx context.Context, hc *http.Client, path, feedURL, userAgent string, maxAge time.Duration) (*Catalog, error) {
	cached, err := loadCatalogFile(path)
	if err != nil {
		log.Printf("catalog: %v", err) // not fatal; fall through to the network
	}
	if cached != nil && time.Since(cached.FetchedAt) < maxAge {
		return cached, nil
	}

	fresh, err := fetchCatalog(ctx, hc, feedURL, userAgent)
	if err != nil {
		if cached != nil {
			log.Printf("catalog: refresh failed (%v); using the cached copy from %s",
				err, cached.FetchedAt.Format(time.RFC3339))
			return cached, nil
		}
		return nil, err
	}
	if err := saveCatalogFile(path, fresh); err != nil {
		log.Printf("catalog: could not write the cache: %v", err) // usable anyway
	}
	return fresh, nil
}
