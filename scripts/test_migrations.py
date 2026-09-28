"""Checks for the migration ordering guard."""
import datetime
import pathlib
import subprocess
import tempfile
import unittest

import migrations as m

NOW = datetime.datetime(2026, 9, 28, 15, 30, tzinfo=datetime.timezone.utc)
BASE = ['001_corpora.sql', '016_delivery_attempts.sql', '20260928T1400Z_on_main.sql']


class Problems(unittest.TestCase):
    def test_no_additions_pass(self):
        self.assertEqual(m.problems(BASE, BASE, NOW), [])

    def test_stamped_after_main_passes(self):
        self.assertEqual(m.problems(BASE, BASE + ['20260928T1500Z_mine.sql'], NOW), [])

    def test_stamped_before_main_latest_fails_with_exact_fix(self):
        [problem] = m.problems(BASE, BASE + ['20260928T1200Z_mine.sql'], NOW)
        self.assertIn('20260928T1200Z_mine.sql', problem)
        self.assertIn("main's latest migration is 20260928T1400Z_on_main.sql", problem)
        self.assertIn('git mv migrations/20260928T1200Z_mine.sql migrations/20260928T1530Z_mine.sql', problem)
        self.assertIn('make migration-restamp file=20260928T1200Z_mine.sql', problem)

    def test_new_numbered_file_fails_with_restamp_fix(self):
        [problem] = m.problems(BASE, BASE + ['017_connector_usage.sql'], NOW)
        self.assertIn('git mv migrations/017_connector_usage.sql migrations/20260928T1530Z_connector_usage.sql', problem)

    def test_restamps_keep_the_branch_order_and_stay_distinct(self):
        head = BASE + ['20260928T1200Z_first.sql', '20260928T1500Z_second.sql']
        problems = m.problems(BASE, head, NOW)
        text = '\n'.join(problems)
        self.assertIn('migrations/20260928T1530Z_first.sql', text)
        self.assertIn('migrations/20260928T1531Z_second.sql', text)

    def test_same_minute_as_main_latest_fails(self):
        [problem] = m.problems(BASE, BASE + ['20260928T1400Z_zzz_mine.sql'], NOW)
        self.assertIn('git mv migrations/20260928T1400Z_zzz_mine.sql migrations/20260928T1530Z_zzz_mine.sql', problem)

    def test_several_additions_only_offer_the_git_mv_fixes(self):
        head = BASE + ['20260928T1200Z_first.sql', '20260928T1500Z_second.sql']
        text = '\n'.join(m.problems(BASE, head, NOW))
        self.assertNotIn('make migration-restamp', text)
        self.assertNotIn('no other file needs to change', text)

    def test_restamp_never_lands_before_main_when_clock_lags(self):
        early = datetime.datetime(2026, 9, 28, 13, 0, tzinfo=datetime.timezone.utc)
        [problem] = m.problems(BASE, BASE + ['20260928T1200Z_mine.sql'], early)
        self.assertIn('migrations/20260928T1401Z_mine.sql', problem)

    def test_bad_name_fails(self):
        [problem] = m.problems(BASE, BASE + ['20260928T1500Z_Mine.sql'], NOW)
        self.assertIn('20260928T1500Z_Mine.sql', problem)

    def test_branch_behind_main_passes(self):
        # A branch cut before main's latest migration lacks it; that is not a failure.
        self.assertEqual(m.problems(BASE, BASE[:-1], NOW), [])
        self.assertEqual(m.missing(BASE, BASE[:-1]), ['20260928T1400Z_on_main.sql'])

    def test_renamed_main_migration_fails(self):
        head = [n for n in BASE if n != '016_delivery_attempts.sql'] + ['016_renamed.sql']
        [problem] = m.problems(BASE, head, NOW)
        self.assertIn('016_renamed.sql', problem)


def git(cwd, *args):
    subprocess.run(['git', *args], cwd=cwd, check=True, capture_output=True)


class BaseRef(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.tmp.name)
        git(self.root, 'init', '-q', '-b', 'main')
        git(self.root, 'config', 'user.email', 'test@example.invalid')
        git(self.root, 'config', 'user.name', 'test')
        (self.root / 'migrations').mkdir()
        (self.root / 'migrations' / '001_a.sql').write_text('SELECT 1;\n')
        git(self.root, 'add', '.')
        git(self.root, 'commit', '-q', '-m', 'base')

    def tearDown(self):
        self.tmp.cleanup()

    def test_lists_migrations_on_a_resolvable_base(self):
        self.assertEqual(m.base_names(self.root, 'main'), ['001_a.sql'])

    def test_unresolvable_base_fails_loudly(self):
        with self.assertRaises(m.BaseUnavailable) as raised:
            m.base_names(self.root, 'origin/main')
        self.assertIn('git fetch --no-tags --depth=1 origin main', str(raised.exception))


if __name__ == '__main__':
    unittest.main()
