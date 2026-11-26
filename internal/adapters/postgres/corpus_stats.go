package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/visibility"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CorpusStatsStore struct{ Pool *pgxpool.Pool }

var _ content.CorpusStatsReader = CorpusStatsStore{}

// RecordCount keeps exact bounded catalog semantics. A large unbounded query
// cannot count the corpus: its only canonical read is a capped membership probe.
func (s CorpusStatsStore) RecordCount(ctx context.Context, org, id string, q content.RecordQuery) (out content.RecordCountResult, err error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var header content.CorpusStats
	if _, enforced := visibility.Principals(ctx); enforced {
		header, err = statsCorpus(ctx, tx, org, id)
	} else {
		header, err = statsHeader(ctx, tx, org, id)
	}
	if err != nil {
		return out, err
	}
	out = content.RecordCountResult{ObservedAt: header.ObservedAt, Complete: true}
	q.CorpusIDs = []string{id}
	args := []any{org, id}
	where := catalogRange(q, &args)
	acl := visibilityPredicate(ctx, &args, "records")
	where += acl
	sql := `SELECT count(*) FROM records WHERE ` + where
	if acl == "" && q.AcceptedAfter == nil && q.AcceptedBefore == nil {
		sql = `SELECT count(*) FROM (SELECT 1 FROM records WHERE ` + where + ` LIMIT 10001) capped`
	}

	// Apply the size budget only to the count statement, on the server. A
	// shorter lock budget produces 55P03 instead of disguising infrastructure
	// contention as a large window. Connection/network waits retain caller ctx.
	if _, err = tx.Exec(ctx, `SELECT set_config('statement_timeout','2s',true),set_config('lock_timeout','500ms',true)`); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, sql, args...).Scan(&out.Count); err != nil {
		return out, recordCountQueryError(ctx, err)
	}

	if acl == "" && q.AcceptedAfter == nil && q.AcceptedBefore == nil && out.Count > 10000 {
		out.Count = header.CatalogTotal
		out.Approximate = true
		out.Complete = header.Complete
	}
	err = tx.Commit(ctx)
	return out, err
}

// Classify only the count statement's own server execution timeout. Pool,
// header, commit, lock and connection failures keep their retryable mapping;
// caller cancellation and operator cancellation are not size failures.
func recordCountQueryError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var server *pgconn.PgError
	if errors.As(err, &server) && server.Code == "57014" && server.Message == "canceling statement due to statement timeout" {
		return content.ErrCountTooBroad
	}
	return err
}

// Bootstrap visits one bounded page in one corpus. Canonical writers never
// take this progress lock, and maintain their own observations immediately.
// Call only after all canonical writers have been upgraded.
func (s CorpusStatsStore) Bootstrap(ctx context.Context, limit int) (bool, error) {
	if limit < 1 || limit > 1000 {
		return false, content.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	_, err = tx.Exec(ctx, `INSERT INTO corpus_record_bootstrap(organization,corpus_id)
 SELECT c.organization,c.id FROM corpora c WHERE NOT EXISTS(SELECT FROM corpus_record_bootstrap b WHERE (b.organization,b.corpus_id)=(c.organization,c.id))
 ORDER BY c.organization,c.id LIMIT 100 ON CONFLICT DO NOTHING`)
	if err != nil {
		return false, err
	}
	var org, id, after string
	err = tx.QueryRow(ctx, `SELECT organization,corpus_id,after_id FROM corpus_record_bootstrap WHERE NOT complete ORDER BY organization,corpus_id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&org, &id, &after)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM records WHERE organization=$1 AND corpus_id=$2 AND id>$3 COLLATE "C" ORDER BY id COLLATE "C" LIMIT $4`, org, id, after, limit)
	if err != nil {
		return false, err
	}
	var orgs, ids []string
	for rows.Next() {
		var record string
		if err = rows.Scan(&record); err != nil {
			rows.Close()
			return false, err
		}
		orgs = append(orgs, org)
		ids = append(ids, record)
		after = record
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return false, err
	}
	if len(ids) > 0 {
		batch := &pgx.Batch{}
		queueCorpusRecordObservations(batch, orgs, ids)
		if err = tx.SendBatch(ctx, batch).Close(); err != nil {
			return false, err
		}
	}
	complete := len(ids) < limit
	if _, err = tx.Exec(ctx, `UPDATE corpus_record_bootstrap SET after_id=$3,complete=$4 WHERE organization=$1 AND corpus_id=$2`, org, id, after, complete); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func statsCorpus(ctx context.Context, tx pgx.Tx, org, id string) (content.CorpusStats, error) {
	out := content.CorpusStats{CorpusID: id}
	var archived bool
	err := tx.QueryRow(ctx, `SELECT c.archived,coalesce(b.complete,false),statement_timestamp() FROM corpora c LEFT JOIN corpus_record_bootstrap b ON (b.organization,b.corpus_id)=(c.organization,c.id) WHERE c.organization=$1 AND c.id=$2`, org, id).Scan(&archived, &out.Complete, &out.ObservedAt)
	if err != nil {
		return out, notFound(err)
	}
	if archived {
		return out, corpus.ErrArchived
	}
	return out, nil
}

func statsHeader(ctx context.Context, tx pgx.Tx, org, id string) (content.CorpusStats, error) {
	out, err := statsCorpus(ctx, tx, org, id)
	if err != nil {
		return out, err
	}
	out.Approximate = !out.Complete
	err = tx.QueryRow(ctx, `SELECT coalesce(sum(eligible),0),coalesce(sum(catalog),0),coalesce(sum(undated),0),coalesce(sum(catalog_undated),0),
 (SELECT hour FROM corpus_record_hours WHERE organization=$1 AND corpus_id=$2 ORDER BY hour,shard LIMIT 1),
 (SELECT hour FROM corpus_record_hours WHERE organization=$1 AND corpus_id=$2 ORDER BY hour DESC,shard DESC LIMIT 1)
 FROM corpus_record_totals WHERE organization=$1 AND corpus_id=$2`, org, id).Scan(&out.Total, &out.CatalogTotal, &out.UndatedTotal, &out.CatalogUndatedTotal, &out.FirstCatalogHour, &out.LastCatalogHour)
	return out, err
}

func (s CorpusStatsStore) CorpusStats(ctx context.Context, org, id string, q content.CorpusStatsQuery) (content.CorpusStats, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return content.CorpusStats{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	out, err := statsHeaderForCaller(ctx, tx, org, id)
	if err != nil {
		return out, err
	}
	if q.Histogram {
		resolution := 24 * time.Hour
		unit := "day"
		if q.Hourly {
			resolution = time.Hour
			unit = "hour"
		}
		from, to := out.ObservedAt.UTC().Truncate(resolution), out.ObservedAt.UTC().Truncate(resolution)
		if out.FirstCatalogHour != nil {
			from = out.FirstCatalogHour.UTC().Truncate(resolution)
		}
		if out.LastCatalogHour != nil {
			to = out.LastCatalogHour.UTC().Truncate(resolution).Add(resolution)
		}
		if q.From != nil {
			from = q.From.UTC()
		}
		if q.To != nil {
			to = q.To.UTC()
		}
		if q.HistoryAfter != nil {
			from = q.HistoryAfter.UTC()
		}
		// A page covers at most 10,000 consecutive buckets, even when sparse.
		// Grouping never reads outside that interval; the rollup has <=16 rows/hour.
		end := to
		if from.Add(10000 * resolution).Before(end) {
			end = from.Add(10000 * resolution).Truncate(resolution)
		}
		if end.Before(from) {
			end = from
		}
		h := &content.StatsHistogram{From: from, To: end, ResolutionSeconds: int(resolution / time.Second), Items: []content.StatsBucket{}}
		if end.Before(to) {
			next := end
			h.Next = &next
		}
		args := []any{org, id, unit, from, end}
		sql := `SELECT date_trunc($3,hour,'UTC'),sum(eligible),sum(catalog) FROM corpus_record_hours WHERE organization=$1 AND corpus_id=$2 AND hour >= $4 AND hour < $5 GROUP BY 1 ORDER BY 1`
		if acl := visibilityPredicate(ctx, &args, "r"); acl != "" {
			sql = `SELECT date_trunc($3,r.current_accepted_at,'UTC'),count(*) FILTER (WHERE ` + eligibleVersionSQL + `),count(*)` +
				statsRecordsSQL + ` AND r.current_accepted_at >= $4 AND r.current_accepted_at < $5` + acl + ` GROUP BY 1 ORDER BY 1`
		}
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var b content.StatsBucket
			if err = rows.Scan(&b.Start, &b.Count, &b.CatalogCount); err != nil {
				rows.Close()
				return out, err
			}
			h.Items = append(h.Items, b)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return out, err
		}
		out.Histogram = h
	}
	if q.Sources {
		limit := q.SourceLimit
		if limit == 0 {
			limit = 100
		}
		if limit < 1 || limit > 1000 {
			return out, content.ErrInvalid
		}
		after := content.SourceStatsKey{}
		if q.SourceAfter != nil {
			after = *q.SourceAfter
		}
		// Select a bounded set of source keys before summing its stripes. OFFSET 0
		// fences the keyset selection from an unbounded hash aggregate plan.
		args := []any{org, id, after.Namespace, after.ConnectorID, limit + 1}
		sql := `SELECT k.namespace,k.connector_id,c.eligible,c.catalog FROM (
   SELECT DISTINCT namespace,connector_id FROM corpus_record_sources WHERE organization=$1 AND corpus_id=$2 AND (namespace,connector_id)>($3 COLLATE "C",$4 COLLATE "C") ORDER BY namespace,connector_id LIMIT $5 OFFSET 0
  ) k CROSS JOIN LATERAL (SELECT sum(eligible) AS eligible,sum(catalog) AS catalog FROM corpus_record_sources s WHERE organization=$1 AND corpus_id=$2 AND s.namespace=k.namespace AND s.connector_id=k.connector_id OFFSET 0) c ORDER BY k.namespace,k.connector_id`
		if acl := visibilityPredicate(ctx, &args, "r"); acl != "" {
			sql = `WITH visible AS (
 SELECT r.namespace,coalesce(a.connector_instance_id,p.connector_instance_id,'unknown') AS connector_id,
  ` + eligibleVersionSQL + ` AS eligible
 FROM records r
 LEFT JOIN record_versions v ON (v.organization,v.id)=(r.organization,r.current_version_id)
 LEFT JOIN accepted_revisions a ON (a.organization,a.record_id,a.slot)=(r.organization,r.id,v.slot)
 LEFT JOIN accepted_revisions p ON v.id IS NULL AND (p.organization,p.version_id)=(r.organization,r.desired_version_id) AND p.accepted_at IS NOT NULL
 WHERE r.organization=$1 AND r.corpus_id=$2` + acl + `
)
SELECT namespace,connector_id,count(*) FILTER (WHERE eligible),count(*) FROM visible
WHERE (namespace COLLATE "C",connector_id COLLATE "C")>($3,$4)
GROUP BY namespace,connector_id ORDER BY namespace COLLATE "C",connector_id COLLATE "C" LIMIT $5`
		}
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return out, err
		}
		sources := &content.StatsSources{Items: []content.StatsSource{}}
		for rows.Next() {
			var v content.StatsSource
			if err = rows.Scan(&v.Namespace, &v.ConnectorID, &v.Count, &v.CatalogCount); err != nil {
				rows.Close()
				return out, err
			}
			if len(sources.Items) == limit {
				last := sources.Items[limit-1]
				sources.Next = &content.SourceStatsKey{Namespace: last.Namespace, ConnectorID: last.ConnectorID}
				break
			}
			sources.Items = append(sources.Items, v)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return out, err
		}
		out.Sources = sources
	}
	return out, tx.Commit(ctx)
}

// Enforced aggregates use canonical eligibility and visibility in the same
// repeatable-read snapshot as the caller's histogram and source pages.
const statsRecordsSQL = ` FROM records r LEFT JOIN record_versions v
 ON (v.organization,v.id)=(r.organization,r.current_version_id)
 WHERE r.organization=$1 AND r.corpus_id=$2`

func statsHeaderForCaller(ctx context.Context, tx pgx.Tx, org, id string) (content.CorpusStats, error) {
	args := []any{org, id}
	acl := visibilityPredicate(ctx, &args, "r")
	if acl == "" {
		return statsHeader(ctx, tx, org, id)
	}
	out, err := statsCorpus(ctx, tx, org, id)
	if err != nil {
		return out, err
	}
	out.Complete = true
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE `+eligibleVersionSQL+`),count(*),
 count(*) FILTER (WHERE r.current_accepted_at IS NULL AND `+eligibleVersionSQL+`),
 count(*) FILTER (WHERE r.current_accepted_at IS NULL),
 date_trunc('hour',min(r.current_accepted_at),'UTC'),date_trunc('hour',max(r.current_accepted_at),'UTC')`+
		statsRecordsSQL+acl, args...).Scan(&out.Total, &out.CatalogTotal, &out.UndatedTotal, &out.CatalogUndatedTotal, &out.FirstCatalogHour, &out.LastCatalogHour)
	return out, err
}
