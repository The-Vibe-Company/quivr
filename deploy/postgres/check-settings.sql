-- Deployment smoke contract: explicit 1 GiB memory and 320 GiB volume budgets.
\set ON_ERROR_STOP on
DO $$
BEGIN
    IF current_setting('max_connections') <> '256'
        OR current_setting('shared_buffers') <> '256MB'
        OR current_setting('effective_cache_size') <> '768MB'
        OR current_setting('work_mem') <> '4MB'
        OR current_setting('maintenance_work_mem') <> '64MB'
        OR current_setting('dynamic_shared_memory_type') <> 'mmap'
        OR current_setting('shared_preload_libraries') <> 'pg_stat_statements'
        OR current_setting('track_io_timing') <> 'on'
        OR current_setting('pg_stat_statements.track') <> 'all'
        OR current_setting('random_page_cost') <> '1.1'
        OR current_setting('effective_io_concurrency') <> '200'
        OR current_setting('jit') <> 'on'
        OR current_setting('max_wal_size') <> '32GB'
        OR current_setting('min_wal_size') <> '4GB'
        OR current_setting('checkpoint_timeout') <> '15min'
        OR current_setting('wal_compression') <> 'lz4'
        OR current_setting('synchronous_commit') <> 'on'
        OR current_setting('fsync') <> 'on'
        OR current_setting('full_page_writes') <> 'on' THEN
        RAISE EXCEPTION 'PostgreSQL deployment settings differ from the startup budget contract';
    END IF;
    IF EXISTS (SELECT FROM pg_settings WHERE name IN
        ('max_connections','shared_buffers','effective_cache_size','work_mem',
         'maintenance_work_mem','dynamic_shared_memory_type','shared_preload_libraries',
         'track_io_timing','pg_stat_statements.track','jit','synchronous_commit','fsync','full_page_writes',
         'max_wal_size','min_wal_size','checkpoint_timeout','wal_compression',
         'random_page_cost','effective_io_concurrency')
        AND source <> 'command line') THEN
        RAISE EXCEPTION 'PostgreSQL deployment tuning must override config files';
    END IF;
END $$;
SELECT count(*) FROM pg_stat_statements;
