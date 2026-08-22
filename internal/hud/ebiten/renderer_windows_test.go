//go:build windows && !nolayershell

package ebitenhud

import (
	"df-hud/internal/config"
	"df-hud/internal/hud/scene"
	"df-hud/internal/model"
	"image/color"
	"os"
	"testing"

	"github.com/hajimehoshi/ebiten/v2"
)

func TestParseColorHexNamedAndOpacity(t *testing.T) {
	tests := []struct {
		value   string
		opacity float64
		want    color.NRGBA
	}{
		{"#abc", 1, color.NRGBA{R: 0xaa, G: 0xbb, B: 0xcc, A: 0xff}},
		{"#11223380", 0.5, color.NRGBA{R: 0x11, G: 0x22, B: 0x33, A: 0x40}},
		{"red", 0.25, color.NRGBA{R: 0xff, A: 0x40}},
	}
	for _, test := range tests {
		if got := parseColor(test.value, test.opacity); got != test.want {
			t.Errorf("parseColor(%q, %v) = %#v, want %#v",
				test.value, test.opacity, got, test.want)
		}
	}
}

func TestFirstFontFamily(t *testing.T) {
	if got := firstFontFamily(`"Courier New", monospace`); got != "Courier New" {
		t.Errorf("firstFontFamily = %q", got)
	}
}

func TestResolveStandardWindowsFont(t *testing.T) {
	path, err := windowsFontPath("Courier New", true)
	if err != nil {
		t.Fatalf("resolve Courier New Bold: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("resolved font %s: %v", path, err)
	}
}

func TestMonitorSelectionUsesVisibilityForAuto(t *testing.T) {
	monitors := []desktopMonitor{
		{name: `\\.\DISPLAY1`, primary: true},
		{name: `\\.\DISPLAY2`},
	}
	cfg := config.Default()
	cfg.HUD.Monitor = "auto"
	got, ok := initialMonitor(monitors, cfg, model.Visibility{Monitor: `\\.\DISPLAY2`})
	if !ok || got.name != `\\.\DISPLAY2` {
		t.Fatalf("initialMonitor = %+v, %v", got, ok)
	}

	game := &game{
		target:   monitors[1],
		monitors: monitors,
		warned:   map[string]bool{},
	}
	if got := game.selectMonitor(cfg, model.Visibility{}); got.name != `\\.\DISPLAY2` {
		t.Fatalf("monitor gap moved away from last game target: %+v", got)
	}
}

func TestMonitorWindowSizeKeepsTransparencyInset(t *testing.T) {
	if width, height := monitorWindowSize(desktopMonitor{width: 2560, height: 1440}); width != 2558 || height != 1438 {
		t.Errorf("monitorWindowSize = %dx%d, want 2558x1438", width, height)
	}
	if width, height := monitorWindowSize(desktopMonitor{width: 3840, height: 2160, dpi: 144}); width != 2558 || height != 1438 {
		t.Errorf("4K at 150%% monitorWindowSize = %dx%d, want logical 2558x1438",
			width, height)
	}
}

func TestDesktopMonitorIndexMatchesEbitenginePrimaryFirstOrder(t *testing.T) {
	monitors := []desktopMonitor{
		{name: `\\.\DISPLAY2`},
		{name: `\\.\DISPLAY1`, primary: true},
	}
	if got := desktopMonitorIndex(monitors, monitors[1]); got != 0 {
		t.Errorf("primary monitor index = %d, want 0", got)
	}
	if got := desktopMonitorIndex(monitors, monitors[0]); got != 1 {
		t.Errorf("secondary monitor index = %d, want 1", got)
	}
}

func TestRendererAcceptsCompleteSceneOffscreen(t *testing.T) {
	drawer, err := newRenderer()
	if err != nil {
		t.Fatal(err)
	}
	image := ebiten.NewImage(100, 100)
	drawer.draw(image, scene.Scene{
		TextGroups: []scene.Group{{
			Position: scene.Point{X: 2, Y: 2},
			Rows: []scene.Row{{Runs: []scene.TextRun{{
				Text: "HUD",
				Style: scene.TextStyle{
					FontFamily: "monospace", FontSize: 12,
					Weight: scene.WeightBold, Color: "#ffffff", Opacity: 1,
				},
			}}}},
		}},
		Map: &scene.MapGroup{
			Position: scene.Point{X: 40, Y: 40},
			Cells: []scene.MapCell{{
				Bounds: scene.Rect{Width: 20, Height: 20},
				Fill:   "#ff0000", Alpha: 1,
			}},
		},
	})
	if got := image.Bounds(); got.Dx() != 100 || got.Dy() != 100 {
		t.Errorf("offscreen bounds = %v", got)
	}
}
