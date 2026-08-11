package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Configuration. Three rules shape everything here, and each one exists
// because the alternative bites somebody later:
//
//  1. Unknown keys are an ERROR, not a warning. A typo'd key in a silently
//     ignoring parser means you spend an evening wondering why
//     `only_when_game_runing` does nothing.
//  2. An interval below its floor is a STARTUP ERROR, never a silent clamp.
//     These intervals decide how often we hit somebody else's game server;
//     quietly "fixing" a too-low value teaches you that the number you wrote
//     is the number in use, which is the wrong lesson to learn about a
//     service that temp-bans for bursty traffic.
//  3. Validation reports every problem at once (errors.Join), so fixing a
//     config is one pass rather than a guessing game.
//
// A missing config file is fine: the defaults below are the intended setup,
// and df-hud must be useful before you have written a line of TOML.

const (
	defaultBaseURL     = "https://fairview.deadfrontier.com/onlinezombiemmo"
	defaultAllstatsURL = "https://fairview.deadfrontier.com/onlinezombiemmo/dfdata/get_allstats.php?printvars=1"
	defaultListen      = "127.0.0.1:9275"
)

// Interval floors. Politeness, not taste: the game server is not ours, and
// the account has previously been temp-banned for bursty request patterns.
// The active floor is set so that at worst we look like a busy human playing
// the game, which is roughly what one get_values every few seconds is.
const (
	floorActiveInterval    = 5 * time.Second
	floorIdleInterval      = 30 * time.Second
	floorChallengeInterval = 60 * time.Second
	floorCatalogInterval   = time.Hour
	floorRequestTimeout    = 2 * time.Second
	ceilRequestTimeout     = 60 * time.Second
	ceilJitter             = 0.5
)

// duration lets intervals be written the way humans write them ("10s", "24h").
// time.Duration is an int64, so without this the TOML would have to carry a
// nanosecond count.
type duration struct{ time.Duration }

func (d *duration) UnmarshalText(text []byte) error {
	s := strings.TrimSpace(string(text))
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is not a duration; write it like \"10s\", \"2m\" or \"24h\"", s)
	}
	d.Duration = v
	return nil
}

func (d duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

type Config struct {
	DF      DFConfig      `toml:"df"`
	Bridge  BridgeConfig  `toml:"bridge"`
	Poll    PollConfig    `toml:"poll"`
	Game    GameConfig    `toml:"game"`
	Paths   PathsConfig   `toml:"paths"`
	HUD     HUDConfig     `toml:"hud"`
	Widget  WidgetConfig  `toml:"widget"`
	Console ConsoleConfig `toml:"console"`

	// path is where this came from, for log lines and reload. Empty means
	// built-in defaults, i.e. no file existed.
	path string
}

type DFConfig struct {
	BaseURL     string   `toml:"base_url"`
	AllstatsURL string   `toml:"allstats_url"`
	UserAgent   string   `toml:"user_agent"`
	Timeout     duration `toml:"timeout"`

	// SKeyGen is the fallback signing salt for hashed calls. Normally leave
	// this empty: the bridge userscript reports the live value from page
	// context and a reported salt wins, which is what makes hashed calls
	// survive the game rotating it. Set it only to work without the bridge.
	SKeyGen string `toml:"skeygen"`
}

type BridgeConfig struct {
	Enabled bool   `toml:"enabled"`
	Listen  string `toml:"listen"`
}

type PollConfig struct {
	// Active applies while the game process is running; Idle when it is not.
	// With OnlyWhenGameRunning set (the default), Idle never applies at all
	// and closing the game takes traffic to zero.
	//
	// See the comment on minRequestGap in poller.go for why this is not
	// refined further by df_inoutpost: it would be one poll stale by
	// construction, and would delay the outpost-to-city transition by a whole
	// idle interval at exactly the moment the HUD matters.
	ActiveInterval    duration `toml:"active_interval"`
	IdleInterval      duration `toml:"idle_interval"`
	ChallengeInterval duration `toml:"challenge_interval"`
	CatalogInterval   duration `toml:"catalog_interval"`

	// Jitter spreads requests so a restart loop cannot line up into a
	// thundering herd, and so our traffic never looks metronomic.
	Jitter float64 `toml:"jitter"`

	// OnlyWhenGameRunning takes traffic to exactly zero when you are not
	// playing. Turning it off means df-hud polls around the clock; the idle
	// floor is what keeps that defensible.
	OnlyWhenGameRunning bool `toml:"only_when_game_running"`

	// BackoffMax caps the exponential backoff after transport failures. Note
	// that a rejected credential is NOT a failure to back off from: it stops
	// polling outright (see ErrStaleCredentials).
	BackoffMax duration `toml:"backoff_max"`
}

type GameConfig struct {
	// Process is the client executable's name, matched against argv[0]'s
	// basename only - see procScanner.scan for why the whole command line is
	// not searched.
	Process string `toml:"process"`

	// ScanInterval is how often /proc is scanned. This is a local read costing
	// a millisecond, not a network request, so it has no politeness floor -
	// only a sanity one. Hyprland window events trigger an immediate rescan on
	// top of this, so the interval is a backstop rather than the main path.
	ScanInterval duration `toml:"scan_interval"`
}

type PathsConfig struct {
	// DataDir holds credentials.json (0600), state.json and catalog.json.
	DataDir string `toml:"data_dir"`
}

type HUDConfig struct {
	Enabled bool `toml:"enabled"`

	// Monitor is a connector name ("DP-1") or "auto".
	//
	// "auto" means the compositor decides, which in practice puts the HUD on
	// the focused monitor. Following the GAME's monitor would be better and is
	// what this key will eventually mean, but it needs the game window's output
	// from Hyprland's command socket plus a way to move a live layer surface.
	// Documented as what it does, not as what it should do.
	Monitor string `toml:"monitor"`

	// Layer must be "overlay" for the HUD to be visible over a fullscreen
	// game: "top" sits BELOW fullscreen surfaces. The other values are
	// accepted for debugging and warned about.
	Layer   string   `toml:"layer"`
	Anchors []string `toml:"anchors"`

	MarginTop    int `toml:"margin_top"`
	MarginRight  int `toml:"margin_right"`
	MarginBottom int `toml:"margin_bottom"`
	MarginLeft   int `toml:"margin_left"`

	// ClickThrough must stay true in normal use - the HUD sits on top of the
	// game, so any pointer event it keeps is one the game does not get. It is
	// configurable only so the surface can be poked at while debugging.
	ClickThrough bool `toml:"click_through"`

	FontFamily string  `toml:"font_family"`
	FontSize   float64 `toml:"font_size"`
	TextColor  string  `toml:"text_color"`
	Opacity    float64 `toml:"opacity"`

	// CSS is an optional path to a stylesheet loaded after the built-in one,
	// so appearance can be changed without a rebuild.
	CSS string `toml:"css"`
}

type WidgetConfig struct {
	Block      BlockWidgetConfig      `toml:"block"`
	Session    SessionWidgetConfig    `toml:"session"`
	XP         XPWidgetConfig         `toml:"xp"`
	Challenges ChallengesWidgetConfig `toml:"challenges"`
}

// Every widget carries Enabled and Order. Order is a sort key, not an index,
// so widgets can be reordered by editing one number and gaps are deliberate.
type BlockWidgetConfig struct {
	Enabled bool `toml:"enabled"`
	Order   int  `toml:"order"`

	// ShowCoords prints the raw df_positionx/y under the block name. Useful
	// while calibrating the area grid, noise afterwards.
	ShowCoords bool `toml:"show_coords"`
}

type SessionWidgetConfig struct {
	Enabled bool `toml:"enabled"`
	Order   int  `toml:"order"`
}

type XPWidgetConfig struct {
	Enabled bool `toml:"enabled"`
	Order   int  `toml:"order"`

	// Window is the averaging span. MinSamples is the number of usable
	// samples below which the rate is blanked rather than guessed at - two
	// points a few seconds apart extrapolate to a nonsense hourly figure.
	Window     duration `toml:"window"`
	MinSamples int      `toml:"min_samples"`
}

type ChallengesWidgetConfig struct {
	Enabled bool `toml:"enabled"`
	Order   int  `toml:"order"`

	// Pinned seeds the pin list on first boot, by challenge NAME: both the
	// index and the end_time rotate each cycle, so neither identifies a
	// challenge across cycles. Live pin state wins after that.
	Pinned   []string `toml:"pinned"`
	ShowClan bool     `toml:"show_clan"`
	MaxShown int      `toml:"max_shown"`
}

// EffectiveWindow widens the averaging window when it is too short to ever hold
// min_samples at the current poll cadence - otherwise the rate would sit on
// "not enough data" forever and look broken.
//
// This is deliberately a derivation rather than a validation error, unlike the
// interval floors. Those are politeness limits on somebody else's server, where
// silently changing the number teaches the wrong lesson. This one is a local
// display setting whose worst failure is a blank widget, and raising the poll
// interval to be *more* polite should not be rejected because of a widget
// default the user never touched. The caller logs the adjustment at startup, so
// it is derived, not hidden.
func (x XPWidgetConfig) EffectiveWindow(activeInterval time.Duration) time.Duration {
	if x.MinSamples < 2 || activeInterval <= 0 {
		return x.Window.Duration
	}
	// min_samples points need min_samples-1 gaps, plus one gap of slack so a
	// single late sample does not drop the count below the minimum.
	need := time.Duration(x.MinSamples) * activeInterval
	if x.Window.Duration < need {
		return need
	}
	return x.Window.Duration
}

type ConsoleConfig struct {
	// The console is an ordinary toplevel window (not a layer surface) for
	// the things the click-through HUD cannot do: the full challenge list,
	// pin checkboxes, diagnostics. Toggled by POST /api/console/toggle.
	Width  int `toml:"width"`
	Height int `toml:"height"`
}

// defaultConfig is the shipped setup, and is what runs when no file exists.
// The example TOML must agree with it; config_test.go checks that.
func defaultConfig() *Config {
	return &Config{
		DF: DFConfig{
			BaseURL:     defaultBaseURL,
			AllstatsURL: defaultAllstatsURL,
			UserAgent:   "df-hud/" + version + " (+local overlay; contact via Dead Frontier forums)",
			Timeout:     duration{10 * time.Second},
		},
		Bridge: BridgeConfig{
			Enabled: true,
			Listen:  defaultListen,
		},
		Poll: PollConfig{
			ActiveInterval:      duration{10 * time.Second},
			IdleInterval:        duration{2 * time.Minute},
			ChallengeInterval:   duration{5 * time.Minute},
			CatalogInterval:     duration{24 * time.Hour},
			Jitter:              0.10,
			OnlyWhenGameRunning: true,
			BackoffMax:          duration{5 * time.Minute},
		},
		Game: GameConfig{
			Process:      defaultGameProcess,
			ScanInterval: duration{2 * time.Second},
		},
		Paths: PathsConfig{DataDir: defaultDataDir()},
		HUD: HUDConfig{
			Enabled:      true,
			Monitor:      "auto",
			Layer:        "overlay",
			Anchors:      []string{"top", "left"},
			MarginTop:    60, // clears waybar's 36px exclusive zone with room to spare
			MarginLeft:   40,
			ClickThrough: true,
			FontFamily:   "Courier New, monospace",
			FontSize:     12,
			TextColor:    "#e6cc4d", // the game's own HUD yellow
			Opacity:      1.0,
		},
		Widget: WidgetConfig{
			Block:      BlockWidgetConfig{Enabled: true, Order: 10},
			Session:    SessionWidgetConfig{Enabled: true, Order: 20},
			XP:         XPWidgetConfig{Enabled: true, Order: 30, Window: duration{30 * time.Second}, MinSamples: 3},
			Challenges: ChallengesWidgetConfig{Enabled: true, Order: 40, ShowClan: true, MaxShown: 3},
		},
		Console: ConsoleConfig{Width: 720, Height: 560},
	}
}

func defaultConfigPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "df-hud", "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(home, ".config", "df-hud", "config.toml")
}

func defaultDataDir() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "df-hud")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".local", "share", "df-hud")
}

// expandHome turns a leading ~ into the home directory. Paths in a hand-edited
// config get written with ~ whatever we document.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

// loadConfig reads path, or returns the defaults if it does not exist. An
// existing but unreadable or invalid file is an error: silently falling back to
// defaults would hide a broken config behind subtly wrong behaviour.
func loadConfig(path string) (*Config, error) {
	cfg := defaultConfig()
	cfg.path = path

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg.path = ""
		return cfg, cfg.validate()
	}
	if err != nil {
		return nil, err
	}
	md, err := toml.Decode(string(data), cfg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("%s: unknown %s: %s (a typo here would otherwise be silently ignored; see df-hud.example.toml)",
			path, plural(len(keys), "key", "keys"), strings.Join(keys, ", "))
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// validate normalises then checks. Every problem is collected so one run
// reports the whole list.
func (c *Config) validate() error {
	var errs []error

	// --- df ---
	c.DF.BaseURL = strings.TrimRight(strings.TrimSpace(c.DF.BaseURL), "/")
	if c.DF.BaseURL == "" {
		errs = append(errs, errors.New("df.base_url is empty"))
	} else if u, err := url.Parse(c.DF.BaseURL); err != nil {
		errs = append(errs, fmt.Errorf("df.base_url %q: %v", c.DF.BaseURL, err))
	} else if u.Scheme != "https" || u.Host == "" {
		// Credentials are in every request body. Plaintext is not an option.
		errs = append(errs, fmt.Errorf("df.base_url %q must be an https URL with a host: "+
			"every request body carries account credentials", c.DF.BaseURL))
	}
	if c.DF.AllstatsURL != "" {
		if u, err := url.Parse(c.DF.AllstatsURL); err != nil || u.Scheme == "" || u.Host == "" {
			errs = append(errs, fmt.Errorf("df.allstats_url %q is not an absolute URL", c.DF.AllstatsURL))
		}
	}
	if strings.TrimSpace(c.DF.UserAgent) == "" {
		// An honest, identifiable UA is the difference between a tool the
		// server operator can contact and one that just looks like abuse.
		errs = append(errs, errors.New("df.user_agent is empty: identify this tool honestly to the server"))
	}
	errs = appendRange(errs, "df.timeout", c.DF.Timeout.Duration, floorRequestTimeout, ceilRequestTimeout)

	// --- bridge ---
	if c.Bridge.Enabled {
		if err := validateLoopback(c.Bridge.Listen); err != nil {
			errs = append(errs, fmt.Errorf("bridge.listen: %w", err))
		}
	}

	// --- poll ---
	errs = appendFloor(errs, "poll.active_interval", c.Poll.ActiveInterval.Duration, floorActiveInterval)
	errs = appendFloor(errs, "poll.idle_interval", c.Poll.IdleInterval.Duration, floorIdleInterval)
	errs = appendFloor(errs, "poll.challenge_interval", c.Poll.ChallengeInterval.Duration, floorChallengeInterval)
	errs = appendFloor(errs, "poll.catalog_interval", c.Poll.CatalogInterval.Duration, floorCatalogInterval)
	if c.Poll.IdleInterval.Duration < c.Poll.ActiveInterval.Duration {
		errs = append(errs, fmt.Errorf("poll.idle_interval (%s) is shorter than poll.active_interval (%s): "+
			"idle would poll harder than playing, which is backwards",
			c.Poll.IdleInterval, c.Poll.ActiveInterval))
	}
	if c.Poll.Jitter < 0 || c.Poll.Jitter > ceilJitter {
		errs = append(errs, fmt.Errorf("poll.jitter %.3f must be between 0 and %.1f (a fraction of the interval)",
			c.Poll.Jitter, ceilJitter))
	}
	if c.Poll.BackoffMax.Duration < c.Poll.ActiveInterval.Duration {
		errs = append(errs, fmt.Errorf("poll.backoff_max (%s) must be at least poll.active_interval (%s)",
			c.Poll.BackoffMax, c.Poll.ActiveInterval))
	}

	// --- game ---
	c.Game.Process = strings.TrimSpace(c.Game.Process)
	if c.Game.Process == "" {
		errs = append(errs, errors.New("game.process is empty: df-hud would never detect the game"))
	}
	if strings.ContainsAny(c.Game.Process, `/\`) {
		// Only the basename is compared, so a path here would never match and
		// the failure would be silent.
		errs = append(errs, fmt.Errorf("game.process %q must be a bare executable name, not a path",
			c.Game.Process))
	}
	errs = appendRange(errs, "game.scan_interval", c.Game.ScanInterval.Duration, 250*time.Millisecond, 5*time.Minute)

	// --- paths ---
	c.Paths.DataDir = expandHome(strings.TrimSpace(c.Paths.DataDir))
	if c.Paths.DataDir == "" {
		errs = append(errs, errors.New("paths.data_dir is empty"))
	}

	// --- hud ---
	if c.HUD.Enabled {
		if _, err := parseLayer(c.HUD.Layer); err != nil {
			errs = append(errs, fmt.Errorf("hud.layer: %w", err))
		}
		if _, err := parseAnchors(c.HUD.Anchors); err != nil {
			errs = append(errs, fmt.Errorf("hud.anchors: %w", err))
		}
		if c.HUD.FontSize <= 0 {
			errs = append(errs, fmt.Errorf("hud.font_size %.1f must be positive", c.HUD.FontSize))
		}
		if c.HUD.Opacity <= 0 || c.HUD.Opacity > 1 {
			errs = append(errs, fmt.Errorf("hud.opacity %.2f must be in (0, 1]: 0 would render an invisible HUD", c.HUD.Opacity))
		}
		if err := validateColor(c.HUD.TextColor); err != nil {
			errs = append(errs, fmt.Errorf("hud.text_color: %w", err))
		}
		if c.HUD.CSS != "" {
			c.HUD.CSS = expandHome(c.HUD.CSS)
			if _, err := os.Stat(c.HUD.CSS); err != nil {
				errs = append(errs, fmt.Errorf("hud.css %q: %v", c.HUD.CSS, err))
			}
		}
	}

	// --- widgets ---
	if c.Widget.XP.Enabled {
		if c.Widget.XP.MinSamples < 2 {
			errs = append(errs, fmt.Errorf("widget.xp.min_samples %d must be at least 2: a rate needs two points",
				c.Widget.XP.MinSamples))
		}
		if c.Widget.XP.Window.Duration <= 0 {
			errs = append(errs, fmt.Errorf("widget.xp.window %s must be positive", c.Widget.XP.Window))
		}
	}
	if c.Widget.Challenges.Enabled && c.Widget.Challenges.MaxShown < 1 {
		errs = append(errs, fmt.Errorf("widget.challenges.max_shown %d must be at least 1 (disable the widget instead)",
			c.Widget.Challenges.MaxShown))
	}
	for i, name := range c.Widget.Challenges.Pinned {
		if strings.TrimSpace(name) == "" {
			errs = append(errs, fmt.Errorf("widget.challenges.pinned[%d] is empty", i))
		}
	}

	// --- console ---
	if c.Console.Width < 320 || c.Console.Height < 240 {
		errs = append(errs, fmt.Errorf("console size %dx%d is too small to be usable (min 320x240)",
			c.Console.Width, c.Console.Height))
	}

	return errors.Join(errs...)
}

// appendFloor is the politeness check. The message says what the floor is and
// why it exists, because the natural reaction to a rejected interval is to
// wonder whether the number is arbitrary.
func appendFloor(errs []error, key string, got, floor time.Duration) []error {
	if got < floor {
		return append(errs, fmt.Errorf("%s is %s, below the %s minimum: this decides how often df-hud "+
			"hits the game server, so it is rejected rather than quietly raised", key, got, floor))
	}
	return errs
}

func appendRange(errs []error, key string, got, lo, hi time.Duration) []error {
	if got < lo || got > hi {
		return append(errs, fmt.Errorf("%s is %s, outside the allowed %s..%s", key, got, lo, hi))
	}
	return errs
}

// parseLayer maps the config name to the protocol value. Only "overlay" is
// above a fullscreen window; NonFullscreen reports whether the choice will hide
// under the game so the caller can say so out loud at startup.
func parseLayer(s string) (Layer, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "overlay", "":
		return LayerOverlay, nil
	case "top":
		return LayerTop, nil
	case "bottom":
		return LayerBottom, nil
	case "background":
		return LayerBackground, nil
	}
	return LayerOverlay, fmt.Errorf("%q is not a layer: use overlay (the only one drawn above a "+
		"fullscreen game), top, bottom or background", s)
}

// HidesUnderFullscreen is true for every layer except overlay. Worth warning
// about: with the game fullscreen, a "top" HUD is simply invisible, and that
// looks exactly like df-hud being broken.
func (h HUDConfig) HidesUnderFullscreen() bool {
	l, err := parseLayer(h.Layer)
	return err == nil && l != LayerOverlay
}

var anchorEdges = map[string]Edge{
	"top":    EdgeTop,
	"bottom": EdgeBottom,
	"left":   EdgeLeft,
	"right":  EdgeRight,
}

// parseAnchors resolves edge names, rejecting duplicates. Anchoring opposite
// edges is legal - it stretches the surface - so it is allowed.
func parseAnchors(names []string) ([]Edge, error) {
	out := make([]Edge, 0, len(names))
	seen := map[string]bool{}
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		edge, ok := anchorEdges[name]
		if !ok {
			return nil, fmt.Errorf("%q is not an edge: use top, bottom, left or right", raw)
		}
		if seen[name] {
			return nil, fmt.Errorf("%q is listed twice", name)
		}
		seen[name] = true
		out = append(out, edge)
	}
	if len(out) == 0 {
		return nil, errors.New("at least one anchor is required, or the surface is centred with no fixed position")
	}
	return out, nil
}

// Anchors resolves the configured edges. Only call after validate has passed.
func (h HUDConfig) AnchorEdges() []Edge {
	edges, err := parseAnchors(h.Anchors)
	if err != nil {
		return []Edge{EdgeTop, EdgeLeft}
	}
	return edges
}

// LayerValue resolves the configured layer. Only call after validate has passed.
func (h HUDConfig) LayerValue() Layer {
	l, _ := parseLayer(h.Layer)
	return l
}

// validateColor accepts what GTK's CSS accepts in the places we substitute it:
// #rgb, #rrggbb, #rrggbbaa, or a bare name. It exists to catch a typo at
// startup instead of as a GTK parse warning and a HUD that renders black.
func validateColor(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("empty")
	}
	if strings.HasPrefix(s, "#") {
		hex := s[1:]
		switch len(hex) {
		case 3, 4, 6, 8:
		default:
			return fmt.Errorf("%q must have 3, 4, 6 or 8 hex digits after #", s)
		}
		for _, r := range hex {
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return fmt.Errorf("%q contains a non-hex digit %q", s, r)
			}
		}
		return nil
	}
	// A CSS colour name or function. Anything with a quote or brace is a
	// stylesheet injection rather than a colour, since this gets interpolated
	// into CSS.
	if strings.ContainsAny(s, `{}"';`) {
		return fmt.Errorf("%q is not a colour", s)
	}
	return nil
}

// Paths derived from DataDir. Kept as methods so there is one definition of
// each filename and the poller/store cannot drift from the bridge.
func (c *Config) CredentialsPath() string { return filepath.Join(c.Paths.DataDir, "credentials.json") }
func (c *Config) StatePath() string       { return filepath.Join(c.Paths.DataDir, "state.json") }
func (c *Config) CatalogPath() string     { return filepath.Join(c.Paths.DataDir, "catalog.json") }

// EnsureDataDir creates the data directory and proves it is writable at
// startup, rather than discovering at the first save that nothing persists.
// 0700 because credentials.json lives here.
func (c *Config) EnsureDataDir() error {
	if err := os.MkdirAll(c.Paths.DataDir, 0o700); err != nil {
		return fmt.Errorf("paths.data_dir %q: %w", c.Paths.DataDir, err)
	}
	probe := filepath.Join(c.Paths.DataDir, ".writable-probe")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		return fmt.Errorf("paths.data_dir %q is not writable: %w", c.Paths.DataDir, err)
	}
	return os.Remove(probe)
}

// SigningSalt is the salt to use for hashed calls: whatever the bridge last
// reported, falling back to the configured value. Reported wins because it
// comes from live page context, so a game-side rotation heals itself.
func (c *Config) SigningSalt(store *credStore) string {
	if store != nil {
		if salt := store.Salt(); salt != "" {
			return salt
		}
	}
	return c.DF.SKeyGen
}

// RequestsPerHour is the documented traffic budget, printed at startup. Being
// able to state the number is what makes "is this polite?" an answerable
// question instead of a feeling.
func (c *Config) RequestsPerHour(activeFraction float64) float64 {
	if activeFraction < 0 {
		activeFraction = 0
	}
	if activeFraction > 1 {
		activeFraction = 1
	}
	perHour := func(d time.Duration) float64 {
		if d <= 0 {
			return 0
		}
		return float64(time.Hour) / float64(d)
	}
	total := activeFraction*perHour(c.Poll.ActiveInterval.Duration) +
		(1-activeFraction)*perHour(c.Poll.IdleInterval.Duration)
	if c.Widget.Challenges.Enabled {
		total += perHour(c.Poll.ChallengeInterval.Duration)
	}
	total += perHour(c.Poll.CatalogInterval.Duration)
	return total
}

// reloadableFrom returns cfg with the fields that cannot change at runtime
// copied back from old, plus the names of any that the user tried to change.
// Rebinding a listener or moving the data directory under a running poller is
// not worth the failure modes, so those need a restart - said plainly rather
// than pretended to.
func (cfg *Config) reloadableFrom(old *Config) []string {
	var frozen []string
	if cfg.Bridge.Listen != old.Bridge.Listen {
		frozen = append(frozen, "bridge.listen")
		cfg.Bridge.Listen = old.Bridge.Listen
	}
	if cfg.Bridge.Enabled != old.Bridge.Enabled {
		frozen = append(frozen, "bridge.enabled")
		cfg.Bridge.Enabled = old.Bridge.Enabled
	}
	if cfg.Paths.DataDir != old.Paths.DataDir {
		frozen = append(frozen, "paths.data_dir")
		cfg.Paths.DataDir = old.Paths.DataDir
	}
	return frozen
}
