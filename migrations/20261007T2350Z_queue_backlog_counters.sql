-- Observation time describes the query's cutoff. Publication time throttles
-- the next refresh even when that query takes longer than its interval.
ALTER TABLE queue_backlog_snapshots ADD COLUMN published_at timestamptz NOT NULL DEFAULT '-infinity';
