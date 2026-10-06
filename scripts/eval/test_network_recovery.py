"""Recovery owner: bounded time and campaign admission, without wall-clock waits."""
import os
import threading
from concurrent.futures import ThreadPoolExecutor
import unittest
import uuid
from unittest import mock

import network_recovery as recovery


class Clock:
    def __init__(self):
        self.now, self.waits = 100., []

    def sleep(self, seconds):
        self.waits.append(seconds)
        self.now += seconds


class Recovery(unittest.TestCase):
    def test_retry_schedule_caps_delay_and_uses_only_remaining_window(self):
        for seconds, failures, waits in ((60, 3, [1, 2, 4]), (60, 7, [1, 2, 4, 8, 10, 10, 10]),
                                         (12, None, [1, 2, 4, 5]), (30, None, [1, 2, 4, 8, 10, 5]),
                                         (0, None, [])):
            with self.subTest(seconds=seconds, failures=failures):
                clock, attempts = Clock(), []
                def operation():
                    attempts.append(clock.now)
                    if failures is None or len(attempts) <= failures:
                        raise ConnectionError('private transport details')
                    return 'recovered'
                with mock.patch.object(recovery.time, 'monotonic', side_effect=lambda: clock.now), \
                     mock.patch.object(recovery.time, 'sleep', side_effect=clock.sleep):
                    if failures is None:
                        with self.assertRaisesRegex(recovery.Outage, '^network outage window elapsed; no new paid work admitted$'):
                            recovery.retry(operation, lambda error: isinstance(error, ConnectionError), seconds=seconds)
                    else:
                        self.assertEqual(recovery.retry(operation, lambda error: isinstance(error, ConnectionError), seconds=seconds),
                                         'recovered')
                self.assertEqual(clock.waits, waits)
                self.assertEqual(len(attempts), len(waits) + 1)
        # A permanent refusal is not a network outage, even with a long window.
        with mock.patch.object(recovery.time, 'sleep') as sleep:
            with self.assertRaises(PermissionError):
                recovery.retry(lambda: (_ for _ in ()).throw(PermissionError('refused')), lambda error: False)
        sleep.assert_not_called()

    def test_window_accepts_operator_bounds_and_rejects_invalid_values(self):
        for value, expected in ((None, 600), ('0', 0), ('3600', 3600), ('12', 12),
                                ('-1', None), ('3601', None), ('bad', None), ('1.5', None)):
            flags = {} if value is None else {'EVAL_NETWORK_OUTAGE_SECONDS': value}
            with self.subTest(value=value), mock.patch.dict(os.environ, flags, clear=True):
                if expected is None:
                    with self.assertRaisesRegex(ValueError, '^EVAL_NETWORK_OUTAGE_SECONDS must be an integer from 0 to 3600$'):
                        recovery.window()
                else:
                    self.assertEqual(recovery.window(), expected)

    def test_recovery_pauses_admission_until_all_attempts_finish(self):
        for outcome in ('recovered', 'refused', 'exhausted'):
            for overlap in (False, True):
                with self.subTest(outcome=outcome, overlap=overlap):
                    campaign, clock, attempts = uuid.uuid4().hex, Clock(), []
                    gate = recovery.admission(campaign)
                    if overlap:
                        gate.pause()  # Another operation is still recovering.
                    def operation():
                        if attempts:
                            with self.assertRaises(recovery.AdmissionPaused), gate.commit():
                                pass
                        attempts.append(clock.now)
                        if len(attempts) == 1 or outcome == 'exhausted':
                            raise ConnectionError('private transport details')
                        if outcome == 'refused':
                            raise PermissionError('permanent refusal')
                        return 'recovered'
                    with mock.patch.object(recovery.time, 'monotonic', side_effect=lambda: clock.now), \
                         mock.patch.object(recovery.time, 'sleep', side_effect=clock.sleep):
                        if outcome == 'recovered':
                            self.assertEqual(recovery.retry(operation, lambda error: isinstance(error, ConnectionError),
                                                           seconds=3, campaign=campaign), 'recovered')
                        else:
                            with self.assertRaises(PermissionError if outcome == 'refused' else recovery.Outage):
                                recovery.retry(operation, lambda error: isinstance(error, ConnectionError), seconds=3, campaign=campaign)
                    if overlap:
                        with self.assertRaises(recovery.AdmissionPaused), gate.commit():
                            pass
                        gate.finish(False)
                    with mock.patch.dict(os.environ, EVAL_NETWORK_OUTAGE_SECONDS='0'):
                        if outcome == 'exhausted':
                            with self.assertRaises(recovery.AdmissionPaused), gate.commit():
                                pass
                            with self.assertRaises(recovery.Outage):
                                gate.wait()
                        else:
                            gate.wait()
                            with gate.commit():
                                pass

        # Two real retry calls share the same admission fence. Finishing one
        # must not reopen it while the other still has an unreachable transport.
        campaign = uuid.uuid4().hex
        paused = {side: threading.Event() for side in ('first', 'second')}
        release = {side: threading.Event() for side in paused}
        current = threading.local()
        def recover(side):
            current.side = side
            attempts = 0
            def operation():
                nonlocal attempts
                attempts += 1
                if attempts == 1:
                    raise ConnectionError('unreachable')
                return side
            return recovery.retry(operation, lambda error: isinstance(error, ConnectionError), seconds=3, campaign=campaign)
        def wait(seconds):
            side = current.side
            paused[side].set()
            if not release[side].wait(5):
                raise AssertionError('retry transport was not released')
        with mock.patch.object(recovery.time, 'monotonic', return_value=0), \
             mock.patch.object(recovery.time, 'sleep', side_effect=wait), ThreadPoolExecutor(max_workers=2) as pool:
            futures = {side: pool.submit(recover, side) for side in paused}
            try:
                self.assertTrue(all(event.wait(5) for event in paused.values()))
                with self.assertRaises(recovery.AdmissionPaused), recovery.admission(campaign).commit():
                    pass
                release['first'].set()
                self.assertEqual(futures['first'].result(5), 'first')
                with self.assertRaises(recovery.AdmissionPaused), recovery.admission(campaign).commit():
                    pass
                release['second'].set()
                self.assertEqual(futures['second'].result(5), 'second')
                with recovery.admission(campaign).commit():
                    pass
            finally:
                for event in release.values():
                    event.set()


if __name__ == '__main__':
    unittest.main()
