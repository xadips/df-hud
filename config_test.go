package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeConfig puts a config file in a temp dir and returns its path. The dir is
// isolated per test so the watcher tests cannot see each other's writes.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigMissingFileUsesDefaults(t *testing.T) {
	// A first run with no config must work, not fail.
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("a missing config must fall back to defaults: %v", err)
	}
	if cfg.Poll.ActiveInterval.Duration != 10*time.Second {
		t.Errorf("active interval = %s, want the 10s default", cfg.Poll.ActiveInterval)
	}
	if cfg.path != "" {
		t.Errorf("path = %q, want empty to mark built-in defaults", cfg.path)
	}
}

// TestExampleConfigMatchesDefaults is the anti-drift test: df-hud.example.toml
// documents every value as "the default", so it must parse clean AND produce
// exactly defaultConfig(). Change a default without updating the example (or
// vice versa) and this fails, which is the only reliable way to keep shipped
// documentation honest.
func TestExampleConfigMatchesDefaults(t *testing.T) {
	cfg, err := loadConfig("df-hud.example.toml")
	if err != nil {
		t.Fatalf("df-hud.example.toml must load clean: %v", err)
	}
	cfg.path = ""
	want := defaultConfig()
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("df-hud.example.toml has drifted from defaultConfig()\n got: %+v\nwant: %+v", cfg, want)
	}
}

func TestLoadConfigRejectsUnknownKeys(t *testing.T) {
	// The exact failure this guards: a plausible typo in a key that matters.
	path := writeConfig(t, "[poll]\nonly_when_game_runing = true\n")
	_, err := loadConfig(path)
	if err == nil {
		t.Fatal("an unknown key must be rejected, or a typo silently does nothing")
	}
	if !strings.Contains(err.Error(), "only_when_game_runing") {
		t.Errorf("the error must name the offending key, got: %v", err)
	}
}

func TestLoadConfigRejectsUnknownTable(t *testing.T) {
	path := writeConfig(t, "[widget.xpp]\nenabled = true\n")
	if _, err := loadConfig(path); err == nil {
		t.Fatal("an unknown widget table must be rejected")
	}
}

func TestLoadConfigOverridesDefaults(t *testing.T) {
	path := writeConfig(t, `
[poll]
active_interval = "15s"
idle_interval = "5m"
jitter = 0.25

[hud]
layer = "top"
margin_bottom = 12

[widget.session]
x = 900
y = 40
font_size = 18

[widget.xp]
min_samples = 5
window = "2m"
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Poll.ActiveInterval.Duration != 15*time.Second {
		t.Errorf("active = %s, want 15s", cfg.Poll.ActiveInterval)
	}
	if cfg.Poll.Jitter != 0.25 {
		t.Errorf("jitter = %v, want 0.25", cfg.Poll.Jitter)
	}
	// Untouched keys must keep their defaults rather than zeroing out.
	if cfg.Poll.CatalogInterval.Duration != 24*time.Hour {
		t.Errorf("catalog = %s, want the 24h default to survive a partial config", cfg.Poll.CatalogInterval)
	}
	if !cfg.Bridge.Enabled {
		t.Error("bridge.enabled must stay true when the config does not mention it")
	}
	if !cfg.HUD.HidesUnderFullscreen() {
		t.Error(`layer = "top" hides under a fullscreen game and must be reported as such`)
	}
	// Placement overrides land, and the ones not mentioned keep their defaults -
	// the whole point of a group only needing the keys it differs on.
	if cfg.Widget.Session.X != 900 || cfg.Widget.Session.Y != 40 {
		t.Errorf("session placement = %d, %d; want 900, 40", cfg.Widget.Session.X, cfg.Widget.Session.Y)
	}
	if cfg.Widget.Session.FontSize != 18 {
		t.Errorf("session font_size = %v, want 18", cfg.Widget.Session.FontSize)
	}
	if cfg.Widget.Session.Prefix != defaultConfig().Widget.Session.Prefix {
		t.Errorf("session prefix = %q, want the default to survive a partial config",
			cfg.Widget.Session.Prefix)
	}
	if cfg.Widget.XP.X != defaultConfig().Widget.XP.X {
		t.Error("another group's placement must not be disturbed")
	}
}

// TestConfigIntervalFloors pins that every floor is an error, never a clamp.
func TestConfigIntervalFloors(t *testing.T) {
	cases := map[string]string{
		"active below floor":    "[poll]\nactive_interval = \"1s\"\n",
		"idle below floor":      "[poll]\nidle_interval = \"5s\"\n",
		"challenge below floor": "[poll]\nchallenge_interval = \"10s\"\n",
		"catalog below floor":   "[poll]\ncatalog_interval = \"1m\"\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, err := loadConfig(writeConfig(t, body))
			if err == nil {
				t.Fatalf("must be rejected, not clamped; got a usable config: %+v", cfg.Poll)
			}
			if !strings.Contains(err.Error(), "minimum") {
				t.Errorf("the error should explain the minimum, got: %v", err)
			}
		})
	}
}

func TestConfigCrossFieldRules(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"idle faster than active": {
			body: "[poll]\nactive_interval = \"1m\"\nidle_interval = \"30s\"\n",
			want: "backwards",
		},
		"min_samples below two": {
			body: "[widget.xp]\nmin_samples = 1\n",
			want: "two points",
		},
		"backoff shorter than active": {
			body: "[poll]\nactive_interval = \"1m\"\nidle_interval = \"2m\"\nbackoff_max = \"10s\"\n",
			want: "backoff_max",
		},
		"jitter out of range": {
			body: "[poll]\njitter = 0.9\n",
			want: "jitter",
		},
		"challenges max_shown negative": {
			body: "[widget.challenges]\nmax_shown = -1\n",
			want: "max_shown",
		},
		"widget position negative": {
			body: "[widget.xp]\nx = -10\n",
			want: "cannot be negative",
		},
		"widget font size negative": {
			body: "[widget.block]\nfont_size = -2\n",
			want: "font_size",
		},
		"console too small": {
			body: "[console]\nwidth = 100\nheight = 100\n",
			want: "too small",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadConfig(writeConfig(t, tc.body))
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err, tc.want)
			}
		})
	}
}

// A too-low interval and a bad colour in the same file should both be reported,
// so fixing a config is one pass instead of a guessing game.
func TestConfigReportsEveryProblemAtOnce(t *testing.T) {
	path := writeConfig(t, "[poll]\nactive_interval = \"1s\"\n[hud]\ntext_color = \"#gg0011\"\nopacity = 3\n")
	_, err := loadConfig(path)
	if err == nil {
		t.Fatal("expected errors")
	}
	msg := err.Error()
	for _, want := range []string{"active_interval", "text_color", "opacity"} {
		if !strings.Contains(msg, want) {
			t.Errorf("combined error should mention %s:\n%s", want, msg)
		}
	}
}

// Raising the poll interval must not reject the config over an xp window the
// user never touched; the window widens instead, and the startup log says so.
func TestXPEffectiveWindow(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t,
		"[poll]\nactive_interval = \"30s\"\nidle_interval = \"2m\"\n"+
			"[widget.xp]\nwindow = \"30s\"\n"))
	if err != nil {
		t.Fatalf("a slower poll must stay valid: %v", err)
	}
	// A 30s window cannot hold 3 samples at a 30s cadence, so it widens.
	got := cfg.Widget.XP.EffectiveWindow(cfg.Poll.ActiveInterval.Duration)
	if got != 90*time.Second {
		t.Errorf("effective window = %s, want 1m30s (3 samples x 30s)", got)
	}
	// A window already long enough is left exactly as written.
	cfg.Widget.XP.Window = duration{10 * time.Minute}
	if got := cfg.Widget.XP.EffectiveWindow(30 * time.Second); got != 10*time.Minute {
		t.Errorf("effective window = %s, want the configured 10m untouched", got)
	}
	// The default pairing needs no adjustment at all: five minutes holds three
	// samples at any interval the poll floors allow.
	def := defaultConfig()
	if got := def.Widget.XP.EffectiveWindow(def.Poll.ActiveInterval.Duration); got != 5*time.Minute {
		t.Errorf("default effective window = %s, want the configured 5m", got)
	}
}

func TestConfigRejectsNonLoopbackBridge(t *testing.T) {
	// The bridge receives account-equivalent credentials. This must never be
	// reachable off the machine, and the check lives in config as well as in
	// bridge.go so neither path can be the only one.
	for _, listen := range []string{"0.0.0.0:9275", "192.168.1.50:9275", ":9275"} {
		body := "[bridge]\nlisten = \"" + listen + "\"\n"
		if _, err := loadConfig(writeConfig(t, body)); err == nil {
			t.Errorf("listen = %q must be rejected", listen)
		}
	}
	// Disabling the bridge means the address is unused, so it is not checked.
	body := "[bridge]\nenabled = false\nlisten = \"0.0.0.0:9275\"\n"
	if _, err := loadConfig(writeConfig(t, body)); err != nil {
		t.Errorf("an unused listen address should not fail validation: %v", err)
	}
}

func TestConfigRejectsPlaintextBaseURL(t *testing.T) {
	body := "[df]\nbase_url = \"http://fairview.deadfrontier.com/onlinezombiemmo\"\n"
	_, err := loadConfig(writeConfig(t, body))
	if err == nil {
		t.Fatal("http must be rejected: request bodies carry credentials")
	}
	if !strings.Contains(err.Error(), "credentials") {
		t.Errorf("the error should say why, got: %v", err)
	}
}

func TestConfigTrimsBaseURLSlash(t *testing.T) {
	// dfclient builds URLs as base + "/" + call + ".php", so a trailing slash
	// would produce a double slash on every request.
	body := "[df]\nbase_url = \"https://fairview.deadfrontier.com/onlinezombiemmo/\"\n"
	cfg, err := loadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(cfg.DF.BaseURL, "/") {
		t.Errorf("base_url = %q, want the trailing slash trimmed", cfg.DF.BaseURL)
	}
}

func TestConfigBadDurationMentionsTheFormat(t *testing.T) {
	_, err := loadConfig(writeConfig(t, "[poll]\nactive_interval = \"10 seconds\"\n"))
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if !strings.Contains(err.Error(), `"10s"`) {
		t.Errorf("the error should show the expected format, got: %v", err)
	}
}

func TestParseLayer(t *testing.T) {
	for name, want := range map[string]Layer{
		"overlay":    LayerOverlay,
		"OVERLAY":    LayerOverlay,
		"":           LayerOverlay,
		"top":        LayerTop,
		"bottom":     LayerBottom,
		"background": LayerBackground,
	} {
		got, err := parseLayer(name)
		if err != nil {
			t.Errorf("parseLayer(%q): %v", name, err)
		}
		if got != want {
			t.Errorf("parseLayer(%q) = %v, want %v", name, got, want)
		}
	}
	if _, err := parseLayer("above"); err == nil {
		t.Error("an unknown layer name must be rejected")
	}
}

func TestValidateColor(t *testing.T) {
	for _, ok := range []string{"#fff", "#ffff", "#e6cc4d", "#e6cc4dff", "yellow", "rgb(1,2,3)"} {
		if err := validateColor(ok); err != nil {
			t.Errorf("validateColor(%q) = %v, want nil", ok, err)
		}
	}
	// The last two matter: this value is interpolated into a stylesheet.
	for _, bad := range []string{"", "#12345", "#gggggg", `red; } window { background: black`, `"x"`} {
		if err := validateColor(bad); err == nil {
			t.Errorf("validateColor(%q) = nil, want an error", bad)
		}
	}
}

func TestConfigDerivedPaths(t *testing.T) {
	cfg := defaultConfig()
	cfg.Paths.DataDir = "/tmp/df-hud-test"
	if got := cfg.CredentialsPath(); got != "/tmp/df-hud-test/credentials.json" {
		t.Errorf("CredentialsPath = %q", got)
	}
	if got := cfg.StatePath(); got != "/tmp/df-hud-test/state.json" {
		t.Errorf("StatePath = %q", got)
	}
	if got := cfg.CatalogPath(); got != "/tmp/df-hud-test/catalog.json" {
		t.Errorf("CatalogPath = %q", got)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory in this environment")
	}
	if got := expandHome("~/.local/share/df-hud"); got != filepath.Join(home, ".local/share/df-hud") {
		t.Errorf("expandHome = %q", got)
	}
	// A bare ~ inside a path is a literal directory name, not a home marker.
	if got := expandHome("/opt/~/x"); got != "/opt/~/x" {
		t.Errorf("expandHome must only expand a leading ~, got %q", got)
	}
}

func TestEnsureDataDir(t *testing.T) {
	cfg := defaultConfig()
	cfg.Paths.DataDir = filepath.Join(t.TempDir(), "nested", "data")
	if err := cfg.EnsureDataDir(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(cfg.Paths.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	// credentials.json lives here, so the directory itself is private.
	if perm := st.Mode().Perm(); perm != 0o700 {
		t.Errorf("data dir mode = %o, want 700", perm)
	}
	if _, err := os.Stat(filepath.Join(cfg.Paths.DataDir, ".writable-probe")); err == nil {
		t.Error("the write probe must be cleaned up")
	}
}

func TestSigningSaltPrefersTheBridge(t *testing.T) {
	cfg := defaultConfig()
	cfg.DF.SKeyGen = "from-config"
	store := newCredStore("")

	if got := cfg.SigningSalt(store); got != "from-config" {
		t.Errorf("salt = %q, want the configured fallback when the bridge has none", got)
	}
	if _, err := store.Set(Credentials{UserID: "1", Password: "2", SC: "3"}, "from-bridge"); err != nil {
		t.Fatal(err)
	}
	// Reported wins: it comes from live page context, so a game-side rotation
	// heals itself without anyone editing a config file.
	if got := cfg.SigningSalt(store); got != "from-bridge" {
		t.Errorf("salt = %q, want the bridge-reported value to win", got)
	}
}

func TestRequestsPerHour(t *testing.T) {
	cfg := defaultConfig()

	// Idle all hour: get_values every 2m (30/h), and the challenge board
	// stretched to the same 2m rather than its 30s playing cadence (30/h), plus
	// the daily catalog refresh. With only_when_game_running on - the default -
	// this is hypothetical anyway, since closing the game stops everything.
	idle := cfg.RequestsPerHour(0)
	if idle < 55 || idle > 65 {
		t.Errorf("idle budget = %.1f/h, expected ~60 (30 + 30 + 1/24)", idle)
	}

	// Playing all hour: get_values every 10s (360/h), the challenge board every 30s
	// (120/h) and the event map every 60s (60/h). This is the number the README
	// quotes, so a change to the defaults shows up here first.
	active := cfg.RequestsPerHour(1)
	if active < 530 || active > 550 {
		t.Errorf("active budget = %.1f/h, expected ~540 (360 + 120 + 60 + 1/24)", active)
	}

	// The event map is the only third-party traffic, and it stops with the game
	// like everything else.
	noBoss := defaultConfig()
	noBoss.BossMap.Enabled = false
	if with, without := cfg.RequestsPerHour(1), noBoss.RequestsPerHour(1); with-without < 55 {
		t.Errorf("the event map should account for ~60/h, got %.1f", with-without)
	}
	if active <= idle {
		t.Error("playing must cost more requests than idling")
	}
}

// The board polls fast while playing and no faster than the general idle cadence
// when the game is closed, so a short interval chosen for play cannot become an
// all-night poll for someone who has turned only_when_game_running off.
func TestEffectiveChallengeInterval(t *testing.T) {
	cfg := defaultConfig()
	if got := cfg.Poll.EffectiveChallengeInterval(true); got != 30*time.Second {
		t.Errorf("playing = %s, want the configured 30s", got)
	}
	if got := cfg.Poll.EffectiveChallengeInterval(false); got != 2*time.Minute {
		t.Errorf("game closed = %s, want it stretched to the 2m idle cadence", got)
	}

	// A challenge interval already slower than idle is left alone.
	cfg.Poll.ChallengeInterval = duration{10 * time.Minute}
	if got := cfg.Poll.EffectiveChallengeInterval(false); got != 10*time.Minute {
		t.Errorf("game closed = %s, want the configured 10m untouched", got)
	}
}

func TestReloadableFromFreezesRestartOnlyFields(t *testing.T) {
	old := defaultConfig()
	old.Paths.DataDir = "/old/data"
	next := defaultConfig()
	next.Paths.DataDir = "/new/data"
	next.Bridge.Listen = "127.0.0.1:9999"
	next.Poll.ActiveInterval = duration{20 * time.Second}

	frozen := next.reloadableFrom(old)
	if len(frozen) != 2 {
		t.Fatalf("frozen = %v, want bridge.listen and paths.data_dir", frozen)
	}
	if next.Bridge.Listen != old.Bridge.Listen || next.Paths.DataDir != old.Paths.DataDir {
		t.Error("restart-only fields must keep the running values")
	}
	// Everything else reloads.
	if next.Poll.ActiveInterval.Duration != 20*time.Second {
		t.Error("poll.active_interval should have been applied")
	}
}

// TestWatchConfigReloadsOnAtomicSave is the one that justifies watching the
// directory instead of the file: this is how vim, helix and VS Code save.
func TestWatchConfigReloadsOnAtomicSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[poll]\nactive_interval = \"10s\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	// Registering before the edit is the whole point of the two-step API: with
	// setup inside the goroutine, this test races the watch into existence and
	// passes or fails depending on scheduling.
	cw, err := newConfigWatcher(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloaded := make(chan *Config, 4)
	go cw.Run(ctx, func() *Config { return cfg }, func(next *Config, _ []string) { reloaded <- next })

	// Write to a sibling and rename over the target, exactly as an editor does.
	tmp := filepath.Join(dir, ".config.toml.swp")
	if err := os.WriteFile(tmp, []byte("[poll]\nactive_interval = \"25s\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}

	select {
	case next := <-reloaded:
		if next.Poll.ActiveInterval.Duration != 25*time.Second {
			t.Errorf("reloaded active_interval = %s, want 25s", next.Poll.ActiveInterval)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no reload after an atomic save: the watcher is watching the inode, not the directory")
	}
}

// An invalid edit must not take the HUD down mid-game.
func TestWatchConfigKeepsRunningConfigOnBadEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[poll]\nactive_interval = \"10s\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	cw, err := newConfigWatcher(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloaded := make(chan *Config, 4)
	go cw.Run(ctx, func() *Config { return cfg }, func(next *Config, _ []string) { reloaded <- next })

	// Below the floor, so the reload must be refused.
	if err := os.WriteFile(path, []byte("[poll]\nactive_interval = \"1s\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case next := <-reloaded:
		t.Fatalf("an invalid config must not be applied, got active_interval = %s", next.Poll.ActiveInterval)
	case <-time.After(time.Second):
	}

	// A later valid edit still gets through, i.e. the watcher survived.
	if err := os.WriteFile(path, []byte("[poll]\nactive_interval = \"30s\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case next := <-reloaded:
		if next.Poll.ActiveInterval.Duration != 30*time.Second {
			t.Errorf("active_interval = %s, want 30s", next.Poll.ActiveInterval)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher stopped working after rejecting one edit")
	}
}
