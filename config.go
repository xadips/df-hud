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

// Configuration. Three rules, each because the alternative bites later:
//
//  1. Unknown keys are an ERROR. A silently ignored typo means an evening spent
//     wondering why `only_when_game_runing` does nothing.
//  2. An interval below its floor is a STARTUP ERROR, never a silent clamp.
//     Quietly "fixing" it teaches you the number you wrote is the number in use,
//     which is the wrong lesson about a service that temp-bans bursty traffic.
//  3. Every problem is reported at once (errors.Join), so fixing a config is one
//     pass rather than a guessing game.
//
// A missing file is fine: the defaults below are the intended setup.

const (
	defaultBaseURL     = "https://fairview.deadfrontier.com/onlinezombiemmo"
	defaultAllstatsURL = "https://fairview.deadfrontier.com/onlinezombiemmo/dfdata/get_allstats.php?printvars=1"
	defaultListen      = "127.0.0.1:9275"
)

// Interval floors. Politeness, not taste: the game server is not ours, and the
// account has previously been temp-banned for bursty request patterns. The
// active floor is set so that at worst we look like a busy human playing.
const (
	floorActiveInterval = 5 * time.Second
	floorIdleInterval   = 30 * time.Second
	// 30s while playing is ~120 requests/hour for the board, a small fraction of
	// what the game client itself sends. Below that the board is not changing
	// fast enough to be worth asking.
	floorChallengeInterval = 30 * time.Second
	floorCatalogInterval   = time.Hour
	// dfprofiler's own bossmap page re-fetches every 30s per open tab
	// (bossmap.js), and the defaults stay at or under that: 1m in the city, 30s
	// in Onslaught.
	//
	// The FLOOR is 15s, deliberately below their page's rate - set where a human
	// tuning this can reach a faster cadence in Onslaught, whose 300s cycle is
	// coarse at 30s granularity, while stopping well short of hammering. Nothing
	// is fetched unless the game is running, so even the floor is bounded by
	// playing time rather than uptime.
	floorBossMapInterval = 15 * time.Second
	floorRequestTimeout  = 2 * time.Second
	ceilRequestTimeout   = 60 * time.Second
	ceilJitter           = 0.5
)

// duration lets intervals be written the way humans write them ("10s", "24h"),
// rather than as the nanosecond count time.Duration would otherwise need.
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
	GameKeys GameKeysConfig `toml:"game_keys"`
	Paths    PathsConfig    `toml:"paths"`
	HUD      HUDConfig      `toml:"hud"`
	BossMap  BossMapConfig  `toml:"bossmap"`
	Presence PresenceConfig `toml:"presence"`
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

	// SKeyGen is the fallback signing salt for hashed calls. Normally leave it
	// empty: the bridge userscript reports the live value from page context and a
	// reported salt wins, which is what makes hashed calls survive the game
	// rotating it. Set it only to work without the bridge.
	SKeyGen string `toml:"skeygen"`
}

type BridgeConfig struct {
	Enabled bool   `toml:"enabled"`
	Listen  string `toml:"listen"`
}

type PollConfig struct {
	// Active applies while the game process is running; Idle when it is not.
	// With OnlyWhenGameRunning set (the default), Idle never applies at all.
	//
	// See minRequestGap in poller.go for why this is not refined further by
	// df_inoutpost: it would be one poll stale by construction, and would delay
	// the outpost-to-city transition by a whole idle interval at exactly the
	// moment the HUD matters.
	ActiveInterval duration `toml:"active_interval"`
	IdleInterval   duration `toml:"idle_interval"`

	// ChallengeInterval is the board's cadence WHILE PLAYING. With the game
	// closed it stretches to at least IdleInterval - see
	// EffectiveChallengeInterval - so turning only_when_game_running off cannot
	// leave a 30s poll running all night.
	ChallengeInterval duration `toml:"challenge_interval"`
	CatalogInterval   duration `toml:"catalog_interval"`

	// Jitter spreads requests so a restart loop cannot line up into a thundering
	// herd, and so our traffic never looks metronomic.
	Jitter float64 `toml:"jitter"`

	// OnlyWhenGameRunning takes traffic to exactly zero when you are not playing.
	// Turning it off means polling around the clock; the idle floor is what keeps
	// that defensible.
	OnlyWhenGameRunning bool `toml:"only_when_game_running"`

	// BackoffMax caps the exponential backoff after transport failures. A
	// rejected credential is NOT a failure to back off from: it stops polling
	// outright (see ErrStaleCredentials).
	BackoffMax duration `toml:"backoff_max"`
}

type GameConfig struct {
	// Process is the client executable's name, matched against argv[0]'s basename
	// only - see procScanner.scan for why the whole command line is not searched.
	Process string `toml:"process"`

	// WindowClass identifies the game's WINDOW to the compositor, which is a
	// separate question from identifying its process: it decides where the HUD is
	// drawn (hud.follow_game_workspace and monitor = "auto"), never whether
	// df-hud polls.
	//
	// Empty means "derive it from Process", which is right when Wine reports the
	// executable name as the class. It is a key because the value can only be
	// discovered from a running game (df-hud -check-game prints it), and the
	// alternative to a one-line config fix would be a rebuild.
	WindowClass string `toml:"window_class"`

	// WindowTitleIgnore is the titles that are NOT the game even though they
	// carry its class, matched case-insensitively as substrings.
	//
	// The launcher is why. Its dialogs are the same executable and report the same
	// class - measured live:
	//
	//	class: deadfrontier.exe  title: Dead Frontier Configuration  464x406
	//	class: deadfrontier.exe  title: Input Configuration          215x78
	//
	// so class matching alone drew the HUD over a settings dialog. Even the
	// process id does not separate them, since it is one process.
	//
	// An ignore list rather than a positive match on the game's own title: a title
	// we required would break the day they append a version number, and the
	// failure would be an invisible HUD - indistinguishable from df-hud being
	// broken.
	WindowTitleIgnore []string `toml:"window_title_ignore"`

	// ScanInterval is how often /proc is scanned. A local read costing a
	// millisecond, so it has no politeness floor, only a sanity one. Hyprland
	// window events trigger an immediate rescan on top of this.
	ScanInterval duration `toml:"scan_interval"`
}

// WindowMatch is how to recognise the game's window: the class to look for,
// falling back to the process name, and the titles that mean this is not it.
func (g GameConfig) WindowMatch() windowMatch {
	class := g.WindowClass
	if class == "" {
		class = g.Process
	}
	return windowMatch{Class: class, IgnoreTitles: g.WindowTitleIgnore}
}

// WindowMatch with the launcher dialog named, for the code that sends it a key.
func (c *Config) launcherWindowMatch() windowMatch {
	m := c.Game.WindowMatch()
	m.LauncherTitle = c.GameKeys.LauncherTitle
	return m
}

// GameKeysConfig presses a key in the game's window at launch. See gamekeys.go
// for why the FPS display needs this rather than a setting.
type GameKeysConfig struct {
	// FPSDisplay turns the game's FPS readout on once per launch. Off by
	// default: df-hud synthesising input is worth opting into rather than
	// discovering.
	FPSDisplay bool `toml:"fps_display"`

	// FPSKey is the game's own binding for it, a plain X keysym name. A key here
	// is what makes this survive the game rebinding it, or a keyboard layout
	// where "y" is somewhere else.
	FPSKey string `toml:"fps_key"`

	// FPSDelay is how long to wait after the game's own window appears - not
	// after the process starts, which is when the LAUNCHER opens.
	//
	// Long enough that the key lands in the client rather than in a loading
	// screen, short enough that the readout is on before you are playing.
	FPSDelay duration `toml:"fps_delay"`

	// DismissLauncher presses the launcher dialog's default button - Play - as
	// soon as it appears, so the dialog is on screen for a moment instead of
	// waiting for you.
	//
	// It is DISMISSED rather than skipped, and that distinction is the whole
	// design. The dialog can be stopped from appearing at all, by setting
	// PlayerSettings.displayResolutionDialog in DeadFrontier_Data/mainData - but
	// the dialog is also what APPLIES the input bindings. The game reads them
	// from the Wine registry only as part of the dialog running; skip it and the
	// bindings baked into mainData are used instead, so a sprint rebound to space
	// silently becomes shift and space cycles weapons. Measured, at some cost.
	//
	// So pressing Play is not the crude version of skipping it. It is the only
	// way to keep custom keys.
	DismissLauncher bool `toml:"dismiss_launcher"`

	// LauncherKey activates that default button. Return, because Play is the
	// dialog's default and Return is what activates a default button.
	LauncherKey string `toml:"launcher_key"`

	// LauncherTitle identifies the dialog among the launcher's windows. It has to
	// be narrower than game.window_title_ignore, which contains "configuration"
	// and so matches the 215x78 "Input Configuration" key-capture box too - a
	// window with no default button, where Return would do something else.
	LauncherTitle string `toml:"launcher_title"`

	// LauncherDelay is how long to leave the dialog mapped before pressing. Long
	// enough for it to have taken keyboard focus of its own default button.
	LauncherDelay duration `toml:"launcher_delay"`
}

type PathsConfig struct {
	// DataDir holds credentials.json (0600), state.json and catalog.json.
	DataDir string `toml:"data_dir"`
}

type HUDConfig struct {
	Enabled bool `toml:"enabled"`

	// OnlyWhenGameRunning hides the HUD while the game is not running. Named to
	// match poll.only_when_game_running and separate from it on purpose: one
	// governs traffic to somebody else's server, this one governs your pixels.
	OnlyWhenGameRunning bool `toml:"only_when_game_running"`

	// FollowGameWorkspace shows the HUD only while the game's own workspace is
	// displayed. Layer-shell has no per-workspace concept, so this is emulated
	// from the compositor's window list - see visibility.go, including why it
	// fails open.
	FollowGameWorkspace bool `toml:"follow_game_workspace"`

	// Monitor is a connector name ("DP-1") or "auto".
	//
	// "auto" follows the GAME: pinned to whichever monitor the game's window is
	// on, re-checked each time the HUD is shown. If the window cannot be
	// identified the compositor chooses, which means the focused monitor.
	Monitor string `toml:"monitor"`

	// Layer must be "overlay" to be visible over a fullscreen game: "top" sits
	// BELOW fullscreen surfaces. The others are accepted for debugging and warned
	// about.
	Layer string `toml:"layer"`

	// The margins inset the surface from each edge of the monitor, which makes
	// the top-left one the ORIGIN every widget.*.x/y is measured from. Keep these
	// at zero unless you want everything shifted at once.
	MarginTop    int `toml:"margin_top"`
	MarginRight  int `toml:"margin_right"`
	MarginBottom int `toml:"margin_bottom"`
	MarginLeft   int `toml:"margin_left"`

	// ClickThrough must stay true in normal use - the HUD sits on top of the
	// game, so any pointer event it keeps is one the game does not get.
	ClickThrough bool `toml:"click_through"`

	FontFamily string  `toml:"font_family"`
	FontSize   float64 `toml:"font_size"`
	TextColor  string  `toml:"text_color"`
	Opacity    float64 `toml:"opacity"`

	// CSS is an optional stylesheet loaded after the built-in one, so appearance
	// can be changed without a rebuild.
	CSS string `toml:"css"`
}

// BossMapConfig is the third-party event feed: what has spawned on your block.
// The only thing df-hud fetches from a site that is not the game's own, so it
// gets its own switch and intervals. Turning it off costs the threat line.
type BossMapConfig struct {
	Enabled bool   `toml:"enabled"`
	URL     string `toml:"url"`

	// Interval is the MINIMUM gap between fetches, not the cadence. The schedule
	// comes from the feed's own event boundaries plus arriving on a new block;
	// this is the floor none of that can breach.
	Interval duration `toml:"interval"`
	// MaxInterval is the heartbeat, for the once-a-day random spawns that nothing
	// in the data predicts.
	MaxInterval duration `toml:"max_interval"`

	// The same two numbers for while you are standing in Onslaught, and tighter,
	// because the pair above is sized for the city's 3600s cycle. Against
	// Onslaught's 300s one that heartbeat is a whole cycle wide and can miss a
	// turnover outright.
	OnslaughtInterval    duration `toml:"onslaught_interval"`
	OnslaughtMaxInterval duration `toml:"onslaught_max_interval"`
}

// Intervals is the floor and the heartbeat for where you are standing.
func (c BossMapConfig) Intervals(onslaught bool) (min, max time.Duration) {
	if onslaught {
		return c.OnslaughtInterval.Duration, c.OnslaughtMaxInterval.Duration
	}
	return c.Interval.Duration, c.MaxInterval.Duration
}

// PresenceConfig is the game client's own position, read by pretending to be
// Discord. See presence.go for why this beats the polled position.
type PresenceConfig struct {
	Enabled bool `toml:"enabled"`

	// Socket is where to listen. Empty means $XDG_RUNTIME_DIR/discord-ipc-0,
	// which is the only path the game looks at first - so a custom one is only
	// useful with rpc-bridge's BRIDGE_RPC_PATH pointed at the same place.
	//
	// If a real Discord or Vesktop already holds that socket, df-hud logs it and
	// falls back to the poll rather than fighting for it.
	Socket string `toml:"socket"`
}

// SocketPath is the configured socket, or the default one.
func (c PresenceConfig) SocketPath() string {
	if s := strings.TrimSpace(c.Socket); s != "" {
		return expandHome(s)
	}
	return defaultPresenceSocket()
}

// TrayConfig is the StatusNotifierItem in the system tray.
//
// It exists because hiding the HUD when the game is closed leaves df-hud with no
// presence at all: a background process with nothing on screen, no window to
// close and no way to tell whether it is alive.
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
	Map        MapWidgetConfig        `toml:"map"`
}

// Placement is where one group of rows sits, and how its text is drawn.
//
// x and y are pixels from the top-left of the HUD surface, which spans the whole
// monitor - so they are the same numbers you would read off a screenshot. The
// hud.margin_* keys move that origin if you want everything shifted, e.g. clear
// of a bar.
//
// A zero font_size or empty font_family means "inherit from [hud]".
//
// Coordinates are logical pixels, so a scaled monitor wants the numbers you see
// rather than the panel's own.
type Placement struct {
	X int `toml:"x"`
	Y int `toml:"y"`

	FontFamily string  `toml:"font_family"`
	FontSize   float64 `toml:"font_size"`
}

// StatusWidgetConfig places the HUD's own error banner. No enabled key on
// purpose: it is how df-hud reports that it cannot do its job, and a hidden one
// would leave the HUD simply looking broken.
type StatusWidgetConfig struct {
	Placement
}

type BlockWidgetConfig struct {
	Placement
	Enabled bool `toml:"enabled"`

	// Color is this group's normal text colour, empty to follow hud.text_color.
	Color string `toml:"color"`

	// ShowPosition prints the raw coordinates. Off by default because the game
	// already shows them under its own minimap; with it off the city head becomes
	// the region name, which the game does not show anywhere.
	ShowPosition bool `toml:"show_position"`
}

// BossesWidgetConfig is what the city event feed says is where you are.
//
// Its own group rather than part of Block Info: that is two short rows, while
// this can be seven rows long on a nest and empty on most of the map, so
// anything under a combined group would move as you travelled.
type BossesWidgetConfig struct {
	Placement
	Enabled bool `toml:"enabled"`

	// Color is the normal colour for these rows. The amber that says "something
	// is standing here" and the red of an outpost attack still win over it.
	Color string `toml:"color"`

	// ShowNearest reports which way to walk when your own block is empty, which
	// is most blocks.
	ShowNearest bool `toml:"show_nearest"`
}

// SessionWidgetConfig is the run clock: time since you entered the inner city.
// See Store.updateRunLocked for why it is not the client's uptime.
type SessionWidgetConfig struct {
	Placement
	Enabled bool `toml:"enabled"`

	Color string `toml:"color"`

	// Prefix labels the number. A bare clock on an overlay is ambiguous, since
	// the game has its own clocks.
	Prefix string `toml:"prefix"`
}

type XPWidgetConfig struct {
	Placement
	Enabled bool `toml:"enabled"`

	// Color is the rate's normal colour. The stability amber and red still win: a
	// rate averaged over a window with a hole in it looks just as authoritative as
	// one that is not, and the colour is how it admits that.
	Color string `toml:"color"`

	Prefix string `toml:"prefix"`

	// Window is the averaging span. MinSamples is the number of usable samples
	// below which the rate is blanked rather than guessed at - two points a few
	// seconds apart extrapolate to a nonsense hourly figure.
	Window     duration `toml:"window"`
	MinSamples int      `toml:"min_samples"`
}

// ChallengesWidgetConfig is the whole board, filtered.
//
// Three sources worth different amounts to different people, so each is its own
// switch rather than a max_shown that crops arbitrarily:
//
//   - event challenges, which the wire marks `repeatable` (verified live: 1 on
//     exactly the three Summer ones). They pay event currency rather than XP.
//   - clan challenges, which are the clan's progress and not yours.
//   - everything else: the ordinary dailies and weeklies.
//
// ShowCompleted cuts across all three.
type ChallengesWidgetConfig struct {
	Placement
	Enabled bool `toml:"enabled"`

	Color string `toml:"color"`

	ShowRepeatable bool `toml:"show_repeatable"`
	ShowClan       bool `toml:"show_clan"`
	ShowPersonal   bool `toml:"show_personal"`
	ShowCompleted  bool `toml:"show_completed"`

	// ShowSections draws a divider naming each category and moves a shared prefix
	// up into it, so five clan entries say "Weekly Challenge" once between them.
	// Only drawn when more than one category is on screen.
	ShowSections bool `toml:"show_sections"`

	// MaxShown caps the rows. 0 means no cap, which is the point of the window.
	MaxShown int `toml:"max_shown"`

	// UrgentWithin is how close a deadline has to be for an unfinished challenge
	// to be drawn in the alarm colour. 0 turns that off.
	//
	// A display threshold, not a request interval, so it has no politeness floor -
	// but a key rather than a constant because "soon" depends on how you play.
	UrgentWithin duration `toml:"urgent_within"`
}

// mapMinCell is the smallest cell worth drawing: below it a marker character
// stops being legible. mapMaxCell exists because the scale is a budget for the
// WHOLE window, so a tight radius divides it by very few blocks - radius 2 would
// otherwise ask for 236px cells.
//
// mapBaseSize is what scale 1.0 means: the pixel budget for the longest side,
// which at the full 59-block city is 20 pixels a block.
//
// Here rather than beside the drawing code because the config has to reject what
// it cannot draw, and the config compiles without GTK.
const (
	mapMinCell  = 6
	mapMaxCell  = 96
	mapBaseSize = 1180
)

// MapWidgetConfig is the whole city drawn as a grid, in the shape DFProfiler's
// map draws it: a block per cell, shaded by difficulty band, gaps left empty.
//
// It starts HIDDEN even when enabled, and a key brings it up
// (POST /api/widget/map/toggle) - this is something you summon to decide where
// to walk and dismiss ten seconds later. `enabled` still means "do I ever want
// this", so turning it off costs the key as well.
type MapWidgetConfig struct {
	Placement
	Enabled bool `toml:"enabled"`

	Color string `toml:"color"`

	// Center puts the grid in the middle of the monitor and ignores x/y. On by
	// default, because the right coordinate depends on both the monitor and the
	// scale - 1180 pixels wide at 1.0, 1475 at 1.25.
	Center bool `toml:"center"`

	// OffsetX and OffsetY move the centred map, in pixels. Applied on top of the
	// centring rather than instead of it, so "60 pixels up from centre" keeps
	// meaning that when the scale, radius or monitor changes.
	//
	// Ignored when center = false, where x/y already say where it goes.
	OffsetX int `toml:"offset_x"`
	OffsetY int `toml:"offset_y"`

	// Radius crops the map to a square around you, in blocks: 15 draws 31x31, 0
	// is the whole city. At the full 59x55 most of the picture is somewhere you
	// are not going. The key beside the grid crops to match.
	//
	// The window is clamped into the city rather than hanging off the edge, so
	// its size never changes and the group does not jump sideways as you approach
	// a boundary; near an edge you are simply off-centre.
	Radius int `toml:"radius"`

	// Scale sizes the whole group - grid AND key - from one number. 1.0 is 1180
	// pixels across the longest side, 20 per block at the full city, a 13pt key.
	//
	// A pixel budget for the longest side rather than a size per block, which is
	// what makes a cropped map BIGGER rather than merely smaller: the same budget
	// over 31 blocks gives 38px cells, so cutting the radius zooms in. The key's
	// font is derived from the cell size, so one knob moves the lot.
	//
	// FontSize still pins the key's text if you want it fixed while the map moves.
	Scale float64 `toml:"scale"`

	// Opacity multiplies the map's own alpha, which is already partial (0.7, the
	// value their stylesheet uses). The markers are NOT affected: the point is to
	// see the game through the city, not to make the writing on it faint.
	Opacity float64 `toml:"opacity"`

	// ShowList draws the key beside the grid. Without it the digits in the cells
	// mean nothing.
	ShowList bool `toml:"show_list"`
	// MaxListed caps that list, 0 for no cap. What is dropped is counted rather
	// than silently omitted - which is the point of the cap: overflow COUNTED
	// rather than clipped by the edge of the screen. 40 never triggers on a real
	// feed (a busy cycle carries about thirty events) and still fits beside a
	// 1100px map on a 1440px monitor.
	MaxListed int `toml:"max_listed"`
}

// EffectiveChallengeInterval is the board's cadence for the current state.
//
// The configured value is the playing cadence. With the game closed nothing
// generates challenge progress, so it stretches to the idle cadence: a short
// interval chosen for play must not become an all-night poll for someone who has
// turned only_when_game_running off.
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
// min_samples at the current poll cadence - otherwise the rate would sit on "not
// enough data" forever and look broken.
//
// Deliberately a derivation rather than a validation error, unlike the interval
// floors: those are politeness limits on somebody else's server, while this is a
// local display setting whose worst failure is a blank widget. Raising the poll
// interval to be MORE polite should not be rejected because of a widget default
// nobody touched. The caller logs the adjustment at startup.
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
	// An ordinary toplevel window, for the things a HUD group cannot be: the full
	// challenge list with every objective, diagnostics. NOT BUILT YET -
	// /api/console/toggle answers 503 - but the size is validated so it is yours
	// when it lands.
	Width  int `toml:"width"`
	Height int `toml:"height"`
}

// defaultConfig is the shipped setup, and what runs when no file exists. The
// example TOML must agree with it; config_test.go checks that.
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
			Process: defaultGameProcess,
			// The launcher's dialogs are both "<something> Configuration" and
			// carry the game's own class and pid, so this one word is what keeps
			// the HUD off a 464x406 settings box.
			WindowTitleIgnore: []string{"configuration"},
			ScanInterval:      duration{2 * time.Second},
		},
		GameKeys: GameKeysConfig{
			FPSKey: "y",
			// Five seconds after the client's own window is mapped. Measured
			// against nothing in particular, and it does not need to be: the
			// readout is a toggle you want on before you are playing, not at a
			// precise moment.
			FPSDelay:      duration{5 * time.Second},
			LauncherKey:   "Return",
			LauncherTitle: "Dead Frontier Configuration",
			LauncherDelay: duration{500 * time.Millisecond},
		},
		Paths: PathsConfig{DataDir: defaultDataDir()},
		HUD: HUDConfig{
			Enabled:             true,
			OnlyWhenGameRunning: true,
			FollowGameWorkspace: true,
			Monitor:             "auto",
			Layer:               "overlay",
			// Margins zero: each group carries its own coordinates, so the surface
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
			// tab, and enough to notice a spawn within a minute of the hour.
			Interval:    duration{time.Minute},
			MaxInterval: duration{5 * time.Minute},
			// In Onslaught, at their page's own rate rather than half of it. It
			// applies only while standing on 3000,3000 with the game running, and
			// it is the difference between seeing the next cycle when their page
			// does and seeing it a minute later.
			OnslaughtInterval:    duration{30 * time.Second},
			OnslaughtMaxInterval: duration{time.Minute},
		},
		// On by default: it needs no credentials, no network and no Discord, and
		// where it cannot bind it simply stands down.
		Presence: PresenceConfig{Enabled: true},
		Tray:     TrayConfig{Enabled: true},
		// Default positions measured at 2560x1440 against the game's own
		// interface: the clock beside the game's clock, the rate under it, the
		// board down the left where there is nothing to cover, block info right.
		Widget: WidgetConfig{
			Status: StatusWidgetConfig{Placement{X: 10, Y: 10}},
			// Cool blue, because the amber below it means "something here can kill
			// you" and where you are standing is not a warning.
			Block: BlockWidgetConfig{
				Placement: Placement{X: 2340, Y: 300}, Enabled: true, Color: "#9ecbff",
			},
			// Further left than block info: the longest row here is "nearest 12 up
			// 12 left  1054, 1015", and a small gap makes the two read as one column.
			Bosses: BossesWidgetConfig{
				Placement: Placement{X: 2240, Y: 344}, Enabled: true, ShowNearest: true,
			},
			// White, because the game's own HUD is already yellow and green: a
			// reading that is neither a warning nor a status should read as ours.
			Session: SessionWidgetConfig{
				Placement: Placement{X: 350, Y: 60}, Enabled: true,
				Color: "#ffffff", Prefix: "IC Time: ",
			},
			// Five minutes, not thirty seconds. XP arrives in lumps, so a window
			// short enough to contain no lump reads as a rate of zero, and a HUD
			// flipping between 20M/hr and nothing is actively misleading.
			XP: XPWidgetConfig{
				// White to match the clock, and because this sits beside the game's
				// own "LV 415: 8,122,281,000" and reads as its continuation.
				Placement: Placement{X: 160, Y: 85}, Enabled: true,
				Color: "#ffffff", Prefix: "Xp/Hr: ",
				Window: duration{5 * time.Minute}, MinSamples: 3,
			},
			Map: MapWidgetConfig{
				// Middle of a 2560-wide screen, clear of the board on the left and
				// the boss column on the right. Hidden until a key asks for it, so
				// this is where it appears rather than where it lives.
				Placement: Placement{X: 700, Y: 240}, Enabled: true,
				Color:  "#e8e8e8",
				Center: true,
				// The whole city by default: it is what the map is FOR the first
				// time you open it, and cropping is a preference you arrive at.
				Radius:  0,
				Scale:   1,
				Opacity: 1, ShowList: true, MaxListed: 40,
			},
			Challenges: ChallengesWidgetConfig{
				Placement: Placement{X: 10, Y: 190}, Enabled: true,
				// Off-white: the longest column on screen, and the green and red it
				// marks rows with need somewhere quieter to stand out from.
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

// expandHome turns a leading ~ into the home directory, because paths in a
// hand-edited config get written with ~ whatever we document.
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

// validate normalises then checks, collecting every problem so one run reports
// the whole list.
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
		// An honest, identifiable UA is the difference between a tool the server
		// operator can contact and one that just looks like abuse.
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
		// Only the basename is compared, so a path here would never match and the
		// failure would be silent.
		errs = append(errs, fmt.Errorf("game.process %q must be a bare executable name, not a path",
			c.Game.Process))
	}
	c.Game.WindowClass = strings.TrimSpace(c.Game.WindowClass)
	errs = appendRange(errs, "game.scan_interval", c.Game.ScanInterval.Duration, 250*time.Millisecond, 5*time.Minute)

	// --- game_keys ---
	c.GameKeys.FPSKey = strings.TrimSpace(c.GameKeys.FPSKey)
	if c.GameKeys.FPSDisplay {
		if c.GameKeys.FPSKey == "" {
			errs = append(errs, errors.New("game_keys.fps_display is on but game_keys.fps_key is empty"))
		} else if !hyprKeyName.MatchString(c.GameKeys.FPSKey) {
			// The key is interpolated into a compositor command, so anything
			// outside this alphabet is refused at load rather than at the moment
			// it would have been sent - which is mid-launch, with nobody watching.
			errs = append(errs, fmt.Errorf("game_keys.fps_key %q must be a plain key name "+
				"like \"y\" or \"F1\"", c.GameKeys.FPSKey))
		}
	}
	errs = appendRange(errs, "game_keys.fps_delay", c.GameKeys.FPSDelay.Duration, 0, 5*time.Minute)
	c.GameKeys.LauncherKey = strings.TrimSpace(c.GameKeys.LauncherKey)
	c.GameKeys.LauncherTitle = strings.TrimSpace(c.GameKeys.LauncherTitle)
	if c.GameKeys.DismissLauncher {
		if !hyprKeyName.MatchString(c.GameKeys.LauncherKey) {
			errs = append(errs, fmt.Errorf("game_keys.launcher_key %q must be a plain key name "+
				"like \"Return\"", c.GameKeys.LauncherKey))
		}
		if c.GameKeys.LauncherTitle == "" {
			// Without it there is nothing to tell the dialog apart from the
			// key-capture box, and Return would be sent to whichever came first.
			errs = append(errs, errors.New("game_keys.dismiss_launcher is on but "+
				"game_keys.launcher_title is empty"))
		} else if !c.Game.WindowMatch().ignored(c.GameKeys.LauncherTitle) {
			// The dialog must also be one of the windows the HUD refuses to draw
			// over. If it is not, df-hud thinks the launcher IS the game, and the
			// dismissal never runs because there is no launcher to see.
			errs = append(errs, fmt.Errorf("game_keys.launcher_title %q does not match any of "+
				"game.window_title_ignore (%s), so df-hud would treat that window as the game",
				c.GameKeys.LauncherTitle, strings.Join(c.Game.WindowTitleIgnore, ", ")))
		}
	}
	errs = appendRange(errs, "game_keys.launcher_delay", c.GameKeys.LauncherDelay.Duration, 0, time.Minute)

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
		errs = appendFloor(errs, "bossmap.onslaught_interval", c.BossMap.OnslaughtInterval.Duration, floorBossMapInterval)
		if c.BossMap.OnslaughtMaxInterval.Duration < c.BossMap.OnslaughtInterval.Duration {
			errs = append(errs, fmt.Errorf(
				"bossmap.onslaught_max_interval %s is below bossmap.onslaught_interval %s - the heartbeat cannot be shorter than the floor it has to respect",
				c.BossMap.OnslaughtMaxInterval, c.BossMap.OnslaughtInterval))
		}
		if c.BossMap.MaxInterval.Duration < c.BossMap.Interval.Duration {
			errs = append(errs, fmt.Errorf("bossmap.max_interval (%s) is shorter than bossmap.interval (%s): "+
				"the heartbeat cannot be tighter than the minimum gap",
				c.BossMap.MaxInterval, c.BossMap.Interval))
		}
	}

	// --- run_start ---

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
	// Negative coordinates would put a group off the top or left of its own
	// surface, where it is not clipped so much as simply absent - which looks like
	// the group being broken rather than misplaced. The same list drives the
	// generated CSS (see groupStyles).
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
		// without a word, so a malformed colour would leave the group looking
		// untouched with nothing to grep for. This catches bad hex and stylesheet
		// injection, not a misspelled colour NAME - validateColor stays permissive
		// there rather than shipping a list of CSS names to go stale.
		if g.color != "" {
			if err := validateColor(g.color); err != nil {
				errs = append(errs, fmt.Errorf("widget.%s.color: %w", g.name, err))
			}
		}
	}

	// --- map ---
	//
	// A cell too small to read is not a smaller map but a coloured smear, and an
	// opacity of 0 is an invisible group that looks exactly like a broken one.
	if c.Widget.Map.Enabled {
		if c.Widget.Map.Scale <= 0 {
			errs = append(errs, fmt.Errorf("widget.map.scale %g must be positive (1.0 is a %dpx map)",
				c.Widget.Map.Scale, mapBaseSize))
		}
		if c.Widget.Map.Scale > 4 {
			errs = append(errs, fmt.Errorf("widget.map.scale %g is %dpx across, past any monitor (keep it at 4 or under)",
				c.Widget.Map.Scale, int(c.Widget.Map.Scale*mapBaseSize)))
		}
		if c.Widget.Map.Opacity <= 0 || c.Widget.Map.Opacity > 1 {
			errs = append(errs, fmt.Errorf("widget.map.opacity %g must be above 0 and at most 1",
				c.Widget.Map.Opacity))
		}
		if c.Widget.Map.Radius < 0 {
			errs = append(errs, fmt.Errorf("widget.map.radius %d cannot be negative (0 means the whole city)",
				c.Widget.Map.Radius))
		}
		if c.Widget.Map.MaxListed < 0 {
			errs = append(errs, fmt.Errorf("widget.map.max_listed %d cannot be negative (0 means no cap)",
				c.Widget.Map.MaxListed))
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
// why, because the natural reaction to a rejected interval is to wonder whether
// the number is arbitrary.
func appendFloor(errs []error, key string, got, floor time.Duration) []error {
	if got < floor {
		return append(errs, fmt.Errorf("%s is %s, below the %s minimum: this decides how often df-hud "+
			"hits somebody's server, so it is rejected rather than quietly raised", key, got, floor))
	}
	return errs
}

func appendRange(errs []error, key string, got, lo, hi time.Duration) []error {
	if got < lo || got > hi {
		return append(errs, fmt.Errorf("%s is %s, outside the allowed %s..%s", key, got, lo, hi))
	}
	return errs
}

// parseLayer maps the config name to the protocol value. Only "overlay" is above
// a fullscreen window; HidesUnderFullscreen reports whether the choice will hide
// under the game so the caller can say so at startup.
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

// validateColor accepts what GTK's CSS accepts where we substitute it: #rgb,
// #rrggbb, #rrggbbaa, or a bare name. It catches a typo at startup instead of as
// a GTK parse warning and a HUD that renders black.
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
	// stylesheet injection rather than a colour, since this is interpolated into
	// CSS.
	if strings.ContainsAny(s, `{}"';`) {
		return fmt.Errorf("%q is not a colour", s)
	}
	return nil
}

// Paths derived from DataDir. Methods so there is one definition of each
// filename and the poller/store cannot drift from the bridge.
func (c *Config) CredentialsPath() string { return filepath.Join(c.Paths.DataDir, "credentials.json") }
func (c *Config) StatePath() string       { return filepath.Join(c.Paths.DataDir, "state.json") }
func (c *Config) CatalogPath() string     { return filepath.Join(c.Paths.DataDir, "catalog.json") }

// EnsureDataDir creates the data directory and proves it is writable at startup,
// rather than discovering at the first save that nothing persists. 0700 because
// credentials.json lives here.
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

// SigningSalt is the salt for hashed calls: whatever the bridge last reported,
// falling back to the configured value. Reported wins because it comes from live
// page context, so a game-side rotation heals itself.
func (c *Config) SigningSalt(store *credStore) string {
	if store != nil {
		if salt := store.Salt(); salt != "" {
			return salt
		}
	}
	return c.DF.SKeyGen
}

// RequestsPerHour is the documented traffic budget, printed at startup. Being
// able to state the number is what makes "is this polite?" answerable instead of
// a feeling.
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
		// Counted here even though it is a different server: the point of the
		// budget is to answer "how much traffic does this thing make", and an
		// honest answer includes everybody's. The minimum interval is used, so
		// this is the WORST case - a player changing block continuously.
		//
		// onslaught_interval is deliberately NOT counted, though it is tighter.
		// This number is what an hour of normal play costs, and an hour spent
		// entirely inside Onslaught is a different activity - folding its floor in
		// would double the figure quoted for everybody to describe a case almost
		// nobody is in. The ceiling it implies: while standing on 3000,3000 this
		// feed is fetched at up to 120/h, parity with one open tab of their page.
		total += activeFraction * perHour(c.BossMap.Interval.Duration)
	}
	return total
}

// reloadableFrom returns cfg with the fields that cannot change at runtime
// copied back from old, plus the names of any that were changed. Rebinding a
// listener or moving the data directory under a running poller is not worth the
// failure modes, so those need a restart - said plainly rather than pretended to.
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
