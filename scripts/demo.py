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
import connector_plugin
import demo_perf
import fake_feeds
import gotest
import subscription_plugin
import verify_report
from local import ROOT, Stack, port, run


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('command', choices=['dev', 'verify', 'perf', 'reset'])
    args = parser.parse_args()
    verify = args.command == 'verify'
    # verify and perf run on a stack of their own, removed afterwards.
    isolated = args.command in ('verify', 'perf')
    suffix = uuid.uuid4().hex[:10] if isolated else hashlib.sha256(str(ROOT).encode()).hexdigest()[:10]
    stack = Stack(f'quivr-demo-{args.command}-' + suffix if isolated else 'quivr-demo-' + suffix)
    stack.verifying = isolated
    if args.command == 'reset':
        stack.down(True)
        return
    process = None
    started = time.monotonic()
    status = 'failed'
    browser_report = stack.directory / 'playwright.json'
    failures = []
    def interrupted(*_):
        raise KeyboardInterrupt()
    signal.signal(signal.SIGTERM, interrupted)
    try:
        run(['npm', 'ci', '--prefix', 'quivr-search'])
        run(['npm', 'run', 'build', '--prefix', 'quivr-search'])
        if isolated:
            run(['quivr-search/node_modules/.bin/playwright', 'install', 'chromium'])
        # The Alertes tab needs the alerts plugin, whatever QUIVR_ALERTS says. Described
        # alerts are judged by the fake System One server in verification, by TypeSafe
        # in `make demo` when TYPESAFE_API_KEY is set, and are not offered otherwise.
        described = subscription_plugin.described_mode(verify)
        subscription_plugin.select(stack, True, described)
        # The demo's feeds need the first-party connector plugins, whatever QUIVR_<ID> says.
        connector_plugin.select_first_party(stack, [row['id'] for row in connector_plugin.FIRST_PARTY])
        stack.up()
        demo_port = port() if isolated else int(os.environ.get('DEMO_PORT', '5183'))
        demo_password = secrets.token_hex(24) if isolated else os.environ.get('DEMO_PASSWORD', '')
        env = {**os.environ, 'HOST': '127.0.0.1', 'PORT': str(demo_port),
               'QUIVR_API_URL': f"http://127.0.0.1:{stack.state['api_port']}",
               'QUIVR_API_KEY': stack.state['demo'], 'DEMO_PASSWORD': demo_password,
               'DEMO_SECURE_COOKIE': 'false', 'DEMO_STATE_FILE': str(stack.directory / 'demo-state.json'),
               # Keyword alerts use the pinned plugin and read Matches through the API.
               'QUIVR_DEMO_ALERTS_EVALUATOR': subscription_plugin.KEYWORD_EVALUATOR,
               'DEMO_DESCRIBED_ALERTS': 'true' if described != 'off' else ''}
        feeds_url = None
        if verify:
            # The Admin tab rereads the engine's rollups as often as the stack writes them (OBSERVABILITY_OVERRIDES).
            env['DEMO_STATS_MS'] = '200'
        if isolated:
            # Browser tests and the performance corpus use feeds from a local test site, never the internet.
            feeds_server, feeds_url = fake_feeds.start(port=port())
            env.update(DEMO_FEED_PRIVATE_ORIGINS=feeds_url, DEMO_FEED_SUGGESTIONS=json.dumps([
                {'title': 'Fil continu exemple', 'url': feeds_url + '/feeds/ticker.xml?run=suggested'},
                {'title': 'La Revue exemple — Monde', 'url': feeds_url + '/feeds/world.xml?run=suggested'}]))
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
            test_env = {**os.environ, 'QUIVR_DEMO_URL': base, 'QUIVR_DEMO_PASSWORD': demo_password, 'QUIVR_DEMO_FEEDS_URL': feeds_url,
                        'QUIVR_DEMO_ARTIFACTS': str(stack.directory / 'browser')}
            # The JSON report names each failed spec and times every spec (THE-755); list stays in browser.log.
            test_env['PLAYWRIGHT_JSON_OUTPUT_NAME'] = str(browser_report)
            with (stack.directory / 'browser.log').open('w') as log:
                try:
                    run(['npm', 'test', '--prefix', 'quivr-search', '--', '--reporter=list,json'], env=test_env, stdout=log, stderr=log)
                except subprocess.CalledProcessError:
                    failures = verify_report.browser_results(browser_report)[1] or [
                        {'test': 'npm test', 'seconds': None, 'excerpt': gotest.excerpt((stack.directory / 'browser.log').read_text(errors='replace').splitlines())}]
                    for failure in failures:
                        failure['log'] = str(stack.directory / 'browser.log')
                    raise
        elif args.command == 'perf':
            demo_perf.run(base, demo_password, feeds_url, stack.directory, ROOT)
        else:
            print(f'Demo: {base} — Ctrl+C to stop; texts persist until make demo-reset.', flush=True)
            process.wait()
        status = 'passed'
    except KeyboardInterrupt:
        if isolated:
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
            stack.down(isolated)
        # perf writes its own report (perf.json); no return here, which would swallow its failure.
        if args.command != 'perf':
            (stack.directory / 'demo-report.json').write_text(json.dumps({
                'status': status, 'duration_seconds': round(time.monotonic() - started, 3),
                'scope': 'Real core + production demo facade + Chromium browser and HTTP tests',
                'tests': verify_report.browser_results(browser_report)[0], 'failures': failures,
            }, indent=2))
        for failure in failures:
            print(f"\n--- FAIL: {failure['test']} (log {failure['log']})\n" + '\n'.join('    ' + line for line in failure['excerpt'].splitlines()), flush=True)
        print('Demo artifacts:', stack.directory)


if __name__ == '__main__':
    main()
