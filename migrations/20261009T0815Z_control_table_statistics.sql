-- These control tables may never reach PostgreSQL's default analyze threshold.
-- Analyze existing rows now, and make the first subsequent change eligible for
-- autoanalyze so even a tiny installation gets useful planner statistics.
ALTER TABLE corpora SET (autovacuum_analyze_threshold = 0, autovacuum_analyze_scale_factor = 0);
ALTER TABLE tombstones SET (autovacuum_analyze_threshold = 0, autovacuum_analyze_scale_factor = 0);
ALTER TABLE corpus_projection_routes SET (autovacuum_analyze_threshold = 0, autovacuum_analyze_scale_factor = 0);
ALTER TABLE projection_generations SET (autovacuum_analyze_threshold = 0, autovacuum_analyze_scale_factor = 0);
ALTER TABLE storage_organizations SET (autovacuum_analyze_threshold = 0, autovacuum_analyze_scale_factor = 0);
ALTER TABLE storage_spaces SET (autovacuum_analyze_threshold = 0, autovacuum_analyze_scale_factor = 0);

ANALYZE corpora, tombstones, corpus_projection_routes, projection_generations, storage_organizations, storage_spaces;
