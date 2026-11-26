"""Owner of changed-path routing and selected-job completion contracts."""
import json
import os
import re
from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml

import ci_select


class Selection(unittest.TestCase):
    def test_docs_front_eval_and_plugin_select_only_affected_expensive_lanes(self):
        cases = [
            (['docs-site/guides/search.mdx'], [], [], True),
            (['contracts/http/v0/README.md'], [], [], True),
            (['third_party/README.md'], [], [], True),
            (['conformance/README.md'], [], [], True),
            (['scripts/docs.py'], [], [], True),
            (['scripts/mintlify_site.py'], [], [], True),
            (['scripts/test_ci_select.py'], [], [], False),
            (['scripts/denylist.sha256'], [], [], False),
            (['.agents/skills/example/README.md'], [], [], False),
            (['quivr-search/src/App.tsx'], ['demo'], [], False),
            (['scripts/denylist.py', 'docs/start/contributor.md'], [], [], True),
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
                    self.assertEqual([i.get('component', i['plugin']) for i in selected['images']], ['rss', 'core'])
                elif paths[0].startswith('quivr-search/'):
                    self.assertEqual([i['component'] for i in selected['images']], ['web'])
                else:
                    self.assertEqual(selected['images'], [])

    def test_evaluation_dependencies_and_engine_manifest_inputs_are_selected(self):
        for path in ['plugins/jev-rerank/cache.py', 'plugins/core-retrieve/main.go',
                     'plugins/core-ingest/main.go', 'plugins/hosted-embed/main.go',
                     'migrations/expansion.sql', 'internal/retrieval/retrieval.go']:
            with self.subTest(path=path):
                self.assertTrue({'eval-1', 'eval-2', 'eval-3'} <= set(ci_select.select([path])['groups']))
        self.assertEqual([i.get('component', i['plugin']) for i in ci_select.select(['plugins/rss/quivr-plugin.yaml'])['images']], ['', 'rss', 'core'])
        self.assertEqual([i.get('component', i['plugin']) for i in ci_select.select(['plugins/rss/connector.go'])['images']], ['rss', 'core'])
        self.assertTrue(ci_select.select(['tests/fixtures/runtime.md'])['full'])

    def test_engine_sources_and_root_go_dependencies_select_engine_and_core_images(self):
        for path in ['internal/retrieval/retrieval.go', 'go.mod', 'go.sum']:
            with self.subTest(path=path):
                selected = ci_select.select([path])
                self.assertEqual(selected['parts'], ['core', 'monitoring', 'plugins', 'connectors', 'demo'])
                self.assertEqual([i.get('component', i['plugin']) for i in selected['images']], ['', 'core'])
                self.assertTrue(selected['vulnerabilities'])
                self.assertFalse(selected['full'])
                if path in {'go.mod', 'go.sum'}:
                    self.assertIn('contracts', selected['groups'])
                    self.assertTrue(selected['migration-compatibility'])

    def test_sdk_sources_select_images_using_that_language(self):
        for path, group, included, excluded in [
            ('sdks/go/go.sum', 'sdk-go', {'rss', 'core-ingest', 'hosted-embed'}, {'pdf-text', 'jev-rerank'}),
            ('sdks/python/README.md', 'sdk-python', {'pdf-text', 'jev-rerank'}, {'rss', 'core-ingest', 'hosted-embed'}),
        ]:
            with self.subTest(path=path):
                selected = ci_select.select([path])
                images = {i.get('component', i['plugin']) for i in selected['images']}
                self.assertTrue(included | {'core'} <= images)
                self.assertFalse(images & (excluded | {'', 'web', 'postgres'}))
                self.assertIn(group, selected['groups'])
                self.assertEqual(selected['site'], path.endswith('README.md'))

    def test_shared_inputs_and_missing_diff_fail_closed(self):
        for paths in [None, ['scripts/local.py'], ['scripts/prepare_tokenizer.py'],
                      ['tools/cosign/go.sum'], ['.github/workflows/verify.yml'],
                      ['unclassified/README.md'], ['plugins/new-plugin/main.go']]:
            with self.subTest(paths=paths):
                selected = ci_select.select(paths)
                self.assertTrue(selected['full'])
                self.assertTrue(selected['site'])
                self.assertTrue(selected['migration-compatibility'])
                self.assertTrue(selected['vulnerabilities'])
                self.assertIn('eval-3', selected['groups'])
                self.assertIn('postgres', {image.get('component') for image in selected['images']})

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
                      {'event_name': 'workflow_dispatch'},
                      {'event_name': 'pull_request', 'attempt': '2',
                       'pull_request': {'base': {'sha': 'HEAD'}, 'head': {'sha': 'HEAD'}}},
                      {'event_name': 'pull_request', 'pull_request': {'labels': [{'name': 'ci-full'}]}}]:
            with self.subTest(event=event), tempfile.TemporaryDirectory() as folder:
                event_file = Path(folder) / 'event.json'
                event_file.write_text(json.dumps(event))
                output = Path(folder) / 'outputs'
                result = subprocess.run(['python3', str(ci_select.ROOT / 'scripts/ci_select.py')],
                                        env={**os.environ, 'GITHUB_EVENT_NAME': event['event_name'],
                                             'GITHUB_EVENT_PATH': str(event_file), 'GITHUB_OUTPUT': str(output),
                                             'GITHUB_RUN_ATTEMPT': event.get('attempt', '1')},
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                values = dict(line.split('=', 1) for line in output.read_text().splitlines())
                self.assertEqual(json.loads(values['parts'])['part'], ['core', 'monitoring', 'plugins', 'connectors', 'demo'])
                self.assertEqual(values['site'], 'true')
                self.assertEqual(values['full'], 'true')
                self.assertEqual(values['vulnerabilities'], 'true')
                self.assertEqual(values['migration-compatibility'], 'true')
                images = json.loads(values['images'])['include']
                self.assertTrue({'postgres', 'web', 'tei'} <= {i.get('component') for i in images})


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
                                                         'vulnerabilities', 'image-inventory', 'image-vulnerabilities', 'single-machine']}}
        for value, expected in [('false', 0), ('true', 1), ('invalid', 1), (None, 1)]:
            with self.subTest(output=value):
                needs['paths-filter']['outputs']['has-images'] = value
                result = subprocess.run(['python3', str(ci_select.ROOT / 'scripts/ci_select.py'), '--aggregate', 'verify'],
                                        env={**os.environ, 'CI_NEEDS': json.dumps(needs)}, capture_output=True)
                self.assertEqual(result.returncode, expected)
        needs['paths-filter']['outputs']['has-images'] = 'false'
        needs['paths-filter']['outputs']['full'] = 'true'
        for state, expected in [('skipped', 1), ('failure', 1), ('success', 0)]:
            needs['single-machine']['result'] = state
            result = subprocess.run(['python3', str(ci_select.ROOT / 'scripts/ci_select.py'), '--aggregate', 'verify'],
                                    env={**os.environ, 'CI_NEEDS': json.dumps(needs)}, capture_output=True)
            self.assertEqual(result.returncode, expected)


class Workflow(unittest.TestCase):
    def test_image_scans_use_their_producer_output_on_reruns(self):
        prefixes = set()
        for file, producer, consumer in [
            ('verify.yml', 'image-inventory', 'image-vulnerabilities'),
            ('release-images.yml', 'prepare', 'build'),
        ]:
            with self.subTest(file=file):
                jobs = yaml.safe_load((ci_select.ROOT / '.github/workflows' / file).read_text())['jobs']
                build = next(s for s in jobs[producer]['steps'] if s.get('uses') == './.github/actions/grype-database')
                self.assertEqual(jobs[producer]['outputs']['artifact-id'], '${{ steps.' + build['id'] + '.outputs.artifact-id }}')
                prefixes.add(build['with']['artifact-prefix'])
                restore = next(s for s in jobs[consumer]['steps'] if s.get('uses') == './.github/actions/grype-database-restore')
                self.assertIn(producer, jobs[consumer]['needs'])
                # A failed-only rerun retains the successful producer's job outputs.
                self.assertEqual(restore['with']['artifact-id'], '${{ needs.' + producer + '.outputs.artifact-id }}')
                self.assertIs(jobs[consumer]['env']['GRYPE_DB_VALIDATE_AGE'], True)
        self.assertEqual(len(prefixes), 2, 'release publishing and verification share a workflow run')

    def test_metadata_events_cannot_emit_verification_check_names(self):
        workflow = yaml.safe_load((ci_select.ROOT / '.github/workflows/verify.yml').read_text())
        self.assertEqual(set(workflow['on']['pull_request']['types']),
                         {'opened', 'synchronize', 'reopened'})
        required_names = {job.get('name', name) for name, job in workflow['jobs'].items()}
        metadata = yaml.safe_load((ci_select.ROOT / '.github/workflows/verify-metadata.yml').read_text())
        self.assertEqual(set(metadata['on']['pull_request']['types']), {'labeled', 'unlabeled', 'edited'})
        for name, job in metadata['jobs'].items():
            emitted = job.get('name', name)
            self.assertNotIn('${{', emitted, 'metadata check names must be fixed')
            for required in required_names:
                pattern = '.*'.join(re.escape(part) for part in re.split(r'\$\{\{.*?\}\}', required))
                self.assertNotRegex(emitted, '^' + pattern + '$')
            self.assertNotIn('uses', job, 'metadata must dispatch, not call verification jobs inline')

        def evaluate(value, context, condition=False):
            def expression(match):
                def reference(match):
                    current = context
                    for key in match.group().split('.'):
                        current = current.get(key, '') if isinstance(current, dict) else ''
                    return repr(current)
                source = re.sub(r'\b(?:github|inputs|needs|matrix)\.[\w.-]+', reference, match.group(1))
                return eval(source.replace('&&', ' and ').replace('||', ' or '), {'__builtins__': {}, 'format': lambda template, *args: template.format(*args)})
            if condition and not value.strip().startswith('${{'):
                value = '${{ ' + value + ' }}'
            if value.strip().startswith('${{') and value.strip().endswith('}}'):
                return expression(re.fullmatch(r'\s*\$\{\{(.*?)\}\}\s*', value, re.S))
            return re.sub(r'\$\{\{(.*?)\}\}', lambda match: str(expression(match)), value, flags=re.S)

        cases = [(action, {'label': {'name': label}}, label == 'ci-full')
                 for action in ['labeled', 'unlabeled'] for label in ['ci-full', 'ready-to-merge']]
        cases += [('edited', {'changes': changes}, relevant) for changes, relevant in [
            ({'base': {'ref': {'from': 'main'}}}, True),
            ({'body': {'from': 'old description'}}, False),
            ({'title': {'from': 'old title'}}, False), ({}, False),
        ]]
        for action, fields, relevant in cases:
            with self.subTest(action=action, fields=fields):
                context = {'github': {'event': {'action': action, **fields}}}
                self.assertEqual(evaluate(metadata['jobs']['request-verification']['if'], context, True), relevant)
        for event, inputs, pr_number, expected, cancel in [
            ('pull_request', {}, 42, 'verify-42', True),
            ('pull_request', {}, 43, 'verify-43', True),
            ('workflow_dispatch', {}, '', 'verify-refs/heads/main', False),
            ('push', {}, '', 'verify-refs/heads/main', False),
            ('release', {'ref': 'release-ref'}, '', 'verify-release-ref', False),
        ]:
            with self.subTest(event=event, inputs=inputs):
                context = {'github': {'event_name': event, 'ref': 'refs/heads/main',
                                     'event': {'pull_request': {'number': pr_number}}}, 'inputs': inputs}
                self.assertEqual(evaluate(workflow['concurrency']['group'], context), expected)
                self.assertEqual(evaluate(workflow['concurrency']['cancel-in-progress'], context), cancel)
        checkout_ref = workflow['jobs']['paths-filter']['steps'][0]['with']['ref']
        for event, attempt, inputs, expected in [
            ('pull_request', 1, {}, 'original-merge'),
            ('pull_request', 2, {}, 'refs/pull/42/merge'),
            ('workflow_call', 2, {'ref': 'release-sha'}, 'release-sha'),
            ('workflow_dispatch', 1, {}, 'original-merge'),
        ]:
            with self.subTest(checkout_event=event, attempt=attempt):
                context = {'github': {'event_name': event, 'run_attempt': attempt, 'sha': 'original-merge',
                                     'event': {'pull_request': {'number': 42}}}, 'inputs': inputs}
                self.assertEqual(evaluate(checkout_ref, context), expected)

        build = next(step['with'] for step in workflow['jobs']['image-vulnerabilities']['steps']
                     if step.get('uses', '').startswith('docker/build-push-action@'))
        for event, ref, exports in [
            ('push', 'refs/heads/main', True),
            ('push', 'refs/heads/topic', False),
            ('pull_request', 'refs/pull/42/merge', False),
            ('release', 'refs/tags/v2.0.0', False),
            ('workflow_dispatch', 'refs/heads/main', False),
        ]:
            with self.subTest(cache_event=event, ref=ref):
                context = {'github': {'event_name': event, 'ref': ref}, 'matrix': {'component': 'core'}}
                self.assertEqual(evaluate(build['cache-from'], context), 'type=gha,scope=scan-core')
                self.assertEqual(evaluate(build['cache-to'], context),
                                 'type=gha,mode=max,scope=scan-core' if exports else '')

        migration_command = next(step['run'] for step in workflow['jobs']['migration-compatibility']['steps']
                                 if step.get('run', '').startswith('make migration-compatibility'))
        for base, before, expected in [('current-base', 'push-before', 'current-base'),
                                       ('', 'push-before', 'push-before'), ('', '', 'HEAD^')]:
            with self.subTest(migration_base=base, before=before):
                context = {'needs': {'paths-filter': {'outputs': {'base': base}}},
                           'github': {'event': {'before': before, 'pull_request': {'base': {'sha': 'stale-base'}}}}}
                self.assertEqual(evaluate(migration_command, context),
                                 f"make migration-compatibility args='--base {expected}'")

    def test_metadata_reruns_only_current_head_pull_request_verification(self):
        metadata = yaml.safe_load((ci_select.ROOT / '.github/workflows/verify-metadata.yml').read_text())
        job = metadata['jobs']['request-verification']
        # GitHub may spend five minutes forcibly canceling the prior run.
        self.assertGreater(job['timeout-minutes'], 5)
        self.assertFalse(any('uses' in step for step in job['steps']))
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            gh = root / 'gh'
            gh.write_text('\n'.join([
                '#!/usr/bin/env python3', 'import json, os, pathlib, sys',
                'args = sys.argv[1:]',
                'with open(os.environ["CALLS"], "a") as log: log.write(json.dumps(args) + "\\n")',
                'if args == ["api", "repos/example/repo/pulls/7"]:',
                '    print(pathlib.Path(os.environ["FIXTURE_PR"]).read_text())',
                'elif args == ["api", "repos/example/repo/actions/workflows/verify.yml/runs?event=pull_request&head_sha=head-sha&per_page=100"]:',
                '    print(pathlib.Path(os.environ["FIXTURE_RUNS"]).read_text())',
                'else:',
                '    assert args in [["run", "cancel", "21", "--repo", "example/repo"], ["run", "watch", "21", "--interval", "1", "--repo", "example/repo"],',
                '                    ["api", "--method", "POST", "repos/example/repo/actions/runs/21/rerun"]], args',
            ]) + '\n')
            gh.chmod(0o755)
            pr, runs, calls = root / 'pr.json', root / 'runs-response.json', root / 'calls.jsonl'
            def invoke():
                calls.write_text('')
                return subprocess.run(['bash', '-e', '-o', 'pipefail', '-c', job['steps'][0]['run']],
                                      capture_output=True, text=True,
                                      env={**os.environ, 'PATH': str(root) + os.pathsep + os.environ['PATH'],
                                           'GITHUB_REPOSITORY': 'example/repo', 'PR_NUMBER': '7',
                                           'RUNNER_TEMP': str(root), 'FIXTURE_PR': str(pr),
                                           'FIXTURE_RUNS': str(runs), 'CALLS': str(calls)})
            pr.write_text(json.dumps({'number': 7, 'state': 'open', 'head': {'sha': 'head-sha',
                                      'repo': {'full_name': 'example/repo'}}}))
            for status in ['completed', 'in_progress', 'queued']:
                with self.subTest(status=status):
                    runs.write_text(json.dumps({'workflow_runs': [
                        {'id': 25, 'event': 'workflow_dispatch', 'head_sha': 'head-sha', 'status': 'completed', 'pull_requests': [{'number': 7}]},
                        {'id': 24, 'event': 'pull_request', 'head_sha': 'stale-head', 'status': 'completed', 'pull_requests': [{'number': 7}]},
                        {'id': 23, 'event': 'pull_request', 'head_sha': 'head-sha', 'status': 'completed', 'pull_requests': [{'number': 8}]},
                        {'id': 21, 'event': 'pull_request', 'head_sha': 'head-sha', 'status': status, 'pull_requests': [{'number': 7}]},
                        {'id': 20, 'event': 'pull_request', 'head_sha': 'head-sha', 'status': 'completed', 'pull_requests': [{'number': 7}]},
                    ]}))
                    result = invoke()
                    self.assertEqual(result.returncode, 0, result.stderr)
                    emitted = [json.loads(line) for line in calls.read_text().splitlines()]
                    expected = [['api', 'repos/example/repo/pulls/7'],
                                ['api', 'repos/example/repo/actions/workflows/verify.yml/runs?event=pull_request&head_sha=head-sha&per_page=100']]
                    if status != 'completed':
                        expected += [['run', 'cancel', '21', '--repo', 'example/repo'], ['run', 'watch', '21', '--interval', '1', '--repo', 'example/repo']]
                    expected += [['api', '--method', 'POST', 'repos/example/repo/actions/runs/21/rerun']]
                    self.assertEqual(emitted, expected)
            runs.write_text(json.dumps({'workflow_runs': []}))
            self.assertNotEqual(invoke().returncode, 0)
            self.assertFalse(any('POST' in json.loads(line) for line in calls.read_text().splitlines()))
            pr.write_text(json.dumps({'number': 7, 'state': 'closed'}))
            self.assertEqual(invoke().returncode, 0)
            self.assertEqual(len(calls.read_text().splitlines()), 1)

    def test_rerun_checks_bind_current_merge_to_original_pr_head(self):
        workflow = yaml.safe_load((ci_select.ROOT / '.github/workflows/verify.yml').read_text())
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            def git(*args):
                return subprocess.check_output(['git', *args], cwd=root, text=True).strip()
            git('init', '-q', '--initial-branch=base')
            git('config', 'user.email', 'fixture@example.invalid')
            git('config', 'user.name', 'Fixture')
            git('config', 'commit.gpgsign', 'false')
            (root / 'base.txt').write_text('base\n')
            git('add', 'base.txt')
            git('commit', '-qm', 'fixture base')
            git('switch', '-qc', 'feature')
            (root / 'head.txt').write_text('head\n')
            git('add', 'head.txt')
            git('commit', '-qm', 'fixture head')
            head = git('rev-parse', 'HEAD')
            git('switch', '-q', 'base')
            (root / 'base.txt').write_text('advanced base\n')
            git('commit', '-qam', 'fixture advanced base')
            base = git('rev-parse', 'HEAD')
            git('merge', '--no-edit', 'feature')
            merge = git('rev-parse', 'HEAD')
            checkout = root / 'checkout'
            git('clone', '-q', '--depth', '2', root.as_uri(), str(checkout))
            resolver = next(step for step in workflow['jobs']['paths-filter']['steps'] if step.get('id') == 'resolve')
            for target, expected in [(head, 0), (base, 1)]:
                with self.subTest(target=target):
                    output = root / 'output'
                    output.write_text('')
                    result = subprocess.run(['bash', '-e', '-c', resolver['run']], cwd=checkout,
                                            env={**os.environ, 'PR_HEAD': target, 'EVENT_NAME': 'pull_request',
                                                 'RUN_ATTEMPT': '2', 'GITHUB_OUTPUT': str(output)}, capture_output=True)
                    self.assertEqual(result.returncode, expected)
                    self.assertEqual(output.read_text(), f'base={base}\nref={merge}\n' if expected == 0 else '')
            self.assertIn('paths-filter', workflow['jobs']['adapter-postgres']['needs'])
            for name, job in workflow['jobs'].items():
                if name == 'paths-filter':
                    continue
                if job.get('uses'):
                    self.assertEqual(job['with']['ref'], '${{ needs.paths-filter.outputs.ref || inputs.ref || github.sha }}')
                    child = yaml.safe_load((ci_select.ROOT / job['uses'].removeprefix('./')).read_text())
                    for child_job in child['jobs'].values():
                        for step in child_job['steps']:
                            if step.get('uses', '').startswith('actions/checkout@'):
                                self.assertEqual(step['with']['ref'], '${{ inputs.ref }}')
                    continue
                for step in job['steps']:
                    if step.get('uses', '').startswith('actions/checkout@'):
                        self.assertEqual(step['with']['ref'], '${{ needs.paths-filter.outputs.ref || inputs.ref || github.sha }}')


if __name__ == '__main__':
    unittest.main()
