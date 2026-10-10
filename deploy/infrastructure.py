#!/usr/bin/env python3
"""Resolve portable infrastructure intent; never contains credentials or live selectors."""
import argparse
import copy
import json
import math
import os
from pathlib import Path
import re
import sys
import shlex
from decimal import Decimal

ROOT = Path(__file__).resolve().parents[1]
DECLARATION = Path(__file__).with_suffix('.json')
POSTGRES_KEYS = {
    'QUIVR_POSTGRES_MEMORY_MB', 'QUIVR_POSTGRES_VOLUME_MB',
    'QUIVR_POSTGRES_MAX_CONNECTIONS', 'QUIVR_POSTGRES_SHARED_BUFFERS',
    'QUIVR_POSTGRES_EFFECTIVE_CACHE_SIZE', 'QUIVR_POSTGRES_WORK_MEM',
    'QUIVR_POSTGRES_MAINTENANCE_WORK_MEM', 'QUIVR_POSTGRES_MAX_WAL_SIZE',
    'QUIVR_POSTGRES_MIN_WAL_SIZE', 'QUIVR_POSTGRES_RANDOM_PAGE_COST',
    'QUIVR_POSTGRES_EFFECTIVE_IO_CONCURRENCY', 'QUIVR_POSTGRES_CHECKPOINT_TIMEOUT',
    'QUIVR_POSTGRES_WAL_COMPRESSION', 'QUIVR_POSTGRES_STAT_STATEMENTS_TRACK',
    'QUIVR_POSTGRES_JIT', 'QUIVR_POSTGRES_SYNCHRONOUS_COMMIT',
}
WEAVIATE_KEYS = {
    'AUTHENTICATION_ANONYMOUS_ACCESS_ENABLED', 'AUTOSCHEMA_ENABLED',
    'DEFAULT_VECTORIZER_MODULE', 'PERSISTENCE_DATA_PATH', 'DEFAULT_QUANTIZATION',
    'ASYNC_INDEXING', 'PERSISTENCE_MEMTABLES_MAX_SIZE_MB', 'RAFT_BOOTSTRAP_TIMEOUT', 'GOMEMLIMIT',
}
AUTOSCALER_KEYS = {
    'QUIVR_AUTOSCALER_BACKEND', 'QUIVR_AUTOSCALER_QUEUE', 'QUIVR_AUTOSCALER_MIN', 'QUIVR_AUTOSCALER_MAX',
    'QUIVR_AUTOSCALER_DOCUMENTS_PER_REPLICA', 'QUIVR_AUTOSCALER_INTERVAL',
    'QUIVR_AUTOSCALER_DOWNSCALE_WINDOW', 'QUIVR_AUTOSCALER_MIN_SCALE_INTERVAL',
    'QUIVR_AUTOSCALER_REQUEST_TIMEOUT',
}
KEYS = {'postgres': POSTGRES_KEYS, 'weaviate': WEAVIATE_KEYS, 'autoscaler': AUTOSCALER_KEYS,
        'api': set(), 'worker': set(), 'worker-bulk': set()}


def merge(original, override):
    result = copy.deepcopy(original)
    for key, value in override.items():
        if isinstance(value, dict) and isinstance(result.get(key), dict):
            result[key] = merge(result[key], value)
        else:
            result[key] = copy.deepcopy(value)
    return result


def validate(services, resolved=False):
    if not isinstance(services, dict) or set(services) - KEYS.keys():
        raise ValueError('unsupported infrastructure service')
    for name, service in services.items():
        if not isinstance(service, dict) or set(service) - {'image', 'environment', 'deploy', 'x-quivr-storage-budget-bytes'}:
            raise ValueError('unsupported infrastructure field')
        image = service.get('image')
        if 'image' in service and (not isinstance(image, str) or
                                  not re.fullmatch(r'[a-zA-Z0-9./:_-]+@sha256:[0-9a-f]{64}', image)):
            raise ValueError('infrastructure images must have an immutable digest')
        storage = service.get('x-quivr-storage-budget-bytes')
        if 'x-quivr-storage-budget-bytes' in service and (name not in ('postgres', 'weaviate') or not isinstance(storage, str)
                                    or not storage.isdigit() or int(storage) < 1048576):
            raise ValueError('invalid storage budget')
        environment = service.get('environment', {})
        if not isinstance(environment, dict) or set(environment) - KEYS[name]:
            raise ValueError('unsupported infrastructure setting')
        for value in environment.values():
            if not isinstance(value, str) or not re.fullmatch(r'[a-zA-Z0-9./_-]+', value):
                raise ValueError('invalid infrastructure value')
        for key, value in environment.items():
            if safe_environment(key, value) == '<invalid>':
                raise ValueError('invalid infrastructure value')
        if name == 'postgres' and environment.get('QUIVR_POSTGRES_MIN_WAL_SIZE'):
            maximum = environment.get('QUIVR_POSTGRES_MAX_WAL_SIZE')
            if maximum or resolved:
                capacity = int(environment.get('QUIVR_POSTGRES_VOLUME_MB', 327680))
                maximum_mb = memory_bytes(maximum) // 1048576 if maximum else min(32768, max(64, capacity // 10))
                if memory_bytes(environment['QUIVR_POSTGRES_MIN_WAL_SIZE']) // 1048576 > maximum_mb:
                    raise ValueError('invalid infrastructure value')
        deployment = service.get('deploy', {})
        if set(deployment) - {'resources'} or set(deployment.get('resources', {})) - {'limits'}:
            raise ValueError('unsupported infrastructure resource field')
        limits = deployment.get('resources', {}).get('limits', {})
        if set(limits) - {'cpus', 'memory'}:
            raise ValueError('unsupported infrastructure resource field')
        try:
            if 'memory' in limits and (not str(limits['memory']).isdigit() or int(limits['memory']) <= 0):
                raise ValueError()
            if 'cpus' in limits and (not math.isfinite(float(limits['cpus'])) or float(limits['cpus']) <= 0):
                raise ValueError()
        except (ValueError, TypeError):
            raise ValueError('invalid infrastructure resource budget') from None


def resolve(profile='small', overrides=None, environ=None, declaration_path=DECLARATION):
    declaration = json.loads(Path(declaration_path).read_text())
    profiles = declaration['x-quivr']['profiles']
    if profile not in profiles:
        raise ValueError('unknown infrastructure profile')
    for name in KEYS.keys() - {'weaviate'}:
        if 'image' in profiles[profile].get(name, {}):
            raise ValueError('per-installation image overrides are supported only for Weaviate; edit build pins and generate')
    services = merge(declaration['services'], {'autoscaler': declaration['x-quivr']['autoscaler']})
    services = merge(services, declaration['x-quivr'].get('services', {}))
    services = merge(services, profiles[profile])
    if environ is not None:
        for name, service in services.items():
            service.setdefault('environment', {}).update({key: environ[key] for key in KEYS[name]
                                                         if environ.get(key)})
    if overrides:
        override = json.loads(Path(overrides).read_text())
        if not isinstance(override, dict) or set(override) != {'services'}:
            raise ValueError('overrides require only a services object')
        validate(override['services'])
        for name in KEYS.keys() - {'weaviate'}:
            if 'image' in override['services'].get(name, {}):
                raise ValueError('per-installation image overrides are supported only for Weaviate; edit build pins and generate')
        services = merge(services, override['services'])
    storage = services['postgres'].get('x-quivr-storage-budget-bytes')
    if storage:
        services['postgres']['environment'].setdefault('QUIVR_POSTGRES_VOLUME_MB', str(int(storage) // 1048576))
    validate(services, resolved=True)
    return services


def compose_model(services):
    return {'services': {name: service for name, service in services.items() if name in ('postgres', 'weaviate')}}


def memory_bytes(value):
    match = re.fullmatch(r'([0-9]+)(kB|MB|GB|TB|KiB|MiB|GiB|TiB)?', str(value))
    if not match:
        raise ValueError('invalid memory size')
    factors = {'kB': 1024, 'MB': 1024**2, 'GB': 1024**3, 'TB': 1024**4,
               'KiB': 1024, 'MiB': 1024**2, 'GiB': 1024**3, 'TiB': 1024**4, None: 1}
    return int(match[1]) * factors[match[2]]


def postgres_expected(service, volume_mb=None):
    env = service['environment']
    memory = int(env.get('QUIVR_POSTGRES_MEMORY_MB',
                         int(service['deploy']['resources']['limits']['memory']) // 1048576))
    value = {
        'max_connections': env['QUIVR_POSTGRES_MAX_CONNECTIONS'],
        'shared_buffers': str(memory // (4 if memory >= 1024 else 8)) + 'MB',
        'effective_cache_size': str(memory * 3 // 4) + 'MB',
        'work_mem': env['QUIVR_POSTGRES_WORK_MEM'],
        'maintenance_work_mem': str(min(512, max(16, memory // 16))) + 'MB',
        'dynamic_shared_memory_type': 'mmap', 'shared_preload_libraries': 'pg_stat_statements',
        'track_io_timing': 'on', 'jit': env['QUIVR_POSTGRES_JIT'],
        'synchronous_commit': env['QUIVR_POSTGRES_SYNCHRONOUS_COMMIT'],
        'fsync': 'on', 'full_page_writes': 'on',
        'pg_stat_statements.track': env['QUIVR_POSTGRES_STAT_STATEMENTS_TRACK'],
    }
    for key in ('SHARED_BUFFERS', 'EFFECTIVE_CACHE_SIZE', 'WORK_MEM', 'MAINTENANCE_WORK_MEM',
                'RANDOM_PAGE_COST', 'EFFECTIVE_IO_CONCURRENCY', 'CHECKPOINT_TIMEOUT', 'WAL_COMPRESSION'):
        if env.get('QUIVR_POSTGRES_' + key):
            value[key.lower()] = env['QUIVR_POSTGRES_' + key]
    budget = env.get('QUIVR_POSTGRES_VOLUME_MB', volume_mb)
    maximum = env.get('QUIVR_POSTGRES_MAX_WAL_SIZE')
    if not maximum and budget is not None:
        maximum = str(min(32768, max(64, int(budget) // 10))) + 'MB'
    value['max_wal_size'] = maximum
    value['min_wal_size'] = env.get('QUIVR_POSTGRES_MIN_WAL_SIZE')
    if maximum and not value['min_wal_size']:
        value['min_wal_size'] = str(min(4096, max(32, memory_bytes(maximum) // 1048576 // 8))) + 'MB'
    return value


def equivalent(key, expected, actual):
    if expected is None or actual is None:
        return False
    if key in ('shared_buffers', 'effective_cache_size', 'work_mem', 'maintenance_work_mem',
               'max_wal_size', 'min_wal_size', 'memory', 'GOMEMLIMIT'):
        try:
            return memory_bytes(expected) == memory_bytes(actual)
        except ValueError:
            return False
    if key in ('cpus', 'random_page_cost', 'effective_io_concurrency', 'max_connections'):
        try:
            return Decimal(str(expected)) == Decimal(str(actual))
        except Exception:
            return False
    return expected == actual


def observation(rows, service, scope, key, expected, actual):
    status = ('unknown' if expected is None or actual is None else
              'match' if equivalent(key, expected, actual) else 'drift')
    rows.append({'service': service, 'scope': scope, 'setting': key,
                 'expected': expected, 'observed': actual, 'status': status})


def storage_observation(rows, name, spec, runtime):
    budget = spec.get('x-quivr-storage-budget-bytes')
    if not budget:
        return
    actual = runtime.get('limits', {}).get('storage')
    observation(rows, name, 'filesystem_capacity', 'minimum_bytes', budget, actual)
    if actual is not None:
        rows[-1]['status'] = 'match' if int(actual) >= int(budget) else 'drift'


def report(rows):
    status = 'drift' if any(row['status'] == 'drift' for row in rows) else (
        'unknown' if any(row['status'] == 'unknown' for row in rows) else 'match')
    return {'status': status, 'observations': rows}


def observed_resources(lines):
    result = {}
    for line in lines:
        key, separator, value = line.partition('=')
        if separator and key in ('__memory', '__cpus', '__storage'):
            try:
                number = Decimal(value)
                if number.is_finite() and number > 0:
                    name = key[2:]
                    smallest = min(number, Decimal(result.get(name, number)))
                    result[name] = str(int(smallest)) if name in ('memory', 'storage') else format(smallest, 'f')
            except Exception:
                continue
    return result


def safe_environment(name, value):
    """Do not echo arbitrary content mistakenly put in a managed setting."""
    if value is None:
        return None
    value = str(value)
    if name in ('QUIVR_POSTGRES_MEMORY_MB', 'QUIVR_POSTGRES_VOLUME_MB',
                'QUIVR_POSTGRES_MAX_CONNECTIONS', 'QUIVR_POSTGRES_EFFECTIVE_IO_CONCURRENCY'):
        bounds = {'QUIVR_POSTGRES_MEMORY_MB': (128, 999999999999),
                  'QUIVR_POSTGRES_VOLUME_MB': (1, 999999999999),
                  'QUIVR_POSTGRES_MAX_CONNECTIONS': (1, 262143),
                  'QUIVR_POSTGRES_EFFECTIVE_IO_CONCURRENCY': (0, 1000)}
        low, high = bounds[name]
        valid = bool(re.fullmatch(r'[0-9]{1,12}', value)) and low <= int(value) <= high
    elif name in ('QUIVR_POSTGRES_SHARED_BUFFERS', 'QUIVR_POSTGRES_EFFECTIVE_CACHE_SIZE',
                  'QUIVR_POSTGRES_WORK_MEM', 'QUIVR_POSTGRES_MAINTENANCE_WORK_MEM',
                  'QUIVR_POSTGRES_MAX_WAL_SIZE', 'QUIVR_POSTGRES_MIN_WAL_SIZE'):
        valid = bool(re.fullmatch(r'[1-9][0-9]{0,11}(kB|MB|GB|TB)', value))
        if valid and name in ('QUIVR_POSTGRES_MAX_WAL_SIZE', 'QUIVR_POSTGRES_MIN_WAL_SIZE'):
            valid = 32 <= memory_bytes(value) // 1048576 <= 2147483647
    elif name == 'QUIVR_POSTGRES_RANDOM_PAGE_COST':
        valid = bool(re.fullmatch(r'[0-9]{1,12}(\.[0-9]{1,6})?', value)) and Decimal(value) <= Decimal('1e10')
    elif name == 'QUIVR_POSTGRES_CHECKPOINT_TIMEOUT':
        match = re.fullmatch(r'([1-9][0-9]{0,11})(s|min|h)', value)
        valid = bool(match) and 30 <= int(match[1]) * {'s': 1, 'min': 60, 'h': 3600}[match[2]] <= 3600
    elif name in ('ASYNC_INDEXING', 'AUTOSCHEMA_ENABLED', 'AUTHENTICATION_ANONYMOUS_ACCESS_ENABLED'):
        valid = value in ('true', 'false')
    elif name == 'DEFAULT_QUANTIZATION':
        valid = value in ('rq-8', 'none', 'rq-1', 'pq', 'bq', 'sq')
    elif name == 'DEFAULT_VECTORIZER_MODULE':
        valid = value == 'none'
    elif name == 'PERSISTENCE_DATA_PATH':
        valid = value == '/var/lib/weaviate'
    elif name.endswith(('MEMTABLES_MAX_SIZE_MB', 'BOOTSTRAP_TIMEOUT', 'MAX_CONNECTIONS',
                         'MEMORY_MB', 'VOLUME_MB', 'EFFECTIVE_IO_CONCURRENCY',
                         'DOCUMENTS_PER_REPLICA', '_MIN', '_MAX')):
        valid = bool(re.fullmatch(r'[0-9]{1,12}', value))
    elif name == 'QUIVR_AUTOSCALER_BACKEND':
        valid = value in ('railway', 'kubernetes')
    elif name == 'QUIVR_AUTOSCALER_QUEUE':
        valid = value in ('bulk', 'live')
    elif name == 'QUIVR_POSTGRES_JIT':
        valid = value in ('on', 'off')
    elif name == 'QUIVR_POSTGRES_SYNCHRONOUS_COMMIT':
        valid = value == 'on'
    elif name == 'QUIVR_POSTGRES_STAT_STATEMENTS_TRACK':
        valid = value in ('none', 'top', 'all')
    elif name == 'QUIVR_POSTGRES_WAL_COMPRESSION':
        valid = value in ('on', 'off', 'pglz', 'lz4', 'zstd')
    else:
        valid = bool(re.fullmatch(r'[0-9]{1,12}(\.[0-9]{1,6})?(kB|MB|GB|TB|KiB|MiB|GiB|TiB|ms|s|min|m|h)?', value))
    return value if valid else '<invalid>'


def runtime_script(name):
    resources = '\n' + (ROOT / 'deploy/report-resources.sh').read_text()
    if name == 'postgres':
        query = (ROOT / 'deploy/postgres/report.sql').read_text()
        return ('psql -XAt -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-postgres}" '
                '-d "${POSTGRES_DB:-postgres}" -c ' + shlex.quote(query) + '\n'
                'directory=${PGDATA:-/var/lib/postgresql/data}\n'
                'df -Pm "$directory" | awk \'NR==2 {print $2}\'\n' + resources)
    return '\n'.join('printf \'%s\\n\' "' + key + '=${' + key + '-}"'
                     for key in sorted(KEYS[name])) + ('\ndirectory=/var/lib/weaviate' if name == 'weaviate' else '') + resources


def write_overlay(path, profile='small', overrides=None, environ=None, declaration_path=DECLARATION):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(compose_model(resolve(profile, overrides, environ, declaration_path)), indent=2) + '\n')
    return path


def generated_files():
    services = resolve()
    defaults = '# Generated by deploy/infrastructure.py generate; edit deploy/infrastructure.json.\n'
    for key, value in services['postgres']['environment'].items():
        defaults += ': "${' + key + ':=' + value + '}"\n'
    dockerfile = ('# Generated by deploy/infrastructure.py generate; edit deploy/infrastructure.json.\n'
                  'FROM ' + services['postgres']['image'] + '\n'
                  'COPY --chmod=755 deploy/postgres/start.sh /usr/local/bin/quivr-postgres\n'
                  'COPY deploy/postgres/defaults.sh /usr/local/bin/defaults.sh\n'
                  'COPY deploy/postgres/init.sql /docker-entrypoint-initdb.d/quivr.sql\n'
                  'ENTRYPOINT ["quivr-postgres"]\nCMD ["postgres"]\n')
    build = json.loads(DECLARATION.read_text())['x-quivr']['build_images']
    for image in build.values():
        if not re.fullmatch(r'[a-zA-Z0-9./:_-]+@sha256:[0-9a-f]{64}', image):
            raise ValueError('build images require immutable digests')
    autoscaler = ('# Generated by deploy/infrastructure.py generate; edit deploy/infrastructure.json.\n'
                  'FROM ' + build['go'] + ' AS build\n'
                  'WORKDIR /app\nCOPY go.mod go.sum ./\n'
                  'COPY cmd/quivr-autoscaler ./cmd/quivr-autoscaler\n'
                  'COPY internal/autoscaling ./internal/autoscaling\n'
                  'RUN CGO_ENABLED=0 go build -trimpath -o /quivr-autoscaler ./cmd/quivr-autoscaler\n\n'
                  'FROM ' + build['autoscaler_runtime'] + '\n'
                  'COPY --from=build /quivr-autoscaler /usr/local/bin/quivr-autoscaler\n'
                  'ENTRYPOINT ["/usr/local/bin/quivr-autoscaler"]\n')
    return {ROOT / 'deploy/postgres/defaults.sh': defaults,
            ROOT / 'deploy/railway/postgres.Dockerfile': dockerfile,
            ROOT / 'cmd/quivr-autoscaler/Dockerfile': autoscaler}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['render', 'generate'])
    parser.add_argument('--profile', default='small')
    parser.add_argument('--overrides', type=Path)
    parser.add_argument('--output', type=Path)
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    try:
        if args.command == 'generate':
            for path, content in generated_files().items():
                if args.check:
                    if not path.exists() or path.read_text() != content:
                        raise ValueError('stale infrastructure artifact: ' + str(path.relative_to(ROOT)))
                else:
                    path.write_text(content)
        elif args.output:
            write_overlay(args.output, args.profile, args.overrides, os.environ)
        else:
            print(json.dumps(compose_model(resolve(args.profile, args.overrides, os.environ)), indent=2))
        return 0
    except (ValueError, OSError, KeyError, TypeError):
        print('Infrastructure declaration or arguments are invalid; check the declaration and override schema.', file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
