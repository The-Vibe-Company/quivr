package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
)

// schemaWait bounds how long startup waits for pending migrations (THE-806):
// the check runs again after First, then after twice as long each time, up to
// Max, until Limit has passed. A zero Limit checks once.
type schemaWait struct {
	Limit, First, Max time.Duration
}

// defaultMigrationWait is the worker's default migration_wait: Railway's
// default healthcheck window.
const defaultMigrationWait = 5 * time.Minute

// awaitSchema returns once check passes. While the schema only lacks
// migrations (postgres.ErrMigrationsPending), which the api applies before it
// serves, it waits and logs once. Any other failure, such as a schema newer
// than this binary, is returned at once, and a stop returns ctx.Err(). The
// probe port opens only after this returns, so /readyz stays unready
// meanwhile.
func awaitSchema(ctx context.Context, check func(context.Context) error, wait schemaWait) error {
	deadline := time.Now().Add(wait.Limit)
	delay, waited := wait.First, false
	for {
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := check(attempt)
		cancel()
		switch {
		case err == nil:
			if waited {
				slog.Info("migrations applied; starting")
			}
			return nil
		case ctx.Err() != nil:
			return ctx.Err()
		}
		remaining := time.Until(deadline)
		if !errors.Is(err, postgres.ErrMigrationsPending) || remaining <= 0 {
			slog.Error("schema readiness failed", "error", err)
			if errors.Is(err, postgres.ErrSchemaNewer) {
				return errors.New("database schema is newer than this binary; deploy a binary that embeds its migrations")
			}
			return errors.New("database/schema unavailable; run migrate")
		}
		if !waited {
			slog.Info("waiting for migrations", "error", err, "limit", wait.Limit.String())
			waited = true
		}
		// The last check runs at the deadline.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(min(delay, remaining)):
		}
		delay = min(2*delay, wait.Max)
	}
}
