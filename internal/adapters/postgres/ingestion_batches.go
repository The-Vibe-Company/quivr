package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

// ClaimIngestionBatches transfers bounded, arrival-ordered receipt groups to
// durable workflow intents. A crash before commit leaves the original outbox;
// a crash after commit recovers the same group and workflow ID.
func (s MaterializationStore) ClaimIngestionBatches(ctx context.Context, limit int) ([]content.DispatchBatch, error) {
	if _, selected := workqueue.Selected(ctx); !selected {
		live, err := s.ClaimIngestionBatches(workqueue.WithClass(ctx, workqueue.Live), limit)
		if err != nil || len(live) >= limit {
			return live, err
		}
		bulk, err := s.ClaimIngestionBatches(workqueue.WithClass(ctx, workqueue.Bulk), limit-len(live))
		return append(live, bulk...), err
	}
	var out []content.DispatchBatch
	for range limit {
		b, err := s.claimIngestionBatch(ctx)
		if err != nil {
			return out, err
		}
		if b.ID == "" {
			break
		}
		out = append(out, b)
		if len(b.Receipts) < 32 {
			// The next poll collects later arrivals; this claim does not chase
			// them into additional tiny workflows while acceptance is ongoing.
			break
		}
	}
	return out, nil
}

func (s MaterializationStore) claimIngestionBatch(ctx context.Context) (content.DispatchBatch, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return content.DispatchBatch{}, err
	}
	defer tx.Rollback(ctx)
	var b content.DispatchBatch
	q, _ := workqueue.Selected(ctx)
	outbox, batches := "ingestion_outbox", "ingestion_batches"
	if q == workqueue.Bulk {
		outbox, batches = "bulk_ingestion_outbox", "bulk_ingestion_batches"
	}
	var raw []byte
	var incoming time.Time
	err = tx.QueryRow(ctx, `SELECT enqueued_at FROM `+outbox+`
WHERE work_queue=$1 AND NOT dispatched
ORDER BY enqueued_at,organization,receipt_id FOR UPDATE SKIP LOCKED LIMIT 1`, q).Scan(&incoming)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return b, err
	}
	var before any
	if !incoming.IsZero() {
		before = incoming
	}
	// Retry persisted groups before handing off newer arrivals. SKIP LOCKED
	// and leases let other dispatchers keep progressing independently.
	err = tx.QueryRow(ctx, `WITH pending AS (
 SELECT id FROM `+batches+` WHERE lease_until<now() AND work_queue=$2
 AND enqueued_at <= COALESCE($1::timestamptz,'infinity'::timestamptz)
 ORDER BY enqueued_at,id FOR UPDATE SKIP LOCKED LIMIT 1
) UPDATE `+batches+` b SET lease_until=now()+interval '5 seconds'
FROM pending p WHERE b.id=p.id RETURNING b.id,b.receipts,b.work_queue`, before, q).Scan(&b.ID, &raw, &b.WorkQueue)
	if err == nil {
		if err = json.Unmarshal(raw, &b.Receipts); err != nil {
			return b, err
		}
		return b, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return b, err
	}
	rows, err := tx.Query(ctx, `WITH pending AS MATERIALIZED (
 SELECT organization,receipt_id FROM `+outbox+`
 WHERE work_queue=$1 AND NOT dispatched
 ORDER BY enqueued_at,organization,receipt_id FOR UPDATE SKIP LOCKED LIMIT 32
), moved AS (
 DELETE FROM `+outbox+` o USING pending p
 WHERE (o.organization,o.receipt_id)=(p.organization,p.receipt_id)
 RETURNING o.organization,o.receipt_id,o.enqueued_at,o.trace_context
) SELECT organization,receipt_id,enqueued_at,trace_context FROM moved ORDER BY enqueued_at,organization,receipt_id`, q)
	if err != nil {
		return b, err
	}
	var oldest time.Time
	var identity []string
	for rows.Next() {
		var d content.Dispatch
		var arrived time.Time
		if err = rows.Scan(&d.Organization, &d.ReceiptID, &arrived, &d.TraceContext); err != nil {
			rows.Close()
			return b, err
		}
		if oldest.IsZero() {
			oldest = arrived
		}
		b.Receipts = append(b.Receipts, d)
		identity = append(identity, d.Organization, d.ReceiptID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return b, err
	}
	if len(b.Receipts) == 0 {
		return b, tx.Commit(ctx)
	}
	b.ID, b.WorkQueue = content.StableID("ingestion-batch-v1", identity...), q
	raw, err = json.Marshal(b.Receipts)
	if err != nil {
		return b, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO `+batches+`(id,receipts,enqueued_at,lease_until,work_queue) VALUES($1,$2,$3,now()+interval '5 seconds',$4)`, b.ID, raw, oldest, b.WorkQueue); err != nil {
		return b, err
	}
	return b, tx.Commit(ctx)
}

// IngestionBatchDispatched acknowledges only after Temporal durably accepts
// the immutable workflow input. Receipt and journal audit facts are retained.
func (s MaterializationStore) IngestionBatchDispatched(ctx context.Context, id string) error {
	q, selected := workqueue.Selected(ctx)
	if !selected {
		_, err := s.Pool.Exec(ctx, `WITH live AS (DELETE FROM ingestion_batches WHERE id=$1) DELETE FROM bulk_ingestion_batches WHERE id=$1`, id)
		return err
	}
	batches := "ingestion_batches"
	if q == workqueue.Bulk {
		batches = "bulk_ingestion_batches"
	}
	_, err := s.Pool.Exec(ctx, "DELETE FROM "+batches+" WHERE id=$1", id)
	return err
}
