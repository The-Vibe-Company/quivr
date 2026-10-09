# Read-only cgroup observations, emitted as machine-readable key=value lines.
# An absent/unlimited cgroup is unknown, not the host's resource allocation.
for root in /sys/fs/cgroup /sys/fs/cgroup/memory; do
    relative=$(awk -F: '($1=="0" && $2=="") || $2 ~ /(^|,)memory(,|$)/ {print $3; exit}' /proc/self/cgroup)
    cgroup_directory="$root$relative"
    [ -d "$cgroup_directory" ] || cgroup_directory=$root
    while [ -d "$cgroup_directory" ]; do
        for file in "$cgroup_directory/memory.max" "$cgroup_directory/memory.limit_in_bytes"; do
            [ -r "$file" ] || continue
            awk '/^[0-9]+$/ {if ($1>0 && $1<9e18) printf "__memory=%.0f\n", $1}' "$file"
        done
        if [ -r "$cgroup_directory/cpu.max" ]; then
            awk '$1 ~ /^[0-9]+$/ && $2>0 {print "__cpus=" $1/$2}' "$cgroup_directory/cpu.max"
        fi
        [ "$cgroup_directory" != "$root" ] || break
        cgroup_directory=${cgroup_directory%/*}
    done
done
# cgroup v1 CPU quotas can use a separate or combined mount and nested groups.
relative=$(awk -F: '$2 ~ /(^|,)cpu(,|$)/ {print $3; exit}' /proc/self/cgroup)
for root in /sys/fs/cgroup/cpu /sys/fs/cgroup/cpu,cpuacct; do
    cgroup_directory="$root$relative"
    [ -d "$cgroup_directory" ] || cgroup_directory=$root
    while [ -d "$cgroup_directory" ]; do
        if [ -r "$cgroup_directory/cpu.cfs_quota_us" ] && [ -r "$cgroup_directory/cpu.cfs_period_us" ]; then
            awk 'NR==1 {quota=$1} NR==2 && quota>0 && $1>0 {print "__cpus=" quota/$1}' \
                "$cgroup_directory/cpu.cfs_quota_us" "$cgroup_directory/cpu.cfs_period_us"
        fi
        [ "$cgroup_directory" != "$root" ] || break
        cgroup_directory=${cgroup_directory%/*}
    done
done

# Filesystem capacity is observed separately from allocated-volume quota.
if [ -n "${directory:-}" ] && [ -d "$directory" ]; then
    df -Pk "$directory" | awk 'NR==2 {printf "__storage=%.0f\n", $2*1024}'
fi
