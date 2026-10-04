"""Opt-in, local-only private responses; never uploaded with published sets."""
import collections
import fcntl
import hashlib
import json
import os
import pathlib
import stat
import tempfile
import threading


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, ensure_ascii=False,
                                     separators=(',', ':'), allow_nan=False).encode()).hexdigest()


class ResponseCache:
    def __init__(self, directory):
        path = pathlib.Path(directory)
        if path.is_symlink():
            raise ValueError('cache must be a local private directory')
        self.directory = path.resolve()
        checkout = pathlib.Path(__file__).resolve().parents[2]
        if self.directory.is_relative_to(checkout) or any((p / '.git').exists() for p in (self.directory, *self.directory.parents)):
            raise ValueError('cache must be outside the checkout')
        self.directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        if self.directory.stat().st_uid != os.getuid():
            raise ValueError('cache must belong to the current user')
        self.directory.chmod(0o700)
        self.lock = threading.RLock()
        self.guard = os.open(self.directory / 'lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
        try:
            info = os.fstat(self.guard)
            if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid():
                raise ValueError('cache lock must be a local owner file')
            os.fchmod(self.guard, 0o600)
            fcntl.flock(self.guard, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except Exception:
            self.close()
            raise ValueError('cache is already in use') from None

    def close(self):
        if self.guard is not None:
            os.close(self.guard)
            self.guard = None

    def get(self, key):
        with self.lock:
            try:
                fd = os.open(self.directory / (key + '.json'), os.O_RDONLY | os.O_NOFOLLOW)
            except FileNotFoundError:
                return None
            with os.fdopen(fd) as source:
                info = os.fstat(source.fileno())
                if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o777 != 0o600:
                    raise ValueError('cache file must be private and owner-only')
                return json.load(source)

    def put(self, key, value):
        with self.lock:
            fd, name = tempfile.mkstemp(dir=self.directory, prefix='.response-')
            try:
                with os.fdopen(fd, 'w') as output:
                    json.dump(value, output, ensure_ascii=False, allow_nan=False)
                    output.flush()
                    os.fsync(output.fileno())
                os.replace(name, self.directory / (key + '.json'))
            finally:
                if os.path.exists(name):
                    os.unlink(name)

    def salt(self, identity):
        with self.lock:
            key = digest(['salt-v1', identity])
            value = self.get(key)
            if value is None:
                value = os.urandom(32).hex()
                self.put(key, value)
            salt = bytes.fromhex(value)
            if len(salt) != 32:
                raise ValueError('invalid cached salt')
            return salt


class Reuse:
    def __init__(self):
        self.lock = threading.Lock()
        self.calls = self.inputs = self.outputs = 0
        self.cost = 0
        self.refusals = collections.Counter()

    def record(self, entry):
        with self.lock:
            self.calls += 1
            self.inputs += entry.get('input_tokens', 0)
            self.outputs += entry.get('output_tokens', 0)
            self.cost += entry.get('cost_usd', 0)
            if 'refusal' in entry:
                self.refusals[entry['refusal']] += 1

    def summary(self):
        with self.lock:
            return {'cached_calls': self.calls, 'cached_input_tokens': self.inputs,
                    'cached_output_tokens': self.outputs, 'cached_cost_usd': self.cost,
                    'cached_refusals': dict(self.refusals)}
