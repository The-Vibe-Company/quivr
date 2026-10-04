-- The Pipeline Plan of a normalization stopped because its normalizer left
-- the active plan and stayed unreachable (THE-782): its failure diagnostic
-- names the plan and the plugin version.
ALTER TABLE normalizations ADD COLUMN failure_plan text;
