CREATE TABLE vector_spaces (id text PRIMARY KEY, manifest jsonb NOT NULL);
ALTER TABLE projection_generations ADD COLUMN space_id text NOT NULL DEFAULT '';
CREATE TABLE embedding_artifacts (
 organization text NOT NULL, id text NOT NULL, derivation_id text NOT NULL,
 segment_id text NOT NULL, space_id text NOT NULL REFERENCES vector_spaces(id),
 metadata jsonb NOT NULL,
 PRIMARY KEY(organization,id), UNIQUE(organization,derivation_id),
 FOREIGN KEY(organization,segment_id) REFERENCES segments(organization,id)
);
CREATE TABLE embedding_coverage (
 organization text NOT NULL, segment_id text NOT NULL,
 generation_id text NOT NULL REFERENCES projection_generations(id),
 artifact_id text NOT NULL,
 PRIMARY KEY(organization,segment_id,generation_id),
 FOREIGN KEY(organization,artifact_id) REFERENCES embedding_artifacts(organization,id)
);
ALTER TABLE record_versions ADD COLUMN enrichment_state text NOT NULL DEFAULT 'queued';
ALTER TABLE record_versions ADD COLUMN enrichment_error text NOT NULL DEFAULT '';
-- Evaluation cutover to the named-vector schema. Restart API/workers and replay.
UPDATE projection_generations SET active=false;
UPDATE record_versions SET baseline_ready=false,processing='queued',error_code='' WHERE NOT quarantined;
UPDATE ingestion_outbox SET dispatched=false,lease_until='-infinity';
