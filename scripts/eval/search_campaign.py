#!/usr/bin/env python3
"""Start, resume and stop a bounded configuration-search campaign outside CI."""
import argparse
import copy
import concurrent.futures
import contextlib
import tempfile
import threading
import subprocess
import time
import json
import math
import os
import pathlib
import re
import sys

import control_store
import network_recovery
import campaign_store
import modal_search
import results
import search_trial

import ci_guard

ROOT = pathlib.Path(__file__).resolve().parents[2]
IDENTIFIER = re.compile(r'[A-Za-z0-9_.-]{1,120}')


def specification(value):
    fields = {'version', 'name', 'ticket', 'policy', 'goal', 'space', 'parallelism', 'max_trials', 'seed'}
    if not isinstance(value, dict) or set(value) != fields or value['version'] != 1:
        raise ValueError('campaign requires only the version-1 fields')
    cfg = json.loads(results.encode(value))
    for key in ('name', 'ticket'):
        if not isinstance(cfg[key], str) or not IDENTIFIER.fullmatch(cfg[key]):
            raise ValueError('campaign and ticket identifiers must be plain opaque identifiers')
    for key, maximum in (('parallelism', 4), ('max_trials', 100000), ('seed', 2**32 - 1)):
        if type(cfg[key]) is not int or not (0 if key == 'seed' else 1) <= cfg[key] <= maximum:
            raise ValueError('invalid campaign parallelism, trial limit or seed')
    cfg['policy'] = modal_search.policy(cfg['policy'])
    for key in ('provider_total_usd', 'modal_total_usd', 'end_at'):
        if key not in cfg['policy']:
            raise ValueError('multi-day campaigns require total caps and end_at')
    baseline = cfg['policy']['baseline']
    if (baseline['model'] != 'Cohere-Embed-V5-Pro' or baseline['dimensions'] != 1024
            or baseline['dense_weight'] != .5):
        raise ValueError('campaign baseline must be explicit Pro, 1024 dimensions, hybrid weight 0.5')
    goal = cfg['goal']
    if (not isinstance(goal, dict) or set(goal) != {'metric', 'weights'} or goal['metric'] != 'ndcg@10'
            or not isinstance(goal['weights'], dict) or not goal['weights']):
        raise ValueError('goal requires ndcg@10 and positive eligible-set weights')
    eligible = {name for name, entry in cfg['policy']['sets'].items() if not entry['diagnostic']}
    if set(goal['weights']) != eligible or any(type(w) not in (int, float) or not math.isfinite(w) or w <= 0
                                               for w in goal['weights'].values()):
        raise ValueError('goal weights must cover exactly the eligible datasets')
    space = cfg['space']
    if not isinstance(space, dict) or not space or set(space) - set(baseline):
        raise ValueError('search space requires supported configuration keys')
    for key, dist in space.items():
        if not isinstance(dist, dict):
            raise ValueError('invalid search distribution')
        if set(dist) == {'choices'}:
            choices = dist['choices']
            if not isinstance(choices, list) or not choices or len(choices) > 100:
                raise ValueError('categorical distribution needs bounded choices')
            for choice in choices:
                search_trial.configuration({**baseline, key: choice})
        elif set(dist) <= {'low', 'high', 'step'} and {'low', 'high'} <= set(dist):
            low, high = dist['low'], dist['high']
            integer = key in ('dimensions', 'window_chars', 'overlap_chars', 'candidate_count')
            for v in (low, high, dist.get('step', 1 if integer else .01)):
                if type(v) not in ((int,) if integer else (int, float)) or not math.isfinite(v):
                    raise ValueError('distribution requires finite correctly typed bounds')
            if low > high or dist.get('step', 1 if integer else .01) <= 0:
                raise ValueError('distribution requires ordered bounds and positive step')
            for bound in (low, high):
                search_trial.configuration({**baseline, key: bound})
        else:
            raise ValueError('distribution requires choices or low/high/step')
    return cfg


def read_spec(path):
    import yaml
    return specification(yaml.safe_load(path.read_text()))



def aggregate(report, sets):
    """Campaign export boundary: never copy records, sample IDs or raw errors."""
    status = report.get('status')
    if status not in ('exploration_finalist', 'rejected', 'capped', 'failed', 'leased'):
        status = 'failed'
    output = {'status': status, 'verdict': report.get('verdict') if report.get('verdict') in ('better', 'cheaper', 'rejected') else None,
              'gates': {}, 'aggregate_sets': {}, 'evidence': []}
    if status == 'failed':
        output['verdict'] = None
        output['reason'] = modal_search.INCOMPLETE_MEASUREMENT
    for name in ('quality', 'no_loss', 'latency', 'price'):
        gate = report.get('gates', {}).get(name, {})
        output['gates'][name] = {'passed': gate.get('passed') is True}
    for name in sets:
        pair = report.get('aggregate_sets', {}).get(name, {})
        output['aggregate_sets'][name] = {}
        for side in ('candidate', 'baseline'):
            source = pair.get(side, {})
            output['aggregate_sets'][name][side] = {
                key: source[key] for key in ('ndcg_at_10', 'latency_p95_ms', 'cost_per_search_usd', 'cost_per_1000_documents_usd')
                if type(source.get(key)) in (int, float) and math.isfinite(source[key]) and source[key] >= 0}
    for item in report.get('work', {}).values():
        receipt = item.get('receipt', {})
        key = receipt.get('result_key', '')
        rid = receipt.get('run_id', '')
        if isinstance(key, str) and re.fullmatch(r'[a-f0-9]{64}', key):
            output['evidence'].append({'result_key': key, 'status': 'synced' if receipt.get('status') == 'synced' else 'pending',
                                       'run_id': rid if isinstance(rid, str) and IDENTIFIER.fullmatch(rid) else None})
    return output


def objectives(report, weights):
    pairs = report['aggregate_sets']
    try:
        quality = sum(weights[n] * pairs[n]['candidate']['ndcg_at_10'] for n in weights) / sum(weights.values())
        cost = max(pairs[n]['candidate']['cost_per_search_usd'] for n in weights)
        latency = max(pairs[n]['candidate']['latency_p95_ms'] for n in weights)
        if quality > 1:
            raise ValueError('invalid quality')
        return [quality, cost, latency]
    except (KeyError, TypeError, ValueError):
        return None


def suggest(trial, spec):
    config = dict(spec['policy']['baseline'])
    for key, dist in spec['space'].items():
        if 'choices' in dist:
            config[key] = trial.suggest_categorical(key, dist['choices'])
        elif key in ('dimensions', 'window_chars', 'overlap_chars', 'candidate_count'):
            config[key] = trial.suggest_int(key, dist['low'], dist['high'], step=dist.get('step', 1))
        else:
            config[key] = trial.suggest_float(key, dist['low'], dist['high'], step=dist.get('step', .01))
    return search_trial.configuration(config)


class Loop:
    def __init__(self, store, name, owner, study, measure):
        self.store, self.name, self.owner, self.study, self.measure = store, name, owner, study, measure
        self.spec = store.snapshot(name)['spec']

    def complete(self, trial, value):
        import optuna
        self.store.renew_owner(self.name, self.owner)
        report = value.get('report')
        if report is None:
            return
        numbers = objectives(report, self.spec['goal']['weights'])
        if report['status'] in ('exploration_finalist', 'rejected') and numbers is not None:
            self.study.tell(trial.number, numbers, skip_if_finished=True)
        else:
            self.study.tell(trial.number, state=optuna.trial.TrialState.FAIL, skip_if_finished=True)

    def perform(self, trial, value):
        self.store.renew_owner(self.name, self.owner)
        result = None
        def publish(report):
            nonlocal result
            saved = {**value, 'report': aggregate(report, self.spec['policy']['sets'])}
            self.store.trial(self.name, self.owner, trial.number, saved)
            result = saved
        try:
            report = self.measure(value['config'], on_report=publish)
            if result is None:
                publish(report)  # No app was admitted, or an external transport returned directly.
        except (control_store.LeaseLost, control_store.Unavailable, control_store.Contention,
                campaign_store.CleanupPending, network_recovery.Outage):
            raise
        except Exception:
            if result is not None:
                raise  # Cleanup cannot overwrite an already published measured report.
            publish({'status': 'failed'})
        return trial, result

    def tick(self, limit=None):
        import optuna
        self.store.renew_owner(self.name, self.owner)
        available = self.store.availability(self.name)
        if available['stopped'] or available['paused']:
            return available
        import campaign_reporting
        snapshot = self.store.snapshot(self.name)
        self.spec = {**snapshot['spec'], 'space': snapshot.get('space', snapshot['spec']['space'])}
        campaign_reporting.enqueue(self.study, snapshot)
        limit = limit or self.spec['parallelism']
        running = self.study.get_trials(states=(optuna.trial.TrialState.RUNNING,))
        state = self.store.snapshot(self.name)['trials']
        replayed, pending = [], []
        for trial in running:
            value = state.get(str(trial.number))
            if value and 'report' in value and value['report']['status'] not in ('capped', 'leased'):
                self.complete(trial, value)
                replayed.append(trial)
            else:
                # No paid launch precedes this durable config mapping. Orphan
                # asks can safely complete suggestions before their first launch.
                if value is None:
                    live_trial = optuna.trial.Trial(self.study, trial._trial_id)
                    try:
                        value = {'config': suggest(live_trial, self.spec)}
                        self.store.trial(self.name, self.owner, trial.number, value)
                    except ValueError:
                        self.study.tell(trial.number, state=optuna.trial.TrialState.FAIL, skip_if_finished=True)
                        continue
                pending.append((trial, value))
        # A replay tick does not immediately launch replacement work.
        if replayed:
            return {'replayed': len(replayed)}
        while len(pending) < limit and (self.study.get_trials(states=(optuna.trial.TrialState.WAITING,))
                                       or len(self.study.trials) < self.spec['max_trials']):
            self.store.renew_owner(self.name, self.owner)
            trial = self.study.ask()
            try:
                value = {'config': suggest(trial, self.spec)}
                self.store.trial(self.name, self.owner, trial.number, value)
                pending.append((trial, value))
            except ValueError:
                self.study.tell(trial.number, state=optuna.trial.TrialState.FAIL, skip_if_finished=True)
        if not pending:
            self.store.stop(self.name, 'trial limit reached')
            return {'stopped': 'trial limit reached'}
        with concurrent.futures.ThreadPoolExecutor(max_workers=limit) as pool:
            futures = [pool.submit(self.perform, trial, value) for trial, value in pending[:limit]]
            for future in concurrent.futures.as_completed(futures):
                trial, value = future.result()
                self.store.renew_owner(self.name, self.owner)
                if value['report']['status'] not in ('capped', 'leased'):
                    self.complete(trial, value)
        result = self.store.availability(self.name)
        result['waiting'] = any(value['report']['status'] == 'leased' for _, value in [f.result() for f in futures])
        result['cleanup_pending'] = any(r['status'] != 'closed'
                                       for r in self.store.snapshot(self.name)['resources'].values())
        return result


@contextlib.contextmanager
def open_study(name, url, seed=42):
    """Separate PostgreSQL schema, verified TLS, bounded pool; no live SQLite."""
    import optuna
    from sqlalchemy.engine import make_url
    from psycopg.conninfo import conninfo_to_dict
    parsed = make_url(url)
    if parsed.drivername not in ('postgresql', 'postgresql+psycopg'):
        raise ValueError('campaign study requires PostgreSQL')
    dsn = parsed.set(drivername='postgresql').render_as_string(hide_password=False)
    validator = control_store.Store(dsn)
    info = conninfo_to_dict(dsn)
    args = {'options': '-c search_path=eval_optuna', 'connect_timeout': 5}
    with contextlib.ExitStack() as stack:
        if validator.ca_pem:
            ca = stack.enter_context(tempfile.NamedTemporaryFile(mode='w', encoding='utf-8'))
            ca.write(validator.ca_pem)
            ca.flush()
            args['sslrootcert'] = ca.name
        if info.get('sslmode'):
            args['sslmode'] = info['sslmode']
        optuna.logging.set_verbosity(optuna.logging.WARNING)
        storage = optuna.storages.RDBStorage(parsed.set(drivername='postgresql+psycopg').render_as_string(hide_password=False),
            engine_kwargs={'pool_pre_ping': True, 'pool_size': 4, 'max_overflow': 0,
                           'creator': lambda: network_recovery.connect(dsn, **args)})
        try:
            yield optuna.create_study(study_name=name, storage=storage, load_if_exists=True,
                sampler=optuna.samplers.NSGAIISampler(seed=seed), directions=['maximize', 'minimize', 'minimize'])
        finally:
            storage.remove_session()
            storage.engine.dispose()



def watchdog_once(store, name, compute):
    import campaign_store
    available = store.availability(name)
    state = store.snapshot(name)
    if available['paused'] or available['stopped']:
        if state['owner']:
            store.release_owner(name, state['owner'])
        campaign_store.cleanup(store, name, compute)
    elif not state['live'] and not state.get('recoverable'):
        campaign_store.cleanup(store, name, compute)
    return available


@contextlib.contextmanager
def guard(store, name, owner, compute, stop_requested=None):
    """Renew ownership and enforce watchdog independently of blocking measurements."""
    done = threading.Event()
    stop_requested = stop_requested or threading.Event()
    failures = []
    def poll():
        next_check = time.monotonic() + 10
        while not done.is_set():
            try:
                # Signal handling only sets an event. Database admission closes
                # here, before the executor waits for interrupted remote calls.
                # Never reenter a DB transaction from a Python signal handler.
                requested = stop_requested.wait(1)
                if done.is_set():
                    return
                if requested:
                    store.stop(name)
                    watchdog_once(store, name, compute)
                    return
                if time.monotonic() < next_check:
                    continue
                available = watchdog_once(store, name, compute)
                if available['paused'] or available['stopped']:
                    return
                store.renew_owner(name, owner)
                next_check = time.monotonic() + 10
            except control_store.Contention:
                next_check = time.monotonic() + 10
            except Exception as error:
                failures.append(error)
                return
    thread = threading.Thread(target=poll, daemon=True)
    thread.start()
    def report():
        import campaign_reporting
        while not done.wait(60):
            campaign_reporting.notify(store, name)
    # Notification timeouts must never delay lease renewal or stop signals.
    reporter = threading.Thread(target=report, daemon=True)
    reporter.start()
    try:
        yield
        if failures:
            if isinstance(failures[0], (network_recovery.Outage, control_store.LeaseLost)):
                raise failures[0]
            raise control_store.Unavailable('campaign guard unavailable; no further scheduling')
    finally:
        done.set()
        thread.join(timeout=60)
        reporter.join(timeout=1)


def lineage():
    if subprocess.run(['git', 'diff', '--quiet', 'HEAD'], cwd=ROOT).returncode:
        raise ValueError('campaign measurement code must be committed')
    sha = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    scorer = 'sha256:' + search_trial.digest({name: (ROOT / 'scripts/eval' / name).read_text()
        for name in ('scoring.py', 'gates.py', 'search_trial.py', 'embeddings.py', 'direct_bakeoff.py', 'private_working.py', 'protected_inputs.py')})
    return sha, scorer


class NativeConfirmation:
    """Translate the trusted engine receipt to the campaign promotion protocol."""
    def __init__(self, store, name, configuration, outbox, *, transport=None):
        import engine_confirmation as native
        if not isinstance(configuration, dict) or set(configuration) != {
                'heldout_family', 'datasets', 'mapping_policy', 'engine_runner_git_sha'}:
            raise ValueError('confirmation requires frozen input and engine metadata')
        self.store, self.name, self.outbox = store, name, pathlib.Path(outbox)
        policy = store.policy(name)
        # Native validation allowlists every metadata field before persistence.
        preview = native.build_request(store, name, 0, policy['baseline'],
            configuration['heldout_family'], configuration['datasets'],
            {n: {'baseline': 'preview/baseline', 'candidate': 'preview/candidate'} for n in policy['sets']},
            configuration['mapping_policy'], configuration['engine_runner_git_sha'])
        self.configuration = {k: preview[k] for k in configuration}
        store.confirmation_configuration(name, self.configuration)
        self.transport = transport or native.ModalAdapter(store, outbox)
        self.engine_git_sha = preview['engine_runner_git_sha']
        self.engine_scorer_digest = preview['engine_runner_scorer_digest']
        self.heldout_fingerprint = preview['heldout_fingerprint']
        self.heldout_family = {'sets': native.family_sets(preview['heldout_family']),
                               'fingerprint': self.heldout_fingerprint}
        # Preserve safe family metadata in the campaign protocol as well.
        if 'sets' in preview['heldout_family']:
            self.heldout_family.update(preview['heldout_family'])
        self.confirmation_policy_digest = native.digest(native.invariants(preview, policy))

    def prepare(self, trial):
        import engine_confirmation as native
        cfg = self.configuration
        candidate = self.store.snapshot(self.name)['trials'][str(trial)]['config']
        native.mapping(candidate, self.store.policy(self.name)['baseline'],
                       cfg['mapping_policy']['production'], candidate=True)
        self.request = native.build_request(self.store, self.name, trial,
            candidate, cfg['heldout_family'],
            cfg['datasets'], self.store.development_keys(self.name, trial),
            cfg['mapping_policy'], self.engine_git_sha)
        # A receipt may authorize only the settings that the promotion target
        # actually deploys. Resolve omitted hosted values with its real offline
        # configure command; never guess execution defaults or segmentation.
        import campaign_promotion as promotion
        with promotion._checkout(ROOT, self.engine_git_sha) as checkout:
            baseline = promotion.effective_settings(checkout)
            effective = {'baseline': baseline, 'candidate': promotion._candidate_settings(
                checkout, candidate, self.store.policy(self.name)['baseline'], 'ranked')}
            manifest = promotion.HostedManifestBuilder()(checkout, baseline['hosted'])
        locked = {k: v.get('const') for k, v in manifest['configuration']['schema']['properties'].items()}
        for side, settings in effective.items():
            hosted = {**locked, **settings['hosted']}
            ingestion = self.request['effective_' + side]['ingestion']
            retrieve = {**settings['retrieve']}
            if retrieve['candidate_count'] == 'request_limit':
                retrieve['candidate_count'] = 10
            if (ingestion.get('kind') != 'hosted'
                    or any(hosted.get(k) != v for k, v in ingestion.items() if k != 'kind')
                    or retrieve != self.request['effective_' + side]['retrieve']):
                raise native.Unmappable('production_settings_mismatch')
        self.campaign_settings = effective

    def __call__(self, request, resource):
        import engine_confirmation as native
        import campaign_promotion as promotion
        req = self.request
        if any(request['effective_' + side] != settings
               for side, settings in self.campaign_settings.items()):
            raise promotion.Refused('confirmed settings differ from the promotion target')
        if any(request[k] != req[k] for k in ('campaign', 'trial', 'baseline_hash', 'candidate_hash',
                                              'git_sha', 'scorer_digest', 'engine_runner_git_sha',
                                              'engine_runner_scorer_digest')):
            raise promotion.Refused('native and campaign request identities differ')
        result = self.transport(copy.deepcopy(req), resource)
        if result.get('status') not in ('confirmed', 'rejected'):
            return {'status': result.get('status') if result.get('status') in
                    ('unavailable', 'leased', 'capped', 'failed') else 'failed', 'confirmation_available': False}
        bindings = {k: req[k] for k in native.BINDINGS}
        key = 'engine-confirmation/' + native.digest(bindings)
        publication = self.store.evidence(self.name, [key]).get(key, {})
        canonical = publication.get('output')
        if (result.get('bindings') != bindings
                or result.get('confirmation_policy_digest') != self.confirmation_policy_digest
                or result.get('confirmation_key') != key or result.get('cleanup_verified') is not True
                or result.get('confirmation_available') is not True
                or result.get('heldout') != {'passed': True, 'fingerprint': self.heldout_fingerprint}
                or canonical is None or {k: v for k, v in result.items() if k != 'receipts'} != canonical):
            raise promotion.Refused('native confirmation binding, policy or cleanup mismatch')
        ids = result.get('compute_ids')
        if (not isinstance(ids, dict) or set(ids) != {'app_id', 'sandbox_id'}
                or any(not isinstance(ids[k], str) or not re.fullmatch(prefix + r'-[A-Za-z0-9_-]+', ids[k])
                       for k, prefix in (('app_id', 'ap'), ('sandbox_id', 'sb')))):
            raise promotion.Refused('native compute termination identities are required')
        resources = self.store.snapshot(self.name)['resources']
        current_id = resource.resource['id'] if resource.resource else None
        if not any(r['app_id'] == ids['app_id'] and (r['status'] == 'closed' or identity == current_id)
                   for identity, r in resources.items()):
            raise promotion.Refused('native app identity is not registered')
        gates = result.get('gates', {})
        if (set(gates) != set(promotion.GATES) or any(type(gates[g].get('passed')) is not bool for g in gates)
                or (all(gates[g]['passed'] for g in gates)) != (result['status'] == 'confirmed')):
            raise promotion.Refused('native confirmation verdict is inconsistent')
        if not result.get('receipts') or any(set(r) != {'result_key', 'run_id', 'status'}
                or r['status'] != 'synced' for r in result['receipts']):
            raise promotion.Refused('native aggregate tracking must be synced')
        expected = {results.record(row)['result_key'] for row in publication['records']}
        if len(result['receipts']) != len(expected) or {r['result_key'] for r in result['receipts']} != expected:
            raise promotion.Refused('native tracking receipts do not match canonical measurements')
        tracking = results.Results(directory=self.outbox)
        for receipt in result['receipts']:
            actual = tracking.get(results.result_key(receipt))
            if actual.get('source') != 'mlflow' or actual.get('run_id') != receipt['run_id']:
                raise promotion.Refused('native aggregate tracking identity is not synced')
        return {k: copy.deepcopy(v) for k, v in {
            **result, 'bindings': request, 'compute_ids': [ids['app_id']]}.items()
            if k != 'confirmation_policy_digest'}


def advance_confirmations(store, name, owner, study, adapter, **kwargs):
    """Confirm Pareto finalists and reconcile durable results into Optuna."""
    import campaign_promotion
    outcomes = campaign_promotion.advance(store, name, owner, adapter=adapter, **kwargs)
    state = store.snapshot(name)
    summaries = {}
    for number, record in state.get('confirmations', {}).items():
        summaries[number] = {k: record[k] for k in ('status', 'reason')}
        if record.get('receipt'):
            summaries[number]['aggregate_sets'] = record['receipt']['aggregate_sets']
        elif record.get('report'):
            summaries[number]['aggregate_sets'] = record['report']['aggregate_sets']
    # Optuna freezes completed trials; study metadata is its supported mutable
    # annotation surface. Keep exploration objective values unchanged.
    if study.user_attrs.get('confirmations') != summaries:
        study.set_user_attr('confirmations', summaries)
    return outcomes


def public_status(store, name, study=None):
    import campaign_reporting
    import campaign_promotion
    state = store.snapshot(name)
    status = store.availability(name)
    output = {'campaign': name, **status, 'ledger': store.summary(name),
              'baseline': state['spec']['policy']['baseline'],
              'space': state.get('space', state['spec']['space']),
              'space_revision': len(state.get('space_revisions', [])),
              'goal': state['spec']['goal'], 'max_trials': state['spec']['max_trials'],
              'agent_token_usage': campaign_reporting.usage(state), 'confirmation_available': False,
              'next_plan': state.get('next_plan'),
              'proposals': [{'id': p['id'], 'config': p.get('config'), 'idea': p.get('idea')}
                            for p in state.get('proposals', {}).values()],
              'notifications': {day: {d: {'status': receipt['status']} for d, receipt in item['deliveries'].items()}
                                for day, item in state.get('digests', {}).items()},
              'compute_cap_notice': modal_search.COMPUTE_NOTICE,
              'cleanup_pending': any(r['status'] != 'closed' for r in state['resources'].values()),
              'trials': [{'number': int(number), 'config': value.get('config'),
                          'report': campaign_reporting.export_report(value.get('report'), state['spec']['policy']['sets'])}
                         for number, value in state['trials'].items()]}
    output['pareto'] = [{'number': p['number'], 'objectives': p['objectives'], 'confirmation': p['confirmation']}
                        for p in campaign_reporting.leaderboard(state)]
    if study is not None:
        output['pareto'] = [{'number': trial.number, 'objectives': trial.values,
                            'confirmation': study.user_attrs.get('confirmations', {}).get(str(trial.number), {})}
                            for trial in study.best_trials]
    output.update(campaign_promotion.public_status(state))
    return output


def supervise(store, name, study, outbox, *, once=False, poll_seconds=15, stop_requested=None,
              confirmation_adapter=None):
    previous_owner = None
    def acquired(owner):
        nonlocal previous_owner
        previous_owner = owner
    while True:
        try:
            try:
                return _supervise(store, name, study, outbox, once=once, poll_seconds=poll_seconds,
                                  stop_requested=stop_requested, confirmation_adapter=confirmation_adapter,
                                  owner_acquired=acquired)
            except control_store.LeaseBusy:
                # Only our own failed release is recoverable. Refuse another
                # live supervisor even after an earlier contention episode.
                if previous_owner is None or store.snapshot(name)['owner'] != previous_owner:
                    raise
        except control_store.Contention:
            pass
        if once:
            return retrying_status(name)
        time.sleep(poll_seconds)


def retrying_status(name):
    return {'campaign': name, 'status': 'retrying', 'reason': 'control store contended; no new paid work admitted'}


def _supervise(store, name, study, outbox, *, once=False, poll_seconds=15, stop_requested=None,
               confirmation_adapter=None, owner_acquired=None):
    import campaign_store
    import campaign_compute
    compute = campaign_compute.ModalCompute()
    configuration = store.snapshot(name).get('confirmation_configuration')
    if confirmation_adapter is None and configuration is not None:
        confirmation_adapter = NativeConfirmation(store, name, configuration, pathlib.Path(outbox) / 'confirmation')
    while True:
        try:
            available = watchdog_once(store, name, compute)
        except campaign_store.CleanupPending:
            if once:
                raise
            time.sleep(poll_seconds)
            continue
        if available['stopped']:
            import campaign_reporting
            campaign_reporting.notify(store, name)
            return public_status(store, name, study)
        if available['paused']:
            import campaign_reporting
            campaign_reporting.notify(store, name)
            if once:
                return public_status(store, name, study)
            time.sleep(poll_seconds)
            continue
        owner = store.acquire(name)
        if owner_acquired:
            owner_acquired(owner)
        try:
            loop = Loop(store, name, owner, study, campaign_compute.Measurement(store, name, owner, outbox, compute))
            with guard(store, name, owner, compute, stop_requested):
                # Recover finalists saved before a crash before tick can reach
                # the terminal trial limit and close new confirmation admission.
                advance_confirmations(store, name, owner, study, confirmation_adapter, compute=compute)
                while True:
                    result = loop.tick()
                    if result.get('cleanup_pending'):
                        break  # Drain exploration compute before confirmation or another trial.
                    import campaign_promotion
                    advance_confirmations(store, name, owner, study, confirmation_adapter, compute=compute)
                    import campaign_reporting
                    campaign_reporting.notify(store, name)
                    if once or result.get('paused') or result.get('stopped'):
                        break
                    if result.get('waiting'):
                        time.sleep(poll_seconds)
        except control_store.LeaseLost:
            if not store.availability(name)['paused'] and not store.availability(name)['stopped']:
                raise
        finally:
            if not isinstance(sys.exc_info()[1], network_recovery.Outage):
                store.release_owner(name, owner)
                try:
                    campaign_store.cleanup(store, name, compute)
                except campaign_store.CleanupPending:
                    if once:
                        raise
        if once:
            return public_status(store, name, study)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    validate = commands.add_parser('validate', help='validate YAML without keys or network')
    validate.add_argument('spec', type=pathlib.Path)
    start = commands.add_parser('start', help='register an immutable YAML campaign and run')
    start.add_argument('spec', type=pathlib.Path)
    for command in ('resume', 'status', 'stop', 'watchdog'):
        sub = commands.add_parser(command)
        sub.add_argument('campaign')
    for command in ('usage', 'propose', 'digest'):
        sub = commands.add_parser(command, help='durable lead/reporting operation; no measurement dispatch')
        sub.add_argument('campaign')
        if command != 'digest':
            sub.add_argument('file', type=pathlib.Path, help='exact receipt or bounded proposal JSON')
        else:
            sub.add_argument('--send', action='store_true', help='deliver through provisioned Linear/Slack credentials')
    for command in ('confirm', 'promote'):
        sub = commands.add_parser(command)
        sub.add_argument('campaign')
        sub.add_argument('trial', type=int)
        if command == 'promote':
            sub.add_argument('--open-pr', action='store_true', help='publish a settings PR only from trusted confirmation')
    for sub in (start, commands.choices['resume'], commands.choices['confirm']):
        sub.add_argument('--confirmation-configuration', type=pathlib.Path,
                         help='freeze trusted held-out fingerprints, production/resources and engine revision')
    commands.choices['confirm'].add_argument('--allow-paid', action='store_true', help='operator-only confirmation outside CI')
    commands.choices['confirm'].add_argument('--outbox', type=pathlib.Path, default=ROOT / '.scratch/eval/results')
    for sub in (start, *(commands.choices[c] for c in ('resume', 'stop', 'watchdog'))):
        sub.add_argument('--allow-paid', action='store_true', help='explicit operator-only live lifecycle; never CI')
        sub.add_argument('--once', action='store_true', help='perform one scheduling/watchdog pass')
        sub.add_argument('--outbox', type=pathlib.Path, default=ROOT / '.scratch/eval/results')
    args = parser.parse_args(argv)
    if args.command == 'promote' and (not args.open_pr or ci_guard.in_ci()):
        parser.error('promotion requires --open-pr outside CI')
    if args.command == 'digest' and args.send and ci_guard.in_ci():
        parser.error('notification delivery is refused in CI')
    if args.command == 'confirm' and args.allow_paid and ci_guard.in_ci():
        parser.error('live confirmation is refused in CI')
    if args.command == 'confirm' and args.confirmation_configuration and not args.allow_paid:
        parser.error('confirmation configuration requires --allow-paid outside CI')
    if args.command == 'validate':
        try:
            print(results.encode(read_spec(args.spec)))
            return 0
        except Exception:
            # YAML parser exceptions can include entire input lines.
            print(results.encode({'status': 'invalid', 'reason': 'campaign YAML or configuration is invalid'}))
            return 2
    if args.command in ('start', 'resume', 'stop', 'watchdog') and (not args.allow_paid or ci_guard.in_ci()):
        parser.error('live lifecycle requires --allow-paid outside CI')
    import campaign_store
    import campaign_compute
    try:
        store = campaign_store.CampaignStore(os.environ['EVAL_CONTROL_DATABASE_URL'])
        if args.command == 'start':
            spec = read_spec(args.spec)
            name = spec['name']
            store.register(spec, *lineage())
        else:
            name = args.campaign
            if not IDENTIFIER.fullmatch(name):
                raise ValueError('invalid campaign identifier')
        if getattr(args, 'confirmation_configuration', None):
            NativeConfirmation(store, name, json.loads(args.confirmation_configuration.read_text()),
                               args.outbox / 'confirmation')
        if args.command in ('confirm', 'promote'):
            import campaign_promotion
            state = store.snapshot(name)
            if args.trial < 0 or str(args.trial) not in state['trials']:
                raise ValueError('invalid trial number')
            if args.command == 'confirm':
                configuration = state.get('confirmation_configuration')
                if configuration is None or not args.allow_paid:
                    output = campaign_promotion.confirmation_status(store, name, args.trial)
                else:
                    compute = campaign_compute.ModalCompute()
                    watchdog_once(store, name, compute)
                    owner = store.acquire(name)
                    try:
                        with guard(store, name, owner, compute):
                            adapter = NativeConfirmation(store, name, configuration, args.outbox / 'confirmation')
                            output = campaign_promotion.confirm(store, name, args.trial, owner,
                                                                adapter=adapter, compute=compute)
                    finally:
                        store.release_owner(name, owner)
                        campaign_store.cleanup(store, name, compute)
            else:
                output = campaign_promotion.promote(store, name, args.trial)
        elif args.command in ('usage', 'propose', 'digest'):
            import campaign_reporting
            if args.command == 'usage':
                output = campaign_reporting.ingest_usage(store, name, json.loads(args.file.read_text()))
            elif args.command == 'propose':
                output = campaign_reporting.propose(store, name, json.loads(args.file.read_text()))
            elif args.send:
                output = campaign_reporting.digest(store, name)
            else:
                output = {'body': campaign_reporting.digest_body(store.snapshot(name), store.availability(name), store.summary(name))}
        elif args.command == 'stop':
            store.stop(name)
            watchdog_once(store, name, campaign_compute.ModalCompute())
            output = public_status(store, name)
        elif args.command == 'watchdog':
            while True:
                try:
                    watchdog_once(store, name, campaign_compute.ModalCompute())
                    output = public_status(store, name)
                except control_store.Contention:
                    output = retrying_status(name)
                if args.once or output.get('stopped'):
                    break
                time.sleep(15)
        else:
            state = store.snapshot(name)
            with open_study(name, os.environ['EVAL_STUDY_DATABASE_URL'], state['spec']['seed']) as study:
                if args.command == 'status':
                    output = public_status(store, name, study)
                else:
                    if (state['git_sha'], state['scorer_digest']) != lineage():
                        raise ValueError('resume requires the frozen measurement checkout')
                    import signal
                    stop_requested = threading.Event()
                    def interrupted(*_):
                        stop_requested.set()
                        raise KeyboardInterrupt()
                    signal.signal(signal.SIGTERM, interrupted)
                    signal.signal(signal.SIGINT, interrupted)
                    try:
                        output = supervise(store, name, study, args.outbox, once=args.once,
                                           stop_requested=stop_requested)
                    except KeyboardInterrupt:
                        store.stop(name)
                        watchdog_once(store, name, campaign_compute.ModalCompute())
                        output = public_status(store, name, study)
        print(results.encode(output))
        if args.command in ('confirm', 'promote'):
            return 0 if output['status'] in ('confirmed', 'opened') else 2
        if args.command == 'digest' and args.send:
            return 0 if all(v['status'] == 'delivered' for v in output.values()) else 2
        return 0
    except control_store.Contention:
        print(results.encode(retrying_status(name)))
        return 2
    except campaign_store.CleanupPending:
        print(results.encode({'status': 'cleanup_pending', 'reason': 'compute termination is not acknowledged; retry stop or watchdog'}))
        return 2
    except Exception:
        print(results.encode({'status': 'failed', 'reason': 'campaign admission or operation failed; no local fallback'}))
        return 2


if __name__ == '__main__':
    raise SystemExit(main())
