package scene

import (
	"df-hud/internal/config"
	"df-hud/internal/hud/render"
	"df-hud/internal/model"
	"fmt"
)

const (
	mapCellBorderColor = "#000000"
	mapDividerColor    = "#ffffff"
	mapMarkerColor     = "#ffffff"
	mapOutpostColor    = "#bfffbf"
	mapMarkerInk       = "#101010"
	mapListGap         = 10
	mapMarkerFontRatio = 0.72
)

// Rect is a logical-pixel rectangle relative to its map group.
type Rect struct {
	X      float64
	Y      float64
	Width  float64
	Height float64
}

// Line is one vector stroke relative to its map group.
type Line struct {
	From    Point
	To      Point
	Width   float64
	Color   string
	Opacity float64
}

// MapCell paints one walkable city block.
type MapCell struct {
	BlockX      int
	BlockY      int
	Bounds      Rect
	Fill        string
	Alpha       float64
	BorderColor string
	BorderWidth float64
	BorderAlpha float64
}

// MapMarker is centered in one city block. MaxWidth keeps multi-character
// identifiers from spilling into a neighbouring block.
type MapMarker struct {
	BlockX     int
	BlockY     int
	Text       string
	Bounds     Rect
	Color      string
	Opacity    float64
	FontSize   float64
	MaxWidth   float64
	FontFamily string
	Weight     Weight
}

// MapGroup is the vector map and its optional text key.
type MapGroup struct {
	Name       string
	Position   Point
	Width      float64
	Height     float64
	CellSize   float64
	Window     render.MapWindow
	Cells      []MapCell
	Dividers   []Line
	Rings      []Line
	Markers    []MapMarker
	ListOffset Point
	ListRows   []Row
}

// Viewport is the monitor's logical size. It is only needed to resolve a
// centered map; ordinary text groups are positioned directly from config.
type Viewport struct {
	Width  int
	Height int
	// GameWidth/Height are Unity's physical render resolution. Their aspect
	// ratio identifies letterboxed game content within the logical viewport.
	GameWidth  int
	GameHeight int
}

// Scene is one immutable snapshot of backend-neutral draw commands.
type Scene struct {
	TextGroups []Group
	Map        *MapGroup
}

// Build creates a complete scene for one view/config/visibility snapshot.
func Build(v *model.View, cfg *config.Config, viewport Viewport, hidden Hidden) Scene {
	out := Scene{TextGroups: BuildTextGroups(v, cfg, hidden)}
	if v == nil || cfg == nil || !cfg.Widget.Map.Enabled || isHidden(hidden, "map") {
		if cfg != nil {
			applyTextLayout(out.TextGroups, newLayoutTransform(cfg, viewport))
		}
		return out
	}
	transform := newLayoutTransform(cfg, viewport)
	applyTextLayout(out.TextGroups, transform)
	out.Map = buildMap(v, cfg, viewport, transform)
	return out
}

func buildMap(v *model.View, cfg *config.Config, viewport Viewport, transform layoutTransform) *MapGroup {
	frame := render.MapFrameFor(v, cfg.Widget.Map)
	if !v.HaveData || (!v.HasPosition && len(frame.Marks) == 0) {
		return nil
	}

	cell := float64(render.MapCellPx(cfg.Widget.Map)) * transform.scale
	width := float64(frame.Window.W) * cell
	height := float64(frame.Window.H) * cell
	group := &MapGroup{
		Name:       "map",
		Position:   mapPosition(cfg, viewport, width, height, transform),
		Width:      width,
		Height:     height,
		CellSize:   cell,
		Window:     frame.Window,
		ListOffset: Point{X: width + mapListGap*transform.scale},
	}

	city := render.City()
	mapOpacity := normalizedOpacity(cfg.Widget.Map.Opacity) * cfg.HUD.Opacity
	for y := 0; y < frame.Window.H; y++ {
		for x := 0; x < frame.Window.W; x++ {
			blockX, blockY := frame.Window.X+x, frame.Window.Y+y
			shade, ok := city.Shade(blockX, blockY)
			if !ok {
				continue
			}
			bounds := Rect{
				X: float64(x) * cell, Y: float64(y) * cell,
				Width: cell, Height: cell,
			}
			group.Cells = append(group.Cells, MapCell{
				BlockX: blockX, BlockY: blockY, Bounds: bounds,
				Fill:        fmt.Sprintf("#%02x%02x%02x", shade.R, shade.G, shade.B),
				Alpha:       shade.Alpha * mapOpacity,
				BorderColor: mapCellBorderColor,
				BorderWidth: transform.scale,
				BorderAlpha: 0.45 * mapOpacity,
			})
		}
	}

	group.Dividers = mapDividers(frame.Window, cell, cfg.HUD.Opacity)
	group.Rings, group.Markers = mapAnnotations(v, frame, cell, cfg)
	scaleLineWidths(group.Dividers, transform.scale)
	scaleLineWidths(group.Rings, transform.scale)
	if cfg.Widget.Map.ShowList {
		group.ListRows = mapListRows(frame, cfg)
		applyTextLayout([]Group{{Rows: group.ListRows}}, layoutTransform{scale: transform.scale})
	}
	return group
}

func scaleLineWidths(lines []Line, scale float64) {
	for i := range lines {
		lines[i].Width *= scale
	}
}

func mapPosition(cfg *config.Config, viewport Viewport, width, height float64,
	transform layoutTransform) Point {
	place := cfg.Widget.Map.Placement
	if !cfg.Widget.Map.Center || viewport.Width <= 0 || viewport.Height <= 0 {
		return transform.point(Point{
			X: float64(cfg.HUD.MarginLeft + place.X),
			Y: float64(cfg.HUD.MarginTop + place.Y),
		})
	}
	usableWidth := float64(cfg.HUD.ReferenceWidth-cfg.HUD.MarginLeft-cfg.HUD.MarginRight) *
		transform.scale
	usableHeight := float64(cfg.HUD.ReferenceHeight-cfg.HUD.MarginTop-cfg.HUD.MarginBottom) *
		transform.scale
	return Point{
		X: transform.offsetX + float64(cfg.HUD.MarginLeft)*transform.scale +
			(usableWidth-width)/2 + float64(cfg.Widget.Map.OffsetX)*transform.scale,
		Y: transform.offsetY + float64(cfg.HUD.MarginTop)*transform.scale +
			(usableHeight-height)/2 + float64(cfg.Widget.Map.OffsetY)*transform.scale,
	}
}

func mapDividers(window render.MapWindow, cell, opacity float64) []Line {
	city := render.City()
	var lines []Line
	for _, blockX := range city.DividersX {
		if blockX <= window.X || blockX >= window.X+window.W {
			continue
		}
		x := float64(blockX-window.X) * cell
		for y := 0; y < window.H; y++ {
			if city.DividesColumn(blockX, window.Y+y) {
				lines = append(lines, Line{
					From:  Point{X: x, Y: float64(y) * cell},
					To:    Point{X: x, Y: float64(y+1) * cell},
					Width: 1, Color: mapDividerColor, Opacity: 0.35 * opacity,
				})
			}
		}
	}
	for _, blockY := range city.DividersY {
		if blockY <= window.Y || blockY >= window.Y+window.H {
			continue
		}
		y := float64(blockY-window.Y) * cell
		for x := 0; x < window.W; x++ {
			if city.DividesRow(window.X+x, blockY) {
				lines = append(lines, Line{
					From:  Point{X: float64(x) * cell, Y: y},
					To:    Point{X: float64(x+1) * cell, Y: y},
					Width: 1, Color: mapDividerColor, Opacity: 0.35 * opacity,
				})
			}
		}
	}
	return lines
}

func mapAnnotations(v *model.View, frame render.MapFrame, cell float64,
	cfg *config.Config) (rings []Line, markers []MapMarker) {
	addMarker := func(blockX, blockY int, text, color string) {
		if text == "" || !render.City().IsBlock(blockX, blockY) ||
			!frame.Window.Contains(blockX, blockY) {
			return
		}
		bounds := blockBounds(frame.Window, cell, blockX, blockY)
		markers = append(markers, MapMarker{
			BlockX: blockX, BlockY: blockY, Text: text, Bounds: bounds,
			Color: color, Opacity: cfg.HUD.Opacity,
			FontSize: cell * mapMarkerFontRatio, MaxWidth: cell * 0.9,
			FontFamily: "monospace", Weight: WeightBold,
		})
	}
	addRing := func(blockX, blockY int, color string, width float64) {
		if !render.City().IsBlock(blockX, blockY) || !frame.Window.Contains(blockX, blockY) {
			return
		}
		bounds := blockBounds(frame.Window, cell, blockX, blockY)
		inset := width / 2
		rings = append(rings,
			Line{
				From:  Point{X: bounds.X + inset, Y: bounds.Y + inset},
				To:    Point{X: bounds.X + bounds.Width - inset, Y: bounds.Y + inset},
				Width: width, Color: color, Opacity: cfg.HUD.Opacity,
			},
			Line{
				From:  Point{X: bounds.X + bounds.Width - inset, Y: bounds.Y + inset},
				To:    Point{X: bounds.X + bounds.Width - inset, Y: bounds.Y + bounds.Height - inset},
				Width: width, Color: color, Opacity: cfg.HUD.Opacity,
			},
			Line{
				From:  Point{X: bounds.X + bounds.Width - inset, Y: bounds.Y + bounds.Height - inset},
				To:    Point{X: bounds.X + inset, Y: bounds.Y + bounds.Height - inset},
				Width: width, Color: color, Opacity: cfg.HUD.Opacity,
			},
			Line{
				From:  Point{X: bounds.X + inset, Y: bounds.Y + bounds.Height - inset},
				To:    Point{X: bounds.X + inset, Y: bounds.Y + inset},
				Width: width, Color: color, Opacity: cfg.HUD.Opacity,
			},
		)
	}

	for _, outpost := range render.Outposts() {
		addMarker(outpost.X, outpost.Y, render.OutpostLetter(outpost.Name), mapOutpostColor)
	}
	for _, mark := range frame.Marks {
		if mark.OffMap {
			continue
		}
		if render.CityMarkRinged(mark) {
			addRing(mark.X, mark.Y, render.CityMarkInk(mark).Hex(), 1.5)
		}
		addMarker(mark.X, mark.Y, mark.Marker, mapMarkerColor)
	}
	if v.HasPosition {
		addRing(v.PositionX, v.PositionY, mapMarkerColor, 2)
	}
	return rings, markers
}

func mapListRows(frame render.MapFrame, cfg *config.Config) []Row {
	place := cfg.Widget.Map.Placement
	if place.FontSize <= 0 {
		place.FontSize = render.MapListPt(cfg.Widget.Map)
	}
	base := groupStyle(cfg, place, cfg.Widget.Map.Color)
	base.Shadows = []Shadow{
		{Color: colorShadow, Offset: Point{X: 1, Y: 1}},
		{Color: colorShadow, Offset: Point{X: -1}},
		{Color: colorShadow, Offset: Point{Y: -1}},
	}

	rows := make([]Row, 0, len(frame.Rows))
	for _, item := range frame.Rows {
		row := Row{}
		switch {
		case item.Marker != "":
			chip := base
			chip.Color = mapMarkerInk
			// The bright category background already provides contrast. A black
			// outline around this near-black glyph thickens and tints B1/N2-style
			// identifiers, while the timer and name beside it still need theirs.
			chip.Shadows = nil
			row.Runs = append(row.Runs, TextRun{Text: item.Marker, Style: chip})
			if item.Timer != "" {
				timer := base
				timer.Opacity *= 0.78
				row.Runs = append(row.Runs, TextRun{Text: " " + item.Timer + "  ", Style: timer})
			} else {
				row.Runs = append(row.Runs, TextRun{Text: " ", Style: base})
			}
			row.Runs[0].Style.Background = item.Color
			row.Runs = append(row.Runs, TextRun{Text: item.Text, Style: base})
		case item.Sub:
			row.Runs = []TextRun{{Text: "        " + item.Text, Style: base}}
		default:
			muted := base
			muted.Opacity *= 0.6
			row.Runs = []TextRun{{Text: item.Text, Style: muted}}
		}
		rows = append(rows, row)
	}
	return rows
}

func blockBounds(window render.MapWindow, cell float64, blockX, blockY int) Rect {
	return Rect{
		X:     float64(blockX-window.X) * cell,
		Y:     float64(blockY-window.Y) * cell,
		Width: cell, Height: cell,
	}
}

func normalizedOpacity(opacity float64) float64 {
	if opacity <= 0 || opacity > 1 {
		return 1
	}
	return opacity
}
