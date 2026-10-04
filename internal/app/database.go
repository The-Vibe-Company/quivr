package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/adapters/tei"
	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BootstrapDatabase is the PostgreSQL part of `quivr migrate`: it applies the
// embedded migrations, registers the deployment's vector spaces (until a
// Pipeline Plan is active: api and worker then register the plan's) and
// routes new Corpora to a default projection generation carrying them. It
// needs no S3, Weaviate or tokenizer, so a bare database can be prepared for
// the PostgreSQL adapter tests (make adapter-postgres). Every step is
// idempotent.
func BootstrapDatabase(ctx context.Context, pool *pgxpool.Pool, spaces []content.RegisteredSpace) error {
	if err := postgres.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Once a Pipeline Plan is active, api and worker register the spaces of
	// the plan they follow, which an operator may have changed since the
	// configuration last applied; migrate leaves them alone.
	plan, err := (postgres.PluginStore{Pool: pool}).ActivePlanID(ctx)
	if err != nil {
		return fmt.Errorf("pipeline plan: %w", err)
	}
	if plan == "" {
		if err := (postgres.SpaceStore{Pool: pool}).RegisterSpaces(ctx, spaces); err != nil {
			return fmt.Errorf("vector space registry: %w", err)
		}
	}
	if err := (postgres.ProjectionStore{Pool: pool}).BootstrapGeneration(ctx, weaviate.InitialCollection, tei.Space().ID); err != nil {
		return fmt.Errorf("default projection generation: %w", err)
	}
	return alignDefaultGeneration(ctx, postgres.ProjectionStore{Pool: pool})
}

// alignDefaultGeneration moves new Corpora onto the registered spaces once
// they changed, keeping every existing Corpus on the generation serving it.
func alignDefaultGeneration(ctx context.Context, store postgres.ProjectionStore) error {
	move, err := store.AlignDefaultGeneration(ctx)
	if err != nil {
		return fmt.Errorf("default projection generation: %w", err)
	}
	if move.Current != "" {
		slog.Info("new Corpora start on the registered vector spaces; existing Corpora keep their generation until rebuilt",
			"generation", move.Current, "previous", move.Previous, "corpora_kept", move.Pinned)
	}
	return nil
}
