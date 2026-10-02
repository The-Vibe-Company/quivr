package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

// Real PostgreSQL owns reservation concurrency, multi-key rollback, instance
// isolation, expiry, and token-scoped release. Explicit times avoid waits.
func TestConnectorReplayReservationsAreAtomicAndExpire(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-replay-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "connectors:write"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "c", Name: "Replay"})
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.ConnectorStore{ContentStore: postgres.ContentStore{Pool: pool}}
	registry, _ := connectors.NewRegistry(connectors.Fixture{})
	sealer, _ := connectors.NewSealer("adapter-test-credential-key-0123456789")
	svc := connectors.Service{Store: store, Registry: registry, Sealer: sealer}
	ids := []string{}
	for _, key := range []string{"a", "b"} {
		created, err := svc.Create(ctx, scope, connectors.CreateInput{Key: key, CorpusID: c.ID, Namespace: key, Kind: "fixture", Config: json.RawMessage(`{"script":[]}`)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, created.ID)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := now.Add(5 * time.Minute)
	reserve := func(id, token string, keys []string, at time.Time, want bool) {
		t.Helper()
		ok, err := store.ReserveReplay(ctx, scope.Organization, id, token, keys, at, at.Add(5*time.Minute))
		if err != nil || ok != want {
			t.Fatalf("reserve %s/%s at %s: %v %v want %v", id, token, at, ok, err, want)
		}
	}
	var wg sync.WaitGroup
	winners := make(chan string, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			token := fmt.Sprintf("token-%d", i)
			ok, err := store.ReserveReplay(ctx, scope.Organization, ids[0], token, []string{"signature-1", "idempotency-1"}, now, until)
			if err != nil {
				errs <- err
			} else if ok {
				winners <- token
			}
		}(i)
	}
	wg.Wait()
	close(winners)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	winner := ""
	n := 0
	for token := range winners {
		winner = token
		n++
	}
	if n != 1 {
		t.Fatalf("concurrent winners=%d want 1", n)
	}
	reserve(ids[0], "blocked", []string{"fresh-signature", "idempotency-1"}, now, false)
	// Failed second-key reservation must not retain its fresh first key.
	reserve(ids[0], "fresh", []string{"fresh-signature"}, now, true)
	reserve(ids[1], "other", []string{"signature-1", "idempotency-1"}, now, true)
	if err := store.ReleaseReplay(ctx, scope.Organization, ids[0], "non-owner"); err != nil {
		t.Fatal(err)
	}
	reserve(ids[0], "still-blocked", []string{"signature-1"}, now, false)
	if err := store.ReleaseReplay(ctx, scope.Organization, ids[0], winner); err != nil {
		t.Fatal(err)
	}
	reserve(ids[0], "replacement", []string{"signature-1", "idempotency-1"}, now, true)
	reserve(ids[0], "expired-replacement", []string{"signature-1", "idempotency-1"}, until, true)
	// An old delivery cannot release the replacement after expiry.
	if err := store.ReleaseReplay(ctx, scope.Organization, ids[0], "replacement"); err != nil {
		t.Fatal(err)
	}
	reserve(ids[0], "kept", []string{"signature-1"}, until, false)
}
