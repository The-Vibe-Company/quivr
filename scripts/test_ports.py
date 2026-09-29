"""Harness ports never collide (THE-728). Runs without Docker.

Every service the harness starts gets a loopback port chosen before the
service binds it. These tests pin that no two services of a run can be handed
the same port, and that the kernel cannot give a chosen port to someone else
in the meantime because it lies outside the ephemeral range.
"""
import os, pathlib, shutil, socket, subprocess, sys, tempfile, unittest, uuid
from concurrent.futures import ThreadPoolExecutor
from unittest import mock

import local
import normalizer_plugin as np
import ports


class Leases(unittest.TestCase):
    """Isolate every test from the host's real lease directory."""
    def setUp(self):
        self.leases = tempfile.TemporaryDirectory()
        patcher = mock.patch.object(ports, 'LEASES', pathlib.Path(self.leases.name) / 'leases')
        patcher.start()
        self.addCleanup(patcher.stop)
        self.addCleanup(self.leases.cleanup)


class Allocation(Leases):
    def test_fifty_concurrent_allocations_never_repeat_a_port(self):
        for _ in range(20):
            with ThreadPoolExecutor(max_workers=50) as pool:
                got = list(pool.map(lambda _: ports.allocate(), range(50)))
            self.assertEqual(len(set(got)), 50, got)

    def test_allocated_ports_lie_below_the_ephemeral_range(self):
        first, _ = ports.ephemeral_range()
        if first - 1024 < ports.MINIMUM_BAND:
            self.skipTest('this host leaves no room below its ephemeral range')
        for _ in range(200):
            p = ports.allocate()
            self.assertGreaterEqual(p, 1024)
            self.assertLess(p, first)

    def test_a_port_someone_else_listens_on_is_skipped(self):
        low, high = ports.band()
        with socket.socket() as busy:
            taken = next(p for p in range(high - 1, low, -1) if p not in ports._reserved and ports._free(p))
            busy.bind(('127.0.0.1', taken))
            busy.listen()
            with mock.patch.object(ports.random, 'randrange', side_effect=[taken] * 3 + list(range(low, high))):
                self.assertNotEqual(ports.allocate(), taken)

    def test_a_reserved_port_is_never_handed_out(self):
        low, high = ports.band()
        reserved = next(p for p in range(high - 1, low, -1) if p not in ports._reserved and ports._free(p))
        ports.reserve([reserved])
        with mock.patch.object(ports.random, 'randrange', side_effect=[reserved] * 3 + list(range(low, high))):
            self.assertNotEqual(ports.allocate(), reserved)

    def test_a_port_leased_by_another_live_harness_process_is_skipped(self):
        low, high = ports.band()
        leased = next(p for p in range(high - 1, low, -1) if p not in ports._reserved and ports._free(p))
        ports.LEASES.mkdir(parents=True, mode=0o700)
        (ports.LEASES / str(leased)).write_text(str(os.getppid()))
        with mock.patch.object(ports.random, 'randrange', side_effect=[leased] * 3 + list(range(low, high))):
            self.assertNotEqual(ports.allocate(), leased)

    def test_a_lease_left_by_an_exited_process_is_reclaimed(self):
        low, high = ports.band()
        stale = next(p for p in range(high - 1, low, -1) if p not in ports._reserved and ports._free(p))
        gone = subprocess.run([sys.executable, '-c', 'import os; print(os.getpid())'], capture_output=True, text=True).stdout.strip()
        ports.LEASES.mkdir(parents=True, mode=0o700)
        (ports.LEASES / str(stale)).write_text(gone)
        with mock.patch.object(ports.random, 'randrange', side_effect=[stale]):
            self.assertEqual(ports.allocate(), stale)
        self.assertEqual((ports.LEASES / str(stale)).read_text(), str(os.getpid()))

    def test_the_band_stays_below_both_the_kernel_and_docker_ranges(self):
        cases = {(49152, 65535): (15000, 32768),  # macOS kernel; Docker Desktop publishes from 32768
                 (32768, 60999): (15000, 32768),  # Linux default
                 (20000, 60999): (15000, 20000),
                 (8000, 60999): (1024, 8000),
                 (1024, 65535): (15000, 32768)}   # no room below: tracked, not protected
        for kernel, expected in cases.items():
            with mock.patch.object(ports, 'ephemeral_range', return_value=kernel):
                self.assertEqual(ports.band(), expected, kernel)

    def test_a_lease_directory_others_can_write_is_refused(self):
        ports.LEASES.mkdir(parents=True, mode=0o777)
        ports.LEASES.chmod(0o777)
        with self.assertRaisesRegex(RuntimeError, 'private directory'):
            ports.allocate()

    def test_reading_the_ephemeral_range_falls_back_safely(self):
        with mock.patch.object(ports, '_read_range', side_effect=OSError):
            first, last = ports.ephemeral_range()
        self.assertLess(first, last)
        low, high = ports.band()
        self.assertLess(low, high)


class Stacks(Leases):
    def setUp(self):
        super().setUp()
        self.names = ['quivr-test-' + uuid.uuid4().hex[:10] for _ in range(50)]

    def tearDown(self):
        for name in self.names:
            shutil.rmtree(local.ROOT / '.scratch' / name, ignore_errors=True)

    def stack_ports(self, stack):
        return [v for k, v in stack.state.items() if k.endswith('_port')]

    def test_fifty_concurrent_stacks_with_their_plugins_share_no_port(self):
        def start(name):
            stack = local.Stack(name)
            stack.state.setdefault('plugin_port', np.stack_port())
            return self.stack_ports(stack)
        with ThreadPoolExecutor(max_workers=50) as pool:
            everything = [p for group in pool.map(start, self.names) for p in group]
        self.assertEqual(len(everything), 50 * 9)
        self.assertEqual(len(set(everything)), len(everything))

    def test_a_reloaded_stack_keeps_its_ports_and_new_ones_avoid_them(self):
        first = local.Stack(self.names[0])
        kept = {k: v for k, v in first.state.items() if k.endswith('_port')}
        with mock.patch.object(ports, '_reserved', set()):
            again = local.Stack(self.names[0])
            self.assertEqual({k: again.state[k] for k in kept}, kept)
            self.assertTrue(set(kept.values()) <= ports._reserved)
            self.assertNotIn(np.stack_port(), kept.values())


if __name__ == '__main__':
    unittest.main()
