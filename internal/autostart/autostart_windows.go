//go:build windows

package autostart

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

const (
	runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	valueName  = "df-hud"
)

func Available() bool { return true }

func Enabled() (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err == nil {
		defer key.Close()
		if _, _, valueErr := key.GetStringValue(valueName); valueErr == nil {
			return true, nil
		} else if !errors.Is(valueErr, registry.ErrNotExist) {
			return false, valueErr
		}
	} else if !errors.Is(err, registry.ErrNotExist) {
		return false, err
	}
	_, err = os.Stat(legacyShortcutPath())
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

func SetEnabled(enabled bool) error {
	if !enabled {
		key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
		if err == nil {
			if deleteErr := key.DeleteValue(valueName); deleteErr != nil &&
				!errors.Is(deleteErr, registry.ErrNotExist) {
				key.Close()
				return deleteErr
			}
			key.Close()
		} else if !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		if err := os.Remove(legacyShortcutPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}

	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if err := key.SetStringValue(valueName, quoteExecutable(executable)); err != nil {
		return err
	}
	if err := os.Remove(legacyShortcutPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Reconcile migrates the old Startup-folder shortcut and refreshes an existing
// registry entry to the executable that is running now. Launching a newly
// extracted release once therefore updates future logins to that release.
func Reconcile() error {
	enabled, err := Enabled()
	if err != nil || !enabled {
		return err
	}
	return SetEnabled(true)
}

func legacyShortcutPath() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			appData = dir
		}
	}
	return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "df-hud.lnk")
}

func quoteExecutable(path string) string { return `"` + path + `"` }
