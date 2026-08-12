package main

import (
	"bytes"
	"image/png"
	"strings"
	"testing"
	"time"
)

func TestTrayTooltip(t *testing.T) {
	visible := hudVisibility{Visible: true}

	// Nothing yet: the tray is up before the first poll, and "starting" is a
	// better answer than an empty tooltip.
	if got := trayTooltip(nil, visible); !strings.Contains(got, "starting") {
		t.Errorf("tooltip = %q", got)
	}

	if got := trayTooltip(&View{}, visible); !strings.Contains(got, "not running") {
		t.Errorf("tooltip = %q, want the closed game named", got)
	}

	playing := &View{
		GameRunning: true, HasSession: true, SessionTime: 12*time.Minute + 34*time.Second,
		HaveData: true, XPAvailable: true, XPPerHour: 19_500_000,
	}
	got := trayTooltip(playing, visible)
	if !strings.Contains(got, "in the city 0:12:34") || !strings.Contains(got, "xp 19,500,000/hr") {
		t.Errorf("tooltip = %q, want the run clock and the rate", got)
	}
	// One fact per line: a single line grows into a stripe across the screen and
	// nothing in it stands out.
	if lines := strings.Split(got, "\n"); len(lines) != 2 {
		t.Errorf("tooltip lines = %q, want one fact per line", lines)
	} else if !strings.HasPrefix(lines[0], "df-hud: in the city") {
		t.Errorf("first line = %q, want the primary state on its own", lines[0])
	}

	// The rate window survives a restart, so a rate with no snapshot behind it is
	// hours old and must not be shown as current.
	stale := &View{GameRunning: true, XPAvailable: true, XPPerHour: 1234}
	if strings.Contains(trayTooltip(stale, visible), "xp ") {
		t.Error("a rate with no current data behind it must not be shown")
	}

	// The client is up but no run has started - the launcher, the loading
	// screen, or an outpost. This is the whole explanation for a HUD with no
	// clock on it, so the tooltip has to carry it.
	loading := &View{GameRunning: true, ClientUptime: 3 * time.Minute}
	got = trayTooltip(loading, visible)
	if !strings.Contains(got, "client up 0:03:00") || !strings.Contains(got, "not in the city") {
		t.Errorf("tooltip = %q, want the client uptime and why there is no clock", got)
	}
}

func TestTrayTooltipExplainsAHiddenOverlay(t *testing.T) {
	hidden := hudVisibility{Visible: false, Reason: "the game is on workspace 7, which is not the one being shown"}
	playing := &View{GameRunning: true, HasSession: true, SessionTime: time.Minute}

	if got := trayTooltip(playing, hidden); !strings.Contains(got, "workspace 7") {
		t.Errorf("tooltip = %q, want the reason the overlay is not on screen", got)
	}
	// With the game closed the reason is already the first thing on the line, so
	// repeating it would just be noise.
	closed := trayTooltip(&View{}, hudVisibility{Visible: false, Reason: "the game is not running"})
	if strings.Count(closed, "not running") != 1 {
		t.Errorf("tooltip = %q, want the closed game said once", closed)
	}
}

func TestTrayTooltipCarriesTheStatus(t *testing.T) {
	v := &View{GameRunning: true, Status: "session expired - open any Dead Frontier page to refresh"}
	if got := trayTooltip(v, hudVisibility{Visible: true}); !strings.Contains(got, "session expired") {
		t.Errorf("tooltip = %q, want the status; with the HUD hidden this is the only place it shows", got)
	}
}

func TestTrayIconActiveFor(t *testing.T) {
	if trayIconActiveFor(nil) {
		t.Error("no view yet is not the active state")
	}
	if trayIconActiveFor(&View{}) {
		t.Error("the game is not running")
	}
	if !trayIconActiveFor(&View{GameRunning: true}) {
		t.Error("the game running is what the colour means")
	}
}

// The icon is generated rather than shipped as a binary, so a test that it
// decodes and has the intended shape is what stands in for looking at the file.
func TestTrayIconPNG(t *testing.T) {
	for _, size := range []int{16, 64} {
		data := trayIconPNG(trayIconActive, size)
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if b := img.Bounds(); b.Dx() != size || b.Dy() != size {
			t.Errorf("size %d: bounds = %v", size, b)
		}
	}

	img, err := png.Decode(bytes.NewReader(trayIconPNG(trayIconActive, 64)))
	if err != nil {
		t.Fatal(err)
	}
	// The centre dot is opaque, the gap between it and the ring is not, and the
	// ring is there. That is the reticle.
	if _, _, _, a := img.At(32, 32).RGBA(); a == 0 {
		t.Error("the centre dot should be drawn")
	}
	if _, _, _, a := img.At(32+13, 32).RGBA(); a != 0 {
		t.Error("the gap between the dot and the ring should be transparent")
	}
	if _, _, _, a := img.At(32+22, 32).RGBA(); a == 0 {
		t.Error("the ring should be drawn")
	}
	// A corner must stay transparent, or the icon is a coloured square in the bar.
	if _, _, _, a := img.At(1, 1).RGBA(); a != 0 {
		t.Error("the corners must be transparent")
	}
}

func TestTrayIconSizeFloor(t *testing.T) {
	// A nonsense size must still produce a decodable icon rather than an
	// zero-dimension image that a tray host may or may not survive.
	img, err := png.Decode(bytes.NewReader(trayIconPNG(trayIconIdle, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() < 8 {
		t.Errorf("bounds = %v, want the floor applied", img.Bounds())
	}
}
