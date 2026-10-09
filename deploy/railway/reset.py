"""Replace only exclusively owned volumes, preserving deployed artifacts and settings."""
import inspect
import json
import os
from pathlib import Path
import shlex
import time

from deploy.railway.infrastructure import Railway
from deploy.reset_support import Checkpoint, ResetFailure, ResetTimeout, terminate_workflows, wait_until

PROJECT = '''query($id:String!){project(id:$id){id
 environments(first:100){edges{node{id projectId canAccess deletedAt}} pageInfo{hasNextPage}}
 services(first:100){edges{node{id name}} pageInfo{hasNextPage}}}}'''
VOLUMES = '''query($environmentId:String!,$projectId:String!){environment(id:$environmentId,projectId:$projectId){id projectId
 volumeInstances(first:500){edges{node{volumeId environmentId serviceId mountPath sizeMB region state isPendingDeletion deletedAt}}
 pageInfo{hasNextPage}}}}'''
INSTANCE = '''query($serviceId:String!,$environmentId:String!){serviceInstance(serviceId:$serviceId,environmentId:$environmentId){
 serviceId environmentId latestDeployment{id status} activeDeployments{id status deploymentStopped canRedeploy instances{id status}}}}'''
DEPLOYMENT = '''query($id:String!){deployment(id:$id){id status deploymentStopped instances{id status}}}'''
STOP = 'mutation($id:String!){deploymentStop(id:$id)}'
REMOVE = 'mutation($id:String!){deploymentRemove(id:$id)}'
REDEPLOY = 'mutation($id:String!){deploymentRedeploy(id:$id){id status}}'
CREATE = 'mutation($input:VolumeCreateInput!){volumeCreate(input:$input){id projectId}}'
ATTACH = '''mutation($volumeId:String!,$environmentId:String!,$input:VolumeInstanceUpdateInput!){
 volumeInstanceUpdate(volumeId:$volumeId,environmentId:$environmentId,input:$input)}'''
DELETE = 'mutation($volumeId:String!){volumeDelete(volumeId:$volumeId)}'
MOUNTS = {'postgres': '/data', 'temporal': '/data', 'seaweed': '/data', 'weaviate': '/var/lib/weaviate'}
WRITERS = ('autoscaler', 'web', 'api', 'worker', 'worker-bulk')
STARTING = ('QUEUED', 'WAITING', 'INITIALIZING', 'BUILDING', 'DEPLOYING')


def active_deployment(deployment):
    return (not deployment['deploymentStopped'] or deployment['status'] in STARTING
            or any(i['status'] in ('RUNNING', 'RESTARTING') for i in deployment['instances']))


class RailwayReset:
    def __init__(self, spec):
        self.spec = spec
        self.adapter = Railway(spec['project'], spec['environment'])
        self.volumes = {}
        self.deployments = {}
        self.instance_ids = {}

    def api(self, query, variables, object_result=False):
        return self.adapter.api(query, variables, object_result=object_result)

    def all_volumes(self):
        result = []
        for environment in self.environments:
            response = self.api(VOLUMES, {'environmentId': environment['id'], 'projectId': self.spec['project']})['environment']
            if not response or response['id'] != environment['id'] or response['projectId'] != self.spec['project']:
                raise RuntimeError('cannot verify volume ownership across project environments')
            volumes = response['volumeInstances']
            if volumes['pageInfo']['hasNextPage']:
                raise RuntimeError('volume enumeration is incomplete')
            for edge in volumes['edges']:
                volume = edge['node']
                if volume['environmentId'] != environment['id']:
                    raise RuntimeError('unexpected volume environment')
                result.append(volume)
        return result

    def service(self, role):
        identifier = self.adapter.targets[role]
        value = self.api(INSTANCE, {'serviceId': identifier, 'environmentId': self.spec['environment']})['serviceInstance']
        if not value or value['serviceId'] != identifier or value['environmentId'] != self.spec['environment']:
            raise RuntimeError('unexpected service instance')
        return value

    def ssh(self, role, command, stdin=None):
        return self.adapter.run(['ssh', *self.adapter.flags(role), '--deployment-instance', self.instance_ids[role],
                                 command], stdin=stdin)

    def storage_inventory(self):
        # Execute the same credential-safe inventory inside the existing API image.
        # Credentials stay on the provider, read from its runtime configuration.
        from deploy import reset_storage
        code = ('import datetime, hashlib, hmac, urllib.parse, urllib.request, xml.etree.ElementTree as ET, json\n'
                + inspect.getsource(reset_storage.s3_request) + '\n' + inspect.getsource(reset_storage.inventory)
                + '\nprint(json.dumps(inventory(json.load(open("/tmp/quivr-runtime.json"))["s3"])))')
        return json.loads(self.ssh('api', 'python3 -c ' + shlex.quote(code)))

    def index_inventory(self):
        from deploy.reset_support import vector_objects
        code = ('import json, urllib.request\n' + inspect.getsource(vector_objects)
                + '\nc=json.load(open("/tmp/quivr-runtime.json")); '
                'r=urllib.request.urlopen(c["weaviate_url"]+"/v1/objects?limit=1",timeout=10); '
                'print(vector_objects(json.load(r)))')
        return int(self.ssh('api', 'python3 -c ' + shlex.quote(code)).strip())

    def preview(self):
        # CLI project tokens only see one environment. Their connection pagination
        # cannot prove ownership of a project-wide volumeDelete target.
        if 'RAILWAY_TOKEN' in os.environ or not os.environ.get('RAILWAY_API_TOKEN'):
            raise ValueError('reset requires an account/workspace RAILWAY_API_TOKEN and no project-scoped RAILWAY_TOKEN')
        project = self.api(PROJECT, {'id': self.spec['project']})['project']
        if project['id'] != self.spec['project'] or any(project[k]['pageInfo']['hasNextPage'] for k in ('environments', 'services')):
            raise RuntimeError('project ownership lookup is incomplete')
        self.environments = [e['node'] for e in project['environments']['edges']]
        if any(e['projectId'] != self.spec['project'] or not e['canAccess'] or e['deletedAt'] for e in self.environments):
            raise RuntimeError('reset needs read access to every project environment to prove volume isolation')
        if self.spec['environment'] not in {e['id'] for e in self.environments}:
            raise RuntimeError('environment is outside the declared project')
        services = [e['node'] for e in project['services']['edges']]
        selectors = self.spec.get('services') or {s['name']: s['name'] for s in services}
        allowed = set(MOUNTS) | set(WRITERS) | {'tei'}
        if set(selectors) - allowed or not (set(MOUNTS) | {'api', 'worker'}) <= set(selectors):
            raise ValueError('declare every service using the supported installation roles')
        # An omitted service could keep writing or autoscaling while volumes change.
        for role, selector in selectors.items():
            matches = [s['id'] for s in services if selector in (s['id'], s['name'])]
            if len(matches) != 1:
                raise RuntimeError('service selector is missing or ambiguous')
            self.adapter.targets[role] = matches[0]
        if len(set(self.adapter.targets.values())) != len(self.adapter.targets) or set(self.adapter.targets.values()) != {s['id'] for s in services}:
            raise RuntimeError('reset requires a dedicated project with every service accounted for')
        volumes = self.all_volumes()
        for role, mount in MOUNTS.items():
            matches = [v for v in volumes if v['serviceId'] == self.adapter.targets[role]
                       and v['environmentId'] == self.spec['environment']]
            if len(matches) != 1 or matches[0]['mountPath'] != mount or matches[0]['state'] != 'READY' or matches[0]['isPendingDeletion'] or matches[0]['deletedAt']:
                raise RuntimeError('expected dedicated data mount is not ready or unambiguous')
            volume = matches[0]
            if len([v for v in volumes if v['volumeId'] == volume['volumeId']]) != 1:
                raise RuntimeError('data volume is shared across environments; reset refused')
            self.volumes[role] = volume
        for role in selectors:
            instance = self.service(role)
            active = [d for d in instance['activeDeployments'] if not d['deploymentStopped']]
            latest = instance.get('latestDeployment')
            if len(active) != 1 or active[0]['status'] != 'SUCCESS' or not active[0]['canRedeploy'] or not latest or latest['id'] != active[0]['id'] or latest['status'] != 'SUCCESS':
                raise RuntimeError('reset requires one stable redeployable artifact for each service')
            running = [i['id'] for i in active[0]['instances'] if i['status'] == 'RUNNING']
            if not running:
                raise RuntimeError('service has no running instance for reset preflight')
            self.deployments[role] = active[0]['id']
            self.instance_ids[role] = running[0]
        self.storage = self.storage_inventory()
        self.index_count = self.index_inventory()
        return {'project': self.spec['project'], 'environment': self.spec['environment'],
                'volumes': {k: v['volumeId'] for k, v in self.volumes.items()},
                'volume_sizes': {k: {'old_sizeMB': v['sizeMB'], 'replacement_sizeMB': None}
                                 for k, v in self.volumes.items()},
                'replacement_capacity': 'unknown until created; Railway replacements use provider defaults; '
                                        'grow them in the Railway dashboard before importing',
                'writers': [k for k in WRITERS if k in selectors], 'object_store': self.storage,
                'vector_objects': self.index_count, 'preserves': 'service settings, replica counts, credentials and exact deployed artifacts',
                'deletion': 'replaced volumes are detached; provider deletion retention may apply'}

    def stop(self, role):
        identifier = self.deployments[role]
        self.api(STOP, {'id': identifier})
        def deployment():
            return self.api(DEPLOYMENT, {'id': identifier})['deployment']
        def quiescent(value):
            return not any(i['status'] in ('RUNNING', 'RESTARTING') for i in value['instances'])
        try:
            wait_until(deployment, lambda d: d['deploymentStopped'] and quiescent(d), seconds=30, interval=1)
        except ResetTimeout:
            # Railway can acknowledge stop while keeping instances running.
            # Removed deployments remain redeployable through the recorded ID.
            self.api(REMOVE, {'id': identifier})
            wait_until(deployment, quiescent, interval=1)

    def stopped(self, roles):
        for role in roles:
            active = [d for d in self.service(role)['activeDeployments'] if active_deployment(d)]
            if active:
                # Never continue deleting while an automatic or concurrent deployment runs.
                for deployment in active:
                    self.api(STOP, {'id': deployment['id']})
                raise RuntimeError('unexpected deployment started during reset; writers stopped, inspect privately')

    def settle_attachment(self, role, baseline, roles, started):
        # Railway may surface a second deployment several seconds after the first.
        # Only new IDs on this service during this attachment window are expected.
        observe_until = started + 10
        deadline = started + 180
        other_roles = [r for r in roles if r != role]
        removed = set()
        def poll():
            self.stopped(other_roles)
            observing = time.monotonic() <= observe_until
            for deployment in self.service(role)['activeDeployments']:
                identifier = deployment['id']
                if identifier in removed or not active_deployment(deployment):
                    continue
                if identifier in baseline or not observing:
                    self.stopped([role])
                    raise RuntimeError('unexpected deployment observed during attachment')
                self.api(REMOVE, {'id': identifier})
                removed.add(identifier)
            deployments = [self.api(DEPLOYMENT, {'id': identifier})['deployment'] for identifier in removed]
            return time.monotonic() >= observe_until and all(d['status'] not in STARTING
                and not any(i['status'] in ('RUNNING', 'RESTARTING') for i in d['instances']) for d in deployments)
        wait_until(poll, lambda ready: ready, seconds=max(0, deadline - time.monotonic()), interval=1)
        self.stopped(roles)

    def restore(self, role, checkpoint):
        value = self.api(REDEPLOY, {'id': self.deployments[role]}, object_result=True)['deploymentRedeploy']
        if not value or not value.get('id'):
            raise RuntimeError('provider did not return the restored deployment')
        checkpoint.data.setdefault('restored', {})[role] = value['id']
        checkpoint.save('restoring')
        deployment = wait_until(lambda: self.api(DEPLOYMENT, {'id': value['id']})['deployment'],
                               lambda d: d['status'] in ('SUCCESS', 'FAILED', 'CRASHED'), seconds=600, interval=1)
        if deployment['status'] != 'SUCCESS':
            raise RuntimeError('restored deployment failed')
        running = [i['id'] for i in deployment['instances'] if i['status'] == 'RUNNING']
        if not running:
            raise RuntimeError('restored service has no running instance')
        self.instance_ids[role] = running[0]

    def reset(self):
        checkpoint = Checkpoint(self.checkpoint_path, self.spec)
        writers = [k for k in WRITERS if k in self.deployments]
        dependencies = list(MOUNTS) + (['tei'] if 'tei' in self.deployments else [])
        roles = writers + dependencies
        volume_sizes = {}
        role = 'installation'
        try:
            checkpoint.save('stopping-writers', deployments=self.deployments, volumes=self.volumes)
            for role in writers:
                self.stop(role)
            role = 'temporal'
            workflows = terminate_workflows(lambda args: self.ssh('temporal', shlex.join(['temporal', *args])), interval=1)
            checkpoint.save('stopping-dependencies', terminated_workflows=workflows)
            for role in dependencies:
                self.stop(role)
            role = 'writer-quiescence'
            self.stopped(roles)
            # Revalidate project-wide ownership immediately before deleting anything.
            for role, old in self.volumes.items():
                current = self.all_volumes()
                if [v for v in current if v['volumeId'] == old['volumeId']] != [old]:
                    raise RuntimeError('volume ownership changed since preflight')
                checkpoint.save('creating-volume', pending_role=role)
                new = self.api(CREATE, {'input': {'projectId': self.spec['project'],
                    'environmentId': self.spec['environment'], 'mountPath': old['mountPath'],
                    'region': old['region']}}, object_result=True)['volumeCreate']
                if not new or not new.get('id') or new['projectId'] != self.spec['project']:
                    raise RuntimeError('unexpected replacement volume')
                checkpoint.data.setdefault('replacements', {})[role] = new['id']
                checkpoint.save('attaching-volume')
                def replacement():
                    return [v for v in self.all_volumes() if v['volumeId'] == new['id']]
                wait_until(replacement, lambda rows: len(rows) == 1 and rows[0]['state'] == 'READY', interval=1)
                variables = {'environmentId': self.spec['environment'], 'input': {'serviceId': None}, 'volumeId': old['volumeId']}
                self.api(ATTACH, variables)
                self.stopped(roles)
                baseline = {d['id'] for d in self.service(role)['activeDeployments']} | {self.deployments[role]}
                variables.update(volumeId=new['id'], input={'serviceId': self.adapter.targets[role],
                                                          'mountPath': old['mountPath']})
                started = time.monotonic()
                self.api(ATTACH, variables)
                attached = wait_until(replacement, lambda rows: len(rows) == 1 and rows[0]['serviceId'] == self.adapter.targets[role]
                           and rows[0]['mountPath'] == old['mountPath'] and rows[0]['environmentId'] == self.spec['environment'], interval=1)
                volume_sizes[role] = {'old_sizeMB': old['sizeMB'], 'replacement_sizeMB': attached[0]['sizeMB'],
                                      'shrunk': attached[0]['sizeMB'] < old['sizeMB']}
                checkpoint.save('attaching-volume', volume_sizes=volume_sizes)
                self.settle_attachment(role, baseline, roles, started)
                def detached():
                    rows = [v for v in self.all_volumes() if v['volumeId'] == old['volumeId']]
                    if len(rows) != 1:
                        raise RuntimeError('old volume is missing or shared; deletion refused')
                    row = rows[0]
                    expected = {**old, 'serviceId': row['serviceId']}
                    if row != expected or row['serviceId'] not in (None, self.adapter.targets[role]):
                        raise RuntimeError('old volume ownership drift; deletion refused')
                    return row['serviceId'] is None
                # An acknowledgement is not proof that detach is visible. Each
                # poll re-enumerates every environment and fails on sharing/drift.
                wait_until(detached, lambda ready: ready, interval=1)
                # Detach visibility can take time. Repeat both terminal guards
                # after that wait, immediately before the destructive request.
                self.stopped(roles)
                if not detached():
                    raise RuntimeError('old attachment returned after detach; deletion refused')
                checkpoint.save('requesting-deletion')
                self.api(DELETE, {'volumeId': old['volumeId']})
            checkpoint.save('restoring')
            for role in dependencies:
                self.restore(role, checkpoint)
            role = 'api'
            self.restore('api', checkpoint)
            role = 'replacement-stores'
            storage = self.storage_inventory()
            if storage['objects_at_least'] or storage['truncated'] or self.index_inventory():
                raise RuntimeError('replacement stores are not empty')
            query = 'SELECT count(*) FROM corpora'
            role = 'postgres'
            count = self.ssh('postgres', 'sh -c ' + shlex.quote(
                'psql -XAt -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c ' + shlex.quote(query)))
            if count.strip() != '0':
                raise RuntimeError('replacement database is not empty')
            for role in ('worker', 'worker-bulk', 'web', 'autoscaler'):
                if role in self.deployments:
                    self.restore(role, checkpoint)
            checkpoint.save('complete')
            return {'replaced_volumes': {k: v['volumeId'] for k, v in self.volumes.items()},
                    'volume_sizes': volume_sizes,
                    'warnings': [f'{role}: replacement volume capacity shrank; grow it in the Railway dashboard before importing'
                                 for role, sizes in volume_sizes.items() if sizes['shrunk']],
                    'old_volumes': 'detached; deletion acknowledged, provider retention may apply',
                    'deleted_vector_objects': self.index_count, 'deleted_blob_objects_at_least': self.storage['objects_at_least'],
                    'terminated_workflows': workflows, 'schema': 'fresh startup migrations applied', 'writers': 'restarted'}
        except Exception:
            # A failed restore can leave an API or worker running. Stop any newly
            # restored writers before returning; preserve all checkpoints for recovery.
            for restored_role, identifier in checkpoint.data.get('restored', {}).items():
                if restored_role in WRITERS:
                    try:
                        self.api(STOP, {'id': identifier})
                    except Exception:
                        pass
            raise ResetFailure(checkpoint.data['phase'], role) from None
        finally:
            checkpoint.close()
