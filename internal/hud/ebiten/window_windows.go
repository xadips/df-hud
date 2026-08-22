//go:build windows && !nolayershell

package ebitenhud

import (
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowTitle             = "df-hud"
	windowInset             = 1
	ebitengineWindowClass   = "GLFW30"
	monitorInfoPrimary      = 1
	monitorDefaultToNearest = 2
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	shcore                  = windows.NewLazySystemDLL("shcore.dll")
	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")
	procGetWindowRect       = user32.NewProc("GetWindowRect")
	procMonitorFromWindow   = user32.NewProc("MonitorFromWindow")
	procGetDpiForMonitor    = shcore.NewProc("GetDpiForMonitor")
	enumeratedMonitors      []desktopMonitor
	enumMonitorCallback     = windows.NewCallback(appendMonitor)
	enumHUDWindowCallback   = windows.NewCallback(selectHUDWindow)

	enumHUDWindowMu   sync.Mutex
	enumHUDWindowPID  uint32
	enumHUDWindowBest windows.HWND
	enumHUDWindowArea int64
)

type winRect struct {
	left, top, right, bottom int32
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
	dpi           int
	primary       bool
}

func appendMonitor(handle, _, _, _ uintptr) uintptr {
	info := monitorInfo{size: uint32(unsafe.Sizeof(monitorInfo{}))}
	ok, _, _ := procGetMonitorInfoW.Call(handle, uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return 1
	}
	enumeratedMonitors = append(enumeratedMonitors, monitorFromInfo(info, monitorDPI(handle)))
	return 1
}

func monitorFromInfo(info monitorInfo, dpi int) desktopMonitor {
	return desktopMonitor{
		name: windows.UTF16ToString(info.device[:]),
		left: int(info.monitor.left), top: int(info.monitor.top),
		width:   int(info.monitor.right - info.monitor.left),
		height:  int(info.monitor.bottom - info.monitor.top),
		dpi:     dpi,
		primary: info.flags&monitorInfoPrimary != 0,
	}
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

func selectHUDWindow(hwnd windows.HWND, _ uintptr) uintptr {
	if !windows.IsWindowVisible(hwnd) {
		return 1
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err != nil ||
		pid != enumHUDWindowPID {
		return 1
	}
	class := make([]uint16, 64)
	n, err := windows.GetClassName(hwnd, &class[0], int32(len(class)))
	if err != nil || windows.UTF16ToString(class[:n]) != ebitengineWindowClass {
		return 1
	}
	var rect winRect
	ok, _, _ := procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect)))
	if ok == 0 {
		return 1
	}
	area := int64(rect.right-rect.left) * int64(rect.bottom-rect.top)
	if area > enumHUDWindowArea {
		enumHUDWindowBest = hwnd
		enumHUDWindowArea = area
	}
	return 1
}

func hudWindowHandle() (windows.HWND, error) {
	enumHUDWindowMu.Lock()
	defer enumHUDWindowMu.Unlock()

	enumHUDWindowPID = uint32(os.Getpid())
	enumHUDWindowBest = 0
	enumHUDWindowArea = -1
	defer func() {
		enumHUDWindowPID = 0
		enumHUDWindowBest = 0
		enumHUDWindowArea = 0
	}()

	if err := windows.EnumWindows(enumHUDWindowCallback, nil); err != nil {
		return 0, err
	}
	if enumHUDWindowBest == 0 {
		return 0, errors.New("no visible Ebitengine top-level window was found")
	}
	return enumHUDWindowBest, nil
}

func hudWindowMonitor() (desktopMonitor, error) {
	hwnd, err := hudWindowHandle()
	if err != nil {
		return desktopMonitor{}, err
	}
	handle, _, callErr := procMonitorFromWindow.Call(uintptr(hwnd), monitorDefaultToNearest)
	if handle == 0 {
		return desktopMonitor{}, fmt.Errorf("MonitorFromWindow: %w", callErr)
	}
	info := monitorInfo{size: uint32(unsafe.Sizeof(monitorInfo{}))}
	ok, _, callErr := procGetMonitorInfoW.Call(handle, uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return desktopMonitor{}, fmt.Errorf("GetMonitorInfoW: %w", callErr)
	}
	return monitorFromInfo(info, monitorDPI(handle)), nil
}

func desktopMonitorIndex(monitors []desktopMonitor, target desktopMonitor) int {
	ordered := append([]desktopMonitor(nil), monitors...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].primary != ordered[j].primary {
			return ordered[i].primary
		}
		return strings.ToLower(ordered[i].name) < strings.ToLower(ordered[j].name)
	})
	for i, monitor := range ordered {
		if strings.EqualFold(monitor.name, target.name) {
			return i
		}
	}
	return 0
}

func monitorWindowSize(m desktopMonitor) (int, int) {
	dpi := m.dpi
	if dpi <= 0 {
		dpi = 96
	}
	width := int(math.Round(float64(m.width) * 96 / float64(dpi)))
	height := int(math.Round(float64(m.height) * 96 / float64(dpi)))
	return width - 2*windowInset, height - 2*windowInset
}

func monitorDPI(handle uintptr) int {
	const effectiveDPI = 0
	var x, y uint32
	result, _, _ := procGetDpiForMonitor.Call(
		handle, effectiveDPI, uintptr(unsafe.Pointer(&x)), uintptr(unsafe.Pointer(&y)))
	if int32(result) < 0 || x == 0 {
		return 96
	}
	return int(x)
}
