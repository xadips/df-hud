package main

import (
	"context"
	"sync"
	"time"
)

// rateGate is the global floor on how closely two requests to the game server
// may follow each other, shared by every poller in the process.
//
// It exists because the guarantee has to be about df-hud's traffic, not about
// one scheduler's traffic. With a per-poller floor, adding the challenge poller
// would quietly have turned "no two requests within 5s" into "no two requests to
// the same endpoint within 5s" - the number in the README would still have read
// 5s while the real behaviour had changed. One gate keeps the documented
// property true no matter how many schedulers are added later.
type rateGate struct {
	mu   sync.Mutex
	min  time.Duration
	last time.Time
	now  func() time.Time
}

func newRateGate(min time.Duration) *rateGate {
	return &rateGate{min: min, now: time.Now}
}

// Wait blocks until a request may be sent, then records it. It returns ctx's
// error if the wait is interrupted, in which case no request was recorded.
//
// The slot is reserved before releasing the lock, so two goroutines arriving
// together are spaced rather than both waiting for the same instant and then
// firing at once.
func (g *rateGate) Wait(ctx context.Context) error {
	g.mu.Lock()
	now := g.now()
	slot := now
	if !g.last.IsZero() {
		if earliest := g.last.Add(g.min); earliest.After(slot) {
			slot = earliest
		}
	}
	g.last = slot
	g.mu.Unlock()

	delay := slot.Sub(now)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Reserved is the instant the most recently granted slot was scheduled for. Used
// by schedulers to avoid setting a wake-up earlier than the gate would allow.
func (g *rateGate) Reserved() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.last
}
