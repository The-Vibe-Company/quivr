"""Private reset checkpoints and bounded dependency operations."""
import fcntl
import json
import os
from pathlib import Path
import tempfile
import time
from urllib.parse import quote, urlsplit


def vector_endpoint(value):
    """Compare endpoint identities without ever reporting their raw contents."""
    try:
        parsed = urlsplit(value)
        if (parsed.scheme not in ('http', 'https') or not parsed.hostname or parsed.username or parsed.password
                or parsed.path not in ('', '/') or parsed.query or parsed.fragment):
            return None
        return parsed.scheme, parsed.hostname.lower(), parsed.port or (443 if parsed.scheme == 'https' else 80)
    except (ValueError, TypeError, AttributeError):
        return None


def require_local_vector(urls, local):
    expected = {vector_endpoint(url) for url in local} - {None}
    if not expected or not urls or any(vector_endpoint(url) not in expected for url in urls):
        raise ValueError('reset refuses an external or unverifiable vector index before stopping writers')


def runtime_vector_command():
    import shlex
    code = 'import json; print(json.dumps(json.load(open("/tmp/quivr-runtime.json")).get("weaviate_url")))'
    return 'python3 -c ' + shlex.quote(code)


def vector_objects(value):
    # Weaviate's pinned response declares totalResults with omitempty.
    if not isinstance(value, dict) or 'objects' not in value:
        raise RuntimeError('unexpected Weaviate object response')
    objects = value['objects']
    count = value.get('totalResults', 0)
    if (objects is not None and not isinstance(objects, list) or isinstance(count, bool)
            or not isinstance(count, int) or count < len(objects or []) or bool(count) != bool(objects)):
        raise RuntimeError('unexpected Weaviate object count')
    return count


def vector_inventory(client):
    """Count indexed Weaviate objects across shared and multi-tenant collections."""
    total = vector_objects(client.request_json('/v1/objects?limit=1'))
    inactive_tenants = 0
    schema = client.request_json('/v1/schema')
    if not isinstance(schema, dict) or 'classes' not in schema:
        raise RuntimeError('unexpected Weaviate schema response')
    classes = schema['classes']
    if classes is None:
        classes = []
    if not isinstance(classes, list):
        raise RuntimeError('unexpected Weaviate schema response')
    for entry in classes:
        if not isinstance(entry, dict):
            raise RuntimeError('unexpected Weaviate schema response')
        mt = entry.get('multiTenancyConfig')
        if not (isinstance(mt, dict) and mt.get('enabled') is True):
            continue
        name = entry.get('class')
        if not isinstance(name, str) or not name:
            raise RuntimeError('unexpected Weaviate class name')
        encoded_class = quote(name, safe='')
        tenants = client.request_json(f'/v1/schema/{encoded_class}/tenants')
        if not isinstance(tenants, list):
            raise RuntimeError('unexpected Weaviate tenants response')
        for tenant in tenants:
            if not isinstance(tenant, dict) or not isinstance(tenant.get('name'), str) or not tenant['name']:
                raise RuntimeError('unexpected Weaviate tenant entry')
            status = tenant.get('activityStatus')
            if status is not None and not isinstance(status, str):
                raise RuntimeError('unexpected Weaviate tenant entry')
            if (status or 'ACTIVE').upper() not in ('ACTIVE', 'HOT'):
                # Inactive or offloaded tenants cannot be queried via /v1/objects
                # without activating them; track them separately rather than
                # misreporting them as an object count.
                inactive_tenants += 1
                continue
            encoded_tenant = quote(tenant['name'], safe='')
            total += vector_objects(client.request_json(
                f'/v1/objects?class={encoded_class}&tenant={encoded_tenant}&limit=1'))
    return {'objects': total, 'inactive_tenants': inactive_tenants}


class ResetTimeout(RuntimeError):
    """A polling deadline expired, rather than a provider request failing."""


class ResetFailure(RuntimeError):
    """Only locally authored phase and role/condition may reach the CLI."""
    def __init__(self, phase, role):
        super().__init__(f'Reset incomplete at phase {phase}, role/condition {role}.')


def wait_until(read, ready, seconds=180, interval=.2):
    deadline = time.monotonic() + seconds
    while True:
        value = read()
        if ready(value):
            return value
        if time.monotonic() >= deadline:
            raise ResetTimeout('reset dependency condition timed out')
        time.sleep(interval)


class Checkpoint:
    """An incomplete destructive reset requires inspection, never a blind replay."""
    def __init__(self, path, spec):
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        for candidate in (self.path, Path(str(self.path) + '.lock')):
            if candidate.exists() or candidate.is_symlink():
                if candidate.is_symlink() or not candidate.is_file() or candidate.stat().st_mode & 0o077:
                    raise RuntimeError('reset checkpoint must be a private regular file')
        self.lock = os.open(str(self.path) + '.lock', os.O_CREAT | os.O_RDWR, 0o600)
        fcntl.flock(self.lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if self.path.exists() and json.loads(self.path.read_text()).get('phase') != 'complete':
            raise RuntimeError('incomplete reset checkpoint requires private inspection')
        self.data = {'version': 1, 'deployment': spec['deployment'], 'project': spec['project'],
                     'environment': spec.get('environment'), 'phase': 'preflight'}

    def save(self, phase, **data):
        self.data.update(data, phase=phase)
        fd, temporary = tempfile.mkstemp(dir=self.path.parent, prefix='.reset-')
        try:
            with os.fdopen(fd, 'w') as stream:
                json.dump(self.data, stream)
                stream.flush()
                os.fsync(stream.fileno())
            os.replace(temporary, self.path)
            directory = os.open(self.path.parent, os.O_RDONLY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)

    def close(self):
        os.close(self.lock)


def terminate_workflows(run, interval=.2):
    arguments = ['workflow', 'count', '--address', '127.0.0.1:7233', '--namespace', 'default',
                 '--query', 'ExecutionStatus = "Running"', '--output', 'json']
    def count():
        value = json.loads(run(arguments))
        # The pinned protobuf serializer omits a zero count entirely.
        if not isinstance(value, dict) or set(value) - {'count'}:
            raise RuntimeError('unexpected Temporal workflow count')
        raw = value.get('count', 0)
        if isinstance(raw, bool) or not str(raw).isascii() or not str(raw).isdigit():
            raise RuntimeError('unexpected Temporal workflow count')
        return int(raw)
    before = count()
    if before:
        run(['workflow', 'terminate', '--address', '127.0.0.1:7233', '--namespace', 'default',
             '--query', 'ExecutionStatus = "Running"', '--reason', 'confirmed installation reset', '--yes'])
        wait_until(count, lambda value: value == 0, interval=interval)
    return before
