//go:build linux && cgo && !nolayershell

package gtk

import (
	"fmt"
	"log"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

type linuxOverlay struct {
	window *gtk.ApplicationWindow
	handle uintptr
}

func preparePlatformRenderer() {}

func checkPlatformOverlay() error {
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
	return nil
}

func newPlatformOverlay(window *gtk.ApplicationWindow) platformOverlay {
	return &linuxOverlay{window: window, handle: window.Native()}
}

func (o *linuxOverlay) setup(*Config) error {
	InitForWindow(o.handle)
	if !IsLayerWindow(o.handle) {
		return fmt.Errorf("InitForWindow did not take; this is an ordinary toplevel, not a layer " +
			"surface (try LD_PRELOAD=/usr/lib/libgtk4-layer-shell.so)")
	}
	SetNamespace(o.handle, namespace)
	SetExclusiveZone(o.handle, -1)
	SetKeyboardMode(o.handle, KeyboardNone)
	return nil
}

func (o *linuxOverlay) applyPlacement(cfg *Config) {
	layer := LayerOverlay
	switch cfg.HUD.LayerValue() {
	case "background":
		layer = LayerBackground
	case "bottom":
		layer = LayerBottom
	case "top":
		layer = LayerTop
	}
	SetLayer(o.handle, layer)
	for _, edge := range []Edge{EdgeTop, EdgeRight, EdgeBottom, EdgeLeft} {
		SetAnchor(o.handle, edge, true)
	}
	SetMargin(o.handle, EdgeTop, cfg.HUD.MarginTop)
	SetMargin(o.handle, EdgeRight, cfg.HUD.MarginRight)
	SetMargin(o.handle, EdgeBottom, cfg.HUD.MarginBottom)
	SetMargin(o.handle, EdgeLeft, cfg.HUD.MarginLeft)
	o.window.SetOpacity(cfg.HUD.Opacity)
}

// A nil monitor preserves layer-shell's "compositor chooses" behaviour.
func (*linuxOverlay) defaultMonitor([]*gdk.Monitor) *gdk.Monitor { return nil }

func (*linuxOverlay) monitorName(monitor *gdk.Monitor) string {
	return monitor.Connector()
}

func (o *linuxOverlay) setMonitor(monitor *gdk.Monitor) {
	if monitor == nil {
		SetMonitor(o.handle, 0)
		return
	}
	SetMonitor(o.handle, monitor.Native())
}

func (o *linuxOverlay) applyMapped(clickThrough bool) bool {
	return SetClickThrough(o.handle, clickThrough)
}

func (*linuxOverlay) maintain(bool) {}

func (*linuxOverlay) ready() {
	log.Printf("hud: ready (check: hyprctl layers | grep -A3 %s)", namespace)
}
