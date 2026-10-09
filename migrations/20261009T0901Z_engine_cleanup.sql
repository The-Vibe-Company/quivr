-- Purge progress advances by scanned rows, including rows that remain live.
ALTER TABLE projection_purges ADD COLUMN cleanup_stage integer NOT NULL DEFAULT 0;
ALTER TABLE projection_purges ADD COLUMN cleanup_cursor jsonb NOT NULL DEFAULT '[]';

-- An exact input stays completed even when a concurrent provider returns late.
CREATE TABLE ingestion_page_completions (
 organization text NOT NULL,
 version_id text NOT NULL,
 recipe text NOT NULL,
 spaces_key text NOT NULL,
 PRIMARY KEY(organization,version_id,recipe,spaces_key)
);
CREATE TABLE ingestion_page_sweep (
 kind text PRIMARY KEY,
 organization text NOT NULL DEFAULT '',
 version_id text NOT NULL DEFAULT '',
 recipe text NOT NULL DEFAULT '',
 spaces_key text NOT NULL DEFAULT '',
 page_number integer NOT NULL DEFAULT -1
);
