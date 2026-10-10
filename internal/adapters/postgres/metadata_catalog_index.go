package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// These are installation-independent work bounds, not usage entitlements.
const metadataCandidateLimit = 10000
const metadataOrderedLimit = 16384
const metadataReadBudget = 250 * time.Millisecond
const catalogColumns = `id,corpus_id,namespace,record_key,withdrawn,coalesce(current_version_id,'') AS current_version_id,current_accepted_at`

func (s RecordStore) metadataRecords(parent context.Context, org, corpusID string, q content.RecordQuery) (result []content.Record, err error) {
	// Pool pressure and connection failures are transient, independent of filter
	// selectivity. Bound acquisition separately and leave its error retryable.
	acquire, stopAcquire := context.WithTimeout(parent, metadataReadBudget)
	var tx pgx.Tx
	db := database(parent, s.Pool)
	if db == s.Pool {
		tx, err = s.Pool.BeginTx(acquire, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	} else {
		tx, err = db.Begin(acquire)
	}
	stopAcquire()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, metadataReadBudget)
	defer cancel()
	defer func() {
		if err == nil {
			return
		}
		if parent.Err() != nil {
			err = parent.Err()
			return
		}
		var sqlError *pgconn.PgError
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &sqlError) && sqlError.Code == "57014" && sqlError.Message == "canceling statement due to statement timeout") {
			err = publicerr.FilterTooBroad
		}
	}()
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	q.CorpusIDs = union(q.CorpusIDs, nil)
	if len(q.CorpusIDs) == 0 {
		q.CorpusIDs = []string{corpusID}
	}
	result = []content.Record{}
	seen := map[string]bool{}
	for _, id := range q.CorpusIDs {
		for _, route := range q.FilterRoutes {
			if route.CorpusID != id {
				continue
			}
			single := q
			single.CorpusIDs = []string{id}
			single.FilterRoutes = []content.CatalogFilterRoute{route}
			page, e := metadataCorpusPage(ctx, tx, org, id, single, route)
			if e != nil {
				return nil, e
			}
			for _, r := range page {
				if !seen[r.ID] {
					result = append(result, r)
					seen[r.ID] = true
				}
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if q.Order == content.AcceptedAtDesc {
			if a.CurrentAcceptedAt == nil && b.CurrentAcceptedAt != nil {
				return false
			}
			if b.CurrentAcceptedAt == nil && a.CurrentAcceptedAt != nil {
				return true
			}
			if a.CurrentAcceptedAt != nil && !a.CurrentAcceptedAt.Equal(*b.CurrentAcceptedAt) {
				return a.CurrentAcceptedAt.After(*b.CurrentAcceptedAt)
			}
			return a.ID > b.ID
		}
		return a.ID < b.ID
	})
	if len(result) > q.Limit {
		result = result[:q.Limit]
	}
	return result, nil
}

// Every statement gets only the time left on the shared data-read deadline.
// Earlier windows and probes consume the same budget as the final page.
func metadataStatement(ctx context.Context, tx pgx.Tx) error {
	deadline, _ := ctx.Deadline()
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return publicerr.FilterTooBroad
	}
	_, err := tx.Exec(ctx, `SELECT set_config('statement_timeout',$1,true)`, fmt.Sprint(max(1, remaining.Milliseconds())))
	return err
}

func metadataCorpusPage(ctx context.Context, tx pgx.Tx, org, id string, q content.RecordQuery, route content.CatalogFilterRoute) ([]content.Record, error) {
	for _, f := range route.Filters {
		if f.Gte == "" && f.Lte == "" {
			continue
		}
		if err := metadataStatement(ctx, tx); err != nil {
			return nil, err
		}
		var datesReady bool
		if err := tx.QueryRow(ctx, `SELECT to_regprocedure('projection_metadata_filter_epoch(text)') IS NOT NULL`).Scan(&datesReady); err != nil {
			return nil, err
		}
		if !datesReady {
			return nil, publicerr.ContentUnavailable
		}
		break
	}
	page, last, complete, err := metadataOrderedWindow(ctx, tx, org, id, q, 512)
	if err != nil || complete {
		return page, err
	}
	if err := metadataStatement(ctx, tx); err != nil {
		return nil, err
	}
	var ready bool
	err = tx.QueryRow(ctx, `SELECT
 (SELECT count(*)=4 FROM pg_index WHERE indexrelid IN
  (to_regclass('projection_metadata_filter_source'),to_regclass('projection_metadata_filter_values_lookup'),
   to_regclass('projection_metadata_filter_dates_lookup'),to_regclass('projection_metadata_filter_pending')) AND indisvalid)
 AND NOT EXISTS(SELECT FROM projection_metadata WHERE organization=$1 AND generation_id=$2 AND NOT filter_indexed)`, org, route.GenerationID).Scan(&ready)
	if err != nil {
		return nil, err
	}
	if ready {
		// Each filter owns at most one bounded anchor. A broad first predicate
		// cannot consume the next predicate's allowance (up to 16 per Corpus).
		for _, filter := range route.Filters {
			versions, complete, err := metadataAnchor(ctx, tx, org, id, route.GenerationID, filter)
			if err != nil {
				return nil, err
			}
			if complete {
				return metadataCandidatePage(ctx, tx, org, id, q, versions)
			}
		}
	}
	scanned := 512
	for size := 1024; scanned < metadataOrderedLimit; size *= 2 {
		window := q
		window.AfterID = last.ID
		window.AfterAcceptedAt = last.CurrentAcceptedAt
		window.Limit = q.Limit - len(page)
		count := min(size, metadataOrderedLimit-scanned)
		more, end, done, e := metadataOrderedWindow(ctx, tx, org, id, window, count)
		if e != nil {
			return nil, e
		}
		page = append(page, more...)
		last = end
		scanned += count
		if done {
			return page, nil
		}
	}
	return nil, publicerr.FilterTooBroad
}

func metadataOrderedWindow(ctx context.Context, tx pgx.Tx, org, id string, q content.RecordQuery, size int) (page []content.Record, last content.Record, complete bool, err error) {
	args := []any{org, id}
	unfiltered := q
	unfiltered.Metadata = nil
	where := catalogRange(unfiltered, &args)
	cursor, order := catalogCursor(q, &args)
	where += cursor
	args = append(args, size)
	limit := fmt.Sprintf("$%d", len(args))
	matched := catalogMetadataDates(q, &args, true)
	sql := `WITH records AS MATERIALIZED (SELECT organization,` + catalogColumns + ` FROM records WHERE ` + where + ` ORDER BY ` + order + ` LIMIT ` + limit + `)
 SELECT ` + catalogColumns + `, (true` + matched + `) AS matched FROM records ORDER BY ` + order
	if err = metadataStatement(ctx, tx); err != nil {
		return
	}
	rows, e := tx.Query(ctx, sql, args...)
	if e != nil {
		err = e
		return
	}
	defer rows.Close()
	page = []content.Record{}
	count := 0
	for rows.Next() {
		var r content.Record
		var matches bool
		if err = rows.Scan(&r.ID, &r.Source.CorpusID, &r.Source.Namespace, &r.Source.RecordKey, &r.Withdrawn, &r.CurrentVersionID, &r.CurrentAcceptedAt, &matches); err != nil {
			return
		}
		last = r
		count++
		if matches && len(page) < q.Limit {
			page = append(page, r)
		}
	}
	err = rows.Err()
	complete = count < size || len(page) >= q.Limit
	return
}

func metadataAnchor(ctx context.Context, tx pgx.Tx, org, id, generation string, f corpus.TypedFilter) ([]string, bool, error) {
	needles := f.AnyOf
	ranged := len(needles) == 0 && (f.Gte != "" || f.Lte != "")
	if ranged {
		needles = []any{nil}
	}
	all := []string{}
	queried := map[string]bool{}
	for _, needle := range needles {
		raw, err := json.Marshal(needle)
		if err != nil {
			return nil, false, err
		}
		if !ranged && queried[string(raw)] {
			continue
		}
		queried[string(raw)] = true
		lastVersion := ""
		var lastDate int64
		for {
			args := []any{org, id, generation, f.Field}
			bind := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
			where := `organization=$1 AND corpus_id=$2 AND generation_id=$3 AND field=$4`
			order, dateColumn := "version_id", "NULL::bigint"
			if f.Gte != "" || f.Lte != "" {
				where += " AND date_epoch_ms IS NOT NULL"
				if f.Gte != "" {
					where += " AND date_epoch_ms >= projection_metadata_filter_epoch(" + bind(f.Gte) + ")"
				}
				if f.Lte != "" {
					where += " AND date_epoch_ms <= projection_metadata_filter_epoch(" + bind(f.Lte) + ")"
				}
			}
			if ranged {
				order, dateColumn = "date_epoch_ms,version_id", "date_epoch_ms"
				if lastVersion != "" {
					where += " AND (date_epoch_ms,version_id) > (" + bind(lastDate) + "," + bind(lastVersion) + ")"
				}
			} else {
				where += " AND value=" + bind(raw) + "::jsonb"
				if lastVersion != "" {
					where += " AND version_id > " + bind(lastVersion)
				}
			}
			// Small keyset windows make ordered index startup cheap even when field
			// statistics underestimate a common value. An eager bitmap/sort over the
			// complete anchor must not be needed to find its overflow sentinel.
			size := min(512, metadataCandidateLimit+1-len(all))
			sql := `SELECT version_id,` + dateColumn + ` FROM projection_metadata_filter_values WHERE ` + where + ` ORDER BY ` + order + ` LIMIT ` + bind(size)
			if err := metadataStatement(ctx, tx); err != nil {
				return nil, false, err
			}
			rows, err := tx.Query(ctx, sql, args...)
			if err != nil {
				return nil, false, err
			}
			count := 0
			for rows.Next() {
				var version string
				var date *int64
				if err = rows.Scan(&version, &date); err != nil {
					rows.Close()
					return nil, false, err
				}
				all = append(all, version)
				lastVersion = version
				if date != nil {
					lastDate = *date
				}
				count++
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, false, err
			}
			if len(all) > metadataCandidateLimit {
				return nil, false, nil
			}
			if count < size {
				break
			}
		}
	}
	return union(all, nil), true, nil
}

func metadataCandidatePage(ctx context.Context, tx pgx.Tx, org, id string, q content.RecordQuery, versions []string) ([]content.Record, error) {
	if len(versions) == 0 {
		return []content.Record{}, nil
	}
	args := []any{org, id, versions}
	unfiltered := q
	unfiltered.Metadata = nil
	where := catalogRange(unfiltered, &args) + catalogMetadataDates(q, &args, true)
	cursor, order := catalogCursor(q, &args)
	where += cursor
	args = append(args, q.Limit)
	// The lateral limit keeps lookup work proportional to the bounded anchor,
	// even when a planner would otherwise prefer the catalog's ordered walk.
	sql := `WITH candidates AS MATERIALIZED (SELECT unnest($3::text[]) AS version_id)
 SELECT records.* FROM candidates CROSS JOIN LATERAL (
 SELECT ` + catalogColumns + ` FROM records WHERE ` + where + ` AND current_version_id=candidates.version_id LIMIT 1
 ) records ORDER BY ` + order + fmt.Sprintf(" LIMIT $%d", len(args))
	if err := metadataStatement(ctx, tx); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := []content.Record{}
	for rows.Next() {
		var r content.Record
		if err = rows.Scan(&r.ID, &r.Source.CorpusID, &r.Source.Namespace, &r.Source.RecordKey, &r.Withdrawn, &r.CurrentVersionID, &r.CurrentAcceptedAt); err != nil {
			return nil, err
		}
		page = append(page, r)
	}
	return page, rows.Err()
}
