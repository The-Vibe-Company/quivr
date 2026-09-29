"""Step tracking, redaction and the report of `make verify` (THE-662).

Every verification step runs through Steps.run so a failure or an interrupt
names the step it happened in. Before artifacts leave the isolated run they are
redacted: every generated secret of that run is replaced wherever it appears.
"""
import json, pathlib, re, time

import gotest

REDACTED = '[REDACTED]'
# Artifacts that hold credentials by design; they are never uploaded and never rewritten.
PRIVATE = {'state.json', 'config.json', 'worker.json', 'short-retention.json', 'keyless.json', 'keyless-worker.json', 's3.json'}
TEXT_SUFFIXES = {'.log', '.json', '.txt', '.md'}
REMAINING_LIMITS = 'docs/quivr-v2-remaining-limits.md'
HOUSEKEEPING = {'capture_diagnostics', 'scoped_cleanup'}
ROTATED = re.compile(r'\.log\.\d+$')


class Interrupted(Exception):
    """Raised by the SIGTERM handler; SIGINT arrives as KeyboardInterrupt."""


class Steps:
    """echo, when set (a print function), gets one line per finished step, so a local run shows progress."""

    def __init__(self, clock=time.monotonic, echo=None):
        self.clock = clock
        self.echo = echo
        self.items = []
        self.current = None

    def run(self, name, fn, *args, **kwargs):
        entry = {'step': name, 'status': 'running'}
        self.items.append(entry)
        outer, self.current = self.current, entry
        start = self.clock()
        try:
            result = fn(*args, **kwargs)
            entry['status'] = 'passed'
        except (KeyboardInterrupt, Interrupted):
            entry['status'] = 'interrupted'
            raise
        except BaseException as error:
            entry['status'], entry['error'] = 'failed', bounded(error)
            # A failed go test or browser run names its failed tests with an excerpt (THE-755).
            if getattr(error, 'failures', None):
                entry['failures'] = error.failures
            raise
        finally:
            entry['seconds'] = round(self.clock() - start, 3)
            self.current = outer
            if self.echo:
                self.echo(f"[verify] {entry['status']:<11} {name} ({entry['seconds']:.1f} s)", flush=True)
        return result

    def record_tests(self, tests):
        """Top-level test outcomes and durations of the running step; they feed the slowest-tests table."""
        if self.current is not None:
            self.current.setdefault('tests', []).extend(tests)

    def failed_step(self):
        """The first scenario step that failed or was interrupted; housekeeping steps are listed but never blamed."""
        for entry in self.items:
            if entry['status'] in ('failed', 'interrupted') and entry['step'] not in HOUSEKEEPING:
                return entry['step']
        return None


def slowest(steps, limit=15):
    """The slowest top-level tests across steps, with the step that ran them."""
    tests = [{**t, 'step': s['step']} for s in steps for t in s.get('tests', []) if t.get('seconds') is not None]
    return sorted(tests, key=lambda t: t['seconds'], reverse=True)[:limit]


def failures_of(steps):
    """Every failed test with its step, in step order."""
    return [{**f, 'step': s['step']} for s in steps for f in s.get('failures', [])]


def failure_text(report):
    """The console block of a failed run: the step, its error, then each failed test with its excerpt."""
    if not report.get('failed_step'):
        return ''
    step = next((s for s in report['steps'] if s['step'] == report['failed_step']), {})
    lines = ['', f"FAILED step {report['failed_step']}: {step.get('error', report['status'])}"]
    for failure in failures_of(report['steps']):
        lines += ['', f"--- FAIL: {failure['test']} (step {failure['step']}, log {failure.get('log', '')})"]
        lines += ['    ' + line for line in failure['excerpt'].splitlines()]
    lines += ['', f"Artifacts: {report['artifacts']}"]
    return '\n'.join(lines) + '\n'


def annotations(failures, title_prefix='verify'):
    """GitHub workflow commands that show each failed test on the run page (message newlines escaped)."""
    def escape(text):
        return text.replace('%', '%25').replace('\r', '%0D').replace('\n', '%0A')
    def prop(text):
        return escape(text).replace(':', '%3A').replace(',', '%2C')
    return [f"::error title={prop(title_prefix + ' ' + f['test'])}::{escape(f['excerpt'][-2000:] or f['test'])}" for f in failures]


ANSI = re.compile(r'\x1b\[[0-9;]*m')


def browser_results(report_path):
    """Playwright's JSON report as (tests, failures): every spec's outcome and seconds, and each
    unexpected result with its error message. Missing or unreadable report: ([], [])."""
    try:
        report = json.loads(pathlib.Path(report_path).read_text())
    except (OSError, ValueError):
        return [], []
    tests, failures = [], []
    def walk(suite):
        for spec in suite.get('specs', []):
            name = f"{spec.get('file', '')}:{spec.get('line', '')} › {spec.get('title', '')}"
            for test in spec.get('tests', []):
                results = test.get('results') or [{}]
                last = results[-1]
                failed = test.get('status') == 'unexpected'
                tests.append({'test': name, 'status': 'fail' if failed else 'pass' if test.get('status') in ('expected', 'flaky') else 'skip',
                              'seconds': round(sum(r.get('duration', 0) for r in results) / 1000, 3)})
                if failed:
                    errors = last.get('errors') or ([last['error']] if last.get('error') else [])
                    message = '\n'.join(ANSI.sub('', e.get('message') or e.get('stack') or '') for e in errors) or last.get('status', 'failed')
                    failures.append({'test': name, 'seconds': tests[-1]['seconds'], 'excerpt': gotest.excerpt(message.splitlines())})
        for child in suite.get('suites', []):
            walk(child)
    for suite in report.get('suites', []):
        walk(suite)
    return tests, failures


def bounded(error, limit=500):
    text = f'{type(error).__name__}: {error}'
    return text if len(text) <= limit else text[:limit] + '…'


def secrets_of(state):
    """Every generated secret of one stack's state: long random values only, never ports or IDs."""
    return sorted({v for k, v in state.items() if isinstance(v, str) and len(v) >= 16 and k not in ('scoped_id',)}, key=len, reverse=True)


def redact_text(text, secrets):
    for secret in secrets:
        text = text.replace(secret, REDACTED)
    return text


def redact_tree(directory, secrets):
    """Redact every exportable text artifact under directory in place; returns the files changed."""
    changed = []
    for path in sorted(pathlib.Path(directory).rglob('*')):
        if not path.is_file() or path.name in PRIVATE or (path.suffix not in TEXT_SUFFIXES and not ROTATED.search(path.name)):
            continue
        try:
            text = path.read_text()
        except (UnicodeDecodeError, OSError):
            continue
        clean = redact_text(text, secrets)
        if clean != text:
            path.write_text(clean)
            changed.append(path.name)
    return changed


def markdown(report):
    lines = [f"# Quivr verification report — {report['status']}", '',
             f"- Source: `{report['source']}`{' (uncommitted changes)' if report.get('dirty') else ''}",
             f"- Platform: {report['pins']['platform']} only; no other platform or production readiness is claimed",
             f"- Duration: {report['duration_seconds']} s"]
    if report.get('failed_step'):
        lines.append(f"- **Failed step: `{report['failed_step']}`** — see its error below and the captured logs")
    if report.get('kept_project'):
        lines.append(f"- Isolated project kept for inspection: `{report['kept_project']}`")
    for retry in report.get('dependency_start_retries') or []:
        lines.append(f"- Dependency start retried (attempt {retry['attempt']}, exited: {', '.join(retry['exited'])})")
    lines += ['', '| Step | Status | Seconds |', '| --- | --- | --- |']
    lines += [f"| {s['step']} | {s['status']} | {s.get('seconds', '')} |" for s in report['steps']]
    errors = [s for s in report['steps'] if s.get('error')]
    if errors:
        lines += ['', '## Errors', ''] + [f"- `{s['step']}`: {s['error']}" for s in errors]
    for failure in failures_of(report['steps']):
        lines += ['', f"### `{failure['test']}` (step `{failure['step']}`)", '', '```text', failure['excerpt'], '```']
    lines += slowest_table(report['steps'])
    lines += ['', '## Timing overrides', '', '```json', json.dumps(report['timing_overrides'], indent=2), '```',
              '', '## Pins', '', f"- Model revision: `{report['pins']['model_revision']}`",
              f"- Tokenizer: {', '.join(report['pins']['tokenizer'])}",
              f"- Toolchain: {report['pins']['toolchain']['go']}; Python {report['pins']['toolchain']['python']}"]
    lines += [f"- Image: `{i}`" for i in report['pins']['images']]
    lines += ['', '## Not claimed', ''] + [f'- {u}' for u in report['pins']['unsupported_platforms']]
    lines += ['- No production, capacity or relevance certification; see the remaining-limit report: ' + REMAINING_LIMITS,
              '', f"Artifacts: `{report['artifacts']}` (dependency inventory: `dependency-inventory.json`)"]
    return '\n'.join(lines) + '\n'


def slowest_table(steps, limit=15):
    tests = slowest(steps, limit)
    if not tests:
        return []
    return ['', f'## Slowest tests (top {len(tests)})', '', '| Test | Step | Status | Seconds |', '| --- | --- | --- | --- |'] + [
        f"| {t['test']} | {t['step']} | {t['status']} | {t['seconds']} |" for t in tests]


def write(directory, report):
    directory = pathlib.Path(directory)
    (directory / 'report.json').write_text(json.dumps(report, indent=2))
    (directory / 'report.md').write_text(markdown(report))
