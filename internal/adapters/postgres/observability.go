package postgres

import (
	"context"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/observability"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ObservabilityStore keeps the observability rollups (THE-795).
type ObservabilityStore struct{ Pool *pgxpool.Pool }

var _ observability.Store = ObservabilityStore{}

// maxRollupRows bounds one read; the tiers keep a window near a hundred
// buckets per key, so it is only a guard.
const maxRollupRows = 20000

// UpsertRollups adds every row to its stored bucket in one transaction. The
// recorder sorts rows by primary key, so concurrent flushes of the api and
// the worker lock rows in the same order. The later error wins.
func (s ObservabilityStore) UpsertRollups(ctx context.Context, rows []observability.Row) error {
	batch := &pgx.Batch{}
	for _, r := range rows {
		var code *string
		var at *time.Time
		if !r.LastErrorAt.IsZero() {
			code, at = &r.LastErrorCode, &r.LastErrorAt
		}
		batch.Queue(`INSERT INTO observability_rollups AS r
  (organization,series,resolution_s,bucket_start,key,count,errors,items_sum,duration_sum_ms,buckets,last_error_code,last_error_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
ON CONFLICT (organization,series,resolution_s,bucket_start,key) DO UPDATE SET
  count=r.count+excluded.count,
  errors=r.errors+excluded.errors,
  items_sum=r.items_sum+excluded.items_sum,
  duration_sum_ms=r.duration_sum_ms+excluded.duration_sum_ms,
  buckets=ARRAY(SELECT coalesce(a,0)+coalesce(b,0) FROM unnest(r.buckets,excluded.buckets) WITH ORDINALITY AS t(a,b,i) ORDER BY i),
  last_error_code=CASE WHEN excluded.last_error_at IS NOT NULL AND (r.last_error_at IS NULL OR excluded.last_error_at>=r.last_error_at)
    THEN excluded.last_error_code ELSE r.last_error_code END,
  last_error_at=greatest(r.last_error_at,excluded.last_error_at)`,
			r.Organization, r.Series, int(r.Resolution/time.Second), r.Start, r.Key, r.Count, r.Errors, r.Items, r.DurationSumMS, r.Buckets[:], code, at)
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error { return tx.SendBatch(ctx, batch).Close() })
}

// PruneRollups deletes the rows of one resolution that start before before.
func (s ObservabilityStore) PruneRollups(ctx context.Context, resolution time.Duration, before time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM observability_rollups WHERE resolution_s=$1 AND bucket_start<$2`, int(resolution/time.Second), before)
	return tag.RowsAffected(), err
}

// ReadRollups lists one Organization's rows of a series and resolution from
// from on, by key then bucket.
func (s ObservabilityStore) ReadRollups(ctx context.Context, org, series string, resolution time.Duration, from time.Time) ([]observability.Row, error) {
	rows, err := s.Pool.Query(ctx, `SELECT key,bucket_start,count,errors,items_sum,duration_sum_ms,buckets,coalesce(last_error_code,''),last_error_at
FROM observability_rollups WHERE organization=$1 AND series=$2 AND resolution_s=$3 AND bucket_start>=$4
ORDER BY key,bucket_start LIMIT $5`, org, series, int(resolution/time.Second), from, maxRollupRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []observability.Row{}
	for rows.Next() {
		r := observability.Row{Organization: org, Series: series, Resolution: resolution}
		var buckets []int64
		var at *time.Time
		if err := rows.Scan(&r.Key, &r.Start, &r.Count, &r.Errors, &r.Items, &r.DurationSumMS, &buckets, &r.LastErrorCode, &at); err != nil {
			return nil, err
		}
		copy(r.Buckets[:], buckets)
		if at != nil {
			r.LastErrorAt = at.UTC()
		}
		r.Start = r.Start.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// TopKeys sums one series per key over the rows from from on, largest first.
func (s ObservabilityStore) TopKeys(ctx context.Context, org, series string, resolution time.Duration, from time.Time, limit int) ([]observability.KeyCount, error) {
	rows, err := s.Pool.Query(ctx, `SELECT key,sum(count)::bigint FROM observability_rollups
WHERE organization=$1 AND series=$2 AND resolution_s=$3 AND bucket_start>=$4
GROUP BY key ORDER BY sum(count) DESC,key LIMIT $5`, org, series, int(resolution/time.Second), from, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []observability.KeyCount{}
	for rows.Next() {
		var k observability.KeyCount
		if err := rows.Scan(&k.Key, &k.Count); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
