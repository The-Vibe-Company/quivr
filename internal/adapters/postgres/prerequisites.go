package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PrerequisiteError contains operator guidance safe to report at startup.
type PrerequisiteError struct {
	problem string
}

func (e *PrerequisiteError) Error() string { return e.problem }

const ledgerInsertDenied = "PostgreSQL role cannot insert into public.schema_migrations. Ask the DBA to verify ownership of the Quivr schema objects and INSERT access to the migration ledger"

// CheckPrerequisites checks the actual session's schema and privileges without
// changing database state. Quivr's SQL uses public, including qualified
// references in the embedded migrations.
func CheckPrerequisites(ctx context.Context, pool *pgxpool.Pool, forMigration bool) error {
	var writable, schemaExists, schemaUsage, schemaFirst, schemaCreate, plpgsql, ledgerReadable bool
	var ledgerWritable bool
	err := pool.QueryRow(ctx, `SELECT
		NOT pg_catalog.current_setting('transaction_read_only')::boolean,
		n.oid IS NOT NULL,
		COALESCE(pg_catalog.has_schema_privilege(n.oid, 'USAGE'), false),
		pg_catalog.current_schema() IS NOT DISTINCT FROM 'public',
		COALESCE(pg_catalog.has_schema_privilege(n.oid, 'CREATE'), false),
		COALESCE(pg_catalog.has_language_privilege(l.oid, 'USAGE'), false),
		pg_catalog.has_table_privilege(c.oid, 'SELECT') IS NOT FALSE,
		pg_catalog.has_table_privilege(c.oid, 'INSERT') IS NOT FALSE
		FROM (SELECT 1) AS session
		LEFT JOIN pg_catalog.pg_namespace n ON n.nspname = 'public'
		LEFT JOIN pg_catalog.pg_language l ON l.lanname = 'plpgsql'
		LEFT JOIN pg_catalog.pg_class c ON c.relnamespace = n.oid AND c.relname = 'schema_migrations'`).
		Scan(&writable, &schemaExists, &schemaUsage, &schemaFirst, &schemaCreate, &plpgsql, &ledgerReadable, &ledgerWritable)
	switch {
	case err != nil:
		return &PrerequisiteError{"could not check PostgreSQL prerequisites. Verify database_url, TLS, and CONNECT access to a PostgreSQL 17 or newer primary"}
	case !writable:
		return &PrerequisiteError{"PostgreSQL session is read-only. Connect Quivr to a writable primary and check the role's default_transaction_read_only setting"}
	case !schemaExists:
		return &PrerequisiteError{"PostgreSQL public schema is missing. Ask the DBA to provision it in the Quivr database"}
	case !schemaUsage:
		return &PrerequisiteError{"PostgreSQL role needs USAGE on schema public. Ask the DBA to grant it to the Quivr role"}
	case !schemaFirst:
		return &PrerequisiteError{"PostgreSQL search_path must resolve public as the current schema. Set search_path to public for the Quivr role in this database"}
	case forMigration && !schemaCreate:
		return &PrerequisiteError{"PostgreSQL migrations need CREATE on schema public. Ask the DBA to grant it to the Quivr role"}
	case forMigration && !plpgsql:
		return &PrerequisiteError{"PostgreSQL migrations need PL/pgSQL. Ask the DBA to ensure the plpgsql extension is installed and the Quivr role has USAGE on LANGUAGE plpgsql"}
	case !ledgerReadable:
		return &PrerequisiteError{"PostgreSQL role cannot read public.schema_migrations. Check database_url and ask the DBA to verify ownership of the Quivr schema objects"}
	case forMigration && !ledgerWritable:
		return &PrerequisiteError{ledgerInsertDenied}
	}
	return nil
}
