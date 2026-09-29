package app

import (
	"context"
	"fmt"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/tei"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/weaviate"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BootstrapDatabase is the PostgreSQL part of `quivr migrate`: it applies the
// embedded migrations and routes new Corpora to the default projection
// generation. It needs no S3, Weaviate or tokenizer, so a bare database can be
// prepared for the PostgreSQL adapter tests (make adapter-postgres). Both steps
// are idempotent.
func BootstrapDatabase(ctx context.Context, pool *pgxpool.Pool) error {
	if err := postgres.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := (postgres.ContentStore{Pool: pool}).BootstrapGeneration(ctx, weaviate.InitialCollection, tei.Space().ID); err != nil {
		return fmt.Errorf("default projection generation: %w", err)
	}
	return nil
}
