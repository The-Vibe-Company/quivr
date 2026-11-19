#!/usr/bin/env python3
"""Render, install or observe an authenticated index host (Ubuntu 24.04)."""
import argparse
import copy
import ipaddress
import json
import os
from pathlib import Path
import re
import shlex
import stat
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
from deploy import infrastructure as infra

CONFIG = Path('/etc/quivr-vector-host')
DATA = Path('/var/lib/quivr-vector-host')
CADDY = 'caddy:2.10.2@sha256:c3d7ee5d2b11f9dc54f947f68a734c84e9c9666c92c88a7f30b9cba5da182adb'
AUTH = infra.VECTOR_HOST_AUTH


def run(args, stdin=None):
    result = subprocess.run(args, input=stdin, capture_output=True, text=True, timeout=600)
    if result.returncode:
        # Never echo Docker environment, command input or external diagnostics.
        raise RuntimeError('Host command failed; inspect the host privately')
    return result.stdout


def options(args):
    if not re.fullmatch(r'(?=.{1,253}$)[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?', args.hostname) or '.' not in args.hostname:
        raise ValueError('use a DNS hostname')
    if any(not label or len(label) > 63 or label.startswith('-') or label.endswith('-')
           for label in args.hostname.split('.')):
        raise ValueError('invalid DNS hostname')
    cidrs = sorted({str(ipaddress.ip_network(value, strict=True)) for value in args.operator_cidr})
    if not cidrs or any(ipaddress.ip_network(value).prefixlen == 0 for value in cidrs):
        raise ValueError('SSH needs restricted operator CIDRs')
    if not re.fullmatch(r'/dev/[A-Za-z0-9_./-]+', args.data_device):
        raise ValueError('use an explicit data device under /dev')
    spec = copy.deepcopy(infra.resolve(args.profile, args.overrides)['weaviate'])
    spec['environment'].update(AUTH)
    return {'hostname': args.hostname, 'operator_cidrs': cidrs, 'data_device': args.data_device,
            'weaviate': spec}


def assets(settings, uuid='UNMOUNTED'):
    spec = settings['weaviate']
    logging = {'driver': 'json-file', 'options': {'max-size': '20m', 'max-file': '5'}}
    def bind(source, destination, readonly=False):
        return {'type': 'bind', 'source': str(source), 'target': destination,
                'read_only': readonly, 'bind': {'create_host_path': False}}
    # The pinned server defaults its persisted cluster identity to os.Hostname.
    # Keep it stable across container recreation as well as machine restarts.
    index = {'image': spec['image'], 'hostname': 'quivr-vector-host', 'environment': spec['environment'],
             'env_file': [str(CONFIG / 'weaviate.env')], 'restart': 'no',
             'deploy': spec['deploy'], 'logging': logging,
             'volumes': [bind(DATA / 'index', '/var/lib/weaviate')], 'networks': ['index']}
    proxy = {'image': CADDY, 'restart': 'no', 'ports': ['443:443/tcp'], 'logging': logging,
             'volumes': [bind(CONFIG / 'Caddyfile', '/etc/caddy/Caddyfile', True),
                         bind(DATA / 'certificates', '/data')], 'networks': ['index']}
    model = {'name': 'quivr-vector-host', 'services': {'weaviate': index, 'proxy': proxy},
             'networks': {'index': {'driver': 'bridge'}}}
    proxy_config = ('{\n  admin off\n  auto_https disable_redirects\n}\n'
                    + settings['hostname'] + ' {\n  tls {\n    issuer acme {\n'
                    '      disable_http_challenge\n    }\n  }\n'
                    # The index client speaks REST and gRPC; both arrive on 443.
                    '  @grpc header Content-Type application/grpc*\n'
                    '  reverse_proxy @grpc h2c://weaviate:50051\n'
                    '  reverse_proxy weaviate:8080\n}\n')
    # Own table only; do not flush the machine's or Docker's rules. The forward
    # hook runs after Docker, so ACCEPT in Docker cannot bypass our policy.
    input_rules = ['ct state established,related accept', 'iifname "lo" accept',
                   'tcp dport 443 accept']
    for cidr in settings['operator_cidrs']:
        family = 'ip6' if ':' in cidr else 'ip'
        input_rules.append(f'{family} saddr {cidr} tcp dport 22 accept')
    firewall = ('table inet quivr_vector_host {\n chain input {\n'
                '  type filter hook input priority 10; policy drop;\n  '
                + ';\n  '.join(input_rules) + ';\n }\n chain forward {\n'
                '  type filter hook forward priority 10; policy accept;\n'
                '  ct state established,related accept\n'
                '  iifname "docker*" accept\n  iifname "br-*" accept\n'
                '  ct status dnat tcp dport 443 accept\n  drop\n }\n}\n')
    guard = ('#!/bin/sh\nset -eu\n'
             + 'test "$(findmnt -n -o UUID --mountpoint ' + str(DATA) + ')" = ' + shlex.quote(uuid) + '\n'
             'exec docker compose --project-name quivr-vector-host -f /etc/quivr-vector-host/compose.json '
             'up --abort-on-container-exit --remove-orphans --no-color\n')
    unit = ('[Unit]\nDescription=Authenticated vector index host\n'
            'RequiresMountsFor=/var/lib/quivr-vector-host\nAfter=docker.service quivr-vector-firewall.service\n'
            'Requires=quivr-vector-firewall.service\nBindsTo=docker.service\nPartOf=docker.service\n'
            '[Service]\nExecStart=/bin/sh /etc/quivr-vector-host/run.sh\n'
            'ExecStop=/usr/bin/docker compose --project-name quivr-vector-host '
            '-f /etc/quivr-vector-host/compose.json down\nRestart=always\nRestartSec=5\n'
            'TimeoutStopSec=120\n[Install]\nWantedBy=multi-user.target docker.service\n')
    firewall_unit = ('[Unit]\nDescription=Vector host firewall\nBefore=quivr-vector-host.service\n'
                     '[Service]\nType=oneshot\nRemainAfterExit=yes\n'
                     'ExecStart=/bin/sh /etc/quivr-vector-host/firewall.sh\n'
                     '[Install]\nWantedBy=multi-user.target\n')
    return {'compose.json': json.dumps(model, indent=2) + '\n', 'Caddyfile': proxy_config,
            'settings.json': json.dumps(settings, indent=2) + '\n', 'run.sh': guard,
            'firewall.nft': firewall,
            'firewall.sh': '#!/bin/sh\nset -eu\n'
                           '(if nft list table inet quivr_vector_host >/dev/null 2>&1; then\n'
                           '  printf "delete table inet quivr_vector_host\\n"\nfi\n'
                           'cat /etc/quivr-vector-host/firewall.nft) | nft -f -\n',
            'quivr-vector-host.service': unit, 'quivr-vector-firewall.service': firewall_unit}


def write(path, content, mode=0o600):
    if path.is_symlink():
        raise ValueError('refuse symlink configuration')
    if path.exists() and path.read_text() == content and stat.S_IMODE(path.stat().st_mode) == mode:
        return False
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + '.new')
    descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, mode)
    with os.fdopen(descriptor, 'w') as stream:
        stream.write(content)
    temporary.chmod(mode)
    temporary.replace(path)
    return True


def mount_disk(device, initialize):
    actual = str(Path(device).resolve(strict=True))
    root_source = run(['findmnt', '-n', '-o', 'SOURCE', '--target', '/']).strip()
    root_disks = run(['lsblk', '-s', '-n', '-p', '-o', 'NAME', root_source]).split()
    tree = json.loads(run(['lsblk', '--json', '--output', 'PATH,TYPE,FSTYPE,UUID,MOUNTPOINTS', actual]))['blockdevices']
    if len(tree) != 1 or tree[0]['type'] != 'disk' or tree[0].get('children') or actual in root_disks:
        raise ValueError('data device must be a separate whole disk, not root or a partitioned disk')
    disk = tree[0]
    if any(point and point != str(DATA) for point in disk.get('mountpoints', [])):
        raise ValueError('data disk is mounted elsewhere')
    filesystem = disk.get('fstype')
    if not filesystem:
        signatures = json.loads(run(['wipefs', '--no-act', '--json', actual])).get('signatures', [])
        if not initialize or signatures:
            raise ValueError('blank disk initialization requires --initialize-empty-disk and no signatures')
        run(['mkfs.ext4', actual])
    elif filesystem != 'ext4':
        raise ValueError('existing data filesystem must be ext4; never reformat it')
    uuid = run(['blkid', '-s', 'UUID', '-o', 'value', actual]).strip()
    if not re.fullmatch(r'[0-9a-fA-F-]{36}', uuid):
        raise ValueError('cannot verify data filesystem UUID')
    DATA.mkdir(parents=True, exist_ok=True)
    mounted = subprocess.run(['findmnt', '-n', '-o', 'UUID', '--mountpoint', str(DATA)],
                             capture_output=True, text=True, timeout=10)
    if mounted.returncode == 0 and mounted.stdout.strip() != uuid:
        raise ValueError('another filesystem occupies the data mount')
    if DATA.is_symlink() or (mounted.returncode != 0 and any(DATA.iterdir())):
        raise ValueError('unmounted data directory must be empty and not a symlink')
    fstab = Path('/etc/fstab')
    lines = fstab.read_text().splitlines()
    entry = f'UUID={uuid} {DATA} ext4 defaults,nofail,x-systemd.device-timeout=90s 0 2'
    existing = [line for line in lines if not line.startswith('#') and len(line.split()) > 1
                and line.split()[1] == str(DATA)]
    if existing and existing != [entry]:
        raise ValueError('conflicting data mount in fstab')
    if not existing:
        write(fstab, '\n'.join(lines + [entry]) + '\n', 0o644)
        run(['systemctl', 'daemon-reload'])
    if mounted.returncode != 0:
        run(['mount', str(DATA)])
    if run(['findmnt', '-n', '-o', 'UUID', '--mountpoint', str(DATA)]).strip() != uuid:
        raise ValueError('data mount UUID does not match')
    return uuid


def key(args):
    if args.key_stdin:
        value = sys.stdin.read().strip()
    elif args.api_key_file:
        source = args.api_key_file
        if source.is_symlink() or not source.is_file() or source.stat().st_mode & 0o077:
            raise ValueError('API key file must be private and regular')
        if source.resolve().is_relative_to(ROOT):
            raise ValueError('API key file must stay outside the repository')
        value = source.read_text().strip()
    else:
        value = os.environ.get('WEAVIATE_API_KEY', '')
    if not re.fullmatch(r'[A-Za-z0-9_.-]{16,256}', value):
        raise ValueError('API key must be 16-256 URL-safe characters')
    return value


def apply(settings, args):
    if os.geteuid() != 0:
        raise ValueError('prepare/apply requires root')
    # Preflight credentials and capacity before disk or firewall changes.
    secret = key(args) if args.command == 'apply' else None
    memory = int(settings['weaviate']['deploy']['resources']['limits']['memory'])
    total = int(next(line.split()[1] for line in Path('/proc/meminfo').read_text().splitlines()
                     if line.startswith('MemTotal:'))) * 1024
    if memory + 2 * 1024**3 > total:
        raise ValueError('host RAM must exceed container limit by at least 2 GiB; set a declared override')
    run(['docker', 'compose', 'version'])
    uuid = mount_disk(settings['data_device'], args.initialize_empty_disk)
    for directory in ('index', 'certificates'):
        (DATA / directory).mkdir(exist_ok=True)
    budget = settings['weaviate'].get('x-quivr-storage-budget-bytes')
    if budget and os.statvfs(DATA).f_blocks * os.statvfs(DATA).f_frsize < int(budget):
        raise ValueError('data filesystem is below the declared storage budget')
    changed = False
    for name, content in assets(settings, uuid).items():
        path = Path('/etc/systemd/system') / name if name.endswith('.service') else CONFIG / name
        changed = write(path, content, 0o644 if name.endswith('.service') else 0o600) or changed
    if secret is not None:
        changed = write(CONFIG / 'weaviate.env', 'AUTHENTICATION_APIKEY_ALLOWED_KEYS=' + secret + '\n') or changed
    # Validate before loading firewall rules or restarting containers.
    run(['nft', '--check', '-f', str(CONFIG / 'firewall.nft')])
    run(['docker', 'compose', '-f', str(CONFIG / 'compose.json'), 'config', '--quiet']) if secret else None
    if changed:
        run(['systemctl', 'daemon-reload'])
        run(['systemctl', 'enable', 'quivr-vector-firewall.service', 'quivr-vector-host.service'])
    if args.command == 'apply':
        if changed:
            run(['systemctl', 'restart', 'quivr-vector-firewall.service'])
            run(['systemctl', 'restart', 'quivr-vector-host.service'])
        else:
            run(['systemctl', 'start', 'quivr-vector-firewall.service', 'quivr-vector-host.service'])
    return {'status': 'changed' if changed else 'unchanged', 'mounted_uuid': uuid}


def observe(settings):
    rows = []
    spec = settings['weaviate']
    actual = {}
    mounted = None
    try:
        ids = run(['docker', 'compose', '-f', str(CONFIG / 'compose.json'), 'ps', '-q', 'weaviate']).split()
        if len(ids) == 1:
            container = json.loads(run(['docker', 'inspect', ids[0]]))[0]
            if container['State']['Running']:
                env = dict(item.split('=', 1) for item in container['Config']['Env'] if '=' in item)
                actual = {'image': container['Config']['Image'], 'environment': env,
                          'memory': str(container['HostConfig']['Memory']),
                          'cpus': str(container['HostConfig']['NanoCpus'] / 1e9)}
        mounted = run(['findmnt', '-n', '-o', 'UUID', '--mountpoint', str(DATA)]).strip()
    except (RuntimeError, ValueError, OSError, KeyError, IndexError, subprocess.TimeoutExpired):
        pass
    image = actual.get('image')
    if image and not re.fullmatch(r'[a-zA-Z0-9./:_-]+@sha256:[0-9a-f]{64}', image):
        image = '<unverified>'
    infra.observation(rows, 'weaviate', 'vector_host', 'image', spec['image'], image)
    for name, value in spec['environment'].items():
        observed = actual.get('environment', {}).get(name)
        if name in AUTH:
            observed = observed if observed in (AUTH[name], 'true', 'false') else None
        else:
            observed = infra.safe_environment(name, observed)
        infra.observation(rows, 'weaviate', 'vector_host', name, value, observed)
    for name, value in spec['deploy']['resources']['limits'].items():
        infra.observation(rows, 'weaviate', 'vector_host', name, value, actual.get(name))
    # Mount health cannot be inferred from the root filesystem's capacity.
    expected_uuid = None
    try:
        guard = (CONFIG / 'run.sh').read_text()
        expected_uuid = re.search(r'= ([0-9a-fA-F-]{36})\n', guard).group(1)
    except (OSError, AttributeError):
        pass
    infra.observation(rows, 'weaviate', 'vector_host', 'mounted_uuid', expected_uuid, mounted)
    capacity = str(os.statvfs(DATA).f_blocks * os.statvfs(DATA).f_frsize) if mounted else None
    infra.storage_observation(rows, 'weaviate', spec, {'limits': {'storage': capacity}})
    return infra.report(rows)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['render', 'prepare', 'apply', 'check'])
    parser.add_argument('--hostname')
    parser.add_argument('--operator-cidr', action='append', default=[])
    parser.add_argument('--data-device')
    parser.add_argument('--profile', default='large')
    parser.add_argument('--overrides', type=Path)
    parser.add_argument('--output', type=Path)
    parser.add_argument('--initialize-empty-disk', action='store_true')
    parser.add_argument('--api-key-file', type=Path)
    parser.add_argument('--key-stdin', action='store_true')
    args = parser.parse_args()
    try:
        settings = json.loads((CONFIG / 'settings.json').read_text()) if not args.hostname else options(args)
        if args.command == 'render':
            if not args.output:
                raise ValueError('render requires --output')
            for name, content in assets(settings).items():
                write(args.output / name, content)
            result = {'status': 'rendered'}
        elif args.command in ('prepare', 'apply'):
            result = apply(settings, args)
        else:
            result = observe(settings)
        print(json.dumps(result, indent=2))
        return {'match': 0, 'drift': 1, 'unknown': 2}.get(result['status'], 0)
    except (ValueError, RuntimeError, OSError, KeyError, TypeError, subprocess.TimeoutExpired):
        print('Vector host request failed; verify arguments, private credentials, disk and host capacity.', file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
