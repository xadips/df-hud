package main

import (
	"strings"
	"testing"
	"time"
)

// These test what the HUD actually draws: the widgets are thin wrappers over
// these functions, so there is no second copy of the logic to drift.

func viewForBlock(inOutpost bool) *View {
	return &View{
		Now:         time.Now(),
		HaveData:    true,
		HasPosition: true,
		PositionX:   1058,
		PositionY:   1016,
		TradeZone:   9,
		ZoneName:    "South Eastern",
		InOutpost:   inOutpost,
		HasDanger:   true,
		DangerLevel: 3,
	}
}

func TestBlockLinesInTheCity(t *testing.T) {
	head, sub, show := blockLines(viewForBlock(false), BlockWidgetConfig{})
	if !show {
		t.Fatal("a known position should render")
	}
	if head != "1058, 1016" {
		t.Errorf("head = %q, want the coordinates", head)
	}
	if !strings.Contains(sub, "South Eastern") || !strings.Contains(sub, "danger 3") {
		t.Errorf("sub = %q, want the region and danger", sub)
	}
}

func TestBlockLinesInAnOutpost(t *testing.T) {
	v := viewForBlock(true)
	v.PositionY = 1019 // Ground Zero
	v.OutpostName = "Ground Zero"
	v.ZoneName = "Outpost"

	head, sub, show := blockLines(v, BlockWidgetConfig{})
	if !show || head != "Ground Zero" {
		t.Errorf("head = %q, want the outpost name", head)
	}
	// Inside an outpost the region and danger are noise, so the sub-line is
	// empty rather than padded.
	if sub != "" {
		t.Errorf("sub = %q, want empty inside an outpost", sub)
	}

	// Unless coordinates were asked for, which is what the calibration flow needs.
	_, sub, _ = blockLines(v, BlockWidgetConfig{ShowCoords: true})
	if sub != "1058, 1019" {
		t.Errorf("sub with show_coords = %q", sub)
	}
}

// An outpost whose coordinates are not in the table means the table has gone out
// of date; say the honest thing rather than printing raw numbers as if they were
// a name.
func TestBlockLinesUnknownOutpost(t *testing.T) {
	v := viewForBlock(true)
	v.OutpostName = ""
	head, _, show := blockLines(v, BlockWidgetConfig{})
	if !show || head != "Outpost" {
		t.Errorf("head = %q, want \"Outpost\"", head)
	}
}

func TestBlockLinesHiddenWithoutData(t *testing.T) {
	if _, _, show := blockLines(&View{}, BlockWidgetConfig{}); show {
		t.Error("no data should not render a row")
	}
	if _, _, show := blockLines(&View{HaveData: true}, BlockWidgetConfig{}); show {
		t.Error("no position should not render a row")
	}
}

func TestBlockLinesOmitsWhatItDoesNotKnow(t *testing.T) {
	v := viewForBlock(false)
	v.ZoneName = ""
	v.HasDanger = false
	_, sub, show := blockLines(v, BlockWidgetConfig{})
	if !show {
		t.Fatal("the position alone is still worth a row")
	}
	if sub != "" {
		t.Errorf("sub = %q, want empty rather than placeholders", sub)
	}
}

func TestBlockLinesShowsBlockSupport(t *testing.T) {
	v := viewForBlock(false)
	v.BlockSupport = 4*time.Minute + 30*time.Second
	_, sub, _ := blockLines(v, BlockWidgetConfig{})
	if !strings.Contains(sub, "support 4m30s") {
		t.Errorf("sub = %q, want the support countdown", sub)
	}
}

func TestSessionLine(t *testing.T) {
	text, show := sessionLine(&View{GameRunning: true, SessionTime: 90 * time.Minute})
	if !show || text != "1:30:00" {
		t.Errorf("sessionLine = %q, %v", text, show)
	}
	// A frozen clock reads as a broken one, so the row disappears instead.
	if _, show := sessionLine(&View{GameRunning: false, SessionTime: time.Hour}); show {
		t.Error("the session row must be hidden when the game is closed")
	}
}

func TestHudLinesRespectsOrderAndStatus(t *testing.T) {
	cfg := defaultConfig()
	v := viewForBlock(false)
	v.GameRunning = true
	v.SessionTime = time.Hour

	lines := hudLines(v, cfg)
	// Defaults put block (10) above session (20).
	if len(lines) != 3 {
		t.Fatalf("lines = %v, want block head, block sub, session", lines)
	}
	if lines[0] != "1058, 1016" || lines[2] != "1:00:00" {
		t.Errorf("lines = %v", lines)
	}

	// Reordering the config reorders the HUD.
	cfg.Widget.Session.Order = 5
	lines = hudLines(v, cfg)
	if lines[0] != "1:00:00" {
		t.Errorf("after reordering, lines = %v; session should be first", lines)
	}

	// A status always leads, because it explains everything below it.
	v.Status = "session expired - open any Dead Frontier page to refresh"
	lines = hudLines(v, cfg)
	if lines[0] != v.Status {
		t.Errorf("status should be the first line, got %v", lines)
	}

	// A disabled widget produces nothing.
	cfg.Widget.Block.Enabled = false
	cfg.Widget.Session.Enabled = false
	lines = hudLines(v, cfg)
	if len(lines) != 1 || lines[0] != v.Status {
		t.Errorf("with every widget off, only the status should render, got %v", lines)
	}
}
