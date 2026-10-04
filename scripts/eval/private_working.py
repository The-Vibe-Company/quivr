"""Private working comparisons: runtime references in, aggregate evidence out."""
import json
import os
import pathlib
import re
import tempfile
import time

import control_store
import direct_bakeoff
import gates
import protected_inputs
import results
import scoring
import search_trial
import trec

IDENTIFIER = re.compile(r'[A-Za-z0-9_.-]{1,120}')
HASH = re.compile(r'[a-f0-9]{64}')
MOUNT_ROOT = pathlib.Path('/eval-working')


def descriptor(name, entry):
    fields = {'name', 'version', 'split', 'digest', 'fingerprint', 'privacy'}
    if (not isinstance(entry, dict) or set(entry) != fields
            or not isinstance(name, str) or not IDENTIFIER.fullmatch(name) or name in ('.', '..')
            or entry['name'] != name or entry['privacy'] != 'private'
            or not isinstance(entry['version'], str) or not IDENTIFIER.fullmatch(entry['version'])
            or any(not isinstance(entry[k], str) or not HASH.fullmatch(entry[k]) for k in ('digest', 'fingerprint'))):
        raise ValueError('invalid private working descriptor')
    if entry['split'] != 'working':
        raise PermissionError('tier 1 requires the working split')
    return entry


def runtime(name):
    """Dispatcher-only references; these never enter the frozen policy."""
    entry = json.loads(os.environ['EVAL_WORKING_RUNTIME'])[name]
    if (set(entry) != {'volume', 'artifact', 'secret', 'identity_env', 'provider_consent'}
            or any(not isinstance(entry[k], str) or not IDENTIFIER.fullmatch(entry[k])
                   for k in ('volume', 'artifact', 'secret'))
            or entry['artifact'] in ('.', '..') or not entry['artifact'].endswith('.tar.gz.age')
            or not re.fullmatch(r'EVAL_WORKING_AGE_[A-Z0-9_]+', entry['identity_env'])
            or entry['provider_consent'] is not True):
        raise ValueError('invalid private working runtime references or missing provider consent')
    return entry


def mounts(policy):
    references = {n: runtime(n) for n, e in policy['sets'].items() if 'input' in e}
    return ({str(MOUNT_ROOT / n): r['volume'] for n, r in references.items()},
            sorted({r['secret'] for r in references.values()}))


def trial(request, store):
    """Admission is owned by remote_trial; plaintext and vectors die with this scope."""
    name, policy = request['dataset'], request['policy']
    entry = descriptor(name, policy['sets'][name]['input'])
    reference = request['working_runtime']
    if reference['provider_consent'] is not True:
        raise PermissionError('private provider consent required')
    lease = request['lease_key'], request['owner']
    with tempfile.TemporaryDirectory(prefix='eval-working-') as temp:
        root = pathlib.Path(temp)
        identity = root / 'identity.txt'
        identity.write_text(os.environ[reference['identity_env']])
        identity.chmod(0o600)
        directory = protected_inputs.decrypt(entry,
            MOUNT_ROOT / name / reference['artifact'], identity, root)
        data = trec.load(directory)
        dataset = {'name': name, 'version': entry['version'], 'split': 'dev',
                   'fingerprint': entry['fingerprint'], 'private': True}
        pairs, rows = {}, {}
        for side, cfg in (('baseline', policy['baseline']), ('candidate', request['config'])):
            store.renew(request['campaign'], *lease, ttl=policy['max_seconds'])
            budget = control_store.Budget(store, request['campaign'], lease)
            hosted = None if cfg['model'] == direct_bakeoff.E5_MODEL else direct_bakeoff.Hosted(
                os.environ['AZURE_FOUNDRY_ENDPOINT'], os.environ['AZURE_FOUNDRY_KEY'], budget, name, policy['prices'])
            started = time.monotonic()
            measured = search_trial.measure(cfg, data, dataset, root / 'vectors', budget, hosted,
                policy['prices'], float(policy['modal_usd_per_second']), request['fresh_latency'],
                os.environ.get('TYPESAFE_API_KEY', ''))
            measured['duration_seconds'] = time.monotonic() - started
            measured['cost']['resource_class'] = 'cpu8-memory16384'
            full_cfg = {**cfg, 'paired_side': side, 'profile': policy['profile'], 'campaign': request['campaign'],
                        'campaign_policy_hash': search_trial.digest(policy), 'fresh_latency': request['fresh_latency'],
                        'paired_candidate_hash': search_trial.digest(request['config']),
                        'prices_usd_per_million': policy['prices'],
                        'modal_usd_per_second': policy['modal_usd_per_second'],
                        'resource_class': 'cpu8-memory16384', 'price_revision': policy['price_revision']}
            pairs[side] = search_trial.record(measured, full_cfg, policy['experiment'],
                                             request['git_sha'], request['scorer_digest'])
            # Explicit output fields only: latency IDs, per-query scores and
            # arbitrary dependency metadata never become durable evidence.
            rows[side] = {**pairs[side], 'per_query': {}, 'cost': {
                'provider': budget.summary(), 'resource_class': 'cpu8-memory16384',
                'latency_method': measured['cost']['latency_method'],
                'price_basis': measured['cost']['price_basis']}}
        verdict = gates.evaluate({name: pairs}, {**policy, 'sets': {name: policy['sets'][name]}})
        if verdict['missing_or_incompatible_sets']:
            raise ValueError('incomplete private pairing')
        stats = scoring.paired(pairs['candidate']['per_query']['ndcg@10'], pairs['baseline']['per_query']['ndcg@10'])
        baseline_key = request['lease_key'] + '/' + request['owner'] + '/baseline'
        rows['candidate']['provenance'] = {'private_pair': {
            'baseline_result_key': results.record(rows['baseline'])['result_key'],
            'candidate_result_key': results.record(rows['candidate'])['result_key'],
            'candidate_payload_digest': search_trial.digest(rows['candidate']),
            'baseline_payload_digest': search_trial.digest(rows['baseline']),
            'baseline_lease_key': baseline_key,
            'statistics': {k: stats[k] for k in ('queries', 'delta', 'p_value')},
            'latency_comparable': verdict['gates']['latency']['details'][name]['comparable'] is True}}
        # Both canonical rows are aggregate-only. The candidate lease fences
        # the invocation; a separate baseline row preserves confirmation APIs.
        claim = store.claim(request['campaign'], baseline_key, policy['max_seconds'])
        if claim['status'] != 'claimed':
            raise RuntimeError('private baseline evidence unavailable')
        store.publish_many(request['campaign'], {baseline_key: (claim['owner'], rows['baseline']),
                                                 lease[0]: (lease[1], rows['candidate'])})
        return rows['candidate']
