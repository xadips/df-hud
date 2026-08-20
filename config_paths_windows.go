//go:build windows

package main

import (
	"os"
	"path/filepath"
)

func defaultConfigPath() string {
	return filepath.Join(windowsAppDir("APPDATA", "Roaming"), "df-hud", "config.toml")
}

func defaultDataDir() string {
	return filepath.Join(windowsAppDir("LOCALAPPDATA", "Local"), "df-hud")
}

func windowsAppDir(env, kind string) string {
	if dir := os.Getenv(env); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "AppData", kind)
}
