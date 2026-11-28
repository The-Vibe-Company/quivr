package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/visibility"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// acceptMetricsTracer counts network round trips (Query/Exec/Begin/Commit/Rollback
// and SendBatch exchanges), pool connection acquisitions, and SQL statements.
type acceptMetricsTracer struct {
	roundTrips atomic.Int64
	conns      atomic.Int64
	stmts      atomic.Int64
}

func (t *acceptMetricsTracer) reset() {
	t.roundTrips.Store(0)
	t.conns.Store(0)
	t.stmts.Store(0)
}

func (t *acceptMetricsTracer) snapshot() (roundTrips, conns, stmts int64) {
	return t.roundTrips.Load(), t.conns.Load(), t.stmts.Load()
}

func (t *acceptMetricsTracer) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	t.conns.Add(1)
	return ctx
}

func (t *acceptMetricsTracer) TraceAcquireEnd(context.Context, *pgxpool.Pool, pgxpool.TraceAcquireEndData) {
}

func (t *acceptMetricsTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	t.roundTrips.Add(1)
	t.stmts.Add(1)
	return ctx
}

func (t *acceptMetricsTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (t *acceptMetricsTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceBatchStartData) context.Context {
	t.roundTrips.Add(1)
	if d.Batch != nil {
		t.stmts.Add(int64(d.Batch.Len()))
	}
	return ctx
}

func (t *acceptMetricsTracer) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}
func (t *acceptMetricsTracer) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData)     {}

var (
	_ pgx.QueryTracer       = (*acceptMetricsTracer)(nil)
	_ pgx.BatchTracer       = (*acceptMetricsTracer)(nil)
	_ pgxpool.AcquireTracer = (*acceptMetricsTracer)(nil)
)

func acceptTracedPool(tb testing.TB, ctx context.Context, tracer *acceptMetricsTracer) *pgxpool.Pool {
	tb.Helper()
	path := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if path == "" {
		tb.Skip("real PostgreSQL suite runs inside make adapter-postgres")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		tb.Fatal(err)
	}
	var cfg struct {
		DatabaseURL string `json:"database_url"`
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		tb.Fatal(err)
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		tb.Fatal(err)
	}
	if tracer != nil {
		poolCfg.ConnConfig.Tracer = tracer
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(pool.Close)
	return pool
}

// BenchmarkAccept measures roundtrips/op, conns/op, and stmts/op across the
// three primary SubmissionStore.Accept paths: first_revision, correction, replay.
func BenchmarkAccept(b *testing.B) {
	ctx := b.Context()
	tracer := &acceptMetricsTracer{}
	pool := acceptTracedPool(b, ctx, tracer)
	org := fmt.Sprintf("bench-accept-%d", time.Now().UnixNano())
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	c, _, err := (Store{Pool: pool}).Create(ctx, org, corpus.CreateInput{Key: "bench", Name: "Accept bench", Resolved: corpus.Retrieval{}})
	if err != nil {
		b.Fatal(err)
	}
	store := SubmissionStore{Pool: pool}

	b.Run("first_revision", func(b *testing.B) {
		tracer.reset()
		b.ResetTimer()
		for i := range b.N {
			key := fmt.Sprintf("first-%d-%d", time.Now().UnixNano(), i)
			cmd := content.Command{
				Key:      key,
				Source:   content.Source{CorpusID: c.ID, Namespace: "bench", RecordKey: key},
				Revision: "r1",
				Content:  content.Text{Kind: "text", Text: "first revision payload"},
			}
			if _, err := store.Accept(ctx, scope, cmd); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		rt, conns, stmts := tracer.snapshot()
		b.ReportMetric(float64(rt)/float64(b.N), "roundtrips/op")
		b.ReportMetric(float64(conns)/float64(b.N), "conns/op")
		b.ReportMetric(float64(stmts)/float64(b.N), "stmts/op")
	})

	b.Run("correction", func(b *testing.B) {
		seedKey := fmt.Sprintf("corr-seed-%d", time.Now().UnixNano())
		seed := content.Command{
			Key:     seedKey,
			Source:  content.Source{CorpusID: c.ID, Namespace: "bench", RecordKey: seedKey},
			Content: content.Text{Kind: "text", Text: "seed revision"},
		}
		if _, err := store.Accept(ctx, scope, seed); err != nil {
			b.Fatal(err)
		}
		tracer.reset()
		b.ResetTimer()
		for i := range b.N {
			cmd := content.Command{
				Key:      fmt.Sprintf("%s-corr-%d", seedKey, i),
				Source:   seed.Source,
				Revision: fmt.Sprintf("rev-%d", i+1),
				Position: fmt.Sprintf("%010d", i+1),
				Content:  content.Text{Kind: "text", Text: fmt.Sprintf("correction %d", i)},
			}
			if _, err := store.Accept(ctx, scope, cmd); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		rt, conns, stmts := tracer.snapshot()
		b.ReportMetric(float64(rt)/float64(b.N), "roundtrips/op")
		b.ReportMetric(float64(conns)/float64(b.N), "conns/op")
		b.ReportMetric(float64(stmts)/float64(b.N), "stmts/op")
	})

	b.Run("replay", func(b *testing.B) {
		replayKey := fmt.Sprintf("replay-%d", time.Now().UnixNano())
		cmd := content.Command{
			Key:      replayKey,
			Source:   content.Source{CorpusID: c.ID, Namespace: "bench", RecordKey: replayKey},
			Revision: "r1",
			Content:  content.Text{Kind: "text", Text: "replay payload"},
		}
		if _, err := store.Accept(ctx, scope, cmd); err != nil {
			b.Fatal(err)
		}
		tracer.reset()
		b.ResetTimer()
		for range b.N {
			if _, err := store.Accept(ctx, scope, cmd); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		rt, conns, stmts := tracer.snapshot()
		b.ReportMetric(float64(rt)/float64(b.N), "roundtrips/op")
		b.ReportMetric(float64(conns)/float64(b.N), "conns/op")
		b.ReportMetric(float64(stmts)/float64(b.N), "stmts/op")
	})
}

// TestAcceptRoundTripsAndConnectionBudget guards the accept-path round-trip
// and single-connection budgets against real PostgreSQL.
func TestAcceptRoundTripsAndConnectionBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	tracer := &acceptMetricsTracer{}
	pool := acceptTracedPool(t, ctx, tracer)
	org := fmt.Sprintf("accept-rt-%d", time.Now().UnixNano())
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	c, _, err := (Store{Pool: pool}).Create(ctx, org, corpus.CreateInput{Key: "rt", Name: "Round trips", Resolved: corpus.Retrieval{}})
	if err != nil {
		t.Fatal(err)
	}
	store := SubmissionStore{Pool: pool}

	// 1. First revision: <= 4 round trips (baseline 4), 1 conn.
	firstCmd := content.Command{
		Key:      "first-rev-key",
		Source:   content.Source{CorpusID: c.ID, Namespace: "rt", RecordKey: "doc-1"},
		Revision: "v1",
		Content:  content.Text{Kind: "text", Text: "first revision"},
	}
	tracer.reset()
	firstReceipt, err := store.Accept(ctx, scope, firstCmd)
	if err != nil {
		t.Fatal(err)
	}
	rt, conns, stmts := tracer.snapshot()
	t.Logf("first_revision: roundtrips=%d conns=%d stmts=%d", rt, conns, stmts)
	if rt > 4 || conns != 1 || !firstReceipt.NewRevision {
		t.Fatalf("first_revision got rt=%d conns=%d newRevision=%v, want rt<=4 conns=1 newRevision=true", rt, conns, firstReceipt.NewRevision)
	}

	// 2. Replay of pending receipt: <= 1 round trip (baseline 5), 1 conn (baseline 2).
	tracer.reset()
	replayed, err := store.Accept(ctx, scope, firstCmd)
	if err != nil {
		t.Fatal(err)
	}
	rt, conns, stmts = tracer.snapshot()
	t.Logf("replay_pending: roundtrips=%d conns=%d stmts=%d", rt, conns, stmts)
	if rt > 1 || conns != 1 || replayed.ID != firstReceipt.ID || replayed.NewRevision {
		t.Fatalf("replay_pending got rt=%d conns=%d receipt=%+v, want rt<=1 conns=1 id=%s newRevision=false", rt, conns, replayed, firstReceipt.ID)
	}

	// 3. Correction with explicit Revision: <= 5 round trips (baseline 6, 0.83x), 1 conn.
	corrRevCmd := content.Command{
		Key:      "corr-rev-key",
		Source:   firstCmd.Source,
		Revision: "v2",
		Position: "2",
		Content:  content.Text{Kind: "text", Text: "second revision"},
	}
	tracer.reset()
	corrReceipt, err := store.Accept(ctx, scope, corrRevCmd)
	if err != nil {
		t.Fatal(err)
	}
	rt, conns, stmts = tracer.snapshot()
	t.Logf("correction_revision: roundtrips=%d conns=%d stmts=%d", rt, conns, stmts)
	if rt > 5 || conns != 1 || !corrReceipt.NewRevision {
		t.Fatalf("correction_revision got rt=%d conns=%d newRevision=%v, want rt<=5 conns=1 newRevision=true", rt, conns, corrReceipt.NewRevision)
	}

	// 4. Correction without Revision (digest slot): <= 5 round trips (baseline 7, 0.71x), 1 conn.
	corrDigestCmd := content.Command{
		Key:      "corr-digest-key",
		Source:   firstCmd.Source,
		Position: "3",
		Content:  content.Text{Kind: "text", Text: "third digest revision"},
	}
	tracer.reset()
	digestReceipt, err := store.Accept(ctx, scope, corrDigestCmd)
	if err != nil {
		t.Fatal(err)
	}
	rt, conns, stmts = tracer.snapshot()
	t.Logf("correction_digest: roundtrips=%d conns=%d stmts=%d", rt, conns, stmts)
	if rt > 5 || conns != 1 || !digestReceipt.NewRevision {
		t.Fatalf("correction_digest got rt=%d conns=%d newRevision=%v, want rt<=5 conns=1 newRevision=true", rt, conns, digestReceipt.NewRevision)
	}

	// 5. Materialize first receipt and verify resolved replay stays 1 RT / 1 conn
	// and matches ReceiptStore.Receipt byte-for-byte.
	mat := MaterializationStore{Pool: pool}
	work, _, err := mat.Work(ctx, org, firstReceipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	pub := content.Publication{
		Normalized: content.Blob{Key: "rt/text", SHA256: "rt-text-sha", Size: 14},
		Manifest:   content.Blob{Key: "rt/manifest", SHA256: "rt-manifest-sha", Size: 2},
		Parts:      []content.PartBlob{{Key: "body", Role: "body", Blob: content.Blob{Key: "rt/text", SHA256: "rt-text-sha", Size: 14}}},
	}
	if err = mat.Publish(ctx, work, pub); err != nil {
		t.Fatal(err)
	}
	expectedReceipt, err := (ReceiptStore{Pool: pool}).Receipt(ctx, org, firstReceipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	tracer.reset()
	resolvedReplay, err := store.Accept(ctx, scope, firstCmd)
	if err != nil {
		t.Fatal(err)
	}
	rt, conns, stmts = tracer.snapshot()
	t.Logf("replay_resolved: roundtrips=%d conns=%d stmts=%d", rt, conns, stmts)
	if rt > 1 || conns != 1 {
		t.Fatalf("replay_resolved got rt=%d conns=%d, want rt<=1 conns=1", rt, conns)
	}
	wantJSON, _ := json.Marshal(expectedReceipt)
	gotJSON, _ := json.Marshal(resolvedReplay)
	if !bytes.Equal(wantJSON, gotJSON) {
		t.Fatalf("resolved replay mismatch:\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}

	// 6. Quarantined replay on a 1-connection pool must never deadlock or
	// acquire a second connection.
	quarantineCmd := content.Command{
		Key:      "quarantined-1conn-key",
		Source:   content.Source{CorpusID: c.ID, Namespace: "rt", RecordKey: "doc-quarantined"},
		Revision: "v1",
		Content:  content.Text{Kind: "text", Text: "quarantined payload"},
	}
	qReceipt, err := store.Accept(ctx, scope, quarantineCmd)
	if err != nil {
		t.Fatal(err)
	}
	qWork, _, err := mat.Work(ctx, org, qReceipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	qPub := content.Publication{
		Normalized: content.Blob{Key: "rt/q-text", SHA256: "rt-q-text-sha", Size: 19},
		Manifest:   content.Blob{Key: "rt/q-manifest", SHA256: "rt-q-manifest-sha", Size: 2},
		Parts:      []content.PartBlob{{Key: "body", Role: "body", Blob: content.Blob{Key: "rt/q-text", SHA256: "rt-q-text-sha", Size: 19}}},
		Quarantine: &content.Diagnostic{Code: "policy_blocked", Message: "Quarantined in test"},
	}
	if err = mat.Publish(ctx, qWork, qPub); err != nil {
		t.Fatal(err)
	}
	singleTracer := &acceptMetricsTracer{}
	singleCfg := pool.Config().Copy()
	singleCfg.MaxConns = 1
	singleCfg.ConnConfig.Tracer = singleTracer
	singlePool, err := pgxpool.NewWithConfig(ctx, singleCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer singlePool.Close()
	qCtx, qCancel := context.WithTimeout(ctx, 5*time.Second)
	defer qCancel()
	singleTracer.reset()
	qReplay, err := (SubmissionStore{Pool: singlePool}).Accept(qCtx, scope, quarantineCmd)
	if err != nil {
		t.Fatalf("quarantined replay on 1-conn pool failed: %v", err)
	}
	rt, conns, stmts = singleTracer.snapshot()
	t.Logf("replay_quarantined_1conn: roundtrips=%d conns=%d stmts=%d", rt, conns, stmts)
	if conns != 1 || qReplay.Availability == nil || qReplay.Availability.State != "quarantined" ||
		len(qReplay.Diagnostics) != 1 || qReplay.Diagnostics[0].Code != "policy_blocked" {
		t.Fatalf("quarantined replay got conns=%d receipt=%+v", conns, qReplay)
	}
}

// TestAcceptConcurrentReplaysConverge verifies that 50 concurrent accepts of
// the same idempotency key converge on 1 Receipt with 49 replays, 50 warm
// replays return byte-identical receipts, and parallel claim locking produces
// zero dead-tuple rewrites (n_tup_upd == 0) on journal_request_claims.
func TestAcceptConcurrentReplaysConverge(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool := acceptTracedPool(t, ctx, nil)
	org := fmt.Sprintf("accept-conc-%d", time.Now().UnixNano())
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	c, _, err := (Store{Pool: pool}).Create(ctx, org, corpus.CreateInput{Key: "conc", Name: "Concurrent", Resolved: corpus.Retrieval{}})
	if err != nil {
		t.Fatal(err)
	}
	store := SubmissionStore{Pool: pool}
	const workers = 50
	cmd := content.Command{
		Key:      "concurrent-50-key",
		Source:   content.Source{CorpusID: c.ID, Namespace: "conc", RecordKey: "doc-50"},
		Revision: "r1",
		Content:  content.Text{Kind: "text", Text: "concurrent replay payload"},
	}

	// Cold race: 50 simultaneous accepts for a brand-new key.
	start := make(chan struct{})
	receipts := make([]content.Receipt, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			receipts[idx], errs[idx] = store.Accept(ctx, scope, cmd)
		}(i)
	}
	close(start)
	wg.Wait()

	newRevisions := 0
	var canonicalID string
	for i := range workers {
		if errs[i] != nil {
			t.Fatalf("cold worker %d failed: %v", i, errs[i])
		}
		if i == 0 {
			canonicalID = receipts[i].ID
		} else if receipts[i].ID != canonicalID {
			t.Fatalf("worker %d receipt ID %q != %q", i, receipts[i].ID, canonicalID)
		}
		if receipts[i].NewRevision {
			newRevisions++
		}
	}
	if newRevisions != 1 {
		t.Fatalf("cold race newRevisions=%d, want 1", newRevisions)
	}
	var dbReceipts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ingestion_receipts WHERE organization=$1 AND request_key=$2`, org, cmd.Key).Scan(&dbReceipts); err != nil {
		t.Fatal(err)
	}
	if dbReceipts != 1 {
		t.Fatalf("ingestion_receipts count=%d, want 1", dbReceipts)
	}

	// Warm replays: 50 concurrent replays must return 50 byte-identical Receipts.
	startWarm := make(chan struct{})
	warmJSON := make([][]byte, workers)
	for i := range workers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-startWarm
			r, err := store.Accept(ctx, scope, cmd)
			errs[idx] = err
			if err == nil {
				warmJSON[idx], errs[idx] = json.Marshal(r)
			}
		}(i)
	}
	close(startWarm)
	wg.Wait()
	for i := range workers {
		if errs[i] != nil {
			t.Fatalf("warm replay %d failed: %v", i, errs[i])
		}
		if i > 0 && !bytes.Equal(warmJSON[i], warmJSON[0]) {
			t.Fatalf("warm replay %d differed:\ngot:  %s\nwant: %s", i, warmJSON[i], warmJSON[0])
		}
	}

	// Parallel claim lock check: repeated lockJournalRequest / lockJournalRecord
	// in parallel mode must execute zero tuple updates on journal_request_claims.
	journal := JournalStore{Pool: pool}
	if err := journal.SetParallel(ctx, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.SetParallel(context.Background(), false) })

	var updBefore int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(pg_stat_get_tuples_updated('journal_request_claims'::regclass), 0)`).Scan(&updBefore); err != nil {
		t.Fatal(err)
	}
	recordID := content.StableID("record", org, c.ID, "conc", "doc-50")
	for range 20 {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := lockJournalRequest(ctx, tx, org, "ingestion", cmd.Key); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := lockJournalRecord(ctx, tx, org, recordID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := lockJournalRecords(ctx, tx, org, []string{recordID, recordID}); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var updAfter int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(pg_stat_get_tuples_updated('journal_request_claims'::regclass), 0)`).Scan(&updAfter); err != nil {
		t.Fatal(err)
	}
	if updAfter != updBefore {
		t.Fatalf("journal_request_claims tuple updates increased by %d (before=%d after=%d), want 0", updAfter-updBefore, updBefore, updAfter)
	}
}

// TestAcceptDeadlockRetryRealPostgres injects 5 consecutive 40P01 deadlocks on
// ingestion_receipts INSERT and proves SubmissionStore.Accept recovers cleanly
// without double-committing.
func TestAcceptDeadlockRetryRealPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool := acceptTracedPool(t, ctx, nil)
	org := fmt.Sprintf("accept-deadlock-%d", time.Now().UnixNano())
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	c, _, err := (Store{Pool: pool}).Create(ctx, org, corpus.CreateInput{Key: "dl", Name: "Deadlock", Resolved: corpus.Retrieval{}})
	if err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprint(time.Now().UnixNano())
	seq := pgx.Identifier{"accept_dl_seq_" + suffix}.Sanitize()
	fn := pgx.Identifier{"accept_dl_fn_" + suffix}.Sanitize()
	trg := pgx.Identifier{"accept_dl_trg_" + suffix}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE SEQUENCE "+seq); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP TRIGGER IF EXISTS "+trg+" ON ingestion_receipts")
		_, _ = pool.Exec(context.Background(), "DROP FUNCTION IF EXISTS "+fn+"()")
		_, _ = pool.Exec(context.Background(), "DROP SEQUENCE IF EXISTS "+seq)
	})
	triggerSQL := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.organization='%s' AND NEW.request_key='dl-5' AND nextval('%s')<=5
 THEN RAISE EXCEPTION 'injected deadlock' USING ERRCODE='40P01'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER %s BEFORE INSERT ON ingestion_receipts FOR EACH ROW EXECUTE FUNCTION %s()`, fn, org, seq, trg, fn)
	if _, err := pool.Exec(ctx, triggerSQL); err != nil {
		t.Fatal(err)
	}
	store := SubmissionStore{Pool: pool}
	cmd := content.Command{
		Key:      "dl-5",
		Source:   content.Source{CorpusID: c.ID, Namespace: "dl", RecordKey: "dl-5"},
		Revision: "r1",
		Content:  content.Text{Kind: "text", Text: "deadlock recovery payload"},
	}
	receipt, err := store.Accept(ctx, scope, cmd)
	if err != nil {
		t.Fatalf("Accept failed across 5 injected 40P01 deadlocks: %v", err)
	}
	if !receipt.NewRevision {
		t.Fatalf("expected NewRevision=true after deadlock recovery, got %+v", receipt)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ingestion_receipts WHERE organization=$1 AND request_key='dl-5'`, org).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("receipts committed=%d, want 1", count)
	}
}

// TestJournalRetryDeadlockBackoffAndCancellation exercises 40P01 retry bounds,
// bounded exponential backoff, and ctx.Done() cancellation without Docker.
func TestJournalRetryDeadlockBackoffAndCancellation(t *testing.T) {
	deadlockErr := func() error {
		return &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}
	}

	// Succeeds across 1..5 injected deadlocks without double-commit.
	for injected := 1; injected <= 5; injected++ {
		t.Run(fmt.Sprintf("recovers-after-%d-deadlocks", injected), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				attempts := 0
				commits := 0
				err := retryJournalWriteWithLimit(t.Context(), "Accept", acceptJournalRetryMaxAttempts, func(ctx context.Context) error {
					attempts++
					if attempts <= injected {
						return deadlockErr()
					}
					commits++
					return nil
				})
				if err != nil {
					t.Fatalf("injected=%d: got err %v, want nil", injected, err)
				}
				if attempts != injected+1 || commits != 1 {
					t.Fatalf("injected=%d: attempts=%d commits=%d, want %d/1", injected, attempts, commits, injected+1)
				}
			})
		})
	}

	t.Run("exhausts-at-explicit-max-attempts", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			attempts := 0
			err := retryJournalWriteWithLimit(t.Context(), "Accept", acceptJournalRetryMaxAttempts, func(ctx context.Context) error {
				attempts++
				return deadlockErr()
			})
			if !isDeadlock(err) || attempts != acceptJournalRetryMaxAttempts {
				t.Fatalf("attempts=%d err=%v, want %d and 40P01", attempts, err, acceptJournalRetryMaxAttempts)
			}
		})
	})

	t.Run("default-retry-preserves-3-attempt-bound-regardless-of-label", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			for _, op := range []string{"Audit.Record", "Renamed.Audit.Op"} {
				attempts := 0
				err := retryJournalWrite(t.Context(), op, func(ctx context.Context) error {
					attempts++
					return deadlockErr()
				})
				if !isDeadlock(err) || attempts != defaultJournalRetryMaxAttempts {
					t.Fatalf("op=%q attempts=%d err=%v, want %d and 40P01", op, attempts, err, defaultJournalRetryMaxAttempts)
				}
			}
		})
	})

	t.Run("non-deadlock-error-does-not-retry", func(t *testing.T) {
		attempts := 0
		want := &pgconn.PgError{Code: "23505", Message: "unique violation"}
		err := retryJournalWriteWithLimit(t.Context(), "Accept", acceptJournalRetryMaxAttempts, func(ctx context.Context) error {
			attempts++
			return want
		})
		if !errors.Is(err, want) || attempts != 1 {
			t.Fatalf("attempts=%d err=%v, want 1 and %v", attempts, err, want)
		}
	})

	t.Run("context-cancel-during-backoff-returns-promptly-without-leaked-tx", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			inAttempt2 := make(chan struct{})
			var openTxs atomic.Int32
			var attempts atomic.Int32
			done := make(chan error, 1)
			go func() {
				done <- retryJournalWriteWithLimit(ctx, "Accept", acceptJournalRetryMaxAttempts, func(work context.Context) error {
					n := attempts.Add(1)
					openTxs.Add(1)
					defer openTxs.Add(-1) // mirrors defer rollbackTx(ctx, tx)
					if n == 2 {
						close(inAttempt2)
					}
					return deadlockErr()
				})
			}()
			<-inAttempt2
			// Wait until the retry goroutine is durably blocked on the backoff
			// timer select so cancel() deterministically interrupts mid-backoff.
			synctest.Wait()
			cancel()
			err := <-done
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err=%v, want context.Canceled", err)
			}
			if got := attempts.Load(); got != 2 {
				t.Fatalf("attempts=%d, want 2 (canceled mid-backoff)", got)
			}
			if got := openTxs.Load(); got != 0 {
				t.Fatalf("openTxs=%d after canceled backoff, want 0", got)
			}
		})
	})

	t.Run("already-canceled-context-does-not-retry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		attempts := 0
		err := retryJournalWriteWithLimit(ctx, "Accept", acceptJournalRetryMaxAttempts, func(work context.Context) error {
			attempts++
			cancel()
			return deadlockErr()
		})
		if !errors.Is(err, context.Canceled) || attempts != 1 {
			t.Fatalf("attempts=%d err=%v, want 1 and context.Canceled", attempts, err)
		}
	})

	t.Run("backoff-is-positive-and-bounded", func(t *testing.T) {
		for attempt := 1; attempt <= 20; attempt++ {
			for range 16 {
				d := journalRetryBackoff(attempt)
				if d < time.Millisecond || d > journalRetryMaxBackoff {
					t.Fatalf("attempt=%d backoff=%s outside [1ms, %s]", attempt, d, journalRetryMaxBackoff)
				}
			}
		}
	})
}

// TestJournalClaimSQLUsesDoNothingAndSelectForUpdate verifies the compiled SQL
// for journal_request_claims lock acquisition never issues no-op DO UPDATE
// tuple rewrites.
func TestJournalClaimSQLUsesDoNothingAndSelectForUpdate(t *testing.T) {
	if strings.Contains(strings.ToUpper(journalClaimInsertSQL), "DO UPDATE") {
		t.Fatalf("journalClaimInsertSQL still performs DO UPDATE: %s", journalClaimInsertSQL)
	}
	if !strings.Contains(strings.ToUpper(journalClaimInsertSQL), "ON CONFLICT(ORGANIZATION,FAMILY,REQUEST_KEY) DO NOTHING") {
		t.Fatalf("journalClaimInsertSQL missing ON CONFLICT DO NOTHING: %s", journalClaimInsertSQL)
	}
	if !strings.Contains(strings.ToUpper(journalClaimLockSQL), "FOR UPDATE") || !strings.Contains(journalClaimLockSQL, "journal_request_claims") {
		t.Fatalf("journalClaimLockSQL missing SELECT FOR UPDATE on journal_request_claims: %s", journalClaimLockSQL)
	}

	batch := &pgx.Batch{}
	queueJournalClaim(batch, "org-1", "ingestion", "key-1")
	if batch.Len() != 2 {
		t.Fatalf("queueJournalClaim queued %d queries, want 2", batch.Len())
	}
	if batch.QueuedQueries[0].SQL != journalClaimInsertSQL || batch.QueuedQueries[1].SQL != journalClaimLockSQL {
		t.Fatalf("queueJournalClaim queued unexpected SQL order: %+v", batch.QueuedQueries)
	}

	deliveries := &pgx.Batch{}
	queueDeliveryResourceLocks(deliveries, "org-1", []string{"del-1"})
	if deliveries.Len() != 4 {
		t.Fatalf("queueDeliveryResourceLocks queued %d queries, want 4", deliveries.Len())
	}
	if strings.Contains(strings.ToUpper(deliveries.QueuedQueries[0].SQL), "DO UPDATE") ||
		!strings.Contains(strings.ToUpper(deliveries.QueuedQueries[0].SQL), "DO NOTHING") {
		t.Fatalf("queueDeliveryResourceLocks[0] must use DO NOTHING: %s", deliveries.QueuedQueries[0].SQL)
	}
	if !strings.Contains(deliveries.QueuedQueries[1].SQL, "journal_request_claims") ||
		!strings.Contains(strings.ToUpper(deliveries.QueuedQueries[1].SQL), "FOR UPDATE") {
		t.Fatalf("queueDeliveryResourceLocks[1] must lock journal_request_claims FOR UPDATE: %s", deliveries.QueuedQueries[1].SQL)
	}
}

// TestAcceptLookupAndHydrationContracts verifies tracer accounting, digest
// matching, digest slot resolution, visibility predicate construction, and
// inline Receipt hydration without requiring Docker.
func TestAcceptLookupAndHydrationContracts(t *testing.T) {
	t.Run("metrics-tracer", func(t *testing.T) {
		tracer := &acceptMetricsTracer{}
		ctx := t.Context()
		tracer.TraceAcquireStart(ctx, nil, pgxpool.TraceAcquireStartData{})
		tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "BEGIN"})
		batch := &pgx.Batch{}
		batch.Queue("SELECT 1")
		batch.Queue("SELECT 2")
		tracer.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{Batch: batch})
		tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "COMMIT"})
		rt, conns, stmts := tracer.snapshot()
		if rt != 3 || conns != 1 || stmts != 4 {
			t.Fatalf("tracer got rt=%d conns=%d stmts=%d, want 3/1/4", rt, conns, stmts)
		}
	})

	t.Run("request-matches-digest", func(t *testing.T) {
		canonical := []byte(`{"idempotency_key":"k1"}`)
		sum := sha256.Sum256(canonical)
		if !requestMatchesDigest(nil, sum[:], canonical, sum) {
			t.Fatal("matching digest should succeed")
		}
		other := sha256.Sum256([]byte(`{"idempotency_key":"k2"}`))
		if requestMatchesDigest(nil, other[:], canonical, sum) {
			t.Fatal("mismatched digest must fail")
		}
		if !requestMatchesDigest(canonical, nil, canonical, sum) {
			t.Fatal("legacy canonical byte fallback should succeed")
		}
		if requestMatchesDigest([]byte(`{}`), nil, canonical, sum) {
			t.Fatal("legacy canonical byte mismatch must fail")
		}
	})

	t.Run("resolve-digest-slot", func(t *testing.T) {
		if got, err := resolveDigestSlot("digest:abc", "", false, pgx.ErrNoRows, 2, "2", "1"); err != nil || got != "digest:abc" {
			t.Fatalf("no rows: got %q err=%v", got, err)
		}
		if got, err := resolveDigestSlot("digest:abc", "digest:abc", true, nil, 2, "2", "1"); err != nil || got != "digest:abc" {
			t.Fatalf("already desired: got %q err=%v", got, err)
		}
		if got, err := resolveDigestSlot("digest:abc", "digest:abc", false, nil, 3, "1", "2"); err != nil || got != "digest:abc" {
			t.Fatalf("older position: got %q err=%v", got, err)
		}
		if got, err := resolveDigestSlot("digest:abc", "digest:abc", false, nil, 3, "3", "2"); err != nil || got != "digest:abc@3" {
			t.Fatalf("revert to earlier bytes: got %q err=%v", got, err)
		}
	})

	t.Run("build-lookup-query-visibility", func(t *testing.T) {
		qPlain, argsPlain := buildAcceptLookupQuery(t.Context(), "org", "ingestion", "key", "rec", "corp")
		if len(argsPlain) != 5 || strings.Contains(qPlain, "visibility_grants") {
			t.Fatalf("plain lookup unexpected: len(args)=%d sql=%s", len(argsPlain), qPlain)
		}
		visCtx := visibility.WithPrincipals(t.Context(), []string{"group:readers"})
		qVis, argsVis := buildAcceptLookupQuery(visCtx, "org", "ingestion", "key", "rec", "corp")
		if len(argsVis) != 6 || !strings.Contains(qVis, "visibility_grants") || !strings.Contains(qVis, "$6") {
			t.Fatalf("visibility lookup unexpected: len(args)=%d sql=%s", len(argsVis), qVis)
		}
	})

	t.Run("hydrate-receipt-states", func(t *testing.T) {
		rs := ReceiptStore{}
		// Pending receipt
		r, err := rs.hydrateReceipt(t.Context(), nil, "org", replayReceiptRow{
			receipt: content.Receipt{
				ID:         "rc-1",
				State:      "pending",
				RecordID:   "rec-1",
				Processing: content.Processing{State: "queued"},
			},
		})
		if err != nil || r.Processing.Phase != "materialization" || r.Availability != nil || len(r.Diagnostics) != 0 {
			t.Fatalf("pending receipt: %+v err=%v", r, err)
		}
		// Retrieval-ready current Version with structured enrichment_reason
		reasonBytes, _ := json.Marshal(content.Diagnostic{Code: "enrichment_paused", Message: "Paused by operator"})
		r, err = rs.hydrateReceipt(t.Context(), nil, "org", replayReceiptRow{
			receipt: content.Receipt{
				ID:        "rc-2",
				State:     "resolved",
				Outcome:   "created",
				RecordID:  "rec-1",
				VersionID: "ver-1",
			},
			baseline:        true,
			current:         true,
			enrichmentState: "blocked",
			enrichmentError: "enrichment_paused",
			enrichmentRaw:   reasonBytes,
		})
		if err != nil || r.Availability == nil || r.Availability.State != "retrieval_ready" || !r.Availability.Searchable ||
			r.Processing.State != "blocked" || r.Processing.Phase != "enrichment" ||
			len(r.Diagnostics) != 1 || r.Diagnostics[0].Code != "enrichment_paused" || r.Diagnostics[0].Message != "Paused by operator" {
			t.Fatalf("enrichment-blocked receipt: %+v err=%v", r, err)
		}
		// Retrying baseline error
		r, err = rs.hydrateReceipt(t.Context(), nil, "org", replayReceiptRow{
			receipt: content.Receipt{
				ID:        "rc-3",
				State:     "resolved",
				Outcome:   "created",
				RecordID:  "rec-1",
				VersionID: "ver-2",
			},
			current:     true,
			vProcessing: "retrying",
			vErrorCode:  "transient_error",
		})
		if err != nil || r.Availability == nil || r.Availability.State != "building_baseline" || r.Availability.Searchable ||
			r.Processing.Phase != "baseline" || len(r.Diagnostics) != 1 || !r.Diagnostics[0].Retryable {
			t.Fatalf("retrying baseline receipt: %+v err=%v", r, err)
		}
		// Quarantined Version hydrates structured diagnostic without needing Pool.
		qBytes, _ := json.Marshal(content.Diagnostic{Code: "unsafe_input", Message: "Blocked by policy"})
		r, err = rs.hydrateReceipt(t.Context(), nil, "org", replayReceiptRow{
			receipt: content.Receipt{
				ID:        "rc-4",
				State:     "resolved",
				Outcome:   "created",
				RecordID:  "rec-1",
				VersionID: "ver-3",
			},
			quarantined:   true,
			current:       true,
			vProcessing:   "blocked",
			vErrorCode:    "unsafe_input",
			quarantineRaw: qBytes,
		})
		if err != nil || r.Availability == nil || r.Availability.State != "quarantined" || r.Availability.Current || r.Availability.Searchable ||
			len(r.Diagnostics) != 1 || r.Diagnostics[0].Code != "unsafe_input" || r.Diagnostics[0].Message != "Blocked by policy" || r.Diagnostics[0].Retryable {
			t.Fatalf("quarantined receipt: %+v err=%v", r, err)
		}
	})
}
