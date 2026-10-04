#!/usr/bin/env python3
"""Protected runner entrypoint. SQL admission precedes input fetch/decryption."""
import argparse
import json
import os
import pathlib
import signal
import sys

import control_store
import engine_measurement
import engine_stack
import results


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--supervise', action='store_true')
    parser.add_argument('--request', required=True)
    parser.add_argument('--output', type=pathlib.Path)
    parser.add_argument('--progress', type=pathlib.Path)
    args = parser.parse_args(argv)
    request = json.loads(args.request)
    if args.supervise:
        return engine_stack.supervise(request['policy'], [sys.executable, __file__, '--request', args.request])
    if args.output is None:
        parser.error('internal runner needs --output')
    def cancel(*_):
        raise InterruptedError('confirmation interrupted')
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, cancel)
    try:
        store = control_store.Store(os.environ['EVAL_CONTROL_DATABASE_URL'])
        references = json.loads(os.environ['EVAL_CONFIRM_INPUTS'])
        runtime = {'endpoint': os.environ.get('AZURE_FOUNDRY_ENDPOINT', ''),
                   'key': os.environ.get('AZURE_FOUNDRY_KEY', '')}
        # Engine/plugin subprocesses receive local engine credentials only.
        # The trusted gate and Store already hold their required credentials.
        for key in list(os.environ):
            if key.startswith(('EVAL_', 'MLFLOW_', 'AZURE_FOUNDRY_')):
                del os.environ[key]
        row = engine_measurement.execute(store, request, references, runtime, args.progress)
    except Exception as error:
        row = {'status': 'failed', **engine_stack.failure(error), 'confirmation_available': False,
               'remote_cleanup_verified': False}
    results.save(args.output, row)
    return 0 if row['status'] in ('confirmed', 'rejected') else 2


if __name__ == '__main__':
    raise SystemExit(main())
