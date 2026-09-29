"""External normalizer of the local harness: the `quivr plugin init` template.

The harness scaffolds the template once per stack, runs it as its own process
with the repository's Python Plugin SDK and pins it in the startup
configuration for text/markdown. Verification then proves startup refusal of
invalid pins and that an unreachable plugin leaves the API and worker healthy.
Every oracle is a process exit status or a public HTTP read.
"""
import json, os, pathlib, signal, subprocess, sys, time, urllib.error, urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[1]
NAME = 'markdown-sections'
MODULE = NAME.replace('-', '_')
SDK = ROOT / '.scratch' / 'plugin-sdk'


def directory(stack):
    return stack.directory / 'normalizer-plugin'


def manifest(stack):
    return directory(stack) / 'quivr-plugin.yaml'


def pin(stack, manifest_path=None, configuration=None):
    """Startup pin of QUIVR_CONFIG: manifest, endpoint, configuration, routes.

    None (no pin) until prepare scaffolded the plugin, so stacks that never
    prepare it, such as the measurement harness, run without a normalizer.
    """
    if manifest_path is None and not manifest(stack).exists():
        return None
    stack.state.setdefault('plugin_port', stack_port())
    return {'manifest': str(manifest_path or manifest(stack)), 'endpoint': f"http://127.0.0.1:{stack.state['plugin_port']}",
            'configuration': configuration if configuration is not None else {'max_sections': 32},
            'routes': [{'media_type': 'text/markdown', 'mode': 'required'}]}


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
    """Scaffold the template with the stack's quivr binary (once) and assign its port."""
    stack.state.setdefault('plugin_port', stack_port())
    stack.save()
    if not manifest(stack).exists():
        with (stack.directory / 'normalizer-plugin-init.log').open('w') as log:
            subprocess.run([str(stack.directory / 'quivr'), 'plugin', 'init', NAME, '--dir', str(directory(stack))], cwd=ROOT, check=True, stdout=log, stderr=log)


def stack_port():
    import socket
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def healthy(stack):
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{stack.state['plugin_port']}/v0/health", timeout=1) as r:
            return r.status == 200
    except OSError:
        return False


def start(stack):
    """Run the plugin like `quivr plugin dev` does, then wait for its health."""
    stop(stack)
    env = {**os.environ, 'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(stack.state['plugin_port']), 'QUIVR_PLUGIN_MANIFEST': str(manifest(stack))}
    with (stack.directory / 'normalizer-plugin.log').open('a') as log:
        p = subprocess.Popen([str(python()), '-m', MODULE], cwd=directory(stack), env=env, stdout=log, stderr=log, start_new_session=True)
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
        (stack.directory / f'bad-pin-{name}-{command}.log').write_text(result.stderr)
        assert result.returncode != 0 and code in result.stderr, (name, command, result.returncode, result.stderr)


def verify(stack):
    """Refuse invalid pins at startup, then stop the plugin and restart the API and worker."""
    template = manifest(stack).read_text()
    for name, field, bad_range, code in [('plugin-api', 'plugin_api', '">=0.9.0 <1.0.0"', 'incompatible_plugin_api'),
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
