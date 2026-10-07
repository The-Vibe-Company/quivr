package app

import (
	"context"
	"fmt"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"net/http"
	"time"
)

// Both process types expose the same installation-wide snapshot. A failed
// database scrape returns 503; reporting a false zero could scale work to zero.
func queueMetrics(next http.Handler, reader workqueue.Reader) http.Handler {
	names := []string{"quivr_queue_waiting_documents", "quivr_queue_in_progress_documents", "quivr_queue_oldest_waiting_age_seconds"}
	help := []string{"Documents waiting for processing slots.", "Documents being processed by live attempts.", "Age of the oldest waiting document in seconds."}
	if telemetry.Enabled() {
		meter := otel.Meter("quivr.workqueue")
		gauges := make([]metric.Float64ObservableGauge, 3)
		observables := make([]metric.Observable, 3)
		for i, name := range names {
			g, err := meter.Float64ObservableGauge(name, metric.WithDescription(help[i]))
			if err != nil {
				otel.Handle(err)
				break
			}
			gauges[i] = g
			observables[i] = g
		}
		if gauges[2] != nil {
			_, err := meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
				read, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				rows, err := reader.QueueBacklog(read)
				if err != nil {
					return nil
				}
				for _, row := range rows {
					values := []float64{float64(row.Waiting), float64(row.InProgress), row.OldestAgeSeconds}
					for i, value := range values {
						o.ObserveFloat64(gauges[i], value, metric.WithAttributes(attribute.String("queue", row.Queue)))
					}
				}
				return nil
			}, observables...)
			if err != nil {
				otel.Handle(err)
			}
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		read, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		rows, err := reader.QueueBacklog(read)
		if err != nil {
			http.Error(w, "queue backlog unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		for i, name := range names {
			fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", name, help[i], name)
		}
		for _, row := range rows {
			values := []float64{float64(row.Waiting), float64(row.InProgress), row.OldestAgeSeconds}
			for i, value := range values {
				fmt.Fprintf(w, "%s{queue=%q} %g\n", names[i], row.Queue, value)
			}
		}
		next.ServeHTTP(w, r)
	})
}
