"""Trusted paired full-engine measurement; raw input/rankings stay ephemeral."""
import contextlib
import hashlib
import json
import math
import os
import pathlib
import signal
import subprocess
import tempfile
import time
import uuid

import control_store
import embeddings
import engine_confirmation as confirmation
import gates
import results
import scoring
import protected_inputs
import trec

ROOT = pathlib.Path(__file__).resolve().parents[2]


def split_identity(directory):
    rows = [json.loads(line) for line in (pathlib.Path(directory) / 'queries.jsonl').read_text().splitlines() if line.strip()]
    # Detect repeated text as well as IDs: a new ID does not create a held-out query.
    return confirmation.digest(sorted((r['_id'], r['text']) for r in rows)), {r['_id'] for r in rows}, {r['text'] for r in rows}


def inputs(request, references, directory):
    """Called only after single-use SQL admission. References never enter output."""
    if set(references) != set(confirmation.family_sets(request['heldout_family'])):
        raise ValueError('protected input family is incomplete')
    loaded = {}
    for name, entry in confirmation.family_sets(request['heldout_family']).items():
        reference = references[name]
        if set(reference) - {'heldout', 'dev', 'age_identity', 'provider_consent'} or not {'heldout', 'dev'} <= set(reference):
            raise ValueError('invalid protected runtime references')
        path = pathlib.Path(reference['heldout'])
        # A ciphertext checksum is checked before decryption, and TREC content
        # is checked independently afterwards. Private plaintext paths refused.
        if confirmation.private(entry):
            if not reference.get('age_identity') or reference.get('provider_consent') is not True:
                raise PermissionError('private input requires held-out key and provider consent')
            target = pathlib.Path(directory) / name
            target.mkdir()
            held = protected_inputs.decrypt(entry, path, reference['age_identity'], target)
        elif path.is_dir():
            held = path
            if trec.fingerprint(held) != entry['digest']:
                raise ValueError('public held-out checksum mismatch')
        else:
            if trec.sha256_file(path) != entry['digest']:
                raise ValueError('held-out archive checksum mismatch')
            held = trec.materialize(path, pathlib.Path(directory) / name)
        if trec.fingerprint(held) != entry['fingerprint']:
            raise ValueError('held-out content fingerprint mismatch')
        dev = pathlib.Path(reference['dev'])
        if trec.fingerprint(dev) != request['datasets'][name]['fingerprint']:
            raise ValueError('working input fingerprint mismatch')
        dev_split, dev_ids, dev_texts = split_identity(dev)
        _, held_ids, held_texts = split_identity(held)
        if (dev_split != request['datasets'][name]['split_fingerprint']
                or dev_ids & held_ids or dev_texts & held_texts):
            raise ValueError('working and held-out queries overlap or changed')
        data = trec.load(held)
        if not data['qrels'] or not data['corpus']:
            raise ValueError('empty scorable held-out data')
        loaded[name] = data
    return loaded


def paired(clients, data, mappings, budgets, resource_class, rate, sha, *, timeout=600):
    """One ingest per side, alternating paired quality and fresh latency queries."""
    import run
    sides = {}
    ids = sorted(data['qrels'])
    sample = sorted(ids, key=lambda q: (hashlib.sha256(q.encode()).hexdigest(), q))[:50]
    latency_sample = {'policy': 'sha256-query-id-v1; max=50', 'query_ids': sample, 'warmup_query_ids': [ids[0]]}
    for side in ('baseline', 'candidate'):
        client, budget = clients[side], budgets[side]
        for gate in client.embedding_gates:
            gate.phase = 'indexing'
        before, started = budget.summary()['confirmed_cost_usd'], time.monotonic()
        _, corpus = client.call('POST', '/v0/corpora', {'name': 'Engine confirmation', 'idempotency_key': uuid.uuid4().hex}, expected=(201,), attempts=1)
        keys, _ = run.ingest(client, corpus['corpus_id'], 'confirmation', data['corpus'], timeout, min(timeout, 60))
        if len(keys) != len(data['corpus']) or set(keys.values()) != set(data['corpus']):
            raise RuntimeError('engine index is incomplete')
        elapsed = time.monotonic() - started
        index_price = budget.summary()['confirmed_cost_usd'] - before + elapsed * rate
        if budget.summary()['reserved_input_tokens']:
            raise RuntimeError('unknown indexing usage')
        for gate in client.embedding_gates:
            gate.phase = 'query'
        sides[side] = {'client': client, 'corpus': corpus['corpus_id'], 'keys': keys,
                       'ranking': {}, 'latencies': [], 'prices': [],
                       'index_price': index_price * 1000 / len(data['corpus'])}

    def search(side, query):
        context, budget, cfg = sides[side], budgets[side], mappings[side]
        before = budget.summary()['confirmed_cost_usd']
        ranking, timing, error = run.search(context['client'], context['corpus'], data['queries'][query],
            cfg['mode'], cfg['profile'], context['keys'], limit=cfg['request_limit'])
        if error is not None or not gates.finite(timing.get('client_ms')):
            raise RuntimeError('engine search failed')
        if budget.summary()['reserved_input_tokens']:
            raise RuntimeError('unknown search usage')
        return ranking, timing['client_ms'], budget.summary()['confirmed_cost_usd'] - before + timing['client_ms'] / 1000 * rate

    for number, query in enumerate(ids):
        for side in (('baseline', 'candidate') if number % 2 == 0 else ('candidate', 'baseline')):
            sides[side]['ranking'][query] = search(side, query)[0]
    for side in sides:
        search(side, ids[0])  # identical fixed warmup, accounted in shared ledger
    for number, query in enumerate(sample):
        for side in (('candidate', 'baseline') if number % 2 == 0 else ('baseline', 'candidate')):
            _, latency, price = search(side, query)
            sides[side]['latencies'].append(latency)
            sides[side]['prices'].append(price)
    pair = {}
    for side, context in sides.items():
        scores = scoring.score(data['qrels'], context['ranking'])
        times = sorted(context['latencies'])
        pair[side] = {'tier': 'engine', 'git_sha': sha,
            'per_query': scores['per_query'],
            'metrics': {**scores['mean'], 'latency_p95_ms': times[math.ceil(.95 * len(times)) - 1],
                        'cost_per_search_usd': sum(context['prices']) / len(context['prices']),
                        'cost_per_1000_documents_usd': context['index_price']},
            'cost': {'resource_class': resource_class, 'latency_method': 'full-engine fresh serial; fixed hash sample max50; one warmup',
                     'latency_sample': latency_sample}}
    return pair


class EngineBudget(control_store.Budget):
    def check(self):
        available = self.store.availability(self.campaign)
        if available['stopped'] or available['paused']:
            raise embeddings.BudgetExceeded('campaign stopped or capped')


@contextlib.contextmanager
def stack(mapping, budget, runtime, stacks):
    """Real installed plugins. Endpoint/key references exist only in this scope."""
    import sys
    sys.path.insert(0, str(ROOT / 'scripts'))
    import run
    import hosted_embed_plugin
    import ingestion_plugin
    import plugin_environment
    import ports
    client = run.start_stack({}, stacks)
    owned, process, gate = client.stack, None, None
    try:
        configuration = None
        if mapping['ingestion']['kind'] == 'hosted':
            cfg = {k: v for k, v in mapping['ingestion'].items() if k != 'kind'}
            gate = embeddings.Gate(budget, cfg['model'], cfg['format'], runtime['endpoint'], runtime['key'],
                                   runtime['prices'][cfg['model']], cfg['dimensions'])
            gate.__enter__()
            cfg.update(base_url=gate.url, auth='none')
            directory = owned.directory / 'hosted-confirmation'
            binary = hosted_embed_plugin.build(directory)
            config = directory / 'configuration.json'
            config.write_text(results.encode(cfg))
            manifest = directory / 'quivr-plugin.yaml'
            with manifest.open('w') as out:
                subprocess.run([str(binary), 'configure', str(config)], stdout=out, check=True)
            declaration = json.loads(manifest.read_text())
            space = next(iter(declaration['contributions']['ingestion']['spaces']))
            port = ports.allocate()
            with (directory / 'plugin.log').open('w') as out:
                process = subprocess.Popen([str(binary)], env={**plugin_environment.inherited(), 'QUIVR_PLUGIN_HOST': '127.0.0.1',
                    'QUIVR_PLUGIN_PORT': str(port), 'QUIVR_PLUGIN_MANIFEST': str(manifest)},
                    stdout=out, stderr=out, start_new_session=True)
            ingestion_plugin.await_healthy(process, port, directory / 'plugin.log')
            configuration = {'manifest': str(manifest), 'endpoint': f'http://127.0.0.1:{port}',
                             'configuration': cfg, 'spaces': {space: 'served'}}
            client.embedding_gates = [gate]
        owned.stop_processes()
        for name in ('config.json', 'worker.json'):
            path = owned.directory / name
            cfg = json.loads(path.read_text())
            for pin in cfg['plugins']:
                if pathlib.Path(pin['manifest']).parent.name == 'core-retrieve':
                    pin['configuration'] = mapping['retrieve']
            if configuration:
                cfg['plugins'].append(configuration)
                cfg['ingestion'] = {'default': 'hosted.embed'}
            path.write_text(results.encode(cfg))
        owned.start_processes()
        # Admin scope is intentionally separate from the search credential.
        admin = run.Client(client.base, owned.state['operator'])
        _, registrations = admin.call('GET', '/v0/admin/plugins', attempts=1)
        rows = registrations['items']
        retrieval = [r for r in rows if r['plugin_id'] == 'core.retrieve' and r['state'] == 'active']
        # Registry API exposes immutable identity/endpoint, not configuration.
        # Verify the active registration against the actual startup pin; that
        # pin is what the engine validated and passes on every invocation.
        pins = json.loads((owned.directory / 'config.json').read_text())['plugins']
        pin = next(p for p in pins if pathlib.Path(p['manifest']).parent.name == 'core-retrieve')
        if (len(retrieval) != 1 or retrieval[0]['endpoint'] != pin['endpoint']
                or retrieval[0]['version'] != '1.2.0' or pin['configuration'] != mapping['retrieve']):
            raise RuntimeError('active retrieval registration differs')
        if configuration:
            hosted = [r for r in rows if r['plugin_id'] == 'hosted.embed' and r['state'] == 'active']
            if len(hosted) != 1 or hosted[0]['endpoint'] != configuration['endpoint']:
                raise RuntimeError('active ingestion registration differs')
        yield client
    finally:
        # Run every owned cleanup even when a preceding cleanup fails.
        with contextlib.ExitStack() as cleanup:
            if gate is not None:
                cleanup.callback(gate.__exit__, None, None, None)
            if process is not None:
                cleanup.callback(ingestion_plugin.stop_plugin, process)
            cleanup.callback(owned.stop_processes)


def execute(store, request, references, runtime, progress=None):
    """Admit once, load once, measure pairing and project inside trusted scope."""
    import engine_stack
    import run
    lease = request['lease_key'], request['owner']
    req = request['confirmation_request']
    policy = store.policy(req['campaign'])
    runtime = {**runtime, 'prices': policy['prices']}
    req = confirmation.validate(req, policy)
    if store.confirmation_policy(req['campaign']) != confirmation.invariants(req, policy):
        raise ValueError('frozen confirmation binding mismatch')
    ordinal = store.confirmation(req['campaign'], lease)
    stacks, pairs = [], {}
    try:
        with tempfile.TemporaryDirectory(prefix='quivr-heldout-') as directory:
            family = inputs(req, references, directory)
            for count, (name, data) in enumerate(family.items(), 1):
                engine_stack.progress(progress, 'ingesting', count)
                budgets = {side: EngineBudget(store, req['campaign'], lease) for side in ('baseline', 'candidate')}
                mappings = {side: req['effective_' + side] for side in budgets}
                cfg = req['mapping_policy']['resources']
                with contextlib.ExitStack() as contexts:
                    clients = {side: contexts.enter_context(stack(mappings[side], budgets[side], runtime, stacks)) for side in budgets}
                    engine_stack.progress(progress, 'searching', count)
                    pair = paired(clients, data, mappings, budgets,
                        f"cpu{cfg['cpu']}-memory{cfg['memory_mib']}-vm", float(cfg['modal_usd_per_second']),
                        req['engine_runner_git_sha'], timeout=cfg['max_seconds'])
                for row in pair.values():
                    row['dataset'] = {'name': name, **confirmation.family_sets(req['heldout_family'])[name]}
                pairs[name] = pair
            aggregate = confirmation.aggregate(pairs, req, policy)
    finally:
        previous = {sig: signal.getsignal(sig) for sig in (signal.SIGINT, signal.SIGTERM)}
        for sig in previous:
            signal.signal(sig, signal.SIG_IGN)
        engine_stack.progress(progress, 'cleanup')
        try:
            with contextlib.ExitStack() as cleanup:
                for owned in stacks:
                    cleanup.callback(teardown, owned)
        finally:
            for sig, handler in previous.items():
                signal.signal(sig, handler)
    return {**aggregate, 'read_ordinal': ordinal, 'remote_cleanup_verified': True}


def teardown(owned):
    from local import alive
    pids = list(owned.state['pids']) + [value for key, value in owned.state.items() if key.endswith('_plugin_pid')]
    owned.down(reset=True)
    if (owned.compose('ps', '--all', '-q', capture_output=True, text=True).stdout.strip()
            or any(alive(pid) for pid in pids)):
        raise RuntimeError('engine resources remain after cleanup')
