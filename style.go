package main

import (
	"fmt"
	"strings"
)

// Where the HUD's appearance is decided, kept out of ui.go so it is testable and
// so it still compiles under -tags nolayershell. Nothing here touches GTK: it
// generates CSS text from the config, and GTK's only involvement is loading it.

// hudCSS is the base stylesheet. The window is made fully transparent because a
// layer surface is alpha-capable and the theme's background would otherwise
// render as a dark box over the game. The text then has to carry its own
// contrast, since it can sit over anything from bright pavement to a dark
// interior: a layered text-shadow outline is what the game's own HUD does, and it
// stays legible on both without a backing panel.
const hudCSS = `
window, window.background {
  background-color: transparent;
  background-image: none;
  box-shadow: none;
}
label {
  color: %s;
  font-family: %s;
  font-size: %.1fpt;
  font-weight: bold;
  text-shadow: 0 0 4px #000, 1px 1px 0 #000, -1px -1px 0 #000;
}
label.status {
  color: #ff6b6b;
}
label.fixable {
  color: #ffd166;
}
/* Rate stability. Amber for one missed poll, red for two or more: the number
   still looks authoritative when the window has a hole in it, so the colour is
   how the HUD says so. */
label.shaky {
  color: #ffd166;
}
label.unstable {
  color: #ff6b6b;
}
/* What is on your block. Amber because it is a warning rather than a failure;
   an outpost attack is map-wide and gets the red. */
label.threat {
  color: #ffd166;
}
label.threat.urgent {
  color: #ff6b6b;
}
`

// groupClass is the CSS class carrying one group's font overrides. Prefixed so a
// group can never collide with the state classes (status, threat, shaky) that
// widgets add to individual labels.
func groupClass(name string) string { return "group-" + name }

// widgetFontCSS is the per-group font overrides, appended to the base sheet.
//
// Two selectors per group because a group's root is sometimes a bare label
// (the clock, the rate) and sometimes a box of them (block info, the board).
// `.group-x` catches the first, `.group-x label` the second, and both outrank the
// plain `label` rule in the base sheet - a class beats a type selector, which is
// what makes an override take without !important anywhere.
//
// A group with neither key set contributes nothing at all, so the common case
// generates no CSS and inherits [hud] exactly as before.
func widgetFontCSS(cfg *Config) string {
	groups := []struct {
		name  string
		place Placement
	}{
		{"status", cfg.Widget.Status.Placement},
		{"block", cfg.Widget.Block.Placement},
		{"session", cfg.Widget.Session.Placement},
		{"xp", cfg.Widget.XP.Placement},
		{"challenges", cfg.Widget.Challenges.Placement},
	}
	var b strings.Builder
	for _, g := range groups {
		var decls string
		if g.place.FontFamily != "" {
			decls += fmt.Sprintf("  font-family: %s;\n", g.place.FontFamily)
		}
		if g.place.FontSize > 0 {
			decls += fmt.Sprintf("  font-size: %.1fpt;\n", g.place.FontSize)
		}
		if decls == "" {
			continue
		}
		class := groupClass(g.name)
		fmt.Fprintf(&b, ".%s, .%s label {\n%s}\n", class, class, decls)
	}
	return b.String()
}

// widgetSignature is everything the widget tree is built from. Comparing it means
// a reload that only changed a poll interval does not tear down and rebuild every
// label for nothing.
func widgetSignature(cfg *Config) string { return fmt.Sprintf("%+v", cfg.Widget) }

// styleSheet is the base sheet with the per-group overrides after it, in that
// order so the overrides win.
func styleSheet(cfg *Config) string {
	return fmt.Sprintf(hudCSS, cfg.HUD.TextColor, cfg.HUD.FontFamily, cfg.HUD.FontSize) +
		widgetFontCSS(cfg)
}
