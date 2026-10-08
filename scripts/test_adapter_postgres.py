"""make adapter-postgres isolation and cleanup (THE-699).

These run without Docker: Compose and `go test` are replaced by recorders, so
they prove which project is started and removed on success, failure and
interrupt. The live proof is `make adapter-postgres` itself (CI job
adapter-postgres).
"""
import io, json, os, shutil, stat, unittest
from unittest import mock

import adapter_postgres as ap
import verify_report as vr


class Recorder:
    """Stands in for one project's Compose calls; `fail` names the subcommand that fails."""
    def __init__(self, fail=None, raises=None):
        self.calls, self.fail, self.raises = [], fail, raises

    def __call__(self, *args, **kwargs):
        self.calls.append(args)
        if args[0] == self.fail:
            raise self.raises or RuntimeError(args[0] + ' failed')
        if args[0] == 'port':
            return mock.Mock(stdout='127.0.0.1:54321\n')
        if 'stdout' in kwargs and hasattr(kwargs['stdout'], 'write'):
            kwargs['stdout'].write('captured ' + args[0] + '\n')
        return mock.Mock(stdout='')

    def verbs(self):
        return [c[0] for c in self.calls]


class AdapterPostgres(unittest.TestCase):
    def setUp(self):
        self.projects = []
        for stream in ('stdout', 'stderr'):
            patcher = mock.patch('sys.' + stream, io.StringIO())
            patcher.start()
            self.addCleanup(patcher.stop)

    def tearDown(self):
        for p in self.projects:
            shutil.rmtree(p.directory, ignore_errors=True)

    def project(self, recorder):
        p = ap.Project()
        self.projects.append(p)
        p.compose = recorder
        return p

    def test_each_run_has_its_own_project_secret_and_private_directory(self):
        a, b = self.project(Recorder()), self.project(Recorder())
        self.assertNotEqual(a.name, b.name)
        self.assertTrue(a.name.startswith('quivr-adapter-pg-'))
        self.assertNotEqual(a.password, b.password)
        self.assertGreaterEqual(len(a.password), 32)
        self.assertEqual(stat.S_IMODE(a.directory.stat().st_mode), 0o700)

    def test_config_names_only_this_database_and_stays_private(self):
        p = self.project(Recorder())
        config = p.start()
        self.assertEqual(json.loads(config.read_text()), {'database_url': f'postgres://quivr:{p.password}@127.0.0.1:54321/quivr?sslmode=disable'})
        self.assertEqual(stat.S_IMODE(config.stat().st_mode), 0o600)
        self.assertEqual(p.compose.calls[0], ('up', '-d', '--wait', '--wait-timeout', '120', 'postgres'))

    def run_main(self, recorder, tests):
        p = self.project(recorder)
        with mock.patch.object(ap.Project, 'test', tests), mock.patch.object(ap, 'Project', return_value=p):
            return p, ap.main([])

    def test_success_captures_logs_then_removes_only_its_project(self):
        p, code = self.run_main(Recorder(), lambda self, config, args: None)
        self.assertEqual(code, 0)
        self.assertEqual(p.compose.verbs(), ['up', 'exec', 'exec', 'restart', 'up', 'exec', 'port', 'logs', 'ps', 'down'])
        self.assertEqual(p.compose.calls[-1], ('down', '--volumes'))
        self.assertTrue((p.directory / 'postgres.log').exists())
        self.assertFalse((p.directory / 'config.json').exists(), 'credentials outlive the database')

    def test_failed_tests_still_capture_and_clean_up(self):
        def failing(self, config, args):
            raise ap.subprocess.CalledProcessError(1, 'go test')
        p, code = self.run_main(Recorder(), failing)
        self.assertEqual(code, 1)
        self.assertEqual(p.compose.verbs()[-3:], ['logs', 'ps', 'down'])

    def test_failed_start_still_cleans_up(self):
        p, code = self.run_main(Recorder(fail='up'), lambda self, config, args: None)
        self.assertEqual(code, 1)
        self.assertEqual(p.compose.verbs()[-1], 'down')

    def test_interrupt_cleans_up_and_reports_interruption(self):
        def interrupted(self, config, args):
            raise KeyboardInterrupt
        p, code = self.run_main(Recorder(), interrupted)
        self.assertEqual(code, 130)
        self.assertEqual(p.compose.verbs()[-1], 'down')

    def test_sigterm_cleans_up(self):
        def terminated(self, config, args):
            raise vr.Interrupted()
        p, code = self.run_main(Recorder(), terminated)
        self.assertEqual(code, 130)
        self.assertEqual(p.compose.verbs()[-1], 'down')

    def test_keep_on_failure_keeps_the_project(self):
        def failing(self, config, args):
            raise ap.subprocess.CalledProcessError(1, 'go test')
        with mock.patch.dict(os.environ, {'QUIVR_KEEP_ON_FAILURE': '1'}):
            p, code = self.run_main(Recorder(), failing)
        self.assertEqual(code, 1)
        self.assertNotIn('down', p.compose.verbs())

    def test_logs_are_redacted(self):
        p = self.project(Recorder())
        def leaky(self, config, args):
            (self.directory / 'adapter-postgres.log').write_text('dsn ' + self.password + '\n')
        with mock.patch.object(ap.Project, 'test', leaky), mock.patch.object(ap, 'Project', return_value=p):
            ap.main([])
        self.assertNotIn(p.password, (p.directory / 'adapter-postgres.log').read_text())


if __name__ == '__main__':
    unittest.main()
