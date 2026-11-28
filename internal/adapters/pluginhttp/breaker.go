package pluginhttp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost"
)

const (
	defaultBreakerThreshold = 5
	defaultBreakerCooldown  = 5 * time.Second
)

// ErrCircuitOpen is returned when a plugin endpoint's transport circuit breaker
// is open and fast-fails invocations without dialing the peer.
var ErrCircuitOpen = fmt.Errorf("plugin endpoint circuit open: %w", ErrUnavailable)

type breakerState int32

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

type breakerOutcome int

const (
	outcomeSuccess breakerOutcome = iota
	outcomeFailure
	outcomeNeutral
)

// circuitBreaker is a generic consecutive-failure transport circuit breaker.
// In the healthy closed state, Allow and RecordSuccess use a lock-free atomic
// fast path. Once open, invocations fast-fail with ErrCircuitOpen until the
// cooldown elapses, at which point half-open admits exactly one probe.
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

// RecordFailure records a transport/5xx failure. Reaching threshold consecutive
// failures opens the circuit; a failed half-open probe re-opens it.
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
// neutral outcome (such as caller cancellation or a retryable declared
// envelope) without closing or re-opening the circuit.
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

func (b *circuitBreaker) Record(outcome breakerOutcome, isProbe bool) {
	switch outcome {
	case outcomeSuccess:
		b.RecordSuccess(isProbe)
	case outcomeFailure:
		b.RecordFailure(isProbe)
	case outcomeNeutral:
		b.ReleaseProbe(isProbe)
	}
}

func (b *circuitBreaker) State() breakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// breakerRegistry maintains one transport circuit breaker per plugin endpoint.
type breakerRegistry struct {
	threshold int
	cooldown  time.Duration
	now       func() time.Time
	entries   sync.Map // map[string]*circuitBreaker
}

func newBreakerRegistry(threshold int, cooldown time.Duration, now func() time.Time) *breakerRegistry {
	return &breakerRegistry{
		threshold: threshold,
		cooldown:  cooldown,
		now:       now,
	}
}

func (r *breakerRegistry) forEndpoint(endpoint string) *circuitBreaker {
	if endpoint == "" {
		return nil
	}
	if existing, ok := r.entries.Load(endpoint); ok {
		return existing.(*circuitBreaker)
	}
	created := newCircuitBreaker(r.threshold, r.cooldown, r.now)
	actual, _ := r.entries.LoadOrStore(endpoint, created)
	return actual.(*circuitBreaker)
}

func (r *breakerRegistry) delete(endpoint string) {
	r.entries.Delete(endpoint)
}

var defaultBreakers = newBreakerRegistry(defaultBreakerThreshold, defaultBreakerCooldown, nil)

// classifyRoundOutcome maps a search round result to a breaker outcome.
// Only transport errors, timeouts, and HTTP 5xx responses without a valid
// plugin error envelope count as failures. Plugin-declared error envelopes
// (4xx terminal or 5xx retryable), caller cancellation, stopped work, and
// response-size violations never trip the breaker.
func classifyRoundOutcome(ctx context.Context, pin *plugins.Pin, result *devhost.Result, err error) breakerOutcome {
	if err == nil {
		return outcomeSuccess
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return outcomeNeutral
	}
	if pin != nil && plugins.Stopped(ctx, pin) {
		return outcomeNeutral
	}
	if result != nil {
		if result.Error != nil {
			// A valid plugin-declared error envelope proves the peer answered
			// the protocol; never trip the transport breaker.
			if result.Status >= 400 && result.Status < 500 {
				return outcomeSuccess
			}
			return outcomeNeutral
		}
		if len(result.Issues) > 0 && result.Issues[0].Code == devhost.CodeResponseTooLarge {
			return outcomeNeutral
		}
		if result.Status >= 500 {
			return outcomeFailure
		}
		return outcomeNeutral
	}
	if errors.Is(err, plugins.ErrSigningKeys) {
		return outcomeNeutral
	}
	return outcomeFailure
}
