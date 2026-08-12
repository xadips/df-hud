package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

	// Session3D is a FINGERPRINT of df_session3d, never the value.
	//
	// That field is the last untried candidate for "the client just took control",
	// which is the one thing no other field in the record marks: position, zone and
	// df_inoutpost all persist unchanged across a client exit and relaunch, as
	// observed live. If it turns out to change when the 3D client connects, it is
	// exactly the run-start signal.
	//
	// Hashed because a field with "session" in its name gets the benefit of the
	// doubt: a fingerprint answers "did it change" without the value ever reaching
	// a log, a state file or a crash dump.
	Session3D string

	// GoldMember doubles challenge XP rewards (challenge.js:279-283), so it is
	// needed to render a reward honestly rather than at half value.
	GoldMember bool

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
	s.GoldMember = boolVar(vars, "df_goldmember")
	s.Dead = boolVar(vars, "df_dead")
	s.ServerTime = dfCompactTimeVar(vars, "df_servertime")
	s.Session3D = fingerprint(vars["df_session3d"])

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

// fingerprint is a short, one-way digest, for comparing a value we deliberately
// refuse to keep.
func fingerprint(v string) string {
	if v == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:4])
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

	// runStart is when the current trip into the inner city began, zero when
	// there is no run. runSeed is a persisted run restored at startup, consumed
	// once the game it belongs to is confirmed to be the one running.
	runStart time.Time
	runSeed  *RunState

	bossMap     *BossMap
	board       []Challenge
	pinned      []Challenge
	haveBoard   bool
	boardAt     time.Time
	boardStatus string

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
	s.updateRunLocked(snap)
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

// updateRunLocked maintains the run clock: how long you have been playing.
//
// It is not the game client's uptime. Launching Dead Frontier means a launcher, a
// Launch button, a loading screen and then a Start button, so process uptime can
// be minutes ahead of any playing, and a clock counting that is timing a loading
// screen.
//
// Three signals start it, in the order they are trusted:
//
//  1. LEAVING AN OUTPOST: df_inoutpost going from 1 to 0. This is the EDGE, not
//     the value, and that distinction is the whole thing. An earlier version
//     started the clock whenever the field read 0, which is why it began at the
//     launcher: the field was already 0 there and stayed 0, so there was no edge
//     to fire on. The edge means the server has just taken you out of an outpost,
//     which is the only one of the three that fires AT the start of a run rather
//     than at the first sign of activity within it.
//  2. THE POSITION CHANGING, and
//  3. CUMULATIVE XP GOING UP.
//
// Neither 2 nor 3 is any good as a primary signal, which is worth writing down
// because both look convincing: a whole loot run can happen inside one block, and
// killing is not what you do first when you arrive. They are the fallback for when
// df-hud was not watching at the moment of the edge - started mid-run, or a
// previous session that ended out in the city, leaving the record already at 0.
//
// None of the three can happen while a launcher sits on screen: nothing on the
// server moves your character, awards you XP, or takes you out of an outpost while
// you are looking at a Launch button.
//
// Two rejected alternatives, both tried:
//
//   - the process start time. Measurably wrong: reported live as starting the
//     clock when the launcher appeared.
//   - df_inoutpost alone. Also wrong, and more interestingly so: it was already
//     "0" at the launcher, before Start was pressed. So it does not mean "your
//     character is docked at an outpost" in the way the name suggests. It is kept
//     below as an END condition only, where being wrong costs a clock that stops
//     early rather than one that lies about how long you have played.
//
// The cost of using movement is that the clock starts on the first step rather
// than at the Start press, and that df-hud must be watching when it happens (a
// mid-run start is covered by the persisted run instead). Both are cheap next to
// a clock that is confidently wrong.
func (s *Store) updateRunLocked(snap Snapshot) {
	moved := s.havePrev && s.prevSnap.HasPosition && snap.HasPosition &&
		(s.prevSnap.PositionX != snap.PositionX ||
			s.prevSnap.PositionY != snap.PositionY ||
			s.prevSnap.PositionZ != snap.PositionZ)
	// Compared only within the same XP tier: the two differ by a large constant,
	// so a tier change would look like earning hundreds of thousands of XP.
	earned := s.havePrev && snap.XPSource == s.prevSnap.XPSource &&
		snap.CumulativeXP > s.prevSnap.CumulativeXP
	// The EDGE, not the value. The value is what started the clock at the launcher.
	leftOutpost := s.havePrev && s.prevSnap.InOutpost && !snap.InOutpost

	switch {
	case snap.InOutpost:
		if !s.runStart.IsZero() {
			log.Printf("session: run ended after %s (the record says outpost)",
				snap.At.Sub(s.runStart).Round(time.Second))
		}
		s.runStart = time.Time{}
	case s.runStart.IsZero() && (leftOutpost || moved || earned):
		// Timed from the observation that proves it, not from the one before it:
		// never claim to have been playing for longer than there is evidence for.
		s.runStart = snap.At
		var why string
		switch {
		case leftOutpost:
			why = "left the outpost"
		case moved:
			why = fmt.Sprintf("moved to %d, %d", snap.PositionX, snap.PositionY)
		default:
			why = "earned xp"
		}
		log.Printf("session: run started (%s)", why)
	}

	// Evidence for the two unconfirmed fields, gathered while the game is being
	// played rather than guessed at from a single snapshot. Both would give an
	// exact run boundary if their meaning were settled; neither is acted on until
	// it is. See knowledge/player-record-and-signing.md.
	if s.havePrev && s.prevSnap.HasExpSinceStart && snap.HasExpSinceStart {
		prevStart := s.prevSnap.CumulativeXP - s.prevSnap.ExpSinceStart
		nextStart := snap.CumulativeXP - snap.ExpSinceStart
		if prevStart != nextStart {
			log.Printf("session: df_expstart moved by %d while in_outpost=%v",
				nextStart-prevStart, snap.InOutpost)
		}
	}
	if s.havePrev && s.prevSnap.Session3D != snap.Session3D && s.prevSnap.Session3D != "" {
		log.Print("session: df_session3d changed (a new client session, if that is what it means)")
	}
	if s.havePrev && (s.prevSnap.InOutpost != snap.InOutpost || s.prevSnap.TradeZone != snap.TradeZone) {
		// Logged together because they are two views of the same thing and it is
		// not yet known whether they move at the same moment - or whether that
		// moment is pressing Launch or pressing Start. One launch with this in
		// place settles it.
		log.Printf("session: outpost=%v tradezone=%d position=%d,%d (was outpost=%v tradezone=%d)",
			snap.InOutpost, snap.TradeZone, snap.PositionX, snap.PositionY,
			s.prevSnap.InOutpost, s.prevSnap.TradeZone)
	}
}

// RestartRun starts the clock from now, by hand.
//
// It exists because none of the automatic signals is certain: the server's record
// does not mark the client taking control at all, so the clock can only be started
// from evidence of activity, which for a loot run inside a single block can arrive
// a long way into it. A one-click correction beats a number you cannot trust and
// cannot fix.
func (s *Store) RestartRun(at time.Time) {
	s.mu.Lock()
	s.runStart, s.runSeed = at, nil
	s.mu.Unlock()
	log.Print("session: run clock restarted by hand")
}

// SetRunSeed offers a persisted run to restore, so restarting df-hud mid-run
// keeps the clock instead of resetting it to zero. It is applied only once the
// game watcher confirms the same process is still running.
func (s *Store) SetRunSeed(run *RunState) {
	s.mu.Lock()
	s.runSeed = run
	s.mu.Unlock()
}

// Run reports the current run's start and the game process it belongs to, for
// persisting. A zero time means no run is in progress.
func (s *Store) Run() (time.Time, GameState) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.runStart, s.game
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

// SetChallenges replaces the board.
func (s *Store) SetChallenges(board []Challenge, at time.Time) {
	s.mu.Lock()
	s.board, s.haveBoard, s.boardAt = board, true, at
	s.mu.Unlock()
}

// SetPinned replaces the HUD's subset of the board.
func (s *Store) SetPinned(pinned []Challenge) {
	s.mu.Lock()
	s.pinned = pinned
	s.mu.Unlock()
}

// SetChallengeStatus records why the board is missing, so the widget can explain
// itself rather than just being absent.
func (s *Store) SetChallengeStatus(reason string) {
	s.mu.Lock()
	s.boardStatus = reason
	s.mu.Unlock()
}

// Challenges returns the board, for the console window's full list.
func (s *Store) Challenges() ([]Challenge, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.board, s.haveBoard
}

// SetGame records the game process, and keeps the run clock honest about it: a
// closed or relaunched game cannot be the same run.
// SetBossMap replaces the city event map.
func (s *Store) SetBossMap(m *BossMap) {
	s.mu.Lock()
	s.bossMap = m
	s.mu.Unlock()
}

// BossMap is the whole event map, for the console window and diagnostics.
func (s *Store) BossMap() *BossMap {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bossMap
}

func (s *Store) SetGame(g GameState) {
	s.mu.Lock()
	prev := s.game
	s.game = g
	switch {
	case !g.Running:
		s.runStart = time.Time{}
	case prev.Running && !g.SameSession(prev):
		s.runStart = time.Time{}
	case s.runSeed != nil:
		// A run persisted by a previous df-hud process, restored only if it
		// belongs to the game that is running now. Comparing the start time as
		// well as the PID matters because PIDs are recycled.
		if s.runSeed.Matches(g) {
			s.runStart = s.runSeed.StartedAt
			log.Printf("session: resuming the run started %s ago",
				time.Since(s.runSeed.StartedAt).Round(time.Second))
		}
		s.runSeed = nil
	}
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

	// SessionTime is time in the inner city on this run, valid only when
	// HasSession is set - zero is a real value one second after you arrive, so
	// it cannot double as "no run".
	GameRunning bool
	HasSession  bool
	SessionTime time.Duration
	// ClientUptime is how long the game process has been up, which is no longer
	// what the HUD shows. Kept for the tray tooltip, where "the client is up but
	// you are not in the city" is the whole explanation for an empty HUD.
	ClientUptime time.Duration

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

	// BlockEvents is what is standing on your block right now: bosses, bandits,
	// a mission, a QRF. Empty is the normal case - most blocks hold nothing.
	BlockEvents []CityEvent
	// BlockEventsPast is the previous cycle's events, filled only in Onslaught
	// where the cycles overlap.
	BlockEventsPast []CityEvent
	// OutpostAttack is map-wide rather than about your block.
	OutpostAttack bool
	// BossMapAge is how stale the event feed is, so a widget can decline to
	// claim a block is clear on the strength of an hour-old fetch.
	BossMapAge time.Duration
	HasBossMap bool

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

	// Challenges is the board, and Pinned is the subset the HUD shows.
	Challenges      []Challenge
	Pinned          []Challenge
	ChallengeStatus string
	ChallengesDone  int
	ChallengesTotal int

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
		Now:          now,
		GameRunning:  s.game.Running,
		ClientUptime: s.game.Elapsed(now),
		MissedTicks:  s.missedTicks,
	}
	if s.game.Running && !s.runStart.IsZero() {
		v.HasSession = true
		if elapsed := now.Sub(s.runStart); elapsed > 0 {
			v.SessionTime = elapsed
		}
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

	if s.bossMap != nil {
		v.HasBossMap = true
		v.BossMapAge = s.bossMap.Age(now)
		v.OutpostAttack = s.bossMap.OutpostAttack
		if v.HasPosition {
			v.BlockEvents = s.bossMap.At(v.PositionX, v.PositionY, now)
			if v.PositionX == onslaughtCoord && v.PositionY == onslaughtCoord {
				// Onslaught only. Its cycle is five minutes and the cycles overlap,
				// so last cycle's boss is often still in front of you. Out in the
				// city the cycle is an hour and the previous boss is gone.
				v.BlockEventsPast = s.bossMap.AtEnded(v.PositionX, v.PositionY, now)
			}
		}
	}

	v.Pinned = s.pinned
	if s.haveBoard {
		v.Challenges = s.board
		v.ChallengesTotal = len(s.board)
		for _, c := range s.board {
			if c.Complete() {
				v.ChallengesDone++
			}
		}
	}
	v.ChallengeStatus = s.boardStatus

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
