package postgres

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/The-Vibe-Company/quivr/migrations"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const migrationAdvisoryLock int64 = 642001

// Migrate applies pending expand migrations, leaving contracts for the operator.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return MigrateFS(ctx, pool, migrations.Files)
}

// MigrateFS applies, in one transaction and in lexical filename order, every expand
// migration of fsys that is not yet recorded in schema_migrations. Any missing
// file is applied, not only files after the latest applied one, so a database
// at an older head migrates forward without manual steps.
func MigrateFS(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) error {
	return migrateFS(ctx, pool, fsys, false)
}

// MigrateContracts explicitly applies pending expand and contract migrations.
func MigrateContracts(ctx context.Context, pool *pgxpool.Pool) error {
	return MigrateContractsFS(ctx, pool, migrations.Files)
}

// MigrateContractsFS is the explicit operator path for a supplied migration set.
func MigrateContractsFS(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) error {
	return migrateFS(ctx, pool, fsys, true)
}

// ErrMigrationBusy marks a migration that could not take the migration lock
// within its timeout: another migrator holds it. Rerun migrate once it ends.
var ErrMigrationBusy = errors.New("another migration is running")

func migrateFS(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, includeContract bool) error {
	names, err := migrations.Plan(fsys, includeContract)
	if err != nil {
		return err
	}
	// An up-to-date schema needs no lock: a deploy without new migrations
	// starts even while another session holds the migration lock.
	if pending, _, err := pendingMigrations(ctx, pool, names); err != nil || len(pending) == 0 {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Bound locks and SQL work while older processes are serving, and let the
	// server end a migrator that vanished mid-transaction instead of keeping
	// the migration lock until TCP keepalive notices. All three settings are
	// transaction-local and disappear on commit or rollback.
	if _, err = tx.Exec(ctx, "SET LOCAL lock_timeout = '2s'; SET LOCAL statement_timeout = '5s'; SET LOCAL idle_in_transaction_session_timeout = '5s'"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationAdvisoryLock); err != nil {
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && databaseError.Code == "55P03" {
			return fmt.Errorf("%w: %w", ErrMigrationBusy, err)
		}
		return err
	}
	if _, err = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY)"); err != nil {
		return err
	}
	for _, name := range names {
		var applied bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)", name).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		sql, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("migration %s failed: %w", name, err)
		}
		if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations VALUES($1)", name); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// schemaState returns the pending migrations of fsys and, when there are
// some, the latest recorded migration sorting after every file of fsys ("" if
// none).
func schemaState(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) (pending []string, newer string, err error) {
	names, err := migrations.Plan(fsys, false)
	if err != nil {
		return nil, "", err
	}
	pending, tracked, err := pendingMigrations(ctx, pool, names)
	if err != nil || !tracked || len(pending) == 0 {
		return pending, "", err
	}
	var latest *string
	if err = pool.QueryRow(ctx, "SELECT max(name) FROM schema_migrations WHERE name > $1", names[len(names)-1]).Scan(&latest); err != nil {
		return nil, "", err
	}
	if latest != nil {
		newer = *latest
	}
	return pending, newer, nil
}

// ErrMigrationsPending marks a schema that lacks only migrations this binary
// embeds, so running migrate with this binary, or a newer one, completes it.
var ErrMigrationsPending = errors.New("migrations pending")

// ErrSchemaNewer marks a schema that misses a migration of this binary while
// recording a later one it does not embed: it was migrated past this binary
// without that migration, so waiting for the api's migrate would not help.
var ErrSchemaNewer = errors.New("schema newer than this binary")

// SchemaReady fails unless every embedded expand migration has been applied. It is
// derived from the embedded set, so adding a migration edits no other file.
// A schema that only misses migrations wraps ErrMigrationsPending, one newer
// than the binary ErrSchemaNewer. Migrations recorded beyond the embedded set
// are otherwise accepted, so a previous binary still starts after a forward
// migration.
func SchemaReady(ctx context.Context, pool *pgxpool.Pool) error {
	pending, newer, err := schemaState(ctx, pool, migrations.Files)
	switch {
	case err != nil:
		return err
	case len(pending) == 0:
		return nil
	case newer != "":
		return fmt.Errorf("schema migration missing: %s (%d pending) while %s is applied: %w", pending[0], len(pending), newer, ErrSchemaNewer)
	}
	return fmt.Errorf("schema migration missing: %s (%d pending): %w", pending[0], len(pending), ErrMigrationsPending)
}

// pendingMigrations returns, in order, the names not recorded in
// schema_migrations, and whether that table exists yet: before the first
// migration every name is pending.
func pendingMigrations(ctx context.Context, pool *pgxpool.Pool, names []string) (pending []string, tracked bool, err error) {
	if err = pool.QueryRow(ctx, "SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&tracked); err != nil {
		return nil, false, err
	}
	if !tracked {
		return names, false, nil
	}
	applied := map[string]bool{}
	rows, err := pool.Query(ctx, "SELECT name FROM schema_migrations WHERE name = ANY($1)", names)
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, true, err
		}
		applied[name] = true
	}
	if err = rows.Err(); err != nil {
		return nil, true, err
	}
	for _, name := range names {
		if !applied[name] {
			pending = append(pending, name)
		}
	}
	return pending, true, nil
}
