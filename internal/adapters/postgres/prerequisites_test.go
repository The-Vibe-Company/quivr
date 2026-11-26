package postgres_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The stock server and DBA-provisioned login own compatibility at the SQL
// boundary. TestMain bootstraps the blank database with this same login.
func TestExternalPostgresPrerequisites(t *testing.T) {
	if os.Getenv("QUIVR_EXTERNAL_POSTGRES") != "1" {
		t.Skip("requires the stock PostgreSQL fixture and restricted login")
	}
	if os.Getenv("QUIVR_ADAPTER_CONFIG") == "" {
		t.Fatal("QUIVR_ADAPTER_CONFIG must point to the restricted PostgreSQL login")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	var stock, restricted bool
	err := pool.QueryRow(ctx, `SELECT
		current_setting('server_version_num')::int / 10000 = 17
		  AND NOT EXISTS (SELECT FROM pg_extension WHERE extname <> 'plpgsql'),
		NOT (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)
		  AND NOT has_database_privilege(current_database(), 'CREATE')
		FROM pg_roles WHERE rolname = current_user`).Scan(&stock, &restricted)
	if err != nil || !stock || !restricted {
		t.Fatalf("want stock PostgreSQL 17 and restricted role, got stock=%v restricted=%v: %v", stock, restricted, err)
	}
	if err := postgres.CheckPrerequisites(ctx, pool, false); err != nil {
		t.Fatalf("startup preflight with the provisioned role: %v", err)
	}
	if err := postgres.SchemaReady(ctx, pool); err != nil {
		t.Fatalf("bootstrap did not leave a ready schema: %v", err)
	}
	if err := postgres.EnsureIndexes(ctx, pool); err != nil {
		t.Fatalf("startup index maintenance with the provisioned role: %v", err)
	}

	for _, tc := range []struct {
		name, setting, value, user, guidance string
		migrate                              bool
	}{
		{name: "wrong schema", setting: "search_path", value: "pg_catalog, public", guidance: "Set search_path to public", migrate: true},
		{name: "read-only startup", setting: "default_transaction_read_only", value: "on", guidance: "Connect Quivr to a writable primary"},
		{name: "missing schema creation grant", user: "quivr_unprovisioned", guidance: "CREATE on schema public", migrate: true},
		{name: "missing ledger access", user: "quivr_unprovisioned", guidance: "cannot read public.schema_migrations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := pool.Config()
			if tc.setting != "" {
				cfg.ConnConfig.RuntimeParams[tc.setting] = tc.value
			}
			if tc.user != "" {
				cfg.ConnConfig.User = tc.user
			}
			bad, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer bad.Close()
			if tc.migrate {
				err = postgres.Migrate(ctx, bad)
			} else {
				err = postgres.CheckPrerequisites(ctx, bad, false)
			}
			var prerequisite *postgres.PrerequisiteError
			if !errors.As(err, &prerequisite) || !strings.Contains(prerequisite.Error(), tc.guidance) {
				t.Fatalf("want PostgreSQL prerequisite guidance %q, got %v", tc.guidance, err)
			}
		})
	}
}
