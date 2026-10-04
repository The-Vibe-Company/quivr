package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestChangeJournalWindowsAndRetention reads the real commit-ordered journal:
// Corpus filtering, bounded pages, head advancement over invisible positions
// and read-time retention with a short configured window.
func TestChangeJournalWindowsAndRetention(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-changes-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "content:write"}, Corpora: []string{"*"}}
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
	service := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store}
	accept := func(corpusID, key string) {
		t.Helper()
		if _, err := service.Accept(ctx, scope, content.Command{Key: key, Source: content.Source{CorpusID: corpusID, Namespace: "changes", RecordKey: key}, Content: content.Text{Kind: "text", Text: "Change " + key}}); err != nil {
			t.Fatal(err)
		}
	}
	const week = 7 * 24 * time.Hour
	empty, err := store.ReadChanges(ctx, scope.Organization, a.ID, 0, 10, week)
	if err != nil || empty.Head != 0 || empty.Through != 0 || len(empty.Events) != 0 || empty.Expired {
		t.Fatal("empty journal", empty, err)
	}
	accept(a.ID, "a1")
	accept(b.ID, "b1")
	accept(a.ID, "a2")
	// Each acceptance commits receipt.pending then record.accepted: six positions.
	all, err := store.ReadChanges(ctx, scope.Organization, a.ID, 0, 10, week)
	if err != nil || all.Head != 6 || all.Through != 6 || len(all.Events) != 4 {
		t.Fatal("visible window", all, err)
	}
	for i, e := range all.Events {
		if e.CorpusID != a.ID || e.ID == "" || (i > 0 && e.Position <= all.Events[i-1].Position) {
			t.Fatal("event not scoped or not commit ordered", all.Events)
		}
	}
	if all.Events[1].Type != "record.accepted" || all.Events[1].ResourceKind != "record" {
		t.Fatal("missing Record invalidation", all.Events[1])
	}
	page, err := store.ReadChanges(ctx, scope.Organization, a.ID, 0, 3, week)
	if err != nil || len(page.Events) != 3 || page.Through != page.Events[2].Position || page.Head != 6 {
		t.Fatal("bounded page", page, err)
	}
	invisible, err := store.ReadChanges(ctx, scope.Organization, a.ID, 2, 10, week)
	if err != nil || invisible.Events[0].Position != 5 {
		t.Fatal("invisible positions not skipped", invisible, err)
	}
	head, err := store.ReadChanges(ctx, scope.Organization, a.ID, 6, 0, week)
	if err != nil || head.Through != 6 || head.Expired {
		t.Fatal("start at head", head, err)
	}

	// A 1 µs window, the shortest PostgreSQL expresses: position 1 committed
	// several round trips ago, so it is already past retention.
	stale, err := store.ReadChanges(ctx, scope.Organization, a.ID, 0, 10, time.Microsecond)
	if err != nil || !stale.Expired {
		t.Fatal("unconsumed position older than retention did not expire", stale, err)
	}
	caughtUp, err := store.ReadChanges(ctx, scope.Organization, a.ID, 6, 10, time.Microsecond)
	if err != nil || caughtUp.Expired {
		t.Fatal("cursor at head must not expire on a quiet journal", caughtUp, err)
	}
}
