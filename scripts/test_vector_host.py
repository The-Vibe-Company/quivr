"""Dedicated index contracts at the operator CLI boundary; no cloud calls."""
import json
import importlib.util
import io
import os
from contextlib import nullcontext, redirect_stderr, redirect_stdout
from pathlib import Path
import shlex
import subprocess
import sys
import tarfile
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
HOST = ROOT / 'deploy/vector-host/host.py'


class VectorHost(unittest.TestCase):
    def test_render_owns_secure_auth_and_inherits_declared_profiles(self):
        # Owner: a renderer copying defaults or restoring anonymous access would
        # expose the index. Existing resolver tests cannot see this host overlay.
        pin = json.loads((ROOT / 'deploy/infrastructure.json').read_text())['services']['weaviate']['image']
        for profile, memory, asynchronous in [('small', '1700MiB', 'false'), ('large', '16GiB', 'true')]:
            with self.subTest(profile=profile), tempfile.TemporaryDirectory() as directory:
                target = Path(directory)
                flags = [sys.executable, str(HOST), 'render', '--hostname', 'index.example.invalid',
                         '--operator-cidr', '192.0.2.0/24', '--data-device', '/dev/disk/by-id/example.vector-host',
                         '--profile', profile, '--output', str(target / 'rendered')]
                for override in (False, True):
                    if override:
                        source = target / 'override.json'
                        source.write_text('{"services":{"weaviate":{"image":"registry.example/index@sha256:'
                                          + 'a' * 64 + '","environment":{"GOMEMLIMIT":"3GiB"}}}}')
                        flags.extend(['--overrides', str(source)])
                    result = subprocess.run(flags, capture_output=True, text=True, timeout=5,
                                            env={**os.environ, 'WEAVIATE_API_KEY': 'private-fixture-never-render'})
                    self.assertEqual(result.returncode, 0, result.stderr)
                    model = json.loads((target / 'rendered/compose.json').read_text())
                    index = model['services']['weaviate']
                    env = index['environment']
                    self.assertEqual(index['image'], 'registry.example/index@sha256:' + 'a' * 64 if override else pin)
                    self.assertEqual(env['AUTHENTICATION_ANONYMOUS_ACCESS_ENABLED'], 'false')
                    self.assertEqual(env['AUTHENTICATION_APIKEY_ENABLED'], 'true')
                    self.assertEqual(env['AUTHENTICATION_APIKEY_USERS'], 'quivr-engine')
                    self.assertEqual(env['AUTHORIZATION_ADMINLIST_USERS'], 'quivr-engine')
                    self.assertEqual(env['AUTHORIZATION_ADMINLIST_ENABLED'], 'true')
                    self.assertEqual(env['AUTHORIZATION_RBAC_ENABLED'], 'false')
                    self.assertEqual(env['GOMEMLIMIT'], '3GiB' if override else memory)
                    self.assertEqual(env['ASYNC_INDEXING'], asynchronous)
                    self.assertEqual(env['AUTOSCHEMA_ENABLED'], 'false')
                    self.assertNotIn('ports', index)
                    self.assertEqual(index['restart'], 'no')
                    self.assertEqual(index['hostname'], 'quivr-vector-host')
                    self.assertFalse(index['volumes'][0]['bind']['create_host_path'])
                    for path in (target / 'rendered').iterdir():
                        if path.is_file():
                            self.assertNotIn('private-fixture-never-render', path.read_text())

    def test_startup_refuses_missing_or_wrong_filesystem_before_docker(self):
        # Disk lifecycle owner: a detached disk must never turn the index bind
        # into a directory on root. Executes the real generated startup guard.
        module_spec = importlib.util.spec_from_file_location('vector_host', HOST)
        host = importlib.util.module_from_spec(module_spec)
        module_spec.loader.exec_module(host)
        settings = {'hostname': 'index.example.invalid', 'operator_cidrs': ['192.0.2.0/24'],
                    'data_device': '/dev/disk/by-id/example.vector-host', 'weaviate': host.infra.resolve()['weaviate']}
        uuid = '12345678-1234-1234-1234-123456789abc'
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            guard = root / 'run.sh'
            guard.write_text(host.assets(settings, uuid)['run.sh'])
            findmnt = root / 'findmnt'
            docker = root / 'docker'
            docker.write_text('#!/bin/sh\necho called > "$CALL_FILE"\n')
            docker.chmod(0o755)
            for observed in ('', 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', uuid):
                findmnt.write_text('#!/bin/sh\nprintf "%s" "' + observed + '"\n')
                findmnt.chmod(0o755)
                called = root / 'called'
                called.unlink(missing_ok=True)
                result = subprocess.run(['sh', str(guard)], env={**os.environ,
                    'PATH': str(root) + ':/usr/bin:/bin', 'CALL_FILE': str(called)}, capture_output=True, timeout=5)
                self.assertEqual(called.exists(), observed == uuid)
                self.assertEqual(result.returncode == 0, observed == uuid)

    def test_apply_repairs_live_drift_and_requires_verified_startup(self):
        module_spec = importlib.util.spec_from_file_location('vector_host', HOST)
        host = importlib.util.module_from_spec(module_spec)
        module_spec.loader.exec_module(host)
        uuid = '12345678-1234-1234-1234-123456789abc'
        flags = ['--hostname', 'index.example.invalid', '--operator-cidr', '192.0.2.0/24',
                 '--data-device', '/dev/disk/by-id/example.vector-host', '--profile', 'small',
                 '--startup-timeout', '0']
        key = 'fixture-key-never-reported'
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config, data = root / 'config', root / 'data'
            data.mkdir()
            overrides = root / 'override.json'
            overrides.write_text(json.dumps({'services': {'weaviate': {
                'image': 'registry.example/weaviate:1.39.14@sha256:' + 'b' * 64}}}))
            flags.extend(['--overrides', str(overrides)])
            # Docker still runs the previous image despite the desired files
            # having been installed by an interrupted apply.
            live = {'image': 'registry.example/weaviate:1.39.10@sha256:' + 'a' * 64,
                    'version': '1.39.10', 'ready': True, 'environment': {'ASYNC_INDEXING': 'true'}}
            restarts = []
            firewall_loads = []

            class IndexHTTP:
                def __init__(self, address, port, timeout):
                    self.address, self.port = address, port

                def request(self, method, route, headers=None):
                    self.route, self.headers = route, headers or {}

                def getresponse(self):
                    if (self.address, self.port) != ('172.30.0.2', 8080):
                        raise ConnectionError('unreachable index')
                    if self.route == '/v1/.well-known/ready':
                        return nullcontext(SimpleNamespace(status=200 if live['ready'] else 503, read=lambda: b''))
                    if self.route == '/v1/meta' and self.headers.get('Authorization') == 'Bearer ' + key:
                        return nullcontext(SimpleNamespace(status=200, read=lambda: json.dumps({'version': live['version']})))
                    return nullcontext(SimpleNamespace(status=401))

                def close(self):
                    pass

            def run(command, **kwargs):
                if command[-3:] == ['ps', '-q', 'weaviate']:
                    return 'index\n'
                if command == ['docker', 'inspect', 'index']:
                    return json.dumps([{'State': {'Running': True},
                        'Config': {'Image': live['image'], 'Env': [
                            *(f'{name}={value}' for name, value in live['environment'].items()),
                            'AUTHENTICATION_APIKEY_ALLOWED_KEYS=' + key]},
                        'HostConfig': {'Memory': 2147483648, 'NanoCpus': 2000000000},
                        'NetworkSettings': {'Networks': {'quivr-vector-host_index': {'IPAddress': '172.30.0.2'}}}}])
                if command[0] == 'findmnt':
                    return uuid
                if command == ['/bin/sh', str(config / 'firewall.sh')]:
                    firewall_loads.append(command)
                    return ''
                if command == ['systemctl', 'restart', host.HOST_UNIT]:
                    restarts.append(command)
                    index = json.loads((config / 'compose.json').read_text())['services']['weaviate']
                    live.update(image=index['image'], environment=dict(index['environment']), version='1.39.14')
                elif command[0] not in ('systemctl', 'nft', 'docker'):
                    self.fail(f'unexpected host command {command}')
                return ''

            def cli(command):
                with patch.object(sys, 'argv', [str(HOST), command, *flags]), redirect_stdout(io.StringIO()) as output:
                    code = host.main()
                self.assertNotIn(key, output.getvalue())
                return code, json.loads(output.getvalue())

            read_text = Path.read_text
            def read(path, *args, **kwargs):
                if path == Path('/proc/meminfo'):
                    return 'MemTotal: 8388608 kB\n'
                return read_text(path, *args, **kwargs)

            with patch.object(host, 'CONFIG', config), patch.object(host, 'DATA', data), \
                    patch.object(host, 'SYSTEMD', root / 'units'), \
                    patch.object(host, 'COMPOSE', ['docker', 'compose', '-f', str(config / 'compose.json')]), \
                    patch.object(host, 'mount_disk', return_value=uuid), patch.object(host, 'key', return_value=key), \
                    patch.object(host.os, 'geteuid', return_value=0), \
                    patch.object(host.http.client, 'HTTPConnection', IndexHTTP), \
                    patch.object(host, 'run', side_effect=run), patch.object(Path, 'read_text', read):
                self.assertEqual(cli('prepare')[0], 0)
                host.write(config / 'weaviate.env', 'AUTHENTICATION_APIKEY_ALLOWED_KEYS=' + key + '\n')
                declared = json.loads((config / 'compose.json').read_text())['services']['weaviate']['environment']
                live['environment'] = {**declared, **live['environment']}
                code, check = cli('check')
                self.assertEqual(code, 1)
                drifts = {row['setting'] for row in check['observations'] if row['status'] == 'drift'}
                self.assertEqual(drifts, {'image', 'version', 'ASYNC_INDEXING'})
                code, result = cli('apply')
                self.assertEqual((code, result['status'], result['check']['status']), (0, 'changed', 'match'))
                self.assertEqual(cli('apply')[1]['status'], 'unchanged')
                self.assertEqual(len(restarts), 1)
                self.assertEqual(len(firewall_loads), 2)
                del live['environment']['GOMEMLIMIT']
                self.assertEqual(cli('apply')[0], 0)
                self.assertEqual(len(restarts), 2)

                # Matching the image reference alone cannot attest the server
                # version or availability.
                live['version'] = '1.39.10'
                self.assertEqual(cli('apply')[0], 1)
                live['ready'] = False
                code, result = cli('apply')
                self.assertEqual((code, result['check']['status']), (2, 'unknown'))

    def test_azure_rollout_publishes_host_intent_only_after_apply_succeeds(self):
        module_spec = importlib.util.spec_from_file_location('vector_azure', HOST.with_name('azure.py'))
        azure = importlib.util.module_from_spec(module_spec)
        module_spec.loader.exec_module(azure)
        args = SimpleNamespace(profile='small', operator_cidr=['192.0.2.0/24'], startup_timeout=0)
        bootstrap, desired = 'a' * 64, 'b' * 64
        with tempfile.TemporaryDirectory() as directory, patch.object(azure, 'SOURCE', Path(directory)):
            root = Path(directory)
            (root / 'deploy/vector-host').mkdir(parents=True)
            marker = root / 'applied-intent'
            installed = root / 'deploy/vector-host/host.py'
            installed.write_text('raise SystemExit(0)\n')

            def run(command):
                return subprocess.run(shlex.split(command)[1:], input='fixture-key', capture_output=True,
                                      text=True, timeout=5)

            def normal(fingerprint):
                return run(azure.host_apply_command(args, 'index.example.invalid', None, fingerprint, bootstrap))

            self.assertEqual(normal(bootstrap).returncode, 0)
            for exit_code in (1, 0):
                stage = root / f'stage-{exit_code}'
                for relative in azure.HOST_FILES:
                    destination = stage / relative
                    destination.parent.mkdir(parents=True, exist_ok=True)
                    destination.write_text(desired if relative == 'applied-intent' else '')
                (stage / azure.HOST_SCRIPT).write_text(f'raise SystemExit({exit_code})\n')
                rollout = run(azure.host_apply_command(args, 'index.example.invalid', stage, desired, bootstrap))
                self.assertEqual(rollout.returncode, exit_code, rollout.stderr)
                self.assertEqual(marker.read_text(), '' if exit_code else desired)
                self.assertNotEqual(normal(bootstrap).returncode, 0)
                self.assertEqual(normal(desired).returncode, exit_code)

    def test_azure_cli_provisions_zonal_disk_and_has_no_repeat_writes(self):
        # Cloud boundary owner: provider replies describe fixed resources. The
        # real provisioner decides creation and no-op; no real az/SSH runs.
        module_spec = importlib.util.spec_from_file_location('vector_azure', HOST.with_name('azure.py'))
        azure = importlib.util.module_from_spec(module_spec)
        module_spec.loader.exec_module(azure)
        resources, rules, calls = {}, [], []
        init = None
        drift = None
        failure = None
        uploads = []
        applied_intent = None
        sentinel = 'private-provider-payload-never-print'
        def transport(command, **kwargs):
            nonlocal init, applied_intent
            calls.append(command)
            if command[0] == 'ssh':
                if isinstance(kwargs.get('input'), bytes):
                    with tarfile.open(fileobj=io.BytesIO(kwargs['input'])) as archive:
                        uploads.append({entry.name: archive.extractfile(entry).read() for entry in archive})
                    return subprocess.CompletedProcess(command, 0, b'.update.Abc12345\n', b'')
                phase = 'bootstrap' if kwargs.get('input') is None else 'apply'
                staged = phase == 'apply' and '.update.Abc12345/' in command[-1]
                if staged:
                    applied_intent = ''
                if failure == phase + '-timeout':
                    raise subprocess.TimeoutExpired([*command, sentinel], 1800, output=sentinel, stderr=sentinel)
                if failure == phase + '-request':
                    return subprocess.CompletedProcess(command, 1, sentinel, sentinel)
                if failure == 'apply-json' and phase == 'apply':
                    return subprocess.CompletedProcess(command, 0, sentinel, '')
                if kwargs.get('input') is None:
                    if 'applied-intent' in command[-1] and applied_intent is not None:
                        if not applied_intent or applied_intent not in command[-1]:
                            return subprocess.CompletedProcess(command, 1, '', '')
                    return subprocess.CompletedProcess(command, 0, '', '')
                if staged:
                    applied_intent = uploads[-1]['applied-intent'].decode()
                return subprocess.CompletedProcess(command, 0, '{"status":"unchanged"}', '')
            args = command[1:]
            def arg(name):
                return args[args.index(name) + 1]
            if args[:2] == ['group', 'exists']:
                response = True
            elif args[:2] == ['vm', 'list-skus']:
                response = [{'name': 'Standard_E8s_v5', 'resourceType': 'virtualMachines',
                    'locationInfo': [{'zones': ['1']}], 'restrictions': [],
                    'capabilities': [{'name': key, 'value': value} for key, value in
                        [('MemoryGB', '64'), ('vCPUs', '8'), ('UncachedDiskIOPS', '12800'),
                         ('UncachedDiskBytesPerSecond', '290000000')]]}]
            elif args[:2] == ['resource', 'list']:
                response = list(resources.values())
            elif args[:2] == ['tag', 'update']:
                vm = resources['example-index']
                self.assertEqual(arg('--resource-id'), vm['id'])
                self.assertEqual(arg('--operation'), 'Merge')
                name, value = arg('--tags').split('=', 1)
                self.assertEqual(name, 'quivr-vector-applied-intent')
                self.assertEqual(value, applied_intent)
                if failure == 'tag-request':
                    return subprocess.CompletedProcess(command, 1, sentinel, sentinel)
                vm['tags'][name] = value
                response = {}
            elif args[:3] == ['vm', 'run-command', 'invoke']:
                if failure == 'trust-request':
                    return subprocess.CompletedProcess(command, 1, sentinel, sentinel)
                if failure == 'trust-response':
                    return subprocess.CompletedProcess(command, 0, json.dumps({'value': [{'message': sentinel}]}), '')
                response = {'value': [{'message': '[stdout]\nssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n[stderr]\n'}]}
            elif args[:4] == ['network', 'nsg', 'rule', 'list']:
                response = rules
            elif args[:4] == ['network', 'nsg', 'rule', 'create']:
                start = args.index('--source-address-prefixes') + 1
                end = args.index('--source-port-ranges')
                rules.append({'name': arg('--name'), 'priority': int(arg('--priority')),
                    'access': arg('--access'), 'protocol': arg('--protocol'), 'direction': 'Inbound',
                    'destinationPortRange': arg('--destination-port-ranges'),
                    'sourceAddressPrefixes': args[start:end]})
                response = {}
            elif 'create' in args:
                name = arg('--name')
                tags_start = args.index('--tags') + 1
                tags_end = args.index('--only-show-errors')
                kind = {('network', 'nsg'): 'Microsoft.Network/networkSecurityGroups',
                    ('network', 'vnet'): 'Microsoft.Network/virtualNetworks',
                    ('network', 'public-ip'): 'Microsoft.Network/publicIPAddresses',
                    ('network', 'nic'): 'Microsoft.Network/networkInterfaces',
                    ('disk', 'create'): 'Microsoft.Compute/disks',
                    ('vm', 'create'): 'Microsoft.Compute/virtualMachines'}[tuple(args[:2])]
                resources[name] = {'name': name, 'type': kind, 'id': '/subscriptions/example/resourceGroups/example-group/providers/' + kind + '/' + name,
                    'tags': dict(value.split('=', 1) for value in args[tags_start:tags_end])}
                if args[:2] == ['vm', 'create']:
                    init = json.loads(Path(arg('--custom-data')).read_text().split('\n', 1)[1])
                response = {}
            elif args[:3] == ['network', 'public-ip', 'show']:
                response = {'id': '/subscriptions/example/resourceGroups/example-group/providers/Microsoft.Network/publicIPAddresses/example-index-public-ip', 'zones': ['1'],
                            'sku': {'name': 'Standard'}, 'publicIPAllocationMethod': 'Dynamic' if drift == 'ip' else 'Static',
                            'publicIPAddressVersion': 'IPv4',
                            'dnsSettings': {'domainNameLabel': 'example-index',
                            'fqdn': 'example-index.region.cloudapp.azure.com'}}
            elif args[:3] == ['network', 'vnet', 'show']:
                response = {'addressSpace': {'addressPrefixes': ['10.80.0.0/16']},
                    'subnets': [{'name': 'example-index-subnet', 'addressPrefix': '10.81.0.0/24' if drift == 'vnet' else '10.80.0.0/24'}]}
            elif args[:3] == ['network', 'nic', 'show']:
                response = {'id': '/subscriptions/example/resourceGroups/example-group/providers/Microsoft.Network/networkInterfaces/example-index-nic', 'enableAcceleratedNetworking': True,
                    'networkSecurityGroup': {'id': '/subscriptions/example/resourceGroups/example-group/providers/Microsoft.Network/networkSecurityGroups/other-nsg' if drift == 'nic' else '/subscriptions/example/resourceGroups/example-group/providers/Microsoft.Network/networkSecurityGroups/example-index-nsg'},
                    'ipConfigurations': [{'publicIPAddress': {'id': '/subscriptions/example/resourceGroups/example-group/providers/Microsoft.Network/publicIPAddresses/example-index-public-ip'},
                        'subnet': {'id': '/subscriptions/example/resourceGroups/example-group/providers/Microsoft.Network/virtualNetworks/example-index-vnet/subnets/example-index-subnet'}}]}
            elif args[:2] == ['disk', 'show']:
                if failure == 'disk-timeout':
                    raise subprocess.TimeoutExpired([*command, sentinel], 1800, output=sentinel, stderr=sentinel)
                if failure == 'disk-request':
                    return subprocess.CompletedProcess(command, 1, sentinel, sentinel)
                if failure == 'disk-json':
                    return subprocess.CompletedProcess(command, 0, sentinel, '')
                response = json.loads((ROOT / 'scripts/fixtures/example.azure-disk-show/disk.json').read_text())
                # Simulate Azure attaching the created disk when the VM exists.
                response['managedBy'] = resources.get('example-index', {}).get('id')
                if drift in disk_drift:
                    response.update(disk_drift[drift])
                if failure and failure.startswith('missing-'):
                    del response[failure.removeprefix('missing-')]
            elif args[:2] == ['vm', 'show']:
                response = {'id': '/subscriptions/example/resourceGroups/example-group/providers/Microsoft.Compute/virtualMachines/example-index', 'zones': ['1'],
                    'hardwareProfile': {'vmSize': 'Standard_E8s_v5'},
                    'networkProfile': {'networkInterfaces': [{'id': '/subscriptions/example/resourceGroups/example-group/providers/Microsoft.Network/networkInterfaces/example-index-nic'}]},
                    'osProfile': {'adminUsername': 'operator'},
                    'storageProfile': {'imageReference': {'publisher': 'Other' if drift == 'image' else 'Canonical',
                        'offer': 'ubuntu-24_04-lts', 'sku': 'server', 'version': 'latest'}, 'dataDisks': [
                    {'lun': 0, 'caching': 'None', 'deleteOption': 'Detach',
                     'managedDisk': {'id': '/subscriptions/example/resourceGroups/example-group/providers/Microsoft.Compute/disks/example-index-disk'}}]}}
            else:
                self.fail('unexpected provider operation: ' + str(args[:3]))
            return subprocess.CompletedProcess(command, 0, json.dumps(response), '')
        disk_drift = {
            'disk-size': {'diskSizeGB': 256},
            'disk-sku': {'sku': {'name': 'Standard_LRS'}},
            'disk-zone': {'zones': ['2']},
            'disk-iops': {'diskIOPSReadWrite': 3000},
            'disk-mbps': {'diskMBpsReadWrite': 125},
            'disk-owner': {'managedBy': '/subscriptions/example/providers/Microsoft.Compute/virtualMachines/other'},
        }
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            public_key, identity, api_key = [root / name for name in ('public', 'identity', 'key')]
            public_key.write_text('ssh-ed25519 fixture-public-key')
            identity.write_text('fixture-private-key')
            api_key.write_text('fixture-api-key-never-in-cloud-init')
            identity.chmod(0o600)
            api_key.chmod(0o600)
            argv = [str(HOST.with_name('azure.py')), '--resource-group', 'example-group', '--name', 'example-index',
                '--location', 'region', '--zone', '1', '--dns-label', 'example-index', '--admin-user', 'operator',
                '--operator-cidr', '192.0.2.0/24', '--ssh-public-key', str(public_key),
                '--ssh-identity', str(identity), '--api-key-file', str(api_key)]
            with patch.object(azure.subprocess, 'run', side_effect=transport), patch.object(sys, 'argv', argv), redirect_stdout(io.StringIO()):
                self.assertEqual(azure.main(), 0)
                first_calls = list(calls)
                calls.clear()
                self.assertEqual(azure.main(), 0)
                repeat_calls = list(calls)
                calls.clear()
                for drift in ('nic', 'ip', 'vnet', 'image', *disk_drift):
                    with self.subTest(drift=drift), redirect_stderr(io.StringIO()) as error:
                        calls.clear()
                        self.assertEqual(azure.main(), 2)
                        self.assertFalse(any(command[0] == 'ssh' or 'create' in command for command in calls))
                        if drift in disk_drift:
                            self.assertIn('resource=example-index-disk', error.getvalue())
                            self.assertIn('exception=ValueError', error.getvalue())
                drift = None
                cases = [
                    ('disk-request', 'inspect data disk', 'example-index-disk', 'RuntimeError'),
                    ('disk-timeout', 'inspect data disk', 'example-index-disk', 'TimeoutExpired'),
                    ('disk-json', 'inspect data disk', 'example-index-disk', 'JSONDecodeError'),
                    ('trust-request', 'retrieve SSH host key', 'example-index', 'RuntimeError'),
                    ('trust-response', 'validate SSH host key', 'example-index', 'ValueError'),
                    ('bootstrap-request', 'verify host bootstrap', 'example-index', 'RuntimeError'),
                    ('bootstrap-timeout', 'verify host bootstrap', 'example-index', 'TimeoutExpired'),
                    ('apply-request', 'apply host configuration', 'example-index', 'RuntimeError'),
                    ('apply-timeout', 'apply host configuration', 'example-index', 'TimeoutExpired'),
                    ('apply-json', 'apply host configuration', 'example-index', 'JSONDecodeError'),
                    *[('missing-' + key, 'validate data disk', 'example-index-disk', 'KeyError')
                      for key in ('diskSizeGB', 'diskIOPSReadWrite', 'diskMBpsReadWrite', 'sku')],
                ]
                for failure, step, resource, exception in cases:
                    with self.subTest(failure=failure), redirect_stderr(io.StringIO()) as error, redirect_stdout(io.StringIO()) as output:
                        calls.clear()
                        if failure.startswith('trust-'):
                            (root / 'example-index.vector-host.known_hosts').unlink(missing_ok=True)
                        self.assertEqual(azure.main(), 2)
                        self.assertIn('step=' + step, error.getvalue())
                        self.assertIn('resource=' + resource, error.getvalue())
                        self.assertIn('exception=' + exception, error.getvalue())
                        self.assertEqual(output.getvalue(), '')
                        for private in (sentinel, 'fixture-api-key-never-in-cloud-init', str(identity), '--only-show-errors', 'sudo python3'):
                            self.assertNotIn(private, error.getvalue())
                        self.assertFalse(any('create' in command for command in calls))
                failure = None
                with patch.object(sys, 'argv', [*argv, '--disk-size', '0']), redirect_stderr(io.StringIO()) as error:
                    calls.clear()
                    self.assertEqual(azure.main(), 2)
                    self.assertEqual(calls, [])
                    self.assertIn('step=validate local inputs', error.getvalue())
                    self.assertIn('resource=local inputs', error.getvalue())
                    self.assertIn('exception=ValueError', error.getvalue())
                # All resources and the VM bootstrap marker belong to an earlier
                # revision. Only explicit reconciliation may reuse that intent.
                for resource in resources.values():
                    resource['tags']['quivr-vector-intent'] = 'a' * 64
                calls.clear()
                with redirect_stderr(io.StringIO()):
                    self.assertEqual(azure.main(), 2)
                self.assertFalse(any(command[0] == 'ssh' or
                                     {'create', 'update', 'attach'}.intersection(command) for command in calls))
                calls.clear()
                with patch.object(sys, 'argv', [*argv, '--reconcile']):
                    failure = 'apply-request'
                    with redirect_stderr(io.StringIO()):
                        self.assertEqual(azure.main(), 2)
                    self.assertNotIn('quivr-vector-applied-intent', resources['example-index']['tags'])
                    self.assertFalse(any(command[1:3] == ['tag', 'update'] for command in calls))
                    failure = 'tag-request'
                    with redirect_stderr(io.StringIO()):
                        self.assertEqual(azure.main(), 2)
                    self.assertNotIn('quivr-vector-applied-intent', resources['example-index']['tags'])
                    failure = None
                    calls.clear()
                    uploads.clear()
                    self.assertEqual(azure.main(), 0)
                    self.assertEqual(calls[-1][1:3], ['tag', 'update'])
                    self.assertFalse(any({'create', 'update', 'attach'}.intersection(command) for command in calls[:-1]))
                    verify, upload, apply = [command[-1] for command in calls if command[0] == 'ssh']
                    self.assertIn('a' * 64, verify)
                    self.assertIn('tar -xzm -f -', upload)
                    self.assertIn('.update.Abc12345/deploy/vector-host/host.py apply', apply)
                    self.assertNotIn('--initialize-empty-disk', apply)
                    self.assertEqual(len(uploads), 1)
                    self.assertNotIn('bootstrap-intent', uploads[0])
                    self.assertEqual(resources['example-index']['tags']['quivr-vector-intent'], 'a' * 64)
                    self.assertNotIn(b'fixture-api-key-never-in-cloud-init', b''.join(uploads[0].values()))
                    self.assertEqual(uploads[0]['deploy/infrastructure.json'],
                                     (ROOT / 'deploy/infrastructure.json').read_bytes())
                    limits = json.loads(uploads[0]['override.json'])['services']['weaviate']['deploy']['resources']['limits']
                    self.assertEqual(limits['cpus'], '8.0')
                calls.clear()
                self.assertEqual(azure.main(), 0)
                self.assertEqual(len(uploads), 1)
                self.assertFalse(any({'create', 'update', 'attach'}.intersection(command) for command in calls))
                applied_intent = 'b' * 64
                calls.clear()
                with redirect_stderr(io.StringIO()):
                    self.assertEqual(azure.main(), 2)
                self.assertEqual(len([command for command in calls if command[0] == 'ssh']), 1)
                with patch.object(sys, 'argv', [*argv, '--reconcile']):
                    resources['example-index-disk']['tags']['quivr-vector-intent'] = 'b' * 64
                    calls.clear()
                    with redirect_stderr(io.StringIO()):
                        self.assertEqual(azure.main(), 2)
                    self.assertFalse(any(command[0] == 'ssh' or 'create' in command for command in calls))
            self.assertFalse(any('create' in command or 'update' in command or 'attach' in command for command in repeat_calls))
            disk_command = next(command for command in first_calls if command[1:3] == ['disk', 'create'])
            self.assertIn('PremiumV2_LRS', disk_command)
            self.assertEqual(disk_command[disk_command.index('--disk-mbps-read-write') + 1], '250')
            self.assertNotIn('fixture-api-key-never-in-cloud-init', json.dumps(init))
            ssh_calls = [command for command in first_calls if command[0] == 'ssh']
            self.assertTrue(ssh_calls)
            self.assertTrue(all('StrictHostKeyChecking=yes' in command for command in ssh_calls))
            self.assertFalse(any(command[1:4] == ['vm', 'run-command', 'invoke'] for command in repeat_calls))
            self.assertIn('--initialize-empty-disk', init['runcmd'][0])
            self.assertIn('/dev/disk/azure/scsi1/lun0', init['runcmd'][0])
            self.assertEqual({rule['destinationPortRange'] for rule in rules if rule['access'] == 'Allow'}, {'443', '22'})

    def test_azure_intent_source_hash_covers_all_sources_with_framing(self):
        module_spec = importlib.util.spec_from_file_location('vector_azure', HOST.with_name('azure.py'))
        azure = importlib.util.module_from_spec(module_spec)
        module_spec.loader.exec_module(azure)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in azure.SOURCES:
                target = root / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes((ROOT / name).read_bytes())
            baseline = azure.source_digest(root)
            # Editing every declared source in SOURCES (including report-resources.sh and host.py) must change the digest.
            for name in azure.SOURCES:
                with self.subTest(source=name):
                    target = root / name
                    original = target.read_bytes()
                    target.write_bytes(original + b'\n# changed\n')
                    try:
                        self.assertNotEqual(azure.source_digest(root), baseline)
                    finally:
                        target.write_bytes(original)
            # Shifting a byte across adjacent SOURCES entries must never collide.
            (root / azure.SOURCES[0]).write_bytes(b'ab')
            (root / azure.SOURCES[1]).write_bytes(b'c')
            first = azure.source_digest(root)
            (root / azure.SOURCES[0]).write_bytes(b'a')
            (root / azure.SOURCES[1]).write_bytes(b'bc')
            self.assertNotEqual(azure.source_digest(root), first)


if __name__ == '__main__':
    unittest.main()
