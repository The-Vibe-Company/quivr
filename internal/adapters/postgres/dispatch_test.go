package postgres_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
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
	store := postgres.ContentStore{Pool: pool}
	expected := acceptDispatchBacklog(t, ctx, store, 512)
	for offset := 0; offset < len(expected); offset += 32 {
		got, err := store.Claim(ctx, 32)
		if err != nil || len(got) != 32 {
			t.Fatalf("batch at %d: got %d receipts (%v), want 32", offset, len(got), err)
		}
		for i, d := range got {
			if d != expected[offset+i] {
				t.Fatalf("arrival %d: got %+v, want %+v", offset+i, d, expected[offset+i])
			}
			if err = store.Dispatched(ctx, d); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A newly connected worker cannot redispatch any acknowledged receipt.
	// Reconnect to the isolated database, rather than the shared suite database.
	nextPool, err := pgxpool.NewWithConfig(ctx, pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer nextPool.Close()
	restarted := postgres.ContentStore{Pool: nextPool}
	if got, err := restarted.Claim(ctx, 32); err != nil || len(got) != 0 {
		t.Fatalf("after restart got %+v (%v), want drained queue", got, err)
	}
	var intents, receipts, events int
	if err = pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM ingestion_outbox),(SELECT count(*) FROM ingestion_receipts),(SELECT count(*) FROM change_events WHERE event_type='receipt.pending')").Scan(&intents, &receipts, &events); err != nil {
		t.Fatal(err)
	}
	if intents != 0 || receipts != 512 || events != 512 {
		t.Fatalf("after acknowledgement: %d intents, %d receipts, %d audit events", intents, receipts, events)
	}
}

func acceptDispatchBacklog(t *testing.T, ctx context.Context, store postgres.ContentStore, n int) []content.Dispatch {
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
		receipt, err := (content.Service{Repository: store}).Accept(ctx, scope, content.Command{Key: key, Source: content.Source{CorpusID: c.ID, Namespace: "tests", RecordKey: key}, Content: content.Text{Kind: "text", Text: "Queued receipt"}})
		if err != nil {
			t.Fatal(err)
		}
		expected = append(expected, content.Dispatch{Organization: org, ReceiptID: receipt.ID})
	}
	return expected
}

// Row locks are skipped immediately, and a second connection cannot take a
// live lease. Expiring a dead worker's lease makes precisely that work recoverable.
func TestIngestionClaimsSkipLocksAndRecoverAbandonedLeases(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := postgres.ContentStore{Pool: pool}
	expected := acceptDispatchBacklog(t, ctx, store, 3)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT 1 FROM ingestion_outbox WHERE organization=$1 AND receipt_id=$2 FOR UPDATE", expected[0].Organization, expected[0].ReceiptID); err != nil {
		t.Fatal(err)
	}
	got, err := store.Claim(ctx, 1)
	if err != nil || len(got) != 1 || got[0] != expected[1] {
		t.Fatalf("locked oldest: got %+v (%v), want %+v", got, err, expected[1])
	}
	otherPool, err := pgxpool.NewWithConfig(ctx, pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer otherPool.Close()
	other := postgres.ContentStore{Pool: otherPool}
	got, err = other.Claim(ctx, 32)
	if err != nil || len(got) != 1 || got[0] != expected[2] {
		t.Fatalf("live lease: got %+v (%v), want %+v", got, err, expected[2])
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = other.Claim(ctx, 32)
	if err != nil || len(got) != 1 || got[0] != expected[0] {
		t.Fatalf("unlocked oldest: got %+v (%v), want %+v", got, err, expected[0])
	}
	for _, d := range []content.Dispatch{expected[0], expected[1]} {
		if err = store.Dispatched(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	// Advance the durable lease clock directly, without a wall-clock wait.
	if _, err = pool.Exec(ctx, "UPDATE ingestion_outbox SET lease_until='-infinity'"); err != nil {
		t.Fatal(err)
	}
	got, err = other.Claim(ctx, 32)
	if err != nil || len(got) != 1 || got[0] != expected[2] {
		t.Fatalf("expired lease: got %+v (%v), want %+v", got, err, expected[2])
	}
	if err = other.Dispatched(ctx, got[0]); err != nil {
		t.Fatal(err)
	}
	if got, err = store.Claim(ctx, 32); err != nil || len(got) != 0 {
		t.Fatalf("drained queue: got %+v (%v)", got, err)
	}
}

// Upgrades retain receipt audit facts and the lease of waiting work while
// backfilling its original arrival time and removing historical acknowledgements.
func TestIngestionQueueMigrationPreservesWaitingReceipts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	prior := embedded(t, regexp.MustCompile(".*"))
	for name := range prior {
		if strings.HasSuffix(name, "_ingestion_dispatch_batches.sql") {
			delete(prior, name)
		}
	}
	if err := postgres.MigrateFS(ctx, pool, prior); err != nil {
		t.Fatal(err)
	}
	store := postgres.ContentStore{Pool: pool}
	expected := acceptDispatchBacklog(t, ctx, store, 3)
	first := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	for i, d := range expected {
		if _, err := pool.Exec(ctx, "UPDATE ingestion_receipts SET accepted_at=$3 WHERE organization=$1 AND id=$2", d.Organization, d.ReceiptID, first.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, "UPDATE ingestion_outbox SET dispatched=true WHERE organization=$1 AND receipt_id=$2", expected[0].Organization, expected[0].ReceiptID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE ingestion_outbox SET lease_until='infinity' WHERE organization=$1 AND receipt_id=$2", expected[2].Organization, expected[2].ReceiptID); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var intents, receipts int
	if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM ingestion_outbox),(SELECT count(*) FROM ingestion_receipts)").Scan(&intents, &receipts); err != nil {
		t.Fatal(err)
	}
	if intents != 2 || receipts != 3 {
		t.Fatalf("migration: %d intents, %d receipts, want 2 and 3", intents, receipts)
	}
	var enqueued time.Time
	if err := pool.QueryRow(ctx, "SELECT enqueued_at FROM ingestion_outbox WHERE organization=$1 AND receipt_id=$2", expected[1].Organization, expected[1].ReceiptID).Scan(&enqueued); err != nil {
		t.Fatal(err)
	}
	if !enqueued.Equal(first.Add(time.Second)) {
		t.Fatalf("backfilled arrival %v, want %v", enqueued, first.Add(time.Second))
	}
	got, err := store.Claim(ctx, 32)
	if err != nil || len(got) != 1 || got[0] != expected[1] {
		t.Fatalf("upgrade claim: got %+v (%v), want %+v", got, err, expected[1])
	}
}
