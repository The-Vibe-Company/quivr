-- Explicit structured Manifests, Part extensions and Version-level extensions.
-- Version extensions are immutable source data beside the Manifest; relations
-- stay inside the Manifest blob and are resolved, never mutated, at read time.
ALTER TABLE record_versions ADD COLUMN extensions jsonb NOT NULL DEFAULT '{}';
