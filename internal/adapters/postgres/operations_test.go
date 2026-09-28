package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
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
	store := postgres.ContentStore{Pool: pool}
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
	claimed := map[string]bool{}
	for {
		d, err := store.ClaimOperation(ctx)
		if errors.Is(err, operations.ErrNoDispatch) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if d.Organization == org {
			claimed[d.OperationID] = true
		}
		if err = store.OperationDispatched(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if !claimed[first.ID] || !claimed[other.ID] {
		t.Fatalf("dispatch intent missing: %v", claimed)
	}
}
