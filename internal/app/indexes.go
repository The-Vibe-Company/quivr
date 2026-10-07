package app

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/logging"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IndexMaintenance owns optional performance-index recovery for a live process.
// PostgreSQL's catalog is the durable state; the local state lets probes report
// degradation without waiting for database DDL or a catalog query.
type IndexMaintenance struct {
	Pool  *pgxpool.Pool
	state atomic.Value
}

// State is pending until Run starts, building during an attempt, retrying or
// conflict after a failure, and ready after all definitions are valid.
func (m *IndexMaintenance) State() string {
	if state := m.state.Load(); state != nil {
		return state.(string)
	}
	return "pending"
}

// Run retries until every index is valid or the lifecycle starts draining.
// Each attempt has its own bounded DDL budget in EnsureIndexes. Errors are
// classified without logging raw database diagnostics, credentials or data.
func (m *IndexMaintenance) Run(ctx context.Context) {
	delay := time.Second
	for attempt := 1; ctx.Err() == nil; attempt++ {
		m.state.Store("building")
		slog.InfoContext(ctx, "performance index setup started",
			"event", logging.Diagnostic("quivr.postgres.index_setup"),
			"state", logging.Diagnostic("building"), "attempt", attempt)
		err := postgres.EnsureIndexes(ctx, m.Pool)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			m.state.Store("ready")
			slog.InfoContext(ctx, "performance indexes ready",
				"event", logging.Diagnostic("quivr.postgres.index_setup"),
				"state", logging.Diagnostic("ready"), "attempt", attempt)
			return
		}
		state, code := "retrying", "index_setup_failed"
		switch {
		case errors.Is(err, postgres.ErrIndexConflict):
			state, code = "conflict", "index_definition_conflict"
		case errors.Is(err, postgres.ErrIndexBusy):
			code = "index_setup_busy"
		}
		m.state.Store(state)
		slog.WarnContext(ctx, "performance indexes degraded; background setup will retry",
			"event", logging.Diagnostic("quivr.postgres.index_setup"),
			"state", logging.Diagnostic(state), "error_code", logging.Diagnostic(code),
			"attempt", attempt, "retry_in_seconds", delay.Seconds())
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(2*delay, time.Minute)
	}
}
