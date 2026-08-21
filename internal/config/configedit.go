package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/neongreen/tomlsawyer"
)

// TrayOption is a config-backed checkbox exposed in the tray.
type TrayOption string

const (
	TrayFPSDisplay      TrayOption = "game_keys.fps_display"
	TrayDismissLauncher TrayOption = "game_keys.dismiss_launcher"
	TrayShowChallenges  TrayOption = "widget.challenges.enabled"
)

var configEditMu sync.Mutex

// SetTrayOption changes one supported boolean without rewriting the rest of the
// hand-edited file. Comments, ordering and unrelated formatting are preserved.
func SetTrayOption(path string, option TrayOption, enabled bool) error {
	switch option {
	case TrayFPSDisplay, TrayDismissLauncher, TrayShowChallenges:
	default:
		return fmt.Errorf("unsupported tray option %q", option)
	}
	if path == "" {
		return errors.New("config path is empty")
	}

	configEditMu.Lock()
	defer configEditMu.Unlock()

	data, mode, err := readEditableConfig(path)
	if err != nil {
		return err
	}
	doc, err := tomlsawyer.Parse(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := doc.Set(string(option), enabled); err != nil {
		return fmt.Errorf("%s: set %s: %w", path, option, err)
	}
	return writeConfigAtomic(path, doc.Bytes(), mode)
}

func readEditableConfig(path string) ([]byte, os.FileMode, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0o644, nil
	}
	if err != nil {
		return nil, 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	return data, info.Mode().Perm(), nil
}

func writeConfigAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(mode); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
