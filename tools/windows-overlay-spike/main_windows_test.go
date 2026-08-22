//go:build windows

package main

import "testing"

func TestMonitorSelectionAndMixedCoordinateOffset(t *testing.T) {
	monitors := []desktopMonitor{
		{name: `\\.\DISPLAY1`, left: 0, top: 0, width: 2560, height: 1440, primary: true},
		{name: `\\.\DISPLAY2`, left: -1920, top: 180, width: 1920, height: 1080},
	}

	primary, ok := primaryMonitor(monitors)
	if !ok || primary.name != `\\.\DISPLAY1` {
		t.Fatalf("primary monitor = %+v, %v", primary, ok)
	}
	target, ok := monitorByName(monitors, `\\.\display2`)
	if !ok {
		t.Fatal("case-insensitive Win32 monitor name was not found")
	}
	if x, y := monitorOffset(monitors[0], target); x != -1920 || y != 180 {
		t.Errorf("mixed-coordinate offset = %d,%d, want -1920,180", x, y)
	}
	if x, y := monitorOffset(target, monitors[0]); x != 1920 || y != -180 {
		t.Errorf("reverse mixed-coordinate offset = %d,%d, want 1920,-180", x, y)
	}
}
