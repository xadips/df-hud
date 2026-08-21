//go:build windows

package autostart

import (
	"path/filepath"
	"testing"
)

func TestQuoteExecutablePreservesWindowsPath(t *testing.T) {
	path := `C:\Program Files\df-hud\df-hud.exe`
	if got, want := quoteExecutable(path), `"`+path+`"`; got != want {
		t.Errorf("quoteExecutable() = %q, want %q", got, want)
	}
}

func TestLegacyShortcutUsesAppData(t *testing.T) {
	t.Setenv("APPDATA", `C:\Users\tester\AppData\Roaming`)
	want := filepath.Join(
		`C:\Users\tester\AppData\Roaming`,
		"Microsoft", "Windows", "Start Menu", "Programs", "Startup", "df-hud.lnk",
	)
	if got := legacyShortcutPath(); got != want {
		t.Errorf("legacyShortcutPath() = %q, want %q", got, want)
	}
}
