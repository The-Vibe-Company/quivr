-- Operator diagnostics: settings only, never statement text or connection URLs.
SELECT json_build_object(
  'settings', (SELECT json_object_agg(name, current_setting(name)) FROM pg_settings
    WHERE name IN ('max_connections', 'shared_buffers', 'effective_cache_size',
      'work_mem', 'maintenance_work_mem', 'dynamic_shared_memory_type',
      'shared_preload_libraries', 'pg_stat_statements.track', 'track_io_timing', 'jit', 'synchronous_commit',
      'fsync', 'full_page_writes', 'random_page_cost', 'effective_io_concurrency',
      'max_wal_size', 'min_wal_size', 'checkpoint_timeout', 'wal_compression')),
  'pg_stat_statements', EXISTS (SELECT FROM pg_extension WHERE extname='pg_stat_statements'),
  'statistics_preloaded', 'pg_stat_statements' = ANY (
    string_to_array(replace(current_setting('shared_preload_libraries'), ' ', ''), ','))
);
