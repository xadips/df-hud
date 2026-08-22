package scene

import (
	"df-hud/internal/config"
	"math"
)

type layoutTransform struct {
	scale   float64
	offsetX float64
	offsetY float64
}

func newLayoutTransform(cfg *config.Config, viewport Viewport) layoutTransform {
	if cfg == nil || viewport.Width <= 0 || viewport.Height <= 0 ||
		cfg.HUD.ReferenceWidth <= 0 || cfg.HUD.ReferenceHeight <= 0 {
		return layoutTransform{scale: 1}
	}

	width, height := float64(viewport.Width), float64(viewport.Height)
	contentX, contentY := 0.0, 0.0
	contentWidth, contentHeight := width, height
	if viewport.GameWidth > 0 && viewport.GameHeight > 0 {
		gameAspect := float64(viewport.GameWidth) / float64(viewport.GameHeight)
		viewportAspect := width / height
		if gameAspect > viewportAspect {
			contentHeight = width / gameAspect
			contentY = (height - contentHeight) / 2
		} else if gameAspect < viewportAspect {
			contentWidth = height * gameAspect
			contentX = (width - contentWidth) / 2
		}
	}

	referenceWidth := float64(cfg.HUD.ReferenceWidth)
	referenceHeight := float64(cfg.HUD.ReferenceHeight)
	scale := math.Min(contentWidth/referenceWidth, contentHeight/referenceHeight)
	return layoutTransform{
		scale:   scale,
		offsetX: contentX + (contentWidth-referenceWidth*scale)/2,
		offsetY: contentY + (contentHeight-referenceHeight*scale)/2,
	}
}

func (t layoutTransform) point(point Point) Point {
	return Point{
		X: t.offsetX + point.X*t.scale,
		Y: t.offsetY + point.Y*t.scale,
	}
}

func applyTextLayout(groups []Group, transform layoutTransform) {
	for groupIndex := range groups {
		group := &groups[groupIndex]
		group.Position = transform.point(group.Position)
		for rowIndex := range group.Rows {
			row := &group.Rows[rowIndex]
			row.GapBefore *= transform.scale
			for runIndex := range row.Runs {
				run := &row.Runs[runIndex]
				run.GapAfter *= transform.scale
				run.Style.FontSize *= transform.scale
				run.Style.LetterSpacing *= transform.scale
				for shadowIndex := range run.Style.Shadows {
					shadow := &run.Style.Shadows[shadowIndex]
					shadow.Offset.X *= transform.scale
					shadow.Offset.Y *= transform.scale
					shadow.Blur *= transform.scale
				}
			}
		}
	}
}
