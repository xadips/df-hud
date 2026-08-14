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
/* Every rule below is a STATE colour: it says something the text cannot, and it
   has to survive a per-group colour being configured over the top of it.

   Hence the redundant-looking "window" on each one. A group override is
   ".group-x label", which has exactly the same CSS specificity as "label.threat"
   and comes later in the sheet, so it would win the tie and silently take the
   amber off a bandit pack. Scoping to the window outranks it by one element
   instead, which settles the question by specificity rather than by which rule
   happens to be appended last. */
window label.status {
  color: #ff6b6b;
}
window label.fixable {
  color: #ffd166;
}
/* Rate stability. Amber for one missed poll, red for two or more: the number
   still looks authoritative when the window has a hole in it, so the colour is
   how the HUD says so. */
window label.shaky {
  color: #ffd166;
}
window label.unstable {
  color: #ff6b6b;
}
/* The challenge board. Green for finished, so a glance down the column separates
   what is left from what is not; the alarm red for unfinished work whose deadline
   is inside widget.challenges.urgent_within, where the row has stopped being
   information and become a decision. */
window label.done {
  color: #9be564;
}
window label.expiring {
  color: #ff6b6b;
}
/* A few pixels above every challenge except the first, so a board of nineteen rows
   reads as groups rather than as one column. A margin rather than a blank row: a
   whole row of nothing per challenge would cost more height than the board has. */
window label.board-gap {
  margin-top: 6px;
}
/* The map's key gets a TIGHT outline instead of the glow above.
   The 4px blur is right for a handful of large readings over a moving game, and
   wrong for a dozen close-set rows: every letter's halo lands on its neighbours and
   the whole block turns into a smear with a glow around it. The 1px offsets still
   carry it over a bright scene. */
.group-map label {
  text-shadow: 1px 1px 0 #000, -1px 0 0 #000, 0 -1px 0 #000;
}
/* What is on your block. Amber because it is a warning rather than a failure;
   an outpost attack is map-wide and gets the red. */
window label.threat {
  color: #ffd166;
}
window label.threat.urgent {
  color: #ff6b6b;
}
`

// groupClass is the CSS class carrying one group's font and colour overrides. Prefixed so a
// group can never collide with the state classes (status, threat, shaky) that
// widgets add to individual labels.
func groupClass(name string) string { return "group-" + name }

// groupStyle is one group's appearance, gathered from wherever it lives in the
// config so the generator below reads as a list rather than as five special cases.
//
// Colour is separate from Placement because the status banner does not have one:
// its red and amber ARE the message, so there is deliberately no key to override
// them with. Better an asymmetry that means something than a key that is accepted
// and then ignored.
type groupStyle struct {
	name  string
	place Placement
	color string

	// fontPt is a size the group works out for ITSELF, used when font_size did not
	// pin one. Only the map has one: its key is sized from the blocks beside it, so
	// that widget.map.scale moves the grid and the key together instead of scaling
	// the picture and leaving the writing at 12pt. See mapListPt.
	fontPt float64
}

func groupStyles(cfg *Config) []groupStyle {
	return []groupStyle{
		{name: "status", place: cfg.Widget.Status.Placement},
		{name: "block", place: cfg.Widget.Block.Placement, color: cfg.Widget.Block.Color},
		{name: "bosses", place: cfg.Widget.Bosses.Placement, color: cfg.Widget.Bosses.Color},
		{name: "session", place: cfg.Widget.Session.Placement, color: cfg.Widget.Session.Color},
		{name: "xp", place: cfg.Widget.XP.Placement, color: cfg.Widget.XP.Color},
		{name: "challenges", place: cfg.Widget.Challenges.Placement, color: cfg.Widget.Challenges.Color},
		{name: "map", place: cfg.Widget.Map.Placement, color: cfg.Widget.Map.Color,
			fontPt: mapListPt(cfg.Widget.Map)},
	}
}

// widgetStyleCSS is the per-group font and colour overrides, appended to the base
// sheet.
//
// Two selectors per group because a group's root is sometimes a bare label
// (the clock, the rate) and sometimes a box of them (block info, the board).
// `.group-x` catches the first, `.group-x label` the second, and both outrank the
// plain `label` rule in the base sheet - a class beats a type selector, which is
// what makes an override take without !important anywhere.
//
// A group that sets none of the keys contributes nothing at all, so the common
// case generates no CSS and inherits [hud] exactly as before.
//
// A configured colour is the group's NORMAL colour. The state colours in the base
// sheet still win, because those carry information the colour cannot: a rate that
// has stopped being reliable, an outpost attack, a block with bandits on it. See
// the note in hudCSS for how that is enforced.
func widgetStyleCSS(cfg *Config) string {
	var b strings.Builder
	for _, g := range groupStyles(cfg) {
		var decls string
		if g.place.FontFamily != "" {
			decls += fmt.Sprintf("  font-family: %s;\n", g.place.FontFamily)
		}
		// A pinned font_size wins over a derived one, which is the whole point of
		// setting it: mapListPt returns 0 precisely so that it does.
		if pt := g.place.FontSize; pt > 0 {
			decls += fmt.Sprintf("  font-size: %.1fpt;\n", pt)
		} else if g.fontPt > 0 {
			decls += fmt.Sprintf("  font-size: %.1fpt;\n", g.fontPt)
		}
		if g.color != "" {
			decls += fmt.Sprintf("  color: %s;\n", g.color)
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
		widgetStyleCSS(cfg)
}
