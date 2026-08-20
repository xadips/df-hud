package main

import (
	"context"
	"regexp"
	"strings"
)

// windowPlacement is the desktop's answer about the game's top-level window.
// Known false means no reliable match was possible and callers must fail open.
type windowPlacement struct {
	Known bool

	Class         string
	Title         string
	Address       string
	Workspace     int
	WorkspaceName string
	Monitor       string

	// OnActiveWorkspace is the common visibility result. On Linux it means the
	// Hyprland workspace is displayed. On Windows it means the window is the
	// foreground window, identified by ForegroundRule.
	OnActiveWorkspace bool
	ForegroundRule    bool

	MatchedBy string

	// LauncherOnly distinguishes "no matching window yet" from positive evidence
	// that every matching window is an ignored launcher dialog.
	LauncherOnly    bool
	LauncherAddress string
}

type windowMatch struct {
	Class         string
	IgnoreTitles  []string
	LauncherTitle string
}

func (m windowMatch) isLauncherDialog(title string) bool {
	want := strings.ToLower(strings.TrimSpace(m.LauncherTitle))
	return want != "" && strings.Contains(strings.ToLower(title), want)
}

func (m windowMatch) ignored(title string) bool {
	low := strings.ToLower(title)
	for _, skip := range m.IgnoreTitles {
		skip = strings.ToLower(strings.TrimSpace(skip))
		if skip != "" && strings.Contains(low, skip) {
			return true
		}
	}
	return false
}

func normaliseClass(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimSuffix(s, ".exe")
}

// desktopWindow is the portable shape used by -check-game diagnostics.
type desktopWindow struct {
	Class string
	Title string
	PID   int
}

// desktopClient is the platform runtime needed by visibility, game-key
// automation, focus checks, and diagnostics.
type desktopClient interface {
	windowQuerier
	keySender
	ActiveAddress(context.Context) (string, bool)
	Windows(context.Context) ([]desktopWindow, error)
}

// Kept under its historical name because config validation already refers to
// it. The alphabet is portable; each platform's sender maps supported names to
// its native key representation.
var hyprKeyName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
