package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/app"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost/fakeplugin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestMain prepares the database named by QUIVR_ADAPTER_CONFIG with the
// PostgreSQL part of `quivr migrate`, so the suite runs on a bare database
// (make adapter-postgres). Inside make verify the stack's `quivr migrate`
// already prepared it and registered the pinned plugin's vector spaces, which
// this leaves as they are.
func TestMain(m *testing.M) {
	// Lets this test binary serve as the controllable fake plugin process
	// (normalization_failures_test.go); that process never touches the database.
	fakeplugin.MaybeRun()
	if path := os.Getenv("QUIVR_ADAPTER_CONFIG"); path != "" {
		if err := bootstrap(path); err != nil {
			fmt.Fprintln(os.Stderr, "adapter database bootstrap failed:", err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

// postgresOnly reports a run against PostgreSQL alone (make adapter-postgres).
// Only then may a test that also needs the tokenizer, TEI or S3 skip; inside
// make verify a missing dependency still fails it.
func postgresOnly() bool { return os.Getenv("QUIVR_ADAPTER_POSTGRES_ONLY") == "1" }

func bootstrap(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg struct {
		DatabaseURL string `json:"database_url"`
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	var served int
	if pool.QueryRow(ctx, `SELECT count(*) FROM vector_spaces WHERE role='served'`).Scan(&served) == nil && served > 0 {
		return nil
	}
	return app.BootstrapDatabase(ctx, pool, app.Config{}.DeploymentSpaces(nil))
}
