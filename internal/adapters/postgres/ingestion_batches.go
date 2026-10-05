package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5"
)

// ClaimIngestionBatches transfers bounded, arrival-ordered receipt groups to
// durable workflow intents. A crash before commit leaves the original outbox;
// a crash after commit recovers the same group and workflow ID.
func (s MaterializationStore) ClaimIngestionBatches(ctx context.Context, limit int) ([]content.DispatchBatch, error) {
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
	var raw []byte
	var incoming time.Time
	err = tx.QueryRow(ctx, `SELECT enqueued_at FROM ingestion_outbox
WHERE NOT dispatched AND lease_until<now()
ORDER BY enqueued_at,organization,receipt_id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&incoming)
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
 SELECT id FROM ingestion_batches WHERE lease_until<now()
 AND enqueued_at <= COALESCE($1::timestamptz,'infinity'::timestamptz)
 ORDER BY enqueued_at,id FOR UPDATE SKIP LOCKED LIMIT 1
) UPDATE ingestion_batches b SET lease_until=now()+interval '5 seconds'
FROM pending p WHERE b.id=p.id RETURNING b.id,b.receipts`, before).Scan(&b.ID, &raw)
	if err == nil {
		if err = json.Unmarshal(raw, &b.Receipts); err != nil {
			return b, err
		}
		return b, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return b, err
	}
	rows, err := tx.Query(ctx, `WITH pending AS (
 SELECT organization,receipt_id FROM ingestion_outbox
 WHERE NOT dispatched AND lease_until<now()
 ORDER BY enqueued_at,organization,receipt_id FOR UPDATE SKIP LOCKED LIMIT 32
), moved AS (
 DELETE FROM ingestion_outbox o USING pending p
 WHERE (o.organization,o.receipt_id)=(p.organization,p.receipt_id)
 RETURNING o.organization,o.receipt_id,o.enqueued_at
) SELECT organization,receipt_id,enqueued_at FROM moved ORDER BY enqueued_at,organization,receipt_id`)
	if err != nil {
		return b, err
	}
	var oldest time.Time
	var identity []string
	for rows.Next() {
		var d content.Dispatch
		var arrived time.Time
		if err = rows.Scan(&d.Organization, &d.ReceiptID, &arrived); err != nil {
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
	b.ID = content.StableID("ingestion-batch-v1", identity...)
	raw, err = json.Marshal(b.Receipts)
	if err != nil {
		return b, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO ingestion_batches(id,receipts,enqueued_at,lease_until) VALUES($1,$2,$3,now()+interval '5 seconds')`, b.ID, raw, oldest); err != nil {
		return b, err
	}
	return b, tx.Commit(ctx)
}

// IngestionBatchDispatched acknowledges only after Temporal durably accepts
// the immutable workflow input. Receipt and journal audit facts are retained.
func (s MaterializationStore) IngestionBatchDispatched(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, "DELETE FROM ingestion_batches WHERE id=$1", id)
	return err
}
