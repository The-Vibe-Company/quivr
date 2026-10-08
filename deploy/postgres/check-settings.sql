-- Deployment smoke contract: a fresh server with an explicit 1 GiB budget.
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
        OR current_setting('jit') <> 'on'
        OR current_setting('synchronous_commit') <> 'on'
        OR current_setting('fsync') <> 'on'
        OR current_setting('full_page_writes') <> 'on' THEN
        RAISE EXCEPTION 'PostgreSQL deployment settings differ from the 1 GiB startup contract';
    END IF;
    IF EXISTS (SELECT FROM pg_settings WHERE name IN
        ('max_connections','shared_buffers','effective_cache_size','work_mem',
         'maintenance_work_mem','dynamic_shared_memory_type','shared_preload_libraries',
         'track_io_timing','jit','synchronous_commit','fsync','full_page_writes')
        AND source <> 'command line') THEN
        RAISE EXCEPTION 'PostgreSQL deployment tuning must override config files';
    END IF;
END $$;
SELECT count(*) FROM pg_stat_statements;
