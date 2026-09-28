package monitoring

import (
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// RetryPolicy schedules automatic delivery retries. The accepted defaults are
// a one-second initial delay doubling up to five minutes, jittered, within a
// 24-hour delivery window counted from the Delivery's creation.
type RetryPolicy struct {
	Initial, Max, Window time.Duration
	// Jitter returns a value in [0,1); nil uses math/rand.
	Jitter func() float64
}

// WithDefaults fills unset durations with the accepted defaults.
func (p RetryPolicy) WithDefaults() RetryPolicy {
	if p.Initial <= 0 {
		p.Initial = time.Second
	}
	if p.Max <= 0 {
		p.Max = 5 * time.Minute
	}
	if p.Window <= 0 {
		p.Window = 24 * time.Hour
	}
	return p
}

// Delay returns how long to wait after failed attempt number n before the
// next one. The step min(Initial·2^(n-1), Max) is equally jittered into
// [step/2, step]. A valid Retry-After on 429 or 503 replaces it, floored at
// Initial. The store bounds the result by the remaining delivery window.
func (p RetryPolicy) Delay(n int, o AttemptOutcome) time.Duration {
	p = p.WithDefaults()
	if o.HasRetryAfter && (o.HTTPStatus == http.StatusTooManyRequests || o.HTTPStatus == http.StatusServiceUnavailable) {
		return max(o.RetryAfter, p.Initial)
	}
	step := p.Initial
	for i := 1; i < n && step < p.Max; i++ {
		step *= 2
	}
	step = min(step, p.Max)
	jitter := rand.Float64
	if p.Jitter != nil {
		jitter = p.Jitter
	}
	return step/2 + time.Duration(jitter()*float64(step/2))
}

// ParseRetryAfter reads a Retry-After header: non-negative delta-seconds or an
// HTTP-date (a past date means no wait). ok is false for anything else.
func ParseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	if value == "" {
		return 0, false
	}
	if s, err := strconv.ParseInt(value, 10, 64); err == nil {
		if s < 0 || s > int64(365*24*time.Hour/time.Second) {
			return 0, false
		}
		return time.Duration(s) * time.Second, true
	}
	at, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	return max(at.Sub(now), 0), true
}
