package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/tei"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BootstrapDatabase is the PostgreSQL part of `quivr migrate`: it applies the
// embedded migrations, registers the deployment's vector spaces and routes
// new Corpora to a default projection generation carrying them. It needs no
// S3, Weaviate or tokenizer, so a bare database can be prepared for the
// PostgreSQL adapter tests (make adapter-postgres). Every step is idempotent.
func BootstrapDatabase(ctx context.Context, pool *pgxpool.Pool, spaces []content.RegisteredSpace) error {
	if err := postgres.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := (postgres.ContentStore{Pool: pool}).RegisterSpaces(ctx, spaces); err != nil {
		return fmt.Errorf("vector space registry: %w", err)
	}
	if err := (postgres.ContentStore{Pool: pool}).BootstrapGeneration(ctx, weaviate.InitialCollection, tei.Space().ID); err != nil {
		return fmt.Errorf("default projection generation: %w", err)
	}
	return alignDefaultGeneration(ctx, postgres.ContentStore{Pool: pool})
}

// alignDefaultGeneration moves new Corpora onto the registered spaces once
// they changed, keeping every existing Corpus on the generation serving it.
func alignDefaultGeneration(ctx context.Context, store postgres.ContentStore) error {
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
