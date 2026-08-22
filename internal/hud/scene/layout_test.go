package scene

import (
	"df-hud/internal/config"
	"math"
	"testing"
)

func TestReferenceLayoutScalesAcrossCommonResolutions(t *testing.T) {
	cfg := config.Default()
	point := Point{X: 220, Y: 85}
	tests := []struct {
		name     string
		viewport Viewport
		wantX    float64
		wantY    float64
	}{
		{"1080p", Viewport{Width: 1920, Height: 1080, GameWidth: 1920, GameHeight: 1080}, 165, 63.75},
		{"1440p", Viewport{Width: 2560, Height: 1440, GameWidth: 2560, GameHeight: 1440}, 220, 85},
		{"4k 100 percent", Viewport{Width: 3840, Height: 2160, GameWidth: 3840, GameHeight: 2160}, 330, 127.5},
		// A 4K monitor at 150% DPI exposes a 2560x1440 logical viewport.
		{"4k 150 percent", Viewport{Width: 2560, Height: 1440, GameWidth: 3840, GameHeight: 2160}, 220, 85},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := newLayoutTransform(cfg, test.viewport).point(point)
			if math.Abs(got.X-test.wantX) > 0.01 || math.Abs(got.Y-test.wantY) > 0.01 {
				t.Errorf("point = %.2f,%.2f, want %.2f,%.2f",
					got.X, got.Y, test.wantX, test.wantY)
			}
		})
	}
}

func TestReferenceLayoutCentersInsideUltrawideGameContent(t *testing.T) {
	cfg := config.Default()
	transform := newLayoutTransform(cfg, Viewport{
		Width: 3440, Height: 1440, GameWidth: 2560, GameHeight: 1440,
	})
	got := transform.point(Point{X: 0, Y: 0})
	if got.X != 440 || got.Y != 0 {
		t.Errorf("reference origin = %.1f,%.1f, want centered at 440,0", got.X, got.Y)
	}
}
