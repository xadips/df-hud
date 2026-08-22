// Package scene converts presentation-ready HUD data into backend-neutral draw
// commands. It contains no GTK, Pango, Ebitengine, or platform dependencies.
package scene

import (
	"df-hud/internal/config"
	"df-hud/internal/hud/render"
	"df-hud/internal/model"
)

const (
	colorAlarm          = "#ff6b6b"
	colorWarning        = "#ffd166"
	colorDone           = "#9be564"
	colorOnslaughtPrev  = "#b5b5b5"
	colorOnslaughtNow   = "#ff4d4d"
	colorOnslaughtNext  = "#4fc3ff"
	colorOnslaughtEmpty = "#6f6f6f"
	colorOnslaughtLabel = "#8a8a8a"
	colorWhite          = "#ffffff"
	colorShadow         = "#000000"
)

// Weight is the requested font weight for one text run.
type Weight uint8

const (
	WeightNormal Weight = iota
	WeightBold
)

// Point is a logical-pixel coordinate in the HUD's monitor-sized viewport.
type Point struct {
	X float64
	Y float64
}

// Shadow is one text-shadow pass.
type Shadow struct {
	Offset Point
	Blur   float64
	Color  string
}

// TextStyle is fully resolved from the global and per-group configuration.
type TextStyle struct {
	FontFamily    string
	FontSize      float64
	Weight        Weight
	Color         string
	Background    string
	Opacity       float64
	StrikeThrough bool
	LetterSpacing float64
	Shadows       []Shadow
}

// TextRun is a contiguous piece of text with one style. MinWidthChars and
// GapAfter preserve the few column layouts that are wider than their text.
type TextRun struct {
	Text          string
	Style         TextStyle
	MinWidthChars int
	GapAfter      float64
}

// Row is one line of text. Distribute places the last run at the far edge of
// the widest row in its group, matching the Onslaught header.
type Row struct {
	Runs       []TextRun
	GapBefore  float64
	Distribute bool
}

// Group is one independently positioned collection of rows.
type Group struct {
	Name     string
	Position Point
	Rows     []Row
}

// Hidden reports whether a user-toggleable group is suppressed.
type Hidden func(group string) bool

// BuildTextGroups creates the text portion of a HUD scene. Map geometry is
// intentionally a separate command set because it has its own vector layout.
func BuildTextGroups(v *model.View, cfg *config.Config, hidden Hidden) []Group {
	if v == nil || cfg == nil {
		return nil
	}

	var groups []Group
	if v.Status != "" {
		style := groupStyle(cfg, cfg.Widget.Status.Placement, "")
		if v.StatusIsFix {
			style.Color = colorWarning
		} else {
			style.Color = colorAlarm
		}
		groups = append(groups, textGroup("status", cfg, cfg.Widget.Status.Placement,
			[]Row{plainRow(v.Status, style)}))
	}

	if cfg.Widget.Block.Enabled && !isHidden(hidden, "block") {
		if head, sub, show := render.BlockLines(v, cfg.Widget.Block); show {
			style := groupStyle(cfg, cfg.Widget.Block.Placement, cfg.Widget.Block.Color)
			rows := []Row{plainRow(head, style)}
			if sub != "" {
				rows = append(rows, plainRow(sub, style))
			}
			groups = append(groups, textGroup("block", cfg, cfg.Widget.Block.Placement, rows))
		}
	}

	if cfg.Widget.Bosses.Enabled && !isHidden(hidden, "bosses") {
		if rows := bossRows(v, cfg); len(rows) > 0 {
			groups = append(groups, textGroup("bosses", cfg, cfg.Widget.Bosses.Placement, rows))
		}
	}

	if cfg.Widget.Session.Enabled && !isHidden(hidden, "session") {
		if text, show := render.SessionLine(v, cfg.Widget.Session); show {
			style := groupStyle(cfg, cfg.Widget.Session.Placement, cfg.Widget.Session.Color)
			groups = append(groups, textGroup("session", cfg, cfg.Widget.Session.Placement,
				[]Row{plainRow(text, style)}))
		}
	}

	if cfg.Widget.XP.Enabled && !isHidden(hidden, "xp") {
		if text, class, show := render.XPLine(v, cfg.Widget.XP); show {
			style := groupStyle(cfg, cfg.Widget.XP.Placement, cfg.Widget.XP.Color)
			switch class {
			case "shaky":
				style.Color = colorWarning
			case "unstable":
				style.Color = colorAlarm
			}
			groups = append(groups, textGroup("xp", cfg, cfg.Widget.XP.Placement,
				[]Row{plainRow(text, style)}))
		}
	}

	if cfg.Widget.Challenges.Enabled && !isHidden(hidden, "challenges") {
		if rows := challengeRows(v, cfg); len(rows) > 0 {
			groups = append(groups, textGroup("challenges", cfg,
				cfg.Widget.Challenges.Placement, rows))
		}
	}

	return groups
}

func bossRows(v *model.View, cfg *config.Config) []Row {
	base := groupStyle(cfg, cfg.Widget.Bosses.Placement, cfg.Widget.Bosses.Color)
	var rows []Row
	if attack, show := render.OutpostAttackLine(v); show {
		style := base
		style.Color = colorAlarm
		rows = append(rows, plainRow(attack, style))
	}

	timer, showTimer := render.OnslaughtHeaderTimer(v)
	panel, inOnslaught := render.OnslaughtPanel(v)
	if inOnslaught {
		headerStyle := tightShadow(base)
		headerStyle.Color = colorWhite
		header := Row{Runs: []TextRun{{Text: "Onslaught Cycles", Style: headerStyle}}}
		if showTimer {
			header.Runs = append(header.Runs, TextRun{Text: timer, Style: headerStyle})
			header.Distribute = true
		}
		rows = append(rows, header)

		labelStyle := tightShadow(base)
		labelStyle.Color = colorOnslaughtLabel
		for _, item := range panel {
			contentStyle := tightShadow(base)
			contentStyle.Color = onslaughtColor(item.ContentClass)
			rows = append(rows, Row{Runs: []TextRun{
				{Text: item.Label, Style: labelStyle, MinWidthChars: 5, GapAfter: 6},
				{Text: item.Content, Style: contentStyle},
			}})
		}
		return rows
	}

	threatStyle := base
	threatStyle.Color = colorWarning
	for _, text := range render.ThreatLines(v) {
		rows = append(rows, plainRow(text, threatStyle))
	}
	if nearest, show := render.NearestLine(v, cfg.Widget.Bosses); show {
		rows = append(rows, plainRow(nearest, base))
	}
	return rows
}

func challengeRows(v *model.View, cfg *config.Config) []Row {
	base := groupStyle(cfg, cfg.Widget.Challenges.Placement, cfg.Widget.Challenges.Color)
	lines := render.ChallengeLines(v, cfg.Widget.Challenges)
	rows := make([]Row, 0, len(lines))
	for _, line := range lines {
		style := base
		switch {
		case line.Done:
			style.Color = colorDone
			style.Opacity *= 0.6
			style.StrikeThrough = true
		case line.Urgent:
			style.Color = colorAlarm
		}

		row := Row{}
		if line.Gap {
			row.GapBefore = 6
		}
		if line.Heading {
			heading := style
			heading.Opacity *= 0.5
			row.Runs = []TextRun{{Text: line.Text(), Style: heading}}
			rows = append(rows, row)
			continue
		}

		name := line.Name
		if line.Sub {
			name = "  " + name
		}
		if line.Objective != "" {
			if line.Name != "" {
				name += ": "
			}
			row.Runs = append(row.Runs, TextRun{Text: name, Style: style})
			objective := style
			objective.Opacity *= 0.78
			row.Runs = append(row.Runs, TextRun{Text: line.Objective, Style: objective})
		} else {
			row.Runs = append(row.Runs, TextRun{Text: name, Style: style})
		}

		switch {
		case line.Progress != "":
			row.Runs = append(row.Runs,
				TextRun{Text: line.Padding(), Style: style},
				TextRun{Text: line.Progress, Style: style},
			)
			if line.Countdown != "" {
				countdown := style
				countdown.Opacity *= 0.7
				row.Runs = append(row.Runs, TextRun{Text: "  " + line.Countdown, Style: countdown})
			}
		case line.Countdown != "":
			countdown := style
			countdown.Opacity *= 0.7
			row.Runs = append(row.Runs,
				TextRun{Text: line.Padding(), Style: style},
				TextRun{Text: line.Countdown, Style: countdown},
			)
		}

		rows = append(rows, row)
	}
	return rows
}

func textGroup(name string, cfg *config.Config, place config.Placement, rows []Row) Group {
	return Group{
		Name: name,
		Position: Point{
			X: float64(cfg.HUD.MarginLeft + place.X),
			Y: float64(cfg.HUD.MarginTop + place.Y),
		},
		Rows: rows,
	}
}

func plainRow(text string, style TextStyle) Row {
	return Row{Runs: []TextRun{{Text: text, Style: style}}}
}

func groupStyle(cfg *config.Config, place config.Placement, color string) TextStyle {
	if place.FontFamily == "" {
		place.FontFamily = cfg.HUD.FontFamily
	}
	if place.FontSize <= 0 {
		place.FontSize = cfg.HUD.FontSize
	}
	if color == "" {
		color = cfg.HUD.TextColor
	}
	return TextStyle{
		FontFamily: place.FontFamily,
		FontSize:   place.FontSize,
		Weight:     WeightBold,
		Color:      color,
		Opacity:    cfg.HUD.Opacity,
		Shadows: []Shadow{
			{Color: colorShadow, Blur: 2},
		},
	}
}

func tightShadow(style TextStyle) TextStyle {
	style.Shadows = []Shadow{
		{Color: colorShadow, Offset: Point{X: 1, Y: 1}},
		{Color: colorShadow, Offset: Point{X: -1, Y: 1}},
		{Color: colorShadow, Offset: Point{X: 1, Y: -1}},
		{Color: colorShadow, Offset: Point{X: -1, Y: -1}},
	}
	return style
}

func onslaughtColor(class string) string {
	switch class {
	case render.OnslaughtPrevClass:
		return colorOnslaughtPrev
	case render.OnslaughtNowClass:
		return colorOnslaughtNow
	case render.OnslaughtNextClass:
		return colorOnslaughtNext
	default:
		return colorOnslaughtEmpty
	}
}

func isHidden(hidden Hidden, group string) bool {
	return hidden != nil && hidden(group)
}
