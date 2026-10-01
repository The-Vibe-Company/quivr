-- Searches over their latency objective (THE-828). A search that takes
-- longer than its profile's max_latency_ms still answers; over_objective
-- counts those searches in the rows of the search series, summed like
-- count. Rows of other series, and rows written before, hold 0.
ALTER TABLE observability_rollups ADD COLUMN over_objective bigint NOT NULL DEFAULT 0;
