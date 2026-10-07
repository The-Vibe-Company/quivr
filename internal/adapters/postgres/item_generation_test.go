package postgres_test

import (
	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/app"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"testing"
)

// Upgrading may change the default for NEW Corpora; an existing Corpus must
// stay pinned to its old immutable keyword recipe until its own rebuild.
func TestItemKeywordUpgradePreservesCorpusRoute(t *testing.T) {
	ctx := t.Context()
	pool := scratchDatabase(t, ctx)
	if err := app.BootstrapDatabase(ctx, pool, nil); err != nil {
		t.Fatal(err)
	}
	scope := corpus.Scope{Organization: "item-generation", Actions: []string{"corpora:write"}, Corpora: []string{"*"}}
	corpora := corpus.Service{Store: postgres.Store{Pool: pool}}
	old, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "old", Name: "Existing"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE projection_generations SET item_keywords_projected=false WHERE active`); err != nil {
		t.Fatal(err)
	}
	store := postgres.ProjectionStore{Pool: pool}
	prior, err := store.Generation(ctx, scope.Organization, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.BootstrapDatabase(ctx, pool, nil); err != nil {
		t.Fatal(err)
	}
	retained, err := store.Generation(ctx, scope.Organization, old.ID)
	if err != nil || retained.ID != prior.ID || retained.ItemKeywordsProjected {
		t.Fatalf("existing Corpus moved: %+v %v", retained, err)
	}
	fresh, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "fresh", Name: "New"})
	if err != nil {
		t.Fatal(err)
	}
	next, err := store.Generation(ctx, scope.Organization, fresh.ID)
	if err != nil || !next.ItemKeywordsProjected || next.ID == prior.ID {
		t.Fatalf("new default: %+v %v", next, err)
	}
}
