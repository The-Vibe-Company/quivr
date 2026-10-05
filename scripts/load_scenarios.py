"""Versioned local load scenarios. Unknown keys fail rather than silently changing a run."""
import math
import re


def keys(value, required, optional=()):
    if not isinstance(value, dict) or set(value) - set(required) - set(optional):
        raise ValueError('unknown scenario fields or invalid object')
    missing = set(required) - set(value)
    if missing:
        raise ValueError('missing scenario fields: ' + ', '.join(sorted(missing)))


def number(value, name, minimum, maximum, integer=False):
    if (type(value) not in (int, float) or not math.isfinite(value)
            or not minimum <= value <= maximum or (integer and type(value) is not int)):
        raise ValueError(f'{name} must be {"an integer" if integer else "a number"} in [{minimum}, {maximum}]')


def validate(s):
    keys(s, ('version', 'name', 'seed', 'corpus', 'duration_seconds', 'drain_seconds',
             'search', 'ingestion', 'alerts', 'replicas', 'fake_latency_ms'))
    if type(s['version']) is not int or s['version'] != 1:
        raise ValueError('unsupported scenario version (expected 1)')
    if not isinstance(s['name'], str) or not re.fullmatch(r'[a-z0-9][a-z0-9-]{0,63}', s['name']):
        raise ValueError('name must use lowercase letters, digits and hyphens')
    number(s['seed'], 'seed', 0, 2**32-1, True)
    number(s['duration_seconds'], 'duration_seconds', 1, 86400)
    number(s['drain_seconds'], 'drain_seconds', 1, 3600)
    number(s['alerts'], 'alerts', 0, 10000, True)
    keys(s['corpus'], ('records', 'words_per_record'))
    number(s['corpus']['records'], 'records', 1, 1000000, True)
    number(s['corpus']['words_per_record'], 'words_per_record', 10, 1000, True)
    keys(s['search'], ('concurrency', 'users', 'mix'))
    number(s['search']['concurrency'], 'concurrency', 1, 1000, True)
    number(s['search']['users'], 'users', s['search']['concurrency'], 100000, True)
    mix = s['search']['mix']
    if not isinstance(mix, dict) or not mix or set(mix) - {'lexical', 'semantic', 'hybrid', 'deep'}:
        raise ValueError('search mix supports lexical, semantic, hybrid and deep')
    for mode, weight in mix.items():
        number(weight, f'mix {mode}', 0, 10000, True)
    if sum(mix.values()) == 0:
        raise ValueError('search mix needs a positive weight')
    keys(s['ingestion'], ('per_second', 'concurrency', 'burst'))
    number(s['ingestion']['per_second'], 'per_second', 0, 10000)
    number(s['ingestion']['concurrency'], 'ingestion concurrency', 1, 1000, True)
    burst = s['ingestion']['burst']
    keys(burst, ('at_seconds', 'duration_seconds', 'multiplier'))
    number(burst['at_seconds'], 'burst at_seconds', 0, s['duration_seconds'])
    number(burst['duration_seconds'], 'burst duration_seconds', 0, s['duration_seconds'] - burst['at_seconds'])
    number(burst['multiplier'], 'burst multiplier', 1, 100)
    replicas = s['replicas']
    keys(replicas, ('api', 'worker'), ('kill_at_seconds',))
    for role in ('api', 'worker'):
        number(replicas[role], f'{role} replicas', 1, 2, True)
    if 'kill_at_seconds' in replicas:
        if replicas['api'] != 2 or replicas['worker'] != 2:
            raise ValueError('kill needs two API and worker replicas')
        number(replicas['kill_at_seconds'], 'kill_at_seconds', .1, s['duration_seconds'] - .1)
    keys(s['fake_latency_ms'], ('embedding', 'reranking', 'judge'))
    for kind, delay in s['fake_latency_ms'].items():
        number(delay, kind, 0, 500, True)
    return s


def read(path):
    import yaml
    return validate(yaml.safe_load(path.read_text()))
