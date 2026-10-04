#!/usr/bin/env python3
"""Run a capped temporary full-engine smoke on Modal; never confirms finalists."""
import argparse
import asyncio
import decimal
import hashlib
import json
import os
import pathlib
import re
import signal
import subprocess
import time
import logging

import control_store
import results
import embeddings

ROOT = pathlib.Path(__file__).resolve().parents[2]
COMPUTE_NOTICE = ('The compute cap covers runner reservations, not the full Modal invoice; '
                  'image builds, storage and other account charges need separate operator budgeting.')


def policy(value):
    defaults = {'experiment': 'public/engine-smoke', 'modal_daily_usd': 10,
                'modal_usd_per_second': .001, 'startup_seconds': 900,
                'max_seconds': 1800, 'cleanup_seconds': 60, 'reaper_seconds': 90,
                'keepalive_seconds': 10, 'cpu': 8, 'memory_mib': 16384}
    if not isinstance(value, dict) or set(value) - set(defaults):
        raise ValueError('unknown smoke policy fields; smoke accepts no datasets or candidates')
    cfg = {**defaults, **value}
    if not isinstance(cfg['experiment'], str) or not re.fullmatch(r'public/[A-Za-z0-9/_.-]+', cfg['experiment']):
        raise ValueError('smoke needs a public aggregate experiment')
    for name in ('modal_daily_usd', 'modal_usd_per_second'):
        if control_store.money(cfg[name]) <= 0:
            raise ValueError('smoke caps and conservative resource rate must be positive')
    bounds = {'startup_seconds': (30, 3600), 'max_seconds': (30, 82800),
              'cleanup_seconds': (10, 300), 'reaper_seconds': (30, 300),
              'keepalive_seconds': (1, 30), 'cpu': (2, 64), 'memory_mib': (8192, 262144)}
    for name, (low, high) in bounds.items():
        if type(cfg[name]) is not int or not low <= cfg[name] <= high:
            raise ValueError('invalid smoke resource or lifetime bound: ' + name)
    if cfg['reaper_seconds'] < 3 * cfg['keepalive_seconds']:
        raise ValueError('reaper must allow at least three keepalive intervals')
    if lifetime(cfg) + 120 > 86400:
        raise ValueError('smoke lifetime and publication margin exceed lease maximum')
    return cfg


def lifetime(cfg):
    return sum(cfg[key] for key in ('startup_seconds', 'max_seconds', 'cleanup_seconds', 'reaper_seconds'))


def reservation(cfg):
    return decimal.Decimal(lifetime(cfg)) * control_store.money(cfg['modal_usd_per_second'])


def lineage():
    import engine_stack
    return engine_stack.lineage()


def record(cfg, campaign, sha, measured, elapsed):
    """Construct aggregates; arbitrary remote fields never reach SQL/tracking."""
    import engine_stack
    if (measured.get('status') != 'complete' or measured.get('remote_cleanup_verified') is not True
            or measured.get('sandbox_terminated') is not True or measured.get('documents') != 3
            or measured.get('searches') != 3 or measured.get('modes') != engine_stack.MODES):
        raise RuntimeError('smoke evidence or verified cleanup is incomplete')
    image = measured.get('image_id')
    if not isinstance(image, str) or not re.fullmatch(r'im-[A-Za-z0-9_-]+', image):
        raise ValueError('smoke image identity is missing')
    return {'schema_version': 1, 'experiment': cfg['experiment'], 'git_sha': sha,
            'plugin_digest': 'sha256:' + hashlib.sha256((ROOT / 'scripts/eval/engine_stack.py').read_bytes()).hexdigest(),
            'tier': 'engine', 'machine': f"cpu{cfg['cpu']}-memory{cfg['memory_mib']}-vm",
            'config': {'kind': 'smoke', 'campaign': campaign, 'policy': cfg, 'lineage': lineage(), 'image_id': image},
            'duration_seconds': elapsed,
            'dataset': {'name': 'engine-smoke', 'version': '1', 'split': 'smoke',
                        'fingerprint': engine_stack.fingerprint(), 'private': False},
            'metrics': {'documents': 3, 'searches': 3}, 'per_query': {},
            'cost': {'modal_seconds_upper_bound': elapsed,
                     'modal_usd_upper_bound': elapsed * float(cfg['modal_usd_per_second']),
                     'compute_cap_notice': COMPUTE_NOTICE},
            'provenance': {'kind': 'smoke', 'modes': list(engine_stack.MODES),
                           'remote_cleanup_verified': True, 'sandbox_terminated': True,
                           'confirmation_available': False}}


def image_logs(image_id):
    """Only opaque image identifiers may enter the failure envelope."""
    if isinstance(image_id, str) and re.fullmatch(r'im-[A-Za-z0-9_-]+', image_id):
        return {'image_id': image_id, 'image_logs': 'modal image logs ' + image_id}
    return {'image_id': None}


async def dispatch(store, campaign, cfg, sha, invoke, outbox):
    import engine_stack
    frozen = {**cfg, 'kind': 'smoke', 'provider_daily_usd': 1, 'git_sha': sha,
              'lineage': lineage(), 'fixture_fingerprint': engine_stack.fingerprint()}
    store.campaign(campaign, frozen)
    key = 'engine-smoke/' + hashlib.sha256(results.encode(frozen).encode()).hexdigest()
    ttl = lifetime(cfg) + 120
    claim = store.claim(campaign, key, ttl)
    tracking = results.Results(directory=outbox)
    if claim['status'] == 'done':
        return {'status': 'reused', 'kind': 'smoke', 'confirmation_available': False,
                'receipt': tracking.log(claim['payload'])}
    if claim['status'] == 'leased':
        return {'status': 'leased', 'kind': 'smoke', 'confirmation_available': False}
    owner, charged, started = claim['owner'], None, time.monotonic()
    image_id = None
    async def renew():
        await asyncio.to_thread(store.renew, campaign, key, owner, ttl)
    try:
        charged = store.reserve(campaign, 'modal', reservation(cfg),
                               {'kind': 'engine-smoke', 'lifetime_seconds': lifetime(cfg)}, (key, owner))
        request = {'campaign': campaign, 'policy': cfg, 'git_sha': sha,
                   'lease_key': key, 'owner': owner, 'outbox': str(outbox)}
        measured = await asyncio.wait_for(invoke(request, renew), timeout=lifetime(cfg))
        image_id = measured.get('image_id')
        elapsed = time.monotonic() - started
        # Even failed measurements are settled only when complete remote
        # teardown and termination were observed. Unknown completion stays held.
        if measured.get('sandbox_terminated') is True:
            store.settle(charged, decimal.Decimal(str(elapsed)) * control_store.money(cfg['modal_usd_per_second']),
                         {'sandbox_terminated': True, 'modal_seconds_upper_bound': elapsed})
        row = record(cfg, campaign, sha, measured, elapsed)
        await renew()
        store.publish(campaign, key, owner, row)
        return {'status': 'complete', 'kind': 'smoke', 'confirmation_available': False,
                'documents': 3, 'searches': 3, 'modes': list(engine_stack.MODES),
                'receipt': tracking.log(row), 'ledger': store.summary(campaign)}
    except embeddings.BudgetExceeded as error:
        status = 'capped'
        diagnostic = engine_stack.failure(error)
    except asyncio.CancelledError as error:
        status = 'cancelled'
        diagnostic = engine_stack.failure(InterruptedError())
    except Exception as error:
        status = 'failed'
        diagnostic = engine_stack.failure(error)
    try:
        store.abandon(campaign, key, owner, 'capped' if status == 'capped' else 'failed')
    except (control_store.LeaseLost, control_store.Unavailable):
        pass
    return {'status': status, 'kind': 'smoke', 'confirmation_available': False, **diagnostic,
            **image_logs(image_id),
            'reason': status + '; smoke did not publish clean evidence; unknown charges retained',
            'compute_cap_notice': COMPUTE_NOTICE}


COMPOSE_VERSION = '2.39.4'
COMPOSE_SHA = '7af95166a730b87e172d4fc9aefea8725d3c6c7327d59149267b452114ddb7d4'


def image_definition(modal):
    tracked = set(subprocess.check_output(['git', 'ls-files', '-z'], cwd=ROOT).decode().split('\0'))
    def ignored(path):
        relative = str(path)  # Modal passes paths relative to the upload root.
        return relative not in tracked and not any(name.startswith(relative.rstrip('/') + '/') for name in tracked)
    go = lineage()['go_version']
    return (modal.Image.from_registry(f'golang:{go}-bookworm', add_python='3.12')
            .entrypoint([]).apt_install('docker.io', 'curl', 'ca-certificates', 'iptables')
            .run_commands('mkdir -p /usr/local/lib/docker/cli-plugins',
                f'curl -fsSL https://github.com/docker/compose/releases/download/v{COMPOSE_VERSION}/docker-compose-linux-x86_64 '
                '-o /usr/local/lib/docker/cli-plugins/docker-compose',
                f'echo "{COMPOSE_SHA}  /usr/local/lib/docker/cli-plugins/docker-compose" | sha256sum -c -',
                'chmod 755 /usr/local/lib/docker/cli-plugins/docker-compose', 'docker compose version')
            .pip_install('PyYAML==6.0.3')
            .env({'GOTOOLCHAIN': 'local', 'HF_HUB_DISABLE_TELEMETRY': '1', 'DO_NOT_TRACK': '1'})
            .add_local_dir(ROOT, '/repo', copy=True, ignore=ignored)
            .run_commands('cd /repo && go mod download', 'cd /repo && python scripts/prepare_tokenizer.py',
                          'cd /repo && python scripts/prepare_embeddings.py'))


async def run_modal(request, renew):
    """Bounded SDK boundary. Success leaves this scope only after terminate(wait)."""
    import modal
    cfg = request['policy']
    sandbox, waiter, heartbeat = None, None, None
    observed, verified = None, False
    recovery = pathlib.Path(request['outbox']) / 'active' / (request['campaign'] + '.json')
    recovery.parent.mkdir(parents=True, exist_ok=True)
    async def keepalive():
        while True:
            await renew()
            sandbox.stdin.write(b'ping\n')
            await sandbox.stdin.drain.aio()
            await asyncio.sleep(cfg['keepalive_seconds'])
    async def observe():
        nonlocal observed
        async for line in sandbox.stdout:
            if len(line) > 4096:
                raise ValueError('oversized smoke output')
            row = json.loads(line)
            if row.get('event') == 'result':
                observed = row
            elif row.get('event') == 'progress':
                phase, count = row.get('phase'), row.get('count')
                if phase in ('docker', 'starting', 'ingesting', 'searching', 'cleanup') and type(count) is int and 0 <= count <= 3:
                    logging.info('smoke phase=%s count=%d', phase, count)
                    results.save(recovery, {'app_id': app.app_id, 'sandbox_id': sandbox.object_id,
                                            'state': 'running', 'phase': phase})
        await sandbox.wait.aio(raise_on_termination=False)
    app = modal.App('quivr-engine-smoke')
    try:
        async with app.run.aio(detach=False):
            image = image_definition(modal)
            results.save(recovery, {'app_id': app.app_id, 'state': 'creating'})
            try:
                sandbox = await asyncio.wait_for(modal.Sandbox.create.aio(
                    'python', '/repo/scripts/eval/engine_stack.py', '--supervise', '--settings', results.encode(cfg),
                    app=app, image=image, runtime='vm', cpu=(cfg['cpu'], cfg['cpu']),
                    memory=(cfg['memory_mib'], cfg['memory_mib']), timeout=lifetime(cfg),
                    workdir='/repo', tags={'kind': 'engine-smoke', 'campaign': request['campaign']}),
                    timeout=cfg['startup_seconds'])
                results.save(recovery, {'app_id': app.app_id, 'sandbox_id': sandbox.object_id, 'state': 'running'})
                heartbeat, waiter = asyncio.create_task(keepalive()), asyncio.create_task(observe())
                done, _ = await asyncio.wait((waiter, heartbeat), timeout=cfg['max_seconds'] + cfg['cleanup_seconds'],
                                             return_when=asyncio.FIRST_COMPLETED)
                if not done:
                    raise TimeoutError('smoke execution deadline expired')
                if heartbeat in done:
                    await heartbeat  # ownership/transport failure stops work immediately
                    raise RuntimeError('smoke keepalive stopped')
                await waiter
            finally:
                for task in (heartbeat, waiter):
                    if task:
                        task.cancel()
                await asyncio.gather(*(t for t in (heartbeat, waiter) if t), return_exceptions=True)
                if sandbox is not None:
                    await asyncio.wait_for(sandbox.terminate.aio(wait=True), cfg['cleanup_seconds'])
                    verified = await sandbox.poll.aio() is not None
                    results.save(recovery, {'app_id': app.app_id, 'sandbox_id': sandbox.object_id,
                                            'state': 'terminated' if verified else 'unknown'})
                await renew()
        if observed is None:
            raise RuntimeError('smoke did not return aggregate evidence')
        return {**observed, 'sandbox_terminated': verified, 'image_id': image.object_id}
    except Exception as error:
        import engine_stack
        return {'status': 'failed', **engine_stack.failure(error),
                'remote_cleanup_verified': False, 'sandbox_terminated': verified,
                **image_logs(image.object_id if sandbox is not None else getattr(error, 'image_id', None))}


def launch(cfg, campaign, outbox):
    if subprocess.run(['git', 'diff', '--quiet', 'HEAD'], cwd=ROOT).returncode:
        raise ValueError('measurement code must be committed before live dispatch')
    sha = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    store = control_store.Store(os.environ['EVAL_CONTROL_DATABASE_URL'])
    async def operation():
        loop, task = asyncio.get_running_loop(), asyncio.current_task()
        cancelled = False
        def cancel():
            nonlocal cancelled
            if not cancelled:
                cancelled = True
                task.cancel()
        for sig in (signal.SIGINT, signal.SIGTERM):
            loop.add_signal_handler(sig, cancel)
        try:
            return await dispatch(store, campaign, cfg, sha, run_modal, outbox)
        finally:
            for sig in (signal.SIGINT, signal.SIGTERM):
                loop.remove_signal_handler(sig)
    return asyncio.run(operation())


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--policy', type=pathlib.Path, help='optional smoke resource/rate policy')
    parser.add_argument('--dry-run', action='store_true')
    parser.add_argument('--smoke', action='store_true')
    parser.add_argument('--allow-paid', action='store_true', help='coordinator-only; forbidden in CI')
    parser.add_argument('--campaign', default='')
    parser.add_argument('--outbox', type=pathlib.Path, default=ROOT / '.scratch/eval/engine-smoke')
    args = parser.parse_args(argv)
    try:
        cfg = policy(json.loads(args.policy.read_text()) if args.policy else {})
    except (ValueError, KeyError, OSError, decimal.InvalidOperation) as error:
        parser.error('invalid smoke policy (' + type(error).__name__ + ')')
    if args.dry_run:
        print(results.encode({'kind': 'smoke', 'policy': cfg, 'lineage': lineage(),
                              'modal_reservation_usd': float(reservation(cfg)),
                              'confirmation_available': False, 'compute_cap_notice': COMPUTE_NOTICE}))
        return 0
    if any(os.environ.get(k, '').lower() not in ('', '0', 'false') for k in ('CI', 'GITHUB_ACTIONS')):
        parser.error('CI measurements are refused')
    if not args.smoke or not args.allow_paid or not re.fullmatch(r'[A-Za-z0-9_.-]+', args.campaign):
        parser.error('live smoke requires --smoke --allow-paid --campaign with a plain identifier')
    if not os.environ.get('EVAL_CONTROL_DATABASE_URL'):
        parser.error('coordinator must supply EVAL_CONTROL_DATABASE_URL')
    logging.basicConfig(level=logging.INFO, format='%(message)s')
    try:
        report = launch(cfg, args.campaign, args.outbox)
    except Exception as error:
        import engine_stack
        report = {'status': 'failed', 'kind': 'smoke', 'confirmation_available': False, **engine_stack.failure(error)}
    print(results.encode(report))
    return 0 if report['status'] in ('complete', 'reused') else 2


if __name__ == '__main__':
    raise SystemExit(main())
