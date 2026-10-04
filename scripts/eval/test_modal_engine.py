"""Smoke command owner: refusal, admission, lifecycle and aggregate evidence.

These contracts have no tier-1 owner: a smoke launches a VM/Docker stack and
publishes only after it has gone. Fake Modal transport, not store or Results.
"""
import io
import json
import os
import pathlib
import tempfile
import unittest
import uuid
import asyncio
import types
from unittest import mock

import modal_engine
import control_store


class Command(unittest.TestCase):
    def test_keyless_dry_run_and_ci_refusal_precede_live_launch(self):
        with mock.patch.dict(os.environ, {}, clear=True), mock.patch('sys.stdout', new_callable=io.StringIO) as output:
            self.assertEqual(modal_engine.main(['--dry-run']), 0)
        row = json.loads(output.getvalue())
        self.assertEqual(row['kind'], 'smoke')
        self.assertFalse(row['confirmation_available'])
        self.assertGreater(row['modal_reservation_usd'], 0)
        self.assertIn('full Modal invoice', row['compute_cap_notice'])
        with mock.patch.dict(os.environ, {'CI': 'true'}), mock.patch.object(modal_engine, 'launch') as launch, \
                mock.patch('sys.stderr', new_callable=io.StringIO):
            with self.assertRaises(SystemExit):
                modal_engine.main(['--smoke', '--allow-paid', '--campaign', 'example'])
            launch.assert_not_called()
        for invalid in ({'max_seconds': 0}, {'keepalive_seconds': 100, 'reaper_seconds': 10},
                        {'modal_daily_usd': float('nan')}, {'sets': {'heldout': {}}}):
            with self.subTest(invalid=invalid), self.assertRaises(ValueError):
                modal_engine.policy(invalid)


@unittest.skipUnless(os.environ.get('EVAL_CONTROL_TEST_DSN'), 'requires disposable PostgreSQL')
class Admission(unittest.TestCase):
    def test_shared_admission_replays_only_clean_evidence_and_retains_unknown_charges(self):
        store = control_store.Store(os.environ['EVAL_CONTROL_TEST_DSN'])
        calls = []
        cfg = modal_engine.policy({'max_seconds': 30, 'startup_seconds': 30,
                                   'modal_daily_usd': .3})
        campaign, sha = uuid.uuid4().hex, 'a' * 40
        async def remote(request, renew):
            calls.append(request)
            await renew()
            return {'status': 'complete', 'documents': 3, 'searches': 3,
                    'modes': ['lexical', 'semantic', 'hybrid'], 'remote_cleanup_verified': True,
                    'sandbox_terminated': True, 'duration_seconds': 1,
                    'image_id': 'im-fixture', 'query_text': 'must-never-escape'}
        with tempfile.TemporaryDirectory() as temp, mock.patch.dict(os.environ, {'MLFLOW_TRACKING_URI': ''}):
            outbox = pathlib.Path(temp)
            first = asyncio.run(modal_engine.dispatch(store, campaign, cfg, sha, remote, outbox))
            self.assertEqual(first['status'], 'complete')
            replay = asyncio.run(modal_engine.dispatch(store, campaign, cfg, sha, remote, outbox))
            self.assertEqual(replay['status'], 'reused')
            self.assertEqual(len(calls), 1)
            self.assertNotIn('must-never-escape', ''.join(p.read_text() for p in outbox.glob('*.json')))
            cfg2 = modal_engine.policy({'max_seconds': 30, 'startup_seconds': 30,
                                       'modal_daily_usd': .3, 'experiment': 'public/uncertain'})
            # A different immutable policy gets its own campaign, not a bypass
            # inside the old campaign. A failed invocation keeps its full hold.
            for remote_status in ('complete', 'failed'):
                with self.subTest(remote_status=remote_status):
                    async def uncertain(request, renew):
                        calls.append(request)
                        evidence = {'documents': 3, 'searches': 3, 'modes': ['lexical', 'semantic', 'hybrid']}
                        return {'status': remote_status, 'remote_cleanup_verified': remote_status == 'complete',
                                'sandbox_terminated': False, 'image_id': 'im-build-fixture',
                                'error_message': 'must-never-escape',
                                **(evidence if remote_status == 'complete' else {})}
                    name, before = uuid.uuid4().hex, len(calls)
                    failed = asyncio.run(modal_engine.dispatch(store, name, cfg2, sha, uncertain, outbox))
                    self.assertEqual(failed['status'], 'failed')
                    self.assertEqual(failed['image_id'], 'im-build-fixture')
                    self.assertEqual(failed['image_logs'], 'modal image logs im-build-fixture')
                    self.assertNotIn('must-never-escape', json.dumps(failed))
                    self.assertAlmostEqual(store.summary(name)['modal']['unknown_usd'], .21)
                    capped = asyncio.run(modal_engine.dispatch(store, name, cfg2, sha, remote, outbox))
                    self.assertEqual(capped['status'], 'capped')
                    self.assertEqual(len(calls), before + 1)
            import psycopg
            async def revoked(request, renew):
                calls.append(request)
                with psycopg.connect(os.environ['EVAL_CONTROL_TEST_DSN']) as db:
                    db.execute("UPDATE eval_control.leases SET expires_at=clock_timestamp()-interval '1 second' WHERE campaign=%s", (request['campaign'],))
                await renew()
                self.fail('expired owner continued')
            lost = asyncio.run(modal_engine.dispatch(store, uuid.uuid4().hex, cfg, sha, revoked, outbox))
            self.assertEqual(lost['status'], 'failed')
            self.assertEqual(lost['error_class'], 'LeaseLost')
            self.assertEqual(len(list(outbox.glob('*.json'))), 1)
            too_small = modal_engine.policy({'modal_daily_usd': .001})
            blocked = asyncio.run(modal_engine.dispatch(store, uuid.uuid4().hex, too_small, sha, remote, outbox))
            self.assertEqual(blocked['status'], 'capped')
            self.assertEqual(len(calls), 4)


class Lifecycle(unittest.TestCase):
    def test_termination_precedes_return_on_success_remote_failure_and_cancellation(self):
        for fault in ('none', 'remote', 'cancel', 'lease', 'terminate', 'build', 'build-invalid',
                      'confirmation', 'confirmation-bind', 'confirmation-cancel'):
            with self.subTest(fault=fault), tempfile.TemporaryDirectory() as temp:
                confirmation = fault.startswith('confirmation')
                events = []
                async def renew():
                    events.append('renew')
                    if fault == 'lease':
                        raise control_store.LeaseLost('must-never-escape')
                class Scope:
                    async def __aenter__(self):
                        events.append('app-start')
                    async def __aexit__(self, *args):
                        events.append('app-stop')
                class App:
                    app_id = 'ap-fixture'
                    def __init__(self, *args):
                        self.run = types.SimpleNamespace(aio=lambda **kw: Scope())
                class Lines:
                    async def __aiter__(self):
                        await asyncio.sleep(0)
                        if fault in ('cancel', 'confirmation-cancel'):
                            raise asyncio.CancelledError()
                        if fault == 'remote':
                            raise RuntimeError('secret query text')
                        if fault == 'lease':
                            await asyncio.Future()  # renewal failure must cancel this
                        yield json.dumps({'event': 'result', 'status': 'complete', 'documents': 3,
                            'searches': 3, 'modes': ['lexical', 'semantic', 'hybrid'], 'remote_cleanup_verified': True})
                async def terminated(**kw):
                    events.append('terminate')
                    self.assertTrue(kw['wait'])
                    if fault == 'terminate':
                        raise RuntimeError('secret endpoint')
                async def done(*args, **kwargs):
                    return 137
                sandbox = types.SimpleNamespace(object_id='sb-fixture', stdout=Lines(),
                    stdin=types.SimpleNamespace(write=lambda ping: None, drain=types.SimpleNamespace(aio=done)),
                    wait=types.SimpleNamespace(aio=done), poll=types.SimpleNamespace(aio=done),
                    terminate=types.SimpleNamespace(aio=terminated))
                async def create(*args, **kwargs):
                    events.append('create')
                    self.assertEqual(kwargs['runtime'], 'vm')
                    if confirmation:
                        self.assertLess(events.index('bind'), events.index('create'))
                        self.assertEqual(kwargs['secrets'], ['quivr-eval-engine-confirmation'])
                        self.assertEqual(kwargs['volumes'], {'/protected': 'quivr-eval-heldout'})
                        self.assertIn('engine_confirm_runner.py', args[1])
                        serialized = json.loads(args[-1])
                        self.assertEqual(serialized['lease_key'], 'engine-confirmation/fixture')
                        self.assertNotIn('outbox', serialized)
                    else:
                        self.assertNotIn('secrets', kwargs)
                    if fault.startswith('build'):
                        # Modal's ImageBuildError carries this public attribute
                        # before image hydration; inject that transport failure.
                        error = RuntimeError('secret build output')
                        error.image_id = 'im-build-fixture' if fault == 'build' else 'secret invalid id'
                        raise error
                    return sandbox
                image = mock.Mock(object_id='im-fixture')
                for method in ('entrypoint', 'apt_install', 'run_commands', 'pip_install', 'env', 'add_local_dir'):
                    getattr(image, method).return_value = image
                def source_directory(*args, **kwargs):
                    # Pinned Modal's upload contract passes paths relative to
                    # the source directory, including untracked private files.
                    self.assertFalse(kwargs['ignore'](pathlib.Path('scripts/eval/engine_stack.py')))
                    self.assertTrue(kwargs['ignore'](pathlib.Path('.context/private-key')))
                    return image
                image.add_local_dir.side_effect = source_directory
                modal = types.SimpleNamespace(App=App, Sandbox=types.SimpleNamespace(create=types.SimpleNamespace(aio=create)),
                                               Image=types.SimpleNamespace(from_registry=lambda *args, **kwargs: image),
                                               Secret=types.SimpleNamespace(from_name=lambda name: name),
                                               Volume=types.SimpleNamespace(from_name=lambda name: name))
                def bind(app_id):
                    events.append('bind')
                    self.assertEqual(app_id, 'ap-fixture')
                    if fault == 'confirmation-bind':
                        raise control_store.LeaseLost('secret owner expired')
                request = {'policy': modal_engine.policy({}), 'campaign': 'fixture', 'outbox': temp}
                if confirmation:
                    request.update(confirmation_request={'version': 1}, lease_key='engine-confirmation/fixture',
                                   owner='fixture-owner', app_name='durable-intent', on_app=bind)
                with mock.patch.dict('sys.modules', {'modal': modal}):
                    if fault in ('none', 'confirmation'):
                        result = asyncio.run(modal_engine.run_modal(request, renew))
                        self.assertTrue(result['sandbox_terminated'])
                        if confirmation:
                            self.assertEqual(result['compute_ids'], {'app_id': 'ap-fixture', 'sandbox_id': 'sb-fixture'})
                    elif fault in ('cancel', 'confirmation-cancel'):
                        with self.assertRaises(asyncio.CancelledError):
                            asyncio.run(modal_engine.run_modal(request, renew))
                    else:
                        result = asyncio.run(modal_engine.run_modal(request, renew))
                        self.assertEqual(result['status'], 'failed')
                        self.assertEqual(result['sandbox_terminated'], fault not in ('terminate', 'build', 'build-invalid', 'confirmation-bind'))
                        self.assertNotIn('secret', json.dumps(result))
                if fault == 'confirmation-bind':
                    self.assertNotIn('create', events)
                    self.assertIn('app-stop', events)
                    continue
                if fault.startswith('build'):
                    self.assertNotIn('terminate', events)
                    recovery = json.loads((pathlib.Path(temp) / 'active/fixture.json').read_text())
                    self.assertEqual(recovery['state'], 'creating')
                    if fault == 'build':
                        self.assertEqual(result['image_id'], 'im-build-fixture')
                        self.assertEqual(result['image_logs'], 'modal image logs im-build-fixture')
                    else:
                        self.assertIsNone(result['image_id'])
                        self.assertNotIn('image_logs', result)
                    continue
                self.assertIn('terminate', events)
                self.assertLess(events.index('terminate'), events.index('app-stop'))


if __name__ == '__main__':
    unittest.main()
