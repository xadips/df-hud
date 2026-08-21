//go:build windows

package desktop

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                     = windows.NewLazySystemDLL("user32.dll")
	procGetWindowTextLengthW   = user32.NewProc("GetWindowTextLengthW")
	procGetWindowTextW         = user32.NewProc("GetWindowTextW")
	procMonitorFromWindow      = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW        = user32.NewProc("GetMonitorInfoW")
	procKeybdEvent             = user32.NewProc("keybd_event")
	enumDesktopWindowsCallback = windows.NewCallback(enumDesktopWindow)
)

type windowsDesktopClient struct{}

// NewClient returns the native Windows desktop integration.
func NewClient() Client { return windowsDesktopClient{} }

// CanStartRun reports whether the matched game window is foreground.
func CanStartRun(place windowPlacement) bool {
	return place.Known && place.ForegroundRule && place.OnActiveWorkspace
}

func desktopCanStartRun(place windowPlacement) bool { return CanStartRun(place) }

type windowsWindow struct {
	handle windows.HWND
	class  string
	title  string
	pid    uint32
}

type enumWindowsState struct {
	windows []windowsWindow
}

var (
	enumWindowsMu    sync.Mutex
	activeWindowScan *enumWindowsState
)

func enumDesktopWindow(hwnd windows.HWND, param uintptr) uintptr {
	if !windows.IsWindowVisible(hwnd) {
		return 1
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err != nil || pid == 0 {
		return 1
	}
	state := activeWindowScan
	if state == nil {
		return 0
	}
	state.windows = append(state.windows, windowsWindow{
		handle: hwnd,
		class:  windowsWindowClass(hwnd),
		title:  windowsWindowTitle(hwnd),
		pid:    pid,
	})
	return 1
}

func enumerateDesktopWindows(ctx context.Context) ([]windowsWindow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// EnumWindows invokes the callback synchronously. Serialize scans and expose
	// the current collector through package state rather than round-tripping a Go
	// pointer through LPARAM, which is both unnecessary and rejected by vet.
	enumWindowsMu.Lock()
	defer enumWindowsMu.Unlock()
	state := enumWindowsState{}
	activeWindowScan = &state
	defer func() { activeWindowScan = nil }()
	if err := windows.EnumWindows(enumDesktopWindowsCallback, nil); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return state.windows, nil
}

func windowsWindowClass(hwnd windows.HWND) string {
	buffer := make([]uint16, 256)
	n, err := windows.GetClassName(hwnd, &buffer[0], int32(len(buffer)))
	if err != nil || n <= 0 {
		return ""
	}
	return windows.UTF16ToString(buffer[:n])
}

func windowsWindowTitle(hwnd windows.HWND) string {
	length, _, _ := procGetWindowTextLengthW.Call(uintptr(hwnd))
	if length == 0 {
		return ""
	}
	buffer := make([]uint16, int(length)+1)
	n, _, _ := procGetWindowTextW.Call(
		uintptr(hwnd),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
	)
	if n == 0 {
		return ""
	}
	return windows.UTF16ToString(buffer[:n])
}

func (windowsDesktopClient) GameWindow(ctx context.Context, pid int, match windowMatch) (windowPlacement, error) {
	windowsList, err := enumerateDesktopWindows(ctx)
	if err != nil {
		return windowPlacement{}, err
	}
	return findWindowsGameWindow(windowsList, windows.GetForegroundWindow(), pid, match), nil
}

func findWindowsGameWindow(windowsList []windowsWindow, foreground windows.HWND, pid int, match windowMatch) windowPlacement {
	launcher := false
	var launcherAddress string
	noteLauncher := func(window windowsWindow) {
		launcher = true
		if launcherAddress == "" && match.isLauncherDialog(window.title) {
			launcherAddress = windowsWindowAddress(window.handle)
		}
	}
	place := func(window windowsWindow, matchedBy string) windowPlacement {
		return windowPlacement{
			Known:             true,
			Class:             window.class,
			Title:             window.title,
			Address:           windowsWindowAddress(window.handle),
			Monitor:           windowsWindowMonitor(window.handle),
			OnActiveWorkspace: window.handle != 0 && window.handle == foreground,
			ForegroundRule:    true,
			MatchedBy:         matchedBy,
		}
	}

	if pid > 0 {
		for _, window := range windowsList {
			if window.pid != uint32(pid) {
				continue
			}
			if match.ignored(window.title) {
				noteLauncher(window)
				continue
			}
			return place(window, "process id")
		}
	}
	if want := normaliseClass(match.Class); want != "" {
		for _, window := range windowsList {
			if normaliseClass(window.class) != want {
				continue
			}
			if match.ignored(window.title) {
				noteLauncher(window)
				continue
			}
			return place(window, "window class")
		}
	}
	return windowPlacement{LauncherOnly: launcher, LauncherAddress: launcherAddress}
}

func windowsWindowAddress(hwnd windows.HWND) string {
	return fmt.Sprintf("0x%x", uintptr(hwnd))
}

type windowsRect struct {
	left, top, right, bottom int32
}

type windowsMonitorInfo struct {
	size    uint32
	monitor windowsRect
	work    windowsRect
	flags   uint32
	device  [32]uint16
}

func windowsWindowMonitor(hwnd windows.HWND) string {
	const monitorDefaultToNearest = 2
	monitor, _, _ := procMonitorFromWindow.Call(uintptr(hwnd), monitorDefaultToNearest)
	if monitor == 0 {
		return ""
	}
	info := windowsMonitorInfo{size: uint32(unsafe.Sizeof(windowsMonitorInfo{}))}
	ok, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return ""
	}
	return windows.UTF16ToString(info.device[:])
}

func (windowsDesktopClient) ActiveAddress(ctx context.Context) (string, bool) {
	if ctx.Err() != nil {
		return "", false
	}
	hwnd := windows.GetForegroundWindow()
	if hwnd == 0 {
		return "", false
	}
	return windowsWindowAddress(hwnd), true
}

func (windowsDesktopClient) SendKey(ctx context.Context, key, address string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	hwnd, err := parseWindowsWindowAddress(address)
	if err != nil {
		return err
	}
	if !windows.IsWindow(hwnd) {
		return fmt.Errorf("windows: target window %q no longer exists", address)
	}
	if foreground := windows.GetForegroundWindow(); foreground != hwnd {
		return fmt.Errorf("windows: target window %q is not in the foreground", address)
	}
	vk, ok := windowsVirtualKey(key)
	if !ok {
		return fmt.Errorf("windows: unsupported key name %q", key)
	}
	const keyeventfKeyup = 0x0002
	procKeybdEvent.Call(uintptr(vk), 0, 0, 0)
	procKeybdEvent.Call(uintptr(vk), 0, keyeventfKeyup, 0)
	return nil
}

func parseWindowsWindowAddress(address string) (windows.HWND, error) {
	if !strings.HasPrefix(address, "0x") {
		return 0, fmt.Errorf("windows: %q is not a window handle", address)
	}
	value, err := strconv.ParseUint(address[2:], 16, 64)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("windows: %q is not a window handle", address)
	}
	return windows.HWND(uintptr(value)), nil
}

func windowsVirtualKey(key string) (uint16, bool) {
	upper := strings.ToUpper(strings.TrimSpace(key))
	if len(upper) == 1 {
		c := upper[0]
		if c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			return uint16(c), true
		}
	}
	if strings.HasPrefix(upper, "F") {
		if n, err := strconv.Atoi(upper[1:]); err == nil && n >= 1 && n <= 24 {
			return uint16(0x70 + n - 1), true
		}
	}
	keys := map[string]uint16{
		"BACKSPACE": 0x08,
		"TAB":       0x09,
		"RETURN":    0x0D,
		"ENTER":     0x0D,
		"SHIFT":     0x10,
		"CONTROL":   0x11,
		"CTRL":      0x11,
		"ALT":       0x12,
		"ESCAPE":    0x1B,
		"ESC":       0x1B,
		"SPACE":     0x20,
		"PAGEUP":    0x21,
		"PAGEDOWN":  0x22,
		"END":       0x23,
		"HOME":      0x24,
		"LEFT":      0x25,
		"UP":        0x26,
		"RIGHT":     0x27,
		"DOWN":      0x28,
		"INSERT":    0x2D,
		"DELETE":    0x2E,
	}
	vk, ok := keys[upper]
	return vk, ok
}

func (windowsDesktopClient) Windows(ctx context.Context) ([]desktopWindow, error) {
	windowsList, err := enumerateDesktopWindows(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]desktopWindow, 0, len(windowsList))
	for _, window := range windowsList {
		out = append(out, desktopWindow{
			Class: window.class,
			Title: window.title,
			PID:   int(window.pid),
		})
	}
	return out, nil
}
