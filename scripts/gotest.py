"""Run `go test -json` for the harness and keep what a failure needs on the run page (THE-755).

The log file gets the same text `go test -v` prints, so acceptance.log and adapters.log read as
before. The parsed events give each top-level test's outcome and duration, and for every failed
test its `--- FAIL` block with a bounded excerpt of its output.
"""
import json, signal, subprocess

EXCERPT_LINES = 40
EXCERPT_CHARS = 4000
NOISE = ('=== RUN', '=== PAUSE', '=== CONT', '=== NAME')


class Failed(RuntimeError):
    """A go test run failed; failures lists each failed test with its excerpt."""

    def __init__(self, message, failures, results=None):
        super().__init__(message)
        self.failures = failures
        self.results = results


def excerpt(lines, limit_lines=EXCERPT_LINES, limit_chars=EXCERPT_CHARS):
    """The last lines of a test's output, without run markers, bounded in lines and characters."""
    kept = [line.rstrip('\n') for line in lines if not line.startswith(NOISE) and line.strip()]
    if len(kept) > limit_lines:
        kept = ['…'] + kept[-limit_lines:]
    text = '\n'.join(kept)
    return text if len(text) <= limit_chars else '…' + text[-limit_chars:]


class Results:
    """Accumulates test2json events: outputs per test, final actions and durations."""

    def __init__(self):
        self.output = {}   # (package, test or None) -> [lines]
        self.outcome = {}  # (package, test or None) -> (action, seconds)
        self.order = []
        self.started = []  # tests in start order; one without an outcome never finished (timeout, crash)

    def add(self, event):
        key = (event.get('Package', ''), event.get('Test'))
        if event.get('Action') == 'build-output':
            # Compiler errors are framed by the test binary's import path, not by a package.
            self.output.setdefault(('build', event.get('ImportPath', '')), []).append(event.get('Output', ''))
        elif event.get('Action') == 'run':
            self.started.append(key)
        elif event.get('Action') == 'output':
            self.output.setdefault(key, []).append(event.get('Output', ''))
        elif event.get('Action') in ('pass', 'fail', 'skip'):
            if key not in self.outcome:
                self.order.append(key)
            self.outcome[key] = (event['Action'], round(float(event.get('Elapsed') or 0), 3))
            if event.get('FailedBuild'):
                self.output[key] = self.output.get(('build', event['FailedBuild']), []) + self.output.get(key, [])

    def tests(self):
        """Top-level tests in completion order: name, status and seconds."""
        return [{'test': test, 'status': action, 'seconds': seconds}
                for (package, test), (action, seconds) in ((k, self.outcome[k]) for k in self.order) if test and '/' not in test]

    def own_output(self, key):
        """Whether a test logged anything itself, beyond run markers and its subtests' results."""
        return any(line.strip() and not line.startswith(NOISE) and not line.lstrip().startswith(('--- FAIL', '--- PASS', '--- SKIP'))
                   for line in self.output.get(key, []))

    def timeout_header(self, package):
        """The `panic: test timed out` line and the `running tests:` list that follows it, wherever
        test2json attributed them; kept ahead of the bounded tail, which a goroutine dump would fill."""
        lines = [line for (p, _), out in self.output.items() if p == package for line in out]
        for index, line in enumerate(lines):
            if line.startswith('panic: test timed out'):
                block = [line]
                for following in lines[index + 1:index + 50]:
                    if not following.strip():
                        break
                    block.append(following)
                return block
        return []

    def failures(self, log):
        """Failed tests, then tests that started but never finished in a failed package (a timeout
        panics inside the running test), then failed packages with no failed test, such as a build
        error. A failed parent is listed beside its failed subtests only when it logged something itself."""
        failed = [k for k in self.order if self.outcome[k][0] == 'fail']
        tests = [k for k in failed if k[1]]
        shown = [k for k in tests if self.own_output(k) or not any(o[0] == k[0] and o[1].startswith(k[1] + '/') for o in tests)]
        result = [{'test': test, 'seconds': self.outcome[(package, test)][1], 'excerpt': excerpt(self.output.get((package, test), [])), 'log': str(log)}
                  for package, test in shown]
        failed_packages = {k[0] for k in failed if not k[1]}
        unfinished = [k for k in self.started if k not in self.outcome and k[0] in failed_packages]
        # Only the innermost unfinished test: its parents are unfinished because it is.
        unfinished = [k for k in unfinished if not any(o[0] == k[0] and o[1].startswith(k[1] + '/') for o in unfinished)]
        for package, test in unfinished:
            header = [line.rstrip('\n') for line in self.timeout_header(package)][:EXCERPT_LINES // 2]
            tail = excerpt(self.output.get((package, test), []) + self.output.get((package, None), []), EXCERPT_LINES - len(header))
            result.append({'test': f'{test} (did not finish)', 'seconds': None, 'log': str(log), 'excerpt': '\n'.join(header + [tail])})
        covered = {k[0] for k in tests + unfinished}
        for package, _ in (k for k in failed if not k[1] and k[0] not in covered):
            result.append({'test': f'{package} (package)', 'seconds': self.outcome[(package, None)][1], 'excerpt': excerpt(self.output.get((package, None)) or self.output.get(('', None), [])), 'log': str(log)})
        return result


def run(go, args, cwd, env, log, events=None):
    """Run `go test -json <args>`, appending text output to log and raw events to events.
    Returns the Results; raises Failed naming the failed tests when go test fails."""
    results = Results()
    with open(log, 'a') as text, (open(events, 'a') if events else open('/dev/null', 'w')) as raw:
        proc = subprocess.Popen([go, 'test', '-json', *args], cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        try:
            for line in proc.stdout:
                try:
                    event = json.loads(line)
                    if not isinstance(event, dict):
                        raise ValueError
                except ValueError:
                    # Build errors and anything else outside test2json's framing go to the log as is.
                    text.write(line)
                    results.output.setdefault(('', None), []).append(line)
                    continue
                raw.write(line)
                results.add(event)
                if event.get('Action') in ('output', 'build-output'):
                    text.write(event.get('Output', ''))
            code = proc.wait()
        finally:
            if proc.poll() is None:
                # Interrupted: go test forwards SIGINT to its test binary, so stop it that way first.
                proc.send_signal(signal.SIGINT)
                try:
                    proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait()
            proc.stdout.close()
    if code:
        failures = results.failures(log)
        if not failures:
            failures = [{'test': 'go test', 'seconds': None, 'excerpt': excerpt(results.output.get(('', None), [])), 'log': str(log)}]
        names = ', '.join(f['test'] for f in failures[:5]) + (f' and {len(failures) - 5} more' if len(failures) > 5 else '')
        raise Failed(f'go test failed: {names}; inspect {log}', failures, results)
    return results
