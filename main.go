// M0 SPIKE - throwaway. Proves the toolkit decision before any real code
// depends on it: that gotk4 builds, that the cgo layer-shell shim links in the
// right order, that gtk_layer_is_supported() is true under Hyprland, that the
// surface shows up in `hyprctl layers` under our namespace, that it stays
// visible over a fullscreen game, and whether pointer input passes through.
// Replaced by the real main.go in M1.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// GTK paints the theme's window background by default, which over a game reads
// as an ugly dark box behind the text. A layer surface is already
// alpha-capable on Wayland, so making the window and its .background style
// class fully transparent leaves only the glyphs. The text then needs its own
// contrast, since it may sit over anything: a hard outline via layered
// text-shadow is what the game's own HUD does, and it stays legible on both
// bright pavement and dark interiors without a backing panel.
const spikeCSS = `
window, window.background {
  background-color: transparent;
  background-image: none;
  box-shadow: none;
}
label {
  color: #e6cc4d;
  font-family: "Courier New", monospace;
  font-weight: bold;
  font-size: 12pt;
  text-shadow: 0 0 4px #000, 1px 1px 0 #000, -1px -1px 0 #000;
}
`

const namespace = "df-hud"

func main() {
	log.SetFlags(0)

	if !LayerShellBuilt {
		log.Fatal("built with -tags nolayershell: no layer shell in this binary")
	}
	// Assert before opening anything. A false here is the silent load-order
	// failure, and rendering a fallback window would just look like our bug.
	if !Supported() {
		log.Fatalf("gtk4-layer-shell reports the compositor does not support " +
			"zwlr_layer_shell_v1.\nIf you are on Hyprland this is almost " +
			"certainly the library load order, not the compositor.\nRemedy: " +
			"LD_PRELOAD=/usr/lib/libgtk4-layer-shell.so ./df-hud")
	}
	maj, min, mic := Version()
	fmt.Printf("gtk4-layer-shell %d.%d.%d, zwlr_layer_shell_v1 v%d\n", maj, min, mic, ProtocolVersion())

	app := gtk.NewApplication("com.xadips.dfhud.spike", gio.ApplicationFlagsNone)
	app.ConnectActivate(func() {
		css := gtk.NewCSSProvider()
		css.LoadFromData(spikeCSS)
		gtk.StyleContextAddProviderForDisplay(
			gdk.DisplayGetDefault(), css, gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)

		w := gtk.NewApplicationWindow(app)
		h := w.Native()

		// Order matters: everything below must happen before Present().
		InitForWindow(h)
		SetNamespace(h, namespace)
		SetLayer(h, LayerOverlay) // only Overlay draws above a fullscreen window
		SetAnchor(h, EdgeTop, true)
		SetAnchor(h, EdgeLeft, true)
		SetMargin(h, EdgeTop, 60) // clear waybar's 36px top zone on DP-1
		SetMargin(h, EdgeLeft, 40)
		SetExclusiveZone(h, -1)          // never reserve, never be pushed
		SetKeyboardMode(h, KeyboardNone) // game keeps every keypress

		lbl := gtk.NewLabel("df-hud M0 spike\nCLICK ME - the click should reach whatever is underneath")
		lbl.SetMarginTop(12)
		lbl.SetMarginBottom(12)
		lbl.SetMarginStart(16)
		lbl.SetMarginEnd(16)
		w.SetChild(lbl)
		w.Present()

		if !IsLayerWindow(h) {
			log.Fatal("InitForWindow did not take: this is an ordinary toplevel, not a layer surface")
		}

		// Click-through needs a realized surface, which Present() does not
		// guarantee synchronously. Re-apply on every "notify::surface"-ish
		// opportunity: GTK owns the input region and overwrites ours whenever
		// it recreates the surface.
		applyClickThrough := func() bool {
			ok := SetClickThrough(h, true)
			fmt.Printf("click-through applied: %v\n", ok)
			return ok
		}
		if !applyClickThrough() {
			// Not realized yet: retry on the next main-loop iteration.
			glib.IdleAdd(func() bool {
				if applyClickThrough() {
					return false // done, stop idling
				}
				return true // try again
			})
		}
		w.ConnectMap(func() { applyClickThrough() })

		fmt.Println("layer surface created; check: hyprctl layers | grep -A3 " + namespace)
	})

	if code := app.Run(os.Args); code != 0 {
		os.Exit(code)
	}
}
