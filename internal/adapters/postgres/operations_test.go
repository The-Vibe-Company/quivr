package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func rebuildAdapterPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	path := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if path == "" {
		t.Skip("real PostgreSQL suite runs inside make verify")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		DatabaseURL string `json:"database_url"`
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Acceptance identity is Organization + Corpus + rebuild route + key; the
// changed-request conflict is only reachable here because the public
// ActionRequest carries nothing but the key.
func TestRebuildAcceptanceReplayConflictAndJournal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := rebuildAdapterPool(t, ctx)
	org := fmt.Sprintf("adapter-operations-%d", time.Now().UnixNano())
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write"}, Corpora: []string{"*"}}
	corpora := corpus.Service{Store: postgres.Store{Pool: pool}}
	a, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "a", Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "b", Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	store := contentStores(pool)
	request := []byte(`{"idempotency_key":"rebuild-1"}`)
	first, err := store.AcceptRebuild(ctx, org, a.ID, "rebuild-1", request)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.Kind != operations.KindProjectionRebuild || first.State != operations.StateQueued || first.CorpusID != a.ID || first.TargetGenerationID == "" {
		t.Fatalf("accepted operation %+v", first)
	}
	replay, err := store.AcceptRebuild(ctx, org, a.ID, "rebuild-1", request)
	if err != nil || replay.ID != first.ID || replay.TargetGenerationID != first.TargetGenerationID {
		t.Fatalf("replay %+v %v", replay, err)
	}
	if _, err = store.AcceptRebuild(ctx, org, a.ID, "rebuild-1", []byte(`{"idempotency_key":"rebuild-1","changed":true}`)); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("changed canonical request: %v", err)
	}
	other, err := store.AcceptRebuild(ctx, org, b.ID, "rebuild-1", request)
	if err != nil || other.ID == first.ID || other.TargetGenerationID == first.TargetGenerationID {
		t.Fatalf("same key on another Corpus must be independent: %+v %v", other, err)
	}
	if _, err = store.AcceptRebuild(ctx, org, "corpus_absent", "rebuild-1", request); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatalf("absent Corpus: %v", err)
	}
	if _, err = store.AcceptRebuild(ctx, "another-org", a.ID, "rebuild-1", request); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatalf("foreign Corpus: %v", err)
	}
	read, err := store.Operation(ctx, org, first.ID)
	if err != nil || read.ID != first.ID || read.State != operations.StateQueued || read.Counters == nil || read.Errors == nil {
		t.Fatalf("read %+v %v", read, err)
	}
	if _, err = store.Operation(ctx, "another-org", first.ID); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatalf("foreign read: %v", err)
	}
	window, err := store.ReadChanges(ctx, org, a.ID, 0, 100, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	updates := 0
	for _, e := range window.Events {
		if e.Type == "operation.updated" && e.ResourceKind == "operation" && e.ResourceID == first.ID {
			updates++
		}
	}
	if updates != 1 {
		t.Fatalf("operation.updated events for accepted+replayed operation = %d, want 1", updates)
	}
	// More than one intent must be returned per claim, and every leased batch
	// must be disjoint. Existing acceptance/journal assertions above own identity.
	for i := 0; i < 6; i++ {
		if _, err = store.AcceptRebuild(ctx, org, a.ID, fmt.Sprintf("batch-%d", i), request); err != nil {
			t.Fatal(err)
		}
	}
	firstBatch, err := store.ClaimOperations(ctx, 2)
	if err != nil || len(firstBatch) != 2 {
		t.Fatalf("first batch %+v: %v, want two intents", firstBatch, err)
	}
	// A transaction holding a pending row must not stop another claim.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var locked string
	if err = tx.QueryRow(ctx, `SELECT operation_id FROM operation_outbox WHERE organization=$1 AND NOT dispatched AND lease_until<now() ORDER BY operation_id LIMIT 1 FOR UPDATE`, org).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	secondBatch, err := store.ClaimOperations(ctx, 2)
	if err != nil || len(secondBatch) != 2 {
		t.Fatalf("second batch %+v: %v, want two unlocked intents", secondBatch, err)
	}
	for _, d := range secondBatch {
		if d.OperationID == locked {
			t.Fatal("claimed an intent locked by another dispatcher")
		}
		for _, first := range firstBatch {
			if d == first {
				t.Fatalf("claimed leased intent twice: %+v", d)
			}
		}
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// Make abandoned leases eligible deterministically. No clock wait is needed.
	if _, err = pool.Exec(ctx, `UPDATE operation_outbox SET lease_until='-infinity' WHERE organization=$1 AND NOT dispatched`, org); err != nil {
		t.Fatal(err)
	}
	claimed := map[string]bool{}
	for {
		batch, err := store.ClaimOperations(ctx, 32)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch) == 0 {
			break
		}
		for _, d := range batch {
			if d.Organization == org {
				claimed[d.OperationID] = true
			}
			if err = store.OperationDispatched(ctx, d); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !claimed[first.ID] || !claimed[other.ID] {
		t.Fatalf("dispatch intent missing: %v", claimed)
	}
}
