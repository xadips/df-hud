package poller

import (
	"context"
	"df-hud/internal/config"
	"df-hud/internal/creds"
	"df-hud/internal/dfclient"
	"df-hud/internal/game"
	"df-hud/internal/model"
	"df-hud/internal/rategate"
	"errors"
	"log"
	"math/rand/v2"
	"sync"
	"time"
)

// The poller is the ONLY thing in df-hud that talks to the game server. Every
// widget reads from the store; nothing else issues a request. That is what makes
// the traffic budget a single number instead of an emergent property.
//
// Pacing rules, in order of importance:
//
//  1. One request in flight at a time. The loop is a single goroutine, so this
//     is structural rather than enforced.
//
//  2. No request may follow the previous one by less than MinRequestGap,
//     whatever wakes the loop. Credentials arriving, the game launching and a
//     compositor event can all fire at once; without this floor a wake storm
//     becomes a request burst, which is exactly the pattern that earns a temp
//     ban.
//
//     That floor is ANTI-BURST ONLY, which is why it is a second rather than the
//     five it used to be. What keeps any single endpoint from being hammered is
//     its own interval floor - 5s for the player record, 30s for the challenge
//     board, 30s for the event feed - and those are enforced at config time. The
//     gate cannot improve on them; all it can do is stop two schedulers firing in
//     the same instant.
//
//     Five seconds was measurably wrong for a different reason: the challenge
//     board cannot be fetched until the first player record arrives (the level
//     decides which challenges apply), so the two are serialised by construction
//     and the gap landed in full on the board every time. Measured at 5.0s from
//     startup to the board appearing, on a HUD where everything else was up
//     instantly. One second is the same spacing already used for bulk calls on
//     this account, and the game's own client routinely fires several endpoints
//     closer together than that while loading a page.
//
//  3. Rejected credentials STOP the loop. They are not retried, not backed
//     off - retrying a rejected login is the worst thing we could do. Recovery
//     is a fresh bridge payload, which wakes the loop again.
//
//  4. Failures back off exponentially to poll.backoff_max.
//
//  5. Every interval carries jitter, so a restart loop cannot line requests up
//     and our traffic never looks metronomic.
//
// Cadence: active while the game is RUNNING, idle when it is not (and nothing
// at all when only_when_game_running is set, which is the default).
//
// The plan originally called for active-only-while-out-in-the-city, using
// df_inoutpost. That was dropped deliberately, for two reasons. First,
// df_inoutpost is only known from the PREVIOUS poll, so the refinement is
// always one poll stale by construction. Second, and worse, it would mean the
// outpost-to-city transition takes up to a full idle interval to notice - two
// minutes of a wrong HUD at exactly the moment a run starts, which is when the
// HUD matters most. Restocking in an outpost is short; the saving was not worth
// the lag.
const MinRequestGap = time.Second

type Client = dfclient.Client
type Credentials = creds.Credentials
type CredentialStore = creds.Store
type GameWatcher = game.GameWatcher
type Config = config.Config
type Gate = rategate.Gate
type Tick = model.Tick
type Status = model.PollerStatus

var ErrStaleCredentials = dfclient.ErrStaleCredentials

type Poller struct {
	client *Client
	creds  *CredentialStore
	game   *GameWatcher
	// cfg is a function so a config hot reload changes the cadence without
	// restarting the loop.
	cfg    func() *Config
	onTick func(Tick)

	// now is injectable so tests can drive the clock, but the loop's waiting is
	// still real time; tests use tiny intervals rather than a fake clock.
	now func() time.Time

	// minGap defaults to MinRequestGap. It is a field only so tests can run at
	// millisecond intervals without a five-second floor swallowing every
	// scheduling behaviour they are trying to observe - the burst test keeps
	// the production value, since that is the one that matters.
	minGap time.Duration

	// gate, when set, is the process-wide spacing floor shared with every other
	// scheduler. Without it the minimum gap would only ever be per-poller, which
	// is a weaker property than the one documented.
	gate *Gate

	wake chan struct{}

	mu       sync.RWMutex
	status   Status
	lastPoll time.Time
	// loggedPause avoids repeating the same "waiting for credentials" line on
	// every loop iteration.
	loggedPause string
}

func New(client *Client, creds *CredentialStore, game *GameWatcher, cfg func() *Config) *Poller {
	return &Poller{
		client: client,
		creds:  creds,
		game:   game,
		cfg:    cfg,
		now:    time.Now,
		minGap: MinRequestGap,
		wake:   make(chan struct{}, 1),
	}
}

func (p *Poller) SetOnTick(fn func(Tick)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onTick = fn
}

// Wake asks the loop to reconsider: new credentials, the game launching, a
// config reload. It never causes an immediate request if that would breach the
// minimum request gap, so it is safe to call from any event, as often as it
// fires.
func (p *Poller) Wake() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Resume clears the stale-credential stop. Called when the bridge delivers a
// payload that actually changed something.
func (p *Poller) Resume() {
	p.mu.Lock()
	wasStale := p.status.Stale
	p.status.Stale = false
	p.status.Failures = 0
	p.mu.Unlock()
	if wasStale {
		log.Print("poller: credentials refreshed, resuming")
	}
	p.Wake()
}

func (p *Poller) Status() Status {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.status
}

// pauseReason returns why the loop should not be polling, or "" to poll.
func (p *Poller) pauseReason() string {
	if _, _, ok := p.creds.Get(); !ok {
		return "waiting for the browser bridge to deliver a session"
	}
	p.mu.RLock()
	stale := p.status.Stale
	p.mu.RUnlock()
	if stale {
		return "credentials were rejected; open any Dead Frontier page to refresh them"
	}
	cfg := p.cfg()
	if cfg.Poll.OnlyWhenGameRunning && p.game != nil && !p.game.State().Running {
		return "the game is not running (poll.only_when_game_running)"
	}
	return ""
}

// interval is the base cadence for the current state, before jitter.
func (p *Poller) interval() time.Duration {
	cfg := p.cfg()
	if p.game != nil && p.game.State().Running {
		return cfg.Poll.ActiveInterval.Duration
	}
	return cfg.Poll.IdleInterval.Duration
}

// jittered spreads an interval by +/- the configured fraction.
func (p *Poller) jittered(d time.Duration) time.Duration {
	j := p.cfg().Poll.Jitter
	if j <= 0 {
		return d
	}
	factor := 1 + (rand.Float64()*2-1)*j
	out := time.Duration(float64(d) * factor)
	if out < 0 {
		return d
	}
	return out
}

// backoff is the delay after n consecutive failures: the base interval doubled
// per failure, capped at poll.backoff_max, jittered.
func (p *Poller) backoff(n int) time.Duration {
	cfg := p.cfg()
	d := p.interval()
	for i := 1; i < n && d < cfg.Poll.BackoffMax.Duration; i++ {
		d *= 2
	}
	if d > cfg.Poll.BackoffMax.Duration {
		d = cfg.Poll.BackoffMax.Duration
	}
	return p.jittered(d)
}

// Run polls until ctx is done.
func (p *Poller) Run(ctx context.Context) {
	// The first poll happens as soon as there is anything to poll with, rather
	// than one interval later - otherwise starting df-hud with the game already
	// running shows an empty HUD for ten seconds for no reason.
	next := p.now()

	for {
		if reason := p.pauseReason(); reason != "" {
			p.setPaused(reason)
			select {
			case <-ctx.Done():
				return
			case <-p.wake:
				// Something changed; re-evaluate. Do not poll instantly - the
				// gap check below still applies.
				next = p.now()
				continue
			}
		}
		p.clearPaused()

		// Never sooner than the minimum gap after the last request, whatever
		// the schedule or a wake says.
		if floor := p.lastRequest().Add(p.minGap); next.Before(floor) {
			next = floor
		}
		p.setNextAttempt(next)

		wait := next.Sub(p.now())
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-p.wake:
				timer.Stop()
				// A wake brings the next poll forward to the earliest polite
				// moment, and no earlier.
				next = p.lastRequest().Add(p.minGap)
				continue
			case <-timer.C:
			}
		}

		tick := p.pollOnce(ctx, true)
		if ctx.Err() != nil {
			return
		}
		if tick.Err != nil && errors.Is(tick.Err, ErrStaleCredentials) {
			continue // paused on the next iteration; no delay computed
		}
		if tick.Err != nil {
			next = p.now().Add(p.backoff(p.Status().Failures))
			continue
		}
		next = p.now().Add(p.jittered(p.interval()))
	}
}

// Once performs a single poll regardless of the schedule, for -once and for a
// manual refresh. It still refuses when there are no credentials, and still
// respects the minimum request gap.
func (p *Poller) Once(ctx context.Context) Tick {
	if _, _, ok := p.creds.Get(); !ok {
		return Tick{At: p.now(), Err: errors.New("no credentials yet: run the bridge userscript on any logged-in Dead Frontier page")}
	}
	if gap := p.minGap - p.now().Sub(p.lastRequest()); gap > 0 && !p.lastRequest().IsZero() {
		select {
		case <-ctx.Done():
			return Tick{At: p.now(), Err: ctx.Err()}
		case <-time.After(gap):
		}
	}
	return p.pollOnce(ctx, false)
}

func (p *Poller) pollOnce(ctx context.Context, scheduled bool) Tick {
	cr, _, ok := p.creds.Get()
	if !ok {
		return Tick{At: p.now(), Err: errors.New("no credentials"), Scheduled: scheduled}
	}

	cfg := p.cfg()
	reqCtx, cancel := context.WithTimeout(ctx, cfg.DF.Timeout.Duration)
	defer cancel()

	start := p.now()
	p.mu.Lock()
	p.lastPoll = start
	p.status.LastAttempt = start
	p.status.TotalPolls++
	p.mu.Unlock()

	if p.gate != nil {
		if err := p.gate.Wait(reqCtx); err != nil {
			return Tick{At: p.now(), Err: err, Scheduled: scheduled}
		}
	}
	vars, err := p.client.GetValues(reqCtx, cr)
	tick := Tick{At: p.now(), Vars: vars, Err: err, Scheduled: scheduled}

	p.mu.Lock()
	switch {
	case err == nil:
		p.status.Failures = 0
		p.status.LastError = ""
		p.status.LastSuccess = tick.At
	case errors.Is(err, ErrStaleCredentials):
		p.status.Stale = true
		p.status.LastError = err.Error()
		p.status.TotalFailure++
	default:
		p.status.Failures++
		p.status.LastError = err.Error()
		p.status.TotalFailure++
	}
	failures, stale := p.status.Failures, p.status.Stale
	fn := p.onTick
	p.mu.Unlock()

	switch {
	case stale && err != nil:
		// The one error worth shouting about, with the fix, once.
		log.Print("poller: the server rejected our credentials - polling STOPPED. " +
			"This normally means sc was rotated by a login elsewhere. " +
			"Open any Dead Frontier page with the bridge userscript installed and it will resume by itself.")
	case err != nil && failures == 1:
		log.Printf("poller: %v (backing off; will keep trying)", err)
	case err != nil && failures%10 == 0:
		// Do not narrate every failure of an outage, but do not go silent either.
		log.Printf("poller: still failing after %d attempts: %v", failures, err)
	}

	if fn != nil {
		fn(tick)
	}
	return tick
}

func (p *Poller) lastRequest() time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastPoll
}

func (p *Poller) setPaused(reason string) {
	p.mu.Lock()
	p.status.Paused = true
	p.status.PauseReason = reason
	p.status.NextAttempt = time.Time{}
	shouldLog := p.loggedPause != reason
	p.loggedPause = reason
	p.mu.Unlock()
	if shouldLog {
		log.Printf("poller: paused - %s", reason)
	}
}

func (p *Poller) clearPaused() {
	p.mu.Lock()
	wasPaused := p.status.Paused
	p.status.Paused = false
	p.status.PauseReason = ""
	p.loggedPause = ""
	p.mu.Unlock()
	if wasPaused {
		log.Print("poller: resumed")
	}
}

func (p *Poller) setNextAttempt(t time.Time) {
	p.mu.Lock()
	p.status.NextAttempt = t
	p.mu.Unlock()
}

// SetGate installs the process-wide request spacing gate.
func (p *Poller) SetGate(g *Gate) { p.gate = g }
