"""What the local evaluation stacks take from the machine (THE-878).

Weaviate turns every shard read-only once the disk under its data passes 90%
(DISK_USE_READONLY_PERCENTAGE) and then refuses every write, so ingestion stalls with
baseline_unavailable and enrichment_unavailable until the run times out. The stack keeps only
3 MB of each service's log, and Weaviate logs every refused write, so the line where it switched
is rotated out within minutes. Watch follows each Weaviate log for the whole run and records the
disk, Docker storage and memory at each step, so a report names the resource that ran short.
"""
import datetime
import json
import os
import re
import subprocess
import sys
import threading

# Weaviate's resource monitor: a warning past 80% disk, the switch to read-only and each shard's status change.
ACTIONS = ('read_disk_use', 'read_memory_use', 'set_shard_read_only', 'update_shard_status')


def resource_lines(lines):
    """Weaviate's resource-monitor entries among its JSON log lines, as {time, action, msg, ...}."""
    out = []
    for line in lines:
        try:
            entry = json.loads(line[line.index('{'):])
        except ValueError:
            continue
        if isinstance(entry, dict) and entry.get('action') in ACTIONS:
            out.append({k: entry[k] for k in ['time', 'action', 'msg', 'shard', 'status', 'reason'] if k in entry})
    return out


def cause(entries):
    """The first switch to read-only Weaviate logged, as a sentence, or None."""
    for e in entries:
        if e['action'] == 'set_shard_read_only':
            return f"Weaviate turned its shards read-only at {e.get('time')}: {e.get('msg')}"
    return None


def short(name):
    """A container or process name without its stack's random part: weaviate-1, base-weaviate-1."""
    return re.sub(r'^quivr-eval-[0-9a-f]+-', '', name)


def output(args):
    return subprocess.run(args, capture_output=True, text=True, timeout=60, check=True).stdout


class Watch:
    """Follows each stack's Weaviate log into weaviate-full.log and snapshots the machine."""

    def __init__(self, stacks):
        self.stacks, self.snapshots = stacks, []
        for stack in stacks:
            # Ends by itself when compose down stops the container.
            log = (stack.directory / 'weaviate-full.log').open('w')
            threading.Thread(target=stack.compose, args=('logs', '--follow', '--no-color', '--no-log-prefix', 'weaviate'),
                             kwargs={'stdout': log, 'stderr': subprocess.STDOUT, 'check': False}, daemon=True).start()

    def snapshot(self, label):
        """Disk, Docker storage and memory now; a probe that fails is recorded, never fatal."""
        from local import docker_disk
        out = {'label': label, 'at': datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='seconds')}
        try:
            disk = docker_disk()
            if disk:
                st = os.statvfs(disk[0])
                out['docker_disk'] = {'path': disk[0], 'used_percent': disk[1], 'free_gb': round(st.f_bavail * st.f_frsize / 1e9, 1)}
            out['docker_storage'] = {e['Type']: e['Size'] for e in map(json.loads, output(['docker', 'system', 'df', '--format', '{{json .}}']).splitlines())}
            meminfo = dict(line.split(':', 1) for line in open('/proc/meminfo'))
            out['memory_available_mb'] = int(meminfo['MemAvailable'].split()[0]) // 1024
            prefixes = tuple(stack.name + '-' for stack in self.stacks)
            stats = map(json.loads, output(['docker', 'stats', '--no-stream', '--format', '{{json .}}']).splitlines())
            out['memory'] = {short(e['Name']): e['MemUsage'].split(' / ')[0] for e in stats if e['Name'].startswith(prefixes)}
            for stack in self.stacks:
                for name in ['api', 'worker']:
                    pid = stack.state.get(name + '_pid')
                    status = dict(line.split(':', 1) for line in open(f'/proc/{pid}/status')) if pid else {}
                    if 'VmRSS' in status:
                        out['memory'][short(f'{stack.name}-{name}')] = f"{int(status['VmRSS'].split()[0]) // 1024}MiB"
        except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
            out['error'] = f'{type(error).__name__}: {error}'[:300]
        self.snapshots.append(out)
        print(f"[eval] resources {label}: Docker disk {out.get('docker_disk', {}).get('used_percent', '?')}% used, "
              f"{out.get('memory_available_mb', '?')} MB memory available", flush=True)

    def summary(self):
        """The snapshots and, per stack, Weaviate's resource lines and the cause of a read-only switch."""
        weaviate = {}
        for stack in self.stacks:
            try:
                weaviate[stack.name] = resource_lines((stack.directory / 'weaviate-full.log').read_text(errors='replace').splitlines())
            except OSError as error:
                print(f'[eval] reading the Weaviate log of {stack.name} failed: {error}', file=sys.stderr, flush=True)
        causes = [c for c in map(cause, weaviate.values()) if c]
        return {'snapshots': self.snapshots, 'weaviate': weaviate, 'cause': causes[0] if causes else None}
