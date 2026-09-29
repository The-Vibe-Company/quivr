#!/usr/bin/env python3
"""Put the outcome of `make verify` on the GitHub run page (THE-755).

Reads the reports the harness wrote under .scratch/ and writes, to $GITHUB_STEP_SUMMARY:
the failed step, each failed test with a bounded excerpt, a link to the artifacts, the
slowest tests and the per-step timings. It also prints one ::error annotation per failed
test, so the run page names the test and its error without opening an artifact.

Usage: python3 scripts/ci_summary.py [artifact URL]   (prints the summary when
GITHUB_STEP_SUMMARY is unset, so it can be tried locally).
"""
import glob, json, os, pathlib, sys

import verify_report as vr

ROOT = pathlib.Path(__file__).resolve().parents[1]


def load(pattern):
    reports = []
    for path in sorted(glob.glob(str(ROOT / '.scratch' / pattern))):
        try:
            reports.append((pathlib.Path(path), json.loads(pathlib.Path(path).read_text())))
        except (OSError, ValueError):
            continue
    return reports


def fence(text):
    return ['```text', text.replace('```', "'''"), '```']


def summary(verifications, demos, artifact_url=None):
    """Markdown for the job summary, and the failures to annotate."""
    lines, failures, all_steps = [], [], []
    artifacts = f'[artifacts]({artifact_url})' if artifact_url else 'the run artifacts'
    if not verifications and not demos:
        lines += ['## Verification did not run', '', 'No verification report was written: a step before it failed '
                  '(prerequisite checks, unit tests or setup). Its log on this page names the error.']
    for path, report in verifications:
        steps = report.get('steps', [])
        all_steps += steps
        lines += [f"## Verification {report['status']} in {round(report.get('duration_seconds', 0))} s — `{report.get('run', path.parent.name)}`", '']
        if report.get('failed_step'):
            step = next((s for s in steps if s['step'] == report['failed_step']), {})
            lines += [f"**Failed step `{report['failed_step']}`**: {step.get('error', '')}", '']
            for failure in vr.failures_of(steps):
                failures.append(failure)
                lines += [f"### `{failure['test']}` (step `{failure['step']}`)", ''] + fence(failure['excerpt']) + ['']
            lines += [f'Logs and captures: {artifacts} (`{path.parent.name}/`).', '']
    for path, report in demos:
        lines += [f"## Browser demo {report['status']} in {round(report.get('duration_seconds', 0))} s", '']
        for failure in report.get('failures', []):
            failures.append({**failure, 'step': 'demo'})
            lines += [f"### `{failure['test']}`", ''] + fence(failure['excerpt']) + ['']
        if report['status'] != 'passed':
            lines += [f'Browser log, traces and screenshots: {artifacts} (`{path.parent.name}/`).', '']
        all_steps.append({'step': 'demo', 'tests': report.get('tests', [])})
    lines += vr.slowest_table(all_steps)
    for path, report in verifications:
        lines += ['', f"### Step timings — `{report.get('run', path.parent.name)}`", '', '| Step | Status | Seconds |', '| --- | --- | --- |']
        lines += [f"| {s['step']} | {s['status']} | {s.get('seconds', '')} |" for s in report.get('steps', [])]
        lines += ['', f'Pins, timing overrides and the full report: `{path.parent.name}/report.md` in {artifacts}.']
    return '\n'.join(lines) + '\n', failures


def main():
    artifact_url = sys.argv[1] if len(sys.argv) > 1 and sys.argv[1] else None
    text, failures = summary(load('quivr-verify-*/report.json'), load('quivr-demo-verify-*/demo-report.json'), artifact_url)
    for line in vr.annotations(failures):
        print(line)
    target = os.environ.get('GITHUB_STEP_SUMMARY')
    if target:
        with open(target, 'a') as out:
            out.write(text)
    else:
        print(text)


if __name__ == '__main__':
    main()
