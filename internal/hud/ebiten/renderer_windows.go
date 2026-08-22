//go:build windows && !nolayershell

package ebitenhud

import (
	"bytes"
	"df-hud/internal/hud/scene"
	"fmt"
	"image/color"
	"log"
	"math"
	"strconv"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"golang.org/x/image/colornames"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
)

type faceKey struct {
	family string
	size   int
	weight scene.Weight
}

type sourceKey struct {
	family string
	weight scene.Weight
}

type renderer struct {
	regular *text.GoTextFaceSource
	bold    *text.GoTextFaceSource
	faces   map[faceKey]*text.GoTextFace
	sources map[sourceKey]*text.GoTextFaceSource
	warned  map[string]bool
}

func newRenderer() (*renderer, error) {
	regular, err := text.NewGoTextFaceSource(bytes.NewReader(gomono.TTF))
	if err != nil {
		return nil, fmt.Errorf("parse fallback regular font: %w", err)
	}
	bold, err := text.NewGoTextFaceSource(bytes.NewReader(gomonobold.TTF))
	if err != nil {
		return nil, fmt.Errorf("parse fallback bold font: %w", err)
	}
	return &renderer{
		regular: regular,
		bold:    bold,
		faces:   map[faceKey]*text.GoTextFace{},
		sources: map[sourceKey]*text.GoTextFaceSource{},
		warned:  map[string]bool{},
	}, nil
}

func (r *renderer) draw(dst *ebiten.Image, frame scene.Scene) {
	for _, group := range frame.TextGroups {
		r.drawGroup(dst, group)
	}
	if frame.Map != nil {
		r.drawMap(dst, frame.Map)
	}
}

func (r *renderer) drawGroup(dst *ebiten.Image, group scene.Group) {
	groupWidth := r.groupWidth(group.Rows)
	x0, y := group.Position.X, group.Position.Y
	for _, row := range group.Rows {
		y += row.GapBefore
		height := r.rowHeight(row)
		x := x0
		for i, run := range row.Runs {
			width := r.runWidth(run)
			if row.Distribute && i == len(row.Runs)-1 {
				x = x0 + groupWidth - width
			}
			r.drawRun(dst, run, x, y, height)
			x += width + run.GapAfter
		}
		y += height
	}
}

func (r *renderer) drawRun(dst *ebiten.Image, run scene.TextRun, x, y, height float64) {
	if run.Text == "" {
		return
	}
	face := r.face(run.Style)
	width := text.Advance(run.Text, face)
	if run.Style.Background != "" {
		vector.FillRect(dst, float32(x), float32(y), float32(width), float32(height),
			parseColor(run.Style.Background, run.Style.Opacity), false)
	}
	for _, shadow := range run.Style.Shadows {
		if shadow.Blur > 0 {
			r.drawBlurredText(dst, run.Text, face, x, y, shadow, run.Style.Opacity)
			continue
		}
		r.drawText(dst, run.Text, face, x+shadow.Offset.X, y+shadow.Offset.Y,
			parseColor(shadow.Color, run.Style.Opacity))
	}
	r.drawText(dst, run.Text, face, x, y, parseColor(run.Style.Color, run.Style.Opacity))
	if run.Style.StrikeThrough {
		vector.StrokeLine(dst, float32(x), float32(y+height*0.52),
			float32(x+width), float32(y+height*0.52), 1,
			parseColor(run.Style.Color, run.Style.Opacity), true)
	}
}

func (r *renderer) drawBlurredText(dst *ebiten.Image, value string, face text.Face,
	x, y float64, shadow scene.Shadow, opacity float64) {
	const alpha = 0.38
	radius := math.Max(1, shadow.Blur/2)
	for _, offset := range [...]scene.Point{
		{X: -radius}, {X: radius}, {Y: -radius}, {Y: radius},
		{X: -radius, Y: -radius}, {X: radius, Y: -radius},
		{X: -radius, Y: radius}, {X: radius, Y: radius},
	} {
		r.drawText(dst, value, face, x+offset.X, y+offset.Y,
			parseColor(shadow.Color, opacity*alpha))
	}
}

func (r *renderer) drawText(dst *ebiten.Image, value string, face text.Face,
	x, y float64, clr color.Color) {
	options := &text.DrawOptions{}
	options.GeoM.Translate(x, y)
	options.ColorScale.ScaleWithColor(clr)
	text.Draw(dst, value, face, options)
}

func (r *renderer) drawMap(dst *ebiten.Image, group *scene.MapGroup) {
	origin := group.Position
	for _, cell := range group.Cells {
		x, y := origin.X+cell.Bounds.X, origin.Y+cell.Bounds.Y
		vector.FillRect(dst, float32(x), float32(y), float32(cell.Bounds.Width),
			float32(cell.Bounds.Height), parseColor(cell.Fill, cell.Alpha), false)
		vector.StrokeRect(dst, float32(x+0.5), float32(y+0.5),
			float32(cell.Bounds.Width-1), float32(cell.Bounds.Height-1),
			float32(cell.BorderWidth), parseColor(cell.BorderColor, cell.BorderAlpha), false)
	}
	for _, line := range group.Dividers {
		r.drawMapLine(dst, origin, line)
	}
	for _, line := range group.Rings {
		r.drawMapLine(dst, origin, line)
	}
	for _, marker := range group.Markers {
		style := scene.TextStyle{
			FontFamily: marker.FontFamily,
			FontSize:   marker.FontSize * 0.75,
			Weight:     marker.Weight,
			Color:      marker.Color,
			Opacity:    marker.Opacity,
		}
		face := r.face(style)
		size := text.Advance(marker.Text, face)
		if size > marker.MaxWidth && size > 0 {
			style.FontSize *= marker.MaxWidth / size
			face = r.face(style)
		}
		options := &text.DrawOptions{}
		options.PrimaryAlign = text.AlignCenter
		options.SecondaryAlign = text.AlignCenter
		options.GeoM.Translate(
			origin.X+marker.Bounds.X+marker.Bounds.Width/2,
			origin.Y+marker.Bounds.Y+marker.Bounds.Height/2,
		)
		options.ColorScale.ScaleWithColor(parseColor(marker.Color, marker.Opacity))
		text.Draw(dst, marker.Text, face, options)
	}
	if len(group.ListRows) > 0 {
		r.drawGroup(dst, scene.Group{
			Name: "map-list",
			Position: scene.Point{
				X: origin.X + group.ListOffset.X,
				Y: origin.Y + group.ListOffset.Y,
			},
			Rows: group.ListRows,
		})
	}
}

func (r *renderer) drawMapLine(dst *ebiten.Image, origin scene.Point, line scene.Line) {
	vector.StrokeLine(dst,
		float32(origin.X+line.From.X), float32(origin.Y+line.From.Y),
		float32(origin.X+line.To.X), float32(origin.Y+line.To.Y),
		float32(line.Width), parseColor(line.Color, line.Opacity), false)
}

func (r *renderer) groupWidth(rows []scene.Row) float64 {
	var widest float64
	for _, row := range rows {
		var width float64
		for _, run := range row.Runs {
			width += r.runWidth(run) + run.GapAfter
		}
		if width > widest {
			widest = width
		}
	}
	return widest
}

func (r *renderer) runWidth(run scene.TextRun) float64 {
	face := r.face(run.Style)
	width := text.Advance(run.Text, face)
	if run.MinWidthChars > 0 {
		minimum := float64(run.MinWidthChars) * text.Advance("M", face)
		if minimum > width {
			width = minimum
		}
	}
	return width
}

func (r *renderer) rowHeight(row scene.Row) float64 {
	var height float64
	for _, run := range row.Runs {
		metrics := r.face(run.Style).Metrics()
		h := metrics.HAscent + metrics.HDescent + metrics.HLineGap
		if h > height {
			height = h
		}
	}
	if height <= 0 {
		return 1
	}
	return height
}

func (r *renderer) face(style scene.TextStyle) *text.GoTextFace {
	// Config sizes are points, while Ebitengine faces are logical pixels.
	size := int(math.Round(style.FontSize * 4 / 3))
	if size < 1 {
		size = 1
	}
	family := firstFontFamily(style.FontFamily)
	key := faceKey{family: strings.ToLower(family), size: size, weight: style.Weight}
	if face := r.faces[key]; face != nil {
		return face
	}
	source := r.source(family, style.Weight)
	face := &text.GoTextFace{Source: source, Size: float64(size)}
	r.faces[key] = face
	return face
}

func (r *renderer) source(family string, weight scene.Weight) *text.GoTextFaceSource {
	key := sourceKey{family: strings.ToLower(family), weight: weight}
	if source := r.sources[key]; source != nil {
		return source
	}
	if family != "" && !isGenericMonospace(family) {
		source, path, err := loadWindowsFont(family, weight == scene.WeightBold)
		if err == nil {
			log.Printf("hud: resolved font %q to %s", family, path)
			r.sources[key] = source
			return source
		}
		warnKey := "font:" + key.family + fmt.Sprint(":", weight)
		if !r.warned[warnKey] {
			log.Printf("hud: could not resolve Windows font %q (%v); using embedded Go Mono",
				family, err)
			r.warned[warnKey] = true
		}
	}
	source := r.regular
	if weight == scene.WeightBold {
		source = r.bold
	}
	r.sources[key] = source
	return source
}

func firstFontFamily(value string) string {
	for _, family := range strings.Split(value, ",") {
		family = strings.Trim(strings.TrimSpace(family), `"'`)
		if family != "" {
			return family
		}
	}
	return ""
}

func isGenericMonospace(family string) bool {
	switch strings.ToLower(strings.TrimSpace(family)) {
	case "monospace", "mono", "go mono":
		return true
	default:
		return false
	}
}

func parseColor(value string, opacity float64) color.NRGBA {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "#") {
		if parsed, ok := parseHexColor(value[1:]); ok {
			parsed.A = scaleAlpha(parsed.A, opacity)
			return parsed
		}
	}
	if named, ok := colornames.Map[strings.ToLower(value)]; ok {
		return color.NRGBA{R: named.R, G: named.G, B: named.B, A: scaleAlpha(named.A, opacity)}
	}
	return color.NRGBA{R: 255, G: 255, B: 255, A: scaleAlpha(255, opacity)}
}

func parseHexColor(hex string) (color.NRGBA, bool) {
	expand := func(b byte) byte { return (b << 4) | b }
	switch len(hex) {
	case 3, 4:
		values := make([]byte, len(hex))
		for i := range hex {
			n, err := strconv.ParseUint(hex[i:i+1], 16, 8)
			if err != nil {
				return color.NRGBA{}, false
			}
			values[i] = expand(byte(n))
		}
		out := color.NRGBA{R: values[0], G: values[1], B: values[2], A: 255}
		if len(values) == 4 {
			out.A = values[3]
		}
		return out, true
	case 6, 8:
		n, err := strconv.ParseUint(hex, 16, 32)
		if err != nil {
			return color.NRGBA{}, false
		}
		if len(hex) == 6 {
			return color.NRGBA{R: byte(n >> 16), G: byte(n >> 8), B: byte(n), A: 255}, true
		}
		return color.NRGBA{R: byte(n >> 24), G: byte(n >> 16), B: byte(n >> 8), A: byte(n)}, true
	}
	return color.NRGBA{}, false
}

func scaleAlpha(alpha uint8, opacity float64) uint8 {
	if opacity < 0 {
		opacity = 0
	}
	if opacity > 1 {
		opacity = 1
	}
	return uint8(math.Round(float64(alpha) * opacity))
}
