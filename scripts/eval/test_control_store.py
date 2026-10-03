"""SQL adapter owner: races, reconciliation, fencing and confirmation admission.

Uses an explicitly disposable database, never a provider or Modal. Faking SQL
cannot detect lost updates; existing results tests cover only tracking/outboxes.
"""
import os
import pathlib
import unittest
import uuid
from concurrent.futures import ThreadPoolExecutor
from unittest import mock

import control_store
import embeddings


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
