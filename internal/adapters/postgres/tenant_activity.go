package postgres

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
	"github.com/jackc/pgx/v5"
)

// RunTenantActivity reconciles a committed archive or unarchive even after a
// process restart or Weaviate outage. Any worker can drain the queue.
func (s Store) RunTenantActivity(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		work, ok := lifecycle.Admit(ctx)
		if !ok {
			return
		}
		attempt, cancel := context.WithTimeout(work, 5*time.Second)
		more, err := s.ReconcileTenantActivity(attempt)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("tenant activity update failed; will retry", "reason", err.Error())
		}
		if more {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ReconcileTenantActivity applies one queued transition. A shared projection
// fence keeps the serving route stable. Locking the corpus row serializes
// with archive, unarchive and version maintenance.
func (s Store) ReconcileTenantActivity(ctx context.Context) (bool, error) {
	if s.Projection == nil {
		return false, errors.New("tenant activity projection unavailable")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, projectionRoutingLock); err != nil {
		return false, err
	}

	var org, corpusID string
	var archived bool
	err = tx.QueryRow(ctx, `SELECT c.organization,c.id,c.archived
 FROM projection_tenant_activity a JOIN corpora c ON (c.organization,c.id)=(a.organization,a.corpus_id)
 WHERE a.due_at<=now() ORDER BY a.due_at,a.organization,a.corpus_id
 LIMIT 1 FOR NO KEY UPDATE OF c SKIP LOCKED`).Scan(&org, &corpusID, &archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	collection, generation, err := corpusProjectionRoute(ctx, tx, org, corpusID)
	if err != nil {
		return false, err
	}

	projectErr := s.Projection.SetGenerationActive(ctx, collection, org, corpusID, generation, !archived)
	checkpoint, cancel := lifecycle.CleanupContext(ctx, 2*time.Second)
	defer cancel()
	statement := `DELETE FROM projection_tenant_activity WHERE organization=$1 AND corpus_id=$2`
	if projectErr != nil {
		statement = `UPDATE projection_tenant_activity SET due_at=now()+interval '10 seconds' WHERE organization=$1 AND corpus_id=$2`
	}
	if _, err := tx.Exec(checkpoint, statement, org, corpusID); err != nil {
		return false, errors.Join(projectErr, err)
	}
	if err := tx.Commit(checkpoint); err != nil {
		return false, errors.Join(projectErr, err)
	}
	return true, projectErr
}
