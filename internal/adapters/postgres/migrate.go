package postgres

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/The-Vibe-Company/quivr-v2/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Migrate applies every embedded migration that is not yet recorded.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return MigrateFS(ctx, pool, migrations.Files)
}

// MigrateFS applies, in one transaction and in lexical filename order, every
// migration of fsys that is not yet recorded in schema_migrations. Any missing
// file is applied, not only files after the latest applied one, so a database
// at an older head migrates forward without manual steps.
func MigrateFS(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) error {
	names, err := migrations.NamesIn(fsys)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(642001)"); err != nil {
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

// PendingMigrations lists, in apply order, the migrations of fsys that are not
// recorded in schema_migrations.
func PendingMigrations(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) ([]string, error) {
	names, err := migrations.NamesIn(fsys)
	if err != nil {
		return nil, err
	}
	var tracked bool
	if err = pool.QueryRow(ctx, "SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&tracked); err != nil {
		return nil, err
	}
	if !tracked {
		return names, nil
	}
	applied := map[string]bool{}
	rows, err := pool.Query(ctx, "SELECT name FROM schema_migrations WHERE name = ANY($1)", names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		applied[name] = true
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	var pending []string
	for _, name := range names {
		if !applied[name] {
			pending = append(pending, name)
		}
	}
	return pending, nil
}

// SchemaReady fails unless every embedded migration has been applied. It is
// derived from the embedded set, so adding a migration edits no other file.
func SchemaReady(ctx context.Context, pool *pgxpool.Pool) error {
	pending, err := PendingMigrations(ctx, pool, migrations.Files)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		return fmt.Errorf("schema migration missing: %s (%d pending)", pending[0], len(pending))
	}
	return nil
}
