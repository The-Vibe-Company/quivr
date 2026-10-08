"""Provider transport owns endpoint admission and DNS deadline cleanup."""
import subprocess
import time
import unittest
from unittest.mock import patch

from quivr_plugin.system_one import SystemOne


class Transport(unittest.TestCase):
    def test_dns_timeout_retains_no_paid_work_or_concurrency_slot(self):
        client = SystemOne('fixture-key', 'https://example.invalid/v1/systemone')
        lookups = []
        def timeout(*args, **kwargs):
            lookups.append((args, kwargs))
            raise subprocess.TimeoutExpired(args[0], kwargs['timeout'])
        with patch('subprocess.run', side_effect=timeout), \
             patch('quivr_plugin.system_one.socket.getaddrinfo', side_effect=AssertionError('DNS must be cancellable')):
            for _ in range(9):
                result = client.judge({}, {'q': {}}, time.monotonic() + 1)
                self.assertEqual(result.reason, 'deadline')
                self.assertTrue(result.retryable)
                self.assertEqual((result.scores, result.paid_calls, result.cost_cents), ({}, 0, 0))
        self.assertEqual(len(lookups), 9)
        for args, kwargs in lookups:
            self.assertGreater(kwargs['timeout'], 0)
            self.assertLessEqual(kwargs['timeout'], 1)
            self.assertNotIn('fixture-key', repr((args, kwargs)))
        # A subsequent request must still be admitted after timed-out hostname lookups.
        with patch('quivr_plugin.system_one.http.client.HTTPConnection.connect', side_effect=OSError):
            result = SystemOne('fixture-key', 'http://127.0.0.1').judge({}, {'q': {}}, time.monotonic() + 1)
        self.assertEqual(result.reason, 'transport failure')

    def test_bearer_credentials_require_https_or_numeric_loopback(self):
        for endpoint, allowed in [
            ('http://example.invalid', False), ('http://192.0.2.1', False),
            ('http://localhost', False), ('ftp://example.invalid', False),
            ('https://user:password@example.invalid', False),
            ('https://example.invalid', True), ('http://127.0.0.1', True), ('http://[::1]', True),
        ]:
            with self.subTest(endpoint=endpoint), \
                 patch('quivr_plugin.system_one.http.client.HTTPConnection.connect', side_effect=OSError) as connect:
                result = SystemOne('fixture-key', endpoint).judge({}, {'q': {}}, time.monotonic() + 1)
                self.assertEqual(result.reason, 'transport failure' if allowed else 'invalid endpoint')
                self.assertEqual(connect.call_count, int(allowed))
                self.assertEqual(result.paid_calls, 0)
