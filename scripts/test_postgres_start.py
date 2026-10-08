"""Operator startup settings, at the executable entrypoint boundary."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class PostgresStart(unittest.TestCase):
    def run_start(self, settings, args=()):
        with tempfile.TemporaryDirectory() as directory:
            entry = Path(directory) / 'docker-entrypoint.sh'
            entry.write_text('#!/usr/bin/env python3\nimport json,sys\nprint(json.dumps(sys.argv[1:]))\n')
            entry.chmod(0o755)
            env = {k: v for k, v in os.environ.items() if not k.startswith('QUIVR_POSTGRES_')}
            env.update(settings, PATH=directory + os.pathsep + env['PATH'])
            return subprocess.run(['sh', str(ROOT / 'deploy/postgres/start.sh'), *args],
                                  env=env, capture_output=True, text=True)

    def test_memory_budget_and_overrides_reach_postgres(self):
        for budget, buffers, cache, maintenance in [('1024', '256MB', '768MB', '64MB'),
                                                   ('01024', '256MB', '768MB', '64MB'),
                                                   ('000128', '16MB', '96MB', '16MB'),
                                                   ('512', '64MB', '384MB', '32MB'),
                                                   ('32768', '8192MB', '24576MB', '512MB')]:
            with self.subTest(budget=budget):
                result = self.run_start({'QUIVR_POSTGRES_MEMORY_MB': budget}, ('-c', 'port=54342'))
                self.assertEqual(result.returncode, 0, result.stderr)
                argv = json.loads(result.stdout)
                self.assertEqual(argv[0], 'postgres')
                for setting in ['max_connections=256', 'shared_buffers=' + buffers,
                                'effective_cache_size=' + cache, 'work_mem=4MB',
                                'maintenance_work_mem=' + maintenance,
                                'dynamic_shared_memory_type=mmap',
                                'shared_preload_libraries=pg_stat_statements', 'track_io_timing=on',
                                'synchronous_commit=on', 'fsync=on', 'full_page_writes=on']:
                    self.assertIn(setting, argv)
                self.assertEqual(argv[-2:], ['-c', 'port=54342'])
        result = self.run_start({'QUIVR_POSTGRES_MEMORY_MB': '1024',
                                 'QUIVR_POSTGRES_MAX_CONNECTIONS': '080',
                                 'QUIVR_POSTGRES_SHARED_BUFFERS': '128MB',
                                 'QUIVR_POSTGRES_WORK_MEM': '2MB'})
        self.assertEqual(result.returncode, 0, result.stderr)
        argv = json.loads(result.stdout)
        for setting in ['max_connections=80', 'shared_buffers=128MB', 'work_mem=2MB']:
            self.assertIn(setting, argv)

    def test_invalid_budget_and_limits_refuse_startup(self):
        for settings in [{'QUIVR_POSTGRES_MEMORY_MB': value} for value in ['0', '-1', 'bad', '32']] + [
                {'QUIVR_POSTGRES_MEMORY_MB': '1024', 'QUIVR_POSTGRES_MAX_CONNECTIONS': '0'},
                {'QUIVR_POSTGRES_MEMORY_MB': '1024', 'QUIVR_POSTGRES_WORK_MEM': 'bad'}]:
            with self.subTest(settings=settings):
                result = self.run_start(settings)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, '')

    def test_other_image_commands_are_forwarded(self):
        result = self.run_start({}, ('postgres', '--version'))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), ['postgres', '--version'])


if __name__ == '__main__':
    unittest.main()
