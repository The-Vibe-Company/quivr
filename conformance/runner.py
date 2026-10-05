#!/usr/bin/env python3
"""Validate declarative cases or run them locally and retain a complete evidence report."""
import argparse
from contextlib import contextmanager
import datetime
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import signal
import uuid

import yaml
from jsonschema import Draft202012Validator

ROOT = pathlib.Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))
from conformance.checks import CHECKS, Context, Skip, digest


class CaseLoader(yaml.SafeLoader):
    def construct_mapping(self, node, deep=False):
        keys = [self.construct_object(key, deep=deep) for key, _ in node.value]
        if any(not isinstance(key, str) for key in keys) or len(keys) != len(set(keys)):
            raise ValueError('case keys must be unique strings')
        return super().construct_mapping(node, deep=deep)


def load_cases(suite):
    schema = json.loads((ROOT / 'conformance/schema/case.schema.json').read_text())
    Draft202012Validator.check_schema(schema)
    conditions = []
    for rule in schema.get('allOf', []):
        condition = rule.get('if') if isinstance(rule, dict) else None
        if not isinstance(condition, dict):continue
        check = condition.get('properties', {}).get('check')
        if isinstance(check, dict):conditions.append(check)
    shaped = {condition['const'] for condition in conditions if isinstance(condition.get('const'), str)}
    known = {name for condition in conditions if isinstance(condition.get('not'), dict)
             for name in condition['not'].get('enum', []) if isinstance(name, str)}
    if shaped != set(CHECKS) or known != set(CHECKS):
        raise ValueError('check registry and schema must register the same check types')
    validator = Draft202012Validator(schema)
    files = sorted(list(suite.glob('*.yaml')) + list(suite.glob('*.yml')))
    if not files:
        raise ValueError('suite has no YAML cases: ' + str(suite))
    cases, ids = [], set()
    for path in files:
        try:
            if path.is_symlink() or path.stat().st_size > 128 * 1024:
                raise ValueError('cases must be regular files of at most 128 KiB')
            text = path.read_text()
            # Aliases/anchors are unnecessary for a single case and can amplify hostile input.
            if any(isinstance(token, (yaml.tokens.AliasToken, yaml.tokens.AnchorToken)) for token in yaml.scan(text)):
                raise ValueError('YAML aliases and anchors are not allowed')
            case = yaml.load(text, Loader=CaseLoader)
            json.dumps(case, allow_nan=False)  # Require finite JSON data, not YAML dates/types.
            errors = sorted(validator.iter_errors(case), key=lambda error: str(error.path))
            if errors:
                error = errors[0]
                raise ValueError('schema rejected ' + '/'.join(str(item) for item in error.path) + ': ' + error.validator)
            if case['id'] in ids:
                raise ValueError('duplicate requirement id: ' + case['id'])
            ids.add(case['id'])
            cases.append((path, case))
        except (ValueError, TypeError, yaml.YAMLError, RecursionError) as error:
            # Avoid echoing untrusted YAML tags/content in validator errors.
            if isinstance(error, ValueError) and not isinstance(error, yaml.YAMLError):
                reason = str(error)
            else:
                reason = type(error).__name__
            raise ValueError(str(path) + ': ' + reason) from error
    return cases


def case_result(path, case):
    result = {'id': case['id'], 'check': case['check'], 'case_file': str(path),
              'threshold': case['threshold'], 'measurement': None, 'evidence': {}, 'reason': ''}
    if case.get('maintainer_ticket'):
        result['maintainer_ticket'] = case['maintainer_ticket']
    return result


def run_cases(cases, context):
    results = []
    for path, case in cases:
        result = case_result(path, case)
        try:
            if case['check'] not in CHECKS:
                raise Skip('needs check type ' + case['check'])
            observation = CHECKS[case['check']](context, case['parameters'], case['threshold'])
            result.update(status='met' if observation.met else 'not met', measurement=observation.measurement,
                          evidence=observation.evidence, reason=observation.reason)
        except Skip as error:
            result.update(status='skipped', reason=str(error))
        except Exception as error:
            # Bodies, subprocess output and config must never leak through exception messages.
            result.update(status='error', reason='check failed: ' + type(error).__name__)
        results.append(result)
    return results


def write_report(directory, report):
    directory.mkdir(parents=True, exist_ok=True)
    (directory / 'report.json').write_text(json.dumps(report, indent=2, allow_nan=False) + '\n')
    lines = ['# Conformance report', '', 'Version / source: `' + report['version'] + '`', '',
             'A skipped requirement has no measurement and is not proof of conformance.', '',
             '## Run evidence', '', '```json',
             json.dumps({key: value for key, value in report.items() if key != 'results'}, indent=2, allow_nan=False),
             '```', '']
    for result in report['results']:
        lines += ['## ' + result['id'], '', '**Status:** ' + result['status'], '',
                  '**Case file:** `' + result['case_file'] + '`', '',
                  '**Check:** `' + result['check'] + '`', '',
                  '**Reason:** ' + (result['reason'] or 'none'), '']
        if result.get('maintainer_ticket'):
            lines += ['**Maintainer ticket:** ' + result['maintainer_ticket'], '']
        for name in ['measurement', 'threshold', 'evidence']:
            lines += ['### ' + name.capitalize(), '', '```json',
                      json.dumps(result[name], indent=2, allow_nan=False), '```', '']
    (directory / 'report.md').write_text('\n'.join(lines))


def git(*args):
    return subprocess.run(['git', *args], cwd=ROOT, check=True, capture_output=True, text=True).stdout.strip()


@contextmanager
def source_tree(version):
    if not version:
        yield ROOT, git('rev-parse', 'HEAD'), bool(git('status', '--porcelain'))
        return
    revision = git('rev-parse', '--verify', '--end-of-options', version + '^{commit}')
    with tempfile.TemporaryDirectory(prefix='quivr-source-', dir=ROOT / '.scratch') as directory:
        archive = subprocess.run(['git', 'archive', revision], cwd=ROOT, check=True, capture_output=True).stdout
        subprocess.run(['tar', '-x', '-C', directory], input=archive, check=True)
        yield pathlib.Path(directory), revision, False


def validate_url(value):
    import urllib.parse
    parts = urllib.parse.urlsplit(value)
    if parts.scheme not in ('http', 'https') or not parts.hostname or parts.username or parts.password or parts.query or parts.fragment:
        raise ValueError('target must be an http(s) URL without credentials, query or fragment')
    return value.rstrip('/')


def stop_stack(stack):
    """Attempt every owned cleanup even when a preceding component refuses to stop."""
    sys.path.insert(0, str(ROOT / 'scripts'))
    import normalizer_plugin, subscription_plugin, push_plugin, connector_plugin, fixture_plugin
    actions = []
    for name in ['fake_x', 'fake_graph']:
        if hasattr(stack, name):
            actions.append((name, getattr(stack, name).close))
    actions += [('normalizer', lambda: normalizer_plugin.stop(stack)),
                ('subscription', lambda: subscription_plugin.stop(stack)),
                ('push_source', lambda: push_plugin.stop(stack)),
                ('connector', lambda: connector_plugin.stop(stack))]
    for row in connector_plugin.FIRST_PARTY:
        actions.append((row['id'], lambda name=row['id']: connector_plugin.stop_first_party(stack, only=[name])))
    actions += [('fixture', lambda: fixture_plugin.stop(stack)),
                ('processes', stack.stop_processes),
                ('containers', lambda: stack.compose('down', '--volumes'))]
    errors = []
    for name, action in actions:
        try:
            action()
        except Exception as error:
            errors.append({'component': name, 'error': type(error).__name__})
    return errors


@contextmanager
def target(context, source, isolated):
    if not isolated:
        yield context
        return
    sys.path.insert(0, str(ROOT / 'scripts'))
    import local
    import connector_plugin
    import normalizer_plugin
    import subscription_plugin
    # The existing harness owns its ports, dependencies and fake paid services.
    # Its spawn method combines streams, so override only process capture for this run.
    class ConformanceStack(local.Stack):
        def spawn(self, command, config):
            with (self.directory / (command + '-stdout.log')).open('ab') as stdout, \
                 (self.directory / (command + '-stderr.log')).open('ab') as stderr:
                process = subprocess.Popen([str(self.directory / 'quivr'), command], cwd=ROOT,
                                           env={**os.environ, 'QUIVR_CONFIG': str(self.directory / config)},
                                           stdout=stdout, stderr=stderr, start_new_session=True)
            self.state['pids'].append(process.pid)
            if command in ('api', 'worker'):
                self.state[command + '_pid'] = process.pid
            self.save()
    def interrupted(*_):
        raise InterruptedError('conformance interrupted')
    previous = signal.signal(signal.SIGTERM, interrupted)
    stack = None
    try:
        stack = ConformanceStack('quivr-conformance-' + uuid.uuid4().hex[:10], source=source)
        normalizer_plugin.select(stack, 'template')
        subscription_plugin.select(stack, False, 'off')
        connector_plugin.select(stack, False)
        connector_plugin.select_first_party(stack, connector_plugin.CORE)
        stack.up()
        context.api_url = f"http://127.0.0.1:{stack.state['api_port']}"
        context.probe_url = f"http://127.0.0.1:{stack.state['probe_port']}"
        context.api_key = stack.state['admin']
        context.binary = stack.directory / 'quivr'
        context.logs = {stream: stack.directory / ('api-' + stream + '.log') for stream in ['stdout', 'stderr']}
        yield context
    finally:
        try:
            original_error = sys.exc_info()[1]
            cleanup_errors = stop_stack(stack) if stack is not None else []
            if cleanup_errors:
                if original_error is None:
                    original_error = RuntimeError('isolated cleanup failed')
                    original_error.conformance_cleanup_errors = cleanup_errors
                    raise original_error
                original_error.conformance_cleanup_errors = cleanup_errors
        finally:
            signal.signal(signal.SIGTERM, previous)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--validate', action='store_true', help='validate schemas only; never start a target')
    parser.add_argument('--suite', default=None, help='suite name under conformance/suites')
    parser.add_argument('--version', default='', help='Git revision to build; default current working tree')
    parser.add_argument('--api-url', default='')
    parser.add_argument('--probe-url', default='')
    parser.add_argument('--no-stack', action='store_true', help='only inspect explicitly supplied artifacts')
    parser.add_argument('--binary', type=pathlib.Path)
    parser.add_argument('--stdout-log', type=pathlib.Path)
    parser.add_argument('--stderr-log', type=pathlib.Path)
    parser.add_argument('--image', default='')
    parser.add_argument('--sbom', type=pathlib.Path, help='Syft JSON inventory bound to the selected image id')
    parser.add_argument('--output', type=pathlib.Path, default=ROOT / '.scratch/conformance')
    parser.add_argument('--timeout', type=float, default=5)
    args = parser.parse_args(argv)
    if not 0 < args.timeout <= 60:
        parser.error('timeout must be between 0 and 60 seconds')
    requested_suite = args.suite
    args.suite = args.suite or 'example'
    if not args.suite or '/' in args.suite or '\\' in args.suite or args.suite in ('.', '..'):
        parser.error('suite must be a single directory name')
    suites = ROOT / 'conformance/suites'
    selected = [suites / args.suite] if not args.validate or requested_suite is not None else sorted(suites.iterdir())
    try:
        cases = [case for suite in selected if suite.is_dir() for case in load_cases(suite)]
        if not cases:
            raise ValueError('no cases found')
        if args.validate:
            print(f'Validated {len(cases)} conformance cases; no stack started.')
            return 0
        args.output.mkdir(parents=True, exist_ok=True)
        (ROOT / '.scratch').mkdir(exist_ok=True)
        with source_tree(args.version) as (source, revision, dirty):
            context = Context(api_url=validate_url(args.api_url) if args.api_url else '',
                              probe_url=validate_url(args.probe_url) if args.probe_url else '',
                              api_key=os.environ.get('QUIVR_CONFORMANCE_API_KEY', ''), binary=args.binary,
                              logs={stream: path for stream, path in [('stdout', args.stdout_log), ('stderr', args.stderr_log)] if path},
                              image=args.image, sbom=args.sbom, openapi=source / 'contracts/http/v0/openapi.yaml',
                              evidence_dir=args.output, timeout=args.timeout)
            report = {'format_version': 1, 'version': revision, 'source_working_tree_dirty': dirty,
                      'harness_working_tree_dirty': bool(git('status', '--porcelain')),
                      'suite': args.suite, 'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                      'target_mode': 'existing' if args.api_url or args.probe_url else 'artifacts' if args.no_stack else 'isolated',
                      'harness_revision': git('rev-parse', 'HEAD'),
                      'target': {'api_url': context.api_url, 'probe_url': context.probe_url,
                                 'image': context.image, 'binary_sha256': None}}
            try:
                with target(context, source, isolated=not (args.api_url or args.probe_url or args.no_stack)):
                    report['results'] = run_cases(cases, context)
                    report['target'] = {'api_url': context.api_url, 'probe_url': context.probe_url,
                                        'image': context.image, 'binary_sha256': digest(context.binary.read_bytes()) if context.binary else None}
            except (Exception, KeyboardInterrupt) as error:
                report['run_error'] = type(error).__name__
                if getattr(error, 'conformance_cleanup_errors', None):
                    report['cleanup_errors'] = error.conformance_cleanup_errors
                if 'results' not in report:
                    report['results'] = []
                    for path, case in cases:
                        result = case_result(path, case)
                        if case['check'] not in CHECKS:
                            result.update(status='skipped', reason='needs check type ' + case['check'])
                        else:
                            result.update(status='error', reason='target run failed: ' + type(error).__name__)
                        report['results'].append(result)
            write_report(args.output, report)
            counts = {status: sum(result['status'] == status for result in report['results'])
                      for status in ['met', 'not met', 'error', 'skipped']}
            print(json.dumps(counts) + '\nReports: ' + str(args.output / 'report.json') + ', ' + str(args.output / 'report.md'))
            return 1 if report.get('run_error') or counts['not met'] or counts['error'] else 0
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        print('Conformance input/setup error: ' + type(error).__name__, file=sys.stderr)
        if isinstance(error, ValueError):
            print(str(error), file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
