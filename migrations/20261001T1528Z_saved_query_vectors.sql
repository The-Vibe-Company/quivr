ALTER TABLE saved_query_versions ADD COLUMN query_vectors jsonb NOT NULL DEFAULT '[]'::jsonb;
