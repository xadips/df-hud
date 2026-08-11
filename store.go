package main

import (
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The store is the single place widgets read from. The poller writes snapshots
// into it, the UI derives a View out of it, and nothing else talks to either
// side. Widgets therefore never issue requests, never parse the wire format, and
// never see a map[string]string.

// xpSource records which cumulative-XP tier a snapshot used, so the choice is
// observable rather than assumed.
type xpSource int

const (
	xpSourceNone     xpSource = iota
	xpSourceExpTotal          // df_exptotal, straight from the server
	xpSourceTable             // sum(exp_lvl 2..level) + df_exp, from the catalog
)

func (s xpSource) String() string {
	switch s {
	case xpSourceExpTotal:
		return "df_exptotal"
	case xpSourceTable:
		return "exp table reconstruction"
	}
	return "unavailable"
}

// Snapshot is one parsed observation of the player record. Every field is
// derived from a get_values response; anything the response did not contain is
// left at its zero value and flagged by the corresponding Has* bool, because
// "absent" and "zero" mean genuinely different things here (df_dangerlevel=0 is
// a real danger level; a missing df_dangerlevel is not).
type Snapshot struct {
	At time.Time

	Level      int
	ExpInLevel int64
	// CumulativeXP is total XP earned, the counter XP/hr differences. See
	// XPSource for where it came from.
	CumulativeXP int64
	XPSource     xpSource
	// ExpNeeded is the threshold to leave the current level, 0 at the cap or
	// without a catalog.
	ExpNeeded int64
	// PendingLevels is how many levels the banked XP is already worth. It is
	// normally 0, but the client never levels you up - XP piles into df_exp for
	// a whole run and is cashed in on the way back to an outpost - so this can
	// legitimately be 20 or more.
	PendingLevels int
	FreePoints    int

	PositionX, PositionY, PositionZ int
	HasPosition                     bool

	TradeZone   int
	InOutpost   bool
	DangerLevel int
	HasDanger   bool
	// BlockSupportUntil is when the current block's support expires, zero when
	// there is none.
	BlockSupportUntil time.Time

	HP, HPMax   int
	Cash        int64
	BankCash    int64
	Nourishment int
	HasHunger   bool

	// BoostExpUntil is when an XP boost ends. The XP widget resets its window
	// on a change here, since the rate either side is not comparable.
	BoostExpUntil time.Time

	// Dead is the server's own flag, not an inference from HP.
	Dead bool

	ServerTime time.Time
}

// dfTimeOffset is the constant the game adds to its stored timestamps
// (challenge.js:305). Every *until* field carries it.
const dfTimeOffset = 1_200_000_000

// parseSnapshot turns a get_values response into a Snapshot. It never fails:
// a field that is missing or unparseable is simply absent from the result,
// because a HUD that renders four of five widgets beats one that renders none
// over a single unexpected value.
func parseSnapshot(vars map[string]string, at time.Time, catalog *Catalog) Snapshot {
	s := Snapshot{At: at}

	s.Level, _ = intVar(vars, "df_level")
	s.ExpInLevel, _ = int64Var(vars, "df_exp")
	s.FreePoints, _ = intVar(vars, "df_freepoints")

	// Tier 1: the server's own cumulative counter. Present in every captured
	// player record, and preferred because it needs no catalog and no
	// arithmetic. Tier 2 reconstructs it from the XP table, which differs by a
	// fixed historical offset but advances identically - see
	// knowledge/allstats-map-and-xp.md.
	if total, ok := int64Var(vars, "df_exptotal"); ok && total > 0 {
		s.CumulativeXP, s.XPSource = total, xpSourceExpTotal
	} else if catalog != nil {
		if total, ok := catalog.CumulativeXP(s.Level, s.ExpInLevel); ok {
			s.CumulativeXP, s.XPSource = total, xpSourceTable
		}
	}
	if catalog != nil && s.Level > 0 {
		if needed, ok := catalog.ExpNeeded(s.Level); ok {
			s.ExpNeeded = needed
			s.PendingLevels = pendingLevels(catalog, s.Level, s.ExpInLevel)
		}
	}

	x, okX := intVar(vars, "df_positionx")
	y, okY := intVar(vars, "df_positiony")
	if okX && okY {
		s.PositionX, s.PositionY, s.HasPosition = x, y, true
		s.PositionZ, _ = intVar(vars, "df_positionz")
	}

	s.TradeZone, _ = intVar(vars, "df_tradezone")
	s.InOutpost = boolVar(vars, "df_inoutpost")
	s.DangerLevel, s.HasDanger = intVar(vars, "df_dangerlevel")
	s.BlockSupportUntil = dfTimeVar(vars, "df_block_support_until")

	s.HP, _ = intVar(vars, "df_hpcurrent")
	s.HPMax, _ = intVar(vars, "df_hpmax")
	s.Cash, _ = int64Var(vars, "df_cash")
	s.BankCash, _ = int64Var(vars, "df_bankcash")
	s.Nourishment, s.HasHunger = intVar(vars, "df_hungerhp")
	s.BoostExpUntil = dfTimeVar(vars, "df_boostexpuntil")
	s.Dead = boolVar(vars, "df_dead")
	s.ServerTime = dfTimeVar(vars, "df_servertime")

	return s
}

// pendingLevels counts how many levels the banked XP already covers, by
// replaying the game's own per-level carry-over.
func pendingLevels(c *Catalog, level int, exp int64) int {
	levels := 0
	for {
		need, ok := c.ExpNeeded(level + levels)
		if !ok || need <= 0 || exp < need {
			return levels
		}
		exp -= need
		levels++
		if levels > 500 { // the cap is 415; this is a runaway guard, not a rule
			return levels
		}
	}
}

func intVar(vars map[string]string, key string) (int, bool) {
	raw, ok := vars[key]
	if !ok {
		return 0, false
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, false
	}
	return v, true
}

func int64Var(vars map[string]string, key string) (int64, bool) {
	raw, ok := vars[key]
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// boolVar treats the game's "1" as true. Anything else, including absent, is
// false - the game never sends "true".
func boolVar(vars map[string]string, key string) bool {
	return strings.TrimSpace(vars[key]) == "1"
}

// dfTimeVar decodes one of the game's timestamps, which carry a constant
// +1200000000 offset. Zero means "not set" rather than 1970, and is returned as
// the zero Time so callers can use IsZero.
func dfTimeVar(vars map[string]string, key string) time.Time {
	v, ok := int64Var(vars, key)
	if !ok || v <= 0 {
		return time.Time{}
	}
	return time.Unix(v+dfTimeOffset, 0)
}

// Store holds everything the UI renders, and is the only shared mutable state
// in df-hud. Every accessor locks, so the poller, the game watcher, the bridge
// and the GTK main loop can all touch it.
type Store struct {
	mu sync.RWMutex

	snapshot    Snapshot
	haveSnap    bool
	prevSnap    Snapshot
	havePrev    bool
	game        GameState
	catalog     *Catalog
	poller      PollerStatus
	credsAt     time.Time
	loggedXPSrc bool

	// missedTicks counts consecutive scheduled polls that produced no usable
	// sample. The XP widget colours by this.
	missedTicks int
	lastErr     string
}

func newStore(catalog *Catalog) *Store {
	return &Store{catalog: catalog}
}

// ApplyTick folds one poll outcome into the store. Returns whether a new
// snapshot landed, so the caller can skip work on a failure.
func (s *Store) ApplyTick(tick Tick) bool {
	if tick.Err != nil {
		s.mu.Lock()
		if tick.Scheduled {
			s.missedTicks++
		}
		s.lastErr = tick.Err.Error()
		s.mu.Unlock()
		return false
	}

	s.mu.Lock()
	snap := parseSnapshot(tick.Vars, tick.At, s.catalog)
	if s.haveSnap {
		s.prevSnap, s.havePrev = s.snapshot, true
	}
	s.snapshot, s.haveSnap = snap, true
	s.missedTicks = 0
	s.lastErr = ""
	logSource := !s.loggedXPSrc
	s.loggedXPSrc = true
	s.mu.Unlock()

	if logSource {
		// Which XP tier is live is worth exactly one line, because it changes
		// how the rate is computed and it is otherwise invisible.
		log.Printf("store: cumulative XP from %s", snap.XPSource)
		if snap.XPSource == xpSourceNone {
			log.Print("store: no cumulative XP source; XP/hr will stay blank " +
				"(no df_exptotal in the record and no catalog loaded)")
		}
	}
	return true
}

func (s *Store) SetGame(g GameState) {
	s.mu.Lock()
	s.game = g
	s.mu.Unlock()
}

func (s *Store) SetPollerStatus(st PollerStatus) {
	s.mu.Lock()
	s.poller = st
	s.mu.Unlock()
}

func (s *Store) SetCatalog(c *Catalog) {
	s.mu.Lock()
	s.catalog = c
	s.mu.Unlock()
}

func (s *Store) SetCredentialsAt(t time.Time) {
	s.mu.Lock()
	s.credsAt = t
	s.mu.Unlock()
}

func (s *Store) Snapshot() (Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot, s.haveSnap
}

// View is a flat, immutable picture for the widgets: plain data, no GTK types,
// no locks, no methods that fetch anything. Everything a widget needs to render
// one frame is here, already formatted where formatting is a decision rather
// than a style.
type View struct {
	Now time.Time

	HaveData bool
	DataAge  time.Duration

	// Session is the game-client clock.
	GameRunning bool
	SessionTime time.Duration

	Level         int
	ExpInLevel    int64
	ExpNeeded     int64
	PendingLevels int
	CumulativeXP  int64
	XPSource      string

	HasPosition  bool
	PositionX    int
	PositionY    int
	PositionZ    int
	TradeZone    int
	ZoneName     string
	InOutpost    bool
	OutpostName  string
	HasDanger    bool
	DangerLevel  int
	BlockSupport time.Duration

	HP, HPMax   int
	Cash        int64
	Nourishment int
	HasHunger   bool
	BoostExpIn  time.Duration
	Dead        bool

	// Status is what to show when something is wrong, empty when it is not.
	Status      string
	StatusIsFix bool // true when the user can act on it (stale credentials)
	MissedTicks int
}

// Derive builds the render view. It is a pure function of stored state plus the
// current time: no locks held by callers, no I/O, no allocation the UI has to
// manage. Time-dependent fields (clocks, countdowns) are computed here, which is
// why a 1s UI tick can re-derive without any network activity.
func (s *Store) Derive(now time.Time) *View {
	s.mu.RLock()
	defer s.mu.RUnlock()

	v := &View{
		Now:         now,
		GameRunning: s.game.Running,
		SessionTime: s.game.Elapsed(now),
		MissedTicks: s.missedTicks,
	}

	if s.haveSnap {
		snap := s.snapshot
		v.HaveData = true
		v.DataAge = now.Sub(snap.At)
		v.Level = snap.Level
		v.ExpInLevel = snap.ExpInLevel
		v.ExpNeeded = snap.ExpNeeded
		v.PendingLevels = snap.PendingLevels
		v.CumulativeXP = snap.CumulativeXP
		v.XPSource = snap.XPSource.String()
		v.HasPosition = snap.HasPosition
		v.PositionX, v.PositionY, v.PositionZ = snap.PositionX, snap.PositionY, snap.PositionZ
		v.TradeZone = snap.TradeZone
		v.ZoneName = tradeZoneName(snap.TradeZone)
		v.InOutpost = snap.InOutpost
		v.OutpostName = outpostName(snap.PositionX, snap.PositionY)
		v.HasDanger, v.DangerLevel = snap.HasDanger, snap.DangerLevel
		v.BlockSupport = until(now, snap.BlockSupportUntil)
		v.HP, v.HPMax = snap.HP, snap.HPMax
		v.Cash = snap.Cash
		v.Nourishment, v.HasHunger = snap.Nourishment, snap.HasHunger
		v.BoostExpIn = until(now, snap.BoostExpUntil)
		v.Dead = snap.Dead
	}

	switch {
	case s.poller.Stale:
		v.Status = "session expired - open any Dead Frontier page to refresh"
		v.StatusIsFix = true
	case s.credsAt.IsZero():
		v.Status = "waiting for the bridge script"
		v.StatusIsFix = true
	case s.poller.Paused && s.poller.PauseReason != "":
		v.Status = s.poller.PauseReason
	case s.poller.Failures > 0:
		v.Status = "server not responding (retrying)"
	case !s.haveSnap:
		v.Status = "waiting for the first poll"
	}
	return v
}

// until is a countdown that never goes negative and is zero for an unset time.
func until(now, t time.Time) time.Duration {
	if t.IsZero() {
		return 0
	}
	if d := t.Sub(now); d > 0 {
		return d
	}
	return 0
}
