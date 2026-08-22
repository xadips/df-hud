//go:build windows

package desktop

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestFindWindowsGameWindowTracksForeground(t *testing.T) {
	windowsList := []windowsWindow{
		{handle: 0x10, class: "Other", title: "Other", pid: 10, width: 800, height: 600},
		{handle: 0x20, class: "UnityWndClass", title: "Dead Frontier", pid: 42, width: 2560, height: 1440},
	}
	got := findWindowsGameWindow(windowsList, windows.HWND(0x20), 42, windowMatch{})
	if !got.Known || !got.ForegroundRule || !got.OnActiveWorkspace || !got.Foreground {
		t.Fatalf("foreground game placement = %+v", got)
	}
	if got.Address != "0x20" || got.MatchedBy != "process id" {
		t.Fatalf("placement identity = %+v", got)
	}

	got = findWindowsGameWindow(windowsList, windows.HWND(0x10), 42, windowMatch{})
	if !got.OnActiveWorkspace {
		t.Fatal("alt-tab must not hide a game window that is still visible")
	}
	if desktopCanStartRun(got) {
		t.Fatal("an unfocused game window is not evidence that play started")
	}

	got = findWindowsGameWindow(windowsList, windows.HWND(0x20), 42, windowMatch{})
	if !desktopCanStartRun(got) {
		t.Fatal("the real foreground game window should permit the polling fallback")
	}

	windowsList[1].minimized = true
	got = findWindowsGameWindow(windowsList, windows.HWND(0x10), 42, windowMatch{})
	if got.OnActiveWorkspace || !got.Minimized {
		t.Fatalf("minimized game placement = %+v", got)
	}
}

func TestFindWindowsGameWindowReportsLauncher(t *testing.T) {
	windowsList := []windowsWindow{
		{handle: 0x30, class: "DeadFrontier.exe", title: "Dead Frontier Configuration", pid: 42, width: 640, height: 480},
		{handle: 0x31, class: "ComboLBox", title: "Resolution", pid: 42, width: 180, height: 120},
	}
	match := windowMatch{
		Class:         "DeadFrontier.exe",
		IgnoreTitles:  []string{"configuration"},
		LauncherTitle: "Dead Frontier Configuration",
	}
	got := findWindowsGameWindow(windowsList, windows.HWND(0x30), 42, match)
	if got.Known || !got.LauncherOnly || got.LauncherAddress != "0x30" {
		t.Fatalf("launcher placement = %+v", got)
	}
}

func TestFindWindowsGameWindowIgnoresLauncherPopupControls(t *testing.T) {
	windowsList := []windowsWindow{
		{handle: 0x30, class: "DeadFrontier.exe", title: "Dead Frontier Configuration", pid: 42, width: 640, height: 480},
		{handle: 0x31, class: "ComboLBox", title: "Resolution", pid: 42, width: 180, height: 120},
	}
	match := windowMatch{IgnoreTitles: []string{"configuration"}}
	got := findWindowsGameWindow(windowsList, windows.HWND(0x31), 42, match)
	if got.Known || !got.LauncherOnly {
		t.Fatalf("launcher popup was accepted as the game: %+v", got)
	}

	windowsList = append(windowsList,
		windowsWindow{handle: 0x32, class: "UnityWndClass", title: "Dead Frontier",
			pid: 42, width: 2560, height: 1440})
	got = findWindowsGameWindow(windowsList, windows.HWND(0x32), 42, match)
	if !got.Known || got.Class != "UnityWndClass" {
		t.Fatalf("real Unity window was not selected over launcher controls: %+v", got)
	}
}

func TestWindowsVirtualKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		want uint16
	}{
		{"y", 0x59},
		{"Return", 0x0D},
		{"F12", 0x7B},
		{"Space", 0x20},
	} {
		got, ok := windowsVirtualKey(tc.name)
		if !ok || got != tc.want {
			t.Errorf("windowsVirtualKey(%q) = %#x, %v; want %#x, true", tc.name, got, ok, tc.want)
		}
	}
	if _, ok := windowsVirtualKey("not_a_real_key"); ok {
		t.Error("unsupported key was accepted")
	}
}
