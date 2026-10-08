package postgres_test

import (
	"context"
	"fmt"
	"github.com/The-Vibe-Company/quivr/internal/logging"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The queue owns arrival order across records and organizations, independently
// of receipt hashes or activity completion. Real acceptance supplies the order.
func TestIngestionBacklogLeavesInArrivalOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := contentStores(pool)
	expected := acceptDispatchBacklog(t, ctx, store, 512)
	first, err := store.ClaimIngestionBatches(ctx, 1)
	if err != nil || len(first) != 1 || len(first[0].Receipts) != 32 || !reflect.DeepEqual(first[0].Receipts, expected[:32]) {
		t.Fatalf("first bounded arrival batch: %+v (%v), want first 32 receipts", first, err)
	}
	// Reconnect and advance the durable lease clock: no wall-clock wait.
	otherPool, err := pgxpool.NewWithConfig(ctx, pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer otherPool.Close()
	other := postgres.MaterializationStore{Pool: otherPool}
	if _, err = pool.Exec(ctx, "UPDATE ingestion_batches SET lease_until='-infinity'"); err != nil {
		t.Fatal(err)
	}
	recovered, err := other.ClaimIngestionBatches(ctx, 1)
	if err != nil || !reflect.DeepEqual(recovered, first) {
		t.Fatalf("lost acknowledgement regrouped work: got %+v (%v), want %+v", recovered, err, first)
	}
	if err = other.IngestionBatchDispatched(ctx, recovered[0].ID); err != nil {
		t.Fatal(err)
	}
	rest, err := other.ClaimIngestionBatches(ctx, 32)
	if err != nil || len(rest) != 15 {
		t.Fatalf("remaining arrivals: %+v (%v), want fifteen remaining batches", rest, err)
	}
	for i, b := range rest {
		if len(b.Receipts) != 32 || !reflect.DeepEqual(b.Receipts, expected[(i+1)*32:(i+2)*32]) {
			t.Fatalf("batch %d lost arrival order: %+v", i+1, b)
		}
		if err = other.IngestionBatchDispatched(ctx, b.ID); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := store.ClaimIngestionBatches(ctx, 8); err != nil || len(got) != 0 {
		t.Fatalf("acknowledged work returned: %+v (%v)", got, err)
	}
	var pending, receipts int
	if err = pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM ingestion_outbox)+(SELECT count(*) FROM ingestion_batches),(SELECT count(*) FROM ingestion_receipts)").Scan(&pending, &receipts); err != nil || pending != 0 || receipts != 512 {
		t.Fatalf("transfer lost audit or left work: %d pending, %d receipts (%v)", pending, receipts, err)
	}
}

func acceptDispatchBacklog(t *testing.T, ctx context.Context, store fixtureContentStores, n int) []content.Dispatch {
	t.Helper()
	pool := store.Pool
	var expected []content.Dispatch
	for i := 0; i < n; i++ {
		org := fmt.Sprintf("dispatch-%d", i%2)
		scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write"}, Corpora: []string{"*"}}
		c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "queue", Name: "Queue"})
		if err != nil {
			t.Fatal(err)
		}
		key := fmt.Sprint(i)
		receipt, err := (content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store}).Accept(ctx, scope, content.Command{Key: key, Source: content.Source{CorpusID: c.ID, Namespace: "tests", RecordKey: key}, Content: content.Text{Kind: "text", Text: "Queued receipt"}})
		if err != nil {
			t.Fatal(err)
		}
		expected = append(expected, content.Dispatch{Organization: org, ReceiptID: receipt.ID, TraceContext: telemetry.Encode(ctx)})
	}
	return expected
}

// Row locks are skipped immediately, and a second connection cannot take a
// live lease. Expiring a dead worker's lease makes precisely that work recoverable.
func TestIngestionClaimsSkipLockedReceiptsAndLiveBatchLeases(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := contentStores(pool)
	expected := acceptDispatchBacklog(t, ctx, store, 3)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT 1 FROM ingestion_outbox WHERE organization=$1 AND receipt_id=$2 FOR UPDATE", expected[0].Organization, expected[0].ReceiptID); err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimIngestionBatches(ctx, 1)
	if err != nil || len(first) != 1 || !reflect.DeepEqual(first[0].Receipts, expected[1:]) {
		t.Fatalf("locked oldest: got %+v (%v), want two unlocked arrivals", first, err)
	}
	otherPool, err := pgxpool.NewWithConfig(ctx, pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer otherPool.Close()
	other := postgres.MaterializationStore{Pool: otherPool}
	// An older locked receipt cannot hold up a recoverable batch. Advancing
	// the durable lease directly avoids any wall-clock wait.
	if _, err = pool.Exec(ctx, "UPDATE ingestion_batches SET lease_until='-infinity'"); err != nil {
		t.Fatal(err)
	}
	if got, err := other.ClaimIngestionBatches(ctx, 1); err != nil || !reflect.DeepEqual(got, first) {
		t.Fatalf("locked older receipt blocked batch recovery: %+v (%v), want %+v", got, err, first)
	}
	if got, err := other.ClaimIngestionBatches(ctx, 8); err != nil || len(got) != 0 {
		t.Fatalf("locked receipt and live batch lease returned %+v (%v)", got, err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	last, err := other.ClaimIngestionBatches(ctx, 8)
	if err != nil || len(last) != 1 || !reflect.DeepEqual(last[0].Receipts, expected[:1]) {
		t.Fatalf("unlocked oldest: got %+v (%v), want %+v", last, err, expected[0])
	}
	for _, b := range append(first, last...) {
		if err = other.IngestionBatchDispatched(ctx, b.ID); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := store.ClaimIngestionBatches(ctx, 8); err != nil || len(got) != 0 {
		t.Fatalf("drained queue: %+v (%v)", got, err)
	}
}

// Owns the durable API-to-dispatch handoff: a new pool must recover the original
// W3C parent and caller ID. Temporal tests cannot detect missing persisted data.
func TestAcceptancePersistsTraceAcrossDispatcherRestart(t *testing.T) {
	ctx := telemetry.Extract(context.Background(), http.Header{"Traceparent": []string{"00-11111111111111111111111111111111-2222222222222222-01"}})
	ctx = logging.WithRequestID(ctx, "caller-request-123")
	pool := scratchDatabase(t, ctx)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	expected := acceptDispatchBacklog(t, ctx, contentStores(pool), 1)
	next, err := pgxpool.NewWithConfig(ctx, pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	claimed, err := contentStores(next).ClaimIngestionBatches(context.Background(), 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %v (%v)", claimed, err)
	}
	restored := telemetry.Capture(telemetry.Restore(context.Background(), claimed[0].Receipts[0].TraceContext))
	if restored.Traceparent != "00-11111111111111111111111111111111-2222222222222222-01" || restored.RequestID != "caller-request-123" || claimed[0].Receipts[0].ReceiptID != expected[0].ReceiptID {
		t.Fatalf("durable trace lost: %+v", restored)
	}
}

// A live dispatcher can claim later arrivals without scanning or starting an
// older bulk backlog. Persisted retry batches retain their original class.
func TestIngestionQueueClaimsIsolateLiveFromBulk(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := contentStores(pool)
	bulk := acceptDispatchBacklog(t, workqueue.WithClass(ctx, "bulk"), store, 64)
	// The helper's unique request keys must not replay the older bulk receipts.
	liveCtx := workqueue.WithClass(ctx, "live")
	scope := corpus.Scope{Organization: "queue-live", Actions: []string{"corpora:write", "content:write"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "live", Name: "Live"})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := (content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store}).Accept(liveCtx, scope, content.Command{Key: "live", Source: content.Source{CorpusID: c.ID, Namespace: "tests", RecordKey: "live"}, Content: content.Text{Kind: "text", Text: "Live receipt"}})
	if err != nil {
		t.Fatal(err)
	}
	batches, err := store.ClaimIngestionBatches(liveCtx, 8)
	if err != nil || len(batches) != 1 || batches[0].WorkQueue != "live" || len(batches[0].Receipts) != 1 || batches[0].Receipts[0].ReceiptID != receipt.ID {
		t.Fatalf("live behind bulk: %+v %v", batches, err)
	}
	bulkBatches, err := store.ClaimIngestionBatches(workqueue.WithClass(ctx, "bulk"), 8)
	if err != nil || len(bulkBatches) != 2 {
		t.Fatalf("bulk batches: %+v %v", bulkBatches, err)
	}
	for i, b := range bulkBatches {
		if b.WorkQueue != "bulk" || !reflect.DeepEqual(b.Receipts, bulk[i*32:(i+1)*32]) {
			t.Fatalf("mixed class batch: %+v", b)
		}
	}
	for _, batch := range bulkBatches {
		if err = store.IngestionBatchDispatched(ctx, batch.ID); err != nil {
			t.Fatal(err)
		}
	}
	remaining, err := store.ClaimIngestionBatches(workqueue.WithClass(ctx, workqueue.Bulk), 8)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("acknowledged bulk was reclaimed: %+v %v", remaining, err)
	}
	if _, err = pool.Exec(ctx, "UPDATE ingestion_batches SET lease_until='-infinity'"); err != nil {
		t.Fatal(err)
	}
	retried, err := store.ClaimIngestionBatches(liveCtx, 1)
	if err != nil || !reflect.DeepEqual(retried, batches) {
		t.Fatalf("class changed on lost acknowledgement: %+v %v", retried, err)
	}
}
