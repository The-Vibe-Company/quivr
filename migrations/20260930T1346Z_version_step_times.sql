-- Version step times (THE-794). When each processing step of a Record Version
-- finished, written once by the transaction that commits the step, next to its
-- record.* change event, so operator views never replay the change journal.
-- Nullable columns without a default change only the catalog: no table is
-- rewritten. Versions accepted before this migration keep null step times and
-- are not listed by GET /v0/admin/documents; history is not replayed.
ALTER TABLE accepted_revisions ADD COLUMN accepted_at timestamptz, ADD COLUMN title text;
ALTER TABLE record_versions ADD COLUMN materialized_at timestamptz, ADD COLUMN segmented_at timestamptz,
  ADD COLUMN retrieval_ready_at timestamptz, ADD COLUMN enriched_at timestamptz,
  ADD COLUMN evaluated_at timestamptz, ADD COLUMN quarantined_at timestamptz;
-- Withdrawal is a Record fact: every Version of the Record reads it.
ALTER TABLE records ADD COLUMN withdrawn_at timestamptz;
-- The indexes cover only revisions accepted from now on: each build scans
-- its table once and sorts no existing row, so the migration's locks stay short.
-- The latest documents of an Organization, newest first, as keyset pages.
CREATE INDEX accepted_revisions_latest ON accepted_revisions(organization, accepted_at DESC, version_id DESC) WHERE accepted_at IS NOT NULL;
-- One document's timeline by Version id, also before the Version is materialized.
CREATE INDEX accepted_revisions_by_version ON accepted_revisions(organization, version_id) WHERE accepted_at IS NOT NULL;
-- The evaluated step checks that every not_ready answer about a Version was followed by a decision.
CREATE INDEX evaluation_intents_not_ready ON evaluation_intents(organization, record_version_id) WHERE outcome = 'not_ready';
