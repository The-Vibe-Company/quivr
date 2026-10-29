-- Keep the counter freshness proof independent of publication and age clocks.
-- Previous writers omit this field; a new refresh establishes a safe cutoff.
ALTER TABLE queue_backlog_snapshots
 ADD COLUMN counters_observed_at timestamptz NOT NULL DEFAULT '-infinity';
