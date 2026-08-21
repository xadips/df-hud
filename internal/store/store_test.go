package store

import (
	"bytes"
	internalcatalog "df-hud/internal/catalog"
	"df-hud/internal/citymap"
	internaldfclient "df-hud/internal/dfclient"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

func loadFixtureCatalog(t *testing.T) *Catalog {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "allstats.txt"))
	if err != nil {
		t.Fatal(err)
	}
	vars, err := internaldfclient.ParseFlash(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := internalcatalog.Parse(vars, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// realPlayerRecord is shaped like the captured player records: values as
// strings, the cap level, banked XP far past any threshold, and a real
// df_exptotal.
func realPlayerRecord() map[string]string {
	return map[string]string{
		"df_level":               "415",
		"df_exp":                 "10000",
		"df_exptotal":            "10000000",
		"df_freepoints":          "0",
		"df_positionx":           "1058",
		"df_positiony":           "1019",
		"df_positionz":           "0",
		"df_tradezone":           "21",
		"df_inoutpost":           "1",
		"df_dangerlevel":         "0",
		"df_block_support_until": "0",
		"df_hpcurrent":           "250",
		"df_hpmax":               "298",
		"df_cash":                "1000",
		"df_bankcash":            "50000",
		"df_hungerhp":            "185",
		"df_dead":                "0",
	}
}

func TestParseSnapshotPrefersExpTotal(t *testing.T) {
	c := loadFixtureCatalog(t)
	snap := ParseSnapshot(realPlayerRecord(), time.Now(), c)

	// Tier 1: the server's own counter wins over the table reconstruction,
	// because it needs no catalog and is authoritative.
	if snap.XPSource != xpSourceExpTotal {
		t.Errorf("XPSource = %v, want df_exptotal", snap.XPSource)
	}
	if snap.CumulativeXP != 10_000_000 {
		t.Errorf("CumulativeXP = %d, want df_exptotal verbatim", snap.CumulativeXP)
	}
}

func TestParseSnapshotFallsBackToTheTable(t *testing.T) {
	c := loadFixtureCatalog(t)
	vars := realPlayerRecord()
	delete(vars, "df_exptotal")
	vars["df_level"] = "200"
	vars["df_exp"] = "1000"

	snap := ParseSnapshot(vars, time.Now(), c)
	if snap.XPSource != xpSourceTable {
		t.Errorf("XPSource = %v, want the table reconstruction", snap.XPSource)
	}
	want, _ := c.CumulativeXP(200, 1000)
	if snap.CumulativeXP != want {
		t.Errorf("CumulativeXP = %d, want %d", snap.CumulativeXP, want)
	}

	// With neither source there is no rate to show, and that must be visible
	// rather than silently zero.
	snap = ParseSnapshot(vars, time.Now(), nil)
	if snap.XPSource != xpSourceNone || snap.CumulativeXP != 0 {
		t.Errorf("with no catalog and no df_exptotal: source %v, cumulative %d; want unavailable",
			snap.XPSource, snap.CumulativeXP)
	}
}

// At the cap, df_exp keeps growing forever - the captured account has billions
// against a 176M top threshold - so nothing may try to render progress toward a
// level that cannot exist.
func TestParseSnapshotAtTheLevelCap(t *testing.T) {
	c := loadFixtureCatalog(t)
	snap := ParseSnapshot(realPlayerRecord(), time.Now(), c)

	if snap.Level != 415 {
		t.Fatalf("Level = %d", snap.Level)
	}
	if snap.ExpNeeded != 0 {
		t.Errorf("ExpNeeded = %d at the cap, want 0 (there is no exp_lvl416)", snap.ExpNeeded)
	}
	if snap.PendingLevels != 0 {
		t.Errorf("PendingLevels = %d at the cap, want 0", snap.PendingLevels)
	}
	if snap.ExpInLevel <= 176_000_000 {
		t.Fatal("this fixture should have XP past the largest threshold, or it tests nothing")
	}
}

// The mid-run case: XP banks up without levelling, so PendingLevels reports what
// you will gain on the way back to an outpost. Nothing in the game shows this.
func TestParseSnapshotPendingLevels(t *testing.T) {
	c := loadFixtureCatalog(t)
	vars := realPlayerRecord()
	delete(vars, "df_exptotal")
	vars["df_level"] = "200"

	needed, _ := c.ExpNeeded(200)
	vars["df_exp"] = itoa64(needed * 40) // a long, good run

	snap := ParseSnapshot(vars, time.Now(), c)
	if snap.PendingLevels < 5 {
		t.Errorf("PendingLevels = %d, want many levels banked", snap.PendingLevels)
	}
	// Cross-check against the same replay the catalog test does.
	level, exp := 200, needed*40
	want := 0
	for {
		need, ok := c.ExpNeeded(level)
		if !ok || exp < need {
			break
		}
		exp -= need
		level++
		want++
	}
	if snap.PendingLevels != want {
		t.Errorf("PendingLevels = %d, want %d", snap.PendingLevels, want)
	}
}

func TestParseSnapshotPositionsAndPlace(t *testing.T) {
	snap := ParseSnapshot(realPlayerRecord(), time.Now(), nil)
	if !snap.HasPosition || snap.PositionX != 1058 || snap.PositionY != 1019 {
		t.Errorf("position = (%d,%d) has=%v", snap.PositionX, snap.PositionY, snap.HasPosition)
	}
	if !snap.InOutpost {
		t.Error("df_inoutpost=1 should parse as true")
	}
	if got := citymap.OutpostName(snap.PositionX, snap.PositionY); got != "Ground Zero" {
		t.Errorf("outpost = %q, want Ground Zero (the type-7 coordinates)", got)
	}
}

// "absent" and "zero" are different answers, and conflating them would make a
// missing danger level look like a safe block.
func TestParseSnapshotDistinguishesAbsentFromZero(t *testing.T) {
	vars := realPlayerRecord()
	snap := ParseSnapshot(vars, time.Now(), nil)
	if !snap.HasDanger || snap.DangerLevel != 0 {
		t.Errorf("df_dangerlevel=0 should be present and zero, got has=%v level=%d",
			snap.HasDanger, snap.DangerLevel)
	}

	delete(vars, "df_dangerlevel")
	snap = ParseSnapshot(vars, time.Now(), nil)
	if snap.HasDanger {
		t.Error("a missing df_dangerlevel must not report as danger level 0")
	}

	// Same for position: no coordinates is not the origin.
	delete(vars, "df_positionx")
	snap = ParseSnapshot(vars, time.Now(), nil)
	if snap.HasPosition {
		t.Error("a missing df_positionx must not report a position")
	}
}

func TestParseSnapshotSurvivesGarbage(t *testing.T) {
	// A HUD that renders four of five widgets beats one that renders none.
	vars := map[string]string{
		"df_level":       "not a number",
		"df_exp":         "",
		"df_positionx":   "1058",
		"df_positiony":   "abc",
		"df_dangerlevel": "3",
		"df_cash":        "1000",
	}
	snap := ParseSnapshot(vars, time.Now(), nil)
	if snap.Level != 0 || snap.ExpInLevel != 0 {
		t.Error("unparseable numbers should stay at zero")
	}
	if snap.HasPosition {
		t.Error("a half-parseable position must not be reported")
	}
	if snap.Cash != 1000 || snap.DangerLevel != 3 {
		t.Error("the parseable fields should still be there")
	}
}

// The game uses two different time encodings, and applying the wrong one to an
// expiry field is worth 38 years. This came from live data: a permanent XP boost
// was rendering as expiring in 49 years.
func TestParseSnapshotDecodesGameTimestamps(t *testing.T) {
	// A fixed instant with no sub-second part, so the arithmetic below is exact
	// rather than losing up to a second to truncation.
	now := time.Unix(1786484051, 0)

	// Expiry fields are plain unix seconds - settled by the game's own
	// arithmetic, boostexpuntil - (servertime + 1200000000).
	target := now.Add(90 * time.Second)
	snap := ParseSnapshot(map[string]string{
		"df_boostexpuntil": itoa64(target.Unix()),
	}, now, nil)
	if !snap.BoostExp.At.Equal(target) {
		t.Errorf("BoostExp.At = %s, want %s", snap.BoostExp.At, target)
	}
	if snap.BoostExp.Forever {
		t.Error("a real deadline must not report Forever")
	}
	if got := snap.BoostExp.Remaining(now); got != 90*time.Second {
		t.Errorf("Remaining = %s, want 90s", got)
	}

	// df_servertime uses the compact epoch, unix minus 1.2e9. The live server
	// returned 586484051 for a unix time of 1786484051.
	snap = ParseSnapshot(map[string]string{"df_servertime": "586484051"}, now, nil)
	if got := snap.ServerTime.Unix(); got != 1786484051 {
		t.Errorf("ServerTime = %d, want 1786484051 (value + 1200000000)", got)
	}

	// Zero means unset, not 1970.
	snap = ParseSnapshot(map[string]string{"df_boostexpuntil": "0"}, now, nil)
	if snap.BoostExp.Set() {
		t.Errorf("a zero timestamp must decode as unset, got %+v", snap.BoostExp)
	}
}

// 2147483647 is what the live server actually returns for a permanent boost:
// int32 max, the classic end of 32-bit time. It must be a state, not a
// 13-year countdown.
func TestParseSnapshotForeverSentinel(t *testing.T) {
	now := time.Unix(1786484051, 0)
	for _, raw := range []string{"2147483647", "2147483646"} {
		snap := ParseSnapshot(map[string]string{"df_boostexpuntil": raw}, now, nil)
		if !snap.BoostExp.Forever {
			t.Errorf("df_boostexpuntil=%s must decode as Forever", raw)
		}
		if got := snap.BoostExp.Remaining(now); got != 0 {
			t.Errorf("Forever should have no countdown, got %s", got)
		}
		if !snap.BoostExp.Set() {
			t.Error("Forever is set, not absent")
		}
	}
}

// df_block_support_until was zero in every capture, so its encoding is
// unverified. If it is actually the compact epoch, the plausibility guard makes
// the HUD omit the line rather than confidently display a decades-wrong
// countdown.
func TestParseSnapshotRejectsImplausibleDeadlines(t *testing.T) {
	now := time.Unix(1786484051, 0)
	for name, raw := range map[string]string{
		"compact epoch mistaken for unix": "586484051",
		"long past":                       "1000000000",
		"far future but not the sentinel": itoa64(now.AddDate(5, 0, 0).Unix()),
	} {
		snap := ParseSnapshot(map[string]string{"df_block_support_until": raw}, now, nil)
		if snap.BlockSupport.Set() {
			t.Errorf("%s (%s) should be treated as not-a-deadline, got %+v", name, raw, snap.BlockSupport)
		}
	}
}

func TestStoreApplyTickAndDerive(t *testing.T) {
	c := loadFixtureCatalog(t)
	s := New(c)
	now := time.Now()

	// Before anything arrives, the view must say why it is empty.
	v := s.Derive(now)
	if v.HaveData {
		t.Error("no data should be reported as no data")
	}
	if v.Status == "" {
		t.Error("an empty HUD must explain itself")
	}

	if !s.ApplyTick(Tick{At: now, Vars: realPlayerRecord(), Scheduled: true}) {
		t.Fatal("a good tick should apply")
	}
	s.SetCredentialsAt(now)
	s.SetGame(GameState{Running: true, PID: 1, StartedAt: now.Add(-42 * time.Minute)})

	v = s.Derive(now.Add(3 * time.Second))
	if !v.HaveData {
		t.Fatal("HaveData should be true after a tick")
	}
	if v.DataAge != 3*time.Second {
		t.Errorf("DataAge = %s, want 3s", v.DataAge)
	}
	// 42m at the tick, plus the 3s we advanced to: the client's uptime runs off
	// the process start time, not off when data last arrived.
	if v.ClientUptime != 42*time.Minute+3*time.Second {
		t.Errorf("ClientUptime = %s, want 42m3s from the process start time", v.ClientUptime)
	}
	// The fixture is a record taken in an outpost, so there is no run to time -
	// which is the whole point of the session clock no longer being the client's
	// uptime.
	if v.HasSession {
		t.Errorf("HasSession = true in an outpost, SessionTime = %s", v.SessionTime)
	}
	if v.Level != 415 || v.CumulativeXP != 10_000_000 {
		t.Errorf("level/XP = %d/%d", v.Level, v.CumulativeXP)
	}
	if v.OutpostName != "Ground Zero" || v.ZoneName != "Outpost" {
		t.Errorf("place = %q / %q, want Ground Zero / Outpost", v.OutpostName, v.ZoneName)
	}
	if v.Status != "" {
		t.Errorf("Status = %q, want empty when everything is fine", v.Status)
	}
}

func TestStoreCountsMissedTicks(t *testing.T) {
	s := New(nil)
	now := time.Now()
	s.SetCredentialsAt(now)
	s.ApplyTick(Tick{At: now, Vars: realPlayerRecord(), Scheduled: true})

	for i := 1; i <= 3; i++ {
		if s.ApplyTick(Tick{At: now, Err: errors.New("boom"), Scheduled: true}) {
			t.Fatal("a failed tick must not report a new snapshot")
		}
		if got := s.Derive(now).MissedTicks; got != i {
			t.Errorf("MissedTicks = %d, want %d", got, i)
		}
	}
	// A one-off poll is not a scheduled tick, so it must not count as a miss.
	s.ApplyTick(Tick{At: now, Err: errors.New("boom"), Scheduled: false})
	if got := s.Derive(now).MissedTicks; got != 3 {
		t.Errorf("MissedTicks = %d after an unscheduled failure, want 3", got)
	}
	// A success clears the run.
	s.ApplyTick(Tick{At: now, Vars: realPlayerRecord(), Scheduled: true})
	if got := s.Derive(now).MissedTicks; got != 0 {
		t.Errorf("MissedTicks = %d after a success, want 0", got)
	}
	// The stale snapshot survives failures rather than blanking the HUD.
	if v := s.Derive(now); !v.HaveData {
		t.Error("data should persist across failed polls")
	}
}

// The status line exists to answer "why is nothing updating?", so the most
// actionable cause has to win.
func TestStoreStatusPriority(t *testing.T) {
	now := time.Now()

	s := New(nil)
	if got := s.Derive(now).Status; !strings.Contains(got, "bridge") {
		t.Errorf("with no credentials: %q, want the bridge prompt", got)
	}

	s.SetCredentialsAt(now)
	s.SetPollerStatus(PollerStatus{Failures: 2})
	if got := s.Derive(now).Status; !strings.Contains(got, "not responding") {
		t.Errorf("with failures: %q", got)
	}

	// Stale credentials outrank everything: it is the only one the user can fix.
	s.SetPollerStatus(PollerStatus{Stale: true, Paused: true, PauseReason: "whatever", Failures: 9})
	v := s.Derive(now)
	if !strings.Contains(v.Status, "session expired") {
		t.Errorf("stale credentials should win: %q", v.Status)
	}
	if !v.StatusIsFix {
		t.Error("a fixable status should be marked as such")
	}
}

func TestStoreDeriveCountdownsNeverGoNegative(t *testing.T) {
	s := New(nil)
	now := time.Now()
	vars := realPlayerRecord()
	// A block support that expired a minute ago.
	vars["df_block_support_until"] = itoa64(now.Add(-time.Minute).Unix() - dfTimeOffset)
	s.ApplyTick(Tick{At: now, Vars: vars, Scheduled: true})

	if got := s.Derive(now).BlockSupport; got != 0 {
		t.Errorf("expired block support = %s, want 0 rather than a negative countdown", got)
	}
}

// Every way a run can end says how long it lasted, in the journal.
//
// The clock leaving the HUD is otherwise the last you hear of the run, and quitting
// the game - which is how most runs end - was the path that cleared it silently.
func TestStoreLogsHowLongEachRunLasted(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(s *Store, now time.Time)
		want string
	}{
		{"closing the game", func(s *Store, now time.Time) {
			s.SetGame(GameState{Running: false})
		}, "the game closed"},
		{"relaunching it", func(s *Store, now time.Time) {
			s.SetGame(GameState{Running: true, PID: 99, StartedAt: now})
		}, "the game relaunched"},
		{"dying", func(s *Store, now time.Time) {
			vars := realPlayerRecord()
			vars["df_inoutpost"], vars["df_dead"] = "0", "1"
			s.ApplyTick(Tick{At: now.Add(time.Minute), Vars: vars, Scheduled: true})
		}, "you died"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logged bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logged)
			defer log.SetOutput(old)

			s := New(loadFixtureCatalog(t))
			// In the PAST, because two of these paths are triggered by the game
			// watcher rather than by a poll and so are timed to the real clock: a run
			// that started in the future ends after a negative duration, which
			// endRunLocked declines to claim.
			now := time.Now().Add(-5 * time.Minute)
			s.SetGame(GameState{Running: true, PID: 1, StartedAt: now.Add(-time.Hour)})

			// A run has to be going before it can end. Move one block to start the
			// poll fallback before applying the terminal event under test.
			inCity := func(x string) map[string]string {
				vars := realPlayerRecord()
				vars["df_inoutpost"], vars["df_positionx"] = "0", x
				return vars
			}
			s.ApplyTick(Tick{At: now, Vars: inCity("1040"), Scheduled: true})
			s.ApplyTick(Tick{At: now.Add(30 * time.Second), Vars: inCity("1042"), Scheduled: true})

			logged.Reset()
			tc.end(s, now.Add(30*time.Second))

			out := logged.String()
			if !strings.Contains(out, "run ended after") {
				t.Errorf("%s left no record of the run: %q", tc.name, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("the reason should say %q: %q", tc.want, out)
			}
		})
	}
}

func TestStoreSessionClockIsZeroWhenClosed(t *testing.T) {
	s := New(nil)
	now := time.Now()
	s.SetGame(GameState{Running: false, PID: 5, StartedAt: now.Add(-time.Hour)})
	v := s.Derive(now)
	if v.GameRunning {
		t.Error("GameRunning should be false")
	}
	if v.SessionTime != 0 {
		t.Errorf("SessionTime = %s with the game closed, want 0", v.SessionTime)
	}
}

func TestTradeZoneNames(t *testing.T) {
	for zone, want := range map[int]string{
		1: "North Western", 5: "Central", 9: "South Eastern",
		10: "Wastelands", 21: "Outpost", 22: "Valcrest",
	} {
		if got := citymap.TradeZoneName(zone); got != want {
			t.Errorf("citymap.TradeZoneName(%d) = %q, want %q", zone, got, want)
		}
	}
	// An unknown zone renders as nothing rather than as "Unknown", so the HUD
	// omits the line instead of printing a placeholder.
	if got := citymap.TradeZoneName(99); got != "" {
		t.Errorf("citymap.TradeZoneName(99) = %q, want empty", got)
	}
	if got := citymap.TradeZoneShort(3); got != "NE" {
		t.Errorf("citymap.TradeZoneShort(3) = %q, want NE", got)
	}
}

func TestOutpostNames(t *testing.T) {
	// All seven, straight from initOutpost's coordinate table.
	for _, tc := range []struct {
		x, y int
		want string
	}{
		{1000, 1000, "Nastya's Holdout"},
		{1005, 985, "Dogg's Stockade"},
		{1012, 1019, "Precinct 13"},
		{1029, 1003, "Fort Pastor"},
		{1054, 987, "Secronom Bunker"},
		{1032, 985, "Valcrest"},
		{1058, 1019, "Ground Zero"},
	} {
		if got := citymap.OutpostName(tc.x, tc.y); got != tc.want {
			t.Errorf("citymap.OutpostName(%d,%d) = %q, want %q", tc.x, tc.y, got, tc.want)
		}
	}
	if got := citymap.OutpostName(1040, 1000); got != "" {
		t.Errorf("a city block should not be named an outpost, got %q", got)
	}
}
