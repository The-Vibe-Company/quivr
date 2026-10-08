-- New plans can retain an existing derivation through execution-only tuning.
-- NULL on historical plans preserves their original full-configuration recipe.
ALTER TABLE pipeline_plan_roles
 ADD COLUMN ingestion_recipe text,
 ADD COLUMN ingestion_provenance jsonb;
