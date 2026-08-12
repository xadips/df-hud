package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// The tray icon, drawn in code rather than shipped as a file.
//
// A 32x32 PNG committed as a binary blob would be a blob nobody can review, edit
// or diff, in a repository that is otherwise entirely text - and it would need a
// build step to change a colour. Drawing it is about forty lines, costs a
// millisecond at startup, and makes the two states a parameter rather than two
// files that can drift apart.
//
// The shape is a reticle: a ring, a centre dot and four ticks. It reads as
// something at the 22px waybar actually renders it at, which rules out anything
// with text in it - "DF" at 22px is two smudges.

// trayIconColors are the two states. Yellow is the game's own HUD colour, so the
// icon matches the overlay it controls; grey means the game is not running, which
// is the answer to "is df-hud doing anything right now" at a glance.
var (
	trayIconActive = color.NRGBA{R: 0xe6, G: 0xcc, B: 0x4d, A: 0xff}
	trayIconIdle   = color.NRGBA{R: 0x8a, G: 0x90, B: 0x99, A: 0xff}
)

// trayIconSize is generous on purpose: the host scales down to the bar's height,
// and downscaling is much kinder than upscaling.
const trayIconSize = 64

// trayIconPNG renders the icon and encodes it. SetIcon wants encoded image
// bytes, so there is no point keeping an image.Image around.
func trayIconPNG(c color.NRGBA, size int) []byte {
	if size < 8 {
		size = 8
	}
	// 4x4 supersampling. Without it the ring is visibly stepped once the host
	// scales the icon to the bar height.
	const samples = 4
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			hits := 0
			for sy := 0; sy < samples; sy++ {
				for sx := 0; sx < samples; sx++ {
					fx := (float64(x) + (float64(sx)+0.5)/samples) / float64(size)
					fy := (float64(y) + (float64(sy)+0.5)/samples) / float64(size)
					if trayIconCovers(fx, fy) {
						hits++
					}
				}
			}
			if hits == 0 {
				continue // left fully transparent, so the bar's background shows
			}
			alpha := float64(c.A) * float64(hits) / float64(samples*samples)
			img.SetNRGBA(x, y, color.NRGBA{R: c.R, G: c.G, B: c.B, A: uint8(alpha + 0.5)})
		}
	}

	var buf bytes.Buffer
	// The only error png.Encode returns comes from the writer, and a
	// bytes.Buffer does not fail.
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// trayIconCovers is the shape, in units of the icon's width so the geometry is
// independent of the pixel size.
func trayIconCovers(x, y float64) bool {
	dx, dy := x-0.5, y-0.5
	r := math.Hypot(dx, dy)
	switch {
	case r <= 0.11: // centre dot
		return true
	case r >= 0.29 && r <= 0.39: // ring
		return true
	case r > 0.39 && r <= 0.5: // ticks, out to the edge along both axes
		return math.Abs(dx) <= 0.045 || math.Abs(dy) <= 0.045
	}
	return false
}
