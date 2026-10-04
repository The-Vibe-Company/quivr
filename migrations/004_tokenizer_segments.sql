ALTER TABLE segmentations ADD COLUMN provenance jsonb NOT NULL DEFAULT '{}';
ALTER TABLE segments ADD COLUMN derivation jsonb NOT NULL DEFAULT '{}';
-- Evaluation cutover: restart API/workers and replay into the new tokenizer generation.
UPDATE projection_generations SET active=false;
UPDATE record_versions SET baseline_ready=false,processing='queued',error_code='',quarantined=false
 WHERE NOT quarantined OR error_code='short_text_limit';
UPDATE ingestion_outbox SET dispatched=false,lease_until='-infinity';
