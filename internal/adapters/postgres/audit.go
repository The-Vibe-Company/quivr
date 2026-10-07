package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/audit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AuditStore struct{ Pool *pgxpool.Pool }
type auditTransactionKey struct{}
type auditTransaction struct {
	pool     *pgxpool.Pool
	tx       pgx.Tx
	deadlock *error
}

// database keeps ordinary/background calls on their pool and binds audited
// commands (including existing nested transactions) to the request transaction.
type transactionalDatabase interface {
	querier
	Begin(context.Context) (pgx.Tx, error)
}

func database(ctx context.Context, pool *pgxpool.Pool) transactionalDatabase {
	if current, ok := ctx.Value(auditTransactionKey{}).(auditTransaction); ok && current.pool == pool {
		return current.tx
	}
	return pool
}
func (s AuditStore) Record(ctx context.Context, e *audit.Event, command func(context.Context) error) error {
	initial := *e
	return retryJournalWrite(ctx, "Audit.Record", func(ctx context.Context) error {
		*e = initial
		return s.recordAttempt(ctx, e, command)
	})
}

func (s AuditStore) recordAttempt(ctx context.Context, e *audit.Event, command func(context.Context) error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	action, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	work, hooks := audit.WithCommitHooks(ctx)
	committed := false
	defer func() { hooks(committed) }()
	var deadlock error
	work = context.WithValue(work, auditTransactionKey{}, auditTransaction{s.Pool, action, &deadlock})
	err = command(work)
	// HTTP commands translate storage failures into buffered refusals. Preserve
	// a nested deadlock so the parent retries the command and audit atomically.
	if deadlock != nil {
		return deadlock
	}
	if err != nil {
		if errors.Is(err, audit.ErrReadOnly) {
			// Estimates may cache confirmation facts; commit those read-side writes
			// while excluding the estimate itself from the sensitive-action trail.
			if err = action.Commit(ctx); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		return err
	}
	if e.Outcome == "accepted" {
		err = action.Commit(ctx)
	} else {
		err = action.Rollback(ctx)
	}
	if err != nil {
		return err
	}
	detail, err := json.Marshal(e.Detail)
	if err != nil {
		return err
	}
	err = tx.QueryRow(ctx, `INSERT INTO audit_events(actor,action,target_type,target_id,organization,outcome,request_id,detail)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,occurred_at`, e.Actor, e.Action, e.TargetType, e.TargetID, e.Organization, e.Outcome, e.RequestID, detail).Scan(&e.ID, &e.Time)
	if err != nil {
		return err
	}
	err = tx.Commit(ctx)
	committed = err == nil && e.Outcome == "accepted"
	return err
}
func (s AuditStore) List(ctx context.Context, org string, f audit.Filter) ([]audit.Event, error) {
	if f.Limit < 1 || f.Limit > 201 {
		return nil, errors.New("invalid audit limit")
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,occurred_at,actor,action,target_type,target_id,organization,outcome,request_id,detail FROM audit_events
 WHERE organization=$1 AND ($2::timestamptz IS NULL OR occurred_at >= $2) AND ($3::timestamptz IS NULL OR occurred_at < $3)
 AND ($4='' OR actor=$4) AND ($5='' OR action=$5) AND ($6='' OR target_type=$6) AND ($7='' OR target_id=$7)
 AND ($8::bigint=0 OR id<$8) ORDER BY id DESC LIMIT $9`, org, auditTime(f.Since), auditTime(f.Until), f.Actor, f.Action, f.TargetType, f.TargetID, f.After, f.Limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (audit.Event, error) {
		var e audit.Event
		var detail []byte
		err := row.Scan(&e.ID, &e.Time, &e.Actor, &e.Action, &e.TargetType, &e.TargetID, &e.Organization, &e.Outcome, &e.RequestID, &detail)
		if err == nil {
			err = json.Unmarshal(detail, &e.Detail)
		}
		return e, err
	})
}
func auditTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
func (s AuditStore) PruneAudit(ctx context.Context, months, batch int) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT prune_audit_events($1,$2)`, months, batch).Scan(&n)
	return n, err
}
