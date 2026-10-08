package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/The-Vibe-Company/quivr/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Owns the hot-table upgrade contract against real PostgreSQL. Existing lease
// fencing tests remain the owner of stale-worker exclusion.
func TestHotSmallTablesUpgrade(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	migrateThrough(t, ctx, pool, "20261008T0622Z_vector_index.sql")
	if _, err := pool.Exec(ctx, `INSERT INTO queue_document_attempts VALUES
('upgrade','test','work','document',nextval('queue_document_attempt_tokens'),'infinity')`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"organization_journals", "queue_document_attempts", "pipeline_plan_work", "connector_instances", "queue_backlog_snapshots", "operations"} {
		var disabled bool
		if err := pool.QueryRow(ctx, `SELECT coalesce('vacuum_truncate=false'=ANY(reloptions),false)
FROM pg_class WHERE oid=to_regclass($1)`, table).Scan(&disabled); err != nil || !disabled {
			t.Fatalf("%s must disable vacuum truncation: disabled=%v, err=%v", table, disabled, err)
		}
	}
	for _, table := range []string{"organization_journals", "queue_document_attempts", "queue_backlog_snapshots"} {
		var reserved bool
		if err := pool.QueryRow(ctx, `SELECT coalesce('fillfactor=70'=ANY(reloptions),false)
FROM pg_class WHERE oid=to_regclass($1)`, table).Scan(&reserved); err != nil || !reserved {
			t.Fatalf("%s must reserve room for HOT updates: reserved=%v, err=%v", table, reserved, err)
		}
	}
	var vacuum, analyze, inserts int
	var vacuumScale, analyzeScale, insertScale float64
	if err := pool.QueryRow(ctx, `SELECT
(SELECT option_value::int FROM pg_options_to_table(reloptions) WHERE option_name='autovacuum_vacuum_threshold'),
(SELECT option_value::int FROM pg_options_to_table(reloptions) WHERE option_name='autovacuum_analyze_threshold'),
(SELECT option_value::int FROM pg_options_to_table(reloptions) WHERE option_name='autovacuum_vacuum_insert_threshold'),
(SELECT option_value::float8 FROM pg_options_to_table(reloptions) WHERE option_name='autovacuum_vacuum_scale_factor'),
(SELECT option_value::float8 FROM pg_options_to_table(reloptions) WHERE option_name='autovacuum_analyze_scale_factor'),
(SELECT option_value::float8 FROM pg_options_to_table(reloptions) WHERE option_name='autovacuum_vacuum_insert_scale_factor')
FROM pg_class WHERE oid='queue_document_attempts'::regclass`).Scan(&vacuum, &analyze, &inserts, &vacuumScale, &analyzeScale, &insertScale); err != nil {
		t.Fatal(err)
	}
	if vacuum != 10 || analyze != 10 || inserts != 50 || vacuumScale != 0 || analyzeScale != 0 || insertScale != 0 {
		t.Fatalf("attempt autovacuum thresholds = %d/%d/%d, scales = %g/%g/%g; want 10/10/50 and zero scales", vacuum, analyze, inserts, vacuumScale, analyzeScale, insertScale)
	}
	var expiryIndex, retained bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('queue_document_attempts_active') IS NOT NULL,
EXISTS(SELECT FROM queue_document_attempts WHERE organization='upgrade' AND token>0 AND lease_until='infinity')`).Scan(&expiryIndex, &retained); err != nil || expiryIndex || !retained {
		t.Fatalf("upgrade must remove expiry index and preserve active lease: index=%v, retained=%v, err=%v", expiryIndex, retained, err)
	}
}

// Owns bounded cleanup, including a concurrent writer's locked expired row.
// A smaller batch or a removed lease predicate/limit fails at this boundary.
func TestQueueAttemptCleanupPreservesLiveAndLockedRows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO queue_document_attempts
SELECT 'cleanup','test',i::text,'document',nextval('queue_document_attempt_tokens'),'-infinity'::timestamptz
FROM generate_series(1,1002) s(i);
INSERT INTO queue_document_attempts VALUES
('cleanup','test','live','document',nextval('queue_document_attempt_tokens'),'infinity')`); err != nil {
		t.Fatal(err)
	}
	writer, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(context.Background())
	if _, err := writer.Exec(ctx, `SELECT FROM queue_document_attempts WHERE work_id='1' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	snapshots := QueueSnapshots{Pool: pool}
	for _, wantExpired := range []int{2, 1} {
		refreshQueueWork(t, ctx, snapshots)
		var expired int
		var protected bool
		if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE lease_until<clock_timestamp()),
count(*) FILTER (WHERE work_id IN ('1','live'))=2 FROM queue_document_attempts`).Scan(&expired, &protected); err != nil || expired != wantExpired || !protected {
			t.Fatalf("cleanup: expired=%d protected=%v err=%v; want expired=%d and both protected rows", expired, protected, err, wantExpired)
		}
	}
	if err := writer.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	refreshQueueWork(t, ctx, snapshots)
	var remaining int
	var live bool
	if err := pool.QueryRow(ctx, `SELECT count(*),coalesce(bool_and(work_id='live' AND lease_until='infinity'),false)
FROM queue_document_attempts`).Scan(&remaining, &live); err != nil || remaining != 1 || !live {
		t.Fatalf("cleanup after unlock: remaining=%d live=%v err=%v; want only the live attempt", remaining, live, err)
	}
}

// Owns the storage cost of lease renewal: concurrent writers must mostly use
// HOT updates, rather than churn an index on each new expiry timestamp.
func TestConcurrentQueueAttemptsUseHOTRenewals(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	const writers, renewals, cycles = 32, 50, 40
	start := make(chan struct{})
	errors := make(chan error, writers)
	var workers sync.WaitGroup
	for i := range writers {
		config := pool.Config().Copy()
		config.MaxConns = 1 // Flush the same backend that performed the renewals.
		config.ConnConfig.RuntimeParams["application_name"] = "queue-attempt-writer"
		writer, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			defer writer.Close()
			<-start
			tracker := QueueTracker{Pool: writer}
			work := fmt.Sprintf("work-%02d", i)
			token, err := tracker.claim(ctx, "concurrent", "test", work, "document", time.Minute)
			if err != nil {
				errors <- err
				return
			}
			for range renewals {
				if _, err := tracker.renew(ctx, "concurrent", "test", work, "document", token, time.Minute); err != nil {
					errors <- err
					return
				}
			}
			if err := tracker.release(ctx, "concurrent", "test", work, "document", token); err != nil {
				errors <- err
				return
			}
			for range cycles {
				token, err := tracker.claim(ctx, "concurrent", "test", work, "document", time.Minute)
				if err == nil {
					_, err = tracker.renew(ctx, "concurrent", "test", work, "document", token, time.Minute)
				}
				if err == nil {
					err = tracker.release(ctx, "concurrent", "test", work, "document", token)
				}
				if err != nil {
					errors <- err
					return
				}
			}
			_, err = writer.Exec(ctx, "SELECT pg_stat_force_next_flush()")
			errors <- err
		}(i)
	}
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	close(start)
	peakWaits, samples := 0, 0
	for {
		select {
		case <-done:
			goto finished
		default:
		}
		var waits int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database()
AND application_name='queue-attempt-writer' AND wait_event='BufferContent'`).Scan(&waits); err != nil {
			cancel()
			<-done
			t.Fatal(err)
		}
		peakWaits = max(peakWaits, waits)
		samples++
	}
finished:
	for range writers {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, "SELECT pg_stat_clear_snapshot()"); err != nil {
		t.Fatal(err)
	}
	var updates, hot, retained, indexBytes int64
	if err := pool.QueryRow(ctx, `SELECT n_tup_upd,n_tup_hot_upd,
(SELECT count(*) FROM queue_document_attempts),pg_indexes_size('queue_document_attempts')
FROM pg_stat_all_tables WHERE relid='queue_document_attempts'::regclass`).Scan(&updates, &hot, &retained, &indexBytes); err != nil {
		t.Fatal(err)
	}
	t.Logf("writers=%d updates=%d HOT=%d retained=%d index_bytes=%d BufferContent_peak=%d samples=%d", writers, updates, hot, retained, indexBytes, peakWaits, samples)
	if updates != writers*(renewals+cycles) || hot*10 < updates*9 || retained != 0 {
		t.Fatalf("want all %d renewals observed, at least 90%% HOT updates and no retained attempts; got updates=%d HOT=%d retained=%d", writers*(renewals+cycles), updates, hot, retained)
	}
}

// migrateThrough applies the actual schema prefix, never a selection with
// migrations omitted from the middle of its history.
func migrateThrough(t *testing.T, ctx context.Context, pool *pgxpool.Pool, through string) {
	t.Helper()
	names, err := migrations.Names()
	if err != nil {
		t.Fatal(err)
	}
	prefix := fstest.MapFS{}
	found := false
	for _, name := range names {
		data, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		prefix[name] = &fstest.MapFile{Data: data}
		if name == through {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("migration %q not found", through)
	}
	if err := MigrateFS(ctx, pool, prefix); err != nil {
		t.Fatal(err)
	}
}
