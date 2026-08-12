package main

import (
	"encoding/json"
	"testing"
)

// The shapes below are trimmed from real `hyprctl -j` output. The two details
// that matter and are easy to get wrong: a window's "monitor" is a numeric ID
// rather than a connector name, and a special workspace is tracked in its own
// field on the monitor.
const hyprClientsJSON = `[
  {"class":"kitty","initialClass":"kitty","title":"~","pid":2695,"monitor":0,
   "mapped":true,"workspace":{"id":2,"name":"2"},"visible":true},
  {"class":"deadfrontier.exe","initialClass":"deadfrontier.exe","title":"Dead Frontier",
   "pid":77863,"monitor":1,"mapped":true,"workspace":{"id":13,"name":"13"},"visible":true},
  {"class":"firefox","initialClass":"firefox","title":"Dead Frontier - Inner City",
   "pid":1607,"monitor":0,"mapped":true,"workspace":{"id":1,"name":"1"},"visible":true}
]`

const hyprMonitorsJSON = `[
  {"id":0,"name":"DP-1","focused":false,
   "activeWorkspace":{"id":1,"name":"1"},"specialWorkspace":{"id":0,"name":""}},
  {"id":1,"name":"DP-2","focused":true,
   "activeWorkspace":{"id":13,"name":"13"},"specialWorkspace":{"id":0,"name":""}}
]`

func decodeHyprFixtures(t *testing.T) ([]hyprWindow, []hyprMonitor) {
	t.Helper()
	var windows []hyprWindow
	if err := json.Unmarshal([]byte(hyprClientsJSON), &windows); err != nil {
		t.Fatal(err)
	}
	var monitors []hyprMonitor
	if err := json.Unmarshal([]byte(hyprMonitorsJSON), &monitors); err != nil {
		t.Fatal(err)
	}
	return windows, monitors
}

func TestFindGameWindowByPID(t *testing.T) {
	windows, monitors := decodeHyprFixtures(t)

	got := findGameWindow(windows, monitors, 77863, "DeadFrontier.exe")
	if !got.Known {
		t.Fatal("the game's window should have been found")
	}
	if got.MatchedBy != "process id" {
		t.Errorf("MatchedBy = %q, want the pid to win when it matches", got.MatchedBy)
	}
	if got.Monitor != "DP-2" {
		t.Errorf("Monitor = %q, want the connector name rather than the numeric id", got.Monitor)
	}
	if got.WorkspaceName != "13" || !got.OnActiveWorkspace {
		t.Errorf("workspace %q, active %v; DP-2 is showing 13", got.WorkspaceName, got.OnActiveWorkspace)
	}
}

// The PID a compositor reports for an XWayland window is not guaranteed to be the
// process /proc found, so the class is the fallback that has to work.
func TestFindGameWindowByClassWhenThePIDIsWrong(t *testing.T) {
	windows, monitors := decodeHyprFixtures(t)

	got := findGameWindow(windows, monitors, 999999, "DeadFrontier.exe")
	if !got.Known || got.MatchedBy != "window class" {
		t.Fatalf("got %+v, want a class match", got)
	}
	// Wine reports the class lowercased while the config carries the real
	// executable name, so the comparison has to normalise both.
	if got.Class != "deadfrontier.exe" {
		t.Errorf("Class = %q", got.Class)
	}
}

// A window on a workspace that is not the one being shown is the case the whole
// feature exists for.
func TestFindGameWindowOnAnInactiveWorkspace(t *testing.T) {
	windows, monitors := decodeHyprFixtures(t)
	// DP-1 is showing workspace 1; kitty is on 2.
	got := findGameWindow(windows, monitors, 2695, "")
	if !got.Known {
		t.Fatal("the window should have been found")
	}
	if got.OnActiveWorkspace {
		t.Error("workspace 2 is not what DP-1 is showing, so this must be false")
	}
}

// The one heuristic that looks most convenient is the one that would pick the
// wrong window: the bridge userscript means a browser tab on the game's site is
// usually open, and its title matches better than the game's does.
func TestFindGameWindowNeverMatchesOnTitle(t *testing.T) {
	windows, monitors := decodeHyprFixtures(t)

	got := findGameWindow(windows, monitors, 0, "SomethingElse.exe")
	if got.Known {
		t.Errorf("matched %q (%q) with no pid and no class match; a title must never be enough",
			got.Class, got.Title)
	}
}

func TestFindGameWindowHandlesSpecialWorkspaces(t *testing.T) {
	windows := []hyprWindow{{
		Class: "deadfrontier.exe", PID: 5, Monitor: 0, Mapped: true,
		Workspace: struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}{ID: -98, Name: "special:magic"},
	}}
	monitors := []hyprMonitor{{
		ID: 0, Name: "DP-1",
		ActiveWorkspace: struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}{ID: 1, Name: "1"},
		SpecialWorkspace: struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}{ID: -98, Name: "special:magic"},
	}}

	// Comparing a special workspace against activeWorkspace would say "hidden"
	// while it is plainly on screen.
	if got := findGameWindow(windows, monitors, 5, ""); !got.OnActiveWorkspace {
		t.Errorf("an open special workspace is on screen: %+v", got)
	}
	monitors[0].SpecialWorkspace.ID = 0
	if got := findGameWindow(windows, monitors, 5, ""); got.OnActiveWorkspace {
		t.Error("with the special workspace closed the window is not on screen")
	}
}

func TestFindGameWindowUnknownMonitor(t *testing.T) {
	windows, _ := decodeHyprFixtures(t)
	// No monitor list at all: the window is still identified, but nothing is
	// claimed about where it is - which must not read as "hidden".
	got := findGameWindow(windows, nil, 77863, "")
	if !got.Known {
		t.Fatal("the window is still identifiable without the monitor list")
	}
	if got.Monitor != "" {
		t.Errorf("Monitor = %q, want empty when the monitor is unknown", got.Monitor)
	}
}

func TestNormaliseClass(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"DeadFrontier.exe", "deadfrontier"},
		{"deadfrontier.exe", "deadfrontier"},
		{" DeadFrontier.EXE ", "deadfrontier"},
		{"", ""},
	} {
		if got := normaliseClass(tc.in); got != tc.want {
			t.Errorf("normaliseClass(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
