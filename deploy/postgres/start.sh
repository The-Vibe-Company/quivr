#!/bin/sh
# Shared startup for the pinned official PostgreSQL image on any Linux host.
set -eu
if [ "${1:-}" = postgres ]; then shift; fi
case "${1:-}" in
    --help|--version|-V|--describe-config) exec docker-entrypoint.sh postgres "$@" ;;
    ''|-*) ;;
    *) exec docker-entrypoint.sh "$@" ;;
esac

fail() { echo "Invalid PostgreSQL startup setting: $1" >&2; exit 1; }
positive() {
    case "$2" in ''|*[!0-9]*) fail "$1" ;; esac
    [ "$2" -gt 0 ] 2>/dev/null || fail "$1"
}
memory_mb=${QUIVR_POSTGRES_MEMORY_MB:-}
if [ -z "$memory_mb" ]; then
    # Limits inherited from parents matter too. In a cgroup namespace the
    # container's group is normally mounted at the root of /sys/fs/cgroup.
    memory_mb=$(awk '/^MemTotal:/ {print int($2/1024)}' /proc/meminfo)
    for base in /sys/fs/cgroup /sys/fs/cgroup/memory; do
        relative=$(awk -F: '($1=="0" && $2=="") || $2 ~ /(^|,)memory(,|$)/ {print $3; exit}' /proc/self/cgroup)
        directory="$base$relative"
        [ -d "$directory" ] || directory=$base
        while [ -d "$directory" ]; do
            for file in "$directory/memory.max" "$directory/memory.limit_in_bytes"; do
                [ -r "$file" ] || continue
                limit=$(awk '/^[0-9]+$/ {if ($1>0 && $1<9e18) print int($1/1048576)}' "$file")
                if [ -n "$limit" ] && [ "$limit" -lt "$memory_mb" ]; then memory_mb=$limit; fi
            done
            [ "$directory" != "$base" ] || break
            directory=${directory%/*}
        done
    done
fi
# Shell arithmetic treats a leading zero as octal; operator budgets are decimal.
memory_mb=$(printf '%s\n' "$memory_mb" | sed 's/^0*//')
positive QUIVR_POSTGRES_MEMORY_MB "$memory_mb"
[ "$memory_mb" -ge 128 ] || fail QUIVR_POSTGRES_MEMORY_MB
buffers=$((memory_mb / 4))
[ "$memory_mb" -ge 1024 ] || buffers=$((memory_mb / 8))
cache=$((memory_mb * 3 / 4))
maintenance=$((memory_mb / 16))
[ "$maintenance" -ge 16 ] || maintenance=16
[ "$maintenance" -le 512 ] || maintenance=512
connections=${QUIVR_POSTGRES_MAX_CONNECTIONS:-256}
connections=$(printf '%s\n' "$connections" | sed 's/^0*//')
positive QUIVR_POSTGRES_MAX_CONNECTIONS "$connections"
[ "$connections" -le 262143 ] || fail QUIVR_POSTGRES_MAX_CONNECTIONS
for value in "${QUIVR_POSTGRES_SHARED_BUFFERS:-${buffers}MB}" \
    "${QUIVR_POSTGRES_EFFECTIVE_CACHE_SIZE:-${cache}MB}" \
    "${QUIVR_POSTGRES_WORK_MEM:-4MB}" \
    "${QUIVR_POSTGRES_MAINTENANCE_WORK_MEM:-${maintenance}MB}"; do
    awk -v value="$value" 'BEGIN {exit !(value ~ /^[1-9][0-9]*(kB|MB|GB|TB)$/)}' || fail memory-settings
done

# Filesystem capacity is stable across restarts; free space is not. Quotas on a
# shared filesystem need an explicit volume budget. Fresh PGDATA may not exist.
volume_mb=${QUIVR_POSTGRES_VOLUME_MB:-}
if [ -z "$volume_mb" ]; then
    directory=${PGDATA:-/var/lib/postgresql/data}
    while [ ! -d "$directory" ]; do directory=$(dirname "$directory"); done
    volume_mb=$(df -Pm "$directory" | awk 'NR==2 {print $2}')
fi
volume_mb=$(printf '%s\n' "$volume_mb" | sed 's/^0*//')
positive QUIVR_POSTGRES_VOLUME_MB "$volume_mb"
wal=$((volume_mb / 10))
[ "$wal" -ge 64 ] || wal=64
[ "$wal" -le 32768 ] || wal=32768
max_wal=${QUIVR_POSTGRES_MAX_WAL_SIZE:-${wal}MB}
wal_mb() {
    awk -v value="$1" 'BEGIN {
        if (value !~ /^[1-9][0-9]*(kB|MB|GB|TB)$/) exit 1
        unit=value; sub(/^[0-9]+/, "", unit)
        factor=(unit=="kB" ? 1/1024 : unit=="MB" ? 1 : unit=="GB" ? 1024 : 1048576)
        printf "%.0f\n", int(value * factor)
    }'
}
max_wal_mb=$(wal_mb "$max_wal") || fail QUIVR_POSTGRES_MAX_WAL_SIZE
[ "$max_wal_mb" -ge 32 ] && [ "$max_wal_mb" -le 2147483647 ] || fail QUIVR_POSTGRES_MAX_WAL_SIZE
wal=$((max_wal_mb / 8))
[ "$wal" -ge 32 ] || wal=32
[ "$wal" -le 4096 ] || wal=4096
min_wal=${QUIVR_POSTGRES_MIN_WAL_SIZE:-${wal}MB}
min_wal_mb=$(wal_mb "$min_wal") || fail QUIVR_POSTGRES_MIN_WAL_SIZE
[ "$min_wal_mb" -ge 32 ] && [ "$min_wal_mb" -le "$max_wal_mb" ] || fail QUIVR_POSTGRES_MIN_WAL_SIZE

# Values stay separate argv entries; never evaluate operator input as shell.
set -- postgres \
    -c "max_connections=$connections" \
    -c "shared_buffers=${QUIVR_POSTGRES_SHARED_BUFFERS:-${buffers}MB}" \
    -c "effective_cache_size=${QUIVR_POSTGRES_EFFECTIVE_CACHE_SIZE:-${cache}MB}" \
    -c "work_mem=${QUIVR_POSTGRES_WORK_MEM:-4MB}" \
    -c "maintenance_work_mem=${QUIVR_POSTGRES_MAINTENANCE_WORK_MEM:-${maintenance}MB}" \
    -c dynamic_shared_memory_type=mmap \
    -c shared_preload_libraries=pg_stat_statements \
    -c track_io_timing=on \
    -c "max_wal_size=$max_wal" -c "min_wal_size=$min_wal" \
    -c checkpoint_timeout=15min -c wal_compression=lz4 \
    -c jit=on \
    -c synchronous_commit=on -c fsync=on -c full_page_writes=on \
    "$@"
exec docker-entrypoint.sh "$@"
