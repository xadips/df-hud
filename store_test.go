package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

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
	snap := parseSnapshot(realPlayerRecord(), time.Now(), c)

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

	snap := parseSnapshot(vars, time.Now(), c)
	if snap.XPSource != xpSourceTable {
		t.Errorf("XPSource = %v, want the table reconstruction", snap.XPSource)
	}
	want, _ := c.CumulativeXP(200, 1000)
	if snap.CumulativeXP != want {
		t.Errorf("CumulativeXP = %d, want %d", snap.CumulativeXP, want)
	}

	// With neither source there is no rate to show, and that must be visible
	// rather than silently zero.
	snap = parseSnapshot(vars, time.Now(), nil)
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
	snap := parseSnapshot(realPlayerRecord(), time.Now(), c)

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

	snap := parseSnapshot(vars, time.Now(), c)
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
	snap := parseSnapshot(realPlayerRecord(), time.Now(), nil)
	if !snap.HasPosition || snap.PositionX != 1058 || snap.PositionY != 1019 {
		t.Errorf("position = (%d,%d) has=%v", snap.PositionX, snap.PositionY, snap.HasPosition)
	}
	if !snap.InOutpost {
		t.Error("df_inoutpost=1 should parse as true")
	}
	if got := outpostName(snap.PositionX, snap.PositionY); got != "Ground Zero" {
		t.Errorf("outpost = %q, want Ground Zero (the type-7 coordinates)", got)
	}
}

// "absent" and "zero" are different answers, and conflating them would make a
// missing danger level look like a safe block.
func TestParseSnapshotDistinguishesAbsentFromZero(t *testing.T) {
	vars := realPlayerRecord()
	snap := parseSnapshot(vars, time.Now(), nil)
	if !snap.HasDanger || snap.DangerLevel != 0 {
		t.Errorf("df_dangerlevel=0 should be present and zero, got has=%v level=%d",
			snap.HasDanger, snap.DangerLevel)
	}

	delete(vars, "df_dangerlevel")
	snap = parseSnapshot(vars, time.Now(), nil)
	if snap.HasDanger {
		t.Error("a missing df_dangerlevel must not report as danger level 0")
	}

	// Same for position: no coordinates is not the origin.
	delete(vars, "df_positionx")
	snap = parseSnapshot(vars, time.Now(), nil)
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
	snap := parseSnapshot(vars, time.Now(), nil)
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

func TestParseSnapshotDecodesGameTimestamps(t *testing.T) {
	// The game stores timestamps with a constant +1200000000 offset
	// (challenge.js:305).
	target := time.Now().Add(90 * time.Second).Truncate(time.Second)
	vars := map[string]string{
		"df_block_support_until": itoa64(target.Unix() - dfTimeOffset),
		"df_boostexpuntil":       "0",
	}
	snap := parseSnapshot(vars, time.Now(), nil)
	if !snap.BlockSupportUntil.Equal(target) {
		t.Errorf("BlockSupportUntil = %s, want %s", snap.BlockSupportUntil, target)
	}
	// Zero means unset, not 1970.
	if !snap.BoostExpUntil.IsZero() {
		t.Errorf("a zero timestamp must decode as unset, got %s", snap.BoostExpUntil)
	}
}

func TestStoreApplyTickAndDerive(t *testing.T) {
	c := loadFixtureCatalog(t)
	s := newStore(c)
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
	// 42m at the tick, plus the 3s we advanced to: the clock runs off the
	// process start time, not off when data last arrived.
	if v.SessionTime != 42*time.Minute+3*time.Second {
		t.Errorf("SessionTime = %s, want 42m3s from the process start time", v.SessionTime)
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
	s := newStore(nil)
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

	s := newStore(nil)
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
	s := newStore(nil)
	now := time.Now()
	vars := realPlayerRecord()
	// A block support that expired a minute ago.
	vars["df_block_support_until"] = itoa64(now.Add(-time.Minute).Unix() - dfTimeOffset)
	s.ApplyTick(Tick{At: now, Vars: vars, Scheduled: true})

	if got := s.Derive(now).BlockSupport; got != 0 {
		t.Errorf("expired block support = %s, want 0 rather than a negative countdown", got)
	}
}

func TestStoreSessionClockIsZeroWhenClosed(t *testing.T) {
	s := newStore(nil)
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
		if got := tradeZoneName(zone); got != want {
			t.Errorf("tradeZoneName(%d) = %q, want %q", zone, got, want)
		}
	}
	// An unknown zone renders as nothing rather than as "Unknown", so the HUD
	// omits the line instead of printing a placeholder.
	if got := tradeZoneName(99); got != "" {
		t.Errorf("tradeZoneName(99) = %q, want empty", got)
	}
	if got := tradeZoneShort(3); got != "NE" {
		t.Errorf("tradeZoneShort(3) = %q, want NE", got)
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
		if got := outpostName(tc.x, tc.y); got != tc.want {
			t.Errorf("outpostName(%d,%d) = %q, want %q", tc.x, tc.y, got, tc.want)
		}
	}
	if got := outpostName(1040, 1000); got != "" {
		t.Errorf("a city block should not be named an outpost, got %q", got)
	}
}
