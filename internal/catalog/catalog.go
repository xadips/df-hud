package catalog

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

	"df-hud/internal/dfclient"
)

// The public game data. get_allstats.php?printvars=1 is unauthenticated, about
// 1MB, and changes only when the game updates, so it is fetched at most once a
// day and cached on disk. Same feed df-allstats-watcher already polls.
//
// One thing in it matters to df-hud:
//
//   - exp_lvl<N>: the XP that must accumulate WITHIN level N-1 to reach level
//     N. Not an assumption - the game's own checkIfLevelUp (base.js:448-478)
//     compares df_exp against exp_lvl[level+1] and carries the remainder over
//     as the new df_exp on level-up. That is exactly what makes a cumulative
//     XP counter reconstructable, and therefore what makes XP/hr immune to the
//     level-up reset in df_exp.
//
// The feed also carries two map grids - zones_<x>_<y>_name, 13x13 neighbourhood
// names, and areas_<x>_<y>_*, a 39x39 grid of streets and buildings - and both
// used to be parsed and cached here against the day the transform from
// df_positionx/y onto them was solved. They are gone, because they are not the
// city df-hud draws: the city is 59x55 with 1716 blocks in it, 39x39 is 1521
// cells, and no landmark exists on both sides to pin them together. Nothing in
// the saved web client references either key. See knowledge/city-map.md for
// where the map actually comes from.
const catalogSchemaVersion = 1

const (
	// catalogMaxBody caps the download. The real body is ~1MB; 8MB is room for
	// growth while still refusing to buffer something pathological.
	catalogMaxBody = 8 << 20
	// catalogMinBody rejects a truncated response before it becomes a half
	// catalog with a plausible-looking XP table.
	catalogMinBody = 64 << 10
	catalogMax     = 10000 // sanity bound on level numbers
)

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

	// cumExp[n] is the total XP earned since level 1 by the moment you reach
	// level n, i.e. the prefix sum of ExpToReach. Derived, not persisted:
	// storing it would double the file to save a loop over 415 integers.
	cumExp []int64
}

var expKeyRe = regexp.MustCompile(`^exp_lvl(\d+)$`)

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

	c.finish()
	return c, nil
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

// Summary is one log line at startup, and the fastest way to notice that a
// game update changed the feed's shape.
func (c *Catalog) Summary() string {
	return fmt.Sprintf("levels 2..%d", c.MaxLevel)
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
	if dfclient.LooksLikeHTML(body) {
		return nil, errors.New("catalog: got an HTML page instead of the feed (Cloudflare or an outage)")
	}
	if len(raw) < catalogMinBody {
		return nil, fmt.Errorf("catalog: body is only %d bytes, far short of the ~1MB feed; "+
			"treating it as truncated rather than parsing a partial table", len(raw))
	}
	vars, err := dfclient.ParseFlash(body)
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

// Parse builds a catalog from an allstats variable map.
func Parse(vars map[string]string, fetchedAt time.Time) (*Catalog, error) {
	return parseCatalog(vars, fetchedAt)
}

// Ensure returns a fresh-enough catalog, with stale-cache fallback.
func Ensure(ctx context.Context, hc *http.Client, path, feedURL, userAgent string, maxAge time.Duration) (*Catalog, error) {
	return ensureCatalog(ctx, hc, path, feedURL, userAgent, maxAge)
}

// parseFlash remains package-local for the focused tests and delegates to the
// protocol package that owns the wire format.
func parseFlash(body string) (map[string]string, error) { return dfclient.ParseFlash(body) }
