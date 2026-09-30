-- Observability rollups (THE-795). Each process counts plugin calls,
-- searches and processing steps in memory and adds them here every few
-- seconds, one row per Organization, series, key and time bucket. Every
-- event is written at three resolutions (1 minute kept 2 hours, 15 minutes
-- kept 25 hours, 2 hours kept 7 days) so a read of any window touches about
-- a hundred buckets per key; query text, when the deployment records it, is
-- counted per hour only. buckets holds the counts of the fixed latency
-- buckets of internal/observability, overflow last. The worker deletes rows
-- past their resolution's retention; nothing is kept beyond 7 days.
CREATE TABLE observability_rollups (
  organization text NOT NULL,
  series text NOT NULL,
  resolution_s integer NOT NULL CHECK (resolution_s > 0),
  bucket_start timestamptz NOT NULL,
  key text NOT NULL,
  count bigint NOT NULL DEFAULT 0,
  errors bigint NOT NULL DEFAULT 0,
  items_sum bigint NOT NULL DEFAULT 0,
  duration_sum_ms double precision NOT NULL DEFAULT 0,
  buckets bigint[] NOT NULL,
  last_error_code text,
  last_error_at timestamptz,
  PRIMARY KEY (organization, series, resolution_s, bucket_start, key)
);

CREATE INDEX observability_rollups_expiry ON observability_rollups (resolution_s, bucket_start);
