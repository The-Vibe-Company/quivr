"""Reset safety at the operator boundary; the real Compose proof lives in lifecycle.py."""
import json
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
