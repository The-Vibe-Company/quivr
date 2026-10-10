package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"sync"
	"testing"
	"time"
)

// Owns the storage delta contract: retrying an observation cannot add another
// document, and correction/fencing move or remove only the eligible membership.
// The independent recount uses canonical rows, never the observation ledger.
func TestCorpusRecordLedgerTransitions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	seedQueueCorpus(t, ctx, pool, "example")
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO records(organization,id,corpus_id,namespace,record_key)
 VALUES('example','one','corpus','source','one'),('example','two','corpus','source','two');
 INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,source_position,text_blob_id,manifest_blob_id,provenance,baseline_ready)
 VALUES('example','v1','one','first','first',1,'','blob','blob','{}',true);
 INSERT INTO ingestion_receipts(organization,id,request_key,canonical_request,command,corpus_id,record_id,acceptance_order,slot,digest,accepted_at)
 VALUES('example','receipt','receipt','','{}','corpus','one',1,'first','first','2026-10-01T01:30:00Z');
 UPDATE records SET current_version_id='v1' WHERE id='one'`)
	observe := func() {
		if err := observeCorpusRecords(ctx, pool, []string{"example", "example"}, []string{"one", "two"}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want, catalog, undated int64) {
		t.Helper()
		var got, all, nilDates, exact int64
		if err := pool.QueryRow(ctx, `SELECT coalesce(sum(eligible),0),coalesce(sum(catalog),0),coalesce(sum(catalog_undated),0) FROM corpus_record_totals WHERE organization='example' AND corpus_id='corpus'`).Scan(&got, &all, &nilDates); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM records r JOIN record_versions v ON (v.organization,v.id)=(r.organization,r.current_version_id) WHERE r.organization='example' AND r.corpus_id='corpus' AND v.baseline_ready AND NOT v.quarantined AND NOT r.withdrawn AND NOT EXISTS(SELECT FROM tombstones t WHERE (t.organization,t.record_id)=(r.organization,r.id))`).Scan(&exact); err != nil {
			t.Fatal(err)
		}
		if got != want || got != exact || all != catalog || nilDates != undated {
			t.Fatalf("eligible=%d exact=%d catalog=%d undated=%d; want %d/%d/%d", got, exact, all, nilDates, want, catalog, undated)
		}
	}
	observe()
	observe()
	check(1, 2, 1)
	exec(`UPDATE records SET current_accepted_at='2026-10-02T02:30:00Z' WHERE id='one'`)
	observe()
	var buckets int
	var hour time.Time
	if err := pool.QueryRow(ctx, `SELECT count(*),min(hour) FROM corpus_record_hours WHERE catalog>0`).Scan(&buckets, &hour); err != nil {
		t.Fatal(err)
	}
	if buckets != 1 || !hour.Equal(time.Date(2026, 10, 2, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("correction buckets=%d hour=%s", buckets, hour)
	}
	exec(`UPDATE records SET withdrawn=true WHERE id='one'`)
	observe()
	check(0, 2, 1)
	exec(`UPDATE records SET withdrawn=false WHERE id='one'; INSERT INTO tombstones(organization,record_id) VALUES('example','one')`)
	observe()
	check(0, 2, 1)
	exec(`DELETE FROM tombstones WHERE record_id='one'; UPDATE record_versions SET quarantined=true WHERE id='v1'`)
	observe()
	check(0, 2, 1)
	exec(`UPDATE record_versions SET quarantined=false WHERE id='v1'`)
	observe()
	check(1, 2, 1)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE records SET withdrawn=true WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	if err = observeCorpusRecords(ctx, tx, []string{"example"}, []string{"one"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	check(1, 2, 1)
	var connector string
	if err = pool.QueryRow(ctx, `SELECT connector_id FROM corpus_record_sources WHERE catalog>0 LIMIT 1`).Scan(&connector); err != nil {
		t.Fatal(err)
	}
	if connector != "unknown" {
		t.Fatalf("historical connector=%q, want unknown", connector)
	}
}

type statsReadQuery struct {
	SQL  string
	Args []any
}
type statsReadTrace struct {
	Queries []statsReadQuery
	OnStart func(string)
}

func (s *statsReadTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if s.OnStart != nil {
		s.OnStart(d.SQL)
	}
	if strings.HasPrefix(d.SQL, "SELECT") {
		s.Queries = append(s.Queries, statsReadQuery{d.SQL, d.Args})
	}
	return ctx
}
func (*statsReadTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// Owns request work bounds, rather than machine-dependent timing. Plans from
// real adapter calls must not visit the ledger or enumerate every source.
func TestCorpusStatsBoundedReads(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	seedQueueCorpus(t, ctx, pool, "example")
	if _, err := pool.Exec(ctx, `INSERT INTO records(organization,id,corpus_id,namespace,record_key)
 SELECT 'example','r'||i,'corpus','source',i::text FROM generate_series(1,20000)i;
 INSERT INTO corpus_record_totals(organization,corpus_id,shard,eligible,catalog) VALUES('example','corpus',0,20000,20000);
 INSERT INTO corpus_record_sources(organization,corpus_id,namespace,connector_id,shard,eligible,catalog)
 SELECT 'example','corpus','source-'||lpad(i::text,6,'0'),'unknown',0,1,1 FROM generate_series(1,20000)i;
 INSERT INTO corpus_record_hours(organization,corpus_id,hour,shard,eligible,catalog)
 SELECT 'example','corpus','2025-01-01'::timestamptz+i*interval '1 hour',0,1,1 FROM generate_series(0,8759)i;
 INSERT INTO corpus_record_bootstrap(organization,corpus_id,complete) VALUES('example','corpus',true); ANALYZE`); err != nil {
		t.Fatal(err)
	}
	trace := &statsReadTrace{}
	cfg := pool.Config()
	cfg.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	store := CorpusStatsStore{Pool: traced}
	got, err := store.CorpusStats(ctx, "example", "corpus", content.CorpusStatsQuery{Sources: true, SourceLimit: 10})
	if err != nil || got.Total != 20000 || len(got.Sources.Items) != 10 || got.Sources.Next == nil {
		t.Fatalf("source page: %+v %v", got, err)
	}
	result, err := store.RecordCount(ctx, "example", "corpus", content.RecordQuery{})
	if err != nil || result.Count != 20000 || !result.Approximate || !result.Complete {
		t.Fatalf("unbounded count: %+v %v", result, err)
	}
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(365 * 24 * time.Hour)
	got, err = store.CorpusStats(ctx, "example", "corpus", content.CorpusStatsQuery{Histogram: true, From: &from, To: &to})
	if err != nil || len(got.Histogram.Items) != 365 || got.Histogram.Next != nil {
		t.Fatalf("year histogram: %+v %v", got, err)
	}
	for _, query := range trace.Queries {
		var raw []byte
		if err = pool.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+query.SQL, query.Args...).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var plans []struct{ Plan map[string]any }
		if err = json.Unmarshal(raw, &plans); err != nil {
			t.Fatal(err)
		}
		var walk func(map[string]any)
		walk = func(p map[string]any) {
			relation, _ := p["Relation Name"].(string)
			rows, _ := p["Actual Rows"].(float64)
			loops, _ := p["Actual Loops"].(float64)
			if relation == "corpus_record_observations" {
				t.Fatalf("stats read visited %s: %s", relation, raw)
			}
			if relation == "records" && rows*loops > 10001 {
				t.Fatalf("count probe exceeded cap: %s", raw)
			}
			if relation == "corpus_record_sources" && rows*loops > 400 {
				t.Fatalf("source page enumerated %.0f rows: %s", rows*loops, raw)
			}
			if children, ok := p["Plans"].([]any); ok {
				for _, child := range children {
					walk(child.(map[string]any))
				}
			}
		}
		walk(plans[0].Plan)
	}

	// A lock outage is infrastructure, not an oversized time window. Exercise
	// the actual reader so setting the execution budget cannot hide this class.
	lock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.WithoutCancel(ctx))
	if _, err = lock.Exec(ctx, "LOCK TABLE records IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	_, err = store.RecordCount(ctx, "example", "corpus", content.RecordQuery{AcceptedAfter: &from, AcceptedBefore: &to})
	var storage *pgconn.PgError
	if !errors.As(err, &storage) || storage.Code != "55P03" || errors.Is(err, content.ErrCountTooBroad) {
		t.Fatalf("lock timeout became a broad count: %v", err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err = store.RecordCount(canceled, "example", "corpus", content.RecordQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation changed: %v", err)
	}

}

// A bootstrap observer waiting behind a writer must reread canonical state
// after the ledger lock. Distinct writers must also serialize aggregate deltas
// without losing increments. Both regressions are invisible in serial fixtures.
func TestCorpusRecordConcurrentObservers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	seedQueueCorpus(t, ctx, pool, "example")
	if _, err := pool.Exec(ctx, `INSERT INTO records(organization,id,corpus_id,namespace,record_key) SELECT 'example','r'||i,'corpus','source',i::text FROM generate_series(1,32)i`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	fail := make(chan error, 32)
	for i := 1; i <= 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			err := observeCorpusRecords(ctx, pool, []string{"example"}, []string{fmt.Sprintf("r%d", i)})
			fail <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(fail)
	for err := range fail {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := (CorpusStatsStore{Pool: pool}).CorpusStats(ctx, "example", "corpus", content.CorpusStatsQuery{Sources: true})
	if err != nil || got.CatalogTotal != 32 || len(got.Sources.Items) != 1 || got.Sources.Items[0].CatalogCount != 32 {
		t.Fatalf("concurrent totals: %+v %v", got, err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `UPDATE records SET current_accepted_at='2026-10-01T01:00:00Z' WHERE id='r1'`); err != nil {
		t.Fatal(err)
	}
	if err = observeCorpusRecords(ctx, tx, []string{"example"}, []string{"r1"}); err != nil {
		t.Fatal(err)
	}
	waiter, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer waiter.Release()
	pid := waiter.Conn().PgConn().PID()
	done := make(chan error, 1)
	go func() {
		err := observeCorpusRecords(ctx, waiter, []string{"example"}, []string{"r1"})
		done <- err
	}()
	// Await the actual database lock condition, not elapsed wall-clock time.
	for {
		var blocked bool
		if err = pool.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1))>0`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	got, err = (CorpusStatsStore{Pool: pool}).CorpusStats(ctx, "example", "corpus", content.CorpusStatsQuery{Histogram: true})
	if err != nil || got.CatalogTotal != 32 || got.CatalogUndatedTotal != 31 || len(got.Histogram.Items) != 1 || got.Histogram.Items[0].CatalogCount != 1 {
		t.Fatalf("post-lock snapshot: %+v %v", got, err)
	}
}

// Owns incremental upgrade progress and the read boundary. Recreating the
// adapter must resume the durable cursor; complete results reconcile both
// memberships and preserve history older than a year.
func TestCorpusStatsResumableBootstrap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	seedQueueCorpus(t, ctx, pool, "example")
	if _, err := pool.Exec(ctx, `INSERT INTO records(organization,id,corpus_id,namespace,record_key)
 VALUES('example','one','corpus','source','one'),('example','two','corpus','source','two'),('example','three','corpus','source','three');
 UPDATE records SET current_accepted_at='2020-01-01T01:30:00Z' WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	store := CorpusStatsStore{Pool: pool}
	got, err := store.CorpusStats(ctx, "example", "corpus", content.CorpusStatsQuery{Histogram: true, Sources: true})
	if err != nil || got.Complete || !got.Approximate {
		t.Fatalf("before bootstrap: %+v %v", got, err)
	}
	if _, err = store.Bootstrap(ctx, 1); err != nil {
		t.Fatal(err)
	}
	got, err = store.CorpusStats(ctx, "example", "corpus", content.CorpusStatsQuery{})
	if err != nil || got.Complete || got.CatalogTotal != 1 {
		t.Fatalf("partial bootstrap: %+v %v", got, err)
	}
	// Another adapter/process continues, rather than recounting from the start.
	resumed := CorpusStatsStore{Pool: pool}

	// Complete initialization between the header and rollup reads. A response
	// must keep its earlier snapshot, including partial flags and all series.
	trace := &statsReadTrace{}
	trace.OnStart = func(sql string) {
		if !strings.Contains(sql, "FROM corpus_record_totals") {
			return
		}
		trace.OnStart = nil
		for range 4 {
			if _, err = resumed.Bootstrap(ctx, 1); err != nil {
				t.Fatal(err)
			}
		}
	}
	cfg := pool.Config()
	cfg.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	earlier, err := (CorpusStatsStore{Pool: traced}).CorpusStats(ctx, "example", "corpus", content.CorpusStatsQuery{Histogram: true, Sources: true})
	if err != nil || earlier.Complete || earlier.CatalogTotal != 1 || len(earlier.Sources.Items) != 1 || earlier.Sources.Items[0].CatalogCount != 1 {
		t.Fatalf("bootstrap snapshot: %+v %v", earlier, err)
	}

	got, err = resumed.CorpusStats(ctx, "example", "corpus", content.CorpusStatsQuery{Histogram: true, Sources: true})
	if err != nil || !got.Complete || got.Approximate || got.CatalogTotal != 3 || got.CatalogUndatedTotal != 2 || len(got.Histogram.Items) != 1 || got.Histogram.Items[0].CatalogCount != 1 || len(got.Sources.Items) != 1 || got.Sources.Items[0].ConnectorID != "unknown" {
		t.Fatalf("complete bootstrap: %+v %v", got, err)
	}
	if _, err = resumed.CorpusStats(ctx, "foreign", "corpus", content.CorpusStatsQuery{}); err == nil {
		t.Fatal("foreign corpus was visible")
	}
	if _, err = pool.Exec(ctx, `UPDATE corpora SET archived=true WHERE organization='example' AND id='corpus'`); err != nil {
		t.Fatal(err)
	}
	if _, err = resumed.CorpusStats(ctx, "example", "corpus", content.CorpusStatsQuery{}); err == nil {
		t.Fatal("archived corpus was visible")
	}
	if _, err = pool.Exec(ctx, `UPDATE corpora SET archived=false WHERE organization='example' AND id='corpus'`); err != nil {
		t.Fatal(err)
	}
	got, err = resumed.CorpusStats(ctx, "example", "corpus", content.CorpusStatsQuery{})
	if err != nil || got.CatalogTotal != 3 {
		t.Fatalf("restored total: %+v %v", got, err)
	}
}

// Use the same production batch on a connection or transaction; no SQL
// installation or test-only production seam is needed.
func observeCorpusRecords(ctx context.Context, db interface {
	SendBatch(context.Context, *pgx.Batch) pgx.BatchResults
}, orgs, ids []string) error {
	batch := &pgx.Batch{}
	queueCorpusRecordObservations(batch, orgs, ids)
	return db.SendBatch(ctx, batch).Close()
}

// Owns the external PostgreSQL error classification, cheaply and without
// waiting for a server timeout. Only an execution-budget cancellation is broad.
func TestRecordCountErrorClassification(t *testing.T) {
	canceled, stop := context.WithCancel(context.Background())
	stop()
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		input error
		want  error
	}{
		{"execution budget", context.Background(), &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}, content.ErrCountTooBroad},
		{"lock", context.Background(), &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"}, nil},
		{"operator cancellation", context.Background(), &pgconn.PgError{Code: "57014", Message: "canceling statement due to user request"}, nil},
		{"connection", context.Background(), errors.New("connection unavailable"), nil},
		{"caller wins", canceled, &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			if want == nil {
				want = tc.input
			}
			if got := recordCountQueryError(tc.ctx, tc.input); !errors.Is(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
}
