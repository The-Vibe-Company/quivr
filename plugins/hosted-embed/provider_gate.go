package main

import (
	"context"
	"sync"
	"time"
)

// providerGate shares admission and throttling across document/query calls
// in one plugin process. Already outstanding calls may finish after a 429/503.
type providerGate struct {
	slots        chan struct{}
	mu           sync.Mutex
	blockedUntil time.Time
}

func (g *providerGate) acquire(ctx context.Context) error {
	select {
	case g.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	for {
		g.mu.Lock()
		delay := time.Until(g.blockedUntil)
		g.mu.Unlock()
		if delay <= 0 {
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			g.release()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func (g *providerGate) release() { <-g.slots }
func (g *providerGate) throttle(delay time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	until := time.Now().Add(delay)
	if until.After(g.blockedUntil) {
		g.blockedUntil = until
	}
}
