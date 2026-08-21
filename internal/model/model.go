// Package model contains domain data shared across df-hud services.
//
// It intentionally contains no service, persistence, transport, or platform
// logic, so focused internal packages can exchange data without import cycles.
package model

import (
	"strings"
	"time"
)

// GameState is one observation of the game process.
type GameState struct {
	Running   bool
	PID       int
	StartedAt time.Time
}

func (g GameState) Elapsed(now time.Time) time.Duration {
	if !g.Running || g.StartedAt.IsZero() {
		return 0
	}
	if d := now.Sub(g.StartedAt); d > 0 {
		return d
	}
	return 0
}

func (g GameState) SameSession(other GameState) bool {
	return g.Running && other.Running && g.PID == other.PID && g.StartedAt.Equal(other.StartedAt)
}

// PresenceState is the game client's last published location.
type PresenceState struct {
	At time.Time

	HasPosition bool
	X, Y        int
	Place       string
	Indoors     bool

	InOutpost   bool
	OutpostName string
	Loading     bool
	Details     string
}

// Objective is one requirement within a challenge.
type Objective struct {
	Name     string
	Target   int64
	Score    int64
	HasScore bool
}

func (o Objective) Done() bool { return o.Target > 0 && o.Score >= o.Target }

func (o Objective) Fraction() float64 {
	if o.Target <= 0 {
		return 0
	}
	if f := float64(o.Score) / float64(o.Target); f < 1 {
		return f
	}
	return 1
}

// Challenge is one entry on the challenge board.
type Challenge struct {
	Index int
	ID    string
	Name  string
	Desc  string
	Clan  bool

	Start time.Time
	End   time.Time

	Objectives []Objective

	MinLevel, MaxLevel int
	Repeatable         bool

	RewardExp     int64
	RewardCash    int64
	RewardCredits int64
	RewardPoints  int64
	RewardItems   string
	RewardSpecial string
}

func (c Challenge) Complete() bool {
	if len(c.Objectives) == 0 {
		return false
	}
	for _, o := range c.Objectives {
		if !o.Done() {
			return false
		}
	}
	return true
}

func (c Challenge) Progress() (score, target int64) {
	for _, o := range c.Objectives {
		score += o.Score
		target += o.Target
	}
	return score, target
}

func (c Challenge) Started() bool {
	for _, o := range c.Objectives {
		if o.Score > 0 {
			return true
		}
	}
	return false
}

func (c Challenge) Remaining(now time.Time) time.Duration {
	if c.End.IsZero() {
		return 0
	}
	if d := c.End.Sub(now); d > 0 {
		return d
	}
	return 0
}

func (c Challenge) Eligible(level int) bool {
	if c.Clan || c.MinLevel == 0 && c.MaxLevel == 0 {
		return true
	}
	if level >= c.MinLevel && level <= c.MaxLevel {
		return true
	}
	for _, o := range c.Objectives {
		if o.HasScore {
			return true
		}
	}
	return false
}

// XPSource records which cumulative-XP tier supplied a snapshot.
type XPSource int

const (
	XPSourceNone XPSource = iota
	XPSourceExpTotal
	XPSourceTable
)

func (s XPSource) String() string {
	switch s {
	case XPSourceExpTotal:
		return "df_exptotal"
	case XPSourceTable:
		return "exp table reconstruction"
	default:
		return "unavailable"
	}
}

// Deadline is one game expiry timestamp, including its never-expiring state.
type Deadline struct {
	At      time.Time
	Forever bool
}

func (d Deadline) Set() bool { return d.Forever || !d.At.IsZero() }

func (d Deadline) Remaining(now time.Time) time.Duration {
	if d.Forever || d.At.IsZero() {
		return 0
	}
	if remaining := d.At.Sub(now); remaining > 0 {
		return remaining
	}
	return 0
}

// Snapshot is one parsed player-record observation.
type Snapshot struct {
	At time.Time

	Level         int
	ExpInLevel    int64
	CumulativeXP  int64
	XPSource      XPSource
	ExpNeeded     int64
	PendingLevels int
	FreePoints    int

	ExpSinceStart    int64
	HasExpSinceStart bool

	PositionX, PositionY, PositionZ int
	HasPosition                     bool

	TradeZone    int
	InOutpost    bool
	DangerLevel  int
	HasDanger    bool
	BlockSupport Deadline

	HP, HPMax   int
	Cash        int64
	HasCash     bool
	BankCash    int64
	Nourishment int
	HasHunger   bool

	BoostExp   Deadline
	Session3D  string
	GoldMember bool
	Dead       bool
	ServerTime time.Time
}

// XPSample is one point in the persistent rate window.
type XPSample struct {
	At         time.Time `json:"at"`
	Cumulative int64     `json:"cumulative"`
	Source     string    `json:"source"`
}

// RunState is a run in progress persisted across df-hud restarts.
type RunState struct {
	StartedAt     time.Time `json:"started_at"`
	GamePID       int       `json:"game_pid"`
	GameStartedAt time.Time `json:"game_started_at"`
}

func (r *RunState) Matches(g GameState) bool {
	if r == nil || !g.Running || r.StartedAt.IsZero() {
		return false
	}
	return r.GamePID == g.PID && r.GameStartedAt.Equal(g.StartedAt)
}

// Tick is one player-record polling outcome.
type Tick struct {
	At        time.Time
	Vars      map[string]string
	Err       error
	Scheduled bool
}

// PollerStatus explains why player-record data is or is not arriving.
type PollerStatus struct {
	Paused       bool
	PauseReason  string
	Stale        bool
	Failures     int
	LastSuccess  time.Time
	LastAttempt  time.Time
	LastError    string
	NextAttempt  time.Time
	TotalPolls   int
	TotalFailure int
}

// XPStability describes confidence based on missed scheduled polls.
type XPStability int

const (
	XPSteady XPStability = iota
	XPShaky
	XPUnstable
)

func (s XPStability) String() string {
	switch s {
	case XPShaky:
		return "shaky"
	case XPUnstable:
		return "unstable"
	default:
		return "steady"
	}
}

func (s XPStability) CSSClass() string {
	switch s {
	case XPShaky:
		return "shaky"
	case XPUnstable:
		return "unstable"
	default:
		return ""
	}
}

// XPRate is a computed rate plus its trust context.
type XPRate struct {
	Available   bool
	PerHour     float64
	Gained      int64
	Span        time.Duration
	Samples     int
	Stability   XPStability
	Provisional bool
	Why         string
}

// CityEventKind classifies a city event.
type CityEventKind int

const (
	EventSpawn CityEventKind = iota
	EventMission
	EventQRF
	EventUnknown
)

func (k CityEventKind) String() string {
	switch k {
	case EventMission:
		return "mission"
	case EventQRF:
		return "qrf"
	case EventUnknown:
		return "unknown"
	default:
		return "spawn"
	}
}

// CityEvent is one decoded event-feed entry.
type CityEvent struct {
	ID         string
	Kind       CityEventKind
	Title      string
	Enemies    []string
	Objectives []string
	RewardExp  int64
	Slot       int
	Locations  [][2]int
	Start      time.Time
	End        time.Time
	Started    bool
	Ended      bool
	Onslaught  bool
}

// Label returns the compact event description used by HUD and diagnostics.
func (e CityEvent) Label() string {
	switch e.Kind {
	case EventMission:
		if e.Title != "" {
			return "mission: " + e.Title
		}
		return "mission"
	case EventQRF:
		if e.Title != "" {
			return e.Title
		}
		return "quick reaction force"
	case EventUnknown:
		return "unknown GPS"
	}
	if len(e.Enemies) == 0 {
		return "something"
	}
	return strings.Join(e.Enemies, " + ")
}

func (e CityEvent) ActiveAt(now time.Time) bool {
	if e.Start.IsZero() || e.End.IsZero() {
		return e.Started && !e.Ended
	}
	return !now.Before(e.Start) && now.Before(e.End)
}

func (e CityEvent) UpcomingAt(now time.Time) bool {
	if e.Start.IsZero() {
		return !e.Started && !e.Ended
	}
	return now.Before(e.Start)
}

func (e CityEvent) EndedRecentlyAt(now time.Time, pastWindow time.Duration) bool {
	if e.End.IsZero() {
		return e.Ended
	}
	return !now.Before(e.End) && now.Sub(e.End) <= pastWindow
}

// Walk is a route summary in city blocks.
type Walk struct {
	Blocks int
	DX, DY int
	Detour int
}

// CityMark is one active event at one location, ready for presentation.
type CityMark struct {
	Marker    string
	Label     string
	Enemies   []string
	Kind      CityEventKind
	X, Y      int
	EndsIn    time.Duration
	OffMap    bool
	Walk      Walk
	Reachable bool
}

// View is the immutable, presentation-ready state derived by the store.
type View struct {
	Now time.Time

	HaveData bool
	DataAge  time.Duration

	GameRunning  bool
	HasSession   bool
	SessionTime  time.Duration
	ClientUptime time.Duration

	Level         int
	ExpInLevel    int64
	ExpNeeded     int64
	PendingLevels int
	CumulativeXP  int64
	XPSource      string

	HasPosition    bool
	PositionX      int
	PositionY      int
	PositionZ      int
	PositionSource string
	TradeZone      int
	ZoneName       string
	InOutpost      bool
	OutpostName    string
	HasDanger      bool
	DangerLevel    int
	BlockSupport   time.Duration

	BlockEvents           []CityEvent
	BlockEventsPast       []CityEvent
	BlockEventsUpcoming   []CityEvent
	HasOnslaughtCountdown bool
	OnslaughtCountdown    time.Duration
	OutpostAttack         bool

	HasNearest              bool
	NearestLabel            string
	NearestDX, NearestDY    int
	NearestX, NearestY      int
	NearestDistanceInBlocks int
	NearestDetour           int

	CityMarks  []CityMark
	BossMapAge time.Duration
	HasBossMap bool

	HP, HPMax       int
	Cash            int64
	Nourishment     int
	HasHunger       bool
	BoostExpIn      time.Duration
	BoostExpForever bool
	Dead            bool

	XPPerHour     float64
	XPAvailable   bool
	XPProvisional bool
	XPWhy         string
	XPSpan        time.Duration
	XPSamples     int
	XPStability   XPStability

	Challenges      []Challenge
	ChallengeStatus string
	ChallengesDone  int
	ChallengesTotal int

	Status      string
	StatusIsFix bool
	MissedTicks int
}

// Visibility is the published HUD visibility decision.
type Visibility struct {
	Visible bool
	Reason  string
	Monitor string
}
