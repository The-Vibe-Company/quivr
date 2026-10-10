package postgres

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// concurrentIndexes contains installation-wide performance indexes. Predicates
// use PostgreSQL's canonical pg_get_expr spelling so an existing index can be
// checked before reuse or recovery. These definitions are trusted engine SQL.
var concurrentIndexes = []struct {
	Name, Table string
	Columns     []string
	Predicate   string
}{
	{Name: "segments_by_segmentation", Table: "segments", Columns: []string{"organization", "segmentation_id"}},
	{Name: "projection_purges_cleanup_due", Table: "projection_purges", Columns: []string{"noticed_at"}, Predicate: "((purged_at IS NULL) OR (cleanup_stage < 3))"},
	{Name: "queue_enrichment_pending", Table: "queue_enrichment_records", Columns: []string{"organization", "version_id"}, Predicate: "pending"},
	{Name: "queue_versions_baseline", Table: "record_versions", Columns: []string{"organization", "id"}, Predicate: "((NOT baseline_ready) AND (NOT quarantined) AND (processing = ANY (ARRAY['queued'::text, 'running'::text, 'retrying'::text])))"},
	{Name: "queue_receipts_pending", Table: "ingestion_receipts", Columns: []string{"organization", "id"}, Predicate: "((route_family = 'ingestion'::text) AND (state = 'pending'::text))"},
	{Name: "queue_evaluations_pending", Table: "ingestion_evaluations", Columns: []string{"organization", "version_id"}, Predicate: "(state = 'queued'::text)"},
	{Name: "queue_serving_pending", Table: "serving_projections", Columns: []string{"organization", "version_id"}, Predicate: "(state = 'queued'::text)"},
	{Name: "queue_operations_active", Table: "operations", Columns: []string{"organization", "corpus_id"}, Predicate: "(state = ANY (ARRAY['queued'::text, 'running'::text]))"},
	{Name: "accepted_revisions_version_lookup", Table: "accepted_revisions", Columns: []string{"organization", "version_id"}},
	{Name: "records_by_current_version", Table: "records", Columns: []string{"organization", "corpus_id", "current_version_id"}},
	{Name: "projection_metadata_filter_source", Table: "projection_metadata_filter_values", Columns: []string{"organization", "version_id", "generation_id"}},
	{Name: "projection_metadata_filter_values_lookup", Table: "projection_metadata_filter_values", Columns: []string{"organization", "corpus_id", "generation_id", "field", "value", "version_id"}},
	{Name: "projection_metadata_filter_dates_lookup", Table: "projection_metadata_filter_values", Columns: []string{"organization", "corpus_id", "generation_id", "field", "date_epoch_ms", "version_id"}, Predicate: "(date_epoch_ms IS NOT NULL)"},
	{Name: "projection_metadata_filter_pending", Table: "projection_metadata", Columns: []string{"organization", "generation_id", "version_id"}, Predicate: "(NOT filter_indexed)"},
}

// Index setup errors let the CLI report operator actions without logging raw
// database diagnostics, which can contain deployment credentials or data.
var (
	ErrIndexSetup    = errors.New("concurrent index setup failed; background maintenance will retry")
	ErrIndexBusy     = errors.New("another database setup is running")
	ErrIndexConflict = errors.New("concurrent index definition conflicts with an existing object")
)

// Reserved independently of schema migrations: a build waiting for an older
// writer must not prevent another process from applying required migrations.
const indexAdvisoryLock int64 = 642004

//go:embed metadata_catalog_sync.sql
var metadataCatalogSync string

// Synchronization is optional performance maintenance, outside the declarative
// schema expansion. Install it atomically before any source is marked indexed.
// A canceled attempt leaves all unpopulated sources on the bounded read path.
func ensureMetadataCatalogSync(ctx context.Context, conn *pgx.Conn) error {
	var installed bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_trigger
 WHERE tgrelid='projection_metadata'::regclass AND tgname='projection_metadata_filter_sync'
 AND tgenabled='O' AND NOT tgisinternal
 AND tgfoid=to_regprocedure('projection_metadata_filter_sync()'))`).Scan(&installed); err != nil {
		return err
	}
	if installed {
		return nil
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'; SET LOCAL statement_timeout='5s'`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, metadataCatalogSync); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// EnsureIndexes runs resumable index work after schema migrations commit. Each
// build preserves concurrent writes, reuses a matching valid index, and repairs
// the invalid catalog entry PostgreSQL can leave after a canceled build.
func EnsureIndexes(ctx context.Context, pool *pgxpool.Pool) (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w: %w", ErrIndexSetup, err)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	pooled, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	// A dedicated session keeps settings and the session lock out of the pool.
	conn := pooled.Hijack()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		conn.Close(cleanup)
	}()
	if _, err := conn.Exec(ctx, "SET lock_timeout = '0'; SET statement_timeout = '30min'"); err != nil {
		return err
	}
	var locked bool
	// Only one live process maintains indexes. Other processes retry promptly
	// instead of waiting on this session for the duration of its index work.
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", indexAdvisoryLock).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return ErrIndexBusy
	}
	if err := ensureMetadataCatalogSync(ctx, conn); err != nil {
		return err
	}
	for _, index := range concurrentIndexes {
		state := func() (valid, matches bool, err error) {
			err = conn.QueryRow(ctx, `SELECT COALESCE(i.indisvalid, false), COALESCE(
 i.indrelid=to_regclass($2) AND am.amname='btree' AND NOT i.indisunique
 AND i.indexprs IS NULL AND i.indnatts=i.indnkeyatts
 AND NOT EXISTS(SELECT FROM unnest(i.indkey) WITH ORDINALITY k(attnum, ordinal)
                JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=k.attnum
                JOIN pg_opclass opc ON opc.oid=i.indclass[k.ordinal::int-1]
                WHERE i.indcollation[k.ordinal::int-1]<>a.attcollation OR NOT opc.opcdefault)
 AND ARRAY(SELECT a.attname::text FROM unnest(i.indkey) WITH ORDINALITY k(attnum, ordinal)
           JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=k.attnum
           ORDER BY k.ordinal)=$3::text[]
 AND COALESCE(pg_get_expr(i.indpred, i.indrelid), '')=$4, false)
FROM pg_class c LEFT JOIN pg_index i ON i.indexrelid=c.oid
LEFT JOIN pg_am am ON am.oid=c.relam WHERE c.oid=to_regclass($1)`,
				index.Name, index.Table, index.Columns, index.Predicate).Scan(&valid, &matches)
			return
		}
		valid, matches, err := state()
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("inspect index %s: %w", index.Name, err)
		}
		if err == nil {
			if !matches {
				return fmt.Errorf("index %s has an incompatible definition; inspect and rename the conflicting object: %w", index.Name, ErrIndexConflict)
			}
			if valid {
				continue
			}
			if _, err := conn.Exec(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+pgx.Identifier{index.Name}.Sanitize()); err != nil {
				return fmt.Errorf("recover index %s: %w", index.Name, err)
			}
		}
		columns := make([]string, len(index.Columns))
		for n, column := range index.Columns {
			columns[n] = pgx.Identifier{column}.Sanitize()
		}
		sql := "CREATE INDEX CONCURRENTLY IF NOT EXISTS " + pgx.Identifier{index.Name}.Sanitize() +
			" ON " + pgx.Identifier{index.Table}.Sanitize() + " (" + strings.Join(columns, ", ") + ")"
		if index.Predicate != "" {
			sql += " WHERE " + index.Predicate
		}
		if _, err := conn.Exec(ctx, sql); err != nil {
			return fmt.Errorf("build index %s: %w", index.Name, err)
		}
		if valid, matches, err := state(); err != nil || !valid || !matches {
			return fmt.Errorf("index %s did not become valid with the expected definition (valid=%t, matches=%t): %v", index.Name, valid, matches, err)
		}
	}
	// Population commits short batches, so cancellation/restart resumes from the
	// remaining source rows and never holds a write fence across the corpus.
	for {
		result, err := conn.Exec(ctx, `WITH pending AS MATERIALIZED (
 SELECT organization,version_id,generation_id FROM projection_metadata
 WHERE NOT filter_indexed ORDER BY organization,generation_id,version_id
 LIMIT 500 FOR UPDATE SKIP LOCKED)
 UPDATE projection_metadata pm SET data=pm.data FROM pending p
 WHERE (pm.organization,pm.version_id,pm.generation_id)=(p.organization,p.version_id,p.generation_id)`)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 0 {
			break
		}
	}
	var pending bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT FROM projection_metadata WHERE NOT filter_indexed)`).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return ErrIndexBusy
	}
	// All consumers now use point probes or scoped Btree entries. Retiring this
	// installation-wide GIN avoids paying for two complete metadata indexes.
	_, err = conn.Exec(ctx, "DROP INDEX CONCURRENTLY IF EXISTS projection_metadata_values")
	return err
}
