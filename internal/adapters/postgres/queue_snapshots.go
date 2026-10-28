package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// QueueSnapshots decouples autoscaler observations from scans of document and
// operation scopes. Any API or worker process can refresh the shared snapshot;
// an advisory lock admits one refresher for the installation at a time.
type QueueSnapshots struct {
	Pool            *pgxpool.Pool
	RefreshInterval time.Duration
}

func (s QueueSnapshots) refreshInterval() time.Duration {
	if s.RefreshInterval == 0 {
		return workqueue.DefaultRefreshInterval
	}
	return s.RefreshInterval
}

// The first unresolved change bounds how far counters can lag. Reading this
// indexed cutoff in the same statement as the totals preserves that proof even
// if a producer commits or maintenance drains the ledger after publication.
const queueCountersCutoffSQL = `SELECT LEAST(statement_timestamp(),coalesce(
 (SELECT dirty_since FROM parallel_queue_observations
 WHERE revision<>applied_revision ORDER BY dirty_since LIMIT 1),statement_timestamp())) AS observed_at`

// Refresh publishes both queues atomically, at most once per configured
// interval. Bootstrap, expiry cleanup and scope discovery run independently.
// Incomplete initialization/due promotion preserves the last valid observation.
func (s QueueSnapshots) Refresh(ctx context.Context) error {
	if s.Pool == nil {
		return errors.New("workqueue: nil postgres pool")
	}
	tx, err := s.beginRefresh(ctx)
	if err != nil || tx == nil {
		return err
	}
	defer tx.Rollback(ctx)
	published, err := tx.Exec(ctx, `WITH cutoff AS MATERIALIZED (`+queueCountersCutoffSQL+`)
	 INSERT INTO queue_backlog_snapshots(queue,waiting,in_progress,oldest_waiting_age_seconds,ingestion_waiting,observed_at,ingestion_observed_at,counters_observed_at)
	 SELECT backlog.*,statement_timestamp(),statement_timestamp(),cutoff.observed_at FROM (`+queueBacklogSQL()+`) backlog CROSS JOIN cutoff
	 WHERE (SELECT initialized FROM queue_backlog_bootstrap WHERE singleton)
	 AND NOT EXISTS(SELECT FROM queue_scope_reconciliation WHERE kind='quarantine')
	 AND NOT EXISTS(SELECT FROM queue_backlog_documents WHERE NOT ready AND ready_at<=statement_timestamp() LIMIT 1)
	 AND cutoff.observed_at>statement_timestamp()-make_interval(secs => $1::double precision)
	 ON CONFLICT(queue) DO UPDATE SET waiting=EXCLUDED.waiting,in_progress=EXCLUDED.in_progress,
	 oldest_waiting_age_seconds=EXCLUDED.oldest_waiting_age_seconds,observed_at=EXCLUDED.observed_at,
	 ingestion_waiting=EXCLUDED.ingestion_waiting,ingestion_observed_at=EXCLUDED.ingestion_observed_at,
	 counters_observed_at=EXCLUDED.counters_observed_at`, max(time.Minute, 2*s.refreshInterval()).Seconds())
	if err != nil {
		return err
	}
	if published.RowsAffected() == 0 {
		return nil
	}
	// Stamp publication after the expensive query, while retaining its original
	// observation cutoff for oldest-age calculations and stale-data detection.
	if _, err = tx.Exec(ctx, `UPDATE queue_backlog_snapshots SET published_at=clock_timestamp() WHERE queue IN ('live','bulk')`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// beginRefresh elects a refresher only when publication is due.
func (s QueueSnapshots) beginRefresh(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('quivr.queue-backlog-snapshot.v1',0))`).Scan(&locked); err != nil || !locked {
		tx.Rollback(ctx)
		return nil, err
	}
	var fresh bool
	if err = tx.QueryRow(ctx, `SELECT count(*)=2 AND min(published_at)>clock_timestamp()-make_interval(secs => $1::double precision)
	      AND count(ingestion_waiting)=2 AND count(ingestion_observed_at)=2
	      AND min(ingestion_observed_at)>clock_timestamp()-make_interval(secs => $2::double precision)
	      AND min(counters_observed_at)>clock_timestamp()-make_interval(secs => $2::double precision)
	      FROM queue_backlog_snapshots WHERE queue IN ('live','bulk')`, s.refreshInterval().Seconds(), max(time.Minute, 2*s.refreshInterval()).Seconds()).Scan(&fresh); err != nil || fresh {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// QueueBacklog reads two rows, irrespective of corpus or operation size. Data
// older than two refresh intervals (at least one minute) is unavailable rather
// than falsely reported as zero.
func (s QueueSnapshots) QueueBacklog(ctx context.Context) ([]workqueue.Status, error) {
	if s.Pool == nil {
		return nil, errors.New("workqueue: nil postgres pool")
	}
	rows, err := s.Pool.Query(ctx, `SELECT queue,waiting,in_progress,
	 CASE WHEN waiting=0 THEN 0 ELSE oldest_waiting_age_seconds+GREATEST(0,EXTRACT(EPOCH FROM clock_timestamp()-observed_at)) END::double precision,
	     CASE WHEN ingestion_observed_at>clock_timestamp()-make_interval(secs => $1::double precision)
	       THEN ingestion_waiting END
	     FROM queue_backlog_snapshots WHERE queue IN ('live','bulk') AND observed_at>clock_timestamp()-make_interval(secs => $1::double precision)
	     AND counters_observed_at>clock_timestamp()-make_interval(secs => $1::double precision)
	 ORDER BY queue`, max(time.Minute, 2*s.refreshInterval()).Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (workqueue.Status, error) {
		var status workqueue.Status
		err := row.Scan(&status.Queue, &status.Waiting, &status.InProgress, &status.OldestAgeSeconds, &status.IngestionWaiting)
		return status, err
	})
	if err == nil && len(out) != 2 {
		err = errors.New("workqueue: queue snapshot unavailable")
	}
	return out, err
}
