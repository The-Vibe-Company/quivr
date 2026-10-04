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

    def test_cache_batch_round_trips_and_mixed_claims_preserve_each_entry(self):
        import psycopg
        keys = ['embedding/' + str(i) for i in range(128)]
        connect = psycopg.connect
        counts = {'connections': 0, 'sql': 0}
        class CountingConnection:
            def __init__(self, db):
                self.db = db
            def __enter__(self):
                self.db.__enter__()
                return self
            def __exit__(self, *args):
                return self.db.__exit__(*args)
            def execute(self, *args, **kwargs):
                counts['sql'] += 1
                return self.db.execute(*args, **kwargs)
        def counted(*args, **kwargs):
            counts['connections'] += 1
            return CountingConnection(connect(*args, **kwargs))
        with mock.patch('psycopg.connect', side_effect=counted):
            claims = self.store.claim_many(self.name, keys, ttl=86400)
        self.assertEqual(counts['connections'], 1)
        self.assertLessEqual(counts['sql'], 3)
        self.assertEqual(set(claims), set(keys))
        self.assertTrue(all(c['status'] == 'claimed' for c in claims.values()))
        owners = {key: claim['owner'] for key, claim in claims.items()}
        counts.update(connections=0, sql=0)
        with mock.patch('psycopg.connect', side_effect=counted):
            self.store.renew_many(self.name, owners, ttl=86400)
            self.store.publish_many(self.name, {k: (o, {'value': k}) for k, o in owners.items()})
        self.assertEqual(counts['connections'], 2)
        self.assertLessEqual(counts['sql'], 4)
        # A mixed batch must leave completed and active entries untouched while
        # replacing an expired owner and admitting a previously absent entry.
        live = self.store.claim(self.name, 'live')
        stale = self.store.claim(self.name, 'expired')
        with connect(self.dsn) as db:
            db.execute("UPDATE eval_control.leases SET expires_at=clock_timestamp()-interval '1 second' WHERE campaign=%s AND key='expired'", (self.name,))
        with self.assertRaises(control_store.LeaseBusy):
            self.store.claim_many(self.name, ['new', 'expired', 'live', keys[0]], require_available=True)
        with connect(self.dsn) as db:
            self.assertIsNone(db.execute("SELECT owner FROM eval_control.leases WHERE campaign=%s AND key='new'", (self.name,)).fetchone())
            self.assertEqual(db.execute("SELECT owner FROM eval_control.leases WHERE campaign=%s AND key='expired'", (self.name,)).fetchone()[0], stale['owner'])
        claims = self.store.claim_many(self.name, [keys[0], 'live', 'expired', 'new'])
        self.assertEqual(claims[keys[0]], {'status': 'done', 'payload': {'value': keys[0]}})
        self.assertEqual(claims['live'], {'status': 'leased'})
        self.assertEqual(claims['expired']['status'], 'claimed')
        self.assertNotEqual(claims['expired']['owner'], stale['owner'])
        self.assertEqual(claims['new']['status'], 'claimed')
        with connect(self.dsn) as db:
            self.assertEqual(db.execute("SELECT owner FROM eval_control.leases WHERE campaign=%s AND key='live'", (self.name,)).fetchone()[0], live['owner'])

    def test_batch_races_and_failed_fences_roll_back_other_entries(self):
        import psycopg
        keys = ['embedding/race-a', 'embedding/race-b']
        with ThreadPoolExecutor(max_workers=4) as pool:
            batches = list(pool.map(lambda _: self.store.claim_many(self.name, keys), range(4)))
        for key in keys:
            self.assertEqual(sum(b[key]['status'] == 'claimed' for b in batches), 1)
        owners = {k: next(b[k]['owner'] for b in batches if b[k]['status'] == 'claimed') for k in keys}
        # No waits: expire the second lease and show neither UPDATE can partially
        # commit the first entry when its peer has lost ownership.
        with psycopg.connect(self.dsn) as db:
            before = db.execute('SELECT expires_at FROM eval_control.leases WHERE campaign=%s AND key=%s', (self.name, keys[0])).fetchone()[0]
            db.execute("UPDATE eval_control.leases SET expires_at=clock_timestamp()-interval '1 second' WHERE campaign=%s AND key=%s", (self.name, keys[1]))
        with self.assertRaises(control_store.LeaseLost):
            self.store.renew_many(self.name, owners, ttl=86400)
        with self.assertRaises(control_store.LeaseLost):
            self.store.publish_many(self.name, {k: (o, {'canonical': True}) for k, o in owners.items()})
        with psycopg.connect(self.dsn) as db:
            self.assertEqual(db.execute('SELECT expires_at,payload FROM eval_control.leases WHERE campaign=%s AND key=%s', (self.name, keys[0])).fetchone(), (before, None))
        replacement = self.store.claim_many(self.name, [keys[1]])[keys[1]]
        owners[keys[1]] = replacement['owner']
        self.store.publish_many(self.name, {k: (o, {'canonical': True}) for k, o in owners.items()})
        with self.assertRaises(control_store.LeaseLost):
            self.store.renew_many(self.name, owners)
        self.assertTrue(all(c == {'status': 'done', 'payload': {'canonical': True}} for c in self.store.claim_many(self.name, keys).values()))

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
