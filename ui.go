//go:build linux && cgo && !nolayershell

package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// The HUD surface. This is the part M0 existed to de-risk, and the call sequence
// below is the one that spike validated:
//
//   - assert gtk_layer_is_supported() BEFORE opening anything. When
//     gtk4-layer-shell loses the link-order race against libwayland-client it
//     does not fail, it silently no-ops into an ordinary toplevel window - which
//     looks exactly like df-hud being broken. Failing loudly with the LD_PRELOAD
//     remedy is the difference between a two-minute fix and an evening.
//   - every layer-shell property must be set before Present().
//   - IsLayerWindow() after Present() catches the same failure from the other
//     side.
//   - click-through needs a realized surface, which Present() does not guarantee
//     synchronously, and GTK recreates the input region whenever it recreates
//     the surface - hence the idle retry and the re-apply on map.
//
// GTK owns the main OS thread, so Run blocks. Every other component is already
// running in its own goroutine by the time this is called, and they communicate
// only through the store, so nothing here needs a lock.

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
`

type hud struct {
	app    *app
	window *gtk.ApplicationWindow
	box    *gtk.Box
	status *gtk.Label

	widgets []Widget
}

// runUI takes over the calling goroutine, which must be the main OS thread.
func runUI(ctx context.Context, a *app) error {
	if !LayerShellBuilt {
		return fmt.Errorf("this binary was built with -tags nolayershell, so it has no HUD; " +
			"run with -headless, or rebuild without that tag")
	}
	if !Supported() {
		return fmt.Errorf("gtk4-layer-shell reports no zwlr_layer_shell_v1 support.\n" +
			"On Hyprland this is almost certainly the library load order rather than the " +
			"compositor.\nRemedy: LD_PRELOAD=/usr/lib/libgtk4-layer-shell.so df-hud")
	}
	major, minor, micro := Version()
	log.Printf("hud: gtk4-layer-shell %d.%d.%d, zwlr_layer_shell_v1 v%d",
		major, minor, micro, ProtocolVersion())

	gtkApp := gtk.NewApplication("com.xadips.dfhud", gio.ApplicationFlagsNone)
	h := &hud{app: a}
	gtkApp.ConnectActivate(func() { h.build(gtkApp) })

	// Shut the GTK loop down when the context ends, so SIGINT closes the window
	// rather than leaving it up after everything else has stopped.
	go func() {
		<-ctx.Done()
		glib.IdleAdd(func() bool {
			gtkApp.Quit()
			return false
		})
	}()

	// Only the program name: GTK would otherwise try to interpret df-hud's own
	// flags and refuse to start.
	if code := gtkApp.Run([]string{"df-hud"}); code != 0 {
		return fmt.Errorf("gtk exited with code %d", code)
	}
	return nil
}

func (h *hud) build(gtkApp *gtk.Application) {
	cfg := h.app.Config()

	css := gtk.NewCSSProvider()
	css.LoadFromData(fmt.Sprintf(hudCSS, cfg.HUD.TextColor, cfg.HUD.FontFamily, cfg.HUD.FontSize))
	gtk.StyleContextAddProviderForDisplay(gdk.DisplayGetDefault(), css,
		gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
	if cfg.HUD.CSS != "" {
		user := gtk.NewCSSProvider()
		user.LoadFromPath(cfg.HUD.CSS)
		// Loaded after the built-in sheet and at a higher priority, so a user
		// stylesheet can override anything above without editing it.
		gtk.StyleContextAddProviderForDisplay(gdk.DisplayGetDefault(), user,
			gtk.STYLE_PROVIDER_PRIORITY_USER)
	}

	h.window = gtk.NewApplicationWindow(gtkApp)
	handle := h.window.Native()

	// Everything from here to Present() is load-bearing order.
	InitForWindow(handle)
	SetNamespace(handle, namespace)
	SetLayer(handle, cfg.HUD.LayerValue())
	for _, edge := range cfg.HUD.AnchorEdges() {
		SetAnchor(handle, edge, true)
	}
	SetMargin(handle, EdgeTop, cfg.HUD.MarginTop)
	SetMargin(handle, EdgeRight, cfg.HUD.MarginRight)
	SetMargin(handle, EdgeBottom, cfg.HUD.MarginBottom)
	SetMargin(handle, EdgeLeft, cfg.HUD.MarginLeft)
	SetExclusiveZone(handle, -1)          // never reserve space, never be pushed around
	SetKeyboardMode(handle, KeyboardNone) // the game keeps every keypress

	h.box = gtk.NewBox(gtk.OrientationVertical, 2)
	h.status = newHUDLabel()
	h.status.AddCSSClass("status")
	h.box.Append(h.status)

	h.widgets = buildWidgets(cfg)
	for _, w := range h.widgets {
		h.box.Append(w.Root())
	}
	h.window.SetChild(h.box)
	if cfg.HUD.Opacity < 1 {
		h.window.SetOpacity(cfg.HUD.Opacity)
	}

	// Render once before showing, so the surface is sized to real content rather
	// than appearing empty and then jumping.
	h.update()
	h.window.Present()

	if !IsLayerWindow(handle) {
		log.Fatal("hud: InitForWindow did not take - this is an ordinary toplevel, not a " +
			"layer surface, so it will not draw over the game. This is the gtk4-layer-shell " +
			"load-order failure; try LD_PRELOAD=/usr/lib/libgtk4-layer-shell.so")
	}

	if cfg.HUD.ClickThrough {
		h.applyClickThrough()
	}

	// One second, matching the game's own timeKeeper loop: clocks and countdowns
	// move with no network activity at all.
	glib.TimeoutAdd(1000, func() bool {
		h.update()
		if err := h.app.state.MaybeSave(); err != nil {
			log.Printf("state: could not save: %v", err)
		}
		return true
	})

	log.Printf("hud: layer surface up (check: hyprctl layers | grep -A3 %s)", namespace)
}

// applyClickThrough installs an empty input region so every pointer event lands
// on the game instead of the HUD. Retried on idle because Present() does not
// guarantee a realized surface, and re-applied on map because GTK owns the input
// region and replaces ours whenever it recreates the surface.
func (h *hud) applyClickThrough() {
	handle := h.window.Native()
	if !SetClickThrough(handle, true) {
		glib.IdleAdd(func() bool {
			return !SetClickThrough(handle, true) // keep trying until it takes
		})
	}
	h.window.ConnectMap(func() { SetClickThrough(handle, true) })
}

// update renders one frame from a freshly derived view. Runs on the GTK main
// thread, so it touches no locks beyond the store's own.
func (h *hud) update() {
	view := h.app.store.Derive(time.Now())

	if view.Status != "" {
		h.status.SetText(view.Status)
		h.status.SetVisible(true)
		// Amber for something the player can fix, red for something they cannot.
		if view.StatusIsFix {
			h.status.AddCSSClass("fixable")
		} else {
			h.status.RemoveCSSClass("fixable")
		}
	} else {
		h.status.SetVisible(false)
	}

	for _, w := range h.widgets {
		w.Update(view)
	}
}
