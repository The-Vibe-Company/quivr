package postgres

import (
	"context"
	"errors"

	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// QueueSnapshots decouples autoscaler observations from scans of document and
// operation scopes. Any API or worker process can refresh the shared snapshot;
// an advisory lock admits one refresher for the installation at a time.
type QueueSnapshots struct{ Pool *pgxpool.Pool }

// Refresh publishes both queues atomically, at most once per second. Scans run
// outside request/scrape deadlines; a failed scan preserves the last good data.
func (s QueueSnapshots) Refresh(ctx context.Context) error {
	if s.Pool == nil {
		return errors.New("workqueue: nil postgres pool")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('quivr.queue-backlog-snapshot.v1',0))`).Scan(&locked); err != nil || !locked {
		return err
	}
	var fresh bool
	if err = tx.QueryRow(ctx, `SELECT count(*)=2 AND min(observed_at)>clock_timestamp()-interval '1 second' FROM queue_backlog_snapshots WHERE queue IN ('live','bulk')`).Scan(&fresh); err != nil || fresh {
		return err
	}
	// Unique fencing tokens allow bounded cleanup even while an old process
	// still holds an expired token. Ordinary completed attempts are deleted.
	if _, err = tx.Exec(ctx, `DELETE FROM queue_document_attempts WHERE (organization,kind,work_id,document_id) IN (
	 SELECT organization,kind,work_id,document_id FROM queue_document_attempts
	 WHERE lease_until<clock_timestamp() ORDER BY lease_until LIMIT 1000 FOR UPDATE SKIP LOCKED
	)`); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO queue_backlog_snapshots(queue,waiting,in_progress,oldest_waiting_age_seconds,observed_at)
	 SELECT backlog.*,statement_timestamp() FROM (`+queueBacklogSQL()+`) backlog
	 ON CONFLICT(queue) DO UPDATE SET waiting=EXCLUDED.waiting,in_progress=EXCLUDED.in_progress,
	 oldest_waiting_age_seconds=EXCLUDED.oldest_waiting_age_seconds,observed_at=EXCLUDED.observed_at`)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// QueueBacklog reads two rows, irrespective of corpus or operation size. Data
// older than one minute is unavailable rather than falsely reported as zero.
func (s QueueSnapshots) QueueBacklog(ctx context.Context) ([]workqueue.Status, error) {
	if s.Pool == nil {
		return nil, errors.New("workqueue: nil postgres pool")
	}
	rows, err := s.Pool.Query(ctx, `SELECT queue,waiting,in_progress,
	 CASE WHEN waiting=0 THEN 0 ELSE oldest_waiting_age_seconds+GREATEST(0,EXTRACT(EPOCH FROM clock_timestamp()-observed_at)) END::double precision
	 FROM queue_backlog_snapshots WHERE queue IN ('live','bulk') AND observed_at>clock_timestamp()-interval '1 minute'
	 ORDER BY queue`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (workqueue.Status, error) {
		var status workqueue.Status
		err := row.Scan(&status.Queue, &status.Waiting, &status.InProgress, &status.OldestAgeSeconds)
		return status, err
	})
	if err == nil && len(out) != 2 {
		err = errors.New("workqueue: queue snapshot unavailable")
	}
	return out, err
}
