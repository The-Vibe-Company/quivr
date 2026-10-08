package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/app"
	"github.com/The-Vibe-Company/quivr/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Owns nonblocking database setup and resumable performance-index maintenance
// against real PostgreSQL, including invalid-index recovery and name conflicts.
func TestDatabaseSetupAndBackgroundLookupIndexes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, upgraded := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "upgrade"}[upgraded], func(t *testing.T) {
			pool := scratchDatabase(t, ctx)
			if upgraded {
				if err := postgres.Migrate(ctx, pool); err != nil {
					t.Fatal(err)
				}
				// A writer that can hold optional index work indefinitely must not
				// delay required migration setup or schema readiness.
				writer, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer writer.Rollback(ctx)
				if _, err := writer.Exec(ctx, "LOCK TABLE segments IN ROW EXCLUSIVE MODE"); err != nil {
					t.Fatal(err)
				}
				startup, stop := context.WithTimeout(ctx, time.Second)
				err = app.BootstrapDatabase(startup, pool, app.DeploymentSpaces(nil))
				stop()
				if err != nil {
					t.Fatalf("blocked performance index prevented migration setup: %v", err)
				}
				if err := postgres.SchemaReady(ctx, pool); err != nil {
					t.Fatalf("blocked performance index prevented readiness: %v", err)
				}
				if err := writer.Rollback(ctx); err != nil {
					t.Fatal(err)
				}
			}
			bootstrap := func() {
				t.Helper()
				if err := app.BootstrapDatabase(ctx, pool, app.DeploymentSpaces(nil)); err != nil {
					t.Fatal(err)
				}
			}
			index := func(name, want string) uint32 {
				t.Helper()
				var oid uint32
				var valid bool
				var definition string
				if err := pool.QueryRow(ctx, `SELECT i.indexrelid, i.indisvalid, pg_get_indexdef(i.indexrelid)
FROM pg_index i WHERE i.indexrelid=to_regclass($1)`, name).Scan(&oid, &valid, &definition); err != nil {
					t.Fatalf("lookup index %s missing: %v", name, err)
				}
				if !valid || definition != want {
					t.Fatalf("want valid index %s, got valid=%v definition=%s", name, valid, definition)
				}
				return oid
			}
			segmentIndex := func() uint32 {
				return index("segments_by_segmentation", "CREATE INDEX segments_by_segmentation ON public.segments USING btree (organization, segmentation_id)")
			}
			rebuildIndex := func() uint32 {
				return index("records_by_current_version", "CREATE INDEX records_by_current_version ON public.records USING btree (organization, corpus_id, current_version_id)")
			}
			bootstrap()
			if upgraded {
				// Adopt an index already installed by an operator, without rebuilding it.
				if _, err := pool.Exec(ctx, "CREATE INDEX embedding_coverage_by_artifact ON embedding_coverage(organization,artifact_id)"); err != nil {
					t.Fatal(err)
				}
			}
			coverageIndex := func() uint32 {
				return index("embedding_coverage_by_artifact", "CREATE INDEX embedding_coverage_by_artifact ON public.embedding_coverage USING btree (organization, artifact_id)")
			}
			var installedOID uint32
			if upgraded {
				installedOID = coverageIndex()
			}
			if err := postgres.EnsureIndexes(ctx, pool); err != nil {
				t.Fatal(err)
			}
			coverageOID := coverageIndex()
			if upgraded && coverageOID != installedOID {
				t.Fatalf("operator index rebuilt: OID %d became %d", installedOID, coverageOID)
			}
			oid, rebuildOID := segmentIndex(), rebuildIndex()
			bootstrap()
			if err := postgres.EnsureIndexes(ctx, pool); err != nil {
				t.Fatal(err)
			}
			if got := segmentIndex(); got != oid {
				t.Fatalf("rerun rebuilt segment index: OID %d became %d", oid, got)
			}
			if got := rebuildIndex(); got != rebuildOID {
				t.Fatalf("rerun rebuilt current-version index: OID %d became %d", rebuildOID, got)
			}
			if got := coverageIndex(); got != coverageOID {
				t.Fatalf("rerun rebuilt coverage index: OID %d became %d", coverageOID, got)
			}
			if !upgraded {
				return
			}
			if _, err := pool.Exec(ctx, "DROP INDEX segments_by_segmentation"); err != nil {
				t.Fatal(err)
			}
			// An open writer holds the concurrent build after its catalog entry
			// commits. Cancel only once that entry is visible: no clock waits.
			writer, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Rollback(ctx)
			if _, err := writer.Exec(ctx, "LOCK TABLE segments IN ROW EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			maintenance := &app.IndexMaintenance{Pool: pool}
			runCtx, stopMaintenance := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { maintenance.Run(runCtx); close(done) }()
			defer func() { stopMaintenance(); <-done }()
			var buildPID int
			for {
				if err := pool.QueryRow(ctx, `SELECT COALESCE((SELECT pid FROM pg_stat_activity
WHERE datname=current_database() AND query LIKE 'CREATE INDEX CONCURRENTLY%segments_by_segmentation%'
AND wait_event_type='Lock' AND pid<>pg_backend_pid() LIMIT 1), 0)`).Scan(&buildPID); err != nil {
					t.Fatal(err)
				}
				if buildPID != 0 {
					break
				}
				select {
				case <-done:
					t.Fatal("maintenance ended before the blocked build was observed")
				default:
				}
			}
			// The maintainer owns its lock and waits on the writer. Required
			// setup and readiness must still complete before either is released.
			bootstrap()
			if err := postgres.SchemaReady(ctx, pool); err != nil {
				t.Fatalf("index build blocked schema readiness: %v", err)
			}
			startup, stopStartup := context.WithTimeout(ctx, time.Second)
			_, err = (postgres.PluginStore{Pool: pool}).ApplyConfiguration(startup, registry.Seed{})
			stopStartup()
			if err != nil {
				t.Fatalf("index build blocked required plugin configuration: %v", err)
			}
			if err := postgres.EnsureIndexes(ctx, pool); !errors.Is(err, postgres.ErrIndexBusy) {
				t.Fatalf("want prompt competing-maintainer contention, got %v", err)
			}
			// Exercise the actual two-second migrator lock timeout while another
			// installer holds the shared lock; do not delay on the test clock.
			installer, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer installer.Rollback(ctx)
			if _, err := installer.Exec(ctx, "SELECT pg_advisory_xact_lock(642001)"); err != nil {
				t.Fatal(err)
			}
			if err := app.BootstrapDatabase(ctx, pool, app.DeploymentSpaces(nil)); !errors.Is(err, postgres.ErrIndexBusy) {
				t.Fatalf("want retryable setup contention, got %v", err)
			}
			if err := installer.Rollback(ctx); err != nil {
				t.Fatal(err)
			}

			// The real migrator lock timeout elapsed while the optional DDL
			// waited. Its original backend must still be waiting, not canceled
			// by the old two-second index lock timeout.
			var waiting bool
			if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock' AND state='active')", buildPID).Scan(&waiting); err != nil || !waiting {
				t.Fatalf("concurrent DDL must keep waiting for its writer: waiting=%v, %v", waiting, err)
			}
			var canceled bool
			if err := pool.QueryRow(ctx, "SELECT pg_cancel_backend($1)", buildPID).Scan(&canceled); err != nil || !canceled {
				t.Fatalf("cancel blocked index attempt: canceled=%v, %v", canceled, err)
			}
			for maintenance.State() != "retrying" {
				// Round trips observe the canceled build rather than delay by time.
				if _, err := pool.Exec(ctx, "SELECT 1"); err != nil {
					t.Fatal(err)
				}
			}
			var invalid bool
			if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT FROM pg_index WHERE indexrelid=to_regclass('segments_by_segmentation') AND NOT indisvalid)").Scan(&invalid); err != nil || !invalid {
				t.Fatalf("canceled attempt must leave an invalid index: invalid=%v, %v", invalid, err)
			}
			if err := writer.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("background retry did not complete after the writer released its lock")
			}
			if got := maintenance.State(); got != "ready" {
				t.Fatalf("background maintenance state: want ready, got %s", got)
			}
			segmentIndex()
			rebuildIndex()

			// Reuse must preserve the equality query's columns, collations and
			// default operator classes. Conflicting objects remain untouched.
			for _, keys := range []string{"version_id", `organization COLLATE "C", segmentation_id`, "organization text_pattern_ops, segmentation_id"} {
				if _, err := pool.Exec(ctx, "DROP INDEX segments_by_segmentation; CREATE INDEX segments_by_segmentation ON segments("+keys+")"); err != nil {
					t.Fatal(err)
				}
				bootstrap()
				if err := postgres.EnsureIndexes(ctx, pool); err == nil || !strings.Contains(err.Error(), "incompatible definition") {
					t.Fatalf("want actionable index conflict for %s, got %v", keys, err)
				}
				var definition string
				if err := pool.QueryRow(ctx, "SELECT pg_get_indexdef('segments_by_segmentation'::regclass)").Scan(&definition); err != nil || definition != "CREATE INDEX segments_by_segmentation ON public.segments USING btree ("+keys+")" {
					t.Fatalf("conflicting index changed: %s, %v", definition, err)
				}
			}
		})
	}
}

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
	if err := postgres.SchemaReady(ctx, pool); !errors.Is(err, postgres.ErrMigrationsPending) {
		t.Fatalf("numbered schema must need migration: %v", err)
	}
	if err := postgres.MigrateFS(ctx, pool, next); err != nil {
		t.Fatal(err)
	}
	if err := postgres.SchemaReady(ctx, pool); err != nil {
		t.Fatalf("upgraded schema not ready: %v", err)
	}
	var probed bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('migration_probe') IS NOT NULL").Scan(&probed); err != nil || !probed {
		t.Fatalf("probe table applied = %v, %v", probed, err)
	}
	// Rerunning is a no-op: the probe would fail if applied twice.
	if err := postgres.MigrateFS(ctx, pool, next); err != nil {
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

// The real database owns the opt-in contract guarantee: default migrations
// leave old writers usable, and a failed contract rolls back its bookkeeping.
func TestContractMigrationRequiresExplicitOperator(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	fsys := fstest.MapFS{
		"20990101T0000Z_expand.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE old_writer(id int PRIMARY KEY, old_value text)")},
		"20990101T0001Z_contract.sql": &fstest.MapFile{Data: []byte("-- quivr:contract\nALTER TABLE old_writer DROP COLUMN old_value;")},
		"20990101T0002Z_expand.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE future_writer(id int PRIMARY KEY)")},
	}
	if err := postgres.MigrateFS(ctx, pool, fsys); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO old_writer VALUES (1, 'still usable'); INSERT INTO future_writer VALUES (1)"); err != nil {
		t.Fatalf("expand must preserve old writers and apply later independent expansions: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != 2 {
		t.Fatalf("default applied %d migrations, want 2: %v", count, err)
	}
	if err := postgres.MigrateContractsFS(ctx, pool, fsys); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO old_writer(id, old_value) VALUES (2, 'gone')"); err == nil {
		t.Fatal("explicit contract did not remove the old column")
	}
	if err := postgres.MigrateContractsFS(ctx, pool, fsys); err != nil {
		t.Fatalf("contract rerun: %v", err)
	}
	fsys["20990101T0003Z_bad_contract.sql"] = &fstest.MapFile{Data: []byte("-- quivr:contract\nDROP TABLE future_writer; SELECT 1/0;")}
	if err := postgres.MigrateContractsFS(ctx, pool, fsys); err == nil {
		t.Fatal("invalid contract must fail")
	}
	if _, err := pool.Exec(ctx, "INSERT INTO future_writer VALUES (2)"); err != nil {
		t.Fatalf("failed contract must roll back SQL: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != 3 {
		t.Fatalf("failed contract bookkeeping: %d, %v", count, err)
	}
}
