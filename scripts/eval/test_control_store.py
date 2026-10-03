"""SQL adapter owner: races, reconciliation, fencing and confirmation admission.

Uses an explicitly disposable database, never a provider or Modal. Faking SQL
cannot detect lost updates; existing results tests cover only tracking/outboxes.
"""
import importlib.util
import os
import pathlib
import stat
import unittest
import uuid
from concurrent.futures import ThreadPoolExecutor
from unittest import mock

import control_store
import embeddings


@unittest.skipUnless(importlib.util.find_spec('psycopg'), 'requires psycopg')
class TLSConfiguration(unittest.TestCase):
    """Own PEM materialization at the real connection boundary, without SQL fakes."""
    def test_secret_ca_is_private_and_removed_after_success_or_connection_failure(self):
        import psycopg
        from psycopg.conninfo import conninfo_to_dict
        pem = '-----BEGIN CERTIFICATE-----\nexample CA\n-----END CERTIFICATE-----\n'
        dsns = ('postgresql://user:secret@db.example.test/control?sslmode=verify-full',
                'host=db.example.test dbname=control user=user password=secret sslmode=verify-full')
        for dsn in dsns:
            for fails in (False, True):
                with self.subTest(dsn=dsn.split(':')[0], fails=fails):
                    paths = []
                    def connect(connection, **kwargs):
                        info = conninfo_to_dict(connection)
                        self.assertEqual(info['sslmode'], 'verify-full')
                        self.assertEqual(info['password'], 'secret')
                        path = pathlib.Path(info['sslrootcert'])
                        paths.append(path)
                        self.assertEqual(path.read_bytes(), pem.encode())
                        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
                        if fails:
                            raise psycopg.OperationalError('secret ' + pem)
                        return mock.MagicMock()
                    with mock.patch.dict(os.environ, EVAL_CONTROL_CA_PEM=pem), mock.patch('psycopg.connect', side_effect=connect):
                        store = control_store.Store(dsn)
                        if fails:
                            with self.assertRaises(control_store.Unavailable) as error:
                                with store.transaction():
                                    self.fail('failed connection yielded a transaction')
                            self.assertNotIn('secret', str(error.exception))
                            self.assertNotIn(pem, str(error.exception))
                            self.assertIsNone(error.exception.__cause__)
                        else:
                            with store.transaction():
                                self.assertTrue(paths[0].exists())
                    self.assertFalse(paths[0].exists())

    def test_pem_rejects_explicit_root_and_unverified_connections(self):
        for dsn, reason in (
            ('host=db.example.test sslmode=verify-full sslrootcert=/ca.pem', 'sslrootcert'),
            ('postgresql://db.example.test/control?sslmode=verify-full&sslrootcert=', 'sslrootcert'),
            ('host=db.example.test sslmode=require', 'verify-full'),
            ('host=localhost sslmode=disable', 'verify-full'),
        ):
            with self.subTest(reason=reason), mock.patch.dict(os.environ, EVAL_CONTROL_CA_PEM='private CA'):
                with self.assertRaisesRegex(ValueError, reason):
                    control_store.Store(dsn)


@unittest.skipUnless(os.environ.get('EVAL_CONTROL_TEST_DSN'), 'requires disposable PostgreSQL')
class Control(unittest.TestCase):
    def setUp(self):
        import psycopg
        self.dsn = os.environ['EVAL_CONTROL_TEST_DSN']
        with psycopg.connect(self.dsn) as db:
            db.execute((pathlib.Path(__file__).parents[2] / 'deploy/mlflow/eval-control.sql').read_text())
        self.store = control_store.Store(self.dsn)
        self.name = uuid.uuid4().hex
        self.policy = {'provider_daily_usd': 1, 'modal_daily_usd': 1}
        self.store.campaign(self.name, self.policy)

    def test_concurrent_reservations_reconcile_once_and_unknown_attempts_remain_charged(self):
        def attempt(_):
            try:
                return self.store.reserve(self.name, 'provider', .6)
            except embeddings.BudgetExceeded:
                return None
        with ThreadPoolExecutor(max_workers=8) as pool:
            admitted = [r for r in pool.map(attempt, range(8)) if r]
        self.assertEqual(len(admitted), 1)
        self.store.settle(admitted[0], .2)
        self.store.settle(admitted[0], .2)
        self.assertAlmostEqual(self.store.summary(self.name)['provider']['charged_usd'], .2)
        # A cap hit stops that UTC day even if later settlement releases money.
        with self.assertRaises(embeddings.BudgetExceeded):
            self.store.reserve(self.name, 'provider', .1)
        modal = self.store.reserve(self.name, 'modal', .8)
        self.assertAlmostEqual(self.store.summary(self.name)['modal']['unknown_usd'], .8)
        with self.assertRaises(embeddings.BudgetExceeded):
            self.store.settle(modal, 1.1)
        self.assertAlmostEqual(self.store.summary(self.name)['modal']['charged_usd'], 1.1)
        with self.assertRaises(ValueError):
            self.store.campaign(self.name, dict(self.policy, provider_daily_usd=2))

    def test_lease_race_and_expiry_fence_publication_before_outbox_upload(self):
        with ThreadPoolExecutor(max_workers=8) as pool:
            claims = list(pool.map(lambda _: self.store.claim(self.name, 'candidate'), range(8)))
        winner = next(c for c in claims if c['status'] == 'claimed')
        self.assertEqual(sum(c['status'] == 'claimed' for c in claims), 1)
        # Force abandoned expiry as a failure condition; no wall-clock wait.
        import psycopg
        with psycopg.connect(self.dsn) as db:
            db.execute("UPDATE eval_control.leases SET expires_at=now()-interval '1 second' WHERE campaign=%s", (self.name,))
        replacement = self.store.claim(self.name, 'candidate')
        with self.assertRaises(control_store.LeaseLost):
            self.store.publish(self.name, 'candidate', winner['owner'], {'record': 'stale'})
        self.store.publish(self.name, 'candidate', replacement['owner'], {'record': 'canonical'})
        admission = self.store.claim(self.name, 'paid')
        original_fence = self.store.fence
        def expire_after_check(db, name, key, owner):
            original_fence(db, name, key, owner)
            db.execute("UPDATE eval_control.leases SET expires_at=clock_timestamp()-interval '1 second' WHERE campaign=%s AND key=%s", (name, key))
        with mock.patch.object(self.store, 'fence', side_effect=expire_after_check):
            with self.assertRaises(control_store.LeaseLost):
                self.store.reserve(self.name, 'provider', .1, lease=('paid', admission['owner']))
        self.assertNotIn('provider', self.store.summary(self.name))
        replay = self.store.claim(self.name, 'candidate')
        self.assertEqual(replay['status'], 'done')
        self.assertEqual(replay['payload'], {'record': 'canonical'})

    def test_confirmation_counter_is_atomic_and_store_outage_cannot_admit(self):
        def read(_):
            try:
                return self.store.confirmation(self.name)
            except PermissionError:
                return None
        with ThreadPoolExecutor(max_workers=8) as pool:
            reads = [r for r in pool.map(read, range(16)) if r]
        self.assertEqual(sorted(reads), list(range(1, 11)))
        with self.assertRaises(control_store.Unavailable):
            control_store.Store('postgresql://localhost:1/absent?connect_timeout=1').campaign('x', self.policy)


if __name__ == '__main__':
    unittest.main()
