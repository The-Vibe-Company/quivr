-- Receipt membership survives a lost Temporal start acknowledgement. The
-- original receipt intents are transferred here atomically, never discarded.
-- Old APIs retain this default during a rolling upgrade. New APIs explicitly
-- write non-legacy intents with an infinite outbox lease, so old dispatchers
-- cannot start them under a different workflow identity.
ALTER TABLE ingestion_outbox ADD COLUMN legacy_workflow boolean NOT NULL DEFAULT true;
CREATE TABLE ingestion_batches (
 id text PRIMARY KEY,
 receipts jsonb NOT NULL CHECK (jsonb_typeof(receipts)='array' AND jsonb_array_length(receipts) BETWEEN 1 AND 32),
 enqueued_at timestamptz NOT NULL,
 lease_until timestamptz NOT NULL DEFAULT '-infinity',
 legacy_workflow boolean NOT NULL DEFAULT false,
 CHECK (NOT legacy_workflow OR jsonb_array_length(receipts)=1)
);
CREATE INDEX ingestion_batches_arrival ON ingestion_batches(enqueued_at,id);
