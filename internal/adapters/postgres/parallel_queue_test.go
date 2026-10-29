package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5/pgxpool"
)

type parallelQueueReconciliationResult struct {
	more bool
	err  error
}

// Authoring gate: this owns the PostgreSQL concurrency contract that parallel
// producers can commit while a reconciler waits on hashed aggregate rows, and
// that a correction can advance an already-dirty Record without waiting for
// that reconciler. Existing queue owners cover legacy recounts, maintenance
// repair, and stale snapshot reads independently; none holds the aggregate
// stripes in one pool while exercising real SubmissionStore.Accept in another.
// The regression is a reconciler that takes a Record observation lock before
// its aggregate lock, which would block the correction and transfer the old
// counter wait into the producer transaction.
func TestParallelQueueProducersStayAvailableUntilBoundedDrain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool := adapterPool(t, ctx)
	org := fmt.Sprintf("adapter-parallel-queue-%d", time.Now().UnixNano())
	corpora := postgres.Store{Pool: pool}
	c, _, err := corpora.Create(ctx, org, corpus.CreateInput{Key: "parallel-queue", Name: "Parallel queue"})
	if err != nil {
		t.Fatal(err)
	}
	scope := corpus.Scope{Organization: org, Corpora: []string{c.ID}}
	submissions := postgres.SubmissionStore{Pool: pool}
	snapshots := postgres.QueueSnapshots{Pool: pool}

	// Finish bootstrap without forcing an unrelated full-installation repair.
	// Legacy producers maintain the new fixture's contributions synchronously.
	for {
		more, err := snapshots.Maintain(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	unrelated, err := submissions.Accept(ctx, scope, content.Command{
		Key:     "unrelated",
		Source:  content.Source{CorpusID: c.ID, Namespace: "parallel", RecordKey: "unrelated"},
		Content: content.Text{Kind: "text", Text: "unrelated"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE queue_backlog_snapshots SET observed_at='-infinity',published_at='-infinity'`); err != nil {
		t.Fatal(err)
	}
	if err = snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	baseline, err := snapshots.QueueBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var baselineWaiting, baselineIngestion int64
	for _, status := range baseline {
		if status.Queue == "live" {
			baselineWaiting = status.Waiting
			baselineIngestion = *status.IngestionWaiting
		}
	}
	baselineMetric, _, err := (postgres.MaterializationStore{Pool: pool}).IngestionBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	journal := postgres.JournalStore{Pool: pool}
	if err = journal.SetParallel(ctx, true); err != nil {
		t.Fatal(err)
	}
	parallel := true
	defer func() {
		if parallel {
			if err := journal.SetParallel(context.Background(), false); err != nil {
				t.Logf("restore legacy journal mode: %v", err)
			}
		}
	}()

	dirty, err := submissions.Accept(ctx, scope, content.Command{
		Key:     "dirty",
		Source:  content.Source{CorpusID: c.ID, Namespace: "parallel", RecordKey: "dirty"},
		Content: content.Text{Kind: "text", Text: "first"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var revision, applied int64
	if err = pool.QueryRow(ctx, `SELECT revision,applied_revision FROM parallel_queue_observations WHERE organization=$1 AND record_id=$2`, org, dirty.RecordID).Scan(&revision, &applied); err != nil {
		t.Fatal(err)
	}
	if revision <= applied {
		t.Fatalf("accepted Record was not dirty: revision=%d applied=%d", revision, applied)
	}

	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	blockerReleased := false
	defer func() {
		if !blockerReleased {
			_ = blocker.Rollback(context.Background())
		}
	}()
	var lockedRecord string
	if err = blocker.QueryRow(ctx, `SELECT record_id FROM queue_enrichment_records WHERE organization=$1 AND record_id=$2 FOR UPDATE`, org, unrelated.RecordID).Scan(&lockedRecord); err != nil {
		t.Fatal(err)
	}
	if lockedRecord != unrelated.RecordID {
		t.Fatalf("locked enrichment Record=%q, want %q", lockedRecord, unrelated.RecordID)
	}
	var backlogRows int
	rows, err := blocker.Query(ctx, `SELECT queue FROM queue_backlog_totals WHERE stripe=(hashtextextended($1,0)&63)::integer ORDER BY queue FOR UPDATE`, org)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		backlogRows++
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if backlogRows != 2 {
		t.Fatalf("locked backlog rows=%d, want live and bulk", backlogRows)
	}
	var metricRows int
	rows, err = blocker.Query(ctx, `SELECT source FROM queue_metric_totals WHERE stripe=(hashtextextended($1,0)&63)::integer ORDER BY source FOR UPDATE`, org)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		metricRows++
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if metricRows != 3 {
		t.Fatalf("locked metric rows=%d, want three source stripes", metricRows)
	}

	reconcilerConfig := pool.Config().Copy()
	reconcilerConfig.MaxConns = 1
	if reconcilerConfig.ConnConfig.RuntimeParams == nil {
		reconcilerConfig.ConnConfig.RuntimeParams = map[string]string{}
	}
	const reconcilerApplication = "parallel-queue-reconciler"
	reconcilerConfig.ConnConfig.RuntimeParams["application_name"] = reconcilerApplication
	reconcilerPool, err := pgxpool.NewWithConfig(ctx, reconcilerConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()); reconcilerPool.Close() }()
	reconciled := make(chan parallelQueueReconciliationResult, 1)
	go func() {
		more, err := (postgres.QueueSnapshots{Pool: reconcilerPool}).ReconcileParallelQueue(ctx, 1)
		reconciled <- parallelQueueReconciliationResult{more: more, err: err}
	}()
	waitForParallelQueueLock(t, ctx, pool, reconcilerApplication, reconciled)

	// Both writes use the real acceptance boundary while the reconciler is
	// waiting on the old organization's aggregate rows. The correction shares
	// the dirty Record; it would block if reconciliation locked that Record
	// before waiting on the aggregate stripe.
	newReceipt, err := submissions.Accept(ctx, scope, content.Command{
		Key:     "new-record",
		Source:  content.Source{CorpusID: c.ID, Namespace: "parallel", RecordKey: "new-record"},
		Content: content.Text{Kind: "text", Text: "new"},
	})
	if err != nil {
		t.Fatalf("new Record acceptance behind held queue counters: %v", err)
	}
	correctionCtx, stopCorrection := context.WithTimeout(ctx, 3*time.Second)
	correction, err := submissions.Accept(correctionCtx, scope, content.Command{
		Key:     "dirty-correction",
		Source:  dirty.Source,
		Content: content.Text{Kind: "text", Text: "corrected"},
	})
	stopCorrection()
	if err != nil {
		t.Fatalf("dirty Record correction waited on the reconciler observation lock: %v", err)
	}
	if correction.RecordID != dirty.RecordID || !correction.NewRevision || newReceipt.RecordID == dirty.RecordID {
		t.Fatalf("acceptance identities new=%+v dirty=%+v", newReceipt, correction)
	}
	if !parallelQueueReconcilerStillWaiting(t, ctx, pool, reconcilerApplication, reconciled) {
		t.Fatal("reconciler finished before aggregate rows were released")
	}

	if err = pool.QueryRow(ctx, `SELECT count(*) FROM parallel_queue_observations WHERE revision<>applied_revision`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 2 {
		t.Fatalf("dirty Record count after two parallel accepts=%d, want target and new Record", revision)
	}
	if _, err = pool.Exec(ctx, `UPDATE queue_backlog_snapshots SET published_at='-infinity'`); err != nil {
		t.Fatal(err)
	}
	if err = snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	prior, err := snapshots.QueueBacklog(ctx)
	if err != nil {
		t.Fatalf("valid older snapshot unavailable during bounded reconciliation: %v", err)
	}
	for _, status := range prior {
		if status.Queue == "live" && status.Waiting != baselineWaiting {
			t.Fatalf("bounded older totals changed before reconciliation: %+v baseline=%d", status, baselineWaiting)
		}
	}
	if pending, _, err := (postgres.MaterializationStore{Pool: pool}).IngestionBacklog(ctx); err != nil || pending != baselineMetric {
		t.Fatalf("bounded older metric pending=%d baseline=%d err=%v", pending, baselineMetric, err)
	}
	// Advance stored freshness clocks, never the wall clock. A continuously
	// edited dirty Record must not reset the start of its unresolved interval.
	if _, err = pool.Exec(ctx, `UPDATE parallel_queue_observations SET dirty_since=clock_timestamp()-interval '2 minutes' WHERE revision<>applied_revision`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE queue_backlog_snapshots SET observed_at=clock_timestamp()-interval '2 minutes',ingestion_observed_at=clock_timestamp()-interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	if _, err = snapshots.QueueBacklog(ctx); err == nil {
		t.Fatal("expired snapshot reported as fresh")
	}
	if _, _, err = (postgres.MaterializationStore{Pool: pool}).IngestionBacklog(ctx); err == nil {
		t.Fatal("expired aggregate reported as fresh")
	}

	if err = blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	blockerReleased = true
	result := <-reconciled
	if result.err != nil {
		t.Fatal(result.err)
	}
	for result.more {
		result.more, result.err = (postgres.QueueSnapshots{Pool: pool}).ReconcileParallelQueue(ctx, 1000)
		if result.err != nil {
			t.Fatal(result.err)
		}
	}

	if err = pool.QueryRow(ctx, `SELECT count(*) FROM parallel_queue_observations WHERE revision<>applied_revision`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 0 {
		t.Fatalf("dirty revisions remain after bounded drain: %d", revision)
	}
	if err = snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	var canonicalPending, canonicalDocuments int64
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ingestion_receipts WHERE organization=$1 AND route_family='ingestion' AND state='pending'`, org).Scan(&canonicalPending); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM queue_backlog_documents WHERE organization=$1 AND queue='live' AND ready`, org).Scan(&canonicalDocuments); err != nil {
		t.Fatal(err)
	}
	statuses, err := snapshots.QueueBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var liveWaiting, liveIngestion int64
	for _, status := range statuses {
		if status.Queue != "live" {
			continue
		}
		liveWaiting = status.Waiting
		if status.IngestionWaiting == nil {
			t.Fatal("refreshed live snapshot omitted ingestion waiting")
		}
		liveIngestion = *status.IngestionWaiting
	}
	if liveWaiting != baselineWaiting+canonicalDocuments-1 || liveIngestion != baselineIngestion+canonicalDocuments-1 {
		t.Fatalf("snapshot waiting=%d ingestion=%d, canonical documents=%d", liveWaiting, liveIngestion, canonicalDocuments)
	}
	metricPending, _, err := (postgres.MaterializationStore{Pool: pool}).IngestionBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if metricPending != baselineMetric+canonicalPending-1 {
		t.Fatalf("ingestion metric pending=%d, canonical pending receipts=%d", metricPending, canonicalPending)
	}

	if err = journal.SetParallel(ctx, false); err != nil {
		t.Fatal(err)
	}
	parallel = false
}

func waitForParallelQueueLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, application string, results <-chan parallelQueueReconciliationResult) {
	t.Helper()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(
			SELECT FROM pg_stat_activity
			WHERE application_name=$1 AND wait_event_type='Lock'
			  AND query ~ 'queue_(backlog|metric)_totals'
		)`, application).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case result := <-results:
			t.Fatalf("reconciler completed before aggregate lock: more=%v err=%v", result.more, result.err)
		default:
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
	}
}

func parallelQueueReconcilerStillWaiting(t *testing.T, ctx context.Context, pool *pgxpool.Pool, application string, results <-chan parallelQueueReconciliationResult) bool {
	t.Helper()
	var waiting bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT FROM pg_stat_activity
		WHERE application_name=$1 AND wait_event_type='Lock'
		  AND query ~ 'queue_(backlog|metric)_totals'
	)`, application).Scan(&waiting); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-results:
		t.Fatalf("reconciler completed while aggregate rows were held: more=%v err=%v", result.more, result.err)
	default:
	}
	return waiting
}
