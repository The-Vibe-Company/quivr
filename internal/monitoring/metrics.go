package monitoring

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
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
