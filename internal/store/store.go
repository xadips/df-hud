package store

import (
	"crypto/sha256"
	"df-hud/internal/bossmap"
	"df-hud/internal/catalog"
	"df-hud/internal/citymap"
	"df-hud/internal/model"
	"df-hud/internal/xp"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The store is the single place widgets read from. The poller writes snapshots
// in, the UI derives a View out, and nothing else talks to either side - so
// widgets never issue requests, parse the wire format, or see a map[string]string.

type Catalog = catalog.Catalog
type Snapshot = model.Snapshot
type Deadline = model.Deadline
type GameState = model.GameState
type PresenceState = model.PresenceState
type Challenge = model.Challenge
type Tick = model.Tick
type PollerStatus = model.PollerStatus
type XPSample = model.XPSample
type RunState = model.RunState
type BossMap = bossmap.BossMap
type View = model.View

const (
	dfTimeOffset      = 1_200_000_000
	dfForever         = int64(1)<<31 - 8
	dfPlausibleWindow = 365 * 24 * time.Hour
	xpSourceNone      = model.XPSourceNone
	xpSourceExpTotal  = model.XPSourceExpTotal
	xpSourceTable     = model.XPSourceTable
)

// parseSnapshot turns a get_values response into a Snapshot. It never fails: a
// missing or unparseable field is simply absent, because a HUD that renders four
// of five widgets beats one that renders none over a single unexpected value.
func ParseSnapshot(vars map[string]string, at time.Time, catalog *Catalog) Snapshot {
	s := Snapshot{At: at}

	s.Level, _ = intVar(vars, "df_level")
	s.ExpInLevel, _ = int64Var(vars, "df_exp")
	s.FreePoints, _ = intVar(vars, "df_freepoints")

	// Tier 1: the server's own cumulative counter, present in every captured
	// record and preferred because it needs no catalog. Tier 2 reconstructs it
	// from the XP table, which differs by a fixed historical offset but advances
	// identically - see knowledge/allstats-map-and-xp.md.
	if total, ok := int64Var(vars, "df_exptotal"); ok && total > 0 {
		s.CumulativeXP, s.XPSource = total, model.XPSourceExpTotal
	} else if catalog != nil {
		if total, ok := catalog.CumulativeXP(s.Level, s.ExpInLevel); ok {
			s.CumulativeXP, s.XPSource = total, model.XPSourceTable
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
	s.BlockSupport = deadlineVar(vars, "df_block_support_until", at)

	if start, ok := int64Var(vars, "df_expstart"); ok && s.XPSource == model.XPSourceExpTotal && s.CumulativeXP >= start {
		s.ExpSinceStart, s.HasExpSinceStart = s.CumulativeXP-start, true
	}

	s.HP, _ = intVar(vars, "df_hpcurrent")
	s.HPMax, _ = intVar(vars, "df_hpmax")
	// Cash is genuinely zero when the bank holds it all, which is why the presence
	// flag matters here as much as it does for the danger level.
	s.Cash, s.HasCash = int64Var(vars, "df_cash")
	s.BankCash, _ = int64Var(vars, "df_bankcash")
	s.Nourishment, s.HasHunger = intVar(vars, "df_hungerhp")
	s.BoostExp = deadlineVar(vars, "df_boostexpuntil", at)
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

// deadlineVar decodes an expiry field: plain unix seconds, with the int32
// sentinel meaning "never expires" and anything implausible treated as unset.
// See dfPlausibleWindow for why that check is not defensive programming for its
// own sake.
func deadlineVar(vars map[string]string, key string, now time.Time) model.Deadline {
	v, ok := int64Var(vars, key)
	if !ok || v <= 0 {
		return model.Deadline{}
	}
	if v >= dfForever {
		return model.Deadline{Forever: true}
	}
	// Measured against the observation time, not the wall clock: a replayed
	// timeline is read back long after it was recorded, and comparing to "now"
	// would reject every deadline in it.
	at := time.Unix(v, 0)
	if skew := now.Sub(at); skew > dfPlausibleWindow || skew < -dfPlausibleWindow {
		return model.Deadline{}
	}
	return model.Deadline{At: at}
}

// Store holds everything the UI renders, and is the only shared mutable state in
// df-hud. Every accessor locks, so the poller, the game watcher, the bridge and
// the GTK main loop can all touch it.
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
	runStart    time.Time
	runSeed     *RunState
	runTerminal bool
	onRunChange func()

	// presence is where the game CLIENT says you are, which beats the server's
	// own record - see presence.go for the measurement. Kept beside the snapshot
	// rather than folded into it because the two arrive independently and only
	// this one is allowed to answer "where am I".
	presence          PresenceState
	havePresence      bool
	presenceConnected bool

	bossMap     *BossMap
	board       []Challenge
	haveBoard   bool
	boardAt     time.Time
	boardStatus string

	// xpSamples supplies the rate window. A function rather than a field because
	// the window is owned by the persistent state store, and copying it in on
	// every poll would be two sources of truth for the same ring.
	xpSamples  func() []XPSample
	xpMinSamps int

	// missedTicks counts consecutive scheduled polls that produced no usable
	// sample. The XP widget colours by this.
	missedTicks int
	// pendingPenalty and shownPenalty make one hiccup visible for TWO ticks.
	// Without them a single missed poll flashes amber for a fraction of a second
	// and is gone before you look up from the game: the first success after a miss
	// carries the penalty forward, and the second clears it.
	pendingPenalty int
	shownPenalty   int
	lastErr        string
}

func New(catalog *Catalog) *Store {
	return &Store{catalog: catalog, xpMinSamps: 3}
}

// SetOnRunChange registers a callback for persistence and other consumers that
// need the run boundary, rather than making each input path remember to sync it.
// It is always called after the store lock is released.
func (s *Store) SetOnRunChange(fn func()) {
	s.mu.Lock()
	s.onRunChange = fn
	s.mu.Unlock()
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
	snap := ParseSnapshot(tick.Vars, tick.At, s.catalog)
	if s.haveSnap {
		s.prevSnap, s.havePrev = s.snapshot, true
	}
	s.snapshot, s.haveSnap = snap, true
	runChanged := s.updateRunLocked(snap)
	s.missedTicks = 0
	s.shownPenalty, s.pendingPenalty = s.pendingPenalty, 0
	s.lastErr = ""
	logSource := !s.loggedXPSrc
	s.loggedXPSrc = true
	onRunChange := s.onRunChange
	s.mu.Unlock()

	if runChanged && onRunChange != nil {
		onRunChange()
	}
	if logSource {
		// Worth exactly one line: it changes how the rate is computed and is
		// otherwise invisible.
		log.Printf("store: cumulative XP from %s", snap.XPSource)
		if snap.XPSource == model.XPSourceNone {
			log.Print("store: no cumulative XP source; XP/hr will stay blank " +
				"(no df_exptotal in the record and no catalog loaded)")
		}
	}
	return true
}

// updateRunLocked maintains the run clock: how long you have been playing.
//
// NOT the client's uptime. Launching means a launcher, a Launch button, a loading
// screen and then a Start button, so process uptime can be minutes ahead of any
// playing.
//
// Three signals start it, in the order they are trusted:
//
//  1. LEAVING AN OUTPOST: df_inoutpost going from 1 to 0. The EDGE, not the
//     value, and that distinction is the whole thing - an earlier version started
//     the clock whenever the field read 0, which is why it began at the launcher:
//     the field was already 0 there, so there was no edge to fire on.
//  2. THE POSITION CHANGING.
//
// Position does not work as a primary signal, which is worth writing down because
// it looks convincing: a whole loot run can happen inside one block. It covers
// df-hud not watching at the moment of the edge - started mid-run, or a previous
// session that ended out in the city.
//
// None of the three can happen while a launcher sits on screen: nothing on the
// server moves your character or awards XP while you look at a Launch button.
//
// Two rejected alternatives, both tried: the process start time (measurably
// wrong - it started the clock when the launcher appeared), and df_inoutpost
// alone (already "0" at the launcher, so it does not mean what the name suggests;
// kept below as an END condition only, where being wrong costs a clock that stops
// early rather than one that lies).
func (s *Store) updateRunLocked(snap Snapshot) bool {
	moved := s.havePrev && s.prevSnap.HasPosition && snap.HasPosition &&
		(s.prevSnap.PositionX != snap.PositionX ||
			s.prevSnap.PositionY != snap.PositionY ||
			s.prevSnap.PositionZ != snap.PositionZ)
	// The EDGE, not the value. The value is what started the clock at the launcher.
	leftOutpost := s.havePrev && s.prevSnap.InOutpost && !snap.InOutpost
	runChanged := false

	switch {
	case snap.Dead:
		// Dying ends a run as surely as extracting does, and df_dead is the
		// server's own flag rather than an inference from HP. Presence does not
		// publish death. Death returns to the website and closes this game client,
		// so no late activity refresh may start another run in the same process.
		runChanged = s.endRunLocked(snap.At, "you died")
		s.runTerminal = true
	case s.runTerminal:
		// Death is terminal for this client process. A new run needs a newly
		// detected process, not another late frame from this one.
	case s.presenceConnected && s.havePresence && s.presence.Loading:
		// Do not let a lagging poll start the session while the client explicitly
		// says it is still loading.
	case s.runStart.IsZero() && (leftOutpost || moved):
		// Timed from the observation that proves it, not the one before: never
		// claim to have been playing for longer than there is evidence for.
		s.runStart = snap.At
		runChanged = true
		var why string
		switch {
		case leftOutpost:
			why = "left the outpost"
		default:
			why = fmt.Sprintf("moved to %d, %d", snap.PositionX, snap.PositionY)
		}
		log.Printf("session: run started (%s)", why)
	}

	// Evidence for the two unconfirmed fields, gathered while the game is played
	// rather than guessed at from one snapshot. Both would give an exact run
	// boundary if their meaning were settled; neither is acted on until it is.
	// See knowledge/player-record-and-signing.md.
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
		// moment is pressing Launch or pressing Start.
		log.Printf("session: outpost=%v tradezone=%d position=%d,%d (was outpost=%v tradezone=%d)",
			snap.InOutpost, snap.TradeZone, snap.PositionX, snap.PositionY,
			s.prevSnap.InOutpost, s.prevSnap.TradeZone)
	}
	return runChanged
}

// forgetPrevLocked drops the previous snapshot, so no run-start signal compares
// across a game restart - a position from the last session against this one is a
// movement nobody made.
func (s *Store) forgetPrevLocked() {
	s.prevSnap, s.havePrev = Snapshot{}, false
	// The next ApplyTick promotes the current snapshot to "previous" before
	// comparing. Keeping it here would still compare the old process with the
	// first record from the new one, despite clearing prevSnap above.
	s.snapshot, s.haveSnap = Snapshot{}, false
}

// endRunLocked discards the run clock and says how long it had been going.
//
// EVERY path that clears the clock goes through here. Three did not before -
// closing the game, relaunching it, and the game not being detected any more - so
// the commonest way a run ends, quitting, was the one that left no record at all.
// The journal is where you look for yesterday's numbers, so this line has to be
// there:
//
//	session: run ended after 23m41s (the game closed)
//
// Guarded on the clock being set, so the repeated "not running" states a scanner
// produces log once, at the transition, rather than every two seconds.
func (s *Store) endRunLocked(at time.Time, why string) bool {
	if s.runStart.IsZero() {
		return false
	}
	if elapsed := at.Sub(s.runStart); elapsed > 0 {
		log.Printf("session: run ended after %s (%s)", elapsed.Round(time.Second), why)
	}
	s.runStart = time.Time{}
	return true
}

// RestartRun starts the clock from now, and why is the caller's to say.
//
// None of the automatic signals is certain - the record does not mark the client
// taking control at all - so the clock can only start from evidence of activity,
// which for a loot run inside one block can arrive a long way in. A one-click
// correction beats a number you cannot trust and cannot fix.
//
// why is a parameter because the three callers are not the same event.
func (s *Store) RestartRun(at time.Time, why string) bool {
	s.mu.Lock()
	if !s.game.Running || s.runTerminal {
		s.mu.Unlock()
		return false
	}
	s.runStart, s.runSeed = at, nil
	onRunChange := s.onRunChange
	s.mu.Unlock()
	log.Printf("session: run clock started from %s", why)
	if onRunChange != nil {
		onRunChange()
	}
	return true
}

// StartRunIfIdle starts an unconfirmed run without replacing one already
// observed, restored or started by hand. Platform wiring uses this only when it
// has evidence unavailable to the player record itself (on Windows: a real,
// foreground game window rather than the launcher).
func (s *Store) StartRunIfIdle(at time.Time, why string) bool {
	s.mu.Lock()
	presenceBlocks := s.presenceConnected && s.havePresence && s.presence.Loading
	if !s.game.Running || s.runTerminal || presenceBlocks ||
		!s.runStart.IsZero() || s.runSeed != nil {
		s.mu.Unlock()
		return false
	}
	s.runStart = at
	onRunChange := s.onRunChange
	s.mu.Unlock()
	log.Printf("session: run started (%s)", why)
	if onRunChange != nil {
		onRunChange()
	}
	return true
}

// SetRunSeed offers a persisted run to restore, so restarting df-hud mid-run
// keeps the clock. Applied only once the game watcher confirms the same process
// is still running.
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
func (s *Store) Stability() model.XPStability {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stabilityLocked()
}

// stabilityLocked assumes the caller holds the lock, so Derive can use it without
// re-locking (RWMutex is not reentrant, and a nested RLock would deadlock the
// moment a writer is queued).
func (s *Store) stabilityLocked() model.XPStability {
	misses := s.missedTicks
	if s.shownPenalty > misses {
		misses = s.shownPenalty
	}
	switch {
	case misses == 0:
		return model.XPSteady
	case misses == 1:
		return model.XPShaky
	default:
		return model.XPUnstable
	}
}

// SetChallenges replaces the board.
func (s *Store) SetChallenges(board []Challenge, at time.Time) {
	s.mu.Lock()
	s.board, s.haveBoard, s.boardAt = board, true, at
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

// SetPresence records what the game client last published about where you are.
//
// Only accepted while the game is running. The client's last word survives its
// own death - nothing retracts a presence when a process exits - so without this
// a closed game would leave the HUD certain of a position forever.
func (s *Store) SetPresenceConnected(connected bool) {
	s.mu.Lock()
	s.presenceConnected = connected
	s.mu.Unlock()
}

func (s *Store) SetPresence(p PresenceState) {
	s.mu.Lock()
	if !s.game.Running || (!s.game.StartedAt.IsZero() && p.At.Before(s.game.StartedAt)) {
		s.mu.Unlock()
		return
	}
	// A state can only arrive over a live connection. Recording that fact here
	// also keeps direct Store users and tests independent of transport callbacks.
	s.presenceConnected = true
	s.presence, s.havePresence = p, true
	runChanged := s.updateRunFromPresenceLocked(p)
	onRunChange := s.onRunChange
	s.mu.Unlock()
	if runChanged && onRunChange != nil {
		onRunChange()
	}
}

// updateRunFromPresenceLocked starts and ends the run clock from what the client
// says outright, rather than inferring it from the poll.
//
// Loading is ignored rather than treated as an end: zoning happens mid-run, and
// ending there would restart the clock at every doorway.
func (s *Store) updateRunFromPresenceLocked(p PresenceState) bool {
	if s.runSeed != nil || s.runTerminal {
		// A run persisted by a previous df-hud is still waiting to be restored by
		// SetGame. Starting a fresh one here would throw away a clock that is
		// already an hour old.
		return false
	}
	// Named outposts such as "Secronom Bunker" are locations published by the
	// running game client, not evidence that the player returned to the website.
	// The website transition is observed when this process closes.
	if (p.HasPosition || p.InOutpost) && s.runStart.IsZero() {
		s.runStart = p.At
		log.Printf("session: run started (the client reported %s)", presenceKindForRun(p))
		return true
	}
	return false
}

func presenceKindForRun(p PresenceState) string {
	if p.HasPosition {
		return fmt.Sprintf("%d, %d", p.X, p.Y)
	}
	return p.OutpostName
}

// presencePositionLocked is the client's position when it is usable: it exists,
// it is not the "Loading..." placeholder, and it is not older than the poll that
// would otherwise answer.
//
// The staleness check is what makes this safe to prefer. The client publishes at
// most every ~5s and only on a change, so standing still leaves the last frame
// arbitrarily old - fine while it is still true, wrong the moment the game
// closes or the socket drops. Falling back to the poll then costs freshness and
// keeps correctness, which is the right way round.
func (s *Store) presencePositionLocked(now time.Time) (PresenceState, bool) {
	if !s.havePresence || !s.game.Running {
		return PresenceState{}, false
	}
	if now.Sub(s.presence.At) > presenceMaxAge {
		return PresenceState{}, false
	}
	return s.presence, true
}

// ClientInWorld reports whether the game is past its loading screen.
//
// True whenever the client has not said otherwise - no feed, a stale one, or one
// that has not spoken yet - so a machine with no bridge can still send keys.
func (s *Store) ClientInWorld(now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.presencePositionLocked(now)
	if !ok {
		return true
	}
	return !p.Loading
}

// presenceMaxAge is how long the client's last word is trusted after it stops
// talking. Generous, because silence is the NORMAL case - the client only
// publishes on a change, so standing on one block for a minute is a minute of
// silence about a position that is still perfectly true. It only has to be short
// enough that a dead socket does not leave a stale block on screen indefinitely.
const presenceMaxAge = 2 * time.Minute

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

// SetGame records the game process, and keeps the run clock honest about it: a
// closed or relaunched game cannot be the same run.
func (s *Store) SetGame(g GameState) {
	s.mu.Lock()
	prev := s.game
	s.game = g
	sessionChanged := (g.Running || prev.Running) && !g.SameSession(prev)
	if sessionChanged {
		// Presence authority belongs to one game process. A new process gets poll
		// fallback until its own Discord SDK publishes a recognized state.
		s.presence, s.havePresence = PresenceState{}, false
	}
	if g.Running && !g.SameSession(prev) {
		s.runTerminal = false
	}
	runChanged := false
	switch {
	case !g.Running:
		// The commonest end to a run: you quit. Timed to now rather than to the
		// last poll, because the run continued until the client went away and the
		// last poll can be a whole interval behind that.
		runChanged = s.endRunLocked(time.Now(), "the game closed")
		s.runTerminal = false
		s.forgetPrevLocked()
	case prev.Running && !g.SameSession(prev):
		runChanged = s.endRunLocked(time.Now(), "the game relaunched")
		s.forgetPrevLocked()
	case s.runSeed != nil:
		// A run persisted by a previous df-hud process, restored only if it
		// belongs to the game running now. The start time is compared as well as
		// the PID, because PIDs are recycled.
		if s.runSeed.Matches(g) {
			s.runStart = s.runSeed.StartedAt
			runChanged = true
			log.Printf("session: resuming the run started %s ago",
				time.Since(s.runSeed.StartedAt).Round(time.Second))
		}
		s.runSeed = nil
	}
	onRunChange := s.onRunChange
	s.mu.Unlock()
	if runChanged && onRunChange != nil {
		onRunChange()
	}
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

// EffectivePosition is the block currently trusted for location-sensitive work
// outside Derive, such as selecting the Onslaught polling cadence.
func (s *Store) EffectivePosition(now time.Time) (x, y int, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, fresh := s.presencePositionLocked(now); fresh {
		switch {
		case p.HasPosition:
			return p.X, p.Y, true
		case p.InOutpost, p.Loading:
			return 0, 0, false
		}
	}
	if !s.haveSnap || !s.snapshot.HasPosition {
		return 0, 0, false
	}
	return s.snapshot.PositionX, s.snapshot.PositionY, true
}

// Derive builds the render view: a pure function of stored state plus the current
// time, with no I/O. Time-dependent fields are computed here, which is why a 1s
// UI tick can re-derive without any network activity.
func (s *Store) Derive(now time.Time) *View {
	s.mu.RLock()
	defer s.mu.RUnlock()

	v := &View{
		Now:           now,
		GameRunning:   s.game.Running,
		ClientLoading: s.game.Running && s.presenceConnected && !s.havePresence,
		ClientUptime:  s.game.Elapsed(now),
		MissedTicks:   s.missedTicks,
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
		v.ZoneName = citymap.TradeZoneName(snap.TradeZone)
		v.InOutpost = snap.InOutpost
		v.OutpostName = citymap.OutpostName(snap.PositionX, snap.PositionY)
		v.HasDanger, v.DangerLevel = snap.HasDanger, snap.DangerLevel
		v.BlockSupport = snap.BlockSupport.Remaining(now)
		v.HP, v.HPMax = snap.HP, snap.HPMax
		v.Cash = snap.Cash
		v.Nourishment, v.HasHunger = snap.Nourishment, snap.HasHunger
		v.BoostExpIn = snap.BoostExp.Remaining(now)
		v.BoostExpForever = snap.BoostExp.Forever
		v.Dead = snap.Dead
	}

	// Where you are, from the client rather than the server. AFTER the snapshot so
	// it wins, and BEFORE the event map below so the block's events, the nearest
	// walk and the map's own ring are all answering about the same block.
	//
	// Only position and outpost state are taken. Everything else the client
	// publishes is either absent from the presence or better from the poll.
	if p, ok := s.presencePositionLocked(now); ok {
		v.ClientLoading = p.Loading
		switch {
		case p.HasPosition:
			v.HasPosition = true
			v.PositionX, v.PositionY = p.X, p.Y
			v.PositionSource = "presence"
			// Out on the map by definition, which is fresher than df_inoutpost -
			// a field the poll gets wrong for a minute at a time.
			v.InOutpost, v.OutpostName = false, ""
		case p.InOutpost:
			// An outpost is published by NAME, with no coordinates, so the
			// snapshot's position stands - it is right whenever you are standing
			// still, which in an outpost you are.
			v.InOutpost, v.OutpostName = true, p.OutpostName
			v.PositionSource = "presence"
		}
	}

	if s.bossMap != nil {
		v.HasBossMap = true
		v.BossMapAge = s.bossMap.Age(now)
		v.OutpostAttack = s.bossMap.OutpostAttack
		if v.HasPosition {
			v.BlockEvents = s.bossMap.At(v.PositionX, v.PositionY, now)
			if v.PositionX == bossmap.OnslaughtCoord && v.PositionY == bossmap.OnslaughtCoord {
				// Onslaught only: its five-minute cycles overlap, so last cycle's
				// boss is often still in front of you and the next is already in
				// the feed. Out in the city the cycle is an hour, where the
				// previous boss has gone and the next is planning information.
				v.BlockEventsPast = s.bossMap.AtEnded(v.PositionX, v.PositionY, now)
				v.BlockEventsUpcoming = s.bossMap.AtUpcoming(v.PositionX, v.PositionY, now)
				if boundary := s.bossMap.BlockBoundary(v.PositionX, v.PositionY, now); !boundary.IsZero() {
					v.HasOnslaughtCountdown = true
					v.OnslaughtCountdown = boundary.Sub(now)
				}
			}
		}
		// Every active event, for the map group - and the source the nearest one is
		// picked from, so a marker on the map and the row on the HUD cannot
		// disagree. One breadth-first search, shared, which is what makes distances
		// to a dozen bosses cost the same as a distance to one.
		var from [2]int
		var dist []int32
		if v.HasPosition && citymap.Default().IsBlock(v.PositionX, v.PositionY) {
			from = [2]int{v.PositionX, v.PositionY}
			dist = citymap.Default().WalkDistances(v.PositionX, v.PositionY)
		}
		v.CityMarks = s.bossMap.ActiveMarks(now, from, dist)

		// Only when your own block is empty: with something in front of you, the
		// nearest OTHER thing is a distraction.
		if v.HasPosition && len(v.BlockEvents) == 0 && !v.ClientLoading {
			if m, ok := bossmap.NearestMark(v.CityMarks); ok {
				v.HasNearest = true
				v.NearestLabel = m.Label
				v.NearestDX, v.NearestDY = m.Walk.DX, m.Walk.DY
				v.NearestX, v.NearestY = m.X, m.Y
				v.NearestDistanceInBlocks = m.Walk.Blocks
				v.NearestDetour = m.Walk.Detour
			}
		}
	}

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
		rate := xp.ComputeRate(s.xpSamples(), s.xpMinSamps, s.stabilityLocked())
		v.XPAvailable = rate.Available
		v.XPProvisional = rate.Provisional
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
