package main

import (
	"strings"
	"testing"
)

// The default config sets no per-group font, so it must generate no CSS at all.
// A rule emitted for every group would be five rules overriding [hud] with the
// same values it already has - harmless until someone changes hud.font_size and
// finds it does nothing.
func TestWidgetFontCSSIsEmptyByDefault(t *testing.T) {
	if got := widgetFontCSS(defaultConfig()); got != "" {
		t.Errorf("widgetFontCSS = %q, want nothing when no group overrides anything", got)
	}
}

func TestWidgetFontCSSOverrides(t *testing.T) {
	cfg := defaultConfig()
	cfg.Widget.XP.FontSize = 22
	cfg.Widget.Challenges.FontFamily = "Iosevka"

	got := widgetFontCSS(cfg)

	// Both selectors, because a group's root is a bare label for the rate and a box
	// of labels for the board. Only the pair covers both shapes.
	for _, want := range []string{
		".group-xp, .group-xp label {",
		"font-size: 22.0pt;",
		".group-challenges, .group-challenges label {",
		"font-family: Iosevka;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("widgetFontCSS is missing %q:\n%s", want, got)
		}
	}
	// A group that overrides only the size must not also pin the family, or it
	// would stop following hud.font_family.
	xp := got[strings.Index(got, ".group-xp"):]
	if end := strings.Index(xp, "}"); end > 0 && strings.Contains(xp[:end], "font-family") {
		t.Errorf("the xp rule should set only the size:\n%s", xp[:end])
	}
	// Untouched groups contribute nothing.
	if strings.Contains(got, ".group-session") {
		t.Errorf("a group with no overrides must emit no rule:\n%s", got)
	}
}

// The overrides come after the base sheet, because in CSS the later rule wins
// among equals. Reversed, a group's font_size would be silently ignored.
func TestStyleSheetPutsOverridesLast(t *testing.T) {
	cfg := defaultConfig()
	cfg.Widget.XP.FontSize = 22

	sheet := styleSheet(cfg)
	base := strings.Index(sheet, "text-shadow")
	override := strings.Index(sheet, ".group-xp")
	if base < 0 || override < 0 {
		t.Fatalf("sheet is missing the base rules or the override:\n%s", sheet)
	}
	if override < base {
		t.Error("the per-group overrides must come after the base sheet, or they lose")
	}
	// The configured colour and font reach the sheet at all - this is the whole
	// path from a TOML key to a rendered label.
	if !strings.Contains(sheet, cfg.HUD.TextColor) {
		t.Errorf("hud.text_color never reached the sheet:\n%s", sheet)
	}
}

// Group classes must not collide with the state classes widgets put on individual
// labels (status, fixable, threat, urgent, shaky, unstable). A group named
// "status" would otherwise silently restyle the error banner's colour rule.
func TestGroupClassIsNamespaced(t *testing.T) {
	if got := groupClass("status"); got == "status" {
		t.Error("groupClass must not collide with the state class of the same name")
	}
	if got := groupClass("xp"); got != "group-xp" {
		t.Errorf("groupClass(xp) = %q", got)
	}
}
