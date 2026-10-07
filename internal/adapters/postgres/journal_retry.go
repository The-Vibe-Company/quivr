package postgres

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type journalRetryKey struct{}

// Retry only a PostgreSQL deadlock, which guarantees the transaction did not
// commit. Connection errors and ambiguous commits must escape to the caller.
// An audited command retries at its parent, never inside an aborted savepoint.
func retryJournalWrite(ctx context.Context, operation string, write func(context.Context) error) error {
	if current, ok := ctx.Value(auditTransactionKey{}).(auditTransaction); ok {
		err := write(ctx)
		if isDeadlock(err) {
			*current.deadlock = err
		}
		return err
	}
	if ctx.Value(journalRetryKey{}) != nil {
		return write(ctx)
	}
	work := context.WithValue(ctx, journalRetryKey{}, true)
	for attempt := 1; ; attempt++ {
		err := write(work)
		if !isDeadlock(err) {
			return err
		}
		slog.WarnContext(ctx, "journal transaction deadlock", "event", "quivr.postgres.journal_deadlock", "operation", operation, "attempt", attempt, "retry_count", attempt-1, "retry_exhausted", attempt == 3)
		if attempt == 3 {
			return err
		}
		timer := time.NewTimer(time.Duration(1+rand.IntN(10*attempt)) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func isDeadlock(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40P01"
}
