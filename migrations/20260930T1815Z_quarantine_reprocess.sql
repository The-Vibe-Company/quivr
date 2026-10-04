-- Reprocess Versions stuck in quarantine (THE-785, Spec 5 slice 6).
--
-- The step a quarantined Version failed at: 'normalization' when it was
-- published quarantined because its normalizer failed or its route was
-- removed, 'ingestion' when its baseline (segmentation through the ingestion
-- plugin) was refused or stopped. A reprocess reruns that step. Existing
-- quarantines get it from their failed normalization record or code.
ALTER TABLE record_versions ADD COLUMN quarantine_stage text CHECK (quarantine_stage IN ('normalization','ingestion'));
UPDATE record_versions v SET quarantine_stage=CASE
  WHEN v.error_code IN ('normalizer_unrouted','normalization_superseded')
    OR EXISTS(SELECT 1 FROM normalizations n WHERE n.organization=v.organization AND n.version_id=v.id AND n.outcome='failed')
  THEN 'normalization' ELSE 'ingestion' END
WHERE v.quarantined;
-- The quarantined Versions of an Organization, for the operator listing.
CREATE INDEX record_versions_quarantined ON record_versions(organization,id) WHERE quarantined;

ALTER TABLE operations DROP CONSTRAINT operations_kind_check;
ALTER TABLE operations ADD CONSTRAINT operations_kind_check CHECK(kind IN ('projection_rebuild','retrieval_configuration','backfill','quarantine_reprocess'));

-- A dry run records its count under the request's key; a reprocess is
-- accepted only with the key and scope of a recorded dry run.
CREATE TABLE quarantine_reprocess_estimates (
 organization text NOT NULL,
 corpus_id text NOT NULL,
 request_key text NOT NULL,
 canonical_request bytea NOT NULL,
 estimate jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (organization, corpus_id, request_key),
 FOREIGN KEY (organization, corpus_id) REFERENCES corpora(organization, id)
);

-- What a reprocess Operation covers: its filters, the Pipeline Plan it is
-- pinned to and the dry run it was accepted after.
CREATE TABLE quarantine_reprocesses (
 organization text NOT NULL,
 operation_id text NOT NULL,
 plugin text NOT NULL DEFAULT '',
 code text NOT NULL DEFAULT '',
 quarantined_after timestamptz,
 quarantined_before timestamptz,
 plan_id text NOT NULL REFERENCES pipeline_plans(id),
 estimate jsonb NOT NULL,
 PRIMARY KEY (organization, operation_id),
 FOREIGN KEY (organization, operation_id) REFERENCES operations(organization, id)
);

-- The Versions a reprocess took at acceptance, and how far each got:
-- pending, then renormalizing (normalization stage) or released, then one
-- of the outcomes recovered, quarantined (failed again) or skipped. previous
-- is the reason the Version was quarantined with before.
CREATE TABLE quarantine_reprocess_items (
 organization text NOT NULL,
 operation_id text NOT NULL,
 version_id text NOT NULL,
 record_id text NOT NULL,
 receipt_id text NOT NULL,
 stage text NOT NULL CHECK (stage IN ('normalization','ingestion')),
 previous jsonb NOT NULL,
 phase text NOT NULL DEFAULT 'pending' CHECK (phase IN ('pending','renormalizing','released','recovered','quarantined','skipped')),
 outcome_code text NOT NULL DEFAULT '',
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (organization, operation_id, version_id),
 FOREIGN KEY (organization, operation_id) REFERENCES operations(organization, id)
);
-- Each step reads the next pending item and the started one without walking
-- past the items already finished.
CREATE INDEX quarantine_reprocess_items_pending ON quarantine_reprocess_items(organization,operation_id,version_id) WHERE phase='pending';
CREATE INDEX quarantine_reprocess_items_started ON quarantine_reprocess_items(organization,operation_id,version_id) WHERE phase IN ('renormalizing','released');
