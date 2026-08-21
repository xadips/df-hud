//go:build windows

package config

import (
	"path/filepath"
	"testing"
)

func TestWindowsDefaultPathsUseAppData(t *testing.T) {
	t.Setenv("APPDATA", `C:\Users\tester\AppData\Roaming`)
	t.Setenv("LOCALAPPDATA", `C:\Users\tester\AppData\Local`)

	if got, want := defaultConfigPath(), filepath.Join(
		`C:\Users\tester\AppData\Roaming`, "df-hud", "config.toml"); got != want {
		t.Errorf("defaultConfigPath() = %q, want %q", got, want)
	}
	if got, want := defaultDataDir(), filepath.Join(
		`C:\Users\tester\AppData\Local`, "df-hud"); got != want {
		t.Errorf("defaultDataDir() = %q, want %q", got, want)
	}
}
