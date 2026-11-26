package httpapi

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

const (
	defaultSearchInitialLimit  = 64
	defaultSearchMinLimit      = 4
	defaultSearchMaxLimit      = 128
	defaultSearchMaxQueue      = 32
	defaultSearchQueueTimeout  = 100 * time.Millisecond
	defaultSearchTargetLatency = 300 * time.Millisecond
	defaultSearchMinRTTFloor   = 10 * time.Millisecond
	defaultSearchGradientRatio = 3.0
	defaultSearchBackoffRatio  = 0.75
	defaultSearchWindow        = 250 * time.Millisecond
	defaultSearchMinRTTDecay   = 10 * time.Second
)

// SearchAdmissionConfig tunes the AIMD adaptive concurrency limiter and its
// bounded admission wait queue. Zero values select safe engine defaults.
// Negative MaxQueue, QueueTimeout or Window disable queuing, queue wait or
// window batching respectively.
type SearchAdmissionConfig struct {
	InitialLimit  int
	MinLimit      int
	MaxLimit      int
	MaxQueue      int
	QueueTimeout  time.Duration
	TargetLatency time.Duration
	GradientRatio float64
	BackoffRatio  float64
	Window        time.Duration
	Now           func() time.Time
}

type searchWaiter struct {
	ch      chan struct{}
	granted bool
}

// SearchAdmission bounds concurrent searches per API process using an AIMD
// (Additive Increase, Multiplicative Decrease) limiter with a short bounded
// FIFO wait queue. Under healthy high utilization the limit climbs additively
// up to MaxLimit; under latency spikes or downstream overload it shrinks
// multiplicatively down to MinLimit and sheds excess requests cleanly with a
// Retry-After hint.
type SearchAdmission struct {
	initialLimit  int
	minLimit      int
	maxLimit      int
	maxQueue      int
	queueTimeout  time.Duration
	targetLatency time.Duration
	gradientRatio float64
	backoffRatio  float64
	window        time.Duration
	now           func() time.Time

	mu                sync.Mutex
	limit             int
	inFlight          int
	waiters           []*searchWaiter
	minRTT            time.Duration
	minRTTAt          time.Time
	smoothedRTT       time.Duration
	windowStart       time.Time
	windowSamples     int
	windowMaxInFlight int
	windowOverload    bool
	windowOverloads   int
}

// NewSearchAdmission builds an AIMD search limiter with normalized bounds.
func NewSearchAdmission(cfg SearchAdmissionConfig) *SearchAdmission {
	minLimit := cfg.MinLimit
	if minLimit <= 0 {
		minLimit = defaultSearchMinLimit
	}
	maxLimit := cfg.MaxLimit
	if maxLimit <= 0 {
		maxLimit = defaultSearchMaxLimit
	}
	if maxLimit < minLimit {
		maxLimit = minLimit
	}
	initial := cfg.InitialLimit
	if initial <= 0 {
		initial = defaultSearchInitialLimit
	}
	initial = min(max(initial, minLimit), maxLimit)

	maxQueue := cfg.MaxQueue
	switch {
	case maxQueue == 0:
		maxQueue = defaultSearchMaxQueue
	case maxQueue < 0:
		maxQueue = 0
	}
	queueTimeout := cfg.QueueTimeout
	switch {
	case queueTimeout == 0:
		queueTimeout = defaultSearchQueueTimeout
	case queueTimeout < 0:
		queueTimeout = 0
	}
	targetLatency := cfg.TargetLatency
	if targetLatency <= 0 {
		targetLatency = defaultSearchTargetLatency
	}
	gradientRatio := cfg.GradientRatio
	if gradientRatio <= 0 {
		gradientRatio = defaultSearchGradientRatio
	}
	backoffRatio := cfg.BackoffRatio
	if backoffRatio <= 0 || backoffRatio >= 1 {
		backoffRatio = defaultSearchBackoffRatio
	}
	window := cfg.Window
	switch {
	case window == 0:
		window = defaultSearchWindow
	case window < 0:
		window = 0
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &SearchAdmission{
		initialLimit:  initial,
		minLimit:      minLimit,
		maxLimit:      maxLimit,
		maxQueue:      maxQueue,
		queueTimeout:  queueTimeout,
		targetLatency: targetLatency,
		gradientRatio: gradientRatio,
		backoffRatio:  backoffRatio,
		window:        window,
		now:           now,
		limit:         initial,
	}
}

// WithSearchAdmission overrides the default AIMD search concurrency limiter.
func WithSearchAdmission(limiter *SearchAdmission) Option {
	return func(a *API) {
		if limiter != nil {
			a.searchAdmission = limiter
		}
	}
}

// AdmissionLease represents one admitted in-flight search slot.
type AdmissionLease struct {
	limiter *SearchAdmission
	started time.Time
	done    atomic.Bool
}

// Release records search completion feedback and returns the concurrency slot.
// It is idempotent so a deferred fallback release after a panic is safe.
func (lease *AdmissionLease) Release(overload, clientCanceled bool) {
	lease.ReleaseWithTarget(overload, clientCanceled, 0)
}

// ReleaseWithTarget records search completion feedback against an optional
// profile latency objective and returns the concurrency slot. When skipObserve
// is true (client cancellation, non-overload validation/auth errors, or empty
// short-circuits), the slot is returned without updating RTT or AIMD state.
func (lease *AdmissionLease) ReleaseWithTarget(overload, skipObserve bool, targetLatency time.Duration) {
	if lease == nil || !lease.done.CompareAndSwap(false, true) {
		return
	}
	rtt := max(0, lease.limiter.now().Sub(lease.started))
	lease.limiter.release(rtt, overload, skipObserve, targetLatency)
}

// Stats returns a consistent snapshot of the limiter's current state.
func (l *SearchAdmission) Stats() telemetry.SearchAdmissionStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	return telemetry.SearchAdmissionStats{
		Limit:     l.limit,
		InUse:     l.inFlight,
		Available: max(0, l.limit-l.inFlight),
		Queued:    len(l.waiters),
	}
}

// Acquire obtains a concurrency slot immediately or after waiting in the short
// bounded FIFO queue. On saturation or deadline exhaustion it returns admitted
// false along with a positive integer Retry-After delay in seconds.
func (l *SearchAdmission) Acquire(ctx context.Context) (*AdmissionLease, int, bool) {
	if ctx.Err() != nil {
		return nil, l.retryAfter(), false
	}
	l.mu.Lock()
	if l.inFlight < l.limit && len(l.waiters) == 0 {
		l.inFlight++
		started := l.now()
		l.mu.Unlock()
		return &AdmissionLease{limiter: l, started: started}, 0, true
	}
	if l.maxQueue <= 0 || len(l.waiters) >= l.maxQueue || l.queueTimeout <= 0 {
		retry := l.retryAfterLocked()
		l.mu.Unlock()
		return nil, retry, false
	}
	wait := l.queueTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := deadline.Sub(l.now())
		if remaining <= 0 {
			retry := l.retryAfterLocked()
			l.mu.Unlock()
			return nil, retry, false
		}
		if capWait := remaining / 4; capWait < wait {
			wait = capWait
		}
	}
	if wait <= 0 {
		retry := l.retryAfterLocked()
		l.mu.Unlock()
		return nil, retry, false
	}
	w := &searchWaiter{ch: make(chan struct{})}
	l.waiters = append(l.waiters, w)
	l.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-w.ch:
		if ctx.Err() != nil {
			l.release(0, false, true, 0)
			return nil, l.retryAfter(), false
		}
		return &AdmissionLease{limiter: l, started: l.now()}, 0, true
	case <-timer.C:
		return l.abortWaiter(ctx, w, false)
	case <-ctx.Done():
		return l.abortWaiter(ctx, w, true)
	}
}

func (l *SearchAdmission) abortWaiter(ctx context.Context, w *searchWaiter, ctxDone bool) (*AdmissionLease, int, bool) {
	l.mu.Lock()
	if w.granted {
		if !ctxDone && ctx.Err() == nil {
			started := l.now()
			l.mu.Unlock()
			return &AdmissionLease{limiter: l, started: started}, 0, true
		}
		l.inFlight--
		l.dispatchLocked()
		retry := l.retryAfterLocked()
		l.mu.Unlock()
		return nil, retry, false
	}
	for i, candidate := range l.waiters {
		if candidate == w {
			copy(l.waiters[i:], l.waiters[i+1:])
			l.waiters[len(l.waiters)-1] = nil
			l.waiters = l.waiters[:len(l.waiters)-1]
			break
		}
	}
	retry := l.retryAfterLocked()
	l.mu.Unlock()
	return nil, retry, false
}

func (l *SearchAdmission) release(rtt time.Duration, overload, skipObserve bool, targetOverride time.Duration) {
	l.mu.Lock()
	peak := l.inFlight
	l.inFlight--
	if !skipObserve {
		l.observeLocked(rtt, peak, overload, targetOverride)
	}
	l.dispatchLocked()
	l.mu.Unlock()
}

func (l *SearchAdmission) observeLocked(rtt time.Duration, peak int, overload bool, targetOverride time.Duration) {
	now := l.now()
	target := l.targetLatency
	baselineEligible := true
	if targetOverride > l.targetLatency {
		target = targetOverride
		baselineEligible = false
	}
	if baselineEligible {
		if l.smoothedRTT == 0 {
			l.smoothedRTT = rtt
		} else {
			l.smoothedRTT = (3*l.smoothedRTT + rtt) / 4
		}
		if !overload && rtt > 0 {
			if l.minRTT == 0 || rtt < l.minRTT {
				l.minRTT = rtt
				l.minRTTAt = now
			} else if now.Sub(l.minRTTAt) >= defaultSearchMinRTTDecay {
				l.minRTT = (l.minRTT + rtt) / 2
				l.minRTTAt = now
			}
		}
	}
	congested := overload || rtt > target
	if !congested && baselineEligible && l.minRTT > 0 && rtt >= target/2 {
		baseline := max(l.minRTT, defaultSearchMinRTTFloor)
		if float64(rtt) > float64(baseline)*l.gradientRatio {
			congested = true
		}
	}
	if l.windowStart.IsZero() {
		l.windowStart = now
	}
	l.windowSamples++
	if peak > l.windowMaxInFlight {
		l.windowMaxInFlight = peak
	}
	if congested {
		l.windowOverload = true
		l.windowOverloads++
	}
	burstThreshold := max(2, l.limit/8)
	windowElapsed := l.window == 0 || now.Sub(l.windowStart) >= l.window
	if !windowElapsed && !(l.windowOverloads >= burstThreshold && l.windowSamples >= burstThreshold) {
		return
	}
	if l.windowOverload {
		next := int(math.Floor(float64(l.limit) * l.backoffRatio))
		if next >= l.limit {
			next = l.limit - 1
		}
		l.limit = max(l.minLimit, next)
	} else if l.windowMaxInFlight >= max(1, l.limit/2) {
		l.limit = min(l.maxLimit, l.limit+1)
	}
	l.windowStart = now
	l.windowSamples = 0
	l.windowMaxInFlight = 0
	l.windowOverload = false
	l.windowOverloads = 0
}

func (l *SearchAdmission) dispatchLocked() {
	for l.inFlight < l.limit && len(l.waiters) > 0 {
		w := l.waiters[0]
		copy(l.waiters, l.waiters[1:])
		l.waiters[len(l.waiters)-1] = nil
		l.waiters = l.waiters[:len(l.waiters)-1]
		w.granted = true
		l.inFlight++
		close(w.ch)
	}
}

func (l *SearchAdmission) retryAfter() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.retryAfterLocked()
}

func (l *SearchAdmission) retryAfterLocked() int {
	sec := 1
	if l.smoothedRTT >= 500*time.Millisecond {
		sec = int(math.Ceil(l.smoothedRTT.Seconds() * 2))
	} else if l.limit < l.initialLimit/2 {
		sec = 2
	}
	return min(max(sec, 1), 5)
}
