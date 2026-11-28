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
	defaultBreakerIdleTTL   = 10 * time.Minute
	maxBreakerEndpoints     = 256
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
	// outcomePeerResponded means the peer answered over HTTP (e.g. a valid
	// retryable 5xx error envelope or oversized 200 body): it resets a closed
	// consecutive-failure streak without closing a half-open circuit.
	outcomePeerResponded
	// outcomeNeutral means no peer verdict was obtained (e.g. caller
	// cancellation, stopped work, or local signing key error).
	outcomeNeutral
)

// probeToken identifies a single half-open probe lease (0 means non-probe).
type probeToken uint64

// circuitBreaker is a generic consecutive-failure transport circuit breaker.
// In the healthy closed state, Allow and RecordSuccess use a lock-free atomic
// fast path. Once open, invocations fast-fail with ErrCircuitOpen until the
// cooldown elapses, at which point half-open admits exactly one probe (with a
// lease timeout so a stuck or panicked probe can never wedge the endpoint).
type circuitBreaker struct {
	threshold int
	cooldown  time.Duration
	now       func() time.Time

	fastClosed   atomic.Bool
	lastUsedNano atomic.Int64

	mu                  sync.Mutex
	state               breakerState
	consecutiveFailures int
	openedAt            time.Time
	probeInFlight       bool
	probeStartedAt      time.Time
	probeGen            probeToken
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
	b.lastUsedNano.Store(now().UnixNano())
	return b
}

// Allow reports whether a request may proceed and returns a non-zero probeToken
// when the caller owns the single half-open probe slot.
func (b *circuitBreaker) Allow() (allowed bool, probe probeToken) {
	if b.fastClosed.Load() {
		return true, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	switch b.state {
	case breakerClosed:
		return true, 0
	case breakerOpen:
		if now.Sub(b.openedAt) < b.cooldown {
			return false, 0
		}
		b.state = breakerHalfOpen
		b.probeInFlight = true
		b.probeStartedAt = now
		b.probeGen++
		return true, b.probeGen
	case breakerHalfOpen:
		if b.probeInFlight && now.Sub(b.probeStartedAt) < b.cooldown {
			return false, 0
		}
		b.probeInFlight = true
		b.probeStartedAt = now
		b.probeGen++
		return true, b.probeGen
	default:
		return true, 0
	}
}

// RecordSuccess marks a completed request as healthy. A healthy half-open probe
// immediately closes the circuit.
func (b *circuitBreaker) RecordSuccess(probe probeToken) {
	if probe == 0 && b.fastClosed.Load() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if probe != 0 {
		if b.state != breakerHalfOpen || b.probeGen != probe {
			return
		}
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
func (b *circuitBreaker) RecordFailure(probe probeToken) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if probe != 0 {
		if b.state != breakerHalfOpen || b.probeGen != probe {
			return
		}
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

// RecordPeerResponded resets a closed failure streak when the peer answers over
// HTTP without full success (e.g. a valid retryable error envelope or oversized
// response), and releases a half-open probe slot without closing or re-opening.
func (b *circuitBreaker) RecordPeerResponded(probe probeToken) {
	if probe == 0 && b.fastClosed.Load() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if probe != 0 {
		if b.state == breakerHalfOpen && b.probeGen == probe {
			b.probeInFlight = false
		}
		return
	}
	if b.state == breakerClosed {
		b.consecutiveFailures = 0
		b.fastClosed.Store(true)
	}
}

// ReleaseProbe frees the half-open probe slot when a probe finishes with a
// neutral outcome (such as caller cancellation or a recovered panic) without
// closing or re-opening the circuit.
func (b *circuitBreaker) ReleaseProbe(probe probeToken) {
	if probe == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == breakerHalfOpen && b.probeGen == probe {
		b.probeInFlight = false
	}
}

func (b *circuitBreaker) Record(outcome breakerOutcome, probe probeToken) {
	switch outcome {
	case outcomeSuccess:
		b.RecordSuccess(probe)
	case outcomeFailure:
		b.RecordFailure(probe)
	case outcomePeerResponded:
		b.RecordPeerResponded(probe)
	case outcomeNeutral:
		b.ReleaseProbe(probe)
	}
}

func (b *circuitBreaker) State() breakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (b *circuitBreaker) isIdleClosed() bool {
	return b.fastClosed.Load()
}

func (b *circuitBreaker) touch(now time.Time) {
	b.lastUsedNano.Store(now.UnixNano())
}

// breakerRegistry maintains one transport circuit breaker per plugin endpoint
// and evicts retired idle healthy entries once idleTTL elapses so endpoint
// rotations cannot grow unboundedly without disturbing active endpoints.
type breakerRegistry struct {
	threshold  int
	cooldown   time.Duration
	idleTTL    time.Duration
	maxEntries int
	now        func() time.Time
	entries    sync.Map // map[string]*circuitBreaker
	count      atomic.Int64
	sweepMu    sync.Mutex
}

func newBreakerRegistry(threshold int, cooldown time.Duration, now func() time.Time) *breakerRegistry {
	if now == nil {
		now = time.Now
	}
	return &breakerRegistry{
		threshold:  threshold,
		cooldown:   cooldown,
		idleTTL:    defaultBreakerIdleTTL,
		maxEntries: maxBreakerEndpoints,
		now:        now,
	}
}

func (r *breakerRegistry) forEndpoint(endpoint string) *circuitBreaker {
	if endpoint == "" {
		return nil
	}
	now := r.now()
	if existing, ok := r.entries.Load(endpoint); ok {
		b := existing.(*circuitBreaker)
		b.touch(now)
		return b
	}
	if r.count.Load() >= int64(r.maxEntries) {
		r.evictIdle(now)
	}
	created := newCircuitBreaker(r.threshold, r.cooldown, r.now)
	created.touch(now)
	actual, loaded := r.entries.LoadOrStore(endpoint, created)
	if !loaded {
		r.count.Add(1)
	}
	b := actual.(*circuitBreaker)
	b.touch(now)
	return b
}

func (r *breakerRegistry) evictIdle(now time.Time) {
	r.sweepMu.Lock()
	defer r.sweepMu.Unlock()
	if r.count.Load() < int64(r.maxEntries) {
		return
	}
	cutoff := now.Add(-r.idleTTL).UnixNano()
	r.entries.Range(func(k, v any) bool {
		b := v.(*circuitBreaker)
		if b.isIdleClosed() && b.lastUsedNano.Load() <= cutoff {
			if _, loaded := r.entries.LoadAndDelete(k); loaded {
				r.count.Add(-1)
			}
		}
		return true
	})
}

func (r *breakerRegistry) delete(endpoint string) {
	if _, loaded := r.entries.LoadAndDelete(endpoint); loaded {
		r.count.Add(-1)
	}
}

var defaultBreakers = newBreakerRegistry(defaultBreakerThreshold, defaultBreakerCooldown, nil)

// classifyRoundOutcome maps a search round result to a breaker outcome.
// Only transport errors, downstream/per-attempt timeouts (when the caller's
// parent ctx has not expired), and HTTP 5xx responses without a valid plugin
// error envelope count as failures. Caller cancellation, parent-context
// deadline expiry (matching call.invocationFailure), stopped work,
// plugin-declared error envelopes (4xx terminal or 5xx retryable), and
// response-size violations never trip the breaker.
func classifyRoundOutcome(ctx context.Context, pin *plugins.Pin, result *devhost.Result, err error) breakerOutcome {
	if err == nil {
		return outcomeSuccess
	}
	if ctx.Err() != nil {
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
			return outcomePeerResponded
		}
		if len(result.Issues) > 0 && result.Issues[0].Code == devhost.CodeResponseTooLarge {
			return outcomePeerResponded
		}
		if result.Status >= 500 {
			return outcomeFailure
		}
		return outcomePeerResponded
	}
	if errors.Is(err, plugins.ErrSigningKeys) {
		return outcomeNeutral
	}
	return outcomeFailure
}
