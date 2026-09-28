-- Typed Corpus retrieval mappings (THE-660). A logical generation pins the
-- resolved retrieval configuration it was built with and a per-Corpus version.
-- The effective configuration is the routed generation's pin, so configuration
-- and generation activation are one atomic route switch. NULL (the shared
-- default generation and earlier rebuild targets) means the Corpus creation
-- configuration, version 1. No replay or reindex is required.
ALTER TABLE projection_generations ADD COLUMN retrieval jsonb;
ALTER TABLE projection_generations ADD COLUMN retrieval_version integer NOT NULL DEFAULT 1;
ALTER TABLE operations DROP CONSTRAINT operations_kind_check;
ALTER TABLE operations ADD CONSTRAINT operations_kind_check CHECK(kind IN ('projection_rebuild','retrieval_configuration'));
-- Creation configurations stored before this migration were never applied to
-- any projection and were only loosely validated. Record what those Corpora
-- actually serve (no mappings) so ingestion cannot start applying them without
-- a validated rebuild; owners reapply mappings through the configuration route.
UPDATE corpora SET retrieval='{"fields":[]}';
