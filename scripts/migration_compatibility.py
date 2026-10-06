#!/usr/bin/env python3
"""Run the merge-base API/worker and its acceptance cases on candidate expand SQL.

The previous checkout supplies its own harness/configuration/plugins. The
candidate supplies only the migrate binary. No release/image workflow is used.
"""
import argparse
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tarfile
import time
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]
PATTERN = '^(TestInlineMaterialization|TestInlineIdentityAndAuthorization|TestLexicalSearchCanonicalExcerpt|TestLexicalScopeLimitsAndReplay)$'


def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base', default='origin/main')
    parser.add_argument('--previous-source', type=pathlib.Path, help=argparse.SUPPRESS)
    parser.add_argument('--candidate', type=pathlib.Path, help=argparse.SUPPRESS)
    parser.add_argument('--output', type=pathlib.Path, help=argparse.SUPPRESS)
    args = parser.parse_args()
    if args.previous_source:
        exercise(args)
        return
    if not args.base or set(args.base) == {'0'}:
        parser.error('--base has no previous revision; bootstrap pushes cannot prove compatibility')
    base = subprocess.check_output(['git', 'merge-base', 'HEAD', args.base], cwd=ROOT, text=True).strip()
    output = ROOT / '.scratch' / ('quivr-compatibility-'+uuid.uuid4().hex[:10])
    output.mkdir(parents=True, mode=0o700)
    source = output / 'previous'
    source.mkdir()
    archive = output / 'previous.tar'
    run(['git', 'archive', '--format=tar', '-o', str(archive), base], cwd=ROOT)
    with tarfile.open(archive) as files:
        files.extractall(source, filter='data')
    archive.unlink()
    # Reuse only immutable model/tokenizer caches, never database state.
    (source / '.scratch').mkdir()
    for cache in ('e5-model', 'tokenizer'):
        target = ROOT / '.scratch' / cache
        target.mkdir(parents=True, exist_ok=True)
        (source / '.scratch' / cache).symlink_to(target, target_is_directory=True)
    candidate = output / 'candidate'
    run([os.environ.get('GO', 'go'), 'build', '-o', str(candidate), './cmd/quivr'], cwd=ROOT)
    report = {'previous_commit': base, 'candidate_commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(), 'acceptance_pattern': PATTERN, 'result': 'failed'}
    start = time.monotonic()
    try:
        run([sys.executable, str(pathlib.Path(__file__).resolve()), '--previous-source', str(source), '--candidate', str(candidate), '--output', str(output)], cwd=source)
        report['result'] = 'passed'
    finally:
        report['seconds'] = round(time.monotonic()-start, 3)
        (output / 'report.json').write_text(json.dumps(report, indent=2)+'\n')
        # The archive/configs contain local random credentials; only copied
        # logs and the commit/report metadata are uploaded by CI.
        shutil.rmtree(source)
        print('Compatibility evidence:', output, flush=True)


def exercise(args):
    sys.path.insert(0, str(args.previous_source / 'scripts'))
    import local
    stack = local.Stack('quivr-compat-'+uuid.uuid4().hex[:10])
    stack.verifying = True
    try:
        stack.up()
        # Apply candidate expansion while the prior API and worker are live.
        with (args.output / 'candidate-migrate.log').open('w') as log:
            run([str(args.candidate), 'migrate'], env={**os.environ, 'QUIVR_CONFIG': str(stack.directory / 'config.json')}, stdout=log, stderr=log)
        for key in ('probe_port', 'worker_probe_port'):
            stack.await_ready(key)
        stack.tests(PATTERN)
        shutil.copyfile(stack.directory / 'acceptance.log', args.output / 'serving-previous.log')
        # A rollback also has to start successfully, not just keep old processes
        # alive. Reuse the old configuration and worker on the expanded schema.
        stack.stop_processes()
        # Give the reused acceptance journeys a fresh Organization after restart
        # so they prove new writes instead of merely replaying stored receipts.
        for config in ('config.json', 'worker.json'):
            path = stack.directory / config
            cfg = json.loads(path.read_text())
            for scope in cfg['keys'].values():
                scope['organization'] += '_rollback'
            path.write_text(json.dumps(cfg))
        stack.start_processes()
        stack.tests(PATTERN)
        shutil.copyfile(stack.directory / 'acceptance.log', args.output / 'restarted-previous.log')
    finally:
        stack.stop_processes()
        stack.down(reset=True)
        for log in stack.directory.glob('*-startup.log'):
            shutil.copyfile(log, args.output / ('previous-'+log.name))


if __name__ == '__main__':
    main()
