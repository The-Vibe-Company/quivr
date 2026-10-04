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
            async def uncertain(request, renew):
                calls.append(request)
                return {'status': 'complete', 'remote_cleanup_verified': True, 'sandbox_terminated': False}
            cfg2 = modal_engine.policy({'max_seconds': 30, 'startup_seconds': 30,
                                       'modal_daily_usd': .3, 'experiment': 'public/uncertain'})
            # A different immutable policy gets its own campaign, not a bypass
            # inside the old campaign. A failed invocation keeps its full hold.
            name = uuid.uuid4().hex
            failed = asyncio.run(modal_engine.dispatch(store, name, cfg2, sha, uncertain, outbox))
            self.assertEqual(failed['status'], 'failed')
            self.assertAlmostEqual(store.summary(name)['modal']['unknown_usd'], .21)
            capped = asyncio.run(modal_engine.dispatch(store, name, cfg2, sha, remote, outbox))
            self.assertEqual(capped['status'], 'capped')
            self.assertEqual(len(calls), 2)
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
            self.assertEqual(len(calls), 3)


class Lifecycle(unittest.TestCase):
    def test_termination_precedes_return_on_success_remote_failure_and_cancellation(self):
        for fault in ('none', 'remote', 'cancel', 'lease', 'terminate'):
            with self.subTest(fault=fault), tempfile.TemporaryDirectory() as temp:
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
                        if fault == 'cancel':
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
                    self.assertNotIn('secrets', kwargs)
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
                                               Image=types.SimpleNamespace(from_registry=lambda *args, **kwargs: image))
                with mock.patch.dict('sys.modules', {'modal': modal}):
                    if fault == 'none':
                        result = asyncio.run(modal_engine.run_modal({'policy': modal_engine.policy({}),
                             'campaign': 'fixture', 'outbox': temp}, renew))
                        self.assertTrue(result['sandbox_terminated'])
                    elif fault == 'cancel':
                        with self.assertRaises(asyncio.CancelledError):
                            asyncio.run(modal_engine.run_modal({'policy': modal_engine.policy({}),
                                'campaign': 'fixture', 'outbox': temp}, renew))
                    else:
                        result = asyncio.run(modal_engine.run_modal({'policy': modal_engine.policy({}),
                            'campaign': 'fixture', 'outbox': temp}, renew))
                        self.assertEqual(result['status'], 'failed')
                        self.assertEqual(result['sandbox_terminated'], fault != 'terminate')
                        self.assertNotIn('secret', json.dumps(result))
                self.assertIn('terminate', events)
                self.assertLess(events.index('terminate'), events.index('app-stop'))


if __name__ == '__main__':
    unittest.main()
