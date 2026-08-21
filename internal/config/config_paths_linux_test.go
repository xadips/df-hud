//go:build linux

package config

import (
	"path/filepath"
	"testing"
)

func TestLinuxDefaultPathsUseXDGDirectories(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-config")
	t.Setenv("XDG_DATA_HOME", "/tmp/xdg-data")

	if got, want := defaultConfigPath(),
		filepath.Join("/tmp/xdg-config", "df-hud", "config.toml"); got != want {
		t.Errorf("defaultConfigPath() = %q, want %q", got, want)
	}
	if got, want := defaultDataDir(),
		filepath.Join("/tmp/xdg-data", "df-hud"); got != want {
		t.Errorf("defaultDataDir() = %q, want %q", got, want)
	}
}
