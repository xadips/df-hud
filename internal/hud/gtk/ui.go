//go:build (linux || windows) && cgo && !nolayershell

package gtk

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"df-hud/internal/hud/render"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// The shared GTK HUD surface. Platform-specific overlay setup is deliberately
// kept behind platformOverlay:
//
//   - Linux validates and configures gtk4-layer-shell before Present().
//   - Windows configures GTK before realization and the HWND after realization.
//   - native click-through/window styles are re-applied on every map because GTK
//     may recreate or reset the native surface during a hide/show cycle.
//
// GTK owns the main OS thread, so Run blocks. Every other component is already
// running in its own goroutine by the time this is called, and they communicate
// only through the store, so nothing here needs a lock.

type hud struct {
	deps    Dependencies
	window  *gtk.ApplicationWindow
	overlay platformOverlay
	// fixed places each group at its own coordinates. The surface spans the whole
	// monitor, so those coordinates are screen coordinates.
	//
	// This was a single vertical GtkBox in one corner, which is why every group
	// needed an `order`. Four unrelated readings in one column is the wrong shape
	// for a HUD over a game that has its own interface to fit around: the clock
	// wants to be near the game's clock, block info wants the side you glance at,
	// and stacking them means at most one of them is where you would look.
	fixed  *gtk.Fixed
	status *gtk.Label
	css    *gtk.CSSProvider

	widgets []placedWidget
	// widgetSig is the widget configuration the current widget tree was built
	// from, so a reload only rebuilds it when it actually changed.
	widgetSig string

	// visible tracks what the surface is actually doing, because the decision to
	// show or hide arrives repeatedly and mapping an already-mapped surface is
	// wasted work at best.
	visible bool
	// monitorW and monitorH are the size of the monitor the surface is on, kept so
	// a config reload can re-centre a group without waiting for the next remap.
	monitorW, monitorH int
	// monitorPinned is the connector the surface is currently pinned to, empty
	// for the platform default.
	monitorPinned  string
	warnedMonitors map[string]bool
}

// platformOverlay is the window-system seam used by the shared GTK UI.
// Implementations configure an unrealized GtkWindow first, then apply the
// native details that require a mapped surface.
type platformOverlay interface {
	setup(*Config) error
	applyPlacement(*Config)
	defaultMonitor([]*gdk.Monitor) *gdk.Monitor
	monitorName(*gdk.Monitor) string
	setMonitor(*gdk.Monitor)
	applyMapped(bool) bool
	maintain(bool)
	ready()
}

// runUI takes over the calling goroutine, which must be the main OS thread.
func Run(ctx context.Context, deps Dependencies) error {
	if !deps.valid() {
		return errors.New("incomplete GTK dependencies")
	}
	if err := checkPlatformOverlay(); err != nil {
		return err
	}

	gtkApp := gtk.NewApplication("com.xadips.dfhud", gio.ApplicationFlagsNone)
	h := &hud{deps: deps}
	gtkApp.ConnectActivate(func() {
		// Activate can fire more than once. GtkApplication is single-instance by
		// way of a D-Bus name, so starting a second df-hud does not start a second
		// GTK loop: it forwards an activation to this one and exits. Rebuilding
		// here would give this process a second window, a second 1s timer and a
		// second set of widgets, all invisible in the logs except as duplicated
		// lines - which is exactly how it was found.
		if h.window != nil {
			log.Print("hud: another df-hud tried to start; showing this one instead")
			h.applyVisibility(h.deps.Visibility())
			return
		}
		h.build(gtkApp)
	})

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
	// A non-primary instance returns from Run immediately and successfully,
	// having handed its activation to the df-hud that was already running. Left
	// unsaid, that looks like df-hud starting and then quietly deciding not to.
	if gtkApp.IsRemote() {
		return errors.New("another df-hud is already running; it has been asked to show its HUD")
	}
	return nil
}

func (h *hud) build(gtkApp *gtk.Application) {
	cfg := h.deps.Config()

	// Kept, so a config reload can re-load its data in place. A provider already
	// registered with the display re-styles every widget when its data changes,
	// which is what makes font and colour changes take effect without a restart.
	h.css = gtk.NewCSSProvider()
	h.css.LoadFromData(render.StyleSheet(cfg))
	gtk.StyleContextAddProviderForDisplay(gdk.DisplayGetDefault(), h.css,
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
	h.overlay = newPlatformOverlay(h.window)

	// Platform setup has to happen before realization. On Wayland this creates
	// the layer surface; on Windows it sets GTK's decoration and focus policy
	// before GDK creates the HWND.
	if err := h.overlay.setup(cfg); err != nil {
		log.Fatalf("hud: could not initialise overlay: %v", err)
	}
	h.applyPlacement(cfg)

	h.fixed = gtk.NewFixed()
	h.status = newHUDLabel()
	h.status.AddCSSClass("status")
	h.status.AddCSSClass(render.GroupClass("status"))
	h.fixed.Put(h.status, float64(cfg.Widget.Status.X), float64(cfg.Widget.Status.Y))
	h.rebuildWidgets(cfg)
	h.window.SetChild(h.fixed)
	// Re-applied on every map, not just the first. GTK/GDK can recreate the
	// native surface during a hide/show cycle, and both backends need the
	// realized native surface before they can install input/window styles.
	h.window.ConnectMap(h.applyMappedOverlay)

	// The tray item keeps df-hud running with nothing on screen. Without this
	// hold, hiding the HUD would be the last window going away and
	// GtkApplication would exit the moment you closed the game.
	gtkApp.Hold()

	// A keypress must redraw now, not on the next tick: a group that takes a second
	// to disappear reads as the key not having worked.
	h.deps.OnGroupsChange(func() {
		glib.IdleAdd(func() bool {
			if h.visible {
				h.update()
			}
			return false
		})
	})

	h.deps.OnConfigReload(func(next *Config) {
		// Arrives on the config watcher's goroutine.
		glib.IdleAdd(func() bool {
			h.applyConfig(next)
			return false
		})
	})

	// The surface is shown and hidden by the visibility rules from here on, so
	// this is the only Present() that is not one of theirs.
	h.deps.OnVisibilityChange(func(state hudVisibility) {
		// Arrives on the watcher's goroutine; GTK only tolerates the main one.
		glib.IdleAdd(func() bool {
			h.applyVisibility(state)
			return false
		})
	})
	h.applyVisibility(h.deps.Visibility())

	// One second, matching the game's own timeKeeper loop: clocks and countdowns
	// move with no network activity at all.
	glib.TimeoutAdd(1000, func() bool {
		if h.visible {
			h.overlay.maintain(h.deps.Config().HUD.ClickThrough)
			h.update()
		}
		if err := h.deps.MaybeSave(); err != nil {
			log.Printf("state: could not save: %v", err)
		}
		return true
	})

	h.overlay.ready()
}

// applyPlacement updates the platform window geometry and opacity. Both backends
// support applying it to a live surface, so margins can be edited while playing.
//
// All four edges are anchored, so the surface covers the monitor and the origin
// every widget position is measured from is the top-left of the screen (inset by
// the margins). A surface sized to its content could not do that: a group at
// x=2340 would stretch it, and one group's text growing would move the others.
func (h *hud) applyPlacement(cfg *Config) {
	h.overlay.applyPlacement(cfg)
}

// rebuildWidgets replaces the widget tree. The status label is kept: it is the
// HUD's own, not a widget, and it is where a reload failure would be reported.
func (h *hud) rebuildWidgets(cfg *Config) {
	for _, w := range h.widgets {
		h.fixed.Remove(w.w.Root())
	}
	h.widgets = buildWidgets(cfg)
	for _, w := range h.widgets {
		root := w.w.Root()
		// The group class carries the font overrides. Added to the group's root so
		// one rule covers a bare label and a box of rows alike.
		if styled, ok := root.(interface{ AddCSSClass(string) }); ok {
			styled.AddCSSClass(render.GroupClass(w.name))
		}
		h.fixed.Put(root, float64(w.place.X), float64(w.place.Y))
	}
	h.widgetSig = render.WidgetSignature(cfg)
}

// applyConfig re-applies everything the HUD can change without a restart.
func (h *hud) applyConfig(cfg *Config) {
	if h.window == nil {
		return
	}
	h.css.LoadFromData(render.StyleSheet(cfg))
	h.applyPlacement(cfg)
	if h.visible {
		h.applyMappedOverlay()
	}
	if render.WidgetSignature(cfg) != h.widgetSig {
		h.rebuildWidgets(cfg)
		// Re-centre what asked to be centred. A rebuilt group is put back at its
		// configured x/y, so without this, editing the map's scale resized it and
		// left it where the file said - which reads as centring being broken rather
		// than as it not having run yet.
		if h.monitorW > 0 {
			h.centreGroups(h.monitorW, h.monitorH)
		}
	}
	// The status label is the HUD's own rather than a widget, so a rebuild does not
	// reposition it.
	h.fixed.Move(h.status, float64(cfg.Widget.Status.X), float64(cfg.Widget.Status.Y))
	if h.visible {
		h.update()
	}
}

// applyVisibility maps or unmaps the surface, and pins it to the right monitor on
// the way in.
//
// Hiding is a real unmap rather than an empty window or zero opacity. A layer
// surface that exists is a surface the compositor composites, and one that
// exists at the overlay layer is one that can still take the odd event or show up
// in a screenshot; there is no reason to keep it around when there is nothing to
// say.
func (h *hud) applyVisibility(state hudVisibility) {
	if h.window == nil {
		return
	}
	if !state.Visible {
		if h.visible {
			h.visible = false
			h.window.SetVisible(false)
		}
		return
	}

	want := h.wantMonitor(state)
	if h.visible && want != h.monitorPinned {
		// Re-pointing a mapped native surface is backend-sensitive, so use the
		// reliable sequence on both platforms: unmap, re-point, map again.
		h.visible = false
		h.window.SetVisible(false)
	}
	if !h.visible {
		monitor := h.pinMonitor(want)
		// Tell the window how big it is about to be BEFORE the first frame.
		//
		// Hiding destroys the surface and showing recreates it, so on every remap
		// GTK has to lay out the groups once before the compositor's configure
		// arrives with the monitor's real size. Without a default size that first
		// layout happens inside a window a few hundred pixels wide: labels get less
		// width than they asked for and come out squeezed, groups placed near the
		// right edge land nowhere near where they belong, and it all snaps into place
		// a moment later when the configure lands. Reported as text being "smushed
		// and on the wrong coordinates" for about a second after every switch back to
		// the game's workspace.
		h.sizeToMonitor(monitor)
		// Render before mapping, so the surface is sized to real content rather
		// than appearing empty and then jumping.
		h.update()
		// Set before Present, because Present emits the map signal synchronously
		// and the click-through handler on it needs to know the surface is wanted.
		h.visible = true
		h.window.Present()
		return
	}
	h.update()
}

// wantMonitor resolves which output the surface belongs on: an explicitly
// configured connector always wins, and "auto" follows the game.
func (h *hud) wantMonitor(state hudVisibility) string {
	if want := h.deps.Config().HUD.Monitor; want != "" && want != "auto" {
		return want
	}
	return state.Monitor
}

// pinMonitor points the surface at one output, or asks the backend for its
// default. It returns the output resolved for sizing, or nil when the backend
// deliberately leaves the choice to the compositor.
//
// An unknown connector is a warning rather than a failure: losing the whole HUD
// because a connector got renamed after a cable swap would be a poor trade. The
// warning is said once per name, since this now runs on every show.
func (h *hud) pinMonitor(want string) *gdk.Monitor {
	h.monitorPinned = want
	monitors := allMonitors()
	if want == "" {
		monitor := h.overlay.defaultMonitor(monitors)
		h.overlay.setMonitor(monitor)
		return monitor
	}
	monitor := h.monitorByName(monitors, want)
	if monitor != nil {
		h.overlay.setMonitor(monitor)
		return monitor
	}
	if h.warnedMonitors == nil {
		h.warnedMonitors = map[string]bool{}
	}
	if !h.warnedMonitors[want] {
		h.warnedMonitors[want] = true
		log.Printf("hud: no monitor named %q (found: %s); using the platform default",
			want, strings.Join(h.monitorNames(monitors), ", "))
	}
	h.monitorPinned = ""
	monitor = h.overlay.defaultMonitor(monitors)
	h.overlay.setMonitor(monitor)
	return monitor
}

// allMonitors is every output GDK knows about, in its order.
func allMonitors() []*gdk.Monitor {
	display := gdk.DisplayGetDefault()
	if display == nil {
		return nil
	}
	list := display.Monitors()
	out := make([]*gdk.Monitor, 0, list.NItems())
	for i := uint(0); i < list.NItems(); i++ {
		object := list.Item(i)
		if object == nil {
			continue
		}
		if monitor, ok := object.Cast().(*gdk.Monitor); ok {
			out = append(out, monitor)
		}
	}
	return out
}

func (h *hud) monitorByName(monitors []*gdk.Monitor, want string) *gdk.Monitor {
	if want == "" {
		return nil
	}
	for _, monitor := range monitors {
		if h.overlay.monitorName(monitor) == want {
			return monitor
		}
	}
	return nil
}

// monitorNames is for the "no monitor named X" warning. The Win32 backend
// supplies native display device names when GDK has no connector string.
func (h *hud) monitorNames(monitors []*gdk.Monitor) []string {
	names := make([]string, 0, len(monitors))
	for _, monitor := range monitors {
		if name := h.overlay.monitorName(monitor); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// sizeToMonitor sets the window's default size to the output it is about to appear
// on, so the first frame after a remap is laid out at the size it will keep.
//
// With no output resolved - "compositor's choice", or a connector GDK does not
// know - the first one is used as a guess. On a mismatched pair of monitors that
// guess can be wrong, but wrong is no worse than the absent default it replaces:
// either way the compositor's configure corrects it, and the guess is right
// whenever the outputs match, which is most of the time.
func (h *hud) sizeToMonitor(monitor *gdk.Monitor) {
	if monitor == nil {
		if monitors := allMonitors(); len(monitors) > 0 {
			monitor = monitors[0]
		}
	}
	if monitor == nil {
		return
	}
	geometry := monitor.Geometry()
	if geometry == nil {
		return
	}
	width, height := geometry.Width(), geometry.Height()
	if width > 0 && height > 0 {
		h.monitorW, h.monitorH = width, height
		h.window.SetDefaultSize(width, height)
		h.centreGroups(width, height)
	}
}

// centreGroups puts the groups that asked to be centred in the middle of the
// monitor, which cannot be done from the config: a coordinate in a file would have
// to be recomputed by hand for every monitor and every cell size, and the map is
// a thousand pixels wide at one setting and eight hundred at another.
//
// Done here because this is where the monitor's size becomes known - it is also why
// it is re-done on every remap, since the HUD can move between monitors.
func (h *hud) centreGroups(width, height int) {
	for _, placed := range h.widgets {
		centred, ok := placed.w.(interface {
			Centered() bool
			NaturalSize() (int, int)
			CenterOffset() (int, int)
		})
		if !ok || !centred.Centered() {
			continue
		}
		w, hh := centred.NaturalSize()
		// Centred, then nudged. The offset is added after the centring rather than
		// replacing it so it survives everything centring exists to absorb: change
		// the scale, crop the radius, plug in a different monitor, and "40 pixels
		// left of centre" still means that. An x/y would have to be recomputed.
		dx, dy := centred.CenterOffset()
		x, y := (width-w)/2+dx, (height-hh)/2+dy
		h.fixed.Move(placed.w.Root(), float64(max(x, 0)), float64(max(y, 0)))
	}
}

// applyMappedOverlay installs the platform's native overlay styles after the
// surface exists. This includes click-through when configured.
//
// A map signal does not guarantee that the backend's native surface handle is
// available synchronously. The retry gives up if the HUD is hidden again, so a
// hide cannot leave an idle callback spinning.
func (h *hud) applyMappedOverlay() {
	if h.overlay.applyMapped(h.deps.Config().HUD.ClickThrough) {
		// GDK can still normalize native window styles after emitting map. Apply
		// once more from idle so Win32 keeps TOOLWINDOW/NOACTIVATE and Wayland
		// keeps its input region after the map transaction settles.
		glib.IdleAdd(func() bool {
			if h.visible {
				h.overlay.applyMapped(h.deps.Config().HUD.ClickThrough)
			}
			return false
		})
		return
	}
	glib.IdleAdd(func() bool {
		if !h.visible {
			return false
		}
		// Keep trying until GDK exposes the native surface.
		return !h.overlay.applyMapped(h.deps.Config().HUD.ClickThrough)
	})
}

// update renders one frame from a freshly derived view. Runs on the GTK main
// thread, so it touches no locks beyond the store's own.
func (h *hud) update() {
	view := h.deps.Derive(time.Now())

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
		w.w.Update(view)
		// The hand toggle is applied AFTER the widget's own decision, so it wins.
		// Doing it here rather than inside each widget keeps every one of them
		// ignorant of it: a widget decides whether it has anything to say, and this
		// decides whether you want to hear it.
		if h.deps.GroupHidden(w.name) {
			if visible, ok := w.w.Root().(interface{ SetVisible(bool) }); ok {
				visible.SetVisible(false)
			}
		}
	}
}
