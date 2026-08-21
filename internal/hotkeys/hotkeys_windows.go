//go:build windows

package hotkeys

import (
	"context"
	"log"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	modNoRepeat = 0x4000
	pmRemove    = 0x0001
	wmHotkey    = 0x0312
)

var (
	hotkeyUser32       = windows.NewLazySystemDLL("user32.dll")
	procPeekMessageW   = hotkeyUser32.NewProc("PeekMessageW")
	procRegisterHotKey = hotkeyUser32.NewProc("RegisterHotKey")
	procUnregisterKey  = hotkeyUser32.NewProc("UnregisterHotKey")
)

type messagePoint struct {
	x int32
	y int32
}

// Mirrors Win32 MSG. Go inserts the required padding before pointer-sized
// WPARAM on amd64.
type hotkeyMessage struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	point   messagePoint
	private uint32
}

func (h *Hotkeys) run(ctx context.Context) {
	// RegisterHotKey and WM_HOTKEY belong to the calling thread's message queue.
	// Pin the goroutine so registration, dispatch and cleanup all use that same
	// queue.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// PeekMessage creates the thread queue before the first registration.
	var message hotkeyMessage
	procPeekMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0, 0)

	active := make(map[int]registration)
	var previous Config
	previousFocused := false
	havePrevious := false
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	defer unregisterAll(active)

	for {
		cfg := h.config()
		focused := cfg.Enabled && h.focused()
		if !havePrevious || cfg != previous || focused != previousFocused {
			unregisterAll(active)
			clear(active)
			if focused {
				registerAll(active, h.registrations(cfg))
			}
			previous = cfg
			previousFocused = focused
			havePrevious = true
		}

		drainHotkeyMessages(&message, active, h.focused)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func registerAll(active map[int]registration, registrations []registration) {
	for _, registration := range registrations {
		ok, _, callErr := procRegisterHotKey.Call(
			0,
			uintptr(registration.id),
			uintptr(registration.binding.Modifiers|modNoRepeat),
			uintptr(registration.binding.VirtualKey),
		)
		if ok == 0 {
			log.Printf("hotkeys: cannot register %s for %s: %v",
				registration.binding, registration.name, callErr)
			continue
		}
		active[registration.id] = registration
	}
}

func unregisterAll(active map[int]registration) {
	for id := range active {
		procUnregisterKey.Call(0, uintptr(id))
	}
}

func drainHotkeyMessages(message *hotkeyMessage, active map[int]registration, focused func() bool) {
	for {
		haveMessage, _, _ := procPeekMessageW.Call(
			uintptr(unsafe.Pointer(message)),
			0,
			wmHotkey,
			wmHotkey,
			pmRemove,
		)
		if haveMessage == 0 {
			return
		}
		registration, ok := active[int(message.wParam)]
		if !ok || !focused() {
			continue
		}
		registration.action()
	}
}
