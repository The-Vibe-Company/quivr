package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"testing"
	"testing/fstest"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// scratchDatabase creates an empty database next to the adapter database so
// migration history can be exercised without touching shared state.
func scratchDatabase(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
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
	admin, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	suffix := make([]byte, 6)
	if _, err = rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := "quivr_migrate_" + hex.EncodeToString(suffix)
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop %s: %v", name, err)
		}
	})
	scratch, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	scratch.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, scratch)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// embedded copies the embedded migrations whose names match keep.
func embedded(t *testing.T, keep *regexp.Regexp) fstest.MapFS {
	t.Helper()
	names, err := migrations.Names()
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{}
	for _, name := range names {
		if !keep.MatchString(name) {
			continue
		}
		data, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		fsys[name] = &fstest.MapFile{Data: data}
	}
	return fsys
}

func TestDatabaseAtNumberedHeadMigratesForwardToStampedMigrations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)

	// A database migrated by a binary that only knew the numbered set.
	numbered := embedded(t, regexp.MustCompile(`^[0-9]{3}_`))
	if len(numbered) == 0 {
		t.Fatal("no numbered migrations embedded")
	}
	if err := postgres.MigrateFS(ctx, pool, numbered); err != nil {
		t.Fatal(err)
	}

	// The next binary embeds every current migration plus a new stamped one.
	next := embedded(t, regexp.MustCompile(`.`))
	// Stamped at the end of time so it stays last whatever lands on main.
	const probe = "99991231T2359Z_migration_probe.sql"
	next[probe] = &fstest.MapFile{Data: []byte("CREATE TABLE migration_probe (id int PRIMARY KEY)")}
	pending, err := postgres.PendingMigrations(ctx, pool, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) == 0 || pending[len(pending)-1] != probe {
		t.Fatalf("pending before upgrade = %v, want it to end with %s", pending, probe)
	}

	if err = postgres.MigrateFS(ctx, pool, next); err != nil {
		t.Fatal(err)
	}
	if pending, err = postgres.PendingMigrations(ctx, pool, next); err != nil || len(pending) != 0 {
		t.Fatalf("pending after upgrade = %v, %v", pending, err)
	}
	var probed bool
	if err = pool.QueryRow(ctx, "SELECT to_regclass('migration_probe') IS NOT NULL").Scan(&probed); err != nil || !probed {
		t.Fatalf("probe table applied = %v, %v", probed, err)
	}
	// Rerunning is a no-op: the probe would fail if applied twice.
	if err = postgres.MigrateFS(ctx, pool, next); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaReadyRequiresEveryEmbeddedMigration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)

	if err := postgres.SchemaReady(ctx, pool); err == nil {
		t.Fatal("empty database reported ready")
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := postgres.SchemaReady(ctx, pool); err != nil {
		t.Fatalf("migrated database not ready: %v", err)
	}
	names, err := migrations.Names()
	if err != nil {
		t.Fatal(err)
	}
	latest := names[len(names)-1]
	if _, err = pool.Exec(ctx, "DELETE FROM schema_migrations WHERE name=$1", latest); err != nil {
		t.Fatal(err)
	}
	// Only pending migrations: running migrate completes it, so a worker waits.
	if err = postgres.SchemaReady(ctx, pool); err == nil || !errors.Is(err, postgres.ErrMigrationsPending) || !regexp.MustCompile(regexp.QuoteMeta(latest)).MatchString(err.Error()) {
		t.Fatalf("want a pending-migrations error naming %s, got %v", latest, err)
	}
	// A later migration this binary does not embed is applied: the schema is
	// newer than the binary, and waiting would never apply the missing one.
	const later = "99991231T2359Z_from_a_newer_binary.sql"
	if _, err = pool.Exec(ctx, "INSERT INTO schema_migrations VALUES($1)", later); err != nil {
		t.Fatal(err)
	}
	if err = postgres.SchemaReady(ctx, pool); !errors.Is(err, postgres.ErrSchemaNewer) || errors.Is(err, postgres.ErrMigrationsPending) {
		t.Fatalf("want a newer-schema error, got %v", err)
	}
	// With nothing pending, a newer schema stays ready, so the previous binary
	// still starts after a forward migration.
	if _, err = pool.Exec(ctx, "INSERT INTO schema_migrations VALUES($1)", latest); err != nil {
		t.Fatal(err)
	}
	if err = postgres.SchemaReady(ctx, pool); err != nil {
		t.Fatalf("newer schema with nothing pending not ready: %v", err)
	}
}
