"""Reset the existing local Stack without changing its credentials or profile."""
import json
import subprocess
from pathlib import Path
import shlex
import sys
import urllib.request

from deploy.compose.infrastructure import Compose
from deploy.reset_support import Checkpoint, terminate_workflows

ROOT = Path(__file__).resolve().parents[2]
VOLUMES = {'postgres': ('data', '/var/lib/postgresql/data'),
           'temporal': ('temporal-data', '/home/temporal'),
           'seaweed': ('seaweed-data', '/data'),
           'weaviate': ('weaviate-data', '/var/lib/weaviate')}


def check_tracked_processes(state, directory, prune=False):
    """Saved PIDs can be reused. Only signal helpers logging to this Stack."""
    sys.path.insert(0, str(ROOT / 'scripts'))
    from local import alive
    for key in list(state):
        if not key.endswith('_pid'):
            continue
        pid = state[key]
        if not isinstance(pid, int) or pid <= 0:
            raise RuntimeError('invalid tracked process identity')
        if not alive(pid):
            if prune:
                state.pop(key)
            continue
        try:
            if sys.platform == 'darwin':
                response = subprocess.run(['lsof', '-a', '-p', str(pid), '-d', '1', '-Fn'],
                                          capture_output=True, text=True, timeout=5, check=True)
                names = [line[1:] for line in response.stdout.splitlines() if line.startswith('n')]
                if len(names) != 1:
                    raise RuntimeError('ambiguous process output identity')
                output = Path(names[0]).resolve()
            else:
                output = Path(f'/proc/{pid}/fd/1').resolve(strict=True)
            output.relative_to(directory.resolve())
        except (OSError, ValueError, subprocess.SubprocessError):
            raise RuntimeError('tracked PID is outside the selected Stack; refuse to signal it') from None


class ComposeReset:
    def __init__(self, spec):
        self.spec = spec
        self.adapter = Compose(spec['project'])
        self.directory = ROOT / '.scratch' / spec['project']
        self.volumes = {}

    def preview(self):
        if sys.version_info < (3, 12):
            raise ValueError('Compose reset requires Python 3.12+ for the installation plugin SDK')
        if self.spec.get('services'):
            raise ValueError('Compose reset uses the checked-in local Stack service names')
        statefile = self.directory / 'state.json'
        if not statefile.is_file() or statefile.is_symlink() or statefile.stat().st_mode & 0o077:
            raise RuntimeError('selected Stack private state is missing or unsafe')
        self.state = json.loads(statefile.read_text())
        check_tracked_processes(self.state, self.directory)
        self.config = json.loads((self.directory / 'config.json').read_text())
        for role, (logical, destination) in VOLUMES.items():
            if not self.adapter.configured(role):
                raise RuntimeError('reset requires the selected Stack dependencies running')
            container = self.adapter.containers[role]
            details = json.loads(self.adapter.run(['inspect', container]))[0]
            mounts = [mount for mount in details.get('Mounts', [])
                      if mount.get('Type') == 'volume' and mount.get('Destination') == destination]
            if len(mounts) != 1:
                raise RuntimeError('data mount is missing or ambiguous')
            name = mounts[0]['Name']
            volume = json.loads(self.adapter.run(['volume', 'inspect', name]))[0]
            labels = volume.get('Labels') or {}
            if labels.get('com.docker.compose.project') != self.spec['project'] or labels.get('com.docker.compose.volume') != logical:
                raise RuntimeError('data volume is outside the selected Compose declaration')
            attached = self.adapter.run(['ps', '-aq', '--filter', 'volume=' + name]).split()
            if attached != [container]:
                raise RuntimeError('data volume is attached outside the selected service')
            self.volumes[role] = name
        # An archive used as a source must live outside the storage being reset.
        from deploy.reset_storage import inventory
        self.storage = inventory(self.config['s3'])
        with urllib.request.urlopen(self.config['weaviate_url'] + '/v1/objects?limit=1', timeout=10) as response:
            self.index_count = json.load(response)['totalResults']
        return {'volumes': self.volumes, 'object_store': self.storage,
                'vector_objects': self.index_count, 'workflows': 'terminate running workflows in default namespace',
                'writers': 'tracked Stack API and worker processes', 'preserves': 'credentials, infrastructure profile, model cache'}

    def reset(self):
        checkpoint = Checkpoint(self.directory / 'reset-state.json', self.spec)
        stack = None
        try:
            checkpoint.save('stopping-writers', volumes=self.volumes)
            sys.path.insert(0, str(ROOT / 'scripts'))
            from local import Stack
            stack = Stack(self.spec['project'])
            check_tracked_processes(stack.state, self.directory, prune=True)
            stack.save()
            stack.stop_processes()
            postgres = self.adapter.containers['postgres']
            query = "SELECT count(*) FROM pg_stat_activity WHERE backend_type='client backend' AND pid<>pg_backend_pid()"
            connections = self.adapter.run(['exec', postgres, 'sh', '-c',
                'psql -XAt -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c ' + shlex.quote(query)])
            if connections.strip() != '0':
                raise RuntimeError('untracked database clients remain; reset refused with writers stopped')
            workflows = terminate_workflows(lambda args: self.adapter.run(
                ['exec', self.adapter.containers['temporal'], 'temporal', *args]))
            checkpoint.save('deleting-volumes', terminated_workflows=workflows)
            check_tracked_processes(stack.state, self.directory, prune=True)
            stack.save()
            stack.down(True)
            for name in self.volumes.values():
                # A successful inspect proves deletion did not complete.
                remaining = self.adapter.run(['volume', 'ls', '-q', '--filter', 'name=^' + name + '$']).split()
                if name in remaining:
                    raise RuntimeError('selected volume remains after reset')
            checkpoint.save('initializing')
            stack.up()
            # Bootstrap may recreate index classes; documents and blob objects must be absent.
            from deploy.reset_storage import inventory
            config = json.loads((stack.directory / 'config.json').read_text())
            storage = inventory(config['s3'])
            if storage['objects_at_least'] != 0 or storage['truncated']:
                raise RuntimeError('new object store is not empty')
            with urllib.request.urlopen(config['weaviate_url'] + '/v1/objects?limit=1', timeout=10) as response:
                if json.load(response)['totalResults'] != 0:
                    raise RuntimeError('new vector store is not empty')
            postgres = stack.compose('ps', '-q', 'postgres', capture_output=True, text=True).stdout.strip()
            counts = self.adapter.run(['exec', postgres, 'sh', '-c',
                'psql -XAt -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c '
                + shlex.quote('SELECT (SELECT count(*) FROM corpora)+(SELECT count(*) FROM records)')])
            if counts.strip() != '0':
                raise RuntimeError('replacement database is not empty')
            checkpoint.save('complete')
            return {'deleted_volumes': list(self.volumes.values()), 'deleted_vector_objects': self.index_count,
                    'deleted_blob_objects_at_least': self.storage['objects_at_least'],
                    'terminated_workflows': workflows, 'schema': 'fresh migrations applied', 'writers': 'restarted'}
        except Exception:
            if stack is not None:
                stack.stop_processes()
            raise
        finally:
            checkpoint.close()
