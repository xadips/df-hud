//go:build linux && cgo && !nolayershell

package main

import (
	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
	"github.com/diamondburned/gotk4/pkg/pangocairo"
)

// The city, as a grid, in the shape DFProfiler's map draws it: every block shaded
// by its own difficulty band, the gaps left empty, the district lines heavier, an
// identifier on every active event and a ring around the block you are standing on.
//
// THREE THINGS THIS IS NOT, each of which was tried first:
//
//   - not an ordinary window. A toplevel sits BEHIND a fullscreen window, so a map
//     opened while playing is drawn under the game - invisible exactly when it is
//     wanted. It also gets tiled and resized, which silently clipped the east side
//     of the city off a grid that had asked for its full width.
//   - not a second layer surface. It would need its own copy of the monitor
//     pinning, the workspace following and the show/hide rules the HUD already has.
//     As a group it inherits all of them, and the same keybind machinery hides it.
//   - not 1716 labels. That was the first draft, for the sake of per-cell tooltips
//     and hover - both of which are worth nothing on a surface that passes every
//     pointer event through to the game, which is the whole point of the overlay.
//     One canvas draws the same thing in one widget.
//
// So it is one DrawingArea and one label, positioned like any other group, hidden
// until a key asks for it.

type mapWidget struct {
	cfg  MapWidgetConfig
	box  *gtk.Box
	area *gtk.DrawingArea
	list *gtk.Label

	// frame and standing are what the last Update handed the draw function. The draw
	// function runs when GTK feels like it, not when Update says so, so the state it
	// reads has to outlive the call that set it.
	//
	// The frame carries the window, the visible marks and the key together, from one
	// function, so what is drawn on a cell and what the key says about it cannot
	// disagree - they are the same numbering.
	frame       mapFrame
	standing    [2]int
	haveStandin bool
}

func newMapWidget(cfg MapWidgetConfig) *mapWidget {
	w := &mapWidget{
		cfg:  cfg,
		box:  gtk.NewBox(gtk.OrientationHorizontal, 10),
		area: gtk.NewDrawingArea(),
		list: newHUDLabel(),
	}
	cell := w.cell()
	// The window's contents move as you walk but its SIZE is fixed, which is what
	// lets the size request be set once, here.
	bw, bh := mapWindowSize(cfg)
	w.frame.Window = mapWindow{X: theCity.OriginX, Y: theCity.OriginY, W: bw, H: bh}
	w.area.SetSizeRequest(bw*cell, bh*cell)
	w.area.SetDrawFunc(w.draw)
	w.list.SetXAlign(0)
	w.list.SetYAlign(0)
	// Every other group is a box of one-line labels, so newHUDLabel turns single
	// line mode ON. This one is a dozen rows in a single label - it is a table, and
	// its rows are built as one piece of markup - so the whole list collapsed onto
	// one line until this.
	w.list.SetSingleLineMode(false)
	w.box.Append(w.area)
	if cfg.ShowList {
		w.box.Append(w.list)
	}
	return w
}

// cell is the size of one block in pixels. See mapCellPx, which the key's font size
// is derived from as well, so the two scale together.
func (w *mapWidget) cell() int { return mapCellPx(w.cfg) }

func (w *mapWidget) Root() gtk.Widgetter { return w.box }

// NaturalSize is the grid's own size, which is what the HUD centres on when
// widget.map.center is set. The list beside it is deliberately not counted: it
// changes width as bosses come and go, and a map that slid sideways every time a
// name got longer would be worse than one that is centred on the city.
func (w *mapWidget) NaturalSize() (int, int) {
	cell := w.cell()
	bw, bh := mapWindowSize(w.cfg)
	return bw * cell, bh * cell
}

// Centered reports whether the HUD should place this group itself.
func (w *mapWidget) Centered() bool { return w.cfg.Center }

func (w *mapWidget) Update(v *View) {
	w.frame = mapFrameFor(v, w.cfg)
	w.standing = [2]int{v.PositionX, v.PositionY}
	w.haveStandin = v.HasPosition
	// The whole group goes away when there is nothing to draw on it. Not for the
	// sake of the pixels - it is that a map with no events and no position is a
	// picture of a city, which says nothing and hides the game behind it.
	if !v.HaveData || (!w.haveStandin && len(w.frame.Marks) == 0) {
		w.box.SetVisible(false)
		return
	}
	w.box.SetVisible(true)
	if w.cfg.ShowList {
		w.list.SetMarkup(mapListMarkup(v, w.cfg))
	}
	w.area.QueueDraw()
}

// draw paints the city. Everything here is drawn with the map's own alpha, so the
// game shows through it: a solid 59x55 grid over a fullscreen game would be a wall,
// and the point of an overlay is that you can still see what you are shooting.
func (w *mapWidget) draw(_ *gtk.DrawingArea, cr *cairo.Context, _, _ int) {
	cell := float64(w.cell())

	// The blocks. A gap gets nothing at all rather than a dark cell: it is not a
	// place, and drawing it would claim it was one.
	//
	// Each one is drawn with its own border, which is not decoration: without it
	// neighbouring blocks in the same difficulty band merge into one shape and the
	// map stops reading as a grid of places you walk between - you cannot count
	// blocks on it, which is the one thing it is for. Their own map draws the same
	// border for the same reason.
	cr.SetLineWidth(1)
	for y := 0; y < w.frame.Window.H; y++ {
		for x := 0; x < w.frame.Window.W; x++ {
			shade, ok := theCity.Shade(w.frame.Window.X+x, w.frame.Window.Y+y)
			if !ok {
				continue
			}
			// Half-pixel offsets, or a 1px stroke straddles two device pixels and
			// comes out as a soft two-pixel smear.
			left, top := float64(x)*cell+0.5, float64(y)*cell+0.5
			cr.Rectangle(left, top, cell-1, cell-1)
			cr.SetSourceRGBA(float64(shade.R)/255, float64(shade.G)/255, float64(shade.B)/255,
				shade.Alpha*w.opacity())
			cr.FillPreserve()
			cr.SetSourceRGBA(0, 0, 0, 0.45*w.opacity())
			cr.Stroke()
		}
	}

	// The district lines, which are what df_tradezone counts. Drawn over the blocks
	// and under everything else.
	cr.SetSourceRGBA(1, 1, 1, 0.35*w.opacity())
	cr.SetLineWidth(1)
	for _, bx := range theCity.DividersX {
		if bx <= w.frame.Window.X || bx >= w.frame.Window.X+w.frame.Window.W {
			continue
		}
		x := float64(bx-w.frame.Window.X) * cell
		cr.MoveTo(x, 0)
		cr.LineTo(x, float64(w.frame.Window.H)*cell)
	}
	for _, by := range theCity.DividersY {
		if by <= w.frame.Window.Y || by >= w.frame.Window.Y+w.frame.Window.H {
			continue
		}
		y := float64(by-w.frame.Window.Y) * cell
		cr.MoveTo(0, y)
		cr.LineTo(float64(w.frame.Window.W)*cell, y)
	}
	cr.Stroke()

	// Markers are opaque: the background is see-through, the writing on it is not,
	// or it would be unreadable over whatever the game happens to be showing.
	layout := pangocairo.CreateLayout(cr)
	font := pango.NewFontDescription()
	font.SetFamily("monospace")
	font.SetWeight(pango.WeightBold)
	font.SetAbsoluteSize(cell * 0.72 * pango.SCALE)
	layout.SetFontDescription(font)

	for _, o := range outposts {
		w.drawMarker(cr, layout, cell, o.X, o.Y, outpostLetters[o.Name], 0.75, 1, 0.75)
	}
	for _, m := range w.frame.Marks {
		if m.OffMap {
			continue
		}
		// The ring carries the category - a nest, a single boss, a bandit pack, a
		// mission, a QRF - because those are four different decisions and one colour
		// for all of them made the map say "something is here" and nothing else. The
		// same colour is the chip behind that event's letter in the key beside the
		// grid, so the two are one lookup.
		r, g, b := m.Category().Color().Floats()
		w.ring(cr, cell, m.X, m.Y, r, g, b, 1.5)
		// The letter stays white whatever the ring is: it has to be legible on all
		// sixteen of the map's shades, and a coloured glyph one cell wide is not.
		w.drawMarker(cr, layout, cell, m.X, m.Y, m.Marker, 1, 1, 1)
	}
	if w.haveStandin {
		w.ring(cr, cell, w.standing[0], w.standing[1], 1, 1, 1, 2)
	}
}

// ring outlines one block, for an event or for where you are.
func (w *mapWidget) ring(cr *cairo.Context, cell float64, bx, by int, r, g, b, width float64) {
	if !theCity.IsBlock(bx, by) || !w.frame.Window.contains(bx, by) {
		return
	}
	x := float64(bx-w.frame.Window.X) * cell
	y := float64(by-w.frame.Window.Y) * cell
	cr.SetSourceRGBA(r, g, b, 1)
	cr.SetLineWidth(width)
	// Inset by half the line width, or half of it falls in the neighbouring cell.
	cr.Rectangle(x+width/2, y+width/2, cell-width, cell-width)
	cr.Stroke()
}

// drawMarker centres one character in a block.
func (w *mapWidget) drawMarker(cr *cairo.Context, layout *pango.Layout, cell float64,
	bx, by int, text string, r, g, b float64) {
	if text == "" || !theCity.IsBlock(bx, by) || !w.frame.Window.contains(bx, by) {
		return
	}
	layout.SetText(text)
	tw, th := layout.PixelSize()
	x := float64(bx-w.frame.Window.X)*cell + (cell-float64(tw))/2
	y := float64(by-w.frame.Window.Y)*cell + (cell-float64(th))/2
	cr.SetSourceRGBA(r, g, b, 1)
	cr.MoveTo(x, y)
	pangocairo.ShowLayout(cr, layout)
}

func (w *mapWidget) opacity() float64 {
	if w.cfg.Opacity <= 0 || w.cfg.Opacity > 1 {
		return 1
	}
	return w.cfg.Opacity
}
