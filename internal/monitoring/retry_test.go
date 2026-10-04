package monitoring_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/monitoring"
)

func TestRetryDelayIsJitteredExponentialCappedAtMax(t *testing.T) {
	for _, jitter := range []float64{0, 0.5, 0.999} {
		p := monitoring.RetryPolicy{Jitter: func() float64 { return jitter }}
		for n, step := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 9: 256 * time.Second, 10: 5 * time.Minute, 40: 5 * time.Minute} {
			d := p.Delay(n, monitoring.AttemptOutcome{Outcome: monitoring.AttemptRetryableError, HTTPStatus: 500})
			if d < step/2 || d > step {
				t.Fatalf("attempt %d jitter %v: delay %v outside [%v,%v]", n, jitter, d, step/2, step)
			}
		}
	}
	// Defaults are the accepted policy.
	p := monitoring.RetryPolicy{}.WithDefaults()
	if p.Initial != time.Second || p.Max != 5*time.Minute || p.Window != 24*time.Hour {
		t.Fatalf("defaults %+v", p)
	}
}

func TestRetryAfterOnlyFor429And503AndFlooredAtInitial(t *testing.T) {
	p := monitoring.RetryPolicy{Jitter: func() float64 { return 0 }}
	for _, c := range []struct {
		status int
		after  time.Duration
		want   time.Duration
	}{
		{429, 90 * time.Second, 90 * time.Second},
		{503, 2 * time.Hour, 2 * time.Hour}, // the window bound is applied by the store
		{503, 0, time.Second},               // floored at Initial
		{500, 90 * time.Second, 500 * time.Millisecond},
		{408, 90 * time.Second, 500 * time.Millisecond},
	} {
		if got := p.Delay(1, monitoring.AttemptOutcome{Outcome: monitoring.AttemptRetryableError, HTTPStatus: c.status, RetryAfter: c.after, HasRetryAfter: true}); got != c.want {
			t.Fatalf("status %d after %v: got %v want %v", c.status, c.after, got, c.want)
		}
	}
}

func TestParseRetryAfterAcceptsSecondsAndDates(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		value string
		want  time.Duration
		ok    bool
	}{
		{"15", 15 * time.Second, true},
		{"0", 0, true},
		{now.Add(2 * time.Minute).Format(http.TimeFormat), 2 * time.Minute, true},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0, true},
		{"-3", 0, false},
		{"soon", 0, false},
		{"", 0, false},
		{"1.5", 0, false},
	} {
		got, ok := monitoring.ParseRetryAfter(c.value, now)
		if got != c.want || ok != c.ok {
			t.Fatalf("%q: got %v %v want %v %v", c.value, got, ok, c.want, c.ok)
		}
	}
}
