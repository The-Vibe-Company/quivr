"""A failed verification names its failed tests and their errors where the run is watched (THE-755).

The go test case runs a real `go test -json` on a throwaway module, so a change in how
test2json frames output breaks it here instead of on a red CI run.
"""
import json, os, pathlib, re, subprocess, tempfile, unittest
from unittest import mock

import ci_summary
import gotest
import local
import verify_report as vr

GO = os.environ.get('GO', 'go')
MODULE = {
    'go.mod': 'module example.com/fixture\n\ngo 1.21\n',
    'fixture_test.go': '''package fixture

import "testing"

func TestPasses(t *testing.T) {}

func TestTable(t *testing.T) {
	t.Run("good", func(t *testing.T) {})
	t.Run("bad", func(t *testing.T) { t.Log("context line"); t.Fatalf("want %d, got %d", 1, 2) })
	t.Run("fine", func(t *testing.T) {})
}

func TestParentChecks(t *testing.T) {
	t.Run("bad", func(t *testing.T) { t.Fatal("sub failed") })
	t.Errorf("parent saw a wrong total")
}
''',
}


class GoTestFailures(unittest.TestCase):
    def test_failed_subtest_is_named_with_its_assertion_and_the_log_reads_like_verbose(self):
        with tempfile.TemporaryDirectory() as d:
            d = pathlib.Path(d)
            for name, text in MODULE.items():
                (d / name).write_text(text)
            log, events = d / 'acceptance.log', d / 'acceptance.jsonl'
            env = {**os.environ, 'GOFLAGS': '-mod=mod', 'GOWORK': 'off'}
            with self.assertRaises(gotest.Failed) as caught:
                gotest.run(GO, ['-count=1', './...'], d, env, log, events)
            # A failed parent appears beside its failed subtest only when it logged something itself.
            self.assertEqual([f['test'] for f in caught.exception.failures], ['TestTable/bad', 'TestParentChecks/bad', 'TestParentChecks'])
            self.assertIn('parent saw a wrong total', caught.exception.failures[2]['excerpt'])
            # Every top-level test of the failed run keeps its outcome, for the slowest-tests table.
            self.assertEqual([(t['test'], t['status']) for t in caught.exception.results.tests()],
                             [('TestPasses', 'pass'), ('TestTable', 'fail'), ('TestParentChecks', 'fail')])
            self.assertIn('TestTable/bad', str(caught.exception))
            excerpt = caught.exception.failures[0]['excerpt']
            self.assertIn('want 1, got 2', excerpt)
            self.assertIn('--- FAIL: TestTable/bad', excerpt)
            self.assertNotIn('=== RUN', excerpt)
            text = log.read_text()
            self.assertIn('=== RUN   TestPasses', text)
            self.assertIn('--- PASS: TestPasses', text)
            self.assertTrue(all(json.loads(line) for line in events.read_text().splitlines()))

    def test_build_error_is_reported_as_the_package_failure(self):
        with tempfile.TemporaryDirectory() as d:
            d = pathlib.Path(d)
            (d / 'go.mod').write_text(MODULE['go.mod'])
            (d / 'broken_test.go').write_text('package fixture\n\nfunc broken( {\n')
            with self.assertRaises(gotest.Failed) as caught:
                gotest.run(GO, ['./...'], d, {**os.environ, 'GOWORK': 'off'}, d / 'out.log')
            self.assertIn("broken_test.go:3:14: expected ')'", caught.exception.failures[0]['excerpt'])

    def test_timeout_names_the_test_that_did_not_finish_with_its_panic(self):
        with tempfile.TemporaryDirectory() as d:
            d = pathlib.Path(d)
            (d / 'go.mod').write_text(MODULE['go.mod'])
            # Several goroutines make a stack dump longer than the excerpt, as in a real hung acceptance test.
            (d / 'hang_test.go').write_text('package fixture\n\nimport (\n\t"testing"\n\t"time"\n)\n\n'
                                            'func TestQuick(t *testing.T) {}\n\nfunc TestHangs(t *testing.T) {\n'
                                            '\tfor i := 0; i < 30; i++ {\n\t\tgo func() { time.Sleep(time.Minute) }()\n\t}\n\ttime.Sleep(time.Minute)\n}\n')
            with self.assertRaises(gotest.Failed) as caught:
                gotest.run(GO, ['-count=1', '-timeout=1s', './...'], d, {**os.environ, 'GOWORK': 'off'}, d / 'out.log')
            [failure] = caught.exception.failures
            self.assertEqual(failure['test'], 'TestHangs (did not finish)')
            self.assertIn('test timed out after 1s', failure['excerpt'])
            self.assertIn('running tests:', failure['excerpt'])
            self.assertGreater(len((d / 'out.log').read_text().splitlines()), gotest.EXCERPT_LINES)

    def test_excerpt_is_bounded(self):
        text = gotest.excerpt([f'line {i}\n' for i in range(500)])
        self.assertTrue(text.startswith('…'))
        self.assertIn('line 499', text)
        self.assertLessEqual(len(text.splitlines()), gotest.EXCERPT_LINES + 1)


class Steps(unittest.TestCase):
    def test_failed_go_test_step_keeps_its_failures_and_every_test_duration(self):
        stack = local.Stack('quivr-test-' + os.urandom(5).hex())
        self.addCleanup(__import__('shutil').rmtree, stack.directory, True)
        results = gotest.Results()
        for event in [{'Action': 'run', 'Test': 'TestA'}, {'Action': 'pass', 'Test': 'TestA', 'Elapsed': 2},
                      {'Action': 'run', 'Test': 'TestB'}, {'Action': 'fail', 'Test': 'TestB', 'Elapsed': 9}]:
            results.add({'Package': 'p', **event})
        failed = gotest.Failed('go test failed: TestB', [{'test': 'TestB', 'excerpt': 'want 1, got 2', 'log': 'a.log'}], results)
        stack.steps = steps = vr.Steps()
        with mock.patch.object(gotest, 'run', side_effect=failed), self.assertRaises(gotest.Failed):
            steps.run('monitoring', stack.tests, 'TestM')
        self.assertEqual([(t['test'], t['step']) for t in vr.slowest(steps.items)], [('TestB', 'monitoring'), ('TestA', 'monitoring')])
        text = vr.failure_text({'status': 'failed', 'failed_step': 'monitoring', 'steps': steps.items, 'artifacts': '/tmp/run'})
        self.assertIn('FAILED step monitoring', text)
        self.assertIn('--- FAIL: TestB', text)
        self.assertIn('want 1, got 2', text)


class Browser(unittest.TestCase):
    def test_playwright_json_gives_failed_specs_with_their_error(self):
        report = {'suites': [{'title': 'demo.spec.ts', 'specs': [], 'suites': [{'specs': [
            {'title': 'adds text', 'file': 'demo.spec.ts', 'line': 3, 'tests': [{'status': 'expected', 'results': [{'status': 'passed', 'duration': 2500}]}]},
            {'title': 'pauses a source', 'file': 'sources.spec.ts', 'line': 145, 'tests': [{'status': 'unexpected', 'results': [
                {'status': 'failed', 'duration': 12000, 'errors': [{'message': '\x1b[31mError: expect(locator).toHaveText()\x1b[39m\nExpected: "Paused"'}]}]}]}]}]}]}
        with tempfile.TemporaryDirectory() as d:
            path = pathlib.Path(d) / 'playwright.json'
            path.write_text(json.dumps(report))
            tests, failures = vr.browser_results(path)
        self.assertEqual([(t['test'], t['status'], t['seconds']) for t in tests],
                         [('demo.spec.ts:3 › adds text', 'pass', 2.5), ('sources.spec.ts:145 › pauses a source', 'fail', 12.0)])
        self.assertEqual(failures[0]['test'], 'sources.spec.ts:145 › pauses a source')
        self.assertIn('Expected: "Paused"', failures[0]['excerpt'])
        self.assertNotIn('\x1b', failures[0]['excerpt'])
        self.assertEqual(vr.browser_results(pathlib.Path(d) / 'absent.json'), ([], []))


class Summary(unittest.TestCase):
    def test_summary_leads_with_the_failed_test_and_annotates_it(self):
        report = {'status': 'failed', 'failed_step': 'monitoring', 'run': 'quivr-verify-x', 'duration_seconds': 300, 'steps': [
            {'step': 'start_stack', 'status': 'passed', 'seconds': 50},
            {'step': 'monitoring', 'status': 'failed', 'seconds': 200, 'error': 'Failed: go test failed: TestM',
             'tests': [{'test': 'TestM', 'status': 'fail', 'seconds': 12.5}],
             'failures': [{'test': 'TestM', 'excerpt': '--- FAIL: TestM (12.50s)\n    m_test.go:9: want 1, got 2', 'log': 'acceptance.log'}]}]}
        demo = {'status': 'passed', 'duration_seconds': 90, 'tests': [{'test': 'demo.spec.ts:3 › adds text', 'status': 'pass', 'seconds': 30.0}], 'failures': []}
        text, failures = ci_summary.summary([(pathlib.Path('/nowhere/quivr-verify-x/report.json'), report)],
                                            [(pathlib.Path('/nowhere/demo/demo-report.json'), demo)], 'https://example.test/artifact')
        self.assertLess(text.index('Failed step `monitoring`'), text.index('Slowest tests'))
        self.assertIn('m_test.go:9: want 1, got 2', text)
        self.assertIn('(https://example.test/artifact)', text)
        self.assertIn('| demo.spec.ts:3 › adds text | demo | pass | 30.0 |', text)
        self.assertIn('| monitoring | failed | 200 |', text)
        [annotation] = vr.annotations(failures)
        self.assertTrue(annotation.startswith('::error title=verify TestM::'))
        [titled] = vr.annotations([{'test': 'a.spec.ts:3 › one, two', 'excerpt': 'x'}])
        self.assertTrue(titled.startswith('::error title=verify a.spec.ts%3A3 › one%2C two::x'))
        self.assertIn('%0A    m_test.go:9: want 1, got 2', annotation)

    def test_summary_without_reports_says_verification_did_not_run(self):
        text, failures = ci_summary.summary([], [])
        self.assertIn('Verification did not run', text)
        self.assertEqual(failures, [])


class Parts(unittest.TestCase):
    def test_ci_runs_every_part_of_make_verify(self):
        workflow = (local.ROOT / '.github/workflows/verify.yml').read_text()
        matrix = re.search(r'^\s+part: \[(.*)\]$', workflow, re.M).group(1)
        self.assertEqual([p.strip() for p in matrix.split(',')], list(local.parts()) + [local.DEMO])


if __name__ == '__main__':
    unittest.main()
