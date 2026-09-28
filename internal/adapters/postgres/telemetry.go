package postgres

import (
	"context"
	"time"
)

// IngestionBacklog counts Receipts accepted but not yet materialized and the
// age of the oldest one. It reads only pending Receipts through a partial index.
func (s ContentStore) IngestionBacklog(ctx context.Context) (int64, time.Duration, error) {
	var pending int64
	var age float64
	err := s.Pool.QueryRow(ctx, `SELECT count(*),coalesce(extract(epoch FROM now()-min(accepted_at)),0)::double precision
FROM ingestion_receipts WHERE state='pending'`).Scan(&pending, &age)
	return pending, time.Duration(age * float64(time.Second)), err
}

// ReceiptAge is the time elapsed since one Receipt was accepted.
func (s ContentStore) ReceiptAge(ctx context.Context, org, receiptID string) (time.Duration, error) {
	var age float64
	err := s.Pool.QueryRow(ctx, `SELECT extract(epoch FROM now()-accepted_at)::double precision FROM ingestion_receipts WHERE organization=$1 AND id=$2`, org, receiptID).Scan(&age)
	return time.Duration(age * float64(time.Second)), err
}
