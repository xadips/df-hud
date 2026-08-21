package config

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSetTrayOptionPreservesCommentsAndFormatting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	const original = `# keep this header
[game_keys]
# keep this explanation
fps_display = false # keep this inline comment
dismiss_launcher = false

[widget.challenges]
enabled = true
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetTrayOption(path, TrayDismissLauncher, true); err != nil {
		t.Fatal(err)
	}
	if err := SetTrayOption(path, TrayShowChallenges, false); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"# keep this header",
		"# keep this explanation",
		"fps_display = false",
		"# keep this inline comment",
		"dismiss_launcher = true",
		"[widget.challenges]",
		"enabled = false",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("edited config lost %q:\n%s", want, text)
		}
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil {
			t.Fatal(err)
		} else if info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %o, want 600", info.Mode().Perm())
		}
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("edited config does not load: %v", err)
	}
	if !cfg.GameKeys.DismissLauncher || cfg.Widget.Challenges.Enabled {
		t.Fatalf("edited values not applied: %+v %+v", cfg.GameKeys, cfg.Widget.Challenges)
	}
}

func TestSetTrayOptionCreatesMinimalConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	if err := SetTrayOption(path, TrayFPSDisplay, true); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.GameKeys.FPSDisplay {
		t.Fatal("created config did not enable FPS display")
	}
}

func TestSetTrayOptionRejectsUnknownOption(t *testing.T) {
	if err := SetTrayOption(filepath.Join(t.TempDir(), "config.toml"), TrayOption("unknown"), true); err == nil {
		t.Fatal("unknown tray option was accepted")
	}
}

func TestTrayOptionWriteTriggersValidatedReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[game_keys]\nfps_display = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	current, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := newConfigWatcher(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloaded := make(chan *Config, 1)
	go watcher.Run(ctx, func() *Config { return current },
		func(next *Config, _ []string) { reloaded <- next })

	if err := SetTrayOption(path, TrayFPSDisplay, true); err != nil {
		t.Fatal(err)
	}
	select {
	case next := <-reloaded:
		if !next.GameKeys.FPSDisplay {
			t.Fatal("watcher reloaded the old tray value")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tray config write did not trigger reload")
	}
}
