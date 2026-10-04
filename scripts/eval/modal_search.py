#!/usr/bin/env python3
"""Measure public campaign-dev search configurations on Modal, with shared caps."""
import argparse
import decimal
import json
import os
import pathlib
import re
import subprocess
import time

import control_store
import direct_bakeoff
import embeddings
import gates
import public_sets
import results
import search_trial

ROOT = pathlib.Path(__file__).resolve().parents[2]
COMPUTE_NOTICE = ('The compute cap covers compute reserved by this runner, not the full Modal invoice; '
                  'builds, storage and other account charges need separate operator budgeting.')
DIAGNOSTIC = {'mldr-fr': 'joint baseline saturation above 80%',
              'webfaq-fr': 'joint baseline saturation above 80%',
              'trec-covid': 'small query sample', 'mkqa-fr': 'short-answer proxy'}


def failure_summary(error):
    """Short diagnostics without reflected credentials or endpoint URLs."""
    message = str(error)
    credentials = [value for name, value in os.environ.items() if value and
                   re.search(r'KEY|TOKEN|SECRET|PASSWORD|DSN|DATABASE_URL|ENDPOINT|CREDENTIAL|AUTH', name, re.I)]
    for value in sorted(credentials, key=len, reverse=True):
        message = message.replace(value, '[redacted]')
    message = re.sub(r'\b[A-Za-z][A-Za-z0-9+.-]*://[^\s<>\"\']+', '[url]', message)
    message = re.sub(r'(?i)\bBearer\s+\S+', 'Bearer [redacted]', message)
    message = re.sub(r'(?i)\b([\w-]*(?:key|token|secret|password|authorization|credential)[\w-]*)\b[\"\']?\s*[:=]\s*'
                     r'(?:\"[^\"]*\"|\'[^\']*\'|[^\s,;]+)', r'\1=[redacted]', message)
    message = re.sub(r'\b(?:sk-|armada_launch_|armada_)[A-Za-z0-9_-]+', '[redacted]', message)
    return type(error).__name__, ' '.join(message.split())[:160]


def policy(value):
    defaults = {'profile': 'default', 'provider_daily_usd': 1000, 'modal_daily_usd': 1000,
                'max_seconds': 3600, 'startup_seconds': 300, 'baseline': {},
                'prices': {**direct_bakeoff.PRICES, 'jev-1.13.0': .042}, 'gates': {},
                'agent_token_usage': None}
    allowed = set(defaults) | {'experiment', 'sets', 'modal_usd_per_second', 'price_revision',
                               'provider_total_usd', 'modal_total_usd', 'end_at', 'confirmation_limit'}
    if not isinstance(value, dict) or set(value) - allowed:
        raise ValueError('unknown campaign policy fields')
    cfg = {**defaults, **value}
    if not re.fullmatch(r'public/[A-Za-z0-9/_.-]+', cfg['experiment']):
        raise ValueError('tier 1 requires a public aggregate experiment')
    if cfg['profile'] not in ('default', 'deep') or not re.fullmatch(r'[A-Za-z0-9_.-]+', cfg['price_revision']):
        raise ValueError('invalid profile or pricing revision')
    cfg['baseline'] = search_trial.configuration(cfg['baseline'])
    for field in ('provider_daily_usd', 'modal_daily_usd', 'modal_usd_per_second'):
        if control_store.money(cfg[field]) <= 0:
            raise ValueError('daily caps and conservative compute rate must be positive')
    for field, maximum in (('max_seconds', 86400), ('startup_seconds', 3600)):
        if type(cfg[field]) is not int or not 1 <= cfg[field] <= maximum:
            raise ValueError('invalid bounded invocation lifetime')
    if cfg['max_seconds'] < 30:
        raise ValueError('maximum invocation must allow at least 30 seconds')
    if cfg['max_seconds'] + cfg['startup_seconds'] > 86400:
        raise ValueError('total invocation lifetime must fit the maximum lease lifetime')
    if set(cfg['prices']) != set(defaults['prices']):
        raise ValueError('provide all supported provider prices')
    for rate in cfg['prices'].values():
        control_store.money(rate)
    if set(cfg['gates']) - {'min_gain', 'latency_ratio', 'index_usd', 'search_usd'}:
        raise ValueError('unknown gate thresholds')
    for threshold in cfg['gates'].values():
        if control_store.money(threshold) <= 0:
            raise ValueError('gate thresholds must be positive')
    if not isinstance(cfg['sets'], dict) or not cfg['sets']:
        raise ValueError('campaign requires a complete dataset family')
    cfg['sets'] = json.loads(results.encode(cfg['sets']))
    for name, entry in cfg['sets'].items():
        if not isinstance(entry, dict) or set(entry) - {'split', 'diagnostic', 'reason'}:
            raise ValueError('unknown dataset policy fields')
        if entry.get('split') != 'dev':
            raise PermissionError('tier 1 cannot read campaign-heldout/test data')
        if name not in public_sets.SETS:
            raise ValueError('tier 1 requires a registered public dataset')
        mandatory = DIAGNOSTIC.get(name)
        if not public_sets.SETS[name]['promotion_eligible']:
            mandatory = 'restricted licence; diagnostic only'
        if type(entry.get('diagnostic', False)) is not bool:
            raise ValueError('diagnostic must be boolean')
        entry['diagnostic'] = bool(mandatory) or entry.get('diagnostic', False)
        entry['reason'] = mandatory or entry.get('reason')
        if entry['diagnostic'] and (not isinstance(entry['reason'], str) or not entry['reason'].strip()):
            raise ValueError('diagnostic datasets require an explicit reason')
        if entry['reason'] is not None and not re.fullmatch(r'[A-Za-z0-9 .;%-]+', entry['reason']):
            raise ValueError('diagnostic reason must be plain words, without secrets')
    for field in ('provider_total_usd', 'modal_total_usd'):
        if field in cfg and control_store.money(cfg[field]) <= 0:
            raise ValueError('total caps must be positive')
    if 'end_at' in cfg:
        cfg['end_at'] = control_store.end_timestamp(cfg['end_at'])
    if 'confirmation_limit' in cfg and (type(cfg['confirmation_limit']) is not int
                                      or not 1 <= cfg['confirmation_limit'] <= 10):
        raise ValueError('confirmation limit must be 1..10')
    usage = cfg['agent_token_usage']
    if usage is not None and (not isinstance(usage, dict) or set(usage) != {'input_tokens', 'output_tokens'}
            or any(type(v) is not int or v < 0 for v in usage.values())):
        raise ValueError('agent usage requires exact reported input/output token integers')
    return cfg


def frozen_policy(policy, sha, scorer_digest, fresh_latency=True):
    return {**policy, 'git_sha': sha, 'scorer_digest': scorer_digest,
            'registry_digest': search_trial.digest(public_sets.SETS), 'fresh_latency': fresh_latency}


def dispatch(store, campaign, policy, cfg, name, sha, scorer_digest, invoke, outbox, fresh_latency):
    frozen = frozen_policy(policy, sha, scorer_digest, fresh_latency)
    store.campaign(campaign, frozen)
    key = search_trial.digest({'config': cfg, 'dataset': name, 'registry': public_sets.SETS[name],
                              'tier': 'direct', 'sha': sha, 'scorer': scorer_digest, 'fresh_latency': fresh_latency})
    claim = store.claim(campaign, key, policy['max_seconds'] + policy['startup_seconds'])
    tracking = results.Results(directory=outbox)
    if claim['status'] == 'done':
        return {'status': 'reused', 'record': claim['payload'], 'receipt': tracking.log(claim['payload'])}
    if claim['status'] == 'leased':
        return {'status': 'leased', 'reason': 'another worker owns this measurement'}
    owner, reservation = claim['owner'], None
    request = {'campaign': campaign, 'policy': frozen, 'config': cfg, 'dataset': name,
               'lease_key': key, 'owner': owner, 'git_sha': sha,
               'scorer_digest': scorer_digest, 'fresh_latency': fresh_latency}
    try:
        reservation = store.reserve(campaign, 'modal',
            (policy['max_seconds'] + policy['startup_seconds']) * control_store.money(policy['modal_usd_per_second']),
            {'measurement_key': key, 'max_seconds': policy['max_seconds'], 'startup_seconds': policy['startup_seconds']}, (key, owner))
        started = time.monotonic()
        row = invoke(request)
        elapsed = time.monotonic() - started
        store.settle(reservation, decimal.Decimal(str(elapsed)) * control_store.money(policy['modal_usd_per_second']), {'modal_seconds_upper_bound': elapsed})
        if row.get('status') in ('capped', 'failed'):
            return row
        # Remote publication must be canonical before any MLflow upload.
        canonical = store.claim(campaign, key)
        if canonical['status'] != 'done':
            raise RuntimeError('remote measurement did not publish canonical evidence')
        row = canonical['payload']
        return {'status': 'complete', 'record': row, 'receipt': tracking.log(row)}
    except embeddings.BudgetExceeded:
        status, reason = 'capped', 'daily reservation cap reached or usage bound exceeded'
    except Exception:
        status, reason = 'failed', 'measurement or evidence publication failed; uncertain charges retained'
    try:
        store.abandon(campaign, key, owner, status)
    except control_store.LeaseLost:
        pass  # A canonical result or replacement exists; never change it.
    return {'status': status, 'reason': reason}


def remote_trial(request):
    """Serialized Modal function. Imports and data access happen inside admission."""
    import logging
    import os
    import pathlib
    import sys
    import time
    sys.path.insert(0, '/repo/scripts/eval')
    import modal
    import control_store
    import direct_bakeoff
    import embeddings
    import public_sets
    import results
    import search_trial
    import trec
    logging.basicConfig(level=logging.INFO, format='%(message)s')
    log = logging.getLogger(__name__)
    log.info('trial started')
    cfg, policy = request['config'], request['policy']
    # Guard before even preparing/downloading a dataset.
    if policy['sets'][request['dataset']]['split'] != 'dev':
        raise PermissionError('tier 1 refuses campaign-heldout data')
    store = control_store.Store(os.environ['EVAL_CONTROL_DATABASE_URL'])
    store.campaign(request['campaign'], policy)
    lease = request['lease_key'], request['owner']
    store.renew(request['campaign'], *lease, ttl=policy['max_seconds'])
    budget = control_store.Budget(store, request['campaign'], lease)
    volume = modal.Volume.from_name('quivr-eval-embeddings-cache')
    volume.reload()
    # Replay committed evidence even when its originating container has exited.
    results.Results(directory='/eval-cache/results').sync()
    started = time.monotonic()
    try:
        name = request['dataset']
        directory = public_sets.prepare(name, pathlib.Path('/eval-cache/datasets'), include_restricted=True)
        data = trec.load(directory)
        log.info('dataset ready documents=%d queries=%d elapsed_seconds=%.3f',
                 len(data['corpus']), len(data['qrels']), time.monotonic() - started)
        dataset = {'name': name, 'version': search_trial.digest(public_sets.SETS[name]),
                   'split': 'dev', 'fingerprint': trec.fingerprint(directory), 'private': False}
        hosted = None
        if cfg['model'] != direct_bakeoff.E5_MODEL:
            hosted = direct_bakeoff.Hosted(os.environ['AZURE_FOUNDRY_ENDPOINT'], os.environ['AZURE_FOUNDRY_KEY'],
                                          budget, name, policy['prices'])
        measured = search_trial.measure(cfg, data, dataset, '/eval-cache/embeddings', budget, hosted,
                        policy['prices'], float(policy['modal_usd_per_second']), request['fresh_latency'],
                        os.environ.get('TYPESAFE_API_KEY', ''), volume.commit)
        measured['duration_seconds'] = time.monotonic() - started
        measured['cost'].update(modal_seconds=measured['duration_seconds'], resource_class='cpu8-memory16384',
                                agent_token_usage=policy['agent_token_usage'], compute_cap_notice=COMPUTE_NOTICE)
        row = search_trial.record(measured, {**cfg, 'profile': policy['profile'],
                    'campaign': request['campaign'], 'campaign_policy_hash': search_trial.digest(policy),
                    'prices_usd_per_million': policy['prices'],
                    'modal_usd_per_second': policy['modal_usd_per_second'],
                    'resource_class': 'cpu8-memory16384',
                    'price_revision': policy['price_revision'], 'fresh_latency': request['fresh_latency']},
                    policy['experiment'], request['git_sha'], request['scorer_digest'])
        log.info('measurement complete elapsed_seconds=%.3f', measured['duration_seconds'])
        store.publish(request['campaign'], *lease, row)
        results.Results(directory='/eval-cache/results').log(row)
        volume.commit()
        return row
    except embeddings.BudgetExceeded:
        log.info('trial capped elapsed_seconds=%.3f', time.monotonic() - started)
        store.abandon(request['campaign'], *lease, 'capped')
        return {'status': 'capped', 'reason': 'provider daily cap reached'}
    except Exception as error:
        kind, message = failure_summary(error)
        log.info('trial failed error=%s message=%s elapsed_seconds=%.3f',
                 kind, message, time.monotonic() - started)
        store.abandon(request['campaign'], *lease, 'failed')
        return {'status': 'failed', 'reason': 'direct measurement failed; uncertain charges retained'}


def launch(policy, candidate, campaign, outbox, fresh_latency, *, app_name='quivr-search-measurement',
           on_app=lambda identity: None, check=lambda: None):
    import modal
    if subprocess.run(['git', 'diff', '--quiet', 'HEAD'], cwd=ROOT).returncode:
        raise ValueError('measurement code must be committed before live dispatch')
    tracked = set(subprocess.check_output(['git', 'ls-files', '-z'], cwd=ROOT).decode().split('\0'))
    def ignored(path):
        path = pathlib.Path(path)
        relative = str(path.relative_to(ROOT)) if path.is_absolute() else str(path)
        return relative not in tracked and not any(name.startswith(relative.rstrip('/') + '/') for name in tracked)
    image = (modal.Image.debian_slim(python_version='3.12')
             .pip_install_from_requirements(str(ROOT / 'scripts/eval/requirements-modal.txt'))
             .add_local_dir(ROOT, '/repo', ignore=ignored))
    app = modal.App(app_name)
    secrets = [modal.Secret.from_name('quivr-eval-results'), modal.Secret.from_name('quivr-eval-embeddings')]
    if any(c['reranker'] == 'jev' for c in (candidate, policy['baseline'])):
        secrets.append(modal.Secret.from_name('quivr-eval-rerank'))
    remote = app.function(image=image, cpu=(8, 8), memory=(16384, 16384),
        timeout=policy['max_seconds'], startup_timeout=policy['startup_seconds'],
        retries=0, max_containers=4, scaledown_window=2, single_use_containers=True,
        include_source=False, serialized=True, secrets=secrets,
        volumes={'/eval-cache': modal.Volume.from_name('quivr-eval-embeddings-cache', create_if_missing=True)})(remote_trial)
    store = control_store.Store(os.environ['EVAL_CONTROL_DATABASE_URL'])
    sha = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    scorer_digest = 'sha256:' + search_trial.digest({name: (ROOT / 'scripts/eval' / name).read_text()
        for name in ('scoring.py', 'gates.py', 'search_trial.py', 'embeddings.py', 'direct_bakeoff.py')})
    pairs, work = {}, {}
    check()
    with app.run():
        on_app(app.app_id)
        for name in policy['sets']:
            pair = {}
            for side, cfg in (('baseline', policy['baseline']), ('candidate', candidate)):
                check()
                outcome = dispatch(store, campaign, policy, cfg, name, sha, scorer_digest, remote.remote, outbox, fresh_latency)
                work[name + '/' + side] = {k: v for k, v in outcome.items() if k != 'record'}
                if outcome['status'] in ('complete', 'reused'):
                    row = outcome['record']
                    pair[side] = {**row, 'per_query': {key.replace('_at_', '@'): values for key, values in row['per_query'].items()},
                                  'metrics': {key.replace('_at_', '@'): value for key, value in row['metrics'].items()}}
                elif outcome['status'] in ('capped', 'leased'):
                    return {'status': outcome['status'], 'reason': outcome['reason'], 'work': work,
                            'ledger': store.summary(campaign), 'compute_cap_notice': COMPUTE_NOTICE}
            if len(pair) == 2:
                pairs[name] = pair
    aggregate_sets = {name: {side: {key: row.get('metrics', {}).get(key.replace('_at_', '@'), row.get('metrics', {}).get(key))
                            for key in ('ndcg_at_10', 'latency_p95_ms', 'cost_per_search_usd', 'cost_per_1000_documents_usd')}
                            for side, row in pair.items()} for name, pair in pairs.items()}
    return {**gates.evaluate(pairs, policy), 'work': work, 'aggregate_sets': aggregate_sets, 'ledger': store.summary(campaign),
            'agent_token_usage': policy['agent_token_usage'], 'compute_cap_notice': COMPUTE_NOTICE}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--policy', required=True, type=pathlib.Path)
    parser.add_argument('--candidate', required=True, type=pathlib.Path)
    parser.add_argument('--campaign', default='')
    parser.add_argument('--outbox', type=pathlib.Path, default=ROOT / '.scratch/eval/results')
    parser.add_argument('--dry-run', action='store_true')
    parser.add_argument('--cached-exploration', action='store_true', help='no fresh query embedding calls; latency gate stays unmeasured')
    parser.add_argument('--allow-paid', action='store_true', help='coordinator-only live dispatch; never in CI')
    args = parser.parse_args(argv)
    cfg = policy(json.loads(args.policy.read_text()))
    candidate = search_trial.configuration(json.loads(args.candidate.read_text()))
    if args.dry_run:
        print(results.encode({'policy': cfg, 'candidate': candidate,
            'modal_reservation_usd_per_invocation': (cfg['max_seconds'] + cfg['startup_seconds']) * float(cfg['modal_usd_per_second']),
            'provider_estimator': 'shared UTF-8 byte + eight special tokens per input',
            'confirmation_available': False, 'compute_cap_notice': COMPUTE_NOTICE}))
        return 0
    if any(os.environ.get(k, '').lower() not in ('', '0', 'false') for k in ('CI', 'GITHUB_ACTIONS')):
        parser.error('CI measurements are refused')
    if not args.allow_paid or not args.campaign or not re.fullmatch(r'[A-Za-z0-9_.-]+', args.campaign):
        parser.error('live dispatch requires --allow-paid and a plain campaign identifier')
    if not os.environ.get('EVAL_CONTROL_DATABASE_URL'):
        parser.error('coordinator must supply EVAL_CONTROL_DATABASE_URL')
    try:
        report = launch(cfg, candidate, args.campaign, args.outbox, not args.cached_exploration)
    except Exception:
        report = {'status': 'failed', 'reason': 'measurement admission or dispatch failed; no local budget fallback',
                  'compute_cap_notice': COMPUTE_NOTICE}
    print(results.encode(report))
    return 0 if report['status'] == 'exploration_finalist' else 2


if __name__ == '__main__':
    raise SystemExit(main())
