-- quivr:contract
-- quivr:expand 20261007T0550Z_work_queues.sql
-- In-place upgrades from 2.0.0-alpha.6 and older are not supported, so the
-- state that only drained or repaired their work goes. Dispatch writers always
-- name a work queue; no row is the empty, legacy class.
ALTER TABLE ingestion_outbox DROP COLUMN legacy_workflow, DROP COLUMN lease_until, ALTER COLUMN work_queue DROP DEFAULT;
ALTER TABLE bulk_ingestion_outbox DROP COLUMN legacy_workflow, DROP COLUMN lease_until;
ALTER TABLE ingestion_batches DROP COLUMN legacy_workflow, ALTER COLUMN work_queue DROP DEFAULT;
ALTER TABLE bulk_ingestion_batches DROP COLUMN legacy_workflow;
ALTER TABLE ingestion_evaluations ALTER COLUMN work_queue DROP DEFAULT;
ALTER TABLE serving_projections ALTER COLUMN work_queue DROP DEFAULT;
DROP TABLE queue_observation_journals;
DROP TABLE projection_purge_bootstrap;
