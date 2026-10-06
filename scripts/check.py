"""Time the complete local check and its isolated CI groups, using only the stdlib."""
import argparse
import hashlib
import json
import importlib
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import time
import unittest

import gotest

ROOT = Path(__file__).resolve().parent.parent
REPORTS = Path(os.environ.get('QUIVR_CHECK_REPORTS', ROOT / '.scratch' / 'check'))
GROUPS = {
    'guards': ['docs', 'denylist', 'migrations', 'image-context', 'plugin-boundary', 'conformance-validate'],
    'contracts': ['contracts'],
    'go': ['test-go'],
    'python': ['test-python'],
    'eval': ['test-eval'],
    'sdk-python': ['test-sdk-python'],
    'sdk-go': ['test-sdk-go'],
}
LOCAL_GROUPS = tuple(GROUPS)
EVAL_SHARDS = 3
CI_GROUPS = []
for group in LOCAL_GROUPS:
    if group == 'eval':
        for index in range(1, EVAL_SHARDS + 1):
            name = f'eval-{index}'
            GROUPS[name] = GROUPS[group]
            CI_GROUPS.append(name)
    else:
        CI_GROUPS.append(group)


def save(name, report):
    REPORTS.mkdir(parents=True, exist_ok=True)
    (REPORTS / f'{name}.json').write_text(json.dumps(report, indent=2) + '\n')


def run_targets(group):
    """Keep the command's output on the run page and record each target, even on failure."""
    steps = []
    code = 0
    for target in GROUPS[group]:
        started = time.monotonic()
        print(f'::group::{target}' if os.environ.get('GITHUB_ACTIONS') else f'check: {target}', flush=True)
        command = ['make', '--no-print-directory', target]
        if group.startswith('eval-'):
            command.append(f'eval-shard={group.removeprefix("eval-")}/{EVAL_SHARDS}')
        code = subprocess.call(command, cwd=ROOT)
        steps.append({'target': target, 'seconds': round(time.monotonic() - started, 3), 'exit_code': code})
        save(group, {'steps': steps})
        if os.environ.get('GITHUB_ACTIONS'):
            print('::endgroup::', flush=True)
        if code:
            break
    return code


class TimedResult(unittest.TextTestResult):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.timings = []

    def startTest(self, test):
        self.started = time.monotonic()
        super().startTest(test)

    def stopTest(self, test):
        self.timings.append({'test': test.id(), 'seconds': round(time.monotonic() - self.started, 3)})
        super().stopTest(test)


def test_cases(suite):
    for test in suite:
        if isinstance(test, unittest.TestSuite):
            yield from test_cases(test)
        else:
            yield test


def shard_argument(value):
    try:
        index, total = map(int, value.split('/'))
        if 1 <= index <= total:
            return index, total
    except ValueError:
        pass
    raise argparse.ArgumentTypeError('use INDEX/TOTAL with 1 <= INDEX <= TOTAL')


def run_unittest(directory, module=None, preload=(), shard=None):
    # Match unittest's discover/module import paths; keep one process for the suite's fixtures.
    sys.path.insert(0, str(ROOT))
    sys.path.insert(0, str(Path.cwd()))
    # Environment-admission tests may clear os.environ; initialize optional runtimes first.
    for name in preload:
        if importlib.util.find_spec(name) is not None:
            importlib.import_module(name)
    suite = (unittest.defaultTestLoader.loadTestsFromName(module) if module else
             unittest.defaultTestLoader.discover(directory, pattern='test_*.py'))
    if shard:
        index, total = shard
        # Stable IDs partition every test exactly once; keep discovery order for class fixtures.
        suite = unittest.TestSuite(test for test in test_cases(suite)
                                   if int(hashlib.sha256(test.id().encode()).hexdigest(), 16)
                                   % total == index - 1)
    selected = [test.id() for test in test_cases(suite)]
    result = unittest.TextTestRunner(verbosity=2, resultclass=TimedResult).run(suite)
    failures = [{'test': test.id(), 'excerpt': text[-4000:]} for test, text in result.failures + result.errors]
    failures += [{'test': test.id(), 'excerpt': 'Unexpected success for an expected failure'}
                 for test in result.unexpectedSuccesses]
    suffix = f'-shard{shard[0]}of{shard[1]}' if shard else ''
    save('unittest-' + (module or str(Path(directory).resolve())).replace('/', '-') + suffix,
         {'tests': result.timings, 'failures': failures, 'count': result.testsRun,
          'selected_tests': selected, 'skipped': len(result.skipped), 'successful': result.wasSuccessful()})
    return 0 if result.wasSuccessful() else 1


def run_go(directory):
    REPORTS.mkdir(parents=True, exist_ok=True)
    name = 'go-' + directory.replace('/', '-').replace('.', 'root')
    log, events = REPORTS / f'{name}.log', REPORTS / f'{name}.jsonl'
    log.write_text('')
    events.write_text('')
    failures = []
    code = 0
    try:
        result = gotest.run(os.environ.get('GO', 'go'), ['./...'], ROOT / directory,
                            os.environ.copy(), log, events)
    except gotest.Failed as error:
        result, failures, code = error.results, error.failures, 1
    print(log.read_text(), end='', flush=True)
    save(name, {'tests': result.tests() if result else [], 'failures': failures})
    return code


def summary():
    lines = ['## Quick check', '', '| Target | Seconds | Exit code |', '| --- | ---: | ---: |']
    tests, failures = [], []
    for path in sorted(REPORTS.glob('*.json')):
        report = json.loads(path.read_text())
        for step in report.get('steps', []):
            lines.append(f"| {step['target']} | {step['seconds']:.1f} | {step['exit_code']} |")
        tests.extend(report.get('tests', []))
        failures.extend(report.get('failures', []))
    if failures:
        lines += ['', '### Failed tests']
        for failure in failures:
            lines += ['', f"**{failure['test']}**", '```text', failure['excerpt'], '```']
    lines += ['', '### Slowest tests', '', '| Test | Seconds |', '| --- | ---: |']
    for test in sorted(tests, key=lambda t: t['seconds'] or 0, reverse=True)[:20]:
        lines.append(f"| {test['test'].replace('|', '/')} | {test['seconds'] or 0:.3f} |")
    text = '\n'.join(lines) + '\n'
    print(text)
    if os.environ.get('GITHUB_STEP_SUMMARY'):
        with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as output:
            output.write(text)
    return 0


def main():
    global REPORTS
    parser = argparse.ArgumentParser(description=__doc__)
    action = parser.add_mutually_exclusive_group()
    action.add_argument('--group', choices=GROUPS)
    action.add_argument('--unittest', metavar='DIRECTORY')
    action.add_argument('--go', metavar='DIRECTORY')
    action.add_argument('--summary', action='store_true')
    parser.add_argument('--module')
    parser.add_argument('--shard', type=shard_argument, help='Run one stable INDEX/TOTAL partition of unittest IDs')
    parser.add_argument('--preload', action='append', default=[],
                        help='Initialize an installed optional module before test discovery')
    parser.add_argument('--output', type=Path, default=REPORTS, help='Directory for timing reports and logs')
    args = parser.parse_args()
    REPORTS = args.output
    os.environ['QUIVR_CHECK_REPORTS'] = str(REPORTS.resolve())
    if args.unittest:
        return run_unittest(args.unittest, args.module, args.preload, args.shard)
    if args.go:
        return run_go(args.go)
    if args.summary:
        return summary()
    # A new gate never displays test reports left over from an earlier run.
    if REPORTS.exists():
        for path in REPORTS.glob('*'):
            if path.is_file() and path.suffix in ('.json', '.jsonl', '.log'):
                path.unlink()
    groups = [args.group] if args.group else LOCAL_GROUPS
    for group in groups:
        code = run_targets(group)
        if code:
            return code
    return 0


if __name__ == '__main__':
    sys.exit(main())
