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

	// ExpSinceStart is df_exptotal minus df_expstart, which appears to be XP
	// earned since the current trip into the city began (observed ~2M below
	// exptotal while out in the city at level 415).
	//
	// Parsed but NOT rendered by any widget yet: the reset point is unconfirmed
	// and needs one observation across an outpost-to-city transition. A number
	// labelled "this run" that actually means something else is worse than no
	// number. See knowledge/player-record-and-signing.md.
	ExpSinceStart    int64
	HasExpSinceStart bool

	PositionX, PositionY, PositionZ int
	HasPosition                     bool

	TradeZone   int
	InOutpost   bool
	DangerLevel int
	HasDanger   bool
	// BlockSupport is when the current block's support expires.
	BlockSupport dfDeadline

	HP, HPMax   int
	Cash        int64
	HasCash     bool
	BankCash    int64
	Nourishment int
	HasHunger   bool

	// BoostExp is when an XP boost ends, which can legitimately be "never".
	// The XP widget resets its window on a change here, since the rate either
	// side of a boost is not comparable.
	BoostExp dfDeadline

	// Dead is the server's own flag, not an inference from HP.
	Dead bool

	ServerTime time.Time
}

// The game uses TWO different time encodings, and mixing them up is worth 38
// years of error, so they get separate decoders.
//
//  1. A compact epoch, unix minus 1.2e9, used by df_servertime and
//     df_hungertime. Observed values are ~585,000,000 while unix time is
//     ~1,786,000,000.
//
//  2. Plain unix seconds, used by the expiry fields (df_boostexpuntil and
//     friends). This is settled by the game's own arithmetic, as reproduced in
//     the bridge userscript (silverscripts.js:2346):
//
//     durationLeft = df_boostexpuntil - (df_servertime + 1200000000)
//
//     The right-hand side is unix, so the left-hand side must be too.
//
// This was caught by live data: applying the offset to an expiry field produced
// a boost that expired in 49 years.
const dfTimeOffset = 1_200_000_000

// dfForever is the game's "does not expire" sentinel: int32 max, or one less.
// Captured values are literally 2147483647 for a permanent boost. As a unix
// timestamp that is 2038-01-19, the classic 32-bit end of time. the bridge userscript
// treats anything more than 600000 seconds out as infinite for the same reason.
const dfForever = int64(1)<<31 - 8

// dfPlausibleWindow bounds what a real deadline can be. Anything beyond it is
// treated as not-a-deadline rather than rendered.
//
// This exists because df_block_support_until was zero in every capture, so its
// encoding is unverified. If it turns out to use the compact epoch, this guard
// makes the widget omit the line instead of confidently displaying a countdown
// that is decades wrong.
const dfPlausibleWindow = 365 * 24 * time.Hour

// dfDeadline is one of the game's expiry timestamps, with "never" as a state
// rather than as a very large number.
type dfDeadline struct {
	At      time.Time
	Forever bool
}

func (d dfDeadline) Set() bool { return d.Forever || !d.At.IsZero() }

// Remaining is the countdown, zero once expired and zero for Forever - callers
// check Forever and render an infinity mark instead of a number.
func (d dfDeadline) Remaining(now time.Time) time.Duration {
	if d.Forever || d.At.IsZero() {
		return 0
	}
	if remaining := d.At.Sub(now); remaining > 0 {
		return remaining
	}
	return 0
}

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
	s.BlockSupport = dfDeadlineVar(vars, "df_block_support_until", at)

	if start, ok := int64Var(vars, "df_expstart"); ok && s.XPSource == xpSourceExpTotal && s.CumulativeXP >= start {
		s.ExpSinceStart, s.HasExpSinceStart = s.CumulativeXP-start, true
	}

	s.HP, _ = intVar(vars, "df_hpcurrent")
	s.HPMax, _ = intVar(vars, "df_hpmax")
	// Cash is genuinely zero when the bank holds it all, which is why the
	// presence flag matters here as much as it does for the danger level.
	s.Cash, s.HasCash = int64Var(vars, "df_cash")
	s.BankCash, _ = int64Var(vars, "df_bankcash")
	s.Nourishment, s.HasHunger = intVar(vars, "df_hungerhp")
	s.BoostExp = dfDeadlineVar(vars, "df_boostexpuntil", at)
	s.Dead = boolVar(vars, "df_dead")
	s.ServerTime = dfCompactTimeVar(vars, "df_servertime")

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

// dfCompactTimeVar decodes the compact epoch: df_servertime and df_hungertime,
// which are unix minus 1.2e9. Zero means unset rather than 1970.
func dfCompactTimeVar(vars map[string]string, key string) time.Time {
	v, ok := int64Var(vars, key)
	if !ok || v <= 0 {
		return time.Time{}
	}
	return time.Unix(v+dfTimeOffset, 0)
}

// dfDeadlineVar decodes an expiry field: plain unix seconds, with the int32
// sentinel meaning "never expires" and anything implausible treated as unset.
//
// The plausibility check is the important part. It is not defensive
// programming for its own sake: df_block_support_until was zero in every
// capture, so its encoding is unverified, and if it turns out to be the compact
// epoch this makes the HUD omit the line rather than display a countdown that is
// decades wrong. A wrong number presented confidently is worse than no number.
func dfDeadlineVar(vars map[string]string, key string, now time.Time) dfDeadline {
	v, ok := int64Var(vars, key)
	if !ok || v <= 0 {
		return dfDeadline{}
	}
	if v >= dfForever {
		return dfDeadline{Forever: true}
	}
	// Measured against the observation time, not the wall clock: a replayed
	// timeline is read back long after it was recorded, and comparing to "now"
	// would reject every deadline in it.
	at := time.Unix(v, 0)
	if skew := now.Sub(at); skew > dfPlausibleWindow || skew < -dfPlausibleWindow {
		return dfDeadline{}
	}
	return dfDeadline{At: at}
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

	// xpSamples supplies the rate window. A function rather than a field because
	// the window is owned by the persistent state store, and copying it into here
	// on every poll would be two sources of truth for the same ring.
	xpSamples  func() []XPSample
	xpMinSamps int

	// missedTicks counts consecutive scheduled polls that produced no usable
	// sample. The XP widget colours by this.
	missedTicks int
	// pendingPenalty and shownPenalty make one hiccup visible for TWO ticks.
	// Without them a single missed poll flashes amber for a fraction of a second
	// and is gone before you look up from the game, which defeats the point of
	// having a stability signal at all: the first success after a miss carries
	// the penalty forward, and the second clears it.
	pendingPenalty int
	shownPenalty   int
	lastErr        string
}

func newStore(catalog *Catalog) *Store {
	return &Store{catalog: catalog, xpMinSamps: 3}
}

// SetXPWindow wires the rate window in. Without it the XP fields stay blank,
// which is what the headless -once path wants.
func (s *Store) SetXPWindow(samples func() []XPSample, minSamples int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.xpSamples, s.xpMinSamps = samples, minSamples
}

// ApplyTick folds one poll outcome into the store. Returns whether a new
// snapshot landed, so the caller can skip work on a failure.
func (s *Store) ApplyTick(tick Tick) bool {
	if tick.Err != nil {
		s.mu.Lock()
		if tick.Scheduled {
			s.missedTicks++
			if s.missedTicks > s.pendingPenalty {
				s.pendingPenalty = s.missedTicks
			}
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
	s.shownPenalty, s.pendingPenalty = s.pendingPenalty, 0
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

// Stability is how much to trust the current rate. It counts both misses
// happening now and the one carried over from the previous tick.
func (s *Store) Stability() xpStability {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stabilityLocked()
}

// stabilityLocked assumes the caller holds the lock, so Derive can use it without
// re-locking (RWMutex is not reentrant, and a nested RLock would deadlock the
// moment a writer is queued).
func (s *Store) stabilityLocked() xpStability {
	misses := s.missedTicks
	if s.shownPenalty > misses {
		misses = s.shownPenalty
	}
	switch {
	case misses == 0:
		return xpSteady
	case misses == 1:
		return xpShaky
	default:
		return xpUnstable
	}
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

// PreviousSnapshot is the one before the latest, for change detection that needs
// both sides (an XP boost starting, a death).
func (s *Store) PreviousSnapshot() (Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.prevSnap, s.havePrev
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

	HP, HPMax       int
	Cash            int64
	Nourishment     int
	HasHunger       bool
	BoostExpIn      time.Duration
	BoostExpForever bool
	Dead            bool

	// XPRate is blank when there is not enough to say; XPWhy explains why.
	XPPerHour   float64
	XPAvailable bool
	XPWhy       string
	XPSpan      time.Duration
	XPSamples   int
	XPStability xpStability

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
		v.BlockSupport = snap.BlockSupport.Remaining(now)
		v.HP, v.HPMax = snap.HP, snap.HPMax
		v.Cash = snap.Cash
		v.Nourishment, v.HasHunger = snap.Nourishment, snap.HasHunger
		v.BoostExpIn = snap.BoostExp.Remaining(now)
		v.BoostExpForever = snap.BoostExp.Forever
		v.Dead = snap.Dead
	}

	if s.xpSamples != nil {
		rate := computeXPRate(s.xpSamples(), s.xpMinSamps, s.stabilityLocked())
		v.XPAvailable = rate.Available
		v.XPPerHour = rate.PerHour
		v.XPWhy = rate.Why
		v.XPSpan = rate.Span
		v.XPSamples = rate.Samples
		v.XPStability = rate.Stability
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
