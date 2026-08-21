//go:build windows

package tray

import (
	"context"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	trayUser32         = windows.NewLazySystemDLL("user32.dll")
	trayFindWindowExW  = trayUser32.NewProc("FindWindowExW")
	trayGetWindowPID   = trayUser32.NewProc("GetWindowThreadProcessId")
	trayIsWindow       = trayUser32.NewProc("IsWindow")
	trayPostMessageW   = trayUser32.NewProc("PostMessageW")
	trayWindowClass, _ = windows.UTF16PtrFromString("SystrayClass")
)

// startTrayPlatformMaintenance supplies the WM_NULL that Microsoft's
// TrackPopupMenu documentation requires after a notification-area menu closes.
// fyne.io/systray v1.12.2 omits it, which makes later right-clicks appear at a
// stale position or disappear immediately on Windows 11.
func startTrayPlatformMaintenance(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		var hwnd uintptr
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if hwnd == 0 || !trayWindowValid(hwnd) {
					hwnd = currentProcessTrayWindow()
				}
				if hwnd != 0 {
					// WM_NULL is deliberately harmless. Posting periodically
					// guarantees one is queued after TrackPopupMenu's modal
					// loop returns, even though the dependency exposes no
					// menu-closed callback on Windows.
					trayPostMessageW.Call(hwnd, 0, 0, 0)
				}
			}
		}
	}()
}

func trayWindowValid(hwnd uintptr) bool {
	ok, _, _ := trayIsWindow.Call(hwnd)
	return ok != 0
}

func currentProcessTrayWindow() uintptr {
	var after uintptr
	for {
		hwnd, _, _ := trayFindWindowExW.Call(
			0,
			after,
			uintptr(unsafe.Pointer(trayWindowClass)),
			0,
		)
		if hwnd == 0 {
			return 0
		}
		var pid uint32
		trayGetWindowPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid == uint32(os.Getpid()) {
			return hwnd
		}
		after = hwnd
	}
}
