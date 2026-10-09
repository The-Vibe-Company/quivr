"""Select CI from the complete merge-base diff and verify selected job results.

Unknown inputs run everything. This is shared by PR, main and release verification;
measurements and the nightly upgrade proof are outside this inventory.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tomllib

ROOT = Path(__file__).resolve().parent.parent
CHEAP = ['guards', 'go', 'python']


def catalog():
    import check
    import local
    import release_images
    return list(check.CI_GROUPS), list(local.parts()) + [local.DEMO], release_images.inventory(ROOT, 'The-Vibe-Company/quivr')['include']


def changed_paths(base, head='HEAD', root=ROOT):
    """NUL framing also covers spaces/newlines, deleted files and both rename names."""
    if not base:
        return None
    try:
        merge_base = subprocess.check_output(['git', 'merge-base', base, head], cwd=root, stderr=subprocess.DEVNULL).strip()
        raw = subprocess.check_output(['git', 'diff', '--name-status', '-z', '--find-renames', merge_base.decode(), head], cwd=root)
    except subprocess.CalledProcessError:
        return None
    fields = iter(raw.decode('utf-8', errors='surrogateescape').split('\0')[:-1])
    paths = []
    for status in fields:
        paths.append(next(fields))
        if status.startswith(('R', 'C')):
            paths.append(next(fields))
    return paths


def select(paths, full=False):
    all_groups, all_parts, all_images = catalog()
    groups, parts, images = set(CHEAP), set(), set()
    site, migration, vulnerabilities = False, False, False
    reason = 'affected paths'
    if paths is None:
        full, reason = True, 'diff unavailable'
    documentation = tomllib.loads((ROOT / 'docs/inventory.toml').read_text())['pages']
    eval_groups = {g for g in all_groups if g.startswith('eval-')}
    connector_plugins = {'rss', 'x-list', 'm365-mail', 'object-storage-archive'}
    for path in paths or []:
        # Documentation can change lint/site inputs, but cannot change a runtime image.
        if path in documentation or path.startswith(('docs/', 'docs-site/')) or path.endswith('/README.md') or path in {'README.md', 'AGENTS.md', 'CLAUDE.md', 'CONTEXT.md', 'LICENSE'}:
            site = True
        elif path.startswith('quivr-search/'):
            parts.add('demo')
        elif path.startswith('scripts/eval/') or path.startswith('deploy/mlflow/'):
            groups.update(eval_groups)
        elif path.startswith('plugins/'):
            plugin = path.split('/')[1]
            entry = next((i for i in all_images if i['plugin'] == plugin), None)
            if entry is None:
                full, reason = True, 'unknown plugin input'
                continue
            images.add(plugin)
            if path.endswith('/quivr-plugin.yaml'):
                images.add('')
            if plugin in {'jev-rerank', 'core-ingest', 'core-retrieve', 'hosted-embed'}:
                groups.update(eval_groups)
            groups.add('sdk-go' if (ROOT / 'plugins' / plugin / 'go.mod').exists() else 'sdk-python')
            parts.add('connectors' if plugin in connector_plugins else 'plugins')
            # These plugins are also pinned by the default stack and its core proofs.
            if plugin in {'core-ingest', 'hosted-embed', 'pdf-text', 'newsml-g2'}:
                parts.update(all_parts)
        elif path.startswith('sdks/'):
            language = path.split('/')[1]
            if language not in {'go', 'python'}:
                full, reason = True, 'unknown SDK input'
                continue
            groups.add('sdk-' + language)
            groups.update(eval_groups)
            parts.update(all_parts)
            images.update(i['plugin'] for i in all_images if i['plugin'])
        elif path.startswith('internal/monitoring/'):
            groups.update(eval_groups)
            parts.add('monitoring')
            images.add('')
            vulnerabilities = True
        elif path.startswith('internal/connectors/'):
            groups.update(eval_groups)
            parts.add('connectors')
            images.add('')
            vulnerabilities = True
        elif path.startswith(('internal/', 'cmd/', 'client/')):
            groups.update(eval_groups)
            parts.update(all_parts)
            images.add('')
            vulnerabilities = True
            if path.startswith(('cmd/quivr-plugin-api/', 'cmd/quivr-reference/', 'internal/transport/generated/', 'client/')):
                groups.update({'contracts', 'sdk-go', 'sdk-python'})
        elif path.startswith('migrations/'):
            groups.update(eval_groups)
            parts.update(all_parts)
            migration = True
            images.add('')
        elif path.startswith('contracts/'):
            groups.update(all_groups)
            parts.update(all_parts)
            images.update(i['plugin'] for i in all_images)
            site = True
        elif path.startswith(('scripts/measure_', 'tests/load/', 'conformance/')):
            # Local-only measurement inputs; the cheap lane validates Python/conformance.
            pass
        elif path in {'.gitignore', '.gitattributes', '.editorconfig', 'armada.toml', 'skills-lock.json'} or path.startswith(('.agents/', '.claude/', '.codex/')):
            pass
        else:
            # Includes workflow/action, Go dependency, deployment, test and shared harness changes.
            full, reason = True, 'shared or unclassified input'
    if full:
        return {'groups': all_groups, 'parts': all_parts, 'images': all_images, 'site': True,
                'migration-compatibility': True, 'vulnerabilities': True, 'full': True, 'reason': reason}
    return {'groups': [g for g in all_groups if g in groups], 'parts': [p for p in all_parts if p in parts],
            'images': [i for i in all_images if i['plugin'] in images], 'site': site,
            'migration-compatibility': migration, 'vulnerabilities': vulnerabilities,
            'full': False, 'reason': reason}


def complete(selected, results):
    """Every selected dependency must succeed; only explicitly irrelevant skips pass."""
    return results.get('paths-filter') == 'success' and all(
        results.get(job) == 'success' if needed else results.get(job) in {'success', 'skipped'}
        for job, needed in selected.items())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--aggregate', choices=['check', 'verify'])
    args = parser.parse_args()
    if args.aggregate:
        needs = json.loads(os.environ['CI_NEEDS'])
        outputs = needs.get('paths-filter', {}).get('outputs', {})
        if args.aggregate == 'verify' and any(outputs.get(key) not in {'true', 'false'} for key in
                                             ['has-parts', 'has-images', 'site', 'migration-compatibility', 'vulnerabilities', 'full']):
            print('Missing or invalid selector outputs', file=sys.stderr)
            return 1
        selected = {'check-group': True} if args.aggregate == 'check' else {
            'check': True, 'adapter-postgres': True,
            'part': outputs.get('has-parts') == 'true', 'site': outputs.get('site') == 'true',
            'migration-compatibility': outputs.get('migration-compatibility') == 'true',
            'vulnerabilities': outputs.get('vulnerabilities') == 'true',
            'image-inventory': outputs.get('has-images') == 'true',
            'image-vulnerabilities': outputs.get('has-images') == 'true',
        }
        results = {job: info['result'] for job, info in needs.items()}
        print(json.dumps({'selected': selected, 'results': results}, indent=2))
        return 0 if complete(selected, results) else 1
    event_name = os.environ.get('GITHUB_EVENT_NAME', '')
    event_path = os.environ.get('GITHUB_EVENT_PATH')
    event = json.loads(Path(event_path).read_text()) if event_path else {}
    labels = {label['name'] for label in event.get('pull_request', {}).get('labels', [])}
    full = event_name != 'pull_request' or 'ci-full' in labels
    base = event.get('pull_request', {}).get('base', {}).get('sha')
    head = event.get('pull_request', {}).get('head', {}).get('sha', 'HEAD')
    selection = select([] if full else changed_paths(base, head), full=full)
    values = {'groups': json.dumps({'group': selection['groups']}),
              # Nonempty placeholders avoid matrix expansion errors on intentionally skipped jobs.
              'parts': json.dumps({'part': selection['parts'] or ['unused']}),
              'images': json.dumps({'include': selection['images'] or [{'plugin': 'unused', 'file': '', 'target': ''}]}),
              'has-parts': str(bool(selection['parts'])).lower(), 'has-images': str(bool(selection['images'])).lower()}
    values.update({key: str(selection[key]).lower() for key in ['site', 'migration-compatibility', 'vulnerabilities', 'full']})
    print(json.dumps(selection, indent=2))
    if os.environ.get('GITHUB_OUTPUT'):
        with open(os.environ['GITHUB_OUTPUT'], 'a') as output:
            for key, value in values.items():
                output.write(f'{key}={value}\n')
    if os.environ.get('GITHUB_STEP_SUMMARY'):
        with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as output:
            output.write('## Selected CI\n\n```json\n' + json.dumps(selection, indent=2) + '\n```\n')
    return 0


if __name__ == '__main__':
    sys.exit(main())
