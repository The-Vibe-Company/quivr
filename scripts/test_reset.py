"""Reset safety at the operator boundary; the real Compose proof lives in lifecycle.py."""
import json
import contextlib
import io
import os
from unittest.mock import patch
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class ResetConfirmation(unittest.TestCase):
    # Detects accidental removal of either confirmation before adapter selection.
    # No existing harness reset checks this new external command's guard.
    def test_both_confirmation_inputs_are_required_before_adapter_work(self):
        with tempfile.TemporaryDirectory() as directory:
            declaration = Path(directory) / 'installation.json'
            declaration.write_text(json.dumps({'version': 1, 'deployment': 'isolated-install',
                                                'platform': 'compose', 'project': 'isolated-install'}))
            for flags in (['--confirm'], ['--deployment-name', 'isolated-install'],
                          ['--confirm', '--deployment-name', 'wrong-install']):
                with self.subTest(flags=flags):
                    result = subprocess.run([sys.executable, str(ROOT / 'deploy/reset.py'), '-f', str(declaration),
                                             *flags], capture_output=True, text=True)
                    self.assertEqual(result.returncode, 2, result.stderr)
                    self.assertIn('confirmation requires --confirm and the typed deployment name', result.stderr)
                    self.assertNotIn('Docker', result.stderr)


class RailwayIsolation(unittest.TestCase):
    # Provider transport coverage: Compose cannot observe project-wide volumes.
    # A plausible regression is filtering volume ownership to the target environment.
    def test_shared_or_unreadable_volumes_refuse_before_any_mutation(self):
        from deploy.railway.reset import RailwayReset
        from deploy.railway.infrastructure import Railway
        for inaccessible, shared, truncated in ((True, False, False), (False, True, False), (False, False, True)):
            with self.subTest(inaccessible=inaccessible, shared=shared, truncated=truncated):
                calls = []
                def transport(args, stdin=None):
                    query = args[1]
                    variables = json.loads(args[3])
                    calls.append(query)
                    if query.startswith('mutation'):
                        self.fail('unsafe reset issued a mutation')
                    if 'project(id:' in query:
                        return json.dumps({'data': {'project': {'id': 'project',
                            'environments': {'edges': [{'node': {'id': e, 'projectId': 'project',
                                'canAccess': e == 'target' or not inaccessible, 'deletedAt': None}}
                                for e in ('target', 'other')], 'pageInfo': {'hasNextPage': False}},
                            'services': {'edges': [{'node': {'id': r, 'name': r}}
                                for r in ('api', 'worker', 'postgres', 'temporal', 'seaweed', 'weaviate')],
                                'pageInfo': {'hasNextPage': False}}}}})
                    environment = variables['environmentId']
                    mounts = [{'volumeId': 'database-volume', 'environmentId': environment,
                        'serviceId': 'postgres', 'mountPath': '/data', 'region': 'us-west',
                        'state': 'READY', 'isPendingDeletion': False, 'deletedAt': None}]
                    if environment == 'other' and not shared:
                        mounts = []
                    return json.dumps({'data': {'environment': {'id': environment, 'projectId': 'project',
                        'volumeInstances': {'edges': [{'node': v} for v in mounts],
                        'pageInfo': {'hasNextPage': truncated}}}}})
                reset = RailwayReset({'deployment': 'isolated-install', 'project': 'project', 'environment': 'target'})
                reset.adapter = Railway('project', 'target', run=transport)
                with patch.dict(os.environ, {'RAILWAY_API_TOKEN': 'fixture-account-not-real'}):
                    os.environ.pop('RAILWAY_TOKEN', None)
                    with self.assertRaisesRegex(RuntimeError, 'read access|shared across environments|enumeration is incomplete'):
                        reset.preview()
                self.assertTrue(calls)

    # Owns Railway's reset lifecycle when stop acknowledgements are ineffective.
    # Existing preflight refusal tests cannot reach shutdown or artifact restore.
    def test_reset_removes_unresponsive_deployment_and_restores_recorded_artifacts(self):
        from deploy.reset import main
        from deploy.railway.infrastructure import Railway
        roles = ('autoscaler', 'web', 'api', 'worker', 'worker-bulk',
                 'postgres', 'temporal', 'seaweed', 'weaviate')
        for failure in (None, 'stop', 'remove', 'quiescence', 'restore'):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as directory:
                deployments = {role + '-original': {'id': role + '-original', 'status': 'SUCCESS',
                    'deploymentStopped': False, 'canRedeploy': True,
                    'instances': [{'id': role + '-instance', 'status': 'RUNNING'}]} for role in roles}
                current = {role: role + '-original' for role in roles}
                volumes = [{'volumeId': role + '-volume', 'environmentId': 'target', 'serviceId': role,
                    'mountPath': '/var/lib/weaviate' if role == 'weaviate' else '/data',
                    'region': 'us-west', 'state': 'READY', 'isPendingDeletion': False, 'deletedAt': None}
                    for role in ('postgres', 'temporal', 'seaweed', 'weaviate')]
                removed, redeployed, deleted, delays = [], {}, [], []
                clock = [0]
                workflow_reads = [0]
                def sleep(seconds):
                    delays.append(seconds)
                    clock[0] += seconds
                def transport(args, stdin=None):
                    if args[0] == 'ssh':
                        command = args[-1]
                        if 'print(json.dumps(inventory(' in command:
                            return '{"objects_at_least":0,"truncated":false}'
                        if 'workflow count' in command:
                            workflow_reads[0] += 1
                            return '{"count":"1"}' if workflow_reads[0] <= 2 else '{}'
                        return '0'
                    query, variables = args[1], json.loads(args[3])
                    if 'project(id:' in query:
                        data = {'project': {'id': 'project',
                            'environments': {'edges': [{'node': {'id': 'target', 'projectId': 'project',
                                'canAccess': True, 'deletedAt': None}}], 'pageInfo': {'hasNextPage': False}},
                            'services': {'edges': [{'node': {'id': r, 'name': r}} for r in roles],
                                'pageInfo': {'hasNextPage': False}}}}
                    elif 'volumeInstances(' in query:
                        data = {'environment': {'id': 'target', 'projectId': 'project',
                            'volumeInstances': {'edges': [{'node': v.copy()} for v in volumes],
                                'pageInfo': {'hasNextPage': False}}}}
                    elif 'serviceInstance(' in query:
                        role = variables['serviceId']
                        deployment = deployments[current[role]]
                        active = [] if deployment['deploymentStopped'] or deployment['status'] == 'REMOVED' else [deployment]
                        data = {'serviceInstance': {'serviceId': role, 'environmentId': 'target',
                            'latestDeployment': deployment, 'activeDeployments': active}}
                    elif 'deploymentStop(' in query:
                        identifier = variables['id']
                        if identifier == 'web-original':
                            if failure == 'stop':
                                raise RuntimeError('provider-secret-payload')
                        else:
                            deployments[identifier].update(deploymentStopped=True, instances=[])
                        data = {'deploymentStop': True}
                    elif 'deploymentRemove(' in query:
                        identifier = variables['id']
                        removed.append((identifier, clock[0]))
                        if failure == 'remove':
                            raise RuntimeError('provider-secret-payload')
                        deployments[identifier].update(status='REMOVED', instances=[{
                            'id': 'web-instance', 'status': 'RESTARTING' if failure == 'quiescence' else 'REMOVED'}])
                        data = {'deploymentRemove': True}
                    elif 'deploymentRedeploy(' in query:
                        identifier = variables['id']
                        role = identifier.removesuffix('-original')
                        if role == 'web' and failure == 'restore':
                            raise RuntimeError('provider-secret-payload')
                        redeployed[role] = identifier
                        current[role] = role + '-restored'
                        deployments[current[role]] = {'id': current[role], 'status': 'SUCCESS',
                            'deploymentStopped': False, 'canRedeploy': True,
                            'instances': [{'id': role + '-new-instance', 'status': 'RUNNING'}]}
                        data = {'deploymentRedeploy': deployments[current[role]]}
                    elif 'deployment(id:' in query:
                        data = {'deployment': deployments[variables['id']]}
                    elif 'volumeCreate(' in query:
                        self.assertFalse(any(i['status'] in ('RUNNING', 'RESTARTING')
                            for d in deployments.values() for i in d['instances']), 'volume changed before shutdown')
                        volume = {'volumeId': 'replacement-' + str(len(deleted)), 'serviceId': None,
                            'state': 'READY', 'isPendingDeletion': False, 'deletedAt': None,
                            **{k: v for k, v in variables['input'].items() if k != 'projectId'}}
                        volumes.append(volume)
                        data = {'volumeCreate': {'id': volume['volumeId'], 'projectId': 'project'}}
                    elif 'volumeInstanceUpdate(' in query:
                        volume = next(v for v in volumes if v['volumeId'] == variables['volumeId'])
                        volume.update(variables['input'])
                        data = {'volumeInstanceUpdate': True}
                    elif 'volumeDelete(' in query:
                        deleted.append(variables['volumeId'])
                        volumes[:] = [v for v in volumes if v['volumeId'] != variables['volumeId']]
                        data = {'volumeDelete': True}
                    else:
                        self.fail('unexpected Railway request: ' + query)
                    return json.dumps({'data': data})
                declaration = Path(directory) / 'installation.json'
                declaration.write_text(json.dumps({'version': 1, 'deployment': 'isolated-install',
                    'platform': 'railway', 'project': 'project', 'environment': 'target', 'dedicated': True}))
                output, errors = io.StringIO(), io.StringIO()
                with patch('deploy.railway.reset.Railway', side_effect=lambda p, e: Railway(p, e, run=transport)), \
                     patch('deploy.reset_support.time.monotonic', side_effect=lambda: clock[0]), \
                     patch('deploy.reset_support.time.sleep', side_effect=sleep), \
                     patch.dict(os.environ, {'RAILWAY_API_TOKEN': 'fixture-account-not-real'}), \
                     contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
                    os.environ.pop('RAILWAY_TOKEN', None)
                    result = main(['-f', str(declaration), '--confirm', '--deployment-name', 'isolated-install'])
                state = json.loads(Path(str(declaration) + '.reset-state.json').read_text())
                if failure is None:
                    self.assertEqual(result, 0, errors.getvalue())
                    self.assertEqual(state['phase'], 'complete')
                    self.assertEqual(redeployed, {role: role + '-original' for role in roles})
                    self.assertEqual(len(deleted), 4)
                    self.assertTrue(all(deployments[current[r]]['instances'][0]['status'] == 'RUNNING' for r in roles))
                else:
                    self.assertEqual(result, 1)
                    phase = 'restoring' if failure == 'restore' else 'stopping-writers'
                    self.assertEqual(state['phase'], phase)
                    self.assertIn(phase, errors.getvalue())
                    self.assertIn('web', errors.getvalue())
                    self.assertNotIn('provider-secret-payload', output.getvalue() + errors.getvalue())
                    if failure != 'restore':
                        self.assertEqual(deleted, [])
                        self.assertEqual(redeployed, {})
                if failure == 'stop':
                    self.assertEqual(removed, [], 'API errors must not trigger removal')
                else:
                    self.assertEqual(removed, [('web-original', 30)])
                    self.assertTrue(delays)
                    self.assertTrue(all(delay >= 1 for delay in delays), delays)


class TemporalTransport(unittest.TestCase):
    # Owns the pinned CLI's protobuf JSON representation, independently of storage reset.
    def test_count_accepts_decimal_string_before_terminating(self):
        from deploy.reset_support import terminate_workflows
        responses = iter(['{"count":"2"}', '', '{}'])
        calls = []
        def run(args):
            calls.append(args)
            return next(responses)
        self.assertEqual(terminate_workflows(run), 2)
        self.assertEqual(calls[1][:2], ['workflow', 'terminate'])
        self.assertIn('--yes', calls[1])


class WeaviateTransport(unittest.TestCase):
    # The pinned provider omits totalResults for an empty index. This literal
    # wire response owns successful post-reset readback, including omissions.
    def test_empty_count_is_omitted_only_for_an_empty_object_list(self):
        from deploy.reset_support import vector_objects
        self.assertEqual(vector_objects({'objects': []}), 0)
        self.assertEqual(vector_objects({'objects': [{'id': 'fixture'}], 'totalResults': 1}), 1)
        for response in ({}, {'objects': [{'id': 'fixture'}]}, {'objects': [], 'totalResults': True}):
            with self.assertRaisesRegex(RuntimeError, 'Weaviate'):
                vector_objects(response)


class RailwayAuthScope(unittest.TestCase):
    def test_environment_token_cannot_prove_project_volume_isolation(self):
        from deploy.railway.reset import RailwayReset
        reset = RailwayReset({'deployment': 'isolated-install', 'project': 'project', 'environment': 'target'})
        reset.adapter.run = lambda *args, **kwargs: self.fail('project discovery reached with scoped credentials')
        with patch.dict(os.environ, {'RAILWAY_TOKEN': 'fixture-project-not-real', 'RAILWAY_API_TOKEN': 'fixture-account-not-real'}):
            with self.assertRaisesRegex(ValueError, 'account/workspace'):
                reset.preview()


class ComposeProcessScope(unittest.TestCase):
    # A saved helper PID can be reused between local Stack runs. The existing
    # harness stop helpers do not own this new reset boundary's stricter guard.
    def test_live_helper_outside_stack_is_refused_without_signaling(self):
        from deploy.compose.reset import check_tracked_processes
        with tempfile.TemporaryDirectory() as directory, tempfile.TemporaryFile() as outside:
            stack_directory = Path(directory)
            for owned in (False, True):
                with (stack_directory / 'helper.log').open('w') as inside:
                    process = subprocess.Popen([sys.executable, '-c', 'import sys; sys.stdin.read()'],
                                               stdin=subprocess.PIPE, stdout=inside if owned else outside)
                    try:
                        state = {'fixture_plugin_pid': process.pid}
                        if owned:
                            check_tracked_processes(state, stack_directory)
                        else:
                            with self.assertRaisesRegex(RuntimeError, 'outside the selected Stack'):
                                check_tracked_processes(state, stack_directory)
                        self.assertIsNone(process.poll(), 'scope validation signaled a process')
                    finally:
                        process.communicate(b'', timeout=5)
