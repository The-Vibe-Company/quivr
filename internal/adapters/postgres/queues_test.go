package postgres_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/app"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost/fakeplugin"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// This adapter test owns the document-count contract: a pending Receipt and
// its materialized Version are one document, queue origin follows the Receipt,
// and an active durable attempt moves that document from waiting to in
// progress. The rows are read through the real PostgreSQL adapter, so a query
// that accidentally counts workflows/jobs instead of documents fails here.
func TestQueueBacklogCountsDistinctDocumentsAndActiveAttempts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := rebuildAdapterPool(t, ctx)
	org := fmt.Sprintf("adapter-queues-%d", time.Now().UnixNano())
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read", "monitoring:read", "monitoring:write"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "queues", Name: "Queues"})
	if err != nil {
		t.Fatal(err)
	}
	stores := contentStores(pool)
	service := content.Service{Submissions: stores, Receipts: stores, RecordStore: stores, Versions: stores, Materialization: stores}
	reader := postgres.QueueSnapshots{Pool: pool}
	queueFixtureReconcile(t, reader, ctx)
	before := readQueueStatus(t, reader, ctx)

	live, err := service.Accept(ctx, scope, content.Command{
		Key:     "live",
		Source:  content.Source{CorpusID: c.ID, Namespace: "tests", RecordKey: "live"},
		Content: content.Text{Kind: "text", Text: "live"},
	})
	if err != nil {
		t.Fatal(err)
	}
	bulkCtx := workqueue.WithClass(ctx, workqueue.Bulk)
	bulk, err := service.Accept(bulkCtx, scope, content.Command{
		Key:     "bulk",
		Source:  content.Source{CorpusID: c.ID, Namespace: "tests", RecordKey: "bulk"},
		Content: content.Text{Kind: "text", Text: "bulk"},
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted := readQueueStatus(t, reader, ctx)
	if accepted[workqueue.Live].Waiting != before[workqueue.Live].Waiting+1 || accepted[workqueue.Bulk].Waiting != before[workqueue.Bulk].Waiting+1 {
		t.Fatalf("application enqueue totals=%+v over %+v", accepted, before)
	}
	// Make the oldest age deterministic without waiting on wall time.
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE ingestion_receipts SET accepted_at=clock_timestamp()-interval '30 seconds' WHERE organization=$1 AND id=$2`, org, live.ID); err != nil {
		t.Fatal(err)
	}

	work, _, err := stores.Work(ctx, org, bulk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = stores.Publish(ctx, work, publication(
		content.Blob{Key: "queues/bulk", SHA256: "queues-bulk-text", Size: 4},
		content.Blob{Key: "queues/bulk-manifest", SHA256: "queues-bulk-manifest", Size: 2},
	)); err != nil {
		t.Fatal(err)
	}
	var versionID string
	if err = pool.QueryRow(ctx, `SELECT version_id FROM ingestion_receipts WHERE organization=$1 AND id=$2`, org, bulk.ID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	// A replay window can briefly expose the pending receipt alongside the
	// Version it already reserved. Both rows must still count once.
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE ingestion_receipts SET state='pending',outcome=NULL,processing='queued' WHERE organization=$1 AND id=$2`, org, bulk.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE record_versions SET processing='queued',baseline_ready=false WHERE organization=$1 AND id=$2`, org, versionID); err != nil {
		t.Fatal(err)
	}

	status := readQueueStatus(t, reader, ctx)
	if status[workqueue.Live].Waiting != before[workqueue.Live].Waiting+1 || status[workqueue.Live].InProgress != before[workqueue.Live].InProgress {
		t.Fatalf("live backlog = %+v, want one new waiting document over %+v", status[workqueue.Live], before[workqueue.Live])
	}
	if status[workqueue.Bulk].IngestionWaiting == nil || before[workqueue.Bulk].IngestionWaiting == nil || *status[workqueue.Bulk].IngestionWaiting != *before[workqueue.Bulk].IngestionWaiting+1 {
		t.Fatalf("pending receipt and pre-baseline version must count once: %+v over %+v", status[workqueue.Bulk], before[workqueue.Bulk])
	}
	if status[workqueue.Bulk].Waiting != before[workqueue.Bulk].Waiting+1 || status[workqueue.Bulk].InProgress != before[workqueue.Bulk].InProgress {
		t.Fatalf("bulk backlog = %+v, want one new distinct waiting document over %+v", status[workqueue.Bulk], before[workqueue.Bulk])
	}
	if status[workqueue.Live].OldestAgeSeconds < 20 {
		t.Fatalf("live oldest age = %v, want the persisted admission age", status[workqueue.Live].OldestAgeSeconds)
	}
	started, release := make(chan struct{}), make(chan struct{})
	tracked := make(chan error, 1)
	go func() {
		tracked <- workqueue.Track(workqueue.WithTracker(ctx, postgres.QueueTracker{Pool: pool}), org, "ingestion", live.ID, live.ID, func(run context.Context) error {
			close(started)
			select {
			case <-release:
				return run.Err()
			case <-run.Done():
				return run.Err()
			}
		})
	}()
	waitForTrackStart(t, ctx, started, tracked)
	status = readQueueStatus(t, reader, ctx)
	if status[workqueue.Live].IngestionWaiting == nil || *status[workqueue.Live].IngestionWaiting != *before[workqueue.Live].IngestionWaiting {
		t.Fatalf("active ingestion must not count as waiting: %+v", status[workqueue.Live])
	}
	if status[workqueue.Live].Waiting != before[workqueue.Live].Waiting || status[workqueue.Live].InProgress != before[workqueue.Live].InProgress+1 {
		t.Fatalf("live active backlog = %+v, want one new in progress document over %+v", status[workqueue.Live], before[workqueue.Live])
	}
	close(release)
	if err = waitForTrackResult(t, ctx, tracked); err != nil {
		t.Fatal(err)
	}

	// Withdrawn documents leave every queue count, including a pending retry.
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE records SET withdrawn=true WHERE organization=$1 AND id=(SELECT record_id FROM ingestion_receipts WHERE organization=$1 AND id=$2)`, org, live.ID); err != nil {
		t.Fatal(err)
	}
	status = readQueueStatus(t, reader, ctx)
	if status[workqueue.Live].Waiting != before[workqueue.Live].Waiting || status[workqueue.Live].InProgress != before[workqueue.Live].InProgress {
		t.Fatalf("withdrawn live backlog = %+v, want baseline %+v", status[workqueue.Live], before[workqueue.Live])
	}
	// Current enrichment waits, but superseded enrichment that the indexer
	// skips must not remain in the backlog after its replacement finishes.
	// These fixtures write state directly, so they run the writers' observation.
	observe := func(version string) {
		t.Helper()
		var record string
		if err := pool.QueryRow(ctx, `SELECT record_id FROM record_versions WHERE organization=$1 AND id=$2`, org, version).Scan(&record); err != nil {
			t.Fatal(err)
		}
		if err := postgres.ObserveQueueRecords(ctx, pool, org, record); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE ingestion_receipts SET state='resolved',outcome='created' WHERE organization=$1 AND id=$2`, org, bulk.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE record_versions SET baseline_ready=true,processing='idle',enrichment_state='queued' WHERE organization=$1 AND id=$2`, org, versionID); err != nil {
		t.Fatal(err)
	}
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE records SET current_version_id=$2 WHERE organization=$1 AND id=(SELECT record_id FROM record_versions WHERE organization=$1 AND id=$2)`, org, versionID); err != nil {
		t.Fatal(err)
	}
	// A current pointer can reference a Version owned by another Record in
	// imported/legacy data. It must not create a second queue membership.
	if _, err = queueFixtureExec(t, ctx, reader, org, `INSERT INTO records(organization,id,corpus_id,namespace,record_key,current_version_id)
 VALUES($1,'alias',$2,'tests','alias',$3)`, org, c.ID, versionID); err != nil {
		t.Fatal(err)
	}
	observe(versionID)
	status = readQueueStatus(t, reader, ctx)
	if status[workqueue.Bulk].Waiting != before[workqueue.Bulk].Waiting+1 {
		t.Fatalf("current enrichment backlog = %+v, want one waiting document over %+v", status[workqueue.Bulk], before[workqueue.Bulk])
	}
	if status[workqueue.Bulk].IngestionWaiting == nil || *status[workqueue.Bulk].IngestionWaiting != *before[workqueue.Bulk].IngestionWaiting {
		t.Fatalf("post-baseline enrichment must not block acquisition: %+v", status[workqueue.Bulk])
	}
	correction, err := service.Accept(bulkCtx, scope, content.Command{Key: "bulk-correction", Source: bulk.Source, Content: content.Text{Kind: "text", Text: "corrected"}})
	if err != nil {
		t.Fatal(err)
	}
	corrected, _, err := stores.Work(ctx, org, correction.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = stores.Publish(ctx, corrected, publication(content.Blob{Key: "queues/corrected", SHA256: "queues-corrected-text", Size: 9}, content.Blob{Key: "queues/corrected-manifest", SHA256: "queues-corrected-manifest", Size: 2})); err != nil {
		t.Fatal(err)
	}
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE record_versions SET baseline_ready=true,processing='idle',enrichment_state='idle' WHERE organization=$1 AND id=$2`, org, corrected.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE records SET current_version_id=$2 WHERE organization=$1 AND id=(SELECT record_id FROM record_versions WHERE organization=$1 AND id=$2)`, org, corrected.VersionID); err != nil {
		t.Fatal(err)
	}
	observe(corrected.VersionID)
	status = readQueueStatus(t, reader, ctx)
	if status[workqueue.Bulk].Waiting != before[workqueue.Bulk].Waiting || status[workqueue.Bulk].InProgress != before[workqueue.Bulk].InProgress {
		t.Fatalf("superseded enrichment backlog = %+v, want baseline %+v", status[workqueue.Bulk], before[workqueue.Bulk])
	}
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE records SET withdrawn=true WHERE organization=$1 AND id=(SELECT record_id FROM ingestion_receipts WHERE organization=$1 AND id=$2)`, org, bulk.ID); err != nil {
		t.Fatal(err)
	}
	status = readQueueStatus(t, reader, ctx)
	if status[workqueue.Bulk].Waiting != before[workqueue.Bulk].Waiting || status[workqueue.Bulk].InProgress != before[workqueue.Bulk].InProgress {
		t.Fatalf("withdrawn bulk backlog = %+v, want baseline %+v", status[workqueue.Bulk], before[workqueue.Bulk])
	}
}

// Tracking is an observation, never a gate on the work it observes. A retry of
// a tracked document runs, and an attempt whose renewal and release cannot
// reach its row still reports the work's own result. The test's row lock is
// the slow-renewal case seen on a busy database, made deterministic.
func TestQueueTrackerNeverChangesTrackedWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := rebuildAdapterPool(t, ctx)
	org := fmt.Sprintf("adapter-queue-lease-%d", time.Now().UnixNano())
	tracker := postgres.QueueTracker{Pool: pool, Lease: 300 * time.Millisecond}
	renewalFailed, releaseFailed := make(chan struct{}, 1), make(chan struct{}, 1)
	logger := slog.Default()
	slog.SetDefault(slog.New(trackingFailureHandler{Handler: slog.NewTextHandler(os.Stderr, nil), renewed: renewalFailed, released: releaseFailed}))
	t.Cleanup(func() { slog.SetDefault(logger) })
	track := func(started, finish chan struct{}) <-chan error {
		result := make(chan error, 1)
		go func() {
			result <- tracker.Track(ctx, org, "test", "work", "document", func(run context.Context) error {
				close(started)
				select {
				case <-finish:
					return run.Err()
				case <-run.Done():
					return run.Err()
				}
			})
		}()
		waitForTrackStart(t, ctx, started, result)
		return result
	}

	started, finish := make(chan struct{}), make(chan struct{})
	first := track(started, finish)
	retried := false
	if err := tracker.Track(ctx, org, "test", "work", "document", func(context.Context) error { retried = true; return nil }); err != nil || !retried {
		t.Fatalf("retry of a tracked document = %v (ran: %v), want it to run and succeed", err, retried)
	}
	close(finish)
	if err := waitForTrackResult(t, ctx, first); err != nil {
		t.Fatalf("attempt whose row a retry took = %v, want the work's own nil result", err)
	}

	select {
	case <-releaseFailed:
	default:
	}
	started, finish = make(chan struct{}), make(chan struct{})
	slow := track(started, finish)
	lock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Rollback(context.Background()) }()
	var holder int
	if err = lock.QueryRow(ctx, `SELECT pg_backend_pid() FROM queue_document_attempts WHERE organization=$1 FOR UPDATE`, org).Scan(&holder); err != nil {
		t.Fatal(err)
	}
	var renewingPID int
	for renewingPID == 0 {
		if err = pool.QueryRow(ctx, `SELECT coalesce((SELECT pid FROM pg_stat_activity
WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE 'UPDATE queue_document_attempts%' LIMIT 1),0)`, holder).Scan(&renewingPID); err != nil {
			t.Fatalf("renewal did not block behind the row lock: %v", err)
		}
	}
	// Cancel only the blocked renewal, so it fails while real work is running.
	var canceled bool
	if err = pool.QueryRow(ctx, `SELECT pg_cancel_backend($1)`, renewingPID).Scan(&canceled); err != nil || !canceled {
		t.Fatalf("cancel blocked renewal: canceled=%v err=%v", canceled, err)
	}
	select {
	case <-renewalFailed:
	case err = <-slow:
		t.Fatalf("renewal failure ended tracked work: %v", err)
	case <-ctx.Done():
		t.Fatal("renewal failure was not observed")
	}
	close(finish)
	if err = waitForTrackResult(t, ctx, slow); err != nil {
		t.Fatalf("attempt whose renewal and release failed = %v, want the work's own nil result", err)
	}
	select {
	case <-releaseFailed:
	default:
		t.Fatal("locked row did not make release fail")
	}
	if err = lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE queue_document_attempts SET lease_until='-infinity' WHERE organization=$1`, org); err != nil {
		t.Fatal(err)
	}
}

// Observe the real adapter's warning to join the failed renewal without sleeps
// or an injection hook in production. Other logs retain their usual handler.
type trackingFailureHandler struct {
	slog.Handler
	renewed  chan<- struct{}
	released chan<- struct{}
}

func (h trackingFailureHandler) Handle(ctx context.Context, record slog.Record) error {
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "step" {
			var failed chan<- struct{}
			switch attr.Value.String() {
			case "renew":
				failed = h.renewed
			case "release":
				failed = h.released
			}
			select {
			case failed <- struct{}{}:
			default:
			}
		}
		return true
	})
	return h.Handler.Handle(ctx, record)
}

func TestQueueBacklogOperationStateAndLeaseTransitions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := rebuildAdapterPool(t, ctx)
	org := fmt.Sprintf("adapter-queue-operation-%d", time.Now().UnixNano())
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read", "monitoring:read", "monitoring:write"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "operation-queues", Name: "Operation queues"})
	if err != nil {
		t.Fatal(err)
	}
	stores := contentStores(pool)
	service := content.Service{Submissions: stores, Receipts: stores, RecordStore: stores, Versions: stores, Materialization: stores}
	reader := postgres.QueueSnapshots{Pool: pool}
	queueFixtureReconcile(t, reader, ctx)
	before := readQueueStatus(t, reader, ctx)
	receipt, err := service.Accept(ctx, scope, content.Command{
		Key:     "operation-document",
		Source:  content.Source{CorpusID: c.ID, Namespace: "tests", RecordKey: "operation-document"},
		Content: content.Text{Kind: "text", Text: "operation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := stores.Work(ctx, org, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = stores.Publish(ctx, work, publication(
		content.Blob{Key: "queues/operation", SHA256: "queues-operation-text", Size: 9},
		content.Blob{Key: "queues/operation-manifest", SHA256: "queues-operation-manifest", Size: 2},
	)); err != nil {
		t.Fatal(err)
	}
	var versionID string
	if err = pool.QueryRow(ctx, `SELECT version_id FROM ingestion_receipts WHERE organization=$1 AND id=$2`, org, receipt.ID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE record_versions SET baseline_ready=true,processing='idle',enrichment_state='idle' WHERE organization=$1 AND id=$2`, org, versionID); err != nil {
		t.Fatal(err)
	}
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE records SET current_version_id=$3,current_accepted_at=clock_timestamp() WHERE organization=$1 AND id=(SELECT record_id FROM record_versions WHERE organization=$1 AND id=$2)`, org, versionID, versionID); err != nil {
		t.Fatal(err)
	}
	op, err := (postgres.OperationStore{Pool: pool}).AcceptRebuild(ctx, org, c.ID, "queue-operation", []byte(`{"idempotency_key":"queue-operation"}`))
	if err != nil {
		t.Fatal(err)
	}
	status := readQueueStatus(t, reader, ctx)
	if status[workqueue.Bulk].Waiting != before[workqueue.Bulk].Waiting+1 || status[workqueue.Bulk].InProgress != before[workqueue.Bulk].InProgress {
		t.Fatalf("queued rebuild backlog = %+v, want one new waiting document over %+v", status[workqueue.Bulk], before[workqueue.Bulk])
	}
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE operations SET state='paused' WHERE organization=$1 AND id=$2`, org, op.ID); err != nil {
		t.Fatal(err)
	}
	status = readQueueStatus(t, reader, ctx)
	if status[workqueue.Bulk].Waiting != before[workqueue.Bulk].Waiting || status[workqueue.Bulk].InProgress != before[workqueue.Bulk].InProgress {
		t.Fatalf("paused rebuild backlog = %+v, want baseline %+v", status[workqueue.Bulk], before[workqueue.Bulk])
	}
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE operations SET state='running' WHERE organization=$1 AND id=$2`, org, op.ID); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	tracked := make(chan error, 1)
	go func() {
		tracked <- workqueue.Track(workqueue.WithTracker(ctx, postgres.QueueTracker{Pool: pool}), org, "operation", op.ID, versionID, func(run context.Context) error {
			close(started)
			select {
			case <-release:
				return nil
			case <-run.Done():
				return run.Err()
			}
		})
	}()
	waitForTrackStart(t, ctx, started, tracked)
	status = readQueueStatus(t, reader, ctx)
	if status[workqueue.Bulk].Waiting != before[workqueue.Bulk].Waiting || status[workqueue.Bulk].InProgress != before[workqueue.Bulk].InProgress+1 {
		t.Fatalf("active rebuild backlog = %+v, want one new in progress document over %+v", status[workqueue.Bulk], before[workqueue.Bulk])
	}
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE operations SET state='paused' WHERE organization=$1 AND id=$2`, org, op.ID); err != nil {
		t.Fatal(err)
	}
	status = readQueueStatus(t, reader, ctx)
	if status[workqueue.Bulk].Waiting != before[workqueue.Bulk].Waiting || status[workqueue.Bulk].InProgress != before[workqueue.Bulk].InProgress {
		t.Fatalf("paused active rebuild backlog = %+v, want baseline %+v", status[workqueue.Bulk], before[workqueue.Bulk])
	}
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE operations SET state='running' WHERE organization=$1 AND id=$2`, org, op.ID); err != nil {
		t.Fatal(err)
	}
	status = readQueueStatus(t, reader, ctx)
	if status[workqueue.Bulk].Waiting != before[workqueue.Bulk].Waiting || status[workqueue.Bulk].InProgress != before[workqueue.Bulk].InProgress+1 {
		t.Fatalf("resumed active rebuild backlog = %+v, want one new in progress document over %+v", status[workqueue.Bulk], before[workqueue.Bulk])
	}
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE queue_document_attempts SET lease_until='-infinity' WHERE organization=$1 AND kind='operation' AND work_id=$2 AND document_id=$3`, org, op.ID, versionID); err != nil {
		t.Fatal(err)
	}
	status = readQueueStatus(t, reader, ctx)
	if status[workqueue.Bulk].Waiting != before[workqueue.Bulk].Waiting+1 || status[workqueue.Bulk].InProgress != before[workqueue.Bulk].InProgress {
		t.Fatalf("expired rebuild lease backlog = %+v, want one new waiting document over %+v", status[workqueue.Bulk], before[workqueue.Bulk])
	}
	close(release)
	if err = waitForTrackResult(t, ctx, tracked); err != nil {
		t.Fatalf("expired attempt error = %v, want the work's own nil result", err)
	}
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE operations SET state='paused' WHERE organization=$1 AND id=$2`, org, op.ID); err != nil {
		t.Fatal(err)
	}

	// Delayed alert retries are unavailable to workers and must not inflate
	// the autoscaling signal or produce a negative oldest age.
	monitor := monitoring.Service{Store: stores, Corpora: stores, Evaluators: fakeplugin.FixtureEvaluators(), Destinations: map[string]monitoring.Destination{"dest": {Organization: org}}}
	q, err := monitor.CreateSavedQuery(ctx, scope, monitoring.SavedQueryInput{Key: "alerts", Name: "Alerts", Definition: monitoring.Definition{CorpusIDs: []string{c.ID}, Expression: map[string]any{}, RetrievalProfile: "default", TemporalPolicy: "from_activation"}})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := monitor.CreateSubscription(ctx, scope, monitoring.SubscriptionInput{Key: "alerts", Name: "Alerts", SavedQueryID: q.ID, SavedQueryVersionID: q.Current.VersionID, Evaluator: monitoring.Evaluator{PluginID: fakeplugin.FixtureEvaluator, Version: fakeplugin.FixtureEvaluatorVersion}, DestinationID: "dest"})
	if err != nil {
		t.Fatal(err)
	}
	liveBefore := readQueueStatus(t, reader, ctx)[workqueue.Live]
	if _, err = queueFixtureExec(t, ctx, reader, org, `INSERT INTO evaluation_intents(organization,subscription_version_id,sequence,subscription_id,corpus_id,record_id,record_version_id,available_at) SELECT $1,$2,1,$3,$4,record_id,id,clock_timestamp()+interval '1 hour' FROM record_versions WHERE organization=$1 AND id=$5`, org, sub.Current.VersionID, sub.ID, c.ID, versionID); err != nil {
		t.Fatal(err)
	}
	live := readQueueStatus(t, reader, ctx)[workqueue.Live]
	if live.Waiting != liveBefore.Waiting || live.OldestAgeSeconds < 0 {
		t.Fatalf("delayed alert backlog = %+v, want unavailable retry excluded from %+v", live, liveBefore)
	}
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE evaluation_intents SET available_at=clock_timestamp()-interval '30 seconds' WHERE organization=$1`, org); err != nil {
		t.Fatal(err)
	}
	live = readQueueStatus(t, reader, ctx)[workqueue.Live]
	if live.Waiting != liveBefore.Waiting+1 || live.OldestAgeSeconds < 20 {
		t.Fatalf("due alert backlog = %+v, want one available retry with persisted age over %+v", live, liveBefore)
	}
	alertStarted, alertRelease := make(chan struct{}), make(chan struct{})
	alertDone := make(chan error, 1)
	go func() {
		alertDone <- (postgres.QueueTracker{Pool: pool}).Track(ctx, org, "alert", sub.Current.VersionID+":1", versionID, func(run context.Context) error {
			close(alertStarted)
			select {
			case <-run.Done():
				return run.Err()
			case <-alertRelease:
				return nil
			}
		})
	}()
	waitForTrackStart(t, ctx, alertStarted, alertDone)
	live = readQueueStatus(t, reader, ctx)[workqueue.Live]
	if live.InProgress != liveBefore.InProgress+1 {
		t.Fatalf("active alert backlog = %+v, want one admitted attempt over %+v", live, liveBefore)
	}
	// A worker can die after scheduling a delayed retry but before releasing
	// its observation. The still-valid lease must not count unavailable work.
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE evaluation_intents SET available_at=clock_timestamp()+interval '1 hour' WHERE organization=$1`, org); err != nil {
		t.Fatal(err)
	}
	live = readQueueStatus(t, reader, ctx)[workqueue.Live]
	close(alertRelease)
	if err = waitForTrackResult(t, ctx, alertDone); err != nil {
		t.Fatal(err)
	}
	if live.Waiting != liveBefore.Waiting || live.InProgress != liveBefore.InProgress {
		t.Fatalf("delayed retry with unreleased attempt = %+v, want baseline %+v", live, liveBefore)
	}
	if _, err = queueFixtureExec(t, ctx, reader, org, `UPDATE evaluation_intents SET state='done' WHERE organization=$1`, org); err != nil {
		t.Fatal(err)
	}

	// Model a clock crossing for a retry cohort larger than one maintenance page.
	// Canonical intents are due; their stored membership is the preceding,
	// not-yet-promoted state. No wall-clock wait or test-only production clock.
	prior := readQueueStatus(t, reader, ctx)[workqueue.Live]
	exec := func(sql string) {
		t.Helper()
		if _, err := queueFixtureExec(t, ctx, reader, org, sql, org, sub.Current.VersionID, sub.ID, c.ID); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO records(organization,id,corpus_id,namespace,record_key)
 SELECT $1,'cohort-record-'||i,$4,'cohort',i::text FROM generate_series(1,1001) i WHERE $2::text<>'' AND $3::text<>''`)
	if _, err := queueFixtureExec(t, ctx, reader, org, `INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,source_position,text_blob_id,manifest_blob_id,provenance,baseline_ready)
 SELECT organization,'cohort-version-'||i,'cohort-record-'||i,'text',i::text,1,'',text_blob_id,manifest_blob_id,'{}',true FROM record_versions CROSS JOIN generate_series(1,1001) i WHERE organization=$1 AND id=$2`, org, versionID); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO evaluation_intents(organization,subscription_version_id,sequence,subscription_id,corpus_id,record_id,record_version_id,available_at)
 SELECT $1,$2,i+1,$3,$4,'cohort-record-'||i,'cohort-version-'||i,statement_timestamp()-interval '30 seconds' FROM generate_series(1,1001) i`)
	if _, err := pool.Exec(ctx, `WITH changed AS (UPDATE queue_backlog_documents SET ready=false WHERE organization=$1 AND document_id LIKE 'cohort-version-%' RETURNING document_id)
 UPDATE queue_backlog_totals SET pending=pending-(SELECT count(*) FROM changed) WHERE stripe=(hashtextextended($1,0)&63)::integer AND queue='live'`, org); err != nil {
		t.Fatal(err)
	}
	preserved := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE queue_backlog_snapshots SET published_at='-infinity'`); err != nil {
			t.Fatal(err)
		}
		if err := reader.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		statuses, err := reader.QueueBacklog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range statuses {
			if status.Queue == workqueue.Live && status.Waiting != prior.Waiting {
				t.Fatalf("partial due page published: %+v, prior=%+v", status, prior)
			}
		}
		var unstamped bool
		if err := pool.QueryRow(ctx, `SELECT bool_and(published_at='-infinity') FROM queue_backlog_snapshots`).Scan(&unstamped); err != nil || !unstamped {
			t.Fatalf("incomplete publication stamped fresh: unchanged=%v error=%v", unstamped, err)
		}
	}
	preserved()
	if more, err := reader.Maintain(ctx); err != nil || !more {
		t.Fatalf("first due page more=%v error=%v", more, err)
	}
	preserved()
	final := readQueueStatus(t, reader, ctx)[workqueue.Live]
	if final.Waiting != prior.Waiting+1001 {
		t.Fatalf("complete due cohort=%+v, want %d", final, prior.Waiting+1001)
	}
	if _, err := queueFixtureExec(t, ctx, reader, org, `UPDATE records SET withdrawn=true WHERE organization=$1 AND namespace='cohort'`, org); err != nil {
		t.Fatal(err)
	}

}

func readQueueStatus(t *testing.T, reader postgres.QueueSnapshots, ctx context.Context) map[string]workqueue.Status {
	t.Helper()
	for {
		more, err := reader.Maintain(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	assertQueueRecount(t, ctx, reader)
	// Force a refresh at the persisted state transition without waiting for
	// the production polling interval. This exercises the production reader.
	if _, err := reader.Pool.Exec(ctx, `UPDATE queue_backlog_snapshots SET observed_at='-infinity',published_at='-infinity'`); err != nil {
		t.Fatal(err)
	}
	if err := reader.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := reader.QueueBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("queue status rows = %d, want live and bulk", len(rows))
	}
	got := make(map[string]workqueue.Status, len(rows))
	for _, row := range rows {
		got[row.Queue] = row
	}
	return got
}

func waitForTrackStart(t *testing.T, ctx context.Context, started <-chan struct{}, result <-chan error) {
	t.Helper()
	select {
	case <-started:
	case err := <-result:
		t.Fatalf("tracked callback did not start: %v", err)
	case <-ctx.Done():
		t.Fatalf("tracked callback did not start before context ended: %v", ctx.Err())
	}
}

func waitForTrackResult(t *testing.T, ctx context.Context, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		t.Fatalf("tracked callback did not finish before context ended: %v", ctx.Err())
		return ctx.Err()
	}
}

// Autoscaler reads must stay independent of operation/document tables. A
// refresh publishes both queues, while a missing or stale observation fails
// instead of presenting an empty backlog. Table locking makes that dependency
// failure deterministic without sleeps or a size-dependent timing assertion.
// This owns continuous-import publication, which the stale-read and producer
// lock-order owners cannot see. Restoring the any-dirty publication gate must
// fail after a bounded drain makes progress but leaves recent work outstanding.
// Fixture timestamps replace waits; all work enters through real acceptance.
func TestQueueSnapshotsStayAvailableDuringContinuousImports(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	if err := app.BootstrapDatabase(ctx, pool, nil); err != nil {
		t.Fatal(err)
	}
	snapshots := postgres.QueueSnapshots{Pool: pool}
	for {
		more, err := snapshots.Maintain(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	c, _, err := (postgres.Store{Pool: pool}).Create(ctx, "example", corpus.CreateInput{Key: "queues", Name: "Queues"})
	if err != nil {
		t.Fatal(err)
	}
	if err = snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err = (postgres.JournalStore{Pool: pool}).SetParallel(ctx, true); err != nil {
		t.Fatal(err)
	}
	accept := func(key string) {
		t.Helper()
		_, err := (postgres.SubmissionStore{Pool: pool}).Accept(ctx,
			corpus.Scope{Organization: "example", Corpora: []string{c.ID}}, content.Command{
				Key: key, Source: content.Source{CorpusID: c.ID, Namespace: "queues", RecordKey: key},
				Content: content.Text{Kind: "text", Text: key},
			})
		if err != nil {
			t.Fatal(err)
		}
	}
	accept("first")
	accept("second")
	for turn := 1; turn <= 3; turn++ {
		accept(fmt.Sprintf("next-%d", turn))
		more, err := snapshots.ReconcileParallelQueue(ctx, 1)
		if err != nil || !more {
			t.Fatalf("bounded drain turn=%d more=%v error=%v", turn, more, err)
		}
		var dirty, applied int
		var oldestDirty float64
		if err = pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE revision<>applied_revision),
 count(*) FILTER(WHERE revision=applied_revision),
 coalesce(max(extract(epoch FROM clock_timestamp()-dirty_since)) FILTER(WHERE revision<>applied_revision),0)
 FROM parallel_queue_observations`).Scan(&dirty, &applied, &oldestDirty); err != nil {
			t.Fatal(err)
		}
		if dirty != 2 || applied != turn || oldestDirty >= 10 {
			t.Fatalf("turn=%d dirty=%d applied=%d oldest dirty seconds=%f", turn, dirty, applied, oldestDirty)
		}
		// Make publication due and reproduce an expired previous observation
		// without waiting a minute. The outstanding changes themselves are recent.
		if _, err = pool.Exec(ctx, `UPDATE queue_backlog_snapshots SET
 observed_at=clock_timestamp()-interval '2 minutes',published_at='-infinity',
 ingestion_observed_at=clock_timestamp()-interval '2 minutes'`); err != nil {
			t.Fatal(err)
		}
		if err = snapshots.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		read, stop := context.WithTimeout(ctx, time.Second)
		statuses, err := snapshots.QueueBacklog(read)
		readError := read.Err()
		stop()
		if err != nil {
			t.Fatalf("publication starved: turn=%d dirty=%d applied=%d dirty age=%fs read context=%v reader error=%v", turn, dirty, applied, oldestDirty, readError, err)
		}
		if len(statuses) != 2 || statuses[0].Queue != workqueue.Bulk || statuses[1].Queue != workqueue.Live ||
			statuses[1].Waiting != int64(turn) || statuses[1].IngestionWaiting == nil || *statuses[1].IngestionWaiting != int64(turn) {
			t.Fatalf("turn=%d bounded observation=%+v, want both queues and %d reconciled live documents", turn, statuses, turn)
		}
	}
	// Repeated publication cannot move the first unresolved change forward,
	// and dirty age must not become the clock origin for oldest waiting age.
	for turn := 0; turn < 2; turn++ {
		if _, err = pool.Exec(ctx, `UPDATE parallel_queue_observations
 SET dirty_since=clock_timestamp()-interval '40 seconds',changed_at=clock_timestamp()
 WHERE revision<>applied_revision;
 UPDATE queue_backlog_snapshots SET published_at='-infinity'`); err != nil {
			t.Fatal(err)
		}
		if err = snapshots.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		statuses, err := snapshots.QueueBacklog(ctx)
		if err != nil || len(statuses) != 2 || statuses[1].OldestAgeSeconds >= 10 {
			t.Fatalf("recent observation inherited dirty age: statuses=%+v error=%v", statuses, err)
		}
		// Advance the persisted clocks together; the count observation itself
		// remains fresh. This expires only the counter completeness proof.
		if _, err = pool.Exec(ctx, `UPDATE parallel_queue_observations SET dirty_since=dirty_since-interval '30 seconds' WHERE revision<>applied_revision;
 UPDATE queue_backlog_snapshots SET counters_observed_at=counters_observed_at-interval '30 seconds'`); err != nil {
			t.Fatal(err)
		}
		if _, err = snapshots.QueueBacklog(ctx); err == nil {
			t.Fatal("recent publication renewed an unresolved change's freshness budget")
		}
	}
	// A recent correction must preserve the oldest unresolved change. With
	// a longer refresh interval the same lag is valid; the default rejects it.
	var dirtyKey string
	if err = pool.QueryRow(ctx, `SELECT r.record_key FROM parallel_queue_observations q
 JOIN records r ON (r.organization,r.id)=(q.organization,q.record_id)
 WHERE q.revision<>q.applied_revision LIMIT 1`).Scan(&dirtyKey); err != nil {
		t.Fatal(err)
	}
	if _, err = (postgres.SubmissionStore{Pool: pool}).Accept(ctx,
		corpus.Scope{Organization: "example", Corpora: []string{c.ID}}, content.Command{
			Key: "correction", Source: content.Source{CorpusID: c.ID, Namespace: "queues", RecordKey: dirtyKey},
			Content: content.Text{Kind: "text", Text: "corrected"},
		}); err != nil {
		t.Fatal(err)
	}
	if err = snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = snapshots.QueueBacklog(ctx); err == nil {
		t.Fatal("aged dirty work or recent correction advertised as fresh")
	}
	snapshots.RefreshInterval = time.Minute
	if err = snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if statuses, err := snapshots.QueueBacklog(ctx); err != nil || len(statuses) != 2 || statuses[1].IngestionWaiting == nil {
		t.Fatalf("lag within custom two-interval budget unavailable: statuses=%+v error=%v", statuses, err)
	}
	snapshots.RefreshInterval = 0
	if _, err = snapshots.QueueBacklog(ctx); err == nil {
		t.Fatal("custom publication extended the default freshness budget")
	}
	if more, err := snapshots.ReconcileParallelQueue(ctx, 1000); err != nil || more {
		t.Fatalf("final drain more=%v error=%v", more, err)
	}
	if _, err = snapshots.QueueBacklog(ctx); err == nil {
		t.Fatal("draining the ledger made its old published counters fresh")
	}
	// Fresh publication recovers even though a recent publisher used the
	// longer budget. The result is independent of the aggregate ledger.
	if err = snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	var canonical int64
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ingestion_receipts
 WHERE organization='example' AND route_family='ingestion' AND state='pending'`).Scan(&canonical); err != nil {
		t.Fatal(err)
	}
	statuses, err := snapshots.QueueBacklog(ctx)
	if err != nil || len(statuses) != 2 || canonical != 6 || statuses[1].Waiting != canonical ||
		statuses[1].IngestionWaiting == nil || *statuses[1].IngestionWaiting != canonical || statuses[0].Waiting != 0 {
		t.Fatalf("drained observation=%+v canonical=%d error=%v", statuses, canonical, err)
	}
}

func TestQueueSnapshotsReadWithoutDocumentScanAndRejectStaleData(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := rebuildAdapterPool(t, ctx)
	snapshots := postgres.QueueSnapshots{Pool: pool}
	if _, err := snapshots.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `LOCK TABLE record_versions IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	read, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	rows, err := snapshots.QueueBacklog(read)
	if err != nil || len(rows) != 2 {
		t.Fatalf("snapshot read blocked on document scope: rows=%v err=%v", rows, err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE queue_backlog_snapshots SET observed_at=clock_timestamp()-interval '2 minutes',published_at=clock_timestamp()-interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	if _, err = snapshots.QueueBacklog(ctx); err == nil {
		t.Fatal("stale snapshot reported as current backlog")
	}
	if err = snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = snapshots.QueueBacklog(ctx); err != nil {
		t.Fatalf("refreshed snapshot unavailable: %v", err)
	}
}

// The count owners above exercise mixed enqueue/claim/finish/failure and operation
// transitions. This independent canonical recount checks the incremental waiting
// totals after each transition; it deliberately does not read the new ledger.
func assertQueueRecount(t *testing.T, ctx context.Context, reader postgres.QueueSnapshots) {
	t.Helper()
	rows, err := reader.Pool.Query(ctx, `WITH enrichment_versions AS MATERIALIZED (
 SELECT organization,id AS version_id FROM record_versions
), ingestion_work AS MATERIALIZED (
 SELECT CASE WHEN rc.work_queue='bulk' THEN 'bulk' ELSE 'live' END AS queue,
        rc.organization,COALESCE(NULLIF(rc.version_id,''),ar.version_id,rc.id) AS document_id,
        COALESCE(LEAST(rc.accepted_at,ar.accepted_at),rc.accepted_at,ar.accepted_at) AS admitted_at,false AS active
 FROM ingestion_receipts rc
 JOIN records r ON (r.organization,r.id)=(rc.organization,rc.record_id)
 LEFT JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(rc.organization,rc.record_id,rc.slot)
 WHERE rc.route_family='ingestion' AND rc.state='pending'
   AND NOT (r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))
 UNION ALL
 SELECT CASE WHEN rc.work_queue='bulk' THEN 'bulk' ELSE 'live' END,
        v.organization,v.id,COALESCE(LEAST(rc.accepted_at,ar.accepted_at),rc.accepted_at,ar.accepted_at),false
 FROM record_versions v
 JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot)
 LEFT JOIN ingestion_receipts rc ON (rc.organization,rc.record_id,rc.acceptance_order)=(v.organization,v.record_id,v.acceptance_order)
 WHERE NOT (r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))
   AND NOT v.baseline_ready AND NOT v.quarantined AND v.processing IN ('queued','running','retrying')
), work AS (
 SELECT * FROM ingestion_work
 UNION ALL
 SELECT CASE WHEN rc.work_queue='bulk' THEN 'bulk' ELSE 'live' END,
        v.organization,v.id,COALESCE(LEAST(rc.accepted_at,ar.accepted_at),rc.accepted_at,ar.accepted_at),false
 FROM enrichment_versions ev
 JOIN record_versions v ON (v.organization,v.id)=(ev.organization,ev.version_id)
 JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot)
 LEFT JOIN ingestion_receipts rc ON (rc.organization,rc.record_id,rc.acceptance_order)=(v.organization,v.record_id,v.acceptance_order)
 WHERE v.baseline_ready AND r.current_version_id=v.id AND NOT v.quarantined
   AND v.enrichment_state IN ('queued','running','retrying')
   AND NOT (r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))
 UNION ALL
 SELECT CASE WHEN e.work_queue='bulk' THEN 'bulk' ELSE 'live' END,
        e.organization,e.version_id,e.created_at,false
 FROM ingestion_evaluations e
 JOIN record_versions v ON (v.organization,v.id)=(e.organization,e.version_id)
 JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 WHERE e.state='queued'
   AND NOT (r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))
 UNION ALL
 SELECT CASE WHEN sp.work_queue='bulk' THEN 'bulk' ELSE 'live' END,
        sp.organization,sp.version_id,sp.created_at,false
 FROM serving_projections sp
 JOIN record_versions v ON (v.organization,v.id)=(sp.organization,sp.version_id)
 JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 WHERE sp.state='queued'
   AND NOT (r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))
 UNION ALL
 SELECT 'live',
        i.organization,i.record_version_id,i.available_at,false
 FROM evaluation_intents i
 JOIN record_versions v ON (v.organization,v.id)=(i.organization,i.record_version_id)
 JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 WHERE i.kind='evaluation' AND i.state='pending' AND i.available_at<=statement_timestamp()
   AND NOT (r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))
 UNION ALL
 SELECT 'bulk',o.organization,qi.version_id,o.created_at,false
 FROM operations o
 JOIN quarantine_reprocess_items qi ON (qi.organization,qi.operation_id)=(o.organization,o.id)
 JOIN records r ON (r.organization,r.id)=(qi.organization,qi.record_id)
 WHERE o.kind='quarantine_reprocess' AND o.state IN ('queued','running')
   AND qi.phase IN ('pending','renormalizing','released')
   AND NOT (r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id))
), queues(queue) AS (VALUES ('live'),('bulk'))
 SELECT q.queue,
 (SELECT count(*) FROM (SELECT DISTINCT organization,document_id FROM work WHERE queue=q.queue AND document_id<>'') d),
 (SELECT count(*) FROM (SELECT DISTINCT organization,document_id FROM ingestion_work WHERE queue=q.queue AND document_id<>'') d),
 (SELECT sum(pending)::bigint FROM queue_backlog_totals WHERE queue=q.queue),
 (SELECT sum(ingestion_pending)::bigint FROM queue_backlog_totals WHERE queue=q.queue)
 FROM queues q`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var queue string
		var pending, ingestion, actual, actualIngestion int64
		if err := rows.Scan(&queue, &pending, &ingestion, &actual, &actualIngestion); err != nil {
			t.Fatal(err)
		}
		if pending != actual || ingestion != actualIngestion {
			t.Fatalf("%s canonical recount waiting=%d ingestion=%d; maintained=%d ingestion=%d", queue, pending, ingestion, actual, actualIngestion)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// Raw fixture mutations model application observations explicitly. Real service
// writes are read before this helper, so a missing enqueue hook is still caught.
func queueFixtureExec(t *testing.T, ctx context.Context, reader postgres.QueueSnapshots, org, query string, args ...any) (pgconn.CommandTag, error) {
	t.Helper()
	tx, err := reader.Pool.Begin(ctx)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return tag, err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM records WHERE organization=$1 ORDER BY id`, org)
	if err != nil {
		return tag, err
	}
	records, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return tag, err
	}
	// The existing test-only observation bridge uses the production batch. It
	// cannot share this local transaction, so commit the fixture before observing.
	if err = tx.Commit(ctx); err != nil {
		return tag, err
	}
	if len(records) > 0 {
		err = postgres.ObserveQueueRecords(ctx, reader.Pool, org, records...)
	}
	return tag, err
}

// Earlier adapter fixtures may emulate old binaries through direct SQL. Repair
// that global baseline once; every subsequent service transition is observed
// without forcing reconciliation, so missing application hooks still fail.
func queueFixtureReconcile(t *testing.T, reader postgres.QueueSnapshots, ctx context.Context) {
	t.Helper()
	if _, err := reader.Pool.Exec(ctx, `UPDATE queue_backlog_bootstrap SET next_run='-infinity'; UPDATE queue_metric_bootstrap SET next_run='-infinity'`); err != nil {
		t.Fatal(err)
	}
	for {
		more, err := reader.Maintain(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
}
