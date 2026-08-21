//go:build windows && cgo && !nolayershell

package gtk

/*
#cgo pkg-config: gtk4
#cgo windows LDFLAGS: -luser32
#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <gtk/gtk.h>
#include <gdk/win32/gdkwin32.h>
#include <cairo.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

static GtkWindow *df_as_gtk_window(uintptr_t p) {
	return (GtkWindow *)p;
}

// These GTK properties are set before realization so GDK creates the native
// window as close to an overlay as its public API permits. Native styles are
// still applied after realization because no HWND exists before then.
static void df_prepare_win32_overlay(GtkWindow *win) {
	if (win == NULL) return;
	gtk_window_set_decorated(win, FALSE);
	gtk_window_set_resizable(win, FALSE);
	gtk_widget_set_focusable(GTK_WIDGET(win), FALSE);
}

typedef struct {
	const char *connector;
	HMONITOR monitor;
	RECT rect;
} DfMonitorSearch;

static BOOL CALLBACK df_find_monitor(HMONITOR monitor, HDC dc, LPRECT rect, LPARAM data) {
	(void)dc;
	(void)rect;
	DfMonitorSearch *search = (DfMonitorSearch *)data;
	MONITORINFOEXW info;
	char device[128];

	memset(&info, 0, sizeof(info));
	info.cbSize = sizeof(info);
	if (!GetMonitorInfoW(monitor, (MONITORINFO *)&info)) return TRUE;
	if (WideCharToMultiByte(CP_UTF8, 0, info.szDevice, -1, device, sizeof(device),
	                       NULL, NULL) == 0) {
		return TRUE;
	}
	if (_stricmp(device, search->connector) != 0) return TRUE;

	search->monitor = monitor;
	search->rect = info.rcMonitor;
	return FALSE;
}

static int df_monitor_rect_for_connector(const char *connector, RECT *rect) {
	if (connector == NULL || connector[0] == '\0') return 0;
	DfMonitorSearch search;
	memset(&search, 0, sizeof(search));
	search.connector = connector;
	EnumDisplayMonitors(NULL, NULL, df_find_monitor, (LPARAM)&search);
	if (search.monitor == NULL) return 0;
	*rect = search.rect;
	return 1;
}

typedef struct {
	int wanted;
	int current;
	HMONITOR monitor;
	RECT rect;
	WCHAR device[CCHDEVICENAME];
} DfMonitorIndexSearch;

static BOOL CALLBACK df_find_monitor_index(HMONITOR monitor, HDC dc,
                                            LPRECT rect, LPARAM data) {
	(void)dc;
	(void)rect;
	DfMonitorIndexSearch *search = (DfMonitorIndexSearch *)data;
	if (search->current++ != search->wanted) return TRUE;

	MONITORINFOEXW info;
	memset(&info, 0, sizeof(info));
	info.cbSize = sizeof(info);
	if (!GetMonitorInfoW(monitor, (MONITORINFO *)&info)) return FALSE;
	search->monitor = monitor;
	search->rect = info.rcMonitor;
	memcpy(search->device, info.szDevice, sizeof(search->device));
	return FALSE;
}

static int df_monitor_at_index(int index, DfMonitorIndexSearch *search) {
	if (index < 0) return 0;
	memset(search, 0, sizeof(*search));
	search->wanted = index;
	EnumDisplayMonitors(NULL, NULL, df_find_monitor_index, (LPARAM)search);
	return search->monitor != NULL;
}

static char *df_monitor_name_for_index(int index) {
	DfMonitorIndexSearch search;
	if (!df_monitor_at_index(index, &search)) return NULL;
	int bytes = WideCharToMultiByte(CP_UTF8, 0, search.device, -1,
	                                NULL, 0, NULL, NULL);
	if (bytes <= 0) return NULL;
	char *name = (char *)malloc((size_t)bytes);
	if (name == NULL) return NULL;
	if (WideCharToMultiByte(CP_UTF8, 0, search.device, -1, name, bytes,
	                        NULL, NULL) == 0) {
		free(name);
		return NULL;
	}
	return name;
}

// Applies styles only after gtk_native_get_surface() can supply a Win32
// GdkSurface.
static int df_apply_win32_overlay(GtkWindow *win, const char *connector,
                                  int monitor_index,
                                  int fallback_x, int fallback_y,
                                  int fallback_w, int fallback_h,
                                  int margin_top, int margin_right,
                                  int margin_bottom, int margin_left,
                                  int click_through, int alpha) {
	if (win == NULL) return 0;
	GdkSurface *surface = gtk_native_get_surface(GTK_NATIVE(win));
	if (surface == NULL || !GDK_IS_WIN32_SURFACE(surface)) return 0;

	HWND hwnd = GDK_SURFACE_HWND(surface);
	if (hwnd == NULL) return 0;

	LONG_PTR style = GetWindowLongPtrW(hwnd, GWL_STYLE);
	style &= ~(WS_CAPTION | WS_THICKFRAME | WS_MINIMIZEBOX |
	           WS_MAXIMIZEBOX | WS_SYSMENU);
	style |= WS_POPUP;
	SetWindowLongPtrW(hwnd, GWL_STYLE, style);

	LONG_PTR exstyle = GetWindowLongPtrW(hwnd, GWL_EXSTYLE);
	exstyle &= ~WS_EX_APPWINDOW;
	exstyle |= WS_EX_TOOLWINDOW | WS_EX_NOACTIVATE | WS_EX_TOPMOST;
	if (click_through) {
		// Cross-process hit-test pass-through requires the pair. TRANSPARENT
		// alone only changes paint ordering for sibling windows.
		exstyle |= WS_EX_LAYERED | WS_EX_TRANSPARENT;
	} else {
		exstyle &= ~WS_EX_TRANSPARENT;
	}
	SetWindowLongPtrW(hwnd, GWL_EXSTYLE, exstyle);
	if (click_through &&
	    !SetLayeredWindowAttributes(hwnd, 0, (BYTE)alpha, LWA_ALPHA)) {
		return -1;
	}

	RECT target;
	if (!df_monitor_rect_for_connector(connector, &target)) {
		DfMonitorIndexSearch indexed;
		if (df_monitor_at_index(monitor_index, &indexed)) {
			target = indexed.rect;
		} else {
			target.left = fallback_x;
			target.top = fallback_y;
			target.right = fallback_x + fallback_w;
			target.bottom = fallback_y + fallback_h;
		}
	}
	if (target.right <= target.left || target.bottom <= target.top) {
		HMONITOR monitor = MonitorFromWindow(hwnd, MONITOR_DEFAULTTOPRIMARY);
		MONITORINFO info;
		memset(&info, 0, sizeof(info));
		info.cbSize = sizeof(info);
		if (monitor == NULL || !GetMonitorInfoW(monitor, &info)) return 0;
		target = info.rcMonitor;
	}

	int x = target.left + margin_left;
	int y = target.top + margin_top;
	int width = (target.right - target.left) - margin_left - margin_right;
	int height = (target.bottom - target.top) - margin_top - margin_bottom;
	if (width < 1) width = 1;
	if (height < 1) height = 1;

	if (!SetWindowPos(hwnd, HWND_TOPMOST, x, y, width, height,
	                  SWP_NOACTIVATE | SWP_NOOWNERZORDER |
	                  SWP_FRAMECHANGED)) {
		// The HWND exists, so retrying from every idle iteration would spin
		// forever on a policy/permission failure. Distinguish that from the
		// not-realized-yet result above.
		return -1;
	}

	// WS_EX_TRANSPARENT changes paint ordering but, by itself, does not promise
	// cross-process hit-test pass-through. GDK's input region is the actual
	// event-routing contract: an empty region sends every pointer event to the
	// surface underneath, including windows owned by the game process.
	if (click_through) {
		cairo_region_t *empty = cairo_region_create();
		if (empty == NULL) return -1;
		gdk_surface_set_input_region(surface, empty);
		cairo_region_destroy(empty);
	} else {
		gdk_surface_set_input_region(surface, NULL);
	}
	return 1;
}
*/
import "C"

import (
	"log"
	"unsafe"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

type windowsOverlay struct {
	window *gtk.ApplicationWindow
	handle uintptr

	connector       string
	monitorIndex    int
	scaleFactor     int
	x, y            int
	width, height   int
	marginTop       int
	marginRight     int
	marginBottom    int
	marginLeft      int
	clickThrough    bool
	opacity         float64
	placementWarned bool
}

func checkPlatformOverlay() error { return nil }

func newPlatformOverlay(window *gtk.ApplicationWindow) platformOverlay {
	return &windowsOverlay{
		window:       window,
		handle:       window.Native(),
		monitorIndex: -1,
		scaleFactor:  1,
	}
}

func (o *windowsOverlay) setup(cfg *Config) error {
	C.df_prepare_win32_overlay(C.df_as_gtk_window(C.uintptr_t(o.handle)))
	o.clickThrough = cfg.HUD.ClickThrough
	// The realize signal is the earliest point at which GTK guarantees a
	// GdkSurface. Installing WS_EX_NOACTIVATE here prevents the first map from
	// briefly activating or decorating the overlay.
	o.window.ConnectRealize(func() {
		o.applyMapped(o.clickThrough)
	})
	return nil
}

func (o *windowsOverlay) applyPlacement(cfg *Config) {
	o.marginTop = cfg.HUD.MarginTop
	o.marginRight = cfg.HUD.MarginRight
	o.marginBottom = cfg.HUD.MarginBottom
	o.marginLeft = cfg.HUD.MarginLeft
	o.clickThrough = cfg.HUD.ClickThrough
	o.opacity = cfg.HUD.Opacity
	o.window.SetOpacity(cfg.HUD.Opacity)
}

// GDK enumerates the primary monitor first on Win32. Unlike layer-shell there is
// no compositor to choose an output, so use it when no connector was requested.
func (*windowsOverlay) defaultMonitor(monitors []*gdk.Monitor) *gdk.Monitor {
	if len(monitors) == 0 {
		return nil
	}
	return monitors[0]
}

func (*windowsOverlay) monitorName(monitor *gdk.Monitor) string {
	if connector := monitor.Connector(); connector != "" {
		return connector
	}
	for i, candidate := range allMonitors() {
		if candidate.Native() != monitor.Native() {
			continue
		}
		name := C.df_monitor_name_for_index(C.int(i))
		if name == nil {
			return ""
		}
		defer C.free(unsafe.Pointer(name))
		return C.GoString(name)
	}
	return ""
}

func (o *windowsOverlay) setMonitor(monitor *gdk.Monitor) {
	o.connector = ""
	o.monitorIndex = -1
	o.scaleFactor = 1
	o.x, o.y, o.width, o.height = 0, 0, 0, 0
	if monitor == nil {
		return
	}
	o.connector = monitor.Connector()
	if scale := monitor.ScaleFactor(); scale > 0 {
		o.scaleFactor = scale
	}
	for i, candidate := range allMonitors() {
		if candidate.Native() == monitor.Native() {
			o.monitorIndex = i
			break
		}
	}
	if geometry := monitor.Geometry(); geometry != nil {
		o.x = geometry.X()
		o.y = geometry.Y()
		o.width = geometry.Width()
		o.height = geometry.Height()
	}
}

func (o *windowsOverlay) applyMapped(clickThrough bool) bool {
	connector := C.CString(o.connector)
	defer C.free(unsafe.Pointer(connector))
	var through C.int
	if clickThrough {
		through = 1
	}
	alpha := int(o.opacity*255 + 0.5)
	if alpha < 0 {
		alpha = 0
	}
	if alpha > 255 {
		alpha = 255
	}
	result := C.df_apply_win32_overlay(
		C.df_as_gtk_window(C.uintptr_t(o.handle)),
		connector,
		C.int(o.monitorIndex),
		C.int(o.x), C.int(o.y), C.int(o.width), C.int(o.height),
		// Win32 monitor rectangles are device pixels; HUD margins are GTK
		// application pixels, so scale them at the native boundary.
		C.int(o.marginTop*o.scaleFactor), C.int(o.marginRight*o.scaleFactor),
		C.int(o.marginBottom*o.scaleFactor), C.int(o.marginLeft*o.scaleFactor),
		through,
		C.int(alpha),
	)
	if result < 0 && !o.placementWarned {
		o.placementWarned = true
		log.Print("hud: Win32 rejected overlay placement; keeping the GDK-managed window")
	}
	return result != 0
}

func (o *windowsOverlay) maintain(clickThrough bool) {
	o.applyMapped(clickThrough)
}

func (*windowsOverlay) ready() {
	log.Print("hud: ready (Win32 topmost click-through overlay)")
}
