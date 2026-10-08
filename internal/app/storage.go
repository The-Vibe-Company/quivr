package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// storageError contains only engine-owned codes, never a store's diagnostic.
type storageError struct{ code string }

func (e *storageError) Error() string { return e.code }

// runStorage verifies that PostgreSQL has the schema required by this binary.
// It does not boot plugins, a tokenizer, Temporal or a search index, and never
// invokes an embedder or object-storage client.
func runStorage(cfg Config, args []string, out io.Writer) error {
	if len(args) == 0 {
		return &storageError{code: "storage_usage"}
	}
	if args[0] != "status" {
		return &storageError{code: "storage_unknown_command"}
	}
	if len(args) != 1 {
		return &storageError{code: "storage_invalid_arguments"}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	poolConfig, err := postgres.PoolConfig(cfg.DatabaseURL, cfg.TLS.Postgres)
	if err != nil {
		return &storageError{code: "storage_invalid_database_settings"}
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return &storageError{code: "storage_database_unavailable"}
	}
	defer pool.Close()
	if err := postgres.SchemaReady(ctx, pool); err != nil {
		return &storageError{code: "storage_status_failed"}
	}
	return json.NewEncoder(out).Encode(map[string]bool{"compact": true})
}
