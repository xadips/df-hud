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
	floorActiveInterval = 5 * time.Second
	floorIdleInterval   = 30 * time.Second
	// 30s while playing is ~120 requests/hour for the board, which is a small
	// fraction of what the game client itself sends during normal play. Below
	// that the board is not changing fast enough to be worth asking.
	floorChallengeInterval = 30 * time.Second
	floorCatalogInterval   = time.Hour
	// The boss map is somebody else's site, so the floor is set by what that site
	// already does to itself: dfprofiler's own bossmap page re-fetches this
	// endpoint every 30 seconds per open tab (bossmap.js). Matching that is the
	// hard limit - df-hud must never cost the operator more than their own page
	// does - and the default sits at twice it.
	floorBossMapInterval = 30 * time.Second
	floorRequestTimeout  = 2 * time.Second
	ceilRequestTimeout   = 60 * time.Second
	ceilJitter           = 0.5
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
	DF       DFConfig       `toml:"df"`
	Bridge   BridgeConfig   `toml:"bridge"`
	Poll     PollConfig     `toml:"poll"`
	Game     GameConfig     `toml:"game"`
	Paths    PathsConfig    `toml:"paths"`
	HUD      HUDConfig      `toml:"hud"`
	BossMap  BossMapConfig  `toml:"bossmap"`
	RunStart RunStartConfig `toml:"run_start"`
	Tray     TrayConfig     `toml:"tray"`
	Widget   WidgetConfig   `toml:"widget"`
	Console  ConsoleConfig  `toml:"console"`

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
	ActiveInterval duration `toml:"active_interval"`
	IdleInterval   duration `toml:"idle_interval"`

	// ChallengeInterval is the challenge board's cadence WHILE PLAYING. With the
	// game closed it stretches to at least IdleInterval - see
	// EffectiveChallengeInterval - so turning only_when_game_running off cannot
	// leave a 30s poll running all night.
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

	// WindowClass identifies the game's WINDOW to the compositor, which is a
	// separate question from identifying its process: it decides where the HUD
	// is drawn (hud.follow_game_workspace and monitor = "auto"), never whether
	// df-hud polls.
	//
	// Empty means "derive it from Process", which is right when Wine reports the
	// executable name as the class. It exists as a key because the alternative
	// to a one-line config fix would be a rebuild, and the value can only be
	// discovered from a running game (df-hud -check-game prints it).
	WindowClass string `toml:"window_class"`

	// ScanInterval is how often /proc is scanned. This is a local read costing
	// a millisecond, not a network request, so it has no politeness floor -
	// only a sanity one. Hyprland window events trigger an immediate rescan on
	// top of this, so the interval is a backstop rather than the main path.
	ScanInterval duration `toml:"scan_interval"`
}

// WindowMatch is the class to look for, falling back to the process name.
func (g GameConfig) WindowMatch() string {
	if g.WindowClass != "" {
		return g.WindowClass
	}
	return g.Process
}

type PathsConfig struct {
	// DataDir holds credentials.json (0600), state.json and catalog.json.
	DataDir string `toml:"data_dir"`
}

type HUDConfig struct {
	Enabled bool `toml:"enabled"`

	// OnlyWhenGameRunning hides the HUD entirely while the game is not running.
	// Named to match poll.only_when_game_running, and separate from it on
	// purpose: one governs traffic to somebody else's server, this one governs
	// pixels on your own screen.
	OnlyWhenGameRunning bool `toml:"only_when_game_running"`

	// FollowGameWorkspace shows the HUD only while the game's own workspace is
	// the one being displayed. The layer-shell protocol has no per-workspace
	// concept, so this is emulated from the compositor's window list - see
	// visibility.go, including why it fails open.
	FollowGameWorkspace bool `toml:"follow_game_workspace"`

	// Monitor is a connector name ("DP-1") or "auto".
	//
	// "auto" follows the GAME: the surface is pinned to whichever monitor the
	// game's window is on, re-checked each time the HUD is shown. If the window
	// cannot be identified, the compositor chooses, which in practice means the
	// focused monitor.
	Monitor string `toml:"monitor"`

	// Layer must be "overlay" for the HUD to be visible over a fullscreen
	// game: "top" sits BELOW fullscreen surfaces. The other values are
	// accepted for debugging and warned about.
	Layer string `toml:"layer"`

	// The margins inset the surface from each edge of the monitor, which makes
	// the top-left one the ORIGIN every widget.*.x/y is measured from.
	//
	// There used to be an anchors key here, deciding which corner a single stack
	// of rows grew from. It went when each group gained its own coordinates: the
	// surface is now anchored to all four edges so that it covers the monitor and
	// a coordinate means the same thing as it does in a screenshot. Keep these at
	// zero unless you want everything shifted at once.
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

// BossMapConfig is the third-party event feed: what has spawned on your block.
//
// It is the only thing df-hud fetches from a site that is not the game's own, so
// it gets its own switch and its own interval rather than being folded into the
// poll section. Turning it off costs the threat line and nothing else.
type BossMapConfig struct {
	Enabled bool   `toml:"enabled"`
	URL     string `toml:"url"`

	// Interval is the MINIMUM gap between fetches, not the cadence. The schedule
	// itself comes from the feed's own event boundaries plus arriving on a new
	// block; this is the floor none of that can breach.
	Interval duration `toml:"interval"`
	// MaxInterval is the heartbeat, for the events that follow no cycle: the
	// once-a-day random spawns that nothing in the data predicts.
	MaxInterval duration `toml:"max_interval"`
}

// RunStartConfig turns a passed-through click on the game's own Start button into
// the run clock starting.
//
// It exists because nothing in the player record marks the client taking control -
// position, zone and df_inoutpost all survive a client exit unchanged - so the
// only thing that knows a run began is the player pressing the button.
//
// df-hud does not watch input to do this. The compositor passes the click through
// with a non-consuming bind and calls POST /api/run/click; everything below is the
// check df-hud then makes. That keeps global input monitoring, which is a
// keylogger-shaped capability, out of this program entirely.
//
// Two things make it much less fragile than a bare coordinate check: the click
// must be inside the GAME's focused window (so the rectangle is window-relative,
// not screen-relative, and works on either monitor), and it is ignored outright
// once a run is in progress - so every click fired during play is inert, and the
// rectangle only matters on the menu where the button actually is.
type RunStartConfig struct {
	// ClickEnabled gates the endpoint. With no keybind it does nothing anyway.
	ClickEnabled bool `toml:"click_enabled"`

	// The Start button, in pixels from the game window's top-left corner.
	ButtonX      int `toml:"button_x"`
	ButtonY      int `toml:"button_y"`
	ButtonWidth  int `toml:"button_width"`
	ButtonHeight int `toml:"button_height"`
}

// ButtonContains reports whether a window-relative point is on the Start button.
func (r RunStartConfig) ButtonContains(x, y int) bool {
	if r.ButtonWidth <= 0 || r.ButtonHeight <= 0 {
		return false
	}
	return x >= r.ButtonX && x < r.ButtonX+r.ButtonWidth &&
		y >= r.ButtonY && y < r.ButtonY+r.ButtonHeight
}

// TrayConfig is the StatusNotifierItem in the system tray.
//
// It exists because hiding the HUD when the game is closed leaves df-hud with no
// presence at all: a running background process with nothing on screen, no window
// to close and no way to tell whether it is even alive. The tray icon is the
// answer to "is it running, is it getting data, and how do I quit it".
type TrayConfig struct {
	Enabled bool `toml:"enabled"`
}

type WidgetConfig struct {
	Status     StatusWidgetConfig     `toml:"status"`
	Block      BlockWidgetConfig      `toml:"block"`
	Bosses     BossesWidgetConfig     `toml:"bosses"`
	Session    SessionWidgetConfig    `toml:"session"`
	XP         XPWidgetConfig         `toml:"xp"`
	Challenges ChallengesWidgetConfig `toml:"challenges"`
}

// Placement is where one group of rows sits, and how its text is drawn.
//
// x and y are in pixels from the top-left of the HUD surface, which spans the
// whole monitor, so they are screen coordinates - the same numbers you would read
// off a screenshot. (The hud.margin_* keys move that origin if you want
// everything shifted, e.g. clear of a bar.)
//
// This replaced a single stack of rows in one corner with an `order` key. That
// was the wrong model: the four groups answer unrelated questions and want to
// live in different parts of the screen, near whatever they relate to. The clock
// belongs next to the game's own clock, block info belongs on the side you look
// at for it, and neither wants to be third in a list.
//
// A zero font_size or empty font_family means "inherit from [hud]", so a group
// only needs the keys it actually differs on.
//
// Coordinates are logical pixels. On a scaled monitor they are not device pixels,
// so a HiDPI setup wants the numbers you see rather than the panel's own.
type Placement struct {
	X int `toml:"x"`
	Y int `toml:"y"`

	FontFamily string  `toml:"font_family"`
	FontSize   float64 `toml:"font_size"`
}

// StatusWidgetConfig places the HUD's own error banner - stale credentials, a
// board that cannot be fetched. It has no enabled key on purpose: it is how
// df-hud reports that it cannot do its job, and a hidden one would leave the HUD
// simply looking broken.
type StatusWidgetConfig struct {
	Placement
}

type BlockWidgetConfig struct {
	Placement
	Enabled bool `toml:"enabled"`

	// Color is this group's normal text colour, empty to follow hud.text_color.
	Color string `toml:"color"`

	// ShowPosition prints the raw coordinates: as the head out in the city, and
	// under the name of an outpost.
	//
	// Off by default because the game already shows them, under its own minimap -
	// two identical coordinate readouts an inch apart is not information. With it
	// off, the city head becomes the region name instead, which the game does not
	// show anywhere.
	//
	// It replaced show_coords, which did this for outposts only and left the city
	// head with no way to turn it off at all.
	ShowPosition bool `toml:"show_position"`
}

// BossesWidgetConfig is what the city event feed says is where you are: bosses,
// bandit packs, missions, QRF events, and the map-wide outpost attack.
//
// Its own group rather than part of Block Info, because the two answer different
// questions and want different amounts of room. Block Info is two short rows that
// change when you walk; this is a list that can be seven rows long on a boss nest
// and empty on most of the map, so anything placed under a combined group would
// move up and down as you travelled.
type BossesWidgetConfig struct {
	Placement
	Enabled bool `toml:"enabled"`

	// Color is the normal colour for these rows. The amber that says "something is
	// standing here" and the red of an outpost attack still win over it.
	Color string `toml:"color"`

	// ShowNearest reports which way to walk when your own block is empty, which is
	// most blocks. Without it the group is simply absent there - honest, but it
	// wastes the one thing the feed knows that you cannot see for yourself.
	ShowNearest bool `toml:"show_nearest"`
}

// SessionWidgetConfig is the run clock: time since you entered the inner city.
// See Store.updateRunLocked for why it is not the client's uptime.
type SessionWidgetConfig struct {
	Placement
	Enabled bool `toml:"enabled"`

	Color string `toml:"color"`

	// Prefix labels the number. A bare clock on an overlay is ambiguous - the
	// game has its own clocks - so it says what it is timing.
	Prefix string `toml:"prefix"`
}

type XPWidgetConfig struct {
	Placement
	Enabled bool `toml:"enabled"`

	// Color is the rate's normal colour. The stability amber and red still win:
	// a rate averaged over a window with a hole in it looks just as
	// authoritative as one that is not, and the colour is how it admits that.
	Color string `toml:"color"`

	Prefix string `toml:"prefix"`

	// Window is the averaging span. MinSamples is the number of usable
	// samples below which the rate is blanked rather than guessed at - two
	// points a few seconds apart extrapolate to a nonsense hourly figure.
	Window     duration `toml:"window"`
	MinSamples int      `toml:"min_samples"`
}

// ChallengesWidgetConfig is the whole board, filtered.
//
// The board splits into three sources that are worth different amounts to
// different people, so each is its own switch rather than a "max_shown" that
// crops arbitrarily:
//
//   - event challenges, which the wire marks `repeatable` (verified live: it is 1
//     on exactly the three Summer ones and 0 on everything else). They pay event
//     currency rather than XP, so they are the first thing someone not chasing
//     tickets wants gone.
//   - clan challenges, which are the clan's progress and not yours.
//   - everything else: the ordinary daily and weekly ones.
//
// ShowCompleted cuts across all three: a finished challenge is a row that will
// not change again this cycle.
type ChallengesWidgetConfig struct {
	Placement
	Enabled bool `toml:"enabled"`

	Color string `toml:"color"`

	ShowRepeatable bool `toml:"show_repeatable"`
	ShowClan       bool `toml:"show_clan"`
	ShowPersonal   bool `toml:"show_personal"`
	ShowCompleted  bool `toml:"show_completed"`

	// ShowSections draws a divider naming each category, and moves a prefix the
	// whole category shares up into it - so five clan entries say "Weekly
	// Challenge" once between them instead of once each.
	//
	// Only ever drawn when more than one category is on screen: with a single
	// category it is a label on the only thing there.
	ShowSections bool `toml:"show_sections"`

	// MaxShown caps the rows. 0 means no cap, which is the point of the window:
	// the whole board, in the board's own order.
	MaxShown int `toml:"max_shown"`

	// UrgentWithin is how close a deadline has to be for an unfinished challenge to
	// be drawn in the alarm colour. 0 turns that off.
	//
	// A display threshold, not a request interval, so it has no politeness floor -
	// but it is a key rather than a constant because "soon" depends on how you play:
	// two hours is plenty for a daily kill count and nowhere near enough for a
	// weekly you have not started.
	UrgentWithin duration `toml:"urgent_within"`
}

// EffectiveChallengeInterval is the board's cadence for the current state.
//
// The configured value is the playing cadence. With the game closed there is
// nothing generating challenge progress, so it stretches to the idle cadence:
// a short interval chosen for play must not become an all-night poll for someone
// who has turned only_when_game_running off.
func (p PollConfig) EffectiveChallengeInterval(gameRunning bool) time.Duration {
	if gameRunning {
		return p.ChallengeInterval.Duration
	}
	if p.IdleInterval.Duration > p.ChallengeInterval.Duration {
		return p.IdleInterval.Duration
	}
	return p.ChallengeInterval.Duration
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
			ChallengeInterval:   duration{30 * time.Second},
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
			Enabled:             true,
			OnlyWhenGameRunning: true,
			FollowGameWorkspace: true,
			Monitor:             "auto",
			Layer:               "overlay",
			// Zero: each group carries its own coordinates now, so the surface
			// covers the monitor and a margin would only shift every group at once.
			ClickThrough: true,
			FontFamily:   "Courier New, monospace",
			FontSize:     12,
			TextColor:    "#e6cc4d", // the game's own HUD yellow
			Opacity:      1.0,
		},
		BossMap: BossMapConfig{
			Enabled: true,
			URL:     defaultBossMapURL,
			// One minute: half of what dfprofiler's own page costs them per open
			// tab, and enough to notice a spawn within a minute of the hour
			// turning over, which is when they appear.
			Interval:    duration{time.Minute},
			MaxInterval: duration{5 * time.Minute},
		},
		RunStart: RunStartConfig{
			ClickEnabled: true,
			// Measured in the game at 2560x1440. Wrong for any other resolution,
			// which is why it is a config key and not a constant.
			ButtonX: 1230, ButtonY: 660, ButtonWidth: 100, ButtonHeight: 40,
		},
		Tray: TrayConfig{Enabled: true},
		// The default positions are measured at 2560x1440 against the game's own
		// interface: the clock beside the game's clock, the rate under it, the
		// board down the left where there is nothing to cover, and block info on
		// the right. Anything else on another resolution, which is why they are
		// config keys.
		Widget: WidgetConfig{
			Status: StatusWidgetConfig{Placement{X: 10, Y: 10}},
			// Cool blue, because the amber below it means "something here can kill
			// you". Where you are standing is not a warning and should not borrow the
			// colour of one.
			Block: BlockWidgetConfig{
				Placement: Placement{X: 2340, Y: 300}, Enabled: true, Color: "#9ecbff",
			},
			// Close under block info, and further left than it: the longest row this
			// group can draw is now "nearest 12 up 12 left  1054, 1015", and a small
			// gap between the two is what makes them read as one column.
			Bosses: BossesWidgetConfig{
				Placement: Placement{X: 2240, Y: 344}, Enabled: true, ShowNearest: true,
			},
			// White for the clock and off-white for the board: the game's own HUD is
			// already yellow and green, so the readings that are neither a warning nor
			// a status read as ours rather than as more of the game's.
			Session: SessionWidgetConfig{
				Placement: Placement{X: 350, Y: 60}, Enabled: true,
				Color: "#ffffff", Prefix: "IC Time: ",
			},
			// Five minutes, not thirty seconds. XP arrives in lumps - a kill, a
			// challenge - so a window short enough to contain no lump reads as a
			// rate of zero, and a HUD that flips between 20M/hr and nothing is
			// worse than useless: it is actively misleading about how the run is
			// going. Five minutes is long enough to smooth the gaps between kills
			// and short enough to still respond when you change what you are doing.
			XP: XPWidgetConfig{
				// White to match the clock, and because this row sits beside the game's
				// own "LV 415: 8,122,281,000" and is meant to read as its continuation.
				Placement: Placement{X: 160, Y: 85}, Enabled: true,
				Color: "#ffffff", Prefix: "Xp/Hr: ",
				Window: duration{5 * time.Minute}, MinSamples: 3,
			},
			Challenges: ChallengesWidgetConfig{
				Placement: Placement{X: 10, Y: 190}, Enabled: true,
				// Off-white rather than white: the board is the longest column on
				// screen, and the green and red it marks its own rows with need
				// somewhere quieter to stand out from.
				Color:          "#e8e8e8",
				ShowRepeatable: true, ShowClan: true, ShowPersonal: true, ShowCompleted: true,
				ShowSections: true,
				UrgentWithin: duration{2 * time.Hour},
			},
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
	c.Game.WindowClass = strings.TrimSpace(c.Game.WindowClass)
	errs = appendRange(errs, "game.scan_interval", c.Game.ScanInterval.Duration, 250*time.Millisecond, 5*time.Minute)

	// --- paths ---
	c.Paths.DataDir = expandHome(strings.TrimSpace(c.Paths.DataDir))
	if c.Paths.DataDir == "" {
		errs = append(errs, errors.New("paths.data_dir is empty"))
	}

	// --- bossmap ---
	if c.BossMap.Enabled {
		c.BossMap.URL = strings.TrimSpace(c.BossMap.URL)
		if u, err := url.Parse(c.BossMap.URL); err != nil || u.Scheme != "https" || u.Host == "" {
			errs = append(errs, fmt.Errorf("bossmap.url %q must be an absolute https URL", c.BossMap.URL))
		}
		errs = appendFloor(errs, "bossmap.interval", c.BossMap.Interval.Duration, floorBossMapInterval)
		if c.BossMap.MaxInterval.Duration < c.BossMap.Interval.Duration {
			errs = append(errs, fmt.Errorf("bossmap.max_interval (%s) is shorter than bossmap.interval (%s): "+
				"the heartbeat cannot be tighter than the minimum gap",
				c.BossMap.MaxInterval, c.BossMap.Interval))
		}
	}

	// --- run_start ---
	if c.RunStart.ClickEnabled {
		if c.RunStart.ButtonWidth <= 0 || c.RunStart.ButtonHeight <= 0 {
			errs = append(errs, fmt.Errorf("run_start button is %dx%d: a zero-sized button can never be clicked",
				c.RunStart.ButtonWidth, c.RunStart.ButtonHeight))
		}
		if c.RunStart.ButtonX < 0 || c.RunStart.ButtonY < 0 {
			errs = append(errs, fmt.Errorf("run_start button at %d,%d must be inside the window",
				c.RunStart.ButtonX, c.RunStart.ButtonY))
		}
	}

	// --- hud ---
	if c.HUD.Enabled {
		if _, err := parseLayer(c.HUD.Layer); err != nil {
			errs = append(errs, fmt.Errorf("hud.layer: %w", err))
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
	if c.Widget.Challenges.UrgentWithin.Duration < 0 {
		errs = append(errs, fmt.Errorf("widget.challenges.urgent_within %s cannot be negative (0 turns it off)",
			c.Widget.Challenges.UrgentWithin))
	}
	if c.Widget.Challenges.MaxShown < 0 {
		errs = append(errs, fmt.Errorf("widget.challenges.max_shown %d cannot be negative (0 means no cap)",
			c.Widget.Challenges.MaxShown))
	}
	// Placement and appearance, for every group. Negative coordinates would put a
	// group off the top or left of its own surface, where it is not clipped so much
	// as simply absent - which looks like the group being broken rather than
	// misplaced. The same list drives the generated CSS (see groupStyles).
	for _, g := range groupStyles(c) {
		if g.place.X < 0 || g.place.Y < 0 {
			errs = append(errs, fmt.Errorf("widget.%s position %d, %d cannot be negative: "+
				"it is measured from the top-left of the screen", g.name, g.place.X, g.place.Y))
		}
		if g.place.FontSize < 0 {
			errs = append(errs, fmt.Errorf("widget.%s.font_size %g cannot be negative (0 inherits from [hud])",
				g.name, g.place.FontSize))
		}
		// Caught here rather than at render time: GTK skips a rule it cannot parse
		// without a word, so a malformed colour would just leave the group looking
		// untouched with nothing to grep for. Note this catches bad hex and
		// stylesheet injection, not a misspelled colour NAME - validateColor stays
		// permissive there rather than shipping a list of CSS names to go stale.
		if g.color != "" {
			if err := validateColor(g.color); err != nil {
				errs = append(errs, fmt.Errorf("widget.%s.color: %w", g.name, err))
			}
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
		total += activeFraction*perHour(c.Poll.EffectiveChallengeInterval(true)) +
			(1-activeFraction)*perHour(c.Poll.EffectiveChallengeInterval(false))
	}
	total += perHour(c.Poll.CatalogInterval.Duration)
	if c.BossMap.Enabled {
		// Counted in the same number even though it is a different server: the
		// point of the budget is to be able to answer "how much traffic does this
		// thing make", and an honest answer includes everybody's.
		//
		// The minimum interval is used, so this is the WORST case - a player who
		// changes block continuously. Standing still costs the heartbeat instead.
		total += activeFraction * perHour(c.BossMap.Interval.Duration)
	}
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
