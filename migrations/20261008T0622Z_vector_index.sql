-- How the search index stores each registered space's vectors, set from the
-- deployment configuration at startup and copied into each new generation's
-- spaces. NULL on rows registered before index settings, whose generations
-- keep their existing named vectors.
ALTER TABLE vector_spaces ADD COLUMN vector_index jsonb;
