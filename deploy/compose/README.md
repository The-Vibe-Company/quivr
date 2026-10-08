# Size PostgreSQL for imports

The [local stack](../../README.md#quickstart) and Railway template use the same
[PostgreSQL startup script](../postgres/start.sh). It applies settings on every
boot, preserving the official image's initialization and persistent volume.

## Choose connection and memory budgets

Set the engine's `postgres.max_connections` in its JSON configuration per process.
A positive value overrides the database URL's `pool_max_conns`; omitted/zero keeps
URL/driver sizing. Usable server connections must be at least the **sum of pool
sizes across all replicas**, plus other clients and rolling-deployment headroom.
Subtract PostgreSQL's reserved connections from `max_connections`. One API, one
live worker and eight bulk workers with pools of 16 can use 160 connections.
Worker activity slots do not set the pool size.

The server defaults to 256 connections. Memory comes from the tightest detected
cgroup/host limit, or your explicit `QUIVR_POSTGRES_MEMORY_MB` budget (MiB, at least
128). Set a container memory limit or an explicit budget before importing; without
a finite cgroup limit the script uses host memory, which other services also share.

| Server variable | Default |
| --- | --- |
| `QUIVR_POSTGRES_MAX_CONNECTIONS` | 256 |
| `QUIVR_POSTGRES_SHARED_BUFFERS` | 25% of memory; 12.5% below 1 GiB |
| `QUIVR_POSTGRES_EFFECTIVE_CACHE_SIZE` | 75% of memory, a planner estimate, not an allocation |
| `QUIVR_POSTGRES_WORK_MEM` | `4MB` per sort/hash operation; a hash can use more |
| `QUIVR_POSTGRES_MAINTENANCE_WORK_MEM` | 1/16 of memory, bounded to 16–512 MiB |

Memory overrides require positive integers with `kB`, `MB`, `GB` or `TB` units.
Leave room for concurrent operations, connection overhead, autovacuum workers
(each can use maintenance memory) and the OS cache. These are starting ratios,
not a guarantee that all 256 sessions fit a small allocation. Change budgets and
restart PostgreSQL when scaling. See [PostgreSQL memory settings](https://www.postgresql.org/docs/17/runtime-config-resource.html).

## Verify startup

The script sets `dynamic_shared_memory_type=mmap`, preloads `pg_stat_statements`,
enables I/O timing and retains JIT. Fresh databases install the statistics extension.
Command-line tuning overrides existing `ALTER SYSTEM` values in `postgresql.auto.conf`.
`synchronous_commit`, `fsync` and `full_page_writes` stay on: a faster asynchronous
commit can lose acknowledged writes after a crash, so it is not the template default.

From a SQL connection, inspect actual values and their source:

```sql
SELECT name, setting, unit, source FROM pg_settings
WHERE name IN ('max_connections', 'shared_buffers', 'effective_cache_size',
              'work_mem', 'maintenance_work_mem', 'dynamic_shared_memory_type',
              'shared_preload_libraries', 'track_io_timing', 'jit',
              'synchronous_commit', 'fsync', 'full_page_writes');
```

The source should be `command line`. Existing databases need
`CREATE EXTENSION IF NOT EXISTS pg_stat_statements` once to expose statistics views.
