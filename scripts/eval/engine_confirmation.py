"""Trusted full-engine confirmation contract; protected input never leaves the runner."""
import argparse
import asyncio
import copy
import hashlib
import json
import pathlib
import re
import decimal
import math
import os
import signal
import subprocess
import time

import ci_guard
import direct_bakeoff
import results
import search_trial
import gates
import modal_engine
import control_store
import embeddings

ROOT = pathlib.Path(__file__).resolve().parents[2]
VERSION = 1
HASH = re.compile(r'[a-f0-9]{64}')
IDENTIFIER = re.compile(r'[A-Za-z0-9_.-]{1,120}')
KEY = re.compile(r'[A-Za-z0-9/_.-]{1,240}')
HOSTED_EXECUTION = {'batch_size': (1, 32), 'max_batch_tokens': (8, 1048576),
                    'request_timeout_ms': (100, 10000), 'call_budget_ms': (100, 90000),
                    'max_concurrent_requests': (1, 32), 'max_retries': (0, 5)}
BINDINGS = ('version', 'campaign', 'trial', 'baseline_hash', 'candidate_hash',
            'git_sha', 'scorer_digest', 'engine_runner_git_sha',
            'engine_runner_scorer_digest', 'engine_source_hashes', 'policy_hash',
            'datasets', 'dev_evidence', 'heldout_family', 'heldout_family_digest', 'heldout_fingerprint',
            'mapping_policy', 'effective_baseline', 'effective_candidate',
            'baseline_request_limit', 'candidate_request_limit', 'settings_fingerprint')


class Unmappable(ValueError):
    """Stable refusal codes, without reflecting caller-controlled values."""


def digest(value):
    return hashlib.sha256(results.encode(value).encode()).hexdigest()


def mapping(config, baseline, production, *, candidate=False):
    """Map search knobs; production segmentation is explicit, never converted."""
    cfg, base = search_trial.configuration(config), search_trial.configuration(baseline)
    if any(cfg[k] != base[k] for k in ('window_chars', 'overlap_chars')):
        raise Unmappable('character_segmentation')
    if cfg['candidate_count'] > 100:
        raise Unmappable('candidate_depth')
    if cfg['reranker'] != 'none':
        raise Unmappable('reranker')
    if not isinstance(production, dict) or set(production) != {'ingestion', 'hybrid_fusion'}:
        raise Unmappable('production_settings')
    if production['hybrid_fusion'] not in ('relative_score', 'ranked'):
        raise Unmappable('fusion')
    ingestion = copy.deepcopy(production['ingestion'])
    if not isinstance(ingestion, dict):
        raise Unmappable('ingestion')
    kind = ingestion.get('kind')
    if cfg['model'] == direct_bakeoff.E5_MODEL:
        expected = digest(json.loads((ROOT / 'plugins/core-ingest/profile.json').read_text()))
        if ingestion != {'kind': 'core', 'profile_digest': expected}:
            raise Unmappable('pinned_core_ingestion')
    else:
        if cfg['model'] not in ('Cohere-Embed-V5-Pro', 'Cohere-Embed-V5-Fast'):
            raise Unmappable('model')
        if (kind != 'hosted' or set(ingestion) != {'kind', 'max_tokens_per_segment', 'overlap'} | set(HOSTED_EXECUTION)
                or type(ingestion['max_tokens_per_segment']) is not int
                or not 8 <= ingestion['max_tokens_per_segment'] <= 32768
                or type(ingestion['overlap']) is not int
                or not 0 <= ingestion['overlap'] < ingestion['max_tokens_per_segment'] - 8):
            raise Unmappable('hosted_ingestion')
        if (any(type(ingestion[k]) is not int or not low <= ingestion[k] <= high
                for k, (low, high) in HOSTED_EXECUTION.items())
                or ingestion['max_batch_tokens'] < ingestion['max_tokens_per_segment']):
            raise Unmappable('hosted_execution')
        if not re.fullmatch(r'[0-9A-Za-z][0-9A-Za-z._-]{0,31}', cfg['revision']):
            raise Unmappable('model_revision')
        ingestion.update(model=cfg['model'], model_revision=cfg['revision'],
                         dimensions=cfg['dimensions'], format='cohere', metric='cosine')
    weight = cfg['dense_weight']
    return {'ingestion': ingestion, 'retrieve': {'dense_weight': weight,
                'candidate_count': cfg['candidate_count'],
                'hybrid_fusion': 'ranked' if candidate else production['hybrid_fusion']},
            'mode': 'lexical' if weight == 0 else 'semantic' if weight == 1 else 'hybrid',
            'profile': 'default', 'request_limit': 10}


def lineage():
    """Pure engine lineage for trusted adapter configuration, independent of tier 1."""
    sources = ['scripts/eval/' + name for name in
        ('engine_confirmation.py', 'engine_confirm_runner.py', 'engine_measurement.py', 'engine_stack.py',
         'modal_engine.py', 'control_store.py', 'results.py', 'run.py', 'scoring.py', 'gates.py', 'embeddings.py', 'trec.py',
         'search_trial.py', 'direct_bakeoff.py', 'protected_inputs.py')]
    sources += ['scripts/' + name for name in
                ('local.py', 'hosted_embed_plugin.py', 'ingestion_plugin.py', 'connector_plugin.py',
                 'core_ingest_plugin.py', 'retrieval_plugin.py', 'ports.py', 'push_plugin.py', 'fixture_plugin.py',
                 'normalizer_plugin.py', 'subscription_plugin.py', 'prepare_tokenizer.py', 'prepare_embeddings.py')]
    sources += ['deploy/mlflow/eval-control.sql']
    sources += ['plugins/core-ingest/profile.json', 'plugins/core-retrieve/quivr-plugin.yaml',
                'plugins/hosted-embed/configuration.go', 'deploy/compose/compose.yaml',
                'third_party/e5/model-lock.json', 'third_party/tokenizer/requirements-linux-x86_64.txt',
                'third_party/tokenizer/requirements-macos-arm64.txt', 'go.mod', 'go.sum']
    # Bind the complete actual engine/SDK/plugin source, not just a manifest.
    for directory in ('client', 'contracts', 'migrations', 'internal', 'cmd', 'sdks/go', 'plugins/core-ingest',
                      'plugins/core-retrieve', 'plugins/hosted-embed'):
        sources += [str(p.relative_to(ROOT)) for p in (ROOT / directory).rglob('*.go')
                    if not p.name.endswith('_test.go')]
    for directory in ('sdks/go', 'plugins/core-ingest', 'plugins/core-retrieve', 'plugins/hosted-embed'):
        sources += [str(p.relative_to(ROOT)) for p in (ROOT / directory).rglob('*')
                    if p.is_file() and 'testdata' not in p.parts and 'fixtures' not in p.parts
                    and (p.suffix in ('.json', '.yaml', '.py') or p.name in ('go.mod', 'go.sum'))]
    # Include the schemas, migration SQL and all CLI scaffold fixtures embedded
    # by the actual engine binary, including non-code files under fixtures/.
    for directory in ('contracts', 'migrations', 'internal/plugins/scaffold/templates'):
        sources += [str(p.relative_to(ROOT)) for p in (ROOT / directory).rglob('*')
                    if p.is_file() and not p.name.endswith('_test.go')]
    sources += ['internal/adapters/tei/space.json',
                'internal/plugins/devhost/fakeplugin/scriptedsource/fixture.yaml']
    hashes = {name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest()
              for name in sorted(set(sources))}
    return {'engine_runner_scorer_digest': 'sha256:' + digest(hashes),
            'engine_source_hashes': hashes}


def settings_fingerprint(baseline, candidate):
    return digest({'baseline': baseline, 'candidate': candidate})


def family_sets(family):
    """Full safe family envelopes retain their metadata and named set descriptors."""
    return family['sets'] if 'sets' in family else family


def private(entry):
    return entry.get('private', entry.get('privacy') == 'private')


def validate(request, policy):
    """Only constructed, bounded fields may enter SQL or measurement transport."""
    fields = set(BINDINGS) | {'baseline_config', 'candidate_config', 'dev_evidence', 'heldout_family'}
    if not isinstance(request, dict) or set(request) != fields or request['version'] != VERSION:
        raise ValueError('invalid confirmation request fields')
    req = json.loads(results.encode(request))
    if (not IDENTIFIER.fullmatch(str(req['campaign'])) or type(req['trial']) is not int
            or req['trial'] < 0):
        raise ValueError('invalid campaign or trial identity')
    for field in ('git_sha', 'engine_runner_git_sha'):
        if not isinstance(req[field], str) or not re.fullmatch(r'[a-f0-9]{40}', req[field]):
            raise ValueError('invalid source revision')
    for field in ('scorer_digest', 'engine_runner_scorer_digest'):
        if not isinstance(req[field], str) or not re.fullmatch(r'sha256:[a-f0-9]{64}', req[field]):
            raise ValueError('invalid scorer revision')
    if any(req[k] != policy[k] for k in ('git_sha', 'scorer_digest')) or req['policy_hash'] != digest(policy):
        raise ValueError('exploration policy or revision mismatch')
    actual = lineage()
    if any(req[k] != actual[k] for k in actual):
        raise ValueError('trusted engine source mismatch')
    for side in ('baseline', 'candidate'):
        cfg = search_trial.configuration(req[side + '_config'])
        if cfg != req[side + '_config'] or req[side + '_hash'] != digest(cfg):
            raise ValueError('configuration hash mismatch')
    if req['baseline_config'] != policy['baseline']:
        raise ValueError('production baseline differs from exploration baseline')
    mapping_policy = req['mapping_policy']
    if not isinstance(mapping_policy, dict) or set(mapping_policy) != {'production', 'resources'}:
        raise ValueError('explicit production and resource mappings required')
    # Same daily budget and conservative compute rate; resource policy cannot
    # install a separate ledger or change the frozen serving prices/gates.
    resource = modal_engine.policy(mapping_policy['resources'])
    if (resource['modal_daily_usd'] != policy['modal_daily_usd']
            or resource['modal_usd_per_second'] != policy['modal_usd_per_second']
            or resource['experiment'] != policy['experiment'] or policy['profile'] != 'default'):
        raise Unmappable('resource_or_profile_policy')
    mapping_policy['resources'] = resource
    for side in ('baseline', 'candidate'):
        effective = mapping(req[side + '_config'], req['baseline_config'],
                            mapping_policy['production'], candidate=side == 'candidate')
        if req['effective_' + side] != effective or req[side + '_request_limit'] != 10:
            raise ValueError('effective engine settings mismatch')
    if req['settings_fingerprint'] != settings_fingerprint(req['effective_baseline'], req['effective_candidate']):
        raise ValueError('settings fingerprint mismatch')
    family = req['heldout_family']
    if not isinstance(family, dict):
        raise ValueError('invalid confirmation family')
    sets = family_sets(family)
    if 'sets' in family:
        if set(family) - {'sets', 'name', 'version', 'split', 'digest', 'fingerprint', 'private', 'privacy'}:
            raise ValueError('unknown family metadata')
        for key, value in family.items():
            if key == 'sets':
                continue
            if key in ('digest', 'fingerprint'):
                valid = isinstance(value, str) and HASH.fullmatch(value)
            elif key == 'private':
                valid = type(value) is bool
            elif key == 'privacy':
                valid = value in ('public', 'private')
            elif key == 'version' and type(value) is int:
                valid = value >= 1
            else:
                valid = isinstance(value, str) and IDENTIFIER.fullmatch(value)
            if not valid:
                raise ValueError('invalid family metadata')
    if (not isinstance(sets, dict) or set(sets) != set(policy['sets'])
            or set(req['datasets']) != set(sets) or set(req['dev_evidence']) != set(sets)):
        raise ValueError('incomplete confirmation family')
    for name, entry in sets.items():
        if (not IDENTIFIER.fullmatch(name) or not isinstance(entry, dict)
                or set(entry) - {'name', 'version', 'split', 'digest', 'fingerprint', 'private', 'privacy', 'diagnostic'}
                or not {'version', 'split', 'digest', 'fingerprint'} <= set(entry)
                or not ((type(entry['version']) is int and entry['version'] >= 1)
                        or isinstance(entry['version'], str) and IDENTIFIER.fullmatch(entry['version']))
                or entry['split'] != 'heldout' or not ('private' in entry or 'privacy' in entry)
                or ('private' in entry and type(entry['private']) is not bool)
                or ('privacy' in entry and entry['privacy'] not in ('public', 'private'))
                or ('private' in entry and 'privacy' in entry and entry['private'] != (entry['privacy'] == 'private'))
                or ('name' in entry and (not isinstance(entry['name'], str) or not IDENTIFIER.fullmatch(entry['name'])))
                or ('diagnostic' in entry and (type(entry['diagnostic']) is not bool
                                               or entry['diagnostic'] != policy['sets'][name]['diagnostic']))
                or not all(isinstance(entry[k], str) and HASH.fullmatch(entry[k]) for k in ('digest', 'fingerprint'))):
            raise ValueError('invalid held-out descriptor')
        dev = req['datasets'][name]
        if (not isinstance(dev, dict) or set(dev) != {'fingerprint', 'split_fingerprint'}
                or any(not isinstance(v, str) or not HASH.fullmatch(v) for v in dev.values())):
            raise ValueError('invalid frozen dev fingerprints')
        evidence = req['dev_evidence'][name]
        if (not isinstance(evidence, dict) or set(evidence) != {'baseline', 'candidate'}
                or any(not isinstance(v, str) or not KEY.fullmatch(v) for v in evidence.values())):
            raise ValueError('invalid canonical finalist keys')
    if req['heldout_family_digest'] != digest(family) or req['heldout_fingerprint'] != digest({n: e['fingerprint'] for n, e in sets.items()}):
        raise ValueError('held-out identity mismatch')
    if 'sets' in family and (family.get('split', 'heldout') != 'heldout'
            or family.get('fingerprint', req['heldout_fingerprint']) != req['heldout_fingerprint']
            or ('private' in family and family['private'] != any(private(e) for e in sets.values()))
            or ('privacy' in family and (family['privacy'] == 'private') != any(private(e) for e in sets.values()))):
        raise ValueError('family metadata disagrees with held-out sets')
    return req


def invariants(request, policy):
    """Immutable across finalists; actual candidate settings bind each attempt."""
    fields = ('version', 'campaign', 'baseline_hash', 'git_sha', 'scorer_digest',
              'engine_runner_git_sha', 'engine_runner_scorer_digest', 'engine_source_hashes',
              'policy_hash', 'datasets', 'heldout_family', 'heldout_family_digest',
              'heldout_fingerprint', 'mapping_policy', 'effective_baseline', 'baseline_request_limit')
    return {**{k: request[k] for k in fields}, 'gates': policy['gates'],
            'prices': policy['prices'], 'price_revision': policy['price_revision'],
            'profile': policy['profile']}


def finalist(store, req, policy, tracking):
    """Re-evaluate canonical dev pairing; require acknowledged aggregate tracking."""
    keys = [key for pair in req['dev_evidence'].values() for key in pair.values()]
    rows = store.evidence(req['campaign'], list(set(keys)))
    pairs = {}
    for name, keys in req['dev_evidence'].items():
        pairs[name] = {}
        for side, key in keys.items():
            row = rows.get(key)
            if (not row or any(row['config'].get(k) != v for k, v in req[side + '_config'].items())
                    or row['config'].get('campaign') != req['campaign']
                    or row['config'].get('campaign_policy_hash') != search_trial.digest(policy)
                    or row['config'].get('profile') != policy['profile']
                    or row['config'].get('fresh_latency') is not True or row['tier'] != 'direct'
                    or row['git_sha'] != req['git_sha'] or row['plugin_digest'] != req['scorer_digest']
                    or row['dataset']['name'] != name or row['dataset']['split'] != 'dev'
                    or row['dataset']['fingerprint'] != req['datasets'][name]['fingerprint']):
                raise ValueError('canonical finalist evidence mismatch')
            if tracking.get(results.result_key(results.record(row))).get('source') != 'mlflow':
                raise ValueError('finalist aggregate tracking is not synced')
            pairs[name][side] = row
    if gates.evaluate(pairs, policy)['status'] != 'exploration_finalist':
        raise ValueError('canonical dev evidence is not a finalist')


def aggregate(pairs, request, policy):
    """Construct the held-out projection; never copy gate samples or raw rows."""
    verdict = gates.evaluate(pairs, policy)
    if verdict['missing_or_incompatible_sets']:
        raise ValueError('incomplete paired engine evidence')
    output = {'status': 'confirmed' if verdict['status'] == 'exploration_finalist' else 'rejected',
              'confirmation_available': True, 'gates': {}, 'aggregate_sets': {},
              'heldout': {'passed': True, 'fingerprint': request['heldout_fingerprint']}}
    numeric = ('delta', 'adjusted_p', 'significance_level', 'min_gain', 'candidate_p95_ms',
               'baseline_p95_ms', 'ratio', 'max_ratio', 'cost_per_search_usd',
               'cost_per_1000_documents_usd', 'max_search_usd', 'max_index_usd_per_1000_documents')
    for name in ('quality', 'no_loss', 'latency', 'price'):
        source = verdict['gates'][name]
        details = {}
        for dataset in policy['sets']:
            values = source['details'][dataset]
            details[dataset] = {k: values[k] for k in numeric
                if k in values and (values[k] is None or type(values[k]) in (int, float))}
            if 'comparable' in values:
                details[dataset]['comparable'] = values['comparable'] is True
            if 'role' in values:
                details[dataset]['role'] = 'diagnostic' if policy['sets'][dataset]['diagnostic'] else 'gate'
        output['gates'][name] = {'passed': source['passed'] is True, 'details': details}
    for name in policy['sets']:
        stats = verdict['sets'][name]
        output['aggregate_sets'][name] = {'paired': {k: stats[k] for k in ('queries', 'delta', 'p_value', 'adjusted_p')}}
        for side in ('baseline', 'candidate'):
            metrics = pairs[name][side]['metrics']
            output['aggregate_sets'][name][side] = {
                alias: metrics[field] for field, alias in
                (('ndcg@10', 'ndcg_at_10'), ('latency_p95_ms', 'latency_p95_ms'),
                 ('cost_per_search_usd', 'cost_per_search_usd'),
                 ('cost_per_1000_documents_usd', 'cost_per_1000_documents_usd'))}
    results.encode(output)  # Reject nonfinite values before any transport.
    return output


def build_request(store, campaign, trial, candidate, heldout_family, datasets, dev_evidence,
                  mapping_policy, engine_runner_git_sha):
    """Adapter helper; all protected/engine metadata comes from operator config."""
    policy = store.policy(campaign)
    baseline, candidate = policy['baseline'], search_trial.configuration(candidate)
    mapping_policy = copy.deepcopy(mapping_policy)
    mapping_policy['resources'] = modal_engine.policy(mapping_policy['resources'])
    effective_baseline = mapping(baseline, baseline, mapping_policy['production'])
    effective_candidate = mapping(candidate, baseline, mapping_policy['production'], candidate=True)
    req = {'version': VERSION, 'campaign': campaign, 'trial': trial,
           'baseline_config': baseline, 'candidate_config': candidate,
           'baseline_hash': digest(baseline), 'candidate_hash': digest(candidate),
           'git_sha': policy['git_sha'], 'scorer_digest': policy['scorer_digest'],
           'engine_runner_git_sha': engine_runner_git_sha, **lineage(),
           'policy_hash': digest(policy), 'datasets': datasets, 'dev_evidence': dev_evidence,
           'heldout_family': heldout_family, 'heldout_family_digest': digest(heldout_family),
           'heldout_fingerprint': digest({n: e['fingerprint'] for n, e in family_sets(heldout_family).items()}),
           'mapping_policy': mapping_policy, 'effective_baseline': effective_baseline,
           'effective_candidate': effective_candidate, 'baseline_request_limit': 10, 'candidate_request_limit': 10,
           'settings_fingerprint': settings_fingerprint(effective_baseline, effective_candidate)}
    return validate(req, policy)


def project(measured, req, ordinal):
    """Second export fence at remote transport. Reflected extra fields are ignored."""
    if (measured.get('status') not in ('confirmed', 'rejected')
            or measured.get('confirmation_available') is not True
            or measured.get('remote_cleanup_verified') is not True
            or measured.get('sandbox_terminated') is not True
            or measured.get('read_ordinal') != ordinal
            or measured.get('heldout') != {'passed': True, 'fingerprint': req['heldout_fingerprint']}):
        raise ValueError('incomplete engine confirmation or cleanup')
    exported = {'gates': {}, 'aggregate_sets': {}}
    fields = {'quality': ('delta', 'adjusted_p', 'significance_level', 'min_gain'),
              'no_loss': ('delta', 'adjusted_p', 'significance_level'),
              'latency': ('candidate_p95_ms', 'baseline_p95_ms', 'ratio', 'max_ratio'),
              'price': ('cost_per_search_usd', 'cost_per_1000_documents_usd', 'max_search_usd', 'max_index_usd_per_1000_documents')}
    for name, allowed in fields.items():
        source = measured['gates'][name]
        if type(source['passed']) is not bool or set(source['details']) != set(family_sets(req['heldout_family'])):
            raise ValueError('invalid gate family')
        details = {}
        for dataset, values in source['details'].items():
            details[dataset] = {k: values[k] for k in allowed if k in values
                and (values[k] is None or type(values[k]) in (int, float))}
            if name == 'latency':
                details[dataset]['comparable'] = values.get('comparable') is True
            elif name in ('quality', 'no_loss'):
                if values.get('role') not in ('diagnostic', 'gate'):
                    raise ValueError('invalid aggregate set role')
                details[dataset]['role'] = values['role']
        exported['gates'][name] = {'passed': source['passed'], 'details': details}
    for name in family_sets(req['heldout_family']):
        pair = measured['aggregate_sets'][name]
        exported['aggregate_sets'][name] = {}
        for side in ('baseline', 'candidate'):
            exported['aggregate_sets'][name][side] = {}
            for field in ('ndcg_at_10', 'latency_p95_ms', 'cost_per_search_usd', 'cost_per_1000_documents_usd'):
                value = pair[side][field]
                if not gates.finite(value):
                    raise ValueError('unknown aggregate measurement')
                exported['aggregate_sets'][name][side][field] = value
        stats = pair['paired']
        if type(stats['queries']) is not int or stats['queries'] < 1:
            raise ValueError('invalid paired count')
        if (type(stats['delta']) not in (int, float) or not math.isfinite(stats['delta'])
                or any(v is not None and (not gates.finite(v) or v > 1)
                       for v in (stats['p_value'], stats['adjusted_p']))):
            raise ValueError('invalid paired aggregates')
        exported['aggregate_sets'][name]['paired'] = {k: stats[k] for k in ('queries', 'delta', 'p_value', 'adjusted_p')}
    passed = all(g['passed'] for g in exported['gates'].values())
    if passed != (measured['status'] == 'confirmed'):
        raise ValueError('inconsistent confirmation verdict')
    compute_ids = measured.get('compute_ids', {})
    if set(compute_ids) != {'app_id', 'sandbox_id'} or any(
            not isinstance(compute_ids[k], str) or not re.fullmatch(prefix + r'-[A-Za-z0-9_-]+', compute_ids[k])
            for k, prefix in (('app_id', 'ap'), ('sandbox_id', 'sb'))):
        raise ValueError('missing compute termination identity')
    output = {**exported, 'status': measured['status'], 'confirmation_available': True,
              'bindings': {k: req[k] for k in BINDINGS}, 'read_ordinal': ordinal,
              'heldout': {'passed': True, 'fingerprint': req['heldout_fingerprint']},
              'cleanup_verified': True, 'compute_ids': compute_ids}
    results.encode(output)
    return output


def records(output, req, policy, elapsed):
    """Only already-projected fields reach tracking, even for public held-out sets."""
    rows = []
    for name, pair in output['aggregate_sets'].items():
        entry = family_sets(req['heldout_family'])[name]
        for side in ('baseline', 'candidate'):
            rows.append({'schema_version': 1, 'experiment': policy['experiment'], 'tier': 'engine',
                'git_sha': req['engine_runner_git_sha'], 'plugin_digest': req['engine_runner_scorer_digest'],
                'machine': 'modal-engine-confirmation', 'duration_seconds': elapsed,
                'config': {'kind': 'confirmation', 'side': side, 'settings': req['effective_' + side],
                           'bindings': output['bindings'], 'confirmation_policy_digest': output['confirmation_policy_digest']},
                'dataset': {'name': name, **{k: entry[k] for k in ('version', 'split', 'fingerprint')}, 'private': private(entry)},
                'metrics': pair[side], 'per_query': {},
                'cost': {'modal_seconds_upper_bound': elapsed, 'compute_cap_notice': modal_engine.COMPUTE_NOTICE},
                'provenance': {'confirmation_available': True, 'heldout': output['heldout'],
                               'gates': output['gates'], 'paired': pair['paired'],
                               'confirmation_key': output['confirmation_key'], 'read_ordinal': output['read_ordinal'],
                               'cleanup_verified': True, 'compute_ids': output['compute_ids']}})
    return rows


def confirm(store, request, invoke, outbox):
    """One paired full-engine attempt; invoke opens data only inside its fence.

    invoke(remote_request, renew) is the trusted transport. Campaign adapters
    bind app_name/on_app/check there, never pass an imported success JSON.
    """
    key, owner, cfg, measured = None, None, None, {}
    try:
        policy = store.policy(request['campaign'])
        req = validate(request, policy)
        cfg = req['mapping_policy']['resources']
        binding = invariants(req, policy)
        store.register_confirmation(req['campaign'], binding)
        policy_digest = digest(binding)
        key = 'engine-confirmation/' + digest({k: req[k] for k in BINDINGS})
        ttl = modal_engine.lifetime(cfg) + 120
        claim = store.claim(req['campaign'], key, ttl)
        tracking = results.Results(directory=outbox)
        if claim['status'] == 'leased':
            return {'status': 'leased', 'confirmation_available': False, 'confirmation_key': key}
        if claim['status'] == 'done':
            canonical = claim['payload']
        else:
            owner = claim['owner']
            available = store.availability(req['campaign'])
            if available['stopped'] or available['paused'] or available['confirmation_reads_left'] <= 0:
                raise embeddings.BudgetExceeded('confirmation admission unavailable')
            finalist(store, req, policy, tracking)
            reservation = store.reserve(req['campaign'], 'modal', modal_engine.reservation(cfg),
                {'kind': 'engine-confirmation', 'confirmation_key': key}, (key, owner))
            def renew():
                available = store.availability(req['campaign'])
                if available['stopped'] or available['paused']:
                    raise control_store.LeaseLost('confirmation stopped or capped')
                store.renew(req['campaign'], key, owner, ttl)
            remote = {'campaign': req['campaign'], 'policy': cfg, 'confirmation_request': req,
                      'lease_key': key, 'owner': owner, 'outbox': str(outbox)}
            started = time.monotonic()
            measured = invoke(remote, renew)
            elapsed = time.monotonic() - started
            if measured.get('sandbox_terminated') is True:
                store.settle(reservation, decimal.Decimal(str(elapsed)) * control_store.money(cfg['modal_usd_per_second']),
                             {'sandbox_terminated': True})
            ordinal = store.confirmation_proof(req['campaign'], key, owner)['read_ordinal']
            output = {**project(measured, req, ordinal), 'confirmation_key': key,
                      'confirmation_policy_digest': policy_digest}
            canonical = {'output': output, 'records': records(output, req, policy, elapsed)}
            renew()
            store.publish_confirmation(req['campaign'], key, owner, canonical)
        receipts = []
        for row in canonical['records']:
            receipt = tracking.log(row)
            safe = {'result_key': results.result_key(receipt), 'status': receipt['status']}
            if safe['status'] not in ('pending', 'synced'):
                raise ValueError('unknown tracking receipt status')
            if safe['status'] == 'synced':
                if not isinstance(receipt.get('run_id'), str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,128}', receipt['run_id']):
                    raise ValueError('invalid aggregate tracking run identity')
                safe['run_id'] = receipt['run_id']
            receipts.append(safe)
        return {**canonical['output'], 'receipts': receipts}
    except (asyncio.CancelledError, KeyboardInterrupt, Exception) as error:
        import engine_stack
        if owner:
            try:
                store.abandon(request['campaign'], key, owner, 'failed')
            except (control_store.LeaseLost, control_store.Unavailable):
                pass
        status = 'unavailable' if isinstance(error, Unmappable) else 'capped' if isinstance(error, embeddings.BudgetExceeded) else 'failed'
        return {'status': status, 'confirmation_available': False,
                **engine_stack.failure(error),
                'reason': str(error) if isinstance(error, Unmappable) else 'confirmation dependency or lifecycle failed',
                **modal_engine.image_logs(measured.get('image_id')),
                **({'confirmation_key': key} if key else {})}


class ModalAdapter:
    """Trusted campaign seam: callable(request, resource), bounded Modal lifecycle.

    resource.app_name/on_app/check are the supervisor's persisted intent label,
    app registration and owner renewal. Standalone CLI uses the same seam with
    durable intent supplied by its operator; arbitrary result JSON is never read.
    """
    def __init__(self, store, outbox):
        self.store, self.outbox = store, pathlib.Path(outbox)

    def __call__(self, request, resource):
        if ci_guard.in_ci():
            raise PermissionError('paid confirmation is forbidden in CI')
        sha = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
        if (request['engine_runner_git_sha'] != sha
                or subprocess.run(['git', 'diff', '--quiet', 'HEAD'], cwd=ROOT).returncode):
            raise ValueError('engine runner must match committed trusted revision')
        def invoke(remote, renew):
            async def operation():
                async def fenced():
                    await asyncio.to_thread(resource.check)
                    await asyncio.to_thread(renew)
                return await asyncio.wait_for(modal_engine.run_modal(
                    {**remote, 'app_name': resource.app_name, 'on_app': resource.on_app}, fenced),
                    modal_engine.lifetime(remote['policy']))
            return asyncio.run(operation())
        return confirm(self.store, request, invoke, self.outbox)


class StandaloneResource:
    """Lazy durable intent; no resource claim for canonical replay/refusal."""
    def __init__(self, store, request):
        self.store, self.request, self.owner = store, request, None
        self.intent = 'engine-confirmation-' + digest({k: request[k] for k in BINDINGS})[:32]
        self.key = 'confirmation-resource/' + self.intent

    @property
    def app_name(self):
        campaign = self.request['campaign']
        if self.owner is None:
            claim = self.store.claim(campaign, self.key,
                modal_engine.lifetime(self.request['mapping_policy']['resources']) + 120)
            if claim['status'] != 'claimed':
                raise control_store.LeaseLost('confirmation resource is leased')
            self.owner = claim['owner']
            self.store.confirmation_intent(campaign, self.intent, self.owner)
        return self.intent

    def on_app(self, app_id):
        self.store.bind_confirmation_app(self.request['campaign'], self.intent, self.owner, app_id)

    def check(self):
        if self.owner:
            self.store.renew(self.request['campaign'], self.key, self.owner)
        if self.store.availability(self.request['campaign'])['stopped']:
            raise control_store.LeaseLost('campaign stopped')

    def close(self):
        if self.owner:
            self.store.abandon(self.request['campaign'], self.key, self.owner, 'failed')


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dry-run', action='store_true', help='keyless lineage and admission preview')
    parser.add_argument('--allow-paid', action='store_true', help='coordinator only; forbidden in CI')
    parser.add_argument('--campaign')
    parser.add_argument('--trial', type=int)
    parser.add_argument('--candidate', type=pathlib.Path, help='finalist search configuration')
    parser.add_argument('--configuration', type=pathlib.Path, help='trusted operator metadata, not result JSON')
    parser.add_argument('--outbox', type=pathlib.Path, default=ROOT / '.scratch/eval/confirmation')
    args = parser.parse_args(argv)
    if args.dry_run:
        print(results.encode({'version': VERSION, 'confirmation_available': False,
            'engine_runner_scorer_digest': lineage()['engine_runner_scorer_digest'],
            'heldout_read_limit': 10, 'compute_cap_notice': modal_engine.COMPUTE_NOTICE}))
        return 0
    if ci_guard.in_ci() or not args.allow_paid:
        parser.error('live confirmation requires --allow-paid and is forbidden in CI')
    if any(value is None for value in (args.campaign, args.trial, args.candidate, args.configuration)):
        parser.error('live confirmation needs campaign, trial, candidate and trusted configuration')
    try:
        store = control_store.Store(os.environ['EVAL_CONTROL_DATABASE_URL'])
        settings = json.loads(args.configuration.read_text())
        if set(settings) != {'heldout_family', 'datasets', 'dev_evidence', 'mapping_policy'}:
            raise ValueError('invalid trusted configuration fields')
        sha = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
        req = build_request(store, args.campaign, args.trial, json.loads(args.candidate.read_text()),
                            engine_runner_git_sha=sha, **settings)
        resource = StandaloneResource(store, req)
        previous = {sig: signal.getsignal(sig) for sig in (signal.SIGINT, signal.SIGTERM)}
        def cancel(*_):
            for sig in previous:
                signal.signal(sig, signal.SIG_IGN)
            raise KeyboardInterrupt()
        for sig in previous:
            signal.signal(sig, cancel)
        try:
            output = ModalAdapter(store, args.outbox)(req, resource)
        finally:
            for sig, handler in previous.items():
                signal.signal(sig, handler)
            resource.close()
    except (Exception, KeyboardInterrupt) as error:
        import engine_stack
        output = {'status': 'failed', 'confirmation_available': False, **engine_stack.failure(error)}
    print(results.encode(output))
    return 0 if output['status'] in ('confirmed', 'rejected') else 2


if __name__ == '__main__':
    raise SystemExit(main())
