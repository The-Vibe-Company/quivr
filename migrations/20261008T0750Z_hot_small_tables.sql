-- Truncating even a tiny hot table needs ACCESS EXCLUSIVE. Keep its pages
-- reusable without blocking journal writers and renewable queue observations.
ALTER TABLE organization_journals SET (vacuum_truncate = false, fillfactor = 70);
ALTER TABLE queue_document_attempts SET (
 vacuum_truncate = false,
 fillfactor = 70,
 autovacuum_vacuum_threshold = 10,
 autovacuum_vacuum_scale_factor = 0,
 autovacuum_analyze_threshold = 10,
 autovacuum_analyze_scale_factor = 0,
 autovacuum_vacuum_insert_threshold = 50,
 autovacuum_vacuum_insert_scale_factor = 0
);
ALTER TABLE pipeline_plan_work SET (vacuum_truncate = false);
ALTER TABLE connector_instances SET (vacuum_truncate = false);
ALTER TABLE queue_backlog_snapshots SET (vacuum_truncate = false, fillfactor = 70);
ALTER TABLE operations SET (vacuum_truncate = false);

-- Lease renewal changes no primary-key field. Without a timestamp index it
-- can use HOT updates instead of adding entries to a monotonic index.
-- Previous binaries can still run their ordered expired-attempt cleanup.
DROP INDEX queue_document_attempts_active;
