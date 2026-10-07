-- Workload classification is snapshotted at durable acceptance. Empty dispatch
-- classes retain pre-upgrade Temporal routing and are drained by legacy pollers.
ALTER TABLE connector_instances ADD COLUMN work_queue text NOT NULL DEFAULT 'live';
ALTER TABLE ingestion_receipts ADD COLUMN work_queue text NOT NULL DEFAULT 'live';
ALTER TABLE ingestion_outbox ADD COLUMN work_queue text NOT NULL DEFAULT '';
ALTER TABLE ingestion_batches ADD COLUMN work_queue text NOT NULL DEFAULT '';
ALTER TABLE ingestion_evaluations ADD COLUMN work_queue text NOT NULL DEFAULT '';
ALTER TABLE serving_projections ADD COLUMN work_queue text NOT NULL DEFAULT '';

-- Renewable execution observations; expired rows never count as active work.
-- A global fencing token stays unique when completed/expired rows are deleted.
CREATE SEQUENCE queue_document_attempt_tokens;
CREATE TABLE queue_document_attempts (
 organization text NOT NULL, kind text NOT NULL, work_id text NOT NULL,
 document_id text NOT NULL, token bigint NOT NULL, lease_until timestamptz NOT NULL,
 PRIMARY KEY (organization,kind,work_id,document_id)
);
CREATE INDEX queue_document_attempts_active ON queue_document_attempts(lease_until);

-- One installation-wide observation, independent of operation scope size and
-- number of worker/API replicas. Refreshers elect themselves using a database
-- advisory lock; consumers reject missing or stale observations.
CREATE TABLE queue_backlog_snapshots (
 queue text PRIMARY KEY,
 waiting bigint NOT NULL,
 in_progress bigint NOT NULL,
 oldest_waiting_age_seconds double precision NOT NULL,
 observed_at timestamptz NOT NULL
);
