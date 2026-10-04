-- The outbox is a delivery intent, not an audit log. Receipts and journal
-- events remain durable after dispatch; discard historical acknowledgements.
DELETE FROM ingestion_outbox WHERE dispatched;

ALTER TABLE ingestion_outbox ADD COLUMN enqueued_at timestamptz NOT NULL DEFAULT now();
-- Preserve arrival order for waiting work accepted before this migration.
UPDATE ingestion_outbox o SET enqueued_at=r.accepted_at
FROM ingestion_receipts r WHERE (o.organization,o.receipt_id)=(r.organization,r.id);
-- Use queue insertion time rather than transaction start time for new work.
ALTER TABLE ingestion_outbox ALTER COLUMN enqueued_at SET DEFAULT clock_timestamp();

CREATE INDEX ingestion_outbox_pending_arrival
ON ingestion_outbox(enqueued_at,organization,receipt_id) WHERE NOT dispatched;
