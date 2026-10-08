package postgres_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost/fakeplugin"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
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
	// Make the oldest age deterministic without waiting on wall time.
	if _, err = pool.Exec(ctx, `UPDATE ingestion_receipts SET accepted_at=clock_timestamp()-interval '30 seconds' WHERE organization=$1 AND id=$2`, org, live.ID); err != nil {
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
	if _, err = pool.Exec(ctx, `UPDATE ingestion_receipts SET state='pending',outcome=NULL,processing='queued' WHERE organization=$1 AND id=$2`, org, bulk.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE record_versions SET processing='queued',baseline_ready=false WHERE organization=$1 AND id=$2`, org, versionID); err != nil {
		t.Fatal(err)
	}

	status := readQueueStatus(t, reader, ctx)
	if status[workqueue.Live].Waiting != before[workqueue.Live].Waiting+1 || status[workqueue.Live].InProgress != before[workqueue.Live].InProgress {
		t.Fatalf("live backlog = %+v, want one new waiting document over %+v", status[workqueue.Live], before[workqueue.Live])
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
	if status[workqueue.Live].Waiting != before[workqueue.Live].Waiting || status[workqueue.Live].InProgress != before[workqueue.Live].InProgress+1 {
		t.Fatalf("live active backlog = %+v, want one new in progress document over %+v", status[workqueue.Live], before[workqueue.Live])
	}
	close(release)
	if err = waitForTrackResult(t, ctx, tracked); err != nil {
		t.Fatal(err)
	}

	// Withdrawn documents leave every queue count, including a pending retry.
	if _, err = pool.Exec(ctx, `UPDATE records SET withdrawn=true WHERE organization=$1 AND id=(SELECT record_id FROM ingestion_receipts WHERE organization=$1 AND id=$2)`, org, live.ID); err != nil {
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
	if _, err = pool.Exec(ctx, `UPDATE ingestion_receipts SET state='resolved',outcome='created' WHERE organization=$1 AND id=$2`, org, bulk.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE record_versions SET baseline_ready=true,processing='idle',enrichment_state='queued' WHERE organization=$1 AND id=$2`, org, versionID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE records SET current_version_id=$2 WHERE organization=$1 AND id=(SELECT record_id FROM record_versions WHERE organization=$1 AND id=$2)`, org, versionID); err != nil {
		t.Fatal(err)
	}
	observe(versionID)
	status = readQueueStatus(t, reader, ctx)
	if status[workqueue.Bulk].Waiting != before[workqueue.Bulk].Waiting+1 {
		t.Fatalf("current enrichment backlog = %+v, want one waiting document over %+v", status[workqueue.Bulk], before[workqueue.Bulk])
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
	if _, err = pool.Exec(ctx, `UPDATE record_versions SET baseline_ready=true,processing='idle',enrichment_state='idle' WHERE organization=$1 AND id=$2`, org, corrected.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE records SET current_version_id=$2 WHERE organization=$1 AND id=(SELECT record_id FROM record_versions WHERE organization=$1 AND id=$2)`, org, corrected.VersionID); err != nil {
		t.Fatal(err)
	}
	observe(corrected.VersionID)
	status = readQueueStatus(t, reader, ctx)
	if status[workqueue.Bulk].Waiting != before[workqueue.Bulk].Waiting || status[workqueue.Bulk].InProgress != before[workqueue.Bulk].InProgress {
		t.Fatalf("superseded enrichment backlog = %+v, want baseline %+v", status[workqueue.Bulk], before[workqueue.Bulk])
	}
	if _, err = pool.Exec(ctx, `UPDATE records SET withdrawn=true WHERE organization=$1 AND id=(SELECT record_id FROM ingestion_receipts WHERE organization=$1 AND id=$2)`, org, bulk.ID); err != nil {
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
	if _, err = pool.Exec(ctx, `UPDATE record_versions SET baseline_ready=true,processing='idle',enrichment_state='idle' WHERE organization=$1 AND id=$2`, org, versionID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE records SET current_version_id=$3,current_accepted_at=clock_timestamp() WHERE organization=$1 AND id=(SELECT record_id FROM record_versions WHERE organization=$1 AND id=$2)`, org, versionID, versionID); err != nil {
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
	if _, err = pool.Exec(ctx, `UPDATE operations SET state='paused' WHERE organization=$1 AND id=$2`, org, op.ID); err != nil {
		t.Fatal(err)
	}
	status = readQueueStatus(t, reader, ctx)
	if status[workqueue.Bulk].Waiting != before[workqueue.Bulk].Waiting || status[workqueue.Bulk].InProgress != before[workqueue.Bulk].InProgress {
		t.Fatalf("paused rebuild backlog = %+v, want baseline %+v", status[workqueue.Bulk], before[workqueue.Bulk])
	}
	if _, err = pool.Exec(ctx, `UPDATE operations SET state='running' WHERE organization=$1 AND id=$2`, org, op.ID); err != nil {
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
	if _, err = pool.Exec(ctx, `UPDATE operations SET state='paused' WHERE organization=$1 AND id=$2`, org, op.ID); err != nil {
		t.Fatal(err)
	}
	status = readQueueStatus(t, reader, ctx)
	if status[workqueue.Bulk].Waiting != before[workqueue.Bulk].Waiting || status[workqueue.Bulk].InProgress != before[workqueue.Bulk].InProgress {
		t.Fatalf("paused active rebuild backlog = %+v, want baseline %+v", status[workqueue.Bulk], before[workqueue.Bulk])
	}
	if _, err = pool.Exec(ctx, `UPDATE operations SET state='running' WHERE organization=$1 AND id=$2`, org, op.ID); err != nil {
		t.Fatal(err)
	}
	status = readQueueStatus(t, reader, ctx)
	if status[workqueue.Bulk].Waiting != before[workqueue.Bulk].Waiting || status[workqueue.Bulk].InProgress != before[workqueue.Bulk].InProgress+1 {
		t.Fatalf("resumed active rebuild backlog = %+v, want one new in progress document over %+v", status[workqueue.Bulk], before[workqueue.Bulk])
	}
	if _, err = pool.Exec(ctx, `UPDATE queue_document_attempts SET lease_until='-infinity' WHERE organization=$1 AND kind='operation' AND work_id=$2 AND document_id=$3`, org, op.ID, versionID); err != nil {
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
	if _, err = pool.Exec(ctx, `UPDATE operations SET state='paused' WHERE organization=$1 AND id=$2`, org, op.ID); err != nil {
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
	if _, err = pool.Exec(ctx, `INSERT INTO evaluation_intents(organization,subscription_version_id,sequence,subscription_id,corpus_id,record_id,record_version_id,available_at) SELECT $1,$2,1,$3,$4,record_id,id,clock_timestamp()+interval '1 hour' FROM record_versions WHERE organization=$1 AND id=$5`, org, sub.Current.VersionID, sub.ID, c.ID, versionID); err != nil {
		t.Fatal(err)
	}
	live := readQueueStatus(t, reader, ctx)[workqueue.Live]
	if live.Waiting != liveBefore.Waiting || live.OldestAgeSeconds < 0 {
		t.Fatalf("delayed alert backlog = %+v, want unavailable retry excluded from %+v", live, liveBefore)
	}
	if _, err = pool.Exec(ctx, `UPDATE evaluation_intents SET available_at=clock_timestamp()-interval '30 seconds' WHERE organization=$1`, org); err != nil {
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
	if _, err = pool.Exec(ctx, `UPDATE evaluation_intents SET available_at=clock_timestamp()+interval '1 hour' WHERE organization=$1`, org); err != nil {
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
	if _, err = pool.Exec(ctx, `UPDATE evaluation_intents SET state='done' WHERE organization=$1`, org); err != nil {
		t.Fatal(err)
	}

}

func readQueueStatus(t *testing.T, reader postgres.QueueSnapshots, ctx context.Context) map[string]workqueue.Status {
	t.Helper()
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
func TestQueueSnapshotsReadWithoutDocumentScanAndRejectStaleData(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := rebuildAdapterPool(t, ctx)
	snapshots := postgres.QueueSnapshots{Pool: pool}
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
