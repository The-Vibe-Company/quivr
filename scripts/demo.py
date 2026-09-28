#!/usr/bin/env python3
"""Run the browser demo against an isolated, real local core."""
import argparse
import hashlib
import json
import os
import secrets
import signal
import subprocess
import time
import urllib.request
import uuid
from local import ROOT, Stack, port, run


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('command', choices=['dev', 'verify', 'reset'])
    args = parser.parse_args()
    verify = args.command == 'verify'
    suffix = uuid.uuid4().hex[:10] if verify else hashlib.sha256(str(ROOT).encode()).hexdigest()[:10]
    stack = Stack(('quivr-demo-verify-' if verify else 'quivr-demo-') + suffix)
    if args.command == 'reset':
        stack.down(True)
        return
    process = None
    started = time.monotonic()
    status = 'failed'
    def interrupted(*_):
        raise KeyboardInterrupt()
    signal.signal(signal.SIGTERM, interrupted)
    try:
        run(['npm', 'ci', '--prefix', 'quivr-search'])
        run(['npm', 'run', 'build', '--prefix', 'quivr-search'])
        if verify:
            run(['quivr-search/node_modules/.bin/playwright', 'install', 'chromium'])
        stack.up()
        demo_port = port() if verify else int(os.environ.get('DEMO_PORT', '5183'))
        demo_password = secrets.token_hex(24) if verify else os.environ.get('DEMO_PASSWORD', '')
        env = {**os.environ, 'HOST': '127.0.0.1', 'PORT': str(demo_port),
               'QUIVR_API_URL': f"http://127.0.0.1:{stack.state['api_port']}",
               'QUIVR_API_KEY': stack.state['demo'], 'DEMO_PASSWORD': demo_password,
               'DEMO_SECURE_COOKIE': 'false'}
        base = f'http://127.0.0.1:{demo_port}'
        with (stack.directory / 'demo-server.log').open('w') as log:
            process = subprocess.Popen(['node', str(ROOT / 'quivr-search/server.mjs')], env=env, stdout=log, stderr=log)
        deadline = time.monotonic() + 10
        while True:
            if process.poll() is not None:
                raise RuntimeError('demo server failed; inspect demo-server.log')
            try:
                with urllib.request.urlopen(base + '/healthz', timeout=1):
                    break
            except OSError:
                if time.monotonic() > deadline:
                    raise RuntimeError('demo readiness timed out')
                time.sleep(.1)
        if verify:
            test_env = {**os.environ, 'QUIVR_DEMO_URL': base, 'QUIVR_DEMO_PASSWORD': demo_password,
                        'QUIVR_DEMO_ARTIFACTS': str(stack.directory / 'browser')}
            with (stack.directory / 'browser.log').open('w') as log:
                run(['npm', 'test', '--prefix', 'quivr-search'], env=test_env, stdout=log, stderr=log)
        else:
            print(f'Demo: {base} — Ctrl+C to stop; texts persist until make demo-reset.', flush=True)
            process.wait()
        status = 'passed'
    except KeyboardInterrupt:
        if verify:
            raise
    finally:
        if process is not None and process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        try:
            stack.capture()
        finally:
            stack.down(verify)
        (stack.directory / 'demo-report.json').write_text(json.dumps({
            'status': status, 'duration_seconds': round(time.monotonic() - started, 3),
            'scope': 'Real core + production demo facade + Chromium browser and HTTP tests',
        }, indent=2))
        print('Demo artifacts:', stack.directory)


if __name__ == '__main__':
    main()
