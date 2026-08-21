//go:build linux && cgo && !nolayershell

// Package-local cgo shim over gtk4-layer-shell (MIT). This is the ONLY file in
// the project that imports "C".
//
// Why hand-rolled instead of github.com/diamondburned/gotk4-layer-shell: that
// module is AGPL-3.0 wrapping an MIT C library, is a Jan-2024 pseudo-version
// with zero importers, and the surface it wraps is ~10 trivial functions. An
// AGPL dependency would relicense this whole project for no benefit.
//
// The API takes raw uintptr handles rather than gotk4 types on purpose: it
// keeps this file independent of the GTK bindings, so layershell_stub.go can
// satisfy the same signatures on machines with no Wayland and no C library
// (`go build -tags nolayershell`).
//
// LOAD-ORDER HAZARD: gtk4-layer-shell works by interposing on GDK's Wayland
// backend at load time, so it must be loaded before libwayland-client. If the
// link order is wrong the library silently no-ops and you get an ordinary
// toplevel window instead of a layer surface - a confusing, silent failure.
// Hence -Wl,--no-as-needed here, and hence Supported() must be asserted at
// startup by the caller. Escape hatch if a toolchain change ever breaks it:
// LD_PRELOAD=/usr/lib/libgtk4-layer-shell.so
package gtk

/*
#cgo pkg-config: gtk4-layer-shell-0 gtk4 gtk4-wayland wayland-client
#cgo LDFLAGS: -Wl,--no-as-needed
#include <gtk4-layer-shell.h>
#include <gtk/gtk.h>
#include <gdk/wayland/gdkwayland.h>
#include <wayland-client.h>
#include <stdlib.h>
#include <stdint.h>

// gotk4 hands out object addresses as uintptr (Widget.Native()), so reaching
// the C API means turning an integer back into a pointer somewhere. Doing that
// in Go needs uintptr -> unsafe.Pointer, which `go vet` rightly flags as
// "possible misuse of unsafe.Pointer": the GC does not track uintptr, so in the
// general case the object could move or be freed in between. It cannot happen
// here - these are GTK objects on the C heap, owned by GTK for the window's
// lifetime, never Go allocations - but rather than teach every reader to
// dismiss a vet warning, the cast happens in C where it is unremarkable. That
// keeps `go vet ./...` clean with no suppression flags.
static GtkWindow  *df_as_window(uintptr_t p)  { return (GtkWindow *)p; }
static GdkMonitor *df_as_monitor(uintptr_t p) { return (GdkMonitor *)p; }

// df_set_click_through makes the surface transparent to pointer input, so
// clicks land on whatever is underneath (the game) instead of being swallowed.
//
// Neither GTK4 nor gtk4-layer-shell exposes an input-region API, so we reach
// past both to the wl_surface. An EMPTY wl_region as the input region means
// "no part of this surface accepts pointer events"; passing NULL instead
// restores the default of the whole surface accepting them.
//
// Requires a realized surface, so this can only run after the window is
// presented - hence the boolean return rather than a void, so the Go side can
// retry rather than silently do nothing.
static int df_set_click_through(GtkWindow *win, int through) {
	if (win == NULL) return 0;
	GdkSurface *surface = gtk_native_get_surface(GTK_NATIVE(win));
	if (surface == NULL || !GDK_IS_WAYLAND_SURFACE(surface)) return 0;
	struct wl_surface *wls = gdk_wayland_surface_get_wl_surface(GDK_WAYLAND_SURFACE(surface));
	if (wls == NULL) return 0;

	if (through) {
		GdkDisplay *display = gdk_surface_get_display(surface);
		if (display == NULL || !GDK_IS_WAYLAND_DISPLAY(display)) return 0;
		struct wl_compositor *comp =
			gdk_wayland_display_get_wl_compositor(GDK_WAYLAND_DISPLAY(display));
		if (comp == NULL) return 0;
		struct wl_region *empty = wl_compositor_create_region(comp);
		if (empty == NULL) return 0;
		// Deliberately add no rectangles: an empty region rejects all input.
		wl_surface_set_input_region(wls, empty);
		wl_region_destroy(empty);
	} else {
		wl_surface_set_input_region(wls, NULL);
	}
	wl_surface_commit(wls);
	return 1;
}
*/
import "C"

// Only for C.free of a CString below; that is a *C.char, not a uintptr, so it
// is not what vet's unsafeptr check is about.
import "unsafe"

// Layer mirrors GtkLayerShellLayer. Overlay is the only one that draws above a
// fullscreen window, which is required here because the game runs fullscreen.
type Layer int

const (
	LayerBackground Layer = C.GTK_LAYER_SHELL_LAYER_BACKGROUND
	LayerBottom     Layer = C.GTK_LAYER_SHELL_LAYER_BOTTOM
	LayerTop        Layer = C.GTK_LAYER_SHELL_LAYER_TOP
	LayerOverlay    Layer = C.GTK_LAYER_SHELL_LAYER_OVERLAY
)

// Edge mirrors GtkLayerShellEdge.
type Edge int

const (
	EdgeLeft   Edge = C.GTK_LAYER_SHELL_EDGE_LEFT
	EdgeRight  Edge = C.GTK_LAYER_SHELL_EDGE_RIGHT
	EdgeTop    Edge = C.GTK_LAYER_SHELL_EDGE_TOP
	EdgeBottom Edge = C.GTK_LAYER_SHELL_EDGE_BOTTOM
)

// KeyboardMode mirrors GtkLayerShellKeyboardMode. None means the compositor
// never routes keystrokes here, so the game keeps every keypress.
type KeyboardMode int

const (
	KeyboardNone      KeyboardMode = C.GTK_LAYER_SHELL_KEYBOARD_MODE_NONE
	KeyboardExclusive KeyboardMode = C.GTK_LAYER_SHELL_KEYBOARD_MODE_EXCLUSIVE
	KeyboardOnDemand  KeyboardMode = C.GTK_LAYER_SHELL_KEYBOARD_MODE_ON_DEMAND
)

// LayerShellBuilt reports whether this binary has the real shim compiled in
// (false in the stub build). Distinct from Supported(), which asks the running
// compositor.
const LayerShellBuilt = true

func win(p uintptr) *C.GtkWindow { return C.df_as_window(C.uintptr_t(p)) }

// Supported reports whether the current display supports zwlr_layer_shell_v1.
// False also covers the silent load-order failure described above, which is
// why callers must treat it as fatal rather than falling back to a plain
// window that merely looks broken.
func Supported() bool { return C.gtk_layer_is_supported() != 0 }

// Version is the gtk4-layer-shell library version, for startup logging.
func Version() (major, minor, micro int) {
	return int(C.gtk_layer_get_major_version()),
		int(C.gtk_layer_get_minor_version()),
		int(C.gtk_layer_get_micro_version())
}

// ProtocolVersion is the negotiated zwlr_layer_shell_v1 version, 0 if none.
func ProtocolVersion() int { return int(C.gtk_layer_get_protocol_version()) }

// InitForWindow turns a GtkWindow into a layer surface. Must be called before
// the window is presented.
func InitForWindow(w uintptr) { C.gtk_layer_init_for_window(win(w)) }

// IsLayerWindow reports whether InitForWindow took effect on this window.
func IsLayerWindow(w uintptr) bool { return C.gtk_layer_is_layer_window(win(w)) != 0 }

// SetNamespace sets the layer-shell namespace, which is what `hyprctl layers`
// lists and what a Hyprland `hl.layer_rule` matches on.
func SetNamespace(w uintptr, ns string) {
	c := C.CString(ns)
	defer C.free(unsafe.Pointer(c))
	C.gtk_layer_set_namespace(win(w), c)
}

func SetLayer(w uintptr, l Layer) {
	C.gtk_layer_set_layer(win(w), C.GtkLayerShellLayer(l))
}

func SetAnchor(w uintptr, e Edge, anchor bool) {
	var b C.gboolean
	if anchor {
		b = 1
	}
	C.gtk_layer_set_anchor(win(w), C.GtkLayerShellEdge(e), b)
}

func SetMargin(w uintptr, e Edge, px int) {
	C.gtk_layer_set_margin(win(w), C.GtkLayerShellEdge(e), C.int(px))
}

// SetExclusiveZone with -1 means "never reserve space, and never get pushed
// around by anyone else's exclusive zone" - which is what makes positioning
// predictable next to waybar's 36px top zone.
func SetExclusiveZone(w uintptr, zone int) {
	C.gtk_layer_set_exclusive_zone(win(w), C.int(zone))
}

func SetKeyboardMode(w uintptr, m KeyboardMode) {
	C.gtk_layer_set_keyboard_mode(win(w), C.GtkLayerShellKeyboardMode(m))
}

// SetMonitor pins the surface to one output. mon is a GdkMonitor*; passing 0
// lets the compositor choose (which is what "output = auto" relies on).
func SetMonitor(w uintptr, mon uintptr) {
	C.gtk_layer_set_monitor(win(w), C.df_as_monitor(C.uintptr_t(mon)))
}

// SetClickThrough makes the surface ignore pointer input, so clicks pass to the
// window underneath. Combined with KeyboardNone this makes the HUD completely
// non-interactive, which is what lets it sit over the game without stealing a
// single click - anything interactive belongs in the separate console window.
//
// Returns false if the surface is not realized yet: call it after Present(),
// and re-apply whenever the surface is recreated, because GTK sets its own
// input region and will overwrite ours.
func SetClickThrough(w uintptr, through bool) bool {
	var t C.int
	if through {
		t = 1
	}
	return C.df_set_click_through(win(w), t) != 0
}
