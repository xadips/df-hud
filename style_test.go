package main

import (
	"strings"
	"testing"
)

// Only what is actually set gets a rule. A rule emitted for every group would be
// six rules overriding [hud] with the same values it already has - harmless until
// someone changes hud.font_size and finds it does nothing.
func TestWidgetStyleCSSOnlyEmitsWhatIsSet(t *testing.T) {
	bare := defaultConfig()
	bare.Widget.Block.Color = ""
	bare.Widget.Session.Color = ""
	bare.Widget.XP.Color = ""
	bare.Widget.Challenges.Color = ""
	if got := widgetStyleCSS(bare); got != "" {
		t.Errorf("widgetStyleCSS = %q, want nothing when no group overrides anything", got)
	}

	// The defaults DO set two colours - white for the clock, off-white for the
	// board - and those must reach the sheet.
	got := widgetStyleCSS(defaultConfig())
	if !strings.Contains(got, ".group-session") || !strings.Contains(got, "#ffffff") {
		t.Errorf("the default clock colour never reached the sheet:\n%s", got)
	}
	if !strings.Contains(got, ".group-challenges") {
		t.Errorf("the default board colour never reached the sheet:\n%s", got)
	}
	// And the groups that set nothing still contribute nothing. Bosses and the
	// status banner are the two without a default colour: the banner has no key at
	// all, and the boss rows carry their own amber from the state classes.
	if strings.Contains(got, ".group-bosses") || strings.Contains(got, ".group-status") {
		t.Errorf("a group with no overrides emitted a rule:\n%s", got)
	}
}

func TestWidgetStyleCSSOverrides(t *testing.T) {
	cfg := defaultConfig()
	cfg.Widget.XP.FontSize = 22
	cfg.Widget.Challenges.FontFamily = "Iosevka"

	got := widgetStyleCSS(cfg)

	// Both selectors, because a group's root is a bare label for the rate and a box
	// of labels for the board. Only the pair covers both shapes.
	for _, want := range []string{
		".group-xp, .group-xp label {",
		"font-size: 22.0pt;",
		".group-challenges, .group-challenges label {",
		"font-family: Iosevka;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("widgetStyleCSS is missing %q:\n%s", want, got)
		}
	}
	// A group that overrides only the size must not also pin the family, or it
	// would stop following hud.font_family.
	xp := got[strings.Index(got, ".group-xp"):]
	if end := strings.Index(xp, "}"); end > 0 && strings.Contains(xp[:end], "font-family") {
		t.Errorf("the xp rule should set only the size:\n%s", xp[:end])
	}
	// Untouched groups contribute nothing. Bosses sets neither key by default, which
	// is why it is the one checked here.
	if strings.Contains(got, ".group-bosses") {
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

// Per-group colour, and the precedence that makes it safe.
//
// A configured colour is the group's NORMAL colour; the state colours have to keep
// winning, because they say what the text cannot - that a rate is averaged over a
// window with a hole in it, or that there are bandits where you are standing.
//
// This is settled by specificity rather than by which rule is appended last: a
// group override is ".group-x label" and a state rule is "window label.threat",
// which outranks it by one element. Without the window scope the two would tie and
// the later one would win, silently taking the amber off a bandit pack.
func TestStyleSheetColourPrecedence(t *testing.T) {
	cfg := defaultConfig()
	cfg.Widget.Block.Color = "#88ccff"
	cfg.Widget.XP.Color = "#ffffff"

	sheet := styleSheet(cfg)
	if !strings.Contains(sheet, "color: #88ccff;") || !strings.Contains(sheet, "color: #ffffff;") {
		t.Fatalf("the configured colours never reached the sheet:\n%s", sheet)
	}

	// Every state rule must be window-scoped, or a group colour would beat it.
	for _, state := range []string{"status", "fixable", "shaky", "unstable", "threat"} {
		scoped := "window label." + state
		if !strings.Contains(sheet, scoped) {
			t.Errorf("state rule for .%s is not window-scoped, so a per-group colour would win it:\n%s",
				state, sheet)
		}
		// And the unscoped form must not appear, which is what would happen if
		// someone "tidied" the selector back.
		for _, line := range strings.Split(sheet, "\n") {
			if strings.HasPrefix(line, "label."+state) {
				t.Errorf("found an unscoped state rule %q; it would tie with a group colour", line)
			}
		}
	}
}

// The status banner has no colour key at all: its red and amber ARE the message.
// A key that was accepted and then silently outranked would be worse than not
// offering one.
func TestStatusGroupHasNoColourKey(t *testing.T) {
	for _, g := range groupStyles(defaultConfig()) {
		if g.name == "status" && g.color != "" {
			t.Errorf("the status group should carry no colour, got %q", g.color)
		}
	}
	// It still takes placement and font, which is the whole reason it is a group.
	cfg := defaultConfig()
	cfg.Widget.Status.FontSize = 30
	if !strings.Contains(widgetStyleCSS(cfg), ".group-status") {
		t.Error("the status banner must still accept a font override")
	}
}
