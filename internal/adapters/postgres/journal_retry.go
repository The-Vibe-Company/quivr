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

const (
	// defaultJournalRetryMaxAttempts preserves the 3-attempt bound for existing
	// callers (including AuditStore.Record outer transactions) without keying
	// behavior off operation label strings.
	defaultJournalRetryMaxAttempts = 3
	// acceptJournalRetryMaxAttempts bounds 40P01 deadlock retries on submission
	// writes (1 initial attempt + 7 retries).
	acceptJournalRetryMaxAttempts = 8
	journalRetryBaseBackoff       = 5 * time.Millisecond
	journalRetryMaxBackoff        = 250 * time.Millisecond
)

// journalRetryBackoff returns a bounded, jittered exponential backoff delay in
// [1ms, min(journalRetryBaseBackoff << (attempt-1), journalRetryMaxBackoff)].
func journalRetryBackoff(attempt int) time.Duration {
	shift := max(0, min(attempt-1, 6))
	capDelay := min(journalRetryBaseBackoff<<shift, journalRetryMaxBackoff)
	if capDelay <= time.Millisecond {
		return time.Millisecond
	}
	jitterRange := int64(capDelay - time.Millisecond)
	return time.Millisecond + time.Duration(rand.Int64N(jitterRange+1))
}

// Retry only a PostgreSQL deadlock, which guarantees the transaction did not
// commit. Connection errors and ambiguous commits must escape to the caller.
// An audited command retries at its parent, never inside an aborted savepoint.
func retryJournalWrite(ctx context.Context, operation string, write func(context.Context) error) error {
	return retryJournalWriteWithLimit(ctx, operation, defaultJournalRetryMaxAttempts, write)
}

func retryJournalWriteWithLimit(ctx context.Context, operation string, maxAttempts int, write func(context.Context) error) error {
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
	maxAttempts = max(1, maxAttempts)
	work := context.WithValue(ctx, journalRetryKey{}, true)
	for attempt := 1; ; attempt++ {
		err := write(work)
		if !isDeadlock(err) {
			return err
		}
		exhausted := attempt >= maxAttempts
		slog.WarnContext(ctx, "journal transaction deadlock", "event", "quivr.postgres.journal_deadlock", "operation", operation, "attempt", attempt, "retry_count", attempt-1, "retry_exhausted", exhausted)
		if exhausted {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		timer := time.NewTimer(journalRetryBackoff(attempt))
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
