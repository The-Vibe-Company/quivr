"""External normalizer of the local harness.

Plugin API v0 pins a single plugin, so a stack pins one of:

* ``pdf-text``: the reference plugin plugins/pdf-text for application/pdf,
  the default of `make dev`;
* ``template``: the `quivr plugin init` template, scaffolded once per stack,
  for text/markdown; `make verify` starts with it;
* ``none``: no external normalizer;
* a plugin directory: the author's own plugin, pinned for every media type its
  normalizer declares at http://127.0.0.1:$QUIVR_NORMALIZER_PORT (default
  9900) with $QUIVR_NORMALIZER_CONFIG (JSON, default {}). The author runs it,
  for example with `quivr plugin dev --port 9900 <dir>`.

`make dev` reads QUIVR_NORMALIZER (default pdf-text). pdf-text and the template
run as their own process with the repository's Python Plugin SDK. Verification proves
startup refusal of invalid pins and that an unreachable plugin leaves the API
and worker healthy, then switches the pin to pdf-text. Every oracle is a
process exit status or a public HTTP read.
"""
import plugin_environment
import json, os, pathlib, signal, subprocess, sys, time, urllib.error, urllib.request
import ports

ROOT = pathlib.Path(__file__).resolve().parents[1]
NAME = 'markdown-sections'
SDK = ROOT / '.scratch' / 'plugin-sdk'
PDF_TEXT = ROOT / 'plugins' / 'pdf-text'
# Selection -> Python module, routed media type and plugin configuration.
PLUGINS = {'pdf-text': ('pdf_text', ['application/pdf'], {}),
           'newsml-g2': ('newsml_g2', ['application/vnd.iptc.g2.newsitem+xml',
                                    'application/vnd.iptc.g2.newsmessage+xml'], {}),
           'template': (NAME.replace('-', '_'), ['text/markdown'], {'max_sections': 32})}
CHOICES = [*PLUGINS, 'none']


def from_environment():
    """The selection of `make dev`: QUIVR_NORMALIZER, default pdf-text."""
    return os.environ.get('QUIVR_NORMALIZER') or 'pdf-text'


def select(stack, name):
    """Record the selection; a plugin directory also records its port and configuration."""
    if name not in CHOICES:
        path = pathlib.Path(name).expanduser().resolve()
        if not (path / 'quivr-plugin.yaml').is_file():
            raise ValueError(f'unknown normalizer {name!r}; QUIVR_NORMALIZER is one of: {", ".join(CHOICES)}, or a plugin directory with a quivr-plugin.yaml')
        name = str(path)
        stack.state['custom_plugin'] = {'port': int(os.environ.get('QUIVR_NORMALIZER_PORT') or 9900),
                                        'configuration': json.loads(os.environ.get('QUIVR_NORMALIZER_CONFIG') or '{}')}
    stack.state['normalizer'] = name
    stack.save()


def selected(stack):
    return stack.state.get('normalizer')


def custom(stack):
    """Whether the selection is an author's plugin directory."""
    return selected(stack) not in CHOICES + [None]


def directory(stack):
    if custom(stack):
        return pathlib.Path(selected(stack))
    return ROOT / 'plugins' / selected(stack) if selected(stack) in {'pdf-text', 'newsml-g2'} else stack.directory / 'normalizer-plugin'


def manifest(stack):
    return directory(stack) / 'quivr-plugin.yaml'


def describe(stack):
    """One line for `make dev`: what is pinned and how to change it."""
    hint = 'QUIVR_NORMALIZER=pdf-text|newsml-g2|template|none|<plugin-dir>'
    if custom(stack):
        port = stack.state['custom_plugin']['port']
        return f'External normalizer: {directory(stack)}, pinned at http://127.0.0.1:{port}; run it with: quivr plugin dev --port {port} {directory(stack)} ({hint})'
    return f'External normalizer: {selected(stack)} ({hint})'


def media_types(stack):
    """Media types the selected plugin's normalizer declares (quivr plugin inspect --json)."""
    result = subprocess.run([str(stack.directory / 'quivr'), 'plugin', 'inspect', '--json', str(manifest(stack))], cwd=ROOT, capture_output=True, text=True)
    report = json.loads(result.stdout or '{}')
    if not report.get('valid'):
        raise RuntimeError(f'{manifest(stack)} is not a valid plugin manifest; run: quivr plugin inspect {directory(stack)}')
    return report['manifest']['contributions']['normalizer']['media_types']


def pin(stack, manifest_path=None, configuration=None):
    """Startup pin of QUIVR_CONFIG: manifest, endpoint, configuration, routes.

    None (no pin) when nothing is selected or the template is not scaffolded
    yet, so stacks that never select one, such as the measurement harness,
    run without a normalizer.
    """
    name = selected(stack)
    if custom(stack):
        own = stack.state['custom_plugin']
        return {'manifest': str(manifest(stack)), 'endpoint': f"http://127.0.0.1:{own['port']}", 'configuration': own['configuration'],
                'routes': [{'media_type': m, 'mode': 'required'} for m in media_types(stack)]}
    if name not in PLUGINS or (manifest_path is None and not manifest(stack).exists()):
        return None
    _, routed_types, default = PLUGINS[name]
    stack.state.setdefault('plugin_port', stack_port())
    return {'manifest': str(manifest_path or manifest(stack)), 'endpoint': f"http://127.0.0.1:{stack.state['plugin_port']}",
            'configuration': configuration if configuration is not None else default,
            'routes': [{'media_type': value, 'mode': 'required'} for value in routed_types]}


def python():
    """The SDK virtualenv `make test` prepares (scripts/plugin_sdk.sh), created if absent."""
    py = SDK / 'venv' / 'bin' / 'python'
    if not py.exists():
        SDK.mkdir(parents=True, exist_ok=True)
        subprocess.run([sys.executable, '-m', 'venv', str(SDK / 'venv')], check=True)
        subprocess.run([str(SDK / 'venv' / 'bin' / 'pip'), 'install', '-q', '--disable-pip-version-check',
                        '-c', 'contracts/http/v0/checks/requirements.txt', '-e', 'sdks/python'], cwd=ROOT, check=True)
    return py


def prepare(stack):
    """Prepare the selected plugin (pdf-text by default) and assign its port.

    pdf-text is installed into the SDK virtualenv with its pinned dependencies;
    the template is scaffolded once with the stack's quivr binary.
    """
    stack.state.setdefault('normalizer', 'pdf-text')
    stack.state.setdefault('plugin_port', stack_port())
    stack.save()
    name = selected(stack)
    if name in {'pdf-text', 'newsml-g2'}:
        imports = 'import pdf_text, pypdf, cryptography' if name == 'pdf-text' else 'import newsml_g2, defusedxml'
        if subprocess.run([str(python()), '-c', imports], cwd=SDK, capture_output=True).returncode:
            subprocess.run([str(SDK / 'venv' / 'bin' / 'pip'), 'install', '-q', '--disable-pip-version-check',
                            '-c', 'contracts/http/v0/checks/requirements.txt', '-e', str(directory(stack))], cwd=ROOT, check=True)
    elif name == 'template' and not manifest(stack).exists():
        with (stack.directory / 'normalizer-plugin-init.log').open('w') as log:
            subprocess.run([str(stack.directory / 'quivr'), 'plugin', 'init', NAME, '--dir', str(directory(stack))], cwd=ROOT, check=True, stdout=log, stderr=log)


def stack_port():
    """A plugin port no other harness service can be given (scripts/ports.py)."""
    return ports.allocate()


def healthy(stack):
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{stack.state['plugin_port']}/v0/health", timeout=1) as r:
            return r.status == 200
    except OSError:
        return False


def start(stack):
    """Run the selected plugin like `quivr plugin dev` does, then wait for its health."""
    stop(stack)
    if selected(stack) not in PLUGINS:
        return
    env = {**plugin_environment.inherited(), 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(stack.state['plugin_port']), 'QUIVR_PLUGIN_MANIFEST': str(manifest(stack))}
    with (stack.directory / 'normalizer-plugin.log').open('a') as log:
        p = subprocess.Popen([str(python()), '-m', PLUGINS[selected(stack)][0]], cwd=directory(stack), env=env, stdout=log, stderr=log, start_new_session=True)
    stack.state['plugin_pid'] = p.pid
    stack.save()
    deadline = time.monotonic() + 30
    while not healthy(stack):
        if p.poll() is not None or time.monotonic() > deadline:
            raise RuntimeError('normalizer plugin not healthy; inspect ' + str(stack.directory / 'normalizer-plugin.log'))
        time.sleep(.1)


def stop(stack):
    pid = stack.state.pop('plugin_pid', None)
    stack.save()
    if pid is None:
        return
    try:
        os.killpg(pid, signal.SIGTERM)
    except (ProcessLookupError, PermissionError):
        return
    deadline = time.monotonic() + 10
    while healthy(stack) and time.monotonic() < deadline:
        time.sleep(.05)


def probe(port, path):
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{port}{path}", timeout=2) as r:
            return r.status
    except urllib.error.HTTPError as e:
        return e.code
    except OSError:
        return None


def refused(stack, name, plugin, code):
    """api and worker must exit non-zero on the pin, naming the issue code."""
    cfg = json.loads((stack.directory / 'config.json').read_text())
    cfg['plugin'] = plugin
    path = stack.directory / f'bad-pin-{name}.json'
    path.write_text(json.dumps(cfg))
    path.chmod(0o600)
    for command in ['api', 'worker']:
        result = subprocess.run([str(stack.directory / 'quivr'), command], cwd=ROOT, env={**os.environ, 'QUIVR_CONFIG': str(path)}, capture_output=True, text=True, timeout=30)
        (stack.directory / f'bad-pin-{name}-{command}.log').write_text(result.stdout + result.stderr)
        assert result.returncode != 0 and code in result.stdout, (name, command, result.returncode, result.stdout + result.stderr)


def switch(stack, name):
    """Pin another plugin: restart the plugin, api, worker and the short-retention API on the new pin."""
    stop(stack)
    stack.stop_processes()
    select(stack, name)
    prepare(stack)
    stack.config()
    start(stack)
    stack.start_processes()
    stack.start_short_retention_api()


def verify(stack):
    """Refuse invalid pins at startup, then stop the plugin and restart the API and worker."""
    template = manifest(stack).read_text()
    for name, field, bad_range, code in [('plugin-api', 'plugin_api', '">=999.0.0 <1000.0.0"', 'incompatible_plugin_api'),
                                          ('engine', 'engine', '">=9.0.0"', 'incompatible_engine')]:
        bad = stack.directory / f'bad-plugin-{name}'
        bad.mkdir(exist_ok=True)
        lines = [f'  {field}: {bad_range}' if line.strip().startswith(field + ':') else line for line in template.splitlines()]
        (bad / 'quivr-plugin.yaml').write_text('\n'.join(lines) + '\n')
        refused(stack, name, pin(stack, bad / 'quivr-plugin.yaml'), code)
    refused(stack, 'configuration', pin(stack, configuration={'max_sections': 0}), 'invalid_configuration')
    # Declared extension namespaces must be the plugin's own and must not clash with a built-in one.
    for name, text, code in [('foreign-namespace', template.replace(f'{NAME}.outline:', 'other.outline:'), 'foreign_namespace'),
                             ('builtin-namespace', template.replace(f'id: {NAME}', 'id: example').replace(f'{NAME}.outline:', 'example.editorial:'), 'namespace_conflict')]:
        assert text != template, name
        bad = stack.directory / f'bad-plugin-{name}'
        bad.mkdir(exist_ok=True)
        (bad / 'quivr-plugin.yaml').write_text(text)
        refused(stack, name, pin(stack, bad / 'quivr-plugin.yaml'), code)
    # An unreachable plugin is not a startup failure.
    stop(stack)
    stack.stop_processes()
    stack.start_processes()
    # stop_processes also stopped the short-retention API that later steps use.
    stack.start_short_retention_api()
    s = stack.state
    for port in [s['probe_port'], s['worker_probe_port']]:
        assert probe(port, '/healthz') == 204 and probe(port, '/readyz') == 204, port
    (stack.directory / 'normalizer-startup.json').write_text(json.dumps({'startup_refusals': 'passed', 'unreachable_plugin_healthy': 'passed'}))


def outage(stack):
    """With the plugin stopped, routed work waits while the platform stays healthy; it completes once the plugin is back."""
    s = stack.state
    env = {'QUIVR_TEST_NORMALIZER_OUTAGE': '1', 'QUIVR_TEST_API_PROBE_URL': f"http://127.0.0.1:{s['probe_port']}"}
    stop(stack)
    stack.tests('TestNormalizerOutageKeepsThePlatformHealthy', env)
    start(stack)
    stack.tests('TestNormalizerOutageRecovers', env)


# The controllable test plugin (internal/plugins/devhost/fakeplugin) answers each
# request with the failure its Record Key names. A budget of two attempts keeps the
# timeout and retryable scenarios short while still retrying once.
FAULTY_MANIFEST = '''id: quivr-test.faulty
version: 0.1.0
compatibility:
  engine: ">=0.1.0 <0.3.0"
  plugin_api: ">=0.1.0 <0.2.0"
contributions:
  normalizer:
    media_types: [text/x-fault, text/x-fault-optional]
    timeout_ms: 1000
    retry:
      max_attempts: 2
'''

# Its fixed release (THE-785): the same plugin answering every request with a body
# Part core.ingest indexes (fake mode "body"), which the
# operator registers and activates before reprocessing what 0.1.0 quarantined. It
# also declares text/markdown, which the Contract Runner's normative fixture checks.
FIXED_MANIFEST = FAULTY_MANIFEST.replace('version: 0.1.0', 'version: 0.2.0').replace(
    'media_types: [text/x-fault, text/x-fault-optional]', 'media_types: [text/x-fault, text/x-fault-optional, text/markdown]')


def failures(stack):
    """Pin the controllable test plugin, observe every failure class publicly, reprocess a
    quarantined Version before and after activating its fixed release, then restore the stack's pin."""
    directory = stack.directory / 'faulty-plugin'
    directory.mkdir(exist_ok=True)
    binary = directory / 'quivr-fake-plugin'
    subprocess.run([os.environ.get('GO', 'go'), 'test', '-c', '-o', str(binary), './tests/plugin-contract'], cwd=ROOT, check=True)
    manifest_path = directory / 'quivr-plugin.yaml'
    manifest_path.write_text(FAULTY_MANIFEST)
    port = stack_port()
    env = {**plugin_environment.inherited(), 'QUIVR_FAKE_PLUGIN': '1', 'QUIVR_FAKE_PLUGIN_MODE': 'by-record-key',
           'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(port), 'QUIVR_PLUGIN_MANIFEST': str(manifest_path)}
    with (stack.directory / 'faulty-plugin.log').open('a') as log:
        plugin = subprocess.Popen([str(binary), '-test.run=^$'], cwd=directory, env=env, stdout=log, stderr=log, start_new_session=True)
    fixed_manifest = directory / 'quivr-plugin-0.2.0.yaml'
    fixed_manifest.write_text(FIXED_MANIFEST)
    fixed_port = stack_port()
    with (stack.directory / 'fixed-plugin.log').open('a') as log:
        fixed = subprocess.Popen([str(binary), '-test.run=^$'], cwd=directory, stdout=log, stderr=log, start_new_session=True,
                                 env={**env, 'QUIVR_FAKE_PLUGIN_MODE': 'body', 'QUIVR_PLUGIN_PORT': str(fixed_port), 'QUIVR_PLUGIN_MANIFEST': str(fixed_manifest)})
    configs = {name: (stack.directory / name).read_text() for name in ['config.json', 'worker.json']}
    try:
        deadline = time.monotonic() + 30
        for p, name, where in [(plugin, 'faulty', port), (fixed, 'fixed', fixed_port)]:
            while probe(where, '/v0/health') != 200:
                if p.poll() is not None or time.monotonic() > deadline:
                    raise RuntimeError(f'{name} plugin not healthy; inspect ' + str(stack.directory / f'{name}-plugin.log'))
                time.sleep(.1)
        for name, text in configs.items():
            cfg = json.loads(text)
            cfg['plugin'] = {'manifest': str(manifest_path), 'endpoint': f'http://127.0.0.1:{port}',
                             'routes': [{'media_type': 'text/x-fault', 'mode': 'required'}, {'media_type': 'text/x-fault-optional', 'mode': 'optional'}]}
            path = stack.directory / name
            path.write_text(json.dumps(cfg))
            path.chmod(0o600)
        stack.stop_processes()
        stack.start_processes()
        stack.tests('TestNormalizerFailures', {'QUIVR_TEST_FAULTY_NORMALIZER': '1'})
        stack.tests('^TestQuarantineReprocessNormalization$', {'QUIVR_TEST_FIXED_NORMALIZER_ENDPOINT': f'http://127.0.0.1:{fixed_port}',
                                                               'QUIVR_TEST_FIXED_NORMALIZER_MANIFEST': str(fixed_manifest)})
    finally:
        for name, text in configs.items():
            (stack.directory / name).write_text(text)
        for p in [plugin, fixed]:
            try:
                os.killpg(p.pid, signal.SIGTERM)
            except (ProcessLookupError, PermissionError):
                pass
        stack.stop_processes()
        stack.start_processes()
        stack.start_short_retention_api()
