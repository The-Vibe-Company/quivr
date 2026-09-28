package monitoring

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/telemetry"
)

// DeliveryBacklog is the admissible scheduled delivery work: Deliveries that
// are pending or delivering with work due or waiting for a retry. Work parked
// because admission refused it (disabled, withdrawn, destination unavailable)
// is not stuck work and is excluded.
type DeliveryBacklog struct {
	Pending   int64
	OldestAge time.Duration
}

// attemptOutcomes is the bounded label set of the attempt counter: the
// outcomes a request can have. An unknown outcome is inferred from a lost
// lease, never observed by a request, and is read from attempt history.
var attemptOutcomes = []string{AttemptAcknowledged, AttemptRetryableError, AttemptPermanentError}

// DeliveryMetrics counts the outcomes of webhook requests this process sent.
// Its zero value is ready; a nil receiver ignores observations.
type DeliveryMetrics struct {
	counts [3]atomic.Int64
	// Extra renders further process metrics on the same endpoint (processing, THE-662).
	Extra    func(io.Writer)
	once     sync.Once
	duration *telemetry.Histogram
}

func (m *DeliveryMetrics) histogram() *telemetry.Histogram {
	m.once.Do(func() {
		m.duration = telemetry.NewHistogram("quivr_delivery_request_duration_seconds", "Duration of webhook delivery requests with a known outcome.", 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30)
	})
	return m.duration
}

// ObserveDuration records one request's duration; a nil receiver ignores it.
func (m *DeliveryMetrics) ObserveDuration(d time.Duration) {
	if m != nil {
		m.histogram().Observe(d)
	}
}

// Observe counts one request outcome; other labels are dropped.
func (m *DeliveryMetrics) Observe(outcome string) {
	if m == nil {
		return
	}
	for i, o := range attemptOutcomes {
		if o == outcome {
			m.counts[i].Add(1)
		}
	}
}

// Handler serves the counters and backlog gauges in the Prometheus text
// format. A backlog read failure omits the gauges rather than reporting zero.
func (m *DeliveryMetrics) Handler(backlog func(context.Context) (DeliveryBacklog, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprintln(w, "# HELP quivr_delivery_attempts_total Outcomes of webhook delivery requests sent by this process.")
		fmt.Fprintln(w, "# TYPE quivr_delivery_attempts_total counter")
		for i, o := range attemptOutcomes {
			fmt.Fprintf(w, "quivr_delivery_attempts_total{outcome=%q} %d\n", o, m.counts[i].Load())
		}
		m.histogram().Write(w)
		if m.Extra != nil {
			m.Extra(w)
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		b, err := backlog(ctx)
		if err != nil {
			return
		}
		fmt.Fprintln(w, "# HELP quivr_delivery_pending Admissible Deliveries awaiting an attempt or a retry.")
		fmt.Fprintln(w, "# TYPE quivr_delivery_pending gauge")
		fmt.Fprintf(w, "quivr_delivery_pending %d\n", b.Pending)
		fmt.Fprintln(w, "# HELP quivr_delivery_oldest_pending_age_seconds Age of the oldest admissible pending Delivery.")
		fmt.Fprintln(w, "# TYPE quivr_delivery_oldest_pending_age_seconds gauge")
		fmt.Fprintf(w, "quivr_delivery_oldest_pending_age_seconds %g\n", b.OldestAge.Seconds())
	})
}
