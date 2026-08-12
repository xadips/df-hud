package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fixture is a real capture of get_allstats.php?printvars=1, so these tests
// pin df-hud against the actual feed rather than against my idea of it.
func loadFixtureCatalog(t *testing.T) *Catalog {
	t.Helper()
	raw, err := os.ReadFile("testdata/allstats.txt")
	if err != nil {
		t.Fatal(err)
	}
	vars, err := parseFlash(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	c, err := parseCatalog(vars, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCatalogParsesTheRealFeed(t *testing.T) {
	c := loadFixtureCatalog(t)

	if c.MaxLevel != 415 {
		t.Errorf("MaxLevel = %d, want 415 (the cap in checkIfLevelUp)", c.MaxLevel)
	}
	// Both ends of the XP table, hardcoded from the feed.
	if got := c.ExpToReach[2]; got != 125 {
		t.Errorf("exp_lvl2 = %d, want 125", got)
	}
	if got := c.ExpToReach[415]; got != 176_000_000 {
		t.Errorf("exp_lvl415 = %d, want 176000000", got)
	}
	// 2..415 inclusive is 414 thresholds; the parse would have failed on a hole,
	// so a full slice plus unused [0] and [1] is the shape to expect.
	if len(c.ExpToReach) != 416 {
		t.Errorf("ExpToReach length = %d, want 416 (index = level)", len(c.ExpToReach))
	}

}

// TestCumulativeXPIsContinuousAcrossLevelUp is the property XP/hr depends on.
// df_exp resets to the carry-over at every level-up (checkIfLevelUp writes
// currentExp - neededExp), so a delta on df_exp alone goes negative once per
// level. The cumulative reconstruction must instead advance by exactly the XP
// gained - here, one point.
func TestCumulativeXPIsContinuousAcrossLevelUp(t *testing.T) {
	c := loadFixtureCatalog(t)

	for _, level := range []int{1, 2, 5, 50, 200, 414} {
		needed, ok := c.ExpNeeded(level)
		if !ok {
			t.Fatalf("no threshold for level %d", level)
		}
		before, ok1 := c.CumulativeXP(level, needed-1) // one XP short
		after, ok2 := c.CumulativeXP(level+1, 0)       // just levelled, carry-over 0
		if !ok1 || !ok2 {
			t.Fatalf("CumulativeXP unavailable around level %d", level)
		}
		if after-before != 1 {
			t.Errorf("level %d -> %d: cumulative moved by %d, want exactly 1",
				level, level+1, after-before)
		}
	}

	// Strictly increasing across the whole table, which is what makes a
	// negative delta a reliable death signal rather than an artefact.
	prev := int64(-1)
	for level := 1; level <= c.MaxLevel; level++ {
		got, ok := c.CumulativeXP(level, 0)
		if !ok {
			t.Fatalf("CumulativeXP(%d) unavailable", level)
		}
		if got <= prev && level > 1 {
			t.Fatalf("cumulative XP at level %d is %d, not above the previous %d", level, got, prev)
		}
		prev = got
	}
	// Level 1 starts at zero, so "XP since level 1" means what it says.
	if got, _ := c.CumulativeXP(1, 0); got != 0 {
		t.Errorf("CumulativeXP(1, 0) = %d, want 0", got)
	}
	// The first threshold is reachable and matches the feed.
	if got, _ := c.CumulativeXP(2, 0); got != 125 {
		t.Errorf("CumulativeXP(2, 0) = %d, want 125", got)
	}
}

// TestCumulativeXPSurvivesAMultiLevelJump is the case that actually happens.
// The client never levels you up: XP piles into df_exp for a whole run, so it
// can overshoot the threshold enormously (300M banked against 7M needed), and
// returning to an outpost then cashes it in as many levels at once.
//
// The reconstruction has to be indifferent to that, and it is, by construction:
// whatever the prefix sum gains from the level jump, df_exp loses. This test
// pins it, because "20 levels at once" is exactly the shape of input that would
// expose an off-by-one in the prefix sum.
func TestCumulativeXPSurvivesAMultiLevelJump(t *testing.T) {
	c := loadFixtureCatalog(t)

	const startLevel = 200
	// Bank far more than the next threshold - enough for many levels.
	needed, _ := c.ExpNeeded(startLevel)
	banked := needed * 40

	before, ok := c.CumulativeXP(startLevel, banked)
	if !ok {
		t.Fatal("CumulativeXP unavailable")
	}

	// Replay what the website does on the way back to an outpost: one level at
	// a time, carrying the remainder over (base.js:475).
	level, exp := startLevel, banked
	for {
		need, ok := c.ExpNeeded(level)
		if !ok || exp < need {
			break
		}
		exp -= need
		level++
	}
	if level == startLevel {
		t.Fatal("the test input did not cross a single level boundary")
	}
	t.Logf("banked %d XP at level %d -> level %d with %d left over (%d levels at once)",
		banked, startLevel, level, exp, level-startLevel)

	after, ok := c.CumulativeXP(level, exp)
	if !ok {
		t.Fatal("CumulativeXP unavailable after the jump")
	}
	if after != before {
		t.Errorf("cumulative XP changed by %d across a %d-level jump; it must be unchanged, "+
			"since no XP was earned while cashing the levels in", after-before, level-startLevel)
	}
}

func TestExpNeededAtTheCap(t *testing.T) {
	c := loadFixtureCatalog(t)
	if _, ok := c.ExpNeeded(415); ok {
		t.Error("level 415 is the cap and must report no further threshold")
	}
	if _, ok := c.CumulativeXP(416, 0); ok {
		t.Error("a level past the cap must not resolve")
	}
	if _, ok := c.CumulativeXP(0, 0); ok {
		t.Error("level 0 must not resolve")
	}
}

func TestParseCatalogRejectsAHoleInTheXPTable(t *testing.T) {
	// A missing threshold would silently shift every cumulative total past it,
	// producing an XP rate that is wrong but plausible. That must be an error.
	vars := map[string]string{"exp_lvl2": "125", "exp_lvl3": "300", "exp_lvl5": "900"}
	_, err := parseCatalog(vars, time.Now())
	if err == nil {
		t.Fatal("a gap in the XP table must be rejected")
	}
	if !strings.Contains(err.Error(), "exp_lvl4") {
		t.Errorf("the error should name the missing level, got: %v", err)
	}
}

func TestParseCatalogRejectsBadInput(t *testing.T) {
	if _, err := parseCatalog(map[string]string{"exp_lvl2": "lots"}, time.Now()); err == nil {
		t.Error("a non-numeric XP threshold must be rejected")
	}
	if _, err := parseCatalog(map[string]string{"exp_lvl2": "-5"}, time.Now()); err == nil {
		t.Error("a negative XP threshold must be rejected")
	}
	if _, err := parseCatalog(map[string]string{"ammo1_name": "9mm"}, time.Now()); err == nil {
		t.Error("a feed with no XP table at all must be rejected")
	}
}

// The feed carries far more than the XP table - two map grids among the rest -
// and everything df-hud does not use must pass through without complaint.
func TestParseCatalogIgnoresTheRestOfTheFeed(t *testing.T) {
	vars := map[string]string{
		"exp_lvl2":             "125",
		"areas_1_1_type":       "street",
		"areas_1_1_flamingoes": "7",
		"zones_1_1_name":       "Staleston",
		"ammo1_name":           "9mm",
	}
	c, err := parseCatalog(vars, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := c.CumulativeXP(2, 10); got != 135 {
		t.Errorf("the XP table must parse whatever else is in the feed, got %d", got)
	}
}

func TestCatalogDiskRoundTrip(t *testing.T) {
	c := loadFixtureCatalog(t)
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := saveCatalogFile(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := loadCatalogFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("the saved catalog should load back")
	}
	if got.MaxLevel != c.MaxLevel {
		t.Errorf("round trip changed the shape: %s vs %s", got.Summary(), c.Summary())
	}
	// cumExp is derived and not persisted, so finish() must run on load. If it
	// does not, every XP reading silently reads zero.
	want, _ := c.CumulativeXP(100, 500)
	after, ok := got.CumulativeXP(100, 500)
	if !ok || after != want {
		t.Errorf("CumulativeXP after reload = %d (ok=%v), want %d: finish() was not called on load",
			after, ok, want)
	}
}

func TestLoadCatalogFileHandlesMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()

	// Missing is a first run, not an error.
	c, err := loadCatalogFile(filepath.Join(dir, "absent.json"))
	if c != nil || err != nil {
		t.Errorf("missing cache = (%v, %v), want (nil, nil)", c, err)
	}

	// Corrupt is quarantined so df-hud re-fetches instead of crash-looping.
	path := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = loadCatalogFile(path)
	if c != nil || err != nil {
		t.Errorf("corrupt cache = (%v, %v), want (nil, nil) after quarantine", c, err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("the corrupt file should have been moved aside")
	}
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) != 1 {
		t.Errorf("expected exactly one quarantined file, found %d", len(matches))
	}

	// An old schema version is treated the same way.
	if err := os.WriteFile(path, []byte(`{"schema_version":0,"exp_to_reach":[0,0,125]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := loadCatalogFile(path); c != nil || err != nil {
		t.Errorf("old-schema cache = (%v, %v), want (nil, nil)", c, err)
	}
}

// fakeFeed serves the fixture and counts requests, so "did this hit the
// network?" is a fact rather than a guess.
func fakeFeed(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	body, err := os.ReadFile("testdata/allstats.txt")
	if err != nil {
		t.Fatal(err)
	}
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestFetchCatalog(t *testing.T) {
	srv, hits := fakeFeed(t)
	c, err := fetchCatalog(context.Background(), srv.Client(), srv.URL, "df-hud-test")
	if err != nil {
		t.Fatal(err)
	}
	if *hits != 1 {
		t.Errorf("hits = %d, want 1", *hits)
	}
	if c.MaxLevel != 415 {
		t.Errorf("MaxLevel = %d", c.MaxLevel)
	}
}

func TestFetchCatalogRejectsJunk(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		// Cloudflare or a login redirect handed to us as if it were data.
		"html": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("<!DOCTYPE html><html><title>Just a moment...</title>"))
		},
		// A truncated body would otherwise parse into a half XP table.
		"truncated": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("&exp_lvl2=125&exp_lvl3=300"))
		},
		"server error": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", http.StatusBadGateway)
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			if _, err := fetchCatalog(context.Background(), srv.Client(), srv.URL, "df-hud-test"); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestEnsureCatalogUsesFreshCacheWithoutNetwork(t *testing.T) {
	srv, hits := fakeFeed(t)
	path := filepath.Join(t.TempDir(), "catalog.json")

	first, err := ensureCatalog(context.Background(), srv.Client(), path, srv.URL, "df-hud-test", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if *hits != 1 {
		t.Fatalf("hits = %d, want 1 on a cold start", *hits)
	}
	second, err := ensureCatalog(context.Background(), srv.Client(), path, srv.URL, "df-hud-test", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if *hits != 1 {
		t.Errorf("hits = %d: a fresh cache must not touch the network", *hits)
	}
	if second.MaxLevel != first.MaxLevel {
		t.Error("the cached catalog should match the fetched one")
	}
}

// The rule that matters: this data changes only when the game updates, so a
// stale copy beats no HUD.
func TestEnsureCatalogFallsBackToStaleCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	stale := loadFixtureCatalog(t)
	stale.FetchedAt = time.Now().Add(-90 * 24 * time.Hour)
	if err := saveCatalogFile(path, stale); err != nil {
		t.Fatal(err)
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer down.Close()

	c, err := ensureCatalog(context.Background(), down.Client(), path, down.URL, "df-hud-test", time.Hour)
	if err != nil {
		t.Fatalf("a stale cache should be used when the refresh fails: %v", err)
	}
	if c == nil || c.MaxLevel != 415 {
		t.Error("the stale catalog should have been returned intact")
	}
	// And with no cache at all, the failure is real and must surface.
	empty := filepath.Join(t.TempDir(), "catalog.json")
	if _, err := ensureCatalog(context.Background(), down.Client(), empty, down.URL, "df-hud-test", time.Hour); err == nil {
		t.Error("with no cache and no network, ensureCatalog must fail rather than return an empty catalog")
	}
}

func TestEnsureCatalogRefreshesWhenStale(t *testing.T) {
	srv, hits := fakeFeed(t)
	path := filepath.Join(t.TempDir(), "catalog.json")
	stale := loadFixtureCatalog(t)
	stale.FetchedAt = time.Now().Add(-48 * time.Hour)
	if err := saveCatalogFile(path, stale); err != nil {
		t.Fatal(err)
	}

	c, err := ensureCatalog(context.Background(), srv.Client(), path, srv.URL, "df-hud-test", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if *hits != 1 {
		t.Errorf("hits = %d, want 1: a stale cache must trigger a refresh", *hits)
	}
	if time.Since(c.FetchedAt) > time.Minute {
		t.Errorf("FetchedAt = %s, want the refresh time", c.FetchedAt)
	}
	// And the refresh must have been written back, or every start re-downloads.
	reloaded, err := loadCatalogFile(path)
	if err != nil || reloaded == nil {
		t.Fatalf("cache reload: %v", err)
	}
	if time.Since(reloaded.FetchedAt) > time.Minute {
		t.Error("the refreshed catalog was not written to the cache")
	}
}
