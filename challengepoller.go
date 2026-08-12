package main

import (
	"context"
	"errors"
	"log"
	"math/rand/v2"
	"sync"
	"time"
)

// The challenge board's scheduler, separate from the player-record poller
// because the two have genuinely different needs: the board changes on the scale
// of kills rather than steps, it is a hashed call that additionally needs the
// session cookie, and it must not stop the HUD's core data when it fails.
//
// What the two DO share is the rate gate, so "no two requests closer than the
// minimum gap" stays a property of df-hud's traffic rather than of one
// scheduler's traffic.
//
// Failure here is deliberately quieter than in the main poller. A missing salt or
// an expired cookie costs the challenge widget and nothing else, so it degrades
// to "no board" rather than taking the position and XP readouts down with it.
type ChallengePoller struct {
	client *Client
	creds  *credStore
	game   *GameWatcher
	gate   *rateGate
	cfg    func() *Config
	// level supplies the player level and gold status, which scale reward XP.
	// A function because they come from the other poller's snapshot.
	level func() (level int, gold bool)

	onBoard func([]Challenge)

	wake chan struct{}

	mu       sync.RWMutex
	failures int
	lastErr  string
	lastAt   time.Time
	stale    bool
}

func newChallengePoller(client *Client, creds *credStore, game *GameWatcher, gate *rateGate,
	cfg func() *Config, level func() (int, bool)) *ChallengePoller {
	return &ChallengePoller{
		client: client,
		creds:  creds,
		game:   game,
		gate:   gate,
		cfg:    cfg,
		level:  level,
		wake:   make(chan struct{}, 1),
	}
}

func (p *ChallengePoller) SetOnBoard(fn func([]Challenge)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onBoard = fn
}

func (p *ChallengePoller) Wake() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Resume clears the stale-credential stop, on a fresh bridge payload.
func (p *ChallengePoller) Resume() {
	p.mu.Lock()
	p.stale, p.failures = false, 0
	p.mu.Unlock()
	p.Wake()
}

// interval is the cadence for the current state: the configured value while
// playing, stretched to the idle cadence when the game is closed.
func (p *ChallengePoller) interval() time.Duration {
	running := p.game != nil && p.game.State().Running
	return p.cfg().Poll.EffectiveChallengeInterval(running)
}

func (p *ChallengePoller) jittered(d time.Duration) time.Duration {
	j := p.cfg().Poll.Jitter
	if j <= 0 {
		return d
	}
	out := time.Duration(float64(d) * (1 + (rand.Float64()*2-1)*j))
	if out <= 0 {
		return d
	}
	return out
}

// pauseReason mirrors the main poller's, plus the two requirements unique to
// this endpoint.
func (p *ChallengePoller) pauseReason() string {
	cfg := p.cfg()
	if !cfg.Widget.Challenges.Enabled {
		return "the challenge widget is disabled"
	}
	cr, _, ok := p.creds.Get()
	if !ok {
		return "waiting for the browser bridge to deliver a session"
	}
	p.mu.RLock()
	stale := p.stale
	p.mu.RUnlock()
	if stale {
		return "credentials were rejected; open any Dead Frontier page to refresh them"
	}
	if cfg.SigningSalt(p.creds) == "" {
		// Specific on purpose: this is the one failure a user can fix in a
		// minute, and "no challenges" alone would never point them at it.
		return "no signing salt yet - load the Outpost page with the bridge userscript " +
			"(***+) or the the bridge userscript installed"
	}
	if cr.Cookie == "" {
		return "no session cookie yet - the challenge board needs one; load any " +
			"Dead Frontier page to send it"
	}
	if cfg.Poll.OnlyWhenGameRunning && p.game != nil && !p.game.State().Running {
		return "the game is not running (poll.only_when_game_running)"
	}
	// Wait for the player record before the first board.
	//
	// This is not cosmetic ordering. The level drives the eligibility filter the
	// game itself applies, so parsing the board at level 0 hides challenges that
	// are genuinely in-band - a board of 11 where the truth is 12 - and it also
	// leaves reward XP unscaled. A board that is subtly wrong is worse than one
	// that arrives five seconds later.
	if p.level != nil {
		if level, _ := p.level(); level <= 0 {
			return "waiting for the first player record (the level decides which " +
				"challenges apply to you)"
		}
	}
	return ""
}

func (p *ChallengePoller) Run(ctx context.Context) {
	next := time.Now()
	loggedPause := ""

	for {
		if reason := p.pauseReason(); reason != "" {
			if loggedPause != reason {
				log.Printf("challenges: paused - %s", reason)
				loggedPause = reason
			}
			select {
			case <-ctx.Done():
				return
			case <-p.wake:
				next = time.Now()
				continue
			}
		}
		if loggedPause != "" {
			log.Print("challenges: resumed")
			loggedPause = ""
		}

		// Never schedule earlier than the shared gate would allow anyway.
		if floor := p.gate.Reserved(); next.Before(floor) {
			next = floor
		}
		if wait := time.Until(next); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-p.wake:
				timer.Stop()
				next = p.gate.Reserved()
				continue
			case <-timer.C:
			}
		}

		err := p.pollOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil && errors.Is(err, ErrStaleCredentials) {
			continue // paused on the next iteration
		}
		if err != nil {
			next = time.Now().Add(p.backoff())
			continue
		}
		next = time.Now().Add(p.jittered(p.interval()))
	}
}

func (p *ChallengePoller) backoff() time.Duration {
	p.mu.RLock()
	failures := p.failures
	p.mu.RUnlock()
	cfg := p.cfg()
	d := p.interval()
	for i := 1; i < failures && d < cfg.Poll.BackoffMax.Duration; i++ {
		d *= 2
	}
	if d > cfg.Poll.BackoffMax.Duration {
		d = cfg.Poll.BackoffMax.Duration
	}
	return p.jittered(d)
}

// Once fetches the board immediately, subject only to the shared rate gate.
func (p *ChallengePoller) Once(ctx context.Context) error { return p.pollOnce(ctx) }

func (p *ChallengePoller) pollOnce(ctx context.Context) error {
	cr, _, ok := p.creds.Get()
	if !ok {
		return errors.New("no credentials")
	}
	cfg := p.cfg()
	salt := cfg.SigningSalt(p.creds)

	if err := p.gate.Wait(ctx); err != nil {
		return err
	}

	reqCtx, cancel := context.WithTimeout(ctx, cfg.DF.Timeout.Duration)
	defer cancel()

	// The cookie is set per call rather than once at construction because a fresh
	// bridge payload replaces it and the client is shared.
	p.client.Cookie = cr.Cookie
	vars, err := p.client.LoadChallenge(reqCtx, cr, salt)

	p.mu.Lock()
	p.lastAt = time.Now()
	switch {
	case err == nil:
		p.failures, p.lastErr = 0, ""
	case errors.Is(err, ErrStaleCredentials):
		p.stale, p.lastErr = true, err.Error()
	default:
		p.failures++
		p.lastErr = err.Error()
	}
	failures := p.failures
	fn := p.onBoard
	p.mu.Unlock()

	if err != nil {
		// Only the first failure and then every tenth: an outage should not
		// narrate itself, but it should not go silent either.
		if failures == 1 || failures%10 == 0 || errors.Is(err, ErrStaleCredentials) {
			log.Printf("challenges: %v", err)
		}
		return err
	}

	level, gold := 0, false
	if p.level != nil {
		level, gold = p.level()
	}
	board := parseChallenges(vars, level, gold)
	if fn != nil {
		fn(board)
	}
	return nil
}

// Status is what the HUD shows when the board is missing.
func (p *ChallengePoller) Status() (failures int, stale bool, lastErr string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.failures, p.stale, p.lastErr
}
