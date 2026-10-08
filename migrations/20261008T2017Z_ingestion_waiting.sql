-- Nullable until a new refresher publishes the ingestion-only split. Missing
-- data must defer acquisition rather than treating a legacy snapshot as empty.
ALTER TABLE queue_backlog_snapshots ADD COLUMN ingestion_waiting bigint,
 ADD COLUMN ingestion_observed_at timestamptz;
