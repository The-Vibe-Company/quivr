package weaviate

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

const (
	defaultBreakerThreshold = 5
	defaultBreakerCooldown  = 5 * time.Second
)

// ErrCircuitOpen is returned when the Weaviate gRPC circuit breaker is open.
var ErrCircuitOpen = fmt.Errorf("weaviate gRPC circuit open: %w", retrieval.ErrUnavailable)

type breakerState int32

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

// circuitBreaker is a generic, process-level consecutive-failure circuit breaker.
// In the healthy closed state, Allow and RecordSuccess use a lock-free atomic
// fast path. Once open, requests fast-fail with ErrCircuitOpen until cooldown
// elapses, at which point half-open admits exactly one probe.
type circuitBreaker struct {
	threshold int
	cooldown  time.Duration
	now       func() time.Time

	fastClosed atomic.Bool

	mu                  sync.Mutex
	state               breakerState
	consecutiveFailures int
	openedAt            time.Time
	probeInFlight       bool
}

func newCircuitBreaker(threshold int, cooldown time.Duration, now func() time.Time) *circuitBreaker {
	if threshold <= 0 {
		threshold = defaultBreakerThreshold
	}
	if cooldown <= 0 {
		cooldown = defaultBreakerCooldown
	}
	if now == nil {
		now = time.Now
	}
	b := &circuitBreaker{
		threshold: threshold,
		cooldown:  cooldown,
		now:       now,
		state:     breakerClosed,
	}
	b.fastClosed.Store(true)
	return b
}

// Allow reports whether a request may proceed and whether it owns the single
// half-open probe slot.
func (b *circuitBreaker) Allow() (allowed bool, isProbe bool) {
	if b.fastClosed.Load() {
		return true, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case breakerClosed:
		return true, false
	case breakerOpen:
		if b.now().Sub(b.openedAt) < b.cooldown {
			return false, false
		}
		b.state = breakerHalfOpen
		b.probeInFlight = true
		return true, true
	case breakerHalfOpen:
		if b.probeInFlight {
			return false, false
		}
		b.probeInFlight = true
		return true, true
	default:
		return true, false
	}
}

// RecordSuccess marks a completed request as healthy. A healthy half-open probe
// immediately closes the circuit.
func (b *circuitBreaker) RecordSuccess(isProbe bool) {
	if !isProbe && b.fastClosed.Load() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if isProbe {
		b.probeInFlight = false
		b.consecutiveFailures = 0
		b.state = breakerClosed
		b.fastClosed.Store(true)
		return
	}
	if b.state == breakerClosed {
		b.consecutiveFailures = 0
		b.fastClosed.Store(true)
	}
}

// RecordFailure records a backend failure. In closed state, reaching threshold
// consecutive failures opens the circuit; a failed half-open probe re-opens it.
func (b *circuitBreaker) RecordFailure(isProbe bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if isProbe {
		b.probeInFlight = false
		b.state = breakerOpen
		b.openedAt = b.now()
		b.fastClosed.Store(false)
		return
	}
	if b.state != breakerClosed {
		return
	}
	b.consecutiveFailures++
	b.fastClosed.Store(false)
	if b.consecutiveFailures >= b.threshold {
		b.state = breakerOpen
		b.openedAt = b.now()
	}
}

// ReleaseProbe frees the half-open probe slot when a probe finishes with a
// neutral outcome (such as caller cancellation) without recording success or
// failure.
func (b *circuitBreaker) ReleaseProbe(isProbe bool) {
	if !isProbe {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == breakerHalfOpen {
		b.probeInFlight = false
	}
}

// State returns the current breaker state (primarily for tests and diagnostics).
func (b *circuitBreaker) State() breakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}
