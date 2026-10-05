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
    def test_connect_outage_retries_boundedly_but_refusal_and_transaction_errors_do_not_replay(self):
        import psycopg
        clock, waits = [0], []
        def wait(seconds):
            waits.append(seconds)
            clock[0] += seconds
        with mock.patch('time.monotonic', side_effect=lambda: clock[0]), \
             mock.patch('time.sleep', side_effect=wait), \
             mock.patch.dict(os.environ, EVAL_NETWORK_OUTAGE_SECONDS='12', EVAL_CONTROL_CA_PEM=''):
            store = control_store.Store('host=localhost')
            with mock.patch('psycopg.connect', side_effect=[psycopg.OperationalError('DNS secret'),
                    psycopg.OperationalError('TCP secret'), mock.MagicMock()]) as connect:
                with store.transaction():
                    pass
                self.assertEqual(connect.call_count, 3)
                self.assertEqual(waits, [1, 2])
            waits.clear()
            with mock.patch('psycopg.connect', side_effect=psycopg.OperationalError('TLS secret')) as connect:
                with self.assertRaises(control_store.Unavailable) as error:
                    with store.transaction():
                        self.fail('unreachable store admitted work')
                self.assertNotIn('secret', str(error.exception))
                self.assertEqual(waits, [1, 2, 4, 5])
                self.assertEqual(connect.call_count, 5)
            for refused in (psycopg.errors.InvalidPassword('secret'),
                            psycopg.OperationalError('password authentication failed for user secret')):
                waits.clear()
                with mock.patch('psycopg.connect', side_effect=refused) as connect:
                    with self.assertRaises(control_store.Unavailable):
                        with store.transaction():
                            pass
                    self.assertEqual(connect.call_count, 1)
                    self.assertFalse(waits)
            with mock.patch('psycopg.connect', return_value=mock.MagicMock()) as connect:
                with self.assertRaises(control_store.Unavailable):
                    with store.transaction():
                        raise psycopg.OperationalError('connection lost during commit')
                self.assertEqual(connect.call_count, 1, 'an uncertain transaction must never replay')

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
                    with mock.patch.dict(os.environ, EVAL_CONTROL_CA_PEM=pem, EVAL_NETWORK_OUTAGE_SECONDS='0'), mock.patch('psycopg.connect', side_effect=connect):
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
    def test_parallel_admission_rolls_back_when_transport_pauses_before_write(self):
        import threading
        import network_recovery
        campaign = 'gate-' + uuid.uuid4().hex
        store = control_store.Store(os.environ['EVAL_CONTROL_TEST_DSN'])
        store.campaign(campaign, {'provider_daily_usd': 10, 'modal_daily_usd': 10})
        gate = network_recovery.admission(campaign)
        paused, rolled_back = threading.Event(), threading.Event()
        original_lock, original_wait = store.lock, gate.wait
        def lock(db, name):
            result = original_lock(db, name)
            gate.pause()
            paused.set()
            return result
        def wait():
            if paused.is_set():
                rolled_back.set()
            return original_wait()
        with mock.patch.object(store, 'lock', side_effect=lock), mock.patch.object(gate, 'wait', side_effect=wait):
            with ThreadPoolExecutor(max_workers=1) as pool:
                future = pool.submit(store.reserve, campaign, 'provider', 1)
                try:
                    self.assertTrue(paused.wait(5), 'reservation did not reach SQL')
                    self.assertTrue(rolled_back.wait(5), 'paused admission committed instead of rolling back')
                    self.assertNotIn('provider', control_store.Store(store.dsn).summary(campaign))
                finally:
                    # Stop the lock hook before the retry, and release the waiter.
                    store.lock = original_lock
                    gate.finish(False)
                rid = future.result(timeout=5)
        self.assertTrue(rid)
        self.assertEqual(store.summary(campaign)['provider']['charged_usd'], 1)

    def setUp(self):
        import psycopg
        self.dsn = os.environ['EVAL_CONTROL_TEST_DSN']
        with psycopg.connect(self.dsn) as db:
            db.execute((pathlib.Path(__file__).parents[2] / 'deploy/mlflow/eval-control.sql').read_text())
        self.store = control_store.Store(self.dsn)
        self.name = uuid.uuid4().hex
        self.policy = {'provider_daily_usd': 1, 'modal_daily_usd': 1}
        self.store.campaign(self.name, self.policy)

    def test_fenced_confirmation_opens_each_attempt_once_and_shares_the_limit(self):
        # New contract: a lease alone does not prevent duplicate remote delivery
        # reading held-out data twice. Real SQL owns single-use admission.
        binding = {'heldout_family_digest': 'a' * 64, 'baseline_hash': 'b' * 64}
        self.store.register_confirmation(self.name, binding)
        self.assertEqual(self.store.confirmation_policy(self.name), binding)
        with self.assertRaises(ValueError):
            self.store.register_confirmation(self.name, {**binding, 'baseline_hash': 'c' * 64})
        key = 'engine-confirmation/first-finalist'
        owner = self.store.claim(self.name, key)['owner']
        def admit(_):
            try:
                return self.store.confirmation(self.name, (key, owner))
            except PermissionError:
                return None
        with ThreadPoolExecutor(max_workers=8) as pool:
            self.assertEqual([x for x in pool.map(admit, range(8)) if x], [1])
        self.assertEqual(self.store.confirmation_proof(self.name, key, owner)['read_ordinal'], 1)
        # An uncertain first read stays consumed; replacing its lease cannot
        # reuse the ordinal, even for the same finalist.
        self.store.abandon(self.name, key, owner, 'failed')
        replacement = self.store.claim(self.name, key)['owner']
        with self.assertRaises(control_store.LeaseLost):
            self.store.confirmation(self.name, (key, owner))
        self.assertEqual(self.store.confirmation(self.name, (key, replacement)), 2)
        other = 'engine-confirmation/second-finalist'
        second = self.store.claim(self.name, other)['owner']
        self.assertEqual(self.store.confirmation(self.name, (other, second)), 3)
        for number in range(4, 11):
            lease_key = 'remaining-' + str(number)
            held = self.store.claim(self.name, lease_key)['owner']
            self.assertEqual(self.store.confirmation(self.name, (lease_key, held)), number)
        held = self.store.claim(self.name, 'eleventh')['owner']
        with self.assertRaises(PermissionError):
            self.store.confirmation(self.name, ('eleventh', held))
        self.assertEqual(self.store.availability(self.name)['confirmation_reads_left'], 0)
        # The same campaign lock fences durable standalone intent/bind and
        # rejects publication after stop even with a valid consumed read.
        intent = 'intent'
        resource_owner = self.store.claim(self.name, 'confirmation-resource/' + intent)['owner']
        with self.assertRaises(control_store.LeaseLost):
            self.store.bind_confirmation_app(self.name, intent, resource_owner, 'ap-fixture')
        self.store.confirmation_intent(self.name, intent, resource_owner)
        self.store.bind_confirmation_app(self.name, intent, resource_owner, 'ap-fixture')
        with self.assertRaises(control_store.LeaseLost):
            self.store.bind_confirmation_app(self.name, intent, resource_owner, 'ap-different')
        self.store.stop(self.name)
        with self.assertRaises(control_store.LeaseLost):
            self.store.publish_confirmation(self.name, other, second, {'status': 'confirmed'})
        with self.assertRaises(control_store.LeaseLost):
            self.store.bind_confirmation_app(self.name, intent, resource_owner, 'ap-fixture')

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

    def test_total_end_and_stop_guard_paid_work_and_confirmation(self):
        # New lifetime contract: the old daily tests cannot see cross-day
        # totals, deadlines or stopped confirmation. Real SQL owns races.
        import psycopg
        bounded = uuid.uuid4().hex
        self.store.campaign(bounded, {**self.policy, 'provider_total_usd': .7,
                                     'modal_total_usd': 2, 'confirmation_limit': 2})
        first = self.store.reserve(bounded, 'provider', .5)
        self.store.settle(first, .5)
        # Move the charged attempt to yesterday without waiting for midnight.
        with psycopg.connect(self.dsn) as db:
            db.execute("UPDATE eval_control.reservations SET day=day-1 WHERE campaign=%s", (bounded,))
        def attempt(_):
            try:
                return self.store.reserve(bounded, 'provider', .15)
            except embeddings.BudgetExceeded:
                return None
        with ThreadPoolExecutor(max_workers=4) as pool:
            admitted = [r for r in pool.map(attempt, range(4)) if r]
        self.assertEqual(len(admitted), 1)
        self.assertAlmostEqual(self.store.summary(bounded)['provider']['charged_usd'], .65)
        with self.assertRaises(PermissionError):
            self.store.confirmation(bounded)
        expired = uuid.uuid4().hex
        self.store.campaign(expired, {**self.policy, 'end_at': '2000-01-01T00:00:00+00:00'})
        with self.assertRaises(embeddings.BudgetExceeded):
            self.store.reserve(expired, 'modal', .1)
        with self.assertRaises(PermissionError):
            self.store.confirmation(expired)
        limited = uuid.uuid4().hex
        self.store.campaign(limited, {**self.policy, 'confirmation_limit': 2})
        self.assertEqual(self.store.confirmation(limited), 1)
        self.assertEqual(self.store.confirmation(limited), 2)
        with self.assertRaises(PermissionError):
            self.store.confirmation(limited)
        self.store.stop(limited, 'operator stop')
        with self.assertRaises(embeddings.BudgetExceeded):
            self.store.reserve(limited, 'modal', .01)
        with self.assertRaises(PermissionError):
            self.store.confirmation(limited)

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
        self.store.release_many(self.name, {keys[1]: owners[keys[1]]})
        owners[keys[1]] = replacement['owner']
        self.store.publish_many(self.name, {k: (o, {'canonical': True}) for k, o in owners.items()})
        self.store.release_many(self.name, owners)
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
        with mock.patch.dict(os.environ, EVAL_NETWORK_OUTAGE_SECONDS='0'), self.assertRaises(control_store.Unavailable):
            control_store.Store('postgresql://localhost:1/absent?connect_timeout=1').campaign('x', self.policy)

    def test_validation_error_survives_unavailable_claim_release(self):
        dsn = self.store.dsn
        try:
            with mock.patch.dict(os.environ, EVAL_NETWORK_OUTAGE_SECONDS='0'), self.assertLogs('control_store', level='WARNING') as messages:
                with self.assertRaisesRegex(ValueError, '^cache validation failed$'):
                    with self.store.claim_batch(self.name, ['release-outage'], ttl=86400) as claims:
                        self.store.dsn = 'postgresql://localhost:1/absent?connect_timeout=1'
                        raise ValueError('cache validation failed')
            self.assertEqual(messages.output, ['WARNING:control_store:unpublished claim release deferred; store unavailable or contended'])
        finally:
            self.store.dsn = dsn
        import psycopg
        with psycopg.connect(dsn) as db:
            remaining = db.execute("SELECT extract(epoch FROM expires_at-clock_timestamp()) FROM eval_control.leases WHERE campaign=%s AND key='release-outage'", (self.name,)).fetchone()[0]
        self.assertLessEqual(remaining, 600)
        # A failed compensation retains its fence, then can be released later.
        self.assertEqual(self.store.claim(self.name, 'release-outage')['status'], 'leased')
        self.store.release_many(self.name, {'release-outage': claims['release-outage']['owner']})
        self.assertEqual(self.store.claim(self.name, 'release-outage')['status'], 'claimed')


if __name__ == '__main__':
    unittest.main()
