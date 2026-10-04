"""Build and own the shared Go API fake processes; no provider responses here."""
import atexit
import functools
import json
import os
import pathlib
import select
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]


@functools.cache
def binary(name):
    work = ROOT / '.scratch' / 'fakes'
    work.mkdir(parents=True, exist_ok=True)
    # Publish an entire binary atomically even if another suite builds it too.
    fd, temporary = tempfile.mkstemp(prefix=name + '-', dir=work)
    os.close(fd)
    try:
        subprocess.run([os.environ.get('GO', 'go'), 'build', '-o', temporary, './cmd/' + name],
                       cwd=ROOT / 'tests' / 'fakes', check=True)
        target = work / name
        os.replace(temporary, target)
        return target
    finally:
        pathlib.Path(temporary).unlink(missing_ok=True)


class Fake:
    def __init__(self, name, port=0):
        executable = binary(name)
        self.stderr = tempfile.TemporaryFile(mode='w+')
        try:
            self.process = subprocess.Popen([str(executable), '-listen', f'127.0.0.1:{port}'],
                                            stdout=subprocess.PIPE, stderr=self.stderr, text=True)
        except Exception:
            self.stderr.close()
            raise
        try:
            if not select.select([self.process.stdout], [], [], 10)[0]:
                raise RuntimeError(f'{name} did not report its bound address')
            line = self.process.stdout.readline()
            self.url = json.loads(line)['url']
        except Exception as error:
            self.stderr.seek(0)
            diagnostic = self.stderr.read()
            self.close()
            raise RuntimeError(f'{name} startup failed: {error}; {diagnostic}') from error
        atexit.register(self.close)

    def close(self):
        if self.stderr.closed:
            return
        atexit.unregister(self.close)
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=6)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait()
        self.process.stdout.close()
        self.stderr.close()

    def __enter__(self):
        return self

    def __exit__(self, *args):
        self.close()
