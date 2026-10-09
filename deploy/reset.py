#!/usr/bin/env python3
"""Preview or reset a dedicated installation using the existing deployment adapters."""
import argparse
import json
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from deploy.reset_support import ResetFailure


def declaration(path):
    data = json.loads(path.read_text())
    allowed = {'version', 'deployment', 'platform', 'project', 'environment', 'services', 'dedicated'}
    if not isinstance(data, dict) or set(data) - allowed or data.get('version') != 1:
        raise ValueError('invalid installation declaration')
    for field in ('deployment', 'project'):
        if not isinstance(data.get(field), str) or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_. /-]{0,199}', data[field]):
            raise ValueError('invalid deployment selector')
    if data.get('platform') not in ('compose', 'railway'):
        raise ValueError('unsupported reset platform')
    if data['platform'] == 'railway' and not isinstance(data.get('environment'), str):
        raise ValueError('Railway requires an explicit environment')
    return data


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('-f', type=Path, required=True, help='private installation JSON declaration')
    parser.add_argument('--confirm', action='store_true')
    parser.add_argument('--deployment-name', help='type the declared deployment name exactly')
    args = parser.parse_args(argv)
    phase = 'preflight'
    try:
        spec = declaration(args.f)
        if (args.confirm or args.deployment_name is not None) and (not args.confirm or args.deployment_name != spec['deployment']):
            raise ValueError('confirmation requires --confirm and the typed deployment name')
        if spec.get('dedicated') is not True:
            raise ValueError('reset requires dedicated=true: each selected data volume contains only this installation data')
        if spec['platform'] == 'compose':
            from deploy.compose.reset import ComposeReset
            adapter = ComposeReset(spec)
        else:
            from deploy.railway.reset import RailwayReset
            adapter = RailwayReset(spec)
        adapter.checkpoint_path = Path(str(args.f.resolve()) + '.reset-state.json')
        plan = adapter.preview()
        print(json.dumps({'deployment': spec['deployment'], 'mode': 'reset' if args.confirm else 'preview', 'scope': plan}, indent=2), flush=True)
        if not args.confirm:
            return 0
        phase = 'reset'
        result = adapter.reset()
        print(json.dumps({'status': 'reset', 'result': result}, indent=2))
        return 0
    except ValueError as error:
        # Only locally authored validation errors are displayed; providers use
        # RuntimeError with an already sanitized message.
        print(str(error), file=sys.stderr)
        return 2
    except ResetFailure as error:
        print(f'{error} Retain private reset state and inspect the selected deployment privately.', file=sys.stderr)
        return 1
    except Exception:
        print(f'Reset incomplete at phase {phase}, role/condition installation. Retain private reset state and inspect the selected deployment privately.', file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
