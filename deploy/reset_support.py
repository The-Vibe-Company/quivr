"""Private reset checkpoints and bounded dependency operations."""
import fcntl
import json
import os
from pathlib import Path
import tempfile
import time


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


def wait_until(read, ready, seconds=180):
    deadline = time.monotonic() + seconds
    while True:
        value = read()
        if ready(value):
            return value
        if time.monotonic() >= deadline:
            raise RuntimeError('reset dependency condition timed out')
        time.sleep(.2)


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


def terminate_workflows(run):
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
        wait_until(count, lambda value: value == 0)
    return before
