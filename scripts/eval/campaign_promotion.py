"""Campaign-side trusted confirmation hook and configuration-only promotion.

Unconfigured integration remains disabled. search_campaign.NativeConfirmation
implements the native request/result translation described below.
A trusted translator must build an engine_confirmation request explicitly, using
build_request and the actual frozen TREC/split fingerprints and per-set canonical
baseline/candidate SQL lease keys. Campaign result_key lists are not native dev
admission keys. Freeze production ingestion/resources and request_limit=10;
candidate_count is retrieval depth, not the final request limit. Bind the native
lineage scorer/source hashes separately to its engine runner Git revision.

The translator must validate exact native BINDINGS and top-level
confirmation_policy_digest before returning the normalized campaign receipt:
original full campaign-request bindings and a sorted registered app-ID list.
Native compute_ids is an app_id/sandbox_id mapping; both require verified cleanup.
The callable supplies heldout_fingerprint, safe heldout_family, engine_git_sha,
engine_scorer_digest and confirmation_policy_digest; optional fusion is ranked.
It uses resource.app_name/on_app/check for invoke; the native runner alone owns
fenced protected-input admission and held-out reads. Missing metadata fails
before intent. No local success JSON grants trust and raw/private gate details
are not stored.
"""
import ast
import contextlib
import control_store
import copy
import hashlib
import json
import os
import pathlib
import re
import runpy
import subprocess
import tempfile
import urllib.parse
import uuid

import search_trial

ROOT = pathlib.Path(__file__).resolve().parents[2]
DISABLED = 'Full-engine confirmation adapter unavailable; modal_engine is smoke only.'

TARGET = 'deploy/railway/core-entrypoint.py'
GATES = ('quality', 'no_loss', 'latency', 'price')
PRICES = {'Cohere-Embed-V5-Pro': .12, 'Cohere-Embed-V5-Fast': .08}
PRICE_REVISION = '2026-10-03'
HEX = re.compile(r'[a-f0-9]{64}')


class Refused(ValueError):
    """A fixed public reason, never a reflected dependency payload."""


RUNTIME = {'AZURE_FOUNDRY_ENDPOINT': 'https://runtime.invalid', 'AZURE_FOUNDRY_KEY': 'runtime-reference'}


def effective_settings(checkout):
    """Evaluate the actual trusted checkout, using neutral runtime references."""
    module = runpy.run_path(str(pathlib.Path(checkout) / TARGET))
    hosted = module['hosted_configuration'](RUNTIME)
    retrieve = [c for c in module['CONNECTORS'] if c['id'] == 'core-retrieve']
    if len(retrieve) != 1 or callable(retrieve[0].get('configuration')):
        raise Refused('retrieval target must have one literal configuration')
    return {'hosted': hosted, 'retrieve': {
        'dense_weight': .5, 'candidate_count': 'request_limit', 'hybrid_fusion': 'relative_score',
        **retrieve[0].get('configuration', {})}}


def _candidate_settings(checkout, candidate, baseline, fusion):
    candidate, baseline = search_trial.configuration(candidate), search_trial.configuration(baseline)
    if candidate['model'] not in PRICES:
        raise Refused('only Pro and Fast hosted models support promotion')
    if candidate['candidate_count'] > 100:
        raise Refused('engine candidate depth is capped at 100')
    for key in ('window_chars', 'overlap_chars', 'reranker', 'revision'):
        if candidate[key] != baseline[key]:
            raise Refused('changed ' + key + ' has no engine configuration mapping')
    if baseline['reranker'] != 'none':
        raise Refused('reranker confirmation has no hosted configuration mapping')
    if fusion not in ('ranked', 'relative_score'):
        raise Refused('unsupported engine fusion')
    effective = effective_settings(checkout)
    effective['hosted'].update(model=candidate['model'], dimensions=candidate['dimensions'],
                               usd_per_million_tokens=PRICES[candidate['model']])
    effective['retrieve'].update(dense_weight=candidate['dense_weight'],
                                candidate_count=candidate['candidate_count'], hybrid_fusion=fusion)
    import yaml
    try:
        manifest = yaml.safe_load((pathlib.Path(checkout) / 'plugins/core-retrieve/quivr-plugin.yaml').read_text())
        properties = manifest['configuration']['schema']['properties']
        if (set(effective['retrieve']) - set(properties)
                or properties['dense_weight']['type'] != 'number'
                or not properties['dense_weight']['minimum'] <= candidate['dense_weight'] <= properties['dense_weight']['maximum']
                or properties['candidate_count']['type'] != 'integer'
                or not properties['candidate_count']['minimum'] <= candidate['candidate_count'] <= properties['candidate_count']['maximum']
                or fusion not in properties['hybrid_fusion']['enum']
                or candidate['candidate_count'] > manifest['contributions']['retrieval']['limits']['max_candidates']):
            raise ValueError()
    except Exception:
        raise Refused('engine retrieval configuration mapping is unavailable in the evidence-bound checkout') from None
    return effective


def _literal_edits(source, effective):
    tree = ast.parse(source)
    functions = [node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name == 'hosted_configuration']
    if len(functions) != 1:
        raise Refused('hosted configuration target is ambiguous')
    returns = [node.value for node in ast.walk(functions[0]) if isinstance(node, ast.Return)]
    if len(returns) != 1 or not isinstance(returns[0], ast.Dict):
        raise Refused('hosted configuration must return a literal dictionary')
    edits = []
    def offset(node, end=False):
        line = node.end_lineno if end else node.lineno
        column = node.end_col_offset if end else node.col_offset
        # AST columns are UTF-8 byte offsets.
        return sum(len(s.encode()) for s in source.splitlines(keepends=True)[:line-1]) + column
    for key in ('model', 'dimensions', 'usd_per_million_tokens'):
        matches = [v for k, v in zip(returns[0].keys, returns[0].values)
                   if isinstance(k, ast.Constant) and k.value == key]
        if len(matches) != 1 or not isinstance(matches[0], ast.Constant):
            raise Refused('hosted field must be one literal: ' + key)
        edits.append((offset(matches[0]), offset(matches[0], True), repr(effective['hosted'][key])))
    assignments = [node for node in tree.body if isinstance(node, ast.Assign)
                   and any(isinstance(t, ast.Name) and t.id == 'CONNECTORS' for t in node.targets)]
    if len(assignments) != 1 or not isinstance(assignments[0].value, ast.List):
        raise Refused('connector target must be a literal list')
    matches = [d for d in assignments[0].value.elts if isinstance(d, ast.Dict)
               and any(isinstance(k, ast.Constant) and k.value == 'id' and isinstance(v, ast.Constant)
                       and v.value == 'core-retrieve' for k, v in zip(d.keys, d.values))]
    if len(matches) != 1:
        raise Refused('core-retrieve connector target is ambiguous')
    connector = matches[0]
    existing = [v for k, v in zip(connector.keys, connector.values)
                if isinstance(k, ast.Constant) and k.value == 'configuration']
    if len(existing) > 1 or (existing and not isinstance(existing[0], ast.Dict)):
        raise Refused('retrieval configuration target is not literal')
    if existing:
        current = ast.literal_eval(existing[0])
        if set(current) - {'dense_weight', 'candidate_count', 'hybrid_fusion'}:
            raise Refused('unsupported retrieval settings must remain unchanged')
        edits.append((offset(existing[0]), offset(existing[0], True), repr(effective['retrieve'])))
    else:
        end = offset(connector, True) - 1
        edits.append((end, end, ', \'configuration\': ' + repr(effective['retrieve'])))
    encoded = source.encode()
    for start, end, value in sorted(edits, reverse=True):
        encoded = encoded[:start] + value.encode() + encoded[end:]
    return encoded.decode()


class HostedManifestBuilder:
    """Run the checkout's real offline configure command, without provider calls."""
    def __init__(self, binary=None):
        self.binary = binary

    def __call__(self, checkout, configuration):
        with tempfile.TemporaryDirectory(prefix='quivr-manifest-') as directory:
            directory = pathlib.Path(directory)
            binary = self.binary
            if binary is None:
                binary = directory / 'hosted-embed'
                _run(['go', 'build', '-o', str(binary), '.'], pathlib.Path(checkout) / 'plugins/hosted-embed')
            path = directory / 'configuration.json'
            path.write_text(json.dumps(configuration, allow_nan=False))
            return json.loads(_run([str(binary), 'configure', str(path)], checkout))


def _check_manifest(manifest, effective):
    try:
        hosted = effective['hosted']
        properties = manifest['configuration']['schema']['properties']
        spaces = list(manifest['contributions']['ingestion']['spaces'].values())
        if (len(spaces) != 1 or spaces[0]['model'] != hosted['model']
                or spaces[0]['dimensions'] != hosted['dimensions']
                or spaces[0]['input_price']['usd_per_million_tokens'] != hosted['usd_per_million_tokens']
                or any(properties[k]['const'] != value for k, value in hosted.items())):
            raise Refused()
    except (KeyError, TypeError, ValueError):
        raise Refused('generated hosted manifest does not match confirmed settings') from None


def promotion_apply(checkout, candidate, baseline, *, fusion='ranked', builder=None):
    """Apply only the literal hosted/retrieve settings and verify offline output."""
    checkout = pathlib.Path(checkout)
    expected = _candidate_settings(checkout, candidate, baseline, fusion)
    path = checkout / TARGET
    path.write_text(_literal_edits(path.read_text(), expected))
    effective = effective_settings(checkout)
    if effective != expected:
        raise Refused('effective hosted settings do not match confirmed candidate')
    _check_manifest((builder or HostedManifestBuilder())(checkout, effective['hosted']), effective)
    return effective


def _run(argv, cwd):
    try:
        return subprocess.run(argv, cwd=cwd, capture_output=True, text=True, check=True, timeout=180).stdout
    except Exception:
        # Never reflect provider endpoints, credentials or private subprocess output.
        raise Refused('promotion command unavailable or failed') from None


@contextlib.contextmanager
def _checkout(repository, revision):
    if not isinstance(revision, str) or not re.fullmatch(r'[a-f0-9]{40,64}', revision):
        raise Refused('confirmation requires an immutable Git revision')
    with tempfile.TemporaryDirectory(prefix='quivr-promotion-') as directory:
        path = pathlib.Path(directory) / 'checkout'
        _run(['git', 'clone', '--quiet', '--no-hardlinks', '--no-checkout', str(pathlib.Path(repository).resolve()), str(path)], repository)
        _run(['git', 'checkout', '--quiet', '--detach', revision], path)
        yield path


@contextlib.contextmanager
def _checkouts(repository):
    """Share one read-only checkout per revision across one confirmation."""
    with contextlib.ExitStack() as stack:
        paths = {}
        def checkout(revision):
            if not isinstance(revision, str) or revision not in paths:
                paths[revision] = stack.enter_context(_checkout(repository, revision))
            return paths[revision]
        yield checkout


def _fingerprints(checkout):
    paths = _run(['git', 'ls-files', '-z'], checkout).split('\0')
    def relevant(path):
        return (path == TARGET or path.startswith('scripts/eval/engine_')
                or path.startswith(('plugins/', 'third_party/', 'deploy/'))
                or path.startswith(('internal/', 'pkg/', 'schema/', 'migrations/', 'cmd/', 'contracts/', 'sdks/', 'client/'))
                or path in ('go.mod', 'go.sum', '.dockerignore', 'Makefile', 'scripts/prepare_tokenizer.py',
                            'scripts/run.py', 'scripts/local.py')
                or (path.startswith('scripts/') and path.endswith('.py')
                    and not pathlib.PurePosixPath(path).name.startswith('test_'))
                or (path.startswith('scripts/eval/requirements') and path.endswith('.txt'))
                or path in ('scripts/eval/' + name for name in
                    ('scoring.py', 'gates.py', 'search_trial.py', 'embeddings.py', 'direct_bakeoff.py', 'public_sets.json', 'engine_stack.py', 'modal_engine.py',
                     'modal_search.py', 'control_store.py', 'campaign_store.py', 'engine_confirmation.py')))
    return {path: hashlib.sha256((pathlib.Path(checkout) / path).read_bytes()).hexdigest()
            for path in sorted(paths) if path and relevant(path) and not path.endswith('_test.go')}



def _validate_family(family, expected):
    """Frozen aggregate descriptors allow metadata only, never private sources."""
    fields = {'version', 'name', 'split', 'fingerprint', 'digest', 'privacy', 'private', 'sets', 'diagnostic'}
    identifiers = re.compile(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,127}')
    def descriptor(value, nested=False):
        if not isinstance(value, dict) or not value or set(value) - (fields - {'sets'} if nested else fields):
            raise Refused('trusted frozen held-out family descriptor is required')
        for key, item in value.items():
            if key == 'sets':
                if isinstance(item, dict) and item:
                    for name, entry in item.items():
                        if not isinstance(name, str) or not identifiers.fullmatch(name):
                            raise Refused('held-out set identities must be opaque')
                        descriptor(entry, True)
                elif isinstance(item, list) and item:
                    for entry in item:
                        descriptor(entry, True)
                else:
                    raise Refused('frozen held-out family requires aggregate set descriptors')
            elif key in ('fingerprint', 'digest'):
                if not isinstance(item, str) or not re.fullmatch(r'(sha256:)?[a-f0-9]{64}', item):
                    raise Refused('held-out fingerprints must be immutable content digests')
            elif key in ('private', 'diagnostic'):
                if type(item) is not bool:
                    raise Refused('held-out privacy must be explicit')
            elif key == 'privacy':
                if type(item) is not bool and item not in ('public', 'private'):
                    raise Refused('held-out privacy must be explicit')
            elif key == 'version' and type(item) is int and item >= 1:
                continue
            elif not isinstance(item, str) or not identifiers.fullmatch(item):
                raise Refused('held-out metadata must use opaque identities')
    descriptor(family)
    fingerprint = family.get('fingerprint', family.get('digest', ''))
    if fingerprint.removeprefix('sha256:') != expected:
        raise Refused('held-out family fingerprint mismatch')

def _request(state, number, checkout, heldout, fusion='ranked', *,
             engine_git_sha=None, engine_scorer_digest=None, heldout_family=None, engine_checkout=None,
             confirmation_policy_digest=None):
    if not isinstance(heldout, str) or not HEX.fullmatch(heldout):
        raise Refused('trusted adapter must provide a held-out fingerprint')
    if not isinstance(engine_git_sha, str) or not re.fullmatch(r'[a-f0-9]{40,64}', engine_git_sha):
        raise Refused('trusted engine runner Git revision is required')
    if not isinstance(engine_scorer_digest, str) or not re.fullmatch(r'(sha256:)?[a-f0-9]{64}', engine_scorer_digest):
        raise Refused('trusted engine runner scorer fingerprint is required')
    _validate_family(heldout_family, heldout)
    engine_checkout = engine_checkout or checkout
    if not isinstance(confirmation_policy_digest, str) or not HEX.fullmatch(confirmation_policy_digest):
        raise Refused('trusted invariant confirmation policy digest is required')
    trial = state['trials'].get(str(number))
    if not trial or trial.get('report', {}).get('status') != 'exploration_finalist':
        raise Refused('only completed exploration finalists can be confirmed')
    if any(trial['report'].get('gates', {}).get(k, {}).get('passed') is not True for k in GATES):
        raise Refused('exploration gates must all pass before confirmation')
    dev = trial['report'].get('evidence', [])
    if (not isinstance(dev, list) or not dev
            or any(not isinstance(item, dict) or item.get('status') != 'synced'
                   or not isinstance(item.get('result_key'), str) or not HEX.fullmatch(item['result_key']) for item in dev)):
        raise Refused('synced canonical dev finalist evidence is required')
    spec = state['spec']
    if spec['policy']['price_revision'] != PRICE_REVISION:
        raise Refused('dated model pricing requires a matching configured revision')
    scorer = 'sha256:' + search_trial.digest({name: (checkout / 'scripts/eval' / name).read_text()
        for name in ('scoring.py', 'gates.py', 'search_trial.py', 'embeddings.py', 'direct_bakeoff.py')})
    if scorer != state['scorer_digest']:
        raise Refused('scorer fingerprint does not match frozen campaign')
    registry = json.loads((checkout / 'scripts/eval/public_sets.json').read_text())
    baseline, candidate = spec['policy']['baseline'], trial['config']
    datasets = {name: {'dataset': search_trial.digest(registry[name]),
                      'split': search_trial.digest({'dataset': registry[name], 'split': policy['split']})}
                for name, policy in spec['policy']['sets'].items()}
    effective_baseline = effective_settings(engine_checkout)
    if (effective_baseline['hosted']['model'] != baseline['model']
            or effective_baseline['hosted']['dimensions'] != baseline['dimensions']
            or effective_baseline['retrieve']['dense_weight'] != baseline['dense_weight']):
        raise Refused('engine baseline does not match frozen campaign baseline')
    effective_candidate = _candidate_settings(engine_checkout, candidate, baseline, fusion)
    files = _fingerprints(checkout)
    return {'version': 1, 'campaign': spec['name'], 'trial': int(number),
            'baseline_hash': search_trial.digest(baseline), 'candidate_hash': search_trial.digest(candidate),
            'baseline_config': search_trial.configuration(baseline),
            'candidate_config': search_trial.configuration(candidate),
            'confirmation_limit': spec['policy'].get('confirmation_limit', 10),
            'git_sha': state['git_sha'], 'scorer_digest': scorer,
            'policy_hash': search_trial.digest(spec['policy']), 'datasets': datasets,
            'heldout_fingerprint': heldout, 'heldout_family': copy.deepcopy(heldout_family),
            'heldout_family_digest': search_trial.digest(heldout_family),
            'dev_evidence': sorted({item['result_key'] for item in dev}),
            'engine_runner_git_sha': engine_git_sha, 'engine_runner_scorer_digest': engine_scorer_digest,
            'engine_files': files if engine_checkout == checkout else _fingerprints(engine_checkout),
            'confirmation_policy_digest': confirmation_policy_digest,
            'mapping_policy': {'ingestion': 'effective_production_configuration',
                               'character_windows': 'no_conversion', 'reranker': 'none',
                               'fusion': fusion, 'maximum_candidate_count': 100},
            'files': files,
            'effective_baseline': effective_baseline, 'effective_candidate': effective_candidate,
            'baseline_request_limit': baseline['candidate_count'], 'candidate_request_limit': candidate['candidate_count'],
            'settings_fingerprint': search_trial.digest([effective_baseline, effective_candidate,
                                                       baseline['candidate_count'], candidate['candidate_count']])}


class ConfirmationResource:
    """Persist intent lazily, at the runner's actual compute-creation boundary.

    Cache replay, unavailable integration and capped admission do not create an
    intent. Reading app_name/label before creation persists recoverable identity;
    lost creation acknowledgements remain open for the normal watchdog.
    """
    def __init__(self, store, name, owner):
        self.store, self.name, self.owner = store, name, owner
        self.resource = None

    def _intent(self):
        if self.resource is None:
            self.resource = self.store.intent(self.name, self.owner)
        return self.resource

    @property
    def label(self):
        return self._intent()['label']

    @property
    def app_name(self):
        return self.label

    def bind(self, app_id):
        self.store.bind(self.name, self.owner, self._intent()['id'], app_id)

    def on_app(self, app_id):
        self.bind(app_id)

    def check(self):
        self.store.renew_owner(self.name, self.owner)


def _validate_receipt(result, request, registered, reads=None):
    fields = {'status', 'confirmation_available', 'bindings', 'gates', 'heldout',
              'aggregate_sets', 'receipts', 'compute_ids', 'confirmation_key',
              'read_ordinal', 'cleanup_verified'}
    if not isinstance(result, dict) or set(result) != fields:
        raise Refused('confirmation accepts the trusted aggregate engine result only')
    if (result['status'] != 'confirmed' or result['confirmation_available'] is not True
            or search_trial.digest(result['bindings']) != search_trial.digest(request)):
        raise Refused('full-engine confirmation binding mismatch')
    gates = result['gates']
    if (not isinstance(gates, dict) or set(gates) != set(GATES)
            or any(not isinstance(gates[k], dict) or gates[k].get('passed') is not True for k in GATES)
            or result['heldout'] != {'passed': True, 'fingerprint': request['heldout_fingerprint']}
            or result['heldout']['passed'] is not True):
        raise Refused('all four gates and held-out confirmation must pass')
    if (result['cleanup_verified'] is not True
            or not isinstance(result['confirmation_key'], str)
            or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_./:-]{0,255}', result['confirmation_key'])
            or '//' in result['confirmation_key']
            or type(result['read_ordinal']) is not int
            or not 1 <= result['read_ordinal'] <= request['confirmation_limit']
            or reads is None or result['read_ordinal'] > reads):
        raise Refused('canonical read admission and verified full-engine cleanup are required')
    evidence = result['receipts']
    if not isinstance(evidence, list) or not evidence or len(evidence) > 100:
        raise Refused('synced aggregate result evidence is required')
    for item in evidence:
        if (not isinstance(item, dict) or set(item) != {'result_key', 'run_id', 'status'}
                or item['status'] != 'synced' or not isinstance(item['result_key'], str)
                or not HEX.fullmatch(item['result_key']) or not isinstance(item['run_id'], str)
                or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,127}', item['run_id'])):
            raise Refused('synced aggregate result evidence is required')
    if (not isinstance(result['compute_ids'], list) or not registered
            or result['compute_ids'] != sorted(registered)):
        raise Refused('confirmation compute IDs must match registered resources')
    # Runner gate details may contain statistical summaries that this supervisor
    # does not own. Export passed flags and allowed aggregate numbers only.
    import math
    aggregate_sets = result['aggregate_sets']
    if not isinstance(aggregate_sets, dict):
        raise Refused('confirmation aggregates must be a mapping')
    clean_sets = {}
    for name, pair in aggregate_sets.items():
        if not isinstance(name, str) or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,127}', name) or not isinstance(pair, dict):
            raise Refused('confirmation set metadata must be aggregate only')
        clean_sets[name] = {}
        for side in ('baseline', 'candidate'):
            source = pair.get(side, {})
            if not isinstance(source, dict):
                raise Refused('confirmation metrics must be aggregate only')
            clean_sets[name][side] = {key: source[key] for key in (
                'ndcg_at_10', 'latency_p95_ms', 'cost_per_search_usd', 'cost_per_1000_documents_usd')
                if type(source.get(key)) in (int, float) and math.isfinite(source[key]) and source[key] >= 0}
    return copy.deepcopy({**result, 'gates': {k: {'passed': True} for k in GATES},
                          'aggregate_sets': clean_sets})


def confirmation_status(store, name, trial):
    state = store.snapshot(name)
    result = state.get('confirmations', {}).get(str(trial))
    return ({'status': result['status'], 'reason': result['reason']} if result else
            {'status': 'pending_confirmation', 'reason': DISABLED})


def confirm(store, name, trial, owner, *, adapter=None, repository=ROOT, compute=None):
    if adapter is None:
        return {'status': 'pending_confirmation', 'reason': DISABLED}
    if not callable(adapter):
        return {'status': 'pending_confirmation', 'reason': 'A trusted callable confirmation adapter is required.'}
    state = store.snapshot(name)
    existing = state.get('confirmations', {}).get(str(trial), {})
    if existing.get('status') == 'confirmed':
        return {'status': 'confirmed', 'reason': 'Trusted full-engine and held-out confirmation complete.'}
    handle = None
    receipt = None
    report = None
    outcome = {'status': 'pending_confirmation', 'reason': 'Trusted full-engine confirmation failed or evidence is incomplete.'}
    try:
        # Building the request only reads; promotion edits its own checkout later.
        with _checkouts(repository) as checkout:
            if hasattr(adapter, 'prepare'):
                adapter.prepare(trial, checkout)
            engine_checkout = checkout(getattr(adapter, 'engine_git_sha', None))
            request = _request(state, trial, checkout(state['git_sha']), getattr(adapter, 'heldout_fingerprint', None),
                               getattr(adapter, 'fusion', 'ranked'),
                               engine_git_sha=getattr(adapter, 'engine_git_sha', None),
                               engine_scorer_digest=getattr(adapter, 'engine_scorer_digest', None),
                               heldout_family=getattr(adapter, 'heldout_family', None), engine_checkout=engine_checkout,
                               confirmation_policy_digest=getattr(adapter, 'confirmation_policy_digest', None))
        _freeze_confirmation(store, name, owner, request)
        store.renew_owner(name, owner)
        handle = ConfirmationResource(store, name, owner)
        try:
            result = adapter(copy.deepcopy(request), handle)
        except Exception:
            raise RuntimeError('trusted confirmation adapter failed') from None
        store.renew_owner(name, owner)
        if isinstance(result, dict) and result.get('status') in ('rejected', 'unavailable', 'leased', 'capped', 'failed'):
            if result['status'] == 'rejected' and isinstance(result.get('aggregate_sets'), dict):
                from search_campaign import aggregate
                report = aggregate({**result, 'work': {str(i): {'receipt': r} for i, r in enumerate(result.get('receipts', []))}},
                                   state['spec']['policy']['sets'])
            outcome = {'status': result['status'],
                       'reason': 'Trusted engine confirmation is ' + result['status'] + '; promotion remains disabled.'}
            raise Refused(outcome['reason'])
        resources = store.snapshot(name)['resources']
        requested_ids = result.get('compute_ids', []) if isinstance(result, dict) else []
        current_id = handle.resource['id'] if handle.resource else None
        registered = sorted({r['app_id'] for identity, r in resources.items()
                             if r['app_id'] in requested_ids and
                             (r['status'] == 'closed' or identity == current_id)})
        available = store.availability(name)
        reads = request['confirmation_limit'] - available['confirmation_reads_left']
        receipt = _validate_receipt(result, request, registered, reads)
        outcome = {'status': 'confirmed', 'reason': 'Trusted full-engine and held-out confirmation complete.'}
    except Refused as error:
        outcome['reason'] = str(error)
    except Exception as error:
        # Arbitrary adapter exceptions can contain raw private payload or secrets.
        import engine_confirmation
        if isinstance(error, engine_confirmation.Unmappable):
            outcome = {'status': 'unavailable', 'reason': 'Candidate settings have no full-engine mapping; promotion remains disabled.'}
    finally:
        if handle and handle.resource:
            try:
                resource = handle.resource
                current = store.snapshot(name)['resources'][resource['id']]
                if not current['app_id']:
                    raise Refused('confirmation launch acknowledgement missing')
                if compute is None:
                    from campaign_compute import ModalCompute
                    compute = ModalCompute()
                compute.stop(current['app_id'])
                if compute.running(current['app_id']):
                    raise Refused('confirmation compute termination is pending')
                store.closed(name, resource['id'], owner=owner)
            except Exception:
                receipt = None
                report = None
                outcome = {'status': 'pending_confirmation', 'reason': 'Confirmation compute cleanup pending; retry watchdog or stop.'}
    # A stopped/replaced owner cannot publish a receipt after external work.
    _finish_confirmation(store, name, owner, trial, outcome, receipt, report)
    return outcome



@control_store.retry_contention
def _freeze_confirmation(store, name, owner, request):
    with store.mutation(name, owner) as (_, current, __):
        protocol = {k: request[k] for k in ('confirmation_policy_digest',
            'engine_runner_git_sha', 'engine_runner_scorer_digest', 'heldout_family_digest')}
        if current.setdefault('confirmation_protocol', protocol) != protocol:
            raise Refused('confirmation runner policy and held-out family are frozen for this campaign')


@control_store.retry_contention
def _finish_confirmation(store, name, owner, trial, outcome, receipt, report):
    with store.mutation(name, owner) as (_, current, __):
        record = {**outcome}
        if receipt:
            record['receipt'] = receipt
        if report:
            record['report'] = report
        current.setdefault('confirmations', {})[str(trial)] = record


@control_store.retry_contention
def _remember_confirmation(store, name, number, result):
    with store.edit(name) as (_, current):
        current.setdefault('confirmations', {}).setdefault(number, result)


def _main_revision(repository):
    for ref in ('origin/main', 'main'):
        try:
            return _run(['git', 'rev-parse', '--verify', ref], repository).strip()
        except ValueError:
            continue
    raise Refused('current main revision is unavailable')


class GitHub:
    """Git/gh transport: argv only, inherited credentials, no merge operation."""
    def __init__(self, repository=ROOT):
        self.repository = pathlib.Path(repository)

    def current_main(self, checkout):
        remote = _run(['git', 'remote', 'get-url', 'origin'], self.repository).strip()
        _run(['git', 'remote', 'set-url', 'origin', remote], checkout)
        _run(['git', 'fetch', '--quiet', 'origin', 'main'], checkout)
        return _run(['git', 'rev-parse', 'FETCH_HEAD'], checkout).strip()

    def find(self, branch):
        rows = json.loads(_run(['gh', 'pr', 'list', '--state', 'all', '--head', branch,
                               '--json', 'url,body,headRefName'], self.repository))
        if len(rows) > 1 or (rows and rows[0]['headRefName'] != branch):
            raise Refused('promotion branch has conflicting pull requests')
        return rows[0] if rows else None

    def verify(self, checkout, branch):
        _run(['git', 'fetch', '--quiet', 'origin', 'refs/heads/' + branch], checkout)
        if _run(['git', 'diff', 'FETCH_HEAD', '--', TARGET], checkout).strip():
            raise Refused('existing promotion branch does not match confirmed settings')
        ancestor = _run(['git', 'merge-base', 'HEAD', 'FETCH_HEAD'], checkout).strip()
        changed = _run(['git', 'diff', '--name-only', ancestor, 'FETCH_HEAD'], checkout).splitlines()
        if set(changed) - {TARGET}:
            raise Refused('existing promotion branch includes unapproved changes')

    def create(self, checkout, branch, title, body):
        try:
            subprocess.run(['python3', 'scripts/denylist.py', '--stdin'], cwd=self.repository,
                           input=title + '\n' + body, text=True, capture_output=True,
                           check=True, timeout=30)
        except Exception:
            raise Refused('Promotion title and evidence text must pass the repository denylist.') from None
        remote = _run(['git', 'remote', 'get-url', 'origin'], self.repository).strip()
        _run(['git', 'remote', 'set-url', 'origin', remote], checkout)
        # Recover push-before-PR crashes without rewriting the remote branch.
        found = _run(['git', 'ls-remote', '--heads', 'origin', 'refs/heads/' + branch], checkout).strip()
        if found:
            _run(['git', 'fetch', '--quiet', 'origin', 'refs/heads/' + branch], checkout)
            if _run(['git', 'diff', 'FETCH_HEAD', '--', TARGET], checkout).strip():
                raise Refused('existing promotion branch does not match confirmed settings')
            # The branch may contain only the allowlisted configuration change.
            ancestor = _run(['git', 'merge-base', 'HEAD', 'FETCH_HEAD'], checkout).strip()
            changed = _run(['git', 'diff', '--name-only', ancestor, 'FETCH_HEAD'], checkout).splitlines()
            if set(changed) - {TARGET}:
                raise Refused('existing promotion branch includes unapproved changes')
        else:
            _run(['git', 'checkout', '-b', branch], checkout)
            _run(['git', 'add', '--', TARGET], checkout)
            _run(['git', '-c', 'user.name=Campaign Evaluator', '-c',
                  'user.email=evaluation@example.invalid', 'commit', '-m', title], checkout)
            _run(['git', 'push', 'origin', 'HEAD:refs/heads/' + branch], checkout)
        with tempfile.TemporaryDirectory(prefix='quivr-pr-') as directory:
            path = pathlib.Path(directory) / 'body.md'
            path.write_text(body)
            _run(['gh', 'pr', 'create', '--head', branch, '--base', 'main', '--title', title,
                  '--body-file', str(path)], checkout)
        return self.find(branch)


@control_store.retry_contention
def _promotion_claim(store, name, key, branch, evidence):
    with store.edit(name) as (db, state):
        now = db.execute('SELECT clock_timestamp()').fetchone()[0]
        records = state.setdefault('promotions', {})
        previous = records.get(key, {})
        if previous.get('evidence_hash') not in (None, evidence):
            raise Refused('promotion has conflicting confirmation evidence')
        # SQL time, durable metadata and a separate owner fence; compute cleanup
        # can expire admission leases without granting another PR publisher.
        if previous.get('owner') and previous.get('expires_at'):
            from datetime import datetime
            if datetime.fromisoformat(previous['expires_at']) > now:
                return None, {'status': 'preparing', 'reason': 'Promotion is owned by another publisher.', 'branch': branch}
        owner = uuid.uuid4().hex
        from datetime import timedelta
        records[key] = {**previous, 'status': 'preparing', 'reason': 'Preparing confirmed configuration.',
                        'owner': owner, 'expires_at': (now + timedelta(seconds=1800)).isoformat(),
                        'branch': branch, 'evidence_hash': evidence}
        return owner, None


@control_store.retry_contention
def _promotion_finish(store, name, key, owner, outcome):
    with store.edit(name) as (db, state):
        record = state['promotions'][key]
        now = db.execute('SELECT clock_timestamp()').fetchone()[0]
        from datetime import datetime
        if record['owner'] != owner or datetime.fromisoformat(record['expires_at']) <= now:
            raise Refused('promotion publisher ownership expired or changed')
        record.update(outcome, owner=None, expires_at=None)


def _pr_result(pr, marker):
    if (not isinstance(pr, dict) or not isinstance(pr.get('url'), str)
            or not re.fullmatch(r'https://github.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/pull/[0-9]+', pr['url'])
            or marker not in pr.get('body', '')):
        raise Refused('GitHub acknowledgement does not bind confirmation evidence')
    return pr['url']


def _aggregate_uri(uri=None, private_uri=None):
    import results
    uri = uri or os.environ.get('MLFLOW_TRACKING_URI')
    private_uri = private_uri or os.environ.get('MLFLOW_PRIVATE_TRACKING_URI')
    if not uri:
        raise Refused('Aggregate evidence link unavailable; configure the aggregate tracking URI.')
    try:
        aggregate = results.Tracking(uri).uri
        private = results.Tracking(private_uri).uri if private_uri else None
        if (aggregate == private or 'private' in urllib.parse.urlsplit(aggregate).path.lower().split('/')):
            raise ValueError()
        return aggregate
    except Exception:
        raise Refused('Aggregate evidence link unavailable; private or invalid tracking URI is refused.') from None


def _pr_body(receipt, marker, tracking_uri):
    request = receipt['bindings']
    lines = ['Apply the confirmed hosted embedding and retrieval configuration.', '',
             'Full-engine gates: ' + ', '.join(k + '=pass' for k in GATES) + '; held-out=pass.',
             'Baseline: ' + request['git_sha'],
             'Baseline configuration: ' + request['baseline_hash'],
             'Candidate configuration: ' + request['candidate_hash'],
             'Effective settings: ' + request['settings_fingerprint'],
             'Model price revision: ' + PRICE_REVISION, '',
             'Synced aggregate evidence (result-store keys):']
    lines.extend('- [' + item['result_key'] + '](' + tracking_uri +
                 '/api/2.0/mlflow/runs/get?' + urllib.parse.urlencode({'run_id': item['run_id']}) + ')'
                 for item in receipt['receipts'])
    lines.extend(['', 'No automatic merge or production rollout.', '', marker])
    return '\n'.join(lines)


def promote(store, name, trial, *, repository=ROOT, github=None, builder=None,
            aggregate_tracking_uri=None, private_tracking_uri=None):
    """Open one PR only from a stored trusted receipt, after checking live main."""
    state = store.snapshot(name)
    confirmed = state.get('confirmations', {}).get(str(trial), {})
    if confirmed.get('status') != 'confirmed' or not confirmed.get('receipt'):
        return {'status': 'blocked', 'reason': 'Trusted full-engine and held-out confirmation is required.'}
    receipt = confirmed['receipt']
    owner, key = None, None
    branch = None
    try:
        tracking_uri = _aggregate_uri(aggregate_tracking_uri, private_tracking_uri)
        bindings = receipt['bindings']
        fusion = bindings['effective_candidate']['retrieve']['hybrid_fusion']
        with _checkout(repository, state['git_sha']) as checkout, \
             _checkout(repository, bindings['engine_runner_git_sha']) as engine_checkout:
            request = _request(state, trial, checkout, bindings['heldout_fingerprint'], fusion,
                               engine_git_sha=bindings['engine_runner_git_sha'],
                               engine_scorer_digest=bindings['engine_runner_scorer_digest'],
                               heldout_family=bindings['heldout_family'], engine_checkout=engine_checkout,
                               confirmation_policy_digest=bindings['confirmation_policy_digest'])
            resources = state['resources']
            registered = sorted({r['app_id'] for r in resources.values()
                                if r['app_id'] in receipt['compute_ids'] and r['status'] == 'closed'})
            available = store.availability(name)
            reads = request['confirmation_limit'] - available['confirmation_reads_left']
            _validate_receipt(receipt, request, registered, reads)
            # Bind the branch to actual engine settings as well as tier-1 config;
            # ranked and relative-score confirmations can never reuse one PR.
            key = search_trial.digest([request['candidate_hash'], request['settings_fingerprint']])
            if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,127}', name):
                raise Refused('invalid campaign branch identity')
            branch = 'eval/' + name + '/' + key
            github = github or GitHub(repository)
            main = github.current_main(checkout) if hasattr(github, 'current_main') else _main_revision(repository)
            _run(['git', 'checkout', '--quiet', '--detach', main], checkout)
            if _fingerprints(checkout) != request['engine_files']:
                return {'status': 'blocked', 'reason': 'Relevant main drift requires remeasure and trusted confirmation.', 'branch': branch}
            owner, busy = _promotion_claim(store, name, key, branch, search_trial.digest(receipt))
            if busy:
                return busy
            marker = '<!-- campaign-confirmation:' + search_trial.digest(receipt) + ' -->'
            # Find first, even after a prior response timeout. Durable state alone
            # cannot close the external acknowledgement gap.
            baseline = state['spec']['policy']['baseline']
            candidate = state['trials'][str(trial)]['config']
            effective = promotion_apply(checkout, candidate, baseline, fusion=fusion, builder=builder)
            if effective != request['effective_candidate']:
                raise Refused('resulting settings do not match trusted full-engine confirmation')
            if set(_run(['git', 'diff', '--name-only'], checkout).splitlines()) != {TARGET}:
                raise Refused('promotion must change only the allowlisted configuration file')
            existing = github.find(branch)
            if existing:
                if hasattr(github, 'verify'):
                    github.verify(checkout, branch)
                url = _pr_result(existing, marker)
            else:
                title = 'feat(search): apply confirmed hosted retrieval settings'
                url = _pr_result(github.create(checkout, branch, title, _pr_body(receipt, marker, tracking_uri)), marker)
        outcome = {'status': 'opened', 'reason': 'Confirmed configuration PR opened for human review.', 'branch': branch, 'pr_url': url}
    except Refused as error:
        outcome = {'status': 'retry' if owner else 'blocked', 'reason': str(error)}
        if branch:
            outcome['branch'] = branch
    except Exception:
        # Reflected git/GitHub/runner output is never a campaign artifact.
        outcome = {'status': 'retry' if owner else 'blocked',
                   'reason': 'Promotion preparation or acknowledgement failed; verify configuration and retry.'}
        if branch:
            outcome['branch'] = branch
    if owner:
        _promotion_finish(store, name, key, owner, outcome)
    return outcome


def advance(store, name, owner, *, repository=ROOT, adapter=None, compute=None, github=None, builder=None):
    """Advance finalists; absent adapter persists an honest pending state only."""
    state = store.snapshot(name)
    outcomes = []
    import campaign_reporting
    eligible = {**state, 'trials': {n: t for n, t in state['trials'].items()
                                   if t.get('report', {}).get('status') == 'exploration_finalist'}}
    # The parent spec sends the top 2-3 exploration points to the expensive tier.
    points = campaign_reporting.leaderboard(eligible)[:3]
    finalists = {str(p['number']) for p in points}
    order = {str(p['number']): rank for rank, p in enumerate(points)}
    for number, trial in sorted(state['trials'].items(), key=lambda item: (order.get(item[0], len(order)), int(item[0]))):
        if trial.get('report', {}).get('status') != 'exploration_finalist':
            continue
        if adapter is not None and number not in finalists:
            continue
        if state.get('confirmations', {}).get(number, {}).get('status') in ('rejected', 'unavailable'):
            continue
        result = confirm(store, name, int(number), owner, adapter=adapter, repository=repository, compute=compute)
        if adapter is None:
            _remember_confirmation(store, name, number, result)
        elif result['status'] == 'confirmed':
            result = promote(store, name, int(number), repository=repository, github=github, builder=builder)
        outcomes.append({'trial': int(number), **result})
    return outcomes


def public_status(state):
    """Only safe lifecycle summaries; receipts/adapter payload never cross here."""
    configured = 'confirmation_configuration' in state
    return {'confirmation_available': configured,
            'confirmation_reason': 'Trusted full-engine confirmation configured.' if configured else DISABLED,
            'confirmations': [{'trial': int(n), 'status': r['status'], 'reason': r['reason']}
                              for n, r in state.get('confirmations', {}).items()],
            'promotions': [{k: r[k] for k in ('status', 'reason', 'branch', 'pr_url') if k in r}
                           for r in state.get('promotions', {}).values()]}
