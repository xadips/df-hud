//go:build windows

package main

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestFindWindowsGameWindowTracksForeground(t *testing.T) {
	windowsList := []windowsWindow{
		{handle: 0x10, class: "Other", title: "Other", pid: 10},
		{handle: 0x20, class: "UnityWndClass", title: "Dead Frontier", pid: 42},
	}
	got := findWindowsGameWindow(windowsList, windows.HWND(0x20), 42, windowMatch{})
	if !got.Known || !got.ForegroundRule || !got.OnActiveWorkspace {
		t.Fatalf("foreground game placement = %+v", got)
	}
	if got.Address != "0x20" || got.MatchedBy != "process id" {
		t.Fatalf("placement identity = %+v", got)
	}

	got = findWindowsGameWindow(windowsList, windows.HWND(0x10), 42, windowMatch{})
	if got.OnActiveWorkspace {
		t.Fatal("the game must not be visible under the foreground rule after alt-tab")
	}
	if desktopCanStartRun(got) {
		t.Fatal("an unfocused game window is not evidence that play started")
	}

	got = findWindowsGameWindow(windowsList, windows.HWND(0x20), 42, windowMatch{})
	if !desktopCanStartRun(got) {
		t.Fatal("the real foreground game window should permit the polling fallback")
	}
}

func TestFindWindowsGameWindowReportsLauncher(t *testing.T) {
	windowsList := []windowsWindow{
		{handle: 0x30, class: "DeadFrontier.exe", title: "Dead Frontier Configuration", pid: 42},
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
