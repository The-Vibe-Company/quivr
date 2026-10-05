"""Offline owners for load scenario safety and report arithmetic; no stack or waits."""
import copy
import unittest
from unittest import mock
from types import SimpleNamespace

from load_report import distribution, request_summary
from load_scenarios import validate
from load import Workload
from load_stack import LoadStack, local_docker_host


class LoadContracts(unittest.TestCase):
    def test_cleanup_removes_containers_when_a_child_exits_before_signal(self):
        # The process manager and Docker are external boundaries. A vanished
        # process must never prevent teardown of our own stack.
        stack = LoadStack.__new__(LoadStack)
        stack.children = [SimpleNamespace(pid=123, poll=lambda: None, wait=lambda **kwargs: None)]
        stack.state = {'pids': [123]}
        with mock.patch.object(stack, 'save'), mock.patch.object(stack, 'compose') as compose:
            with mock.patch('os.killpg', side_effect=ProcessLookupError):
                stack.down()
        compose.assert_called_once_with('down', '--volumes', timeout=60)

    def test_docker_endpoint_is_local_even_with_a_selected_context(self):
        # Docker's context metadata is the owning external boundary. A remote
        # selection must fail before a stack or volume can be created.
        for host in ('ssh://remote.example', 'tcp://remote.example:2376'):
            with self.subTest(host=host), mock.patch.dict('os.environ', {'DOCKER_HOST': host}, clear=True):
                with self.assertRaisesRegex(ValueError, 'local Unix socket'):
                    local_docker_host()
        with mock.patch.dict('os.environ', {'DOCKER_CONTEXT': 'selected'}, clear=True):
            with mock.patch('subprocess.check_output', return_value='ssh://remote.example\n'):
                with self.assertRaisesRegex(ValueError, 'local Unix socket'):
                    local_docker_host()
            with mock.patch('subprocess.check_output', return_value='unix:///var/run/docker.sock\n'):
                self.assertEqual(local_docker_host(), 'unix:///var/run/docker.sock')

    def test_ingestion_crossing_a_fault_keeps_its_start_time(self):
        stack = SimpleNamespace(state={'admin': 'local'}, endpoints=lambda: ['http://127.0.0.1:1'])
        workload = Workload(stack, {'seed': 42, 'corpus': {'words_per_record': 5}}, None)
        workload.started = 100
        # The transport finishes after the fault at 130, but this request
        # belongs to the pre-fault window. No real wait or clock is involved.
        clock = [129]
        def transport(*args):
            clock[0] = 131
            return {'ms': 2000, 'status': 503, 'error': 'unavailable'}, {}
        with mock.patch('load.time.monotonic', side_effect=lambda: clock[0]), mock.patch('load.call',
                side_effect=transport):
            workload.submit(0, 128, True)
        self.assertLess(workload.requests[0]['at_seconds'], 30)

    def test_report_preserves_errors_and_uses_nearest_rank_percentiles(self):
        # A discarded timeout or interpolation instead of nearest rank would
        # understate this independently worked four-request measurement.
        rows = [{'ms': ms, 'status': status, 'error': error} for ms, status, error in
                [(10, 200, ''), (20, 200, ''), (30, 503, 'plugin_unavailable'), (100, None, 'TimeoutError')]]
        self.assertEqual(distribution([10, 20, 30, 100]),
                         {'count': 4, 'p50_ms': 20, 'p95_ms': 100, 'max_ms': 100})
        self.assertEqual(request_summary(rows, 2, 200), {
            'attempts': 4, 'errors': 2, 'error_rate': .5,
            'attempts_per_second': 2, 'successes_per_second': 1,
            'statuses': {'200': 2, '503': 1, 'transport_error': 1},
            'error_codes': {'plugin_unavailable': 1, 'TimeoutError': 1},
            'latency': {'count': 4, 'p50_ms': 20, 'p95_ms': 100, 'max_ms': 100}})
        self.assertIsNone(distribution([])['p95_ms'])

    def test_scenarios_reject_ambiguous_or_unsafe_workloads(self):
        scenario = {'version': 1, 'name': 'small', 'seed': 42,
                    'corpus': {'records': 20, 'words_per_record': 50},
                    'duration_seconds': 10, 'drain_seconds': 20,
                    'search': {'concurrency': 2, 'users': 10, 'mix':
                               {'lexical': 1, 'semantic': 1, 'hybrid': 1, 'deep': 1}},
                    'ingestion': {'per_second': 1, 'concurrency': 2, 'burst':
                                  {'at_seconds': 3, 'duration_seconds': 2, 'multiplier': 5}},
                    'alerts': 2, 'replicas': {'api': 2, 'worker': 2,
                    'kill_at_seconds': 5},
                    'fake_latency_ms': {'embedding': 2, 'reranking': 3, 'judge': 4}}
        self.assertEqual(validate(scenario), scenario)
        cases = [({'version': 2}, 'version'), ({'external_url': 'https://example.com'}, 'unknown'),
                 ({'replicas': {'api': 1, 'worker': 1, 'kill_at_seconds': 5}}, 'kill'),
                 ({'search': {'concurrency': 2, 'users': 10, 'mix': {'lexcial': 1}}}, 'mix'),
                 ({'alerts': True}, 'alerts'), ({'duration_seconds': float('nan')}, 'duration'),
                 ({'alerts': 10**400}, 'alerts'),
                 ({'duration_seconds': 86400, 'ingestion': {'per_second': 10000,
                   'concurrency': 2, 'burst': {'at_seconds': 0, 'duration_seconds': 86400,
                   'multiplier': 100}}}, 'arrivals'),
                 ({'fake_latency_ms': {'embedding': -1, 'reranking': 3, 'judge': 4}}, 'embedding')]
        for changes, message in cases:
            with self.subTest(changes=changes):
                invalid = copy.deepcopy(scenario)
                invalid.update(changes)
                with self.assertRaisesRegex(ValueError, message):
                    validate(invalid)
