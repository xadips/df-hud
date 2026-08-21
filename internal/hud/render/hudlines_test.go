package render

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

// The default is the region as the head and NO coordinates, because the game prints
// them under its own minimap and two identical readouts an inch apart is not
// information. The region it shows nowhere.
func TestBlockLinesInTheCity(t *testing.T) {
	head, sub, show := blockLines(viewForBlock(false), BlockWidgetConfig{})
	if !show {
		t.Fatal("a known position should render")
	}
	if head != "South Eastern" {
		t.Errorf("head = %q, want the region when the coordinates are off", head)
	}
	// And not twice: with the region promoted to the head it must leave the row
	// below rather than appearing on both.
	if strings.Contains(sub, "South Eastern") {
		t.Errorf("sub = %q, want the region only once", sub)
	}
	if strings.Contains(head+sub, "1058") {
		t.Errorf("%q / %q, want no coordinates by default", head, sub)
	}

	// With them on, the coordinates head the group and the region drops below.
	head, sub, _ = blockLines(viewForBlock(false), BlockWidgetConfig{ShowPosition: true})
	if head != "1058, 1016" {
		t.Errorf("head = %q, want the coordinates", head)
	}
	if !strings.Contains(sub, "South Eastern") {
		t.Errorf("sub = %q, want the region", sub)
	}
	// df_dangerlevel is in the record but not on the HUD: the game's own client
	// never renders it, so there is no way to know whether 0 means safe or
	// unmeasured, and an uninterpretable number on every row is just noise.
	if strings.Contains(sub, "danger") {
		t.Errorf("sub = %q, want no danger level until its scale is known", sub)
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
	_, sub, _ = blockLines(v, BlockWidgetConfig{ShowPosition: true})
	if sub != "1058, 1019" {
		t.Errorf("sub with show_position = %q", sub)
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
	cfg := defaultConfig().Widget.Session
	text, show := sessionLine(&View{GameRunning: true, HasSession: true, SessionTime: 90 * time.Minute}, cfg)
	// The prefix is not decoration: a bare clock on an overlay over a game with
	// its own clocks says nothing about what it is timing.
	if !show || text != "IC Time: 1:30:00" {
		t.Errorf("sessionLine = %q, %v", text, show)
	}
	// A frozen clock reads as a broken one, so the row disappears instead.
	if _, show := sessionLine(&View{GameRunning: false, HasSession: true, SessionTime: time.Hour}, cfg); show {
		t.Error("the session row must be hidden when the game is closed")
	}
	// The client being up is not a session. Between pressing Launch and pressing
	// Start there is nothing to time, and a clock counting the loading screen is
	// the bug this replaced.
	if _, show := sessionLine(&View{GameRunning: true, ClientUptime: 5 * time.Minute}, cfg); show {
		t.Error("the session row must be hidden until a run has actually started")
	}
	// An empty prefix is honoured rather than defaulted, so the label can be
	// turned off without turning the clock off.
	text, _ = sessionLine(&View{GameRunning: true, HasSession: true, SessionTime: time.Minute},
		SessionWidgetConfig{})
	if text != "0:01:00" {
		t.Errorf("with no prefix, sessionLine = %q", text)
	}
}

// -print-hud reads groups down the screen and then across, so what it prints
// tracks where things actually are rather than a sort key that no longer exists.
func TestHudLinesOrdersByPositionAndLeadsWithStatus(t *testing.T) {
	cfg := defaultConfig()
	v := viewForBlock(false)
	v.GameRunning = true
	v.HasSession = true
	v.SessionTime = time.Hour

	lines := hudLines(v, cfg)
	// The defaults stack them clock (y=60), rate (y=85), block info (y=300). The
	// rate is dashes here because this view carries no samples, and it holds its
	// row rather than vanishing. Block info is one row, since the coordinates are
	// off by default and the region takes the head.
	if len(lines) != 3 {
		t.Fatalf("lines = %v, want the clock, the rate and block info", lines)
	}
	if lines[0] != "IC Time: 1:00:00" || lines[1] != "Xp/Hr: --" || lines[2] != "South Eastern" {
		t.Errorf("lines = %v, want them ordered down the screen", lines)
	}

	// Moving a group down the screen moves it down the printed list.
	cfg.Widget.Session.Y = 900
	lines = hudLines(v, cfg)
	if lines[len(lines)-1] != "IC Time: 1:00:00" {
		t.Errorf("after moving the clock to y=900, lines = %v; it should be last", lines)
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
	cfg.Widget.XP.Enabled = false
	lines = hudLines(v, cfg)
	if len(lines) != 1 || lines[0] != v.Status {
		t.Errorf("with every widget off, only the status should render, got %v", lines)
	}
}
