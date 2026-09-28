"""Step tracking, redaction and the report of `make verify` (THE-662).

Every verification step runs through Steps.run so a failure or an interrupt
names the step it happened in. Before artifacts leave the isolated run they are
redacted: every generated secret of that run is replaced wherever it appears.
"""
import json, pathlib, re, time

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
    def __init__(self, clock=time.monotonic):
        self.clock = clock
        self.items = []

    def run(self, name, fn, *args, **kwargs):
        entry = {'step': name, 'status': 'running'}
        self.items.append(entry)
        start = self.clock()
        try:
            result = fn(*args, **kwargs)
        except (KeyboardInterrupt, Interrupted):
            entry['status'] = 'interrupted'
            raise
        except BaseException as error:
            entry['status'], entry['error'] = 'failed', bounded(error)
            raise
        finally:
            entry['seconds'] = round(self.clock() - start, 3)
        entry['status'] = 'passed'
        return result

    def failed_step(self):
        """The first scenario step that failed or was interrupted; housekeeping steps are listed but never blamed."""
        for entry in self.items:
            if entry['status'] in ('failed', 'interrupted') and entry['step'] not in HOUSEKEEPING:
                return entry['step']
        return None


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
    lines += ['', '## Timing overrides', '', '```json', json.dumps(report['timing_overrides'], indent=2), '```',
              '', '## Pins', '', f"- Model revision: `{report['pins']['model_revision']}`",
              f"- Tokenizer: {', '.join(report['pins']['tokenizer'])}",
              f"- Toolchain: {report['pins']['toolchain']['go']}; Python {report['pins']['toolchain']['python']}"]
    lines += [f"- Image: `{i}`" for i in report['pins']['images']]
    lines += ['', '## Not claimed', ''] + [f'- {u}' for u in report['pins']['unsupported_platforms']]
    lines += ['- No production, capacity or relevance certification; see the remaining-limit report: ' + REMAINING_LIMITS,
              '', f"Artifacts: `{report['artifacts']}` (dependency inventory: `dependency-inventory.json`)"]
    return '\n'.join(lines) + '\n'


def write(directory, report):
    directory = pathlib.Path(directory)
    (directory / 'report.json').write_text(json.dumps(report, indent=2))
    (directory / 'report.md').write_text(markdown(report))
