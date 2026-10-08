"""Owned local stack for load measurements; exactly one test-only plugin pin."""
import json
import os
import pathlib
import re
import signal
import subprocess
import time
import urllib.error
import urllib.request
import uuid

from local import CAPTURE_SECRET, GO, ROOT, Stack, MACOS, MODEL, DISK_LIMIT
import ports

PLUGIN = ROOT / 'tests/fakes/load-plugin'
SERVICES = ('postgres', 'temporal', 'seaweed', 'weaviate')
# Durable Version step columns, in pipeline order (accepted_at is the revision's).
STEPS = ('accepted', 'materialized', 'segmented', 'retrieval_ready', 'enriched', 'evaluated')


def runtime_env():
    # Provider keys, external proxy settings and QUIVR_* deployment overrides
    # never reach a measured process. All connection settings come from our config.
    return {k: os.environ[k] for k in ('PATH', 'HOME', 'TMPDIR', 'LANG') if k in os.environ}


def local_docker_host():
    """Resolve Docker's selected endpoint once, refusing every remote transport."""
    context = os.environ.get('DOCKER_CONTEXT')
    host = os.environ.get('DOCKER_HOST') if not context else None
    if not host:
        command = ['docker', 'context', 'inspect']
        if context:
            command.append(context)
        host = subprocess.check_output(command + ['--format', '{{.Endpoints.docker.Host}}'],
                                       text=True).strip()
    if not host.startswith('unix:///'):
        raise ValueError('load measurements require a local Unix socket Docker endpoint')
    return host


class LoadStack(Stack):
    def __init__(self, scenario):
        self.docker_host = local_docker_host()
        super().__init__('quivr-load-' + uuid.uuid4().hex[:10])
        self.scenario = scenario
        self.children = []
        self.apis = []
        self.workers = []
        self.plugin_port = ports.allocate()

    def docker(self, *args, **kwargs):
        return subprocess.run(['docker', '--host', self.docker_host, *args],
                              env={**runtime_env(), **kwargs.pop('env', {})}, check=True, **kwargs)

    def compose(self, *args, **kwargs):
        files = ['-f', str(ROOT / 'deploy/compose/compose.yaml')]
        if MACOS:
            files += ['-f', str(ROOT / 'deploy/compose/compose.macos.yaml')]
        return self.docker('compose', '-p', self.name, *files, *args, env={
            'QUIVR_DB_PASSWORD': self.state['password'], 'QUIVR_LOCAL_ROOT': str(self.directory),
            'QUIVR_MODEL_ROOT': str(MODEL)}, **kwargs)

    def check_disk(self):
        root = self.docker('info', '--format', '{{.DockerRootDir}}',
                           capture_output=True, text=True).stdout.strip()
        if not os.path.isdir(root):  # Docker Desktop's local VM.
            return
        disk = os.statvfs(root)
        used = 100*(disk.f_blocks-disk.f_bfree)/disk.f_blocks
        if used >= DISK_LIMIT:
            raise RuntimeError(f'Docker volume disk is {used:.1f}% full; free space before measuring')

    def config(self):
        addresses = {service: self.compose('port', service, port, capture_output=True,
                     text=True).stdout.strip() for service, port in
                     [('postgres', '5432'), ('temporal', '7233'),
                      ('seaweed', '8333'), ('weaviate', '8080')]}
        s = self.state
        latencies = self.scenario['fake_latency_ms']
        self.cfg = {
            'database_url': f"postgres://quivr:{s['password']}@{addresses['postgres']}/quivr?sslmode=disable",
            'temporal_address': addresses['temporal'],
            'weaviate_url': 'http://' + addresses['weaviate'],
            's3': {'endpoint': 'http://' + addresses['seaweed'], 'access_key': s['s3_access'],
                   'secret_key': s['s3_secret'], 'bucket': 'quivr-content'},
            'cursor_key': s['cursor_key'], 'credential_key': s['credential_key'],
            'log_directory': str(self.directory), 'plugin_plan_poll': '200ms',
            'keys': {s['admin']: {'organization': 'org_load', 'corpora': ['*'], 'actions':
                     ['corpora:read', 'corpora:write', 'content:read', 'content:write',
                      'search:query', 'monitoring:read', 'monitoring:write']}},
            'ingestion': {'default': 'load.fake'},
            'retrieval': {'profiles': {'default': 'load.fake/default', 'deep': 'load.fake/deep'}},
            'plugins': [{'manifest': str(PLUGIN / 'quivr-plugin.yaml'),
                         'endpoint': f'http://127.0.0.1:{self.plugin_port}',
                         'configuration': {key + '_ms': value for key, value in latencies.items()},
                         'spaces': {'load.fake.words': 'served'}}],
            'destinations': {'load-receiver': {'organization': 'org_load',
                             'url': f"http://127.0.0.1:{s['receiver_port']}/capture", 'secret': CAPTURE_SECRET}},
            'delivery': {'allow_private_destinations': True},
        }
        self.write_config('config.json', {**self.cfg, 'listen': f"127.0.0.1:{s['api_port']}",
                                         'probe_listen': f"127.0.0.1:{s['probe_port']}"})

    def write_config(self, name, cfg):
        path = self.directory / name
        path.write_text(json.dumps(cfg))
        path.chmod(0o600)
        return path

    def process(self, argv, label, extra=None):
        with (self.directory / (label + '.log')).open('a') as out:
            child = subprocess.Popen(argv, cwd=ROOT, env={**runtime_env(), **(extra or {})},
                                     stdout=out, stderr=out, start_new_session=True)
        self.children.append(child)
        self.state['pids'].append(child.pid)
        self.save()
        return child

    def ready(self, child, url):
        deadline = time.monotonic() + 60
        while child.poll() is None and time.monotonic() < deadline:
            try:
                with urllib.request.urlopen(url, timeout=1) as response:
                    if response.status in (200, 204):
                        return
            except OSError:
                pass
            time.sleep(.1)
        raise RuntimeError(f'load process did not become ready; inspect {self.directory}')

    def up(self):
        self.check_disk()
        subprocess.run([GO, 'build', '-o', str(self.directory / 'quivr'), './cmd/quivr'],
                       cwd=ROOT, check=True)
        subprocess.run([GO, 'build', '-o', str(self.directory / 'load-plugin'), '.'],
                       cwd=PLUGIN, check=True)
        # Named services exclude TEI; no model download or inference service.
        self.compose('up', '-d', '--wait', '--wait-timeout', '180', *SERVICES)
        self.config()
        subprocess.run([str(self.directory / 'quivr'), 'migrate'],
                       env={**runtime_env(), 'QUIVR_CONFIG': str(self.directory / 'config.json')},
                       stdout=subprocess.DEVNULL, check=True)
        child = self.process([str(self.directory / 'load-plugin')], 'fake-plugin', {
            'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(self.plugin_port),
            'QUIVR_PLUGIN_MANIFEST': str(PLUGIN / 'quivr-plugin.yaml')})
        self.ready(child, f'http://127.0.0.1:{self.plugin_port}/v0/health')
        for role, target in [('worker', self.workers), ('api', self.apis)]:
            for index in range(self.scenario['replicas'][role]):
                address, probe = ports.allocate(), ports.allocate()
                cfg = self.write_config(f'{role}-{index}.json', {**self.cfg,
                      'listen': f'127.0.0.1:{address}', 'probe_listen': f'127.0.0.1:{probe}'})
                child = self.process([str(self.directory / 'quivr'), role], f'{role}-{index}',
                                     {'QUIVR_CONFIG': str(cfg)})
                target.append({'process': child, 'url': f'http://127.0.0.1:{address}'})
                self.ready(child, f'http://127.0.0.1:{probe}/readyz')

    def step_times(self, versions):
        """Durable step times of our own Versions, as epoch seconds by step."""
        if not versions:
            return {}
        if not all(re.fullmatch(r'[A-Za-z0-9_:-]+', v) for v in versions):
            raise ValueError('unexpected Version identifier')
        query = ('SELECT v.id,' + ','.join(f"extract(epoch FROM {'a' if step == 'accepted' else 'v'}.{step}_at)" for step in STEPS)
            + " FROM record_versions v JOIN accepted_revisions a ON (a.organization,a.version_id)=(v.organization,v.id)"
            + " WHERE v.id=ANY(string_to_array('" + ','.join(versions) + "',','))")
        rows = self.compose('exec', '-T', 'postgres', 'psql', '-U', 'quivr', '-d', 'quivr', '-At', '-F', '\t',
                            '-c', query, capture_output=True, text=True).stdout.splitlines()
        return {row[0]: {step: float(value) for step, value in zip(STEPS, row[1:]) if value}
                for row in (line.split('\t') for line in rows if line)}

    def kill_replica(self):
        killed = []
        for role, instances in [('api', self.apis), ('worker', self.workers)]:
            instance = instances[-1]
            instance['process'].kill()
            instance['process'].wait(timeout=10)
            killed.append(role + '-1')
        return killed

    def endpoints(self):
        return [a['url'] for a in self.apis if a['process'].poll() is None]

    def down(self):
        for child in reversed(self.children):
            if child.poll() is None:
                try:
                    os.killpg(child.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass  # Exited after poll; still reap it and remove containers.
        for child in reversed(self.children):
            try:
                child.wait(timeout=10)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(child.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                child.wait(timeout=5)
        self.state['pids'] = []
        self.save()
        self.compose('down', '--volumes', timeout=60)
