package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

func TestCorpusArchiveAndRename(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := rebuildAdapterPool(t, ctx)
	service := corpus.Service{Store: postgres.Store{Pool: pool}}
	scope := corpus.Scope{Organization: fmt.Sprintf("adapter-lifecycle-%d", time.Now().UnixNano()), Actions: []string{"corpora:write", "corpora:read", "corpora:archive", "corpora:rename"}, Corpora: []string{"*"}}
	c, _, err := service.Create(ctx, scope, corpus.CreateInput{Key: "collection", Name: "Collection"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Archive(ctx, scope, c.ID, true); err != nil {
		t.Fatal(err)
	}
	if items, err := service.List(ctx, scope, "", 10); err != nil || len(items) != 0 {
		t.Fatalf("archived listing: %+v %v", items, err)
	}
	if items, err := service.ListArchived(ctx, scope, "", 10, true); err != nil || len(items) != 1 || !items[0].Archived {
		t.Fatalf("administrative listing: %+v %v", items, err)
	}
	if c, err = service.Rename(ctx, scope, c.ID, "Renamed collection"); err != nil || c.Name != "Renamed collection" || !c.Archived {
		t.Fatalf("rename: %+v %v", c, err)
	}
	if _, err = service.Archive(ctx, scope, c.ID, false); err != nil {
		t.Fatal(err)
	}
	if items, err := service.List(ctx, scope, "", 10); err != nil || len(items) != 1 || items[0].Name != "Renamed collection" || items[0].Archived {
		t.Fatalf("restored listing: %+v %v", items, err)
	}
}
