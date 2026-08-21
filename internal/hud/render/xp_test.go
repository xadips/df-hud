package render

import (
	"strings"
	"testing"

	"df-hud/internal/model"
)

func TestXPLine(t *testing.T) {
	cfg := defaultConfig().Widget.XP
	view := &View{
		HaveData: true, XPAvailable: true, XPPerHour: 1_234_567,
		XPStability: model.XPShaky,
	}
	text, class, show := XPLine(view, cfg)
	if !show || text != "Xp/Hr: 1,234,567" || class != "shaky" {
		t.Fatalf("XPLine = %q, %q, %v", text, class, show)
	}

	provisional := &View{
		HaveData: true, XPAvailable: true, XPProvisional: true,
		XPPerHour: 12_000_000, XPStability: model.XPUnstable,
	}
	text, class, show = XPLine(provisional, cfg)
	if !show || text != "Xp/Hr: ~12,000,000" || class != "" {
		t.Fatalf("provisional XPLine = %q, %q, %v", text, class, show)
	}

	text, class, show = XPLine(&View{HaveData: true, XPWhy: "collecting samples"}, cfg)
	if !show || text != "Xp/Hr: --" || class != "" || strings.Contains(text, "collecting") {
		t.Fatalf("pending XPLine = %q, %q, %v", text, class, show)
	}
	if _, _, show := XPLine(&View{}, cfg); show {
		t.Fatal("XP line appeared without data")
	}
}
