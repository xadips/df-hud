// Package rategate provides a process-wide request spacing gate.
package rategate

import (
	"context"
	"sync"
	"time"
)

// Gate enforces a minimum gap between reserved request slots.
type Gate struct {
	mu   sync.Mutex
	min  time.Duration
	last time.Time
	now  func() time.Time
}

// New constructs a Gate with the requested minimum spacing.
func New(min time.Duration) *Gate {
	return &Gate{min: min, now: time.Now}
}

// Wait blocks until a request may be sent, then records it.
func (g *Gate) Wait(ctx context.Context) error {
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

// Reserved is the instant the most recently granted slot was scheduled for.
func (g *Gate) Reserved() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.last
}
