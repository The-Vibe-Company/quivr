-- The structured reason an enrichment stopped with (THE-816). Work pinned to
-- a Pipeline Plan whose ingestion plugin left the active plan and stays
-- unreachable stops its enrichment with a diagnostic naming the plan and the
-- plugin version; enrichment_error keeps its code. Cleared when the
-- enrichment runs again or commits.
ALTER TABLE record_versions ADD COLUMN enrichment_reason jsonb;
