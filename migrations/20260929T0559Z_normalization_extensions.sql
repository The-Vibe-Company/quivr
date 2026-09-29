-- normalization_extensions (THE-684)
-- Top-level extensions a normalizer produced in the plugin's own declared
-- namespaces, validated before they are recorded. Publication merges them into
-- the Version's extensions beside the submitted ones; Part extensions stay in
-- the recorded Manifest object. Earlier outputs produced none.
ALTER TABLE normalizations ADD COLUMN extensions jsonb NOT NULL DEFAULT '{}'::jsonb;
