package postgres

import (
	"context"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

// IngestionBacklog counts Receipts accepted but not yet materialized and the
// age of the oldest one. It reads only pending Receipts through a partial index.
func (s MaterializationStore) IngestionBacklog(ctx context.Context) (int64, time.Duration, error) {
	var pending int64
	var age float64
	err := s.Pool.QueryRow(ctx, `SELECT count(*),coalesce(extract(epoch FROM now()-min(accepted_at)),0)::double precision
FROM ingestion_receipts WHERE state='pending'`).Scan(&pending, &age)
	return pending, time.Duration(age * float64(time.Second)), err
}

// ReceiptSteps reads the time elapsed since one Receipt was accepted and the
// pipeline step times of the Version it resolved to, by primary key. Steps
// stay nil while the Receipt has no Version.
func (s MaterializationStore) ReceiptSteps(ctx context.Context, org, receiptID string) (content.Steps, time.Duration, error) {
	var steps content.Steps
	var age float64
	err := s.Pool.QueryRow(ctx, `SELECT extract(epoch FROM now()-rc.accepted_at)::double precision,rc.accepted_at,v.materialized_at,v.segmented_at,v.retrieval_ready_at,v.enriched_at
FROM ingestion_receipts rc LEFT JOIN record_versions v ON v.organization=rc.organization AND v.id=rc.version_id
WHERE rc.organization=$1 AND rc.id=$2`, org, receiptID).Scan(&age, &steps.Accepted, &steps.Materialized, &steps.Segmented, &steps.RetrievalReady, &steps.Enriched)
	return steps, time.Duration(age * float64(time.Second)), err
}
