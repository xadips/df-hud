//go:build windows

// windows-overlay-spike is a deliberately small release gate for Ebitengine's
// transparent Win32 overlay behavior. It does not import or launch df-hud's GTK
// frontend.
//
// It is not the rewrite spike. Raw WGL (no GLFW) lives in tools/wgl-overlay-spike.
package main

import (
	"errors"
	"flag"
	"fmt"
	"image/color"
	"log"
	"math"
	"os"
	"strings"
	"time"
	"unsafe"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/sys/windows"
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	procFindWindowW         = user32.NewProc("FindWindowW")
	procGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")
	procMonitorFromWindow   = user32.NewProc("MonitorFromWindow")
	enumeratedMonitors      []desktopMonitor
	enumMonitorCallback     = windows.NewCallback(appendMonitor)
)

const (
	spikeWindowTitle        = "df-hud Ebitengine overlay spike"
	monitorInfoPrimary      = 1
	monitorDefaultToNearest = 2
)

type winRect struct {
	left   int32
	top    int32
	right  int32
	bottom int32
}

type monitorInfo struct {
	size    uint32
	monitor winRect
	work    winRect
	flags   uint32
	device  [32]uint16
}

type desktopMonitor struct {
	name          string
	left, top     int
	width, height int
	primary       bool
}

func appendMonitor(handle, _, _, _ uintptr) uintptr {
	info := monitorInfo{size: uint32(unsafe.Sizeof(monitorInfo{}))}
	ok, _, _ := procGetMonitorInfoW.Call(handle, uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return 1
	}
	enumeratedMonitors = append(enumeratedMonitors, desktopMonitor{
		name: windows.UTF16ToString(info.device[:]),
		left: int(info.monitor.left), top: int(info.monitor.top),
		width:   int(info.monitor.right - info.monitor.left),
		height:  int(info.monitor.bottom - info.monitor.top),
		primary: info.flags&monitorInfoPrimary != 0,
	})
	return 1
}

func desktopMonitors() ([]desktopMonitor, error) {
	enumeratedMonitors = enumeratedMonitors[:0]
	ok, _, callErr := procEnumDisplayMonitors.Call(0, 0, enumMonitorCallback, 0)
	if ok == 0 {
		return nil, fmt.Errorf("EnumDisplayMonitors: %w", callErr)
	}
	return append([]desktopMonitor(nil), enumeratedMonitors...), nil
}

func monitorByName(monitors []desktopMonitor, name string) (desktopMonitor, bool) {
	for _, monitor := range monitors {
		if strings.EqualFold(monitor.name, name) {
			return monitor, true
		}
	}
	return desktopMonitor{}, false
}

func primaryMonitor(monitors []desktopMonitor) (desktopMonitor, bool) {
	for _, monitor := range monitors {
		if monitor.primary {
			return monitor, true
		}
	}
	return desktopMonitor{}, false
}

func monitorOffset(source, target desktopMonitor) (x, y int) {
	return target.left - source.left, target.top - source.top
}

func spikeWindowMonitor() (desktopMonitor, error) {
	title, err := windows.UTF16PtrFromString(spikeWindowTitle)
	if err != nil {
		return desktopMonitor{}, err
	}
	hwnd, _, callErr := procFindWindowW.Call(0, uintptr(unsafe.Pointer(title)))
	if hwnd == 0 {
		return desktopMonitor{}, fmt.Errorf("FindWindowW(%q): %w", spikeWindowTitle, callErr)
	}
	handle, _, callErr := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	if handle == 0 {
		return desktopMonitor{}, fmt.Errorf("MonitorFromWindow: %w", callErr)
	}
	info := monitorInfo{size: uint32(unsafe.Sizeof(monitorInfo{}))}
	ok, _, callErr := procGetMonitorInfoW.Call(handle, uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return desktopMonitor{}, fmt.Errorf("GetMonitorInfoW: %w", callErr)
	}
	return desktopMonitor{
		name: windows.UTF16ToString(info.device[:]),
		left: int(info.monitor.left), top: int(info.monitor.top),
		width:   int(info.monitor.right - info.monitor.left),
		height:  int(info.monitor.bottom - info.monitor.top),
		primary: info.flags&monitorInfoPrimary != 0,
	}, nil
}

type spike struct {
	face           text.Face
	target         desktopMonitor
	engineTarget   *ebiten.MonitorType
	explicitTarget bool
	inset          int
	started        time.Time
	stopAfter      time.Duration
	updates        int
	positioned     bool
	backendLogged  bool
}

func (s *spike) Update() error {
	s.updates++
	if !s.backendLogged {
		var info ebiten.DebugInfo
		ebiten.ReadDebugInfo(&info)
		log.Printf("graphics: active %s", info.GraphicsLibrary)
		s.backendLogged = true
	}
	if s.updates == 1 {
		// Ebitengine v2.9 ignores passthrough set before RunGameWithOptions on
		// some systems. Apply it in the first update, before the first draw.
		ebiten.SetWindowMousePassthrough(true)
		if s.engineTarget != nil {
			ebiten.SetMonitor(s.engineTarget)
		}
		log.Printf("first update: requested Ebitengine monitor for %s", s.target.name)
	}
	if s.updates > 1 && !s.positioned {
		current, err := spikeWindowMonitor()
		if err != nil {
			return fmt.Errorf("verify overlay placement: %w", err)
		}
		if strings.EqualFold(current.name, s.target.name) {
			s.positioned = true
			scale := 0.0
			friendlyName := "<unknown>"
			if monitor := ebiten.Monitor(); monitor != nil {
				scale = monitor.DeviceScaleFactor()
				friendlyName = monitor.Name()
			}
			log.Printf("monitor placement confirmed: %s (Ebitengine %q, scale %.2f)",
				current.name, friendlyName, scale)
		} else if s.updates >= 120 {
			return fmt.Errorf("monitor placement gate failed after two seconds: requested %s, window is on %s",
				s.target.name, current.name)
		}
	}
	// Reassert this for the lifetime of the overlay, including after a monitor
	// transition or native surface recreation.
	ebiten.SetWindowMousePassthrough(true)

	if s.stopAfter > 0 && time.Since(s.started) >= s.stopAfter {
		return ebiten.Termination
	}
	return nil
}

func (s *spike) Draw(screen *ebiten.Image) {
	// A transparent GLFW framebuffer still needs a transparent frame. Keep this
	// explicit so untouched pixels never inherit an opaque backbuffer clear.
	screen.Clear()

	elapsed := time.Since(s.started).Seconds()
	x := float32(80 + 30*math.Sin(elapsed*2))
	vector.FillRect(screen, x, 80, 360, 120, color.NRGBA{R: 40, G: 160, B: 255, A: 105}, true)
	vector.StrokeRect(screen, x, 80, 360, 120, 2, color.NRGBA{R: 255, G: 255, B: 255, A: 220}, true)

	state := "placing window"
	if s.positioned {
		state = "passthrough + topmost + unfocused"
	}
	message := fmt.Sprintf("df-hud Ebitengine overlay spike\n%s\nmonitor %s  %dx%d",
		state, s.target.name, s.target.width, s.target.height)
	options := &text.DrawOptions{}
	options.GeoM.Translate(float64(x+18), 112)
	options.ColorScale.ScaleWithColor(color.White)
	text.Draw(screen, message, s.face, options)
}

func (s *spike) Layout(_, _ int) (int, int) {
	return s.windowSize()
}

func (s *spike) windowSize() (int, int) {
	return s.target.width - 2*s.inset, s.target.height - 2*s.inset
}

func run() error {
	monitorName := flag.String("monitor", "", "Win32 monitor name, for example \\\\.\\DISPLAY2")
	duration := flag.Duration("duration", 15*time.Second, "automatic clean-shutdown delay (0 keeps running)")
	inset := flag.Int("inset", 1, "inset every window edge in pixels to preserve DWM transparency")
	autoGraphics := flag.Bool("graphics-auto", false, "diagnostic only: let Ebitengine choose instead of OpenGL")
	flag.Parse()

	monitors, err := desktopMonitors()
	if err != nil {
		return err
	}
	for _, monitor := range monitors {
		log.Printf("monitor: %s at %d,%d (%dx%d)",
			monitor.name, monitor.left, monitor.top, monitor.width, monitor.height)
	}

	target, ok := primaryMonitor(monitors)
	if !ok {
		return fmt.Errorf("Win32 topology did not identify a primary monitor")
	}
	explicitTarget := *monitorName != ""
	if *monitorName != "" {
		var found bool
		target, found = monitorByName(monitors, *monitorName)
		if !found {
			return fmt.Errorf("monitor %q was not found", *monitorName)
		}
	}
	if *inset < 0 || 2*(*inset) >= target.width || 2*(*inset) >= target.height {
		return fmt.Errorf("inset %d is invalid for monitor size %dx%d",
			*inset, target.width, target.height)
	}
	engineMonitors := ebiten.AppendMonitors(nil)
	if len(engineMonitors) == 0 {
		return errors.New("Ebitengine reported no monitors")
	}
	for i, monitor := range engineMonitors {
		width, height := monitor.Size()
		log.Printf("Ebitengine monitor %d: %q (%dx%d)", i, monitor.Name(), width, height)
	}
	engineIndex := 0
	if !target.primary {
		engineIndex = 1
	}
	if engineIndex >= len(engineMonitors) {
		return fmt.Errorf("target %s needs Ebitengine monitor %d, only %d reported",
			target.name, engineIndex, len(engineMonitors))
	}

	graphics := ebiten.GraphicsLibraryOpenGL
	if *autoGraphics {
		graphics = ebiten.GraphicsLibraryAuto
	}
	log.Printf("graphics: requested %s (Auto is diagnostic-only)", graphics)

	ebiten.SetWindowTitle(spikeWindowTitle)
	ebiten.SetWindowDecorated(false)
	ebiten.SetWindowFloating(true)
	// Configure passthrough before native window creation so GLFW can combine
	// its layered style with transparent-framebuffer setup. Update reasserts it
	// because older Ebitengine versions sometimes ignored this early request.
	ebiten.SetWindowMousePassthrough(true)
	ebiten.SetWindowSize(target.width-2*(*inset), target.height-2*(*inset))
	ebiten.SetScreenClearedEveryFrame(true)

	game := &spike{
		face:           text.NewGoXFace(basicfont.Face7x13),
		target:         target,
		engineTarget:   engineMonitors[engineIndex],
		explicitTarget: explicitTarget,
		inset:          *inset,
		started:        time.Now(),
		stopAfter:      *duration,
	}
	err = ebiten.RunGameWithOptions(game, &ebiten.RunGameOptions{
		ScreenTransparent: true,
		InitUnfocused:     true,
		SkipTaskbar:       true,
		GraphicsLibrary:   graphics,
	})
	if err != nil {
		return err
	}
	log.Print("clean shutdown")
	return nil
}

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	if err := run(); err != nil {
		log.Printf("FAILED: %v", err)
		os.Exit(1)
	}
}
