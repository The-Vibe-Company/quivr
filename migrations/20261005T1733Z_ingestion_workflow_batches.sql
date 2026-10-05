-- Receipt membership survives a lost Temporal start acknowledgement. The
-- original receipt intents are transferred here atomically, never discarded.
CREATE TABLE ingestion_batches (
 id text PRIMARY KEY,
 receipts jsonb NOT NULL CHECK (jsonb_typeof(receipts)='array' AND jsonb_array_length(receipts) BETWEEN 1 AND 32),
 enqueued_at timestamptz NOT NULL,
 lease_until timestamptz NOT NULL DEFAULT '-infinity'
);
CREATE INDEX ingestion_batches_arrival ON ingestion_batches(enqueued_at,id);
