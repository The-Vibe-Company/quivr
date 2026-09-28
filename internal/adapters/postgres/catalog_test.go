package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRecordCatalogKeysetTraversal reads the real catalog: stable key order,
// withdrawn Records included, Corpus and Organization isolation, and bounded
// exclusive-key pages.
func TestRecordCatalogKeysetTraversal(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := postgres.ContentStore{Pool: pool}
	service := content.Service{Repository: store, Catalog: store}
	corpora := corpus.Service{Store: postgres.Store{Pool: pool}}
	scope := corpus.Scope{Organization: "adapter-catalog", Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	other := scope
	other.Organization = "adapter-catalog-other"
	a, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "catalog-a", Name: "Catalog A"})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "catalog-b", Name: "Catalog B"})
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for i := range 5 {
		r, err := service.Accept(ctx, scope, content.Command{Key: fmt.Sprint("catalog-", i), Source: content.Source{CorpusID: a.ID, Namespace: "adapter", RecordKey: fmt.Sprint("r", i)}, Content: content.Text{Kind: "text", Text: fmt.Sprint("Record ", i)}})
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, r.RecordID)
	}
	sort.Strings(want)
	if _, err = service.Accept(ctx, scope, content.Command{Key: "catalog-b", Source: content.Source{CorpusID: b.ID, Namespace: "adapter", RecordKey: "r0"}, Content: content.Text{Kind: "text", Text: "Elsewhere"}}); err != nil {
		t.Fatal(err)
	}
	withdrawn, err := service.Withdraw(ctx, scope, content.Withdrawal{Key: "catalog-wd", Source: content.Source{CorpusID: a.ID, Namespace: "adapter", RecordKey: "r3"}})
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	after := ""
	for {
		page, err := service.Records(ctx, scope, a.ID, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) > 2 {
			t.Fatalf("page exceeds limit: %d", len(page))
		}
		for _, r := range page {
			if r.Source.CorpusID != a.ID || r.Withdrawn != (r.ID == withdrawn.RecordID) || r.Source.Namespace != "adapter" {
				t.Fatalf("catalog entry %+v", r)
			}
			got = append(got, r.ID)
		}
		if len(page) < 2 {
			break
		}
		after = page[len(page)-1].ID
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("keyset traversal %v, want %v", got, want)
	}
	if foreign, err := store.Records(ctx, other.Organization, a.ID, "", 10); err != nil || len(foreign) != 0 {
		t.Fatalf("catalog crossed Organizations: %v %v", foreign, err)
	}
}
