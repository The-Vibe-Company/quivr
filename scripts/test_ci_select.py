"""Owner of changed-path routing and selected-job completion contracts."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import ci_select


class Selection(unittest.TestCase):
    def test_docs_front_eval_and_plugin_select_only_affected_expensive_lanes(self):
        cases = [
            (['docs-site/guides/search.mdx'], [], [], True),
            (['contracts/http/v0/README.md'], [], [], True),
            (['third_party/README.md'], [], [], True),
            (['conformance/README.md'], [], [], True),
            (['scripts/eval/README.md'], [], [], True),
            (['quivr-search/src/App.tsx'], ['demo'], [], False),
            (['scripts/eval/campaign.py'], [], ['eval-1', 'eval-2', 'eval-3'], False),
            (['plugins/rss/connector.go'], ['connectors'], ['sdk-go'], False),
        ]
        for paths, parts, extra_groups, site in cases:
            with self.subTest(paths=paths):
                selected = ci_select.select(paths)
                self.assertEqual(selected['parts'], parts)
                self.assertEqual(selected['groups'], ['guards', 'go', 'python', *extra_groups])
                self.assertEqual(selected['site'], site)
                if paths[0].startswith('plugins/'):
                    self.assertEqual([i['plugin'] for i in selected['images']], ['rss'])
                else:
                    self.assertEqual(selected['images'], [])

    def test_evaluation_dependencies_and_engine_manifest_inputs_are_selected(self):
        for path in ['plugins/jev-rerank/cache.py', 'plugins/core-retrieve/main.go',
                     'plugins/core-ingest/main.go', 'plugins/hosted-embed/main.go',
                     'migrations/expansion.sql', 'internal/retrieval/retrieval.go']:
            with self.subTest(path=path):
                self.assertTrue({'eval-1', 'eval-2', 'eval-3'} <= set(ci_select.select([path])['groups']))
        self.assertEqual([i['plugin'] for i in ci_select.select(['plugins/rss/quivr-plugin.yaml'])['images']], ['', 'rss'])
        self.assertEqual([i['plugin'] for i in ci_select.select(['plugins/rss/connector.go'])['images']], ['rss'])
        self.assertTrue(ci_select.select(['tests/fixtures/runtime.md'])['full'])

    def test_engine_shared_inputs_and_unknown_paths_fail_closed(self):
        for path in ['internal/retrieval/retrieval.go', 'scripts/local.py', 'go.sum', 'unclassified/new-input.bin']:
            with self.subTest(path=path):
                selected = ci_select.select([path])
                self.assertEqual(selected['parts'], ['core', 'monitoring', 'plugins', 'connectors', 'demo'])
                self.assertTrue(selected['images'])
        for full in [True]:
            selected = ci_select.select(['README.md'], full=full)
            self.assertIn('eval-3', selected['groups'])
            self.assertTrue(selected['site'])
            self.assertTrue(selected['migration-compatibility'])

    def test_git_diff_covers_deleted_and_both_rename_paths_and_missing_base(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            def git(*args):
                return subprocess.check_output(['git', *args], cwd=root, text=True).strip()
            git('init', '-q')
            git('config', 'user.email', 'fixture@example.invalid')
            git('config', 'user.name', 'Fixture')
            git('config', 'commit.gpgsign', 'false')
            (root / 'old.txt').write_text('retained\n')
            (root / 'deleted.txt').write_text('removed\n')
            git('add', '.')
            git('commit', '-qm', 'fixture baseline')
            base = git('rev-parse', 'HEAD')
            git('mv', 'old.txt', 'new.txt')
            git('rm', 'deleted.txt')
            git('commit', '-qm', 'fixture changed')
            self.assertEqual(set(ci_select.changed_paths(base, 'HEAD', root)), {'old.txt', 'new.txt', 'deleted.txt'})
            self.assertIsNone(ci_select.changed_paths('missing-base', 'HEAD', root))

    def test_cli_main_label_and_reusable_release_select_full_and_write_outputs(self):
        for event in [{'event_name': 'push'}, {'event_name': 'workflow_call'},
                      {'event_name': 'pull_request', 'pull_request': {'labels': [{'name': 'ci-full'}]}}]:
            with self.subTest(event=event), tempfile.TemporaryDirectory() as folder:
                event_file = Path(folder) / 'event.json'
                event_file.write_text(json.dumps(event))
                output = Path(folder) / 'outputs'
                result = subprocess.run(['python3', str(ci_select.ROOT / 'scripts/ci_select.py')],
                                        env={**os.environ, 'GITHUB_EVENT_NAME': event['event_name'],
                                             'GITHUB_EVENT_PATH': str(event_file), 'GITHUB_OUTPUT': str(output)},
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                values = dict(line.split('=', 1) for line in output.read_text().splitlines())
                self.assertEqual(json.loads(values['parts'])['part'], ['core', 'monitoring', 'plugins', 'connectors', 'demo'])
                self.assertEqual(values['site'], 'true')
                self.assertEqual(values['full'], 'true')


class Aggregation(unittest.TestCase):
    def test_only_explicitly_irrelevant_skips_pass(self):
        selection = {'part': False, 'site': True}
        self.assertTrue(ci_select.complete(selection, {'paths-filter': 'success', 'part': 'skipped', 'site': 'success'}))
        for job, result in [('paths-filter', 'failure'), ('part', 'failure'), ('site', 'skipped'),
                            ('site', 'cancelled'), ('site', 'failure')]:
            with self.subTest(job=job, result=result):
                results = {'paths-filter': 'success', 'part': 'skipped', 'site': 'success', job: result}
                self.assertFalse(ci_select.complete(selection, results))
        self.assertFalse(ci_select.complete(selection, {'paths-filter': 'success', 'part': 'skipped'}))
        flags = ['has-parts', 'has-images', 'site', 'migration-compatibility', 'vulnerabilities', 'full']
        needs = {'paths-filter': {'result': 'success', 'outputs': {key: 'false' for key in flags}},
                 'check': {'result': 'success'}, 'adapter-postgres': {'result': 'success'},
                 **{key: {'result': 'skipped'} for key in ['part', 'site', 'migration-compatibility',
                                                         'vulnerabilities', 'image-inventory', 'image-vulnerabilities']}}
        for value, expected in [('false', 0), ('true', 1), ('invalid', 1), (None, 1)]:
            with self.subTest(output=value):
                needs['paths-filter']['outputs']['has-images'] = value
                result = subprocess.run(['python3', str(ci_select.ROOT / 'scripts/ci_select.py'), '--aggregate', 'verify'],
                                        env={**os.environ, 'CI_NEEDS': json.dumps(needs)}, capture_output=True)
                self.assertEqual(result.returncode, expected)



if __name__ == '__main__':
    unittest.main()
