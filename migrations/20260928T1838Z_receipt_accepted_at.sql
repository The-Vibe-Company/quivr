-- receipt_accepted_at (THE-662)
-- When the API durably accepted each Receipt. It feeds the ingestion backlog
-- gauges (count and oldest age of pending Receipts) and the
-- acceptance-to-searchable duration. Receipts accepted before this migration
-- carry the migration time.
ALTER TABLE ingestion_receipts ADD COLUMN accepted_at timestamptz NOT NULL DEFAULT now();
-- Only pending Receipts are scanned by the backlog gauges.
CREATE INDEX ingestion_receipts_pending_accepted ON ingestion_receipts (accepted_at) WHERE state = 'pending';
