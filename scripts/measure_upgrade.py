#!/usr/bin/env python3
"""make measure-upgrade: a live plugin upgrade, drain, rollback and backfill under load (THE-786).

Spec 5's exit criterion, run end to end on an isolated real stack (Linux x86_64
only, like the rest of the harness). Two builds of the Go SDK sample ingestion
plugin (sdks/go/examples/hash-embedder) run side by side: A, 0.1.0, pinned by
the configuration with its small space, and B, 0.2.0, which adds its large
space for evaluation. Each is reached through a proxy that delays
segment_and_embed by PLUGIN_DELAY, like a model would, so work is always in
flight when the plan changes. While a client ingests Records without pause and
a sampler reads the API every 100 ms, an operator follows the upgrade guide
(docs-site/run-quivr/upgrade-a-plugin.mdx):

1. A alone.
2. B is registered, checked and activated; A drains while the worker restarts.
3. One call rolls back to A with pinned work draining; B drains while the api
   restarts, and the plan must survive the restart.
4. B is activated again and A drains; a backfill then fills B's large space
   for every Record accepted before this phase, with a dry run first; the
   worker and the api restart while it runs.

Then the load stops and the gate checks that every Record the client
submitted is searchable exactly once, nothing is quarantined, the API never
failed outside its own restarts, each switch happened with work in flight and
each drain ended (within KILLED_DRAIN_BOUND when the worker was killed during
it), new work ran on the active version, and the backfill filled
its window. The report, upgrade-measurement.json and .md, gives every check
with the per-phase API latency, drain times and which version segmented the
Records. A failed check exits nonzero.
"""
import datetime
import http.server
import json
import os
import platform
import signal
import statistics
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

import ingestion_plugin as sample
import ports

RATE = float(os.environ.get('QUIVR_UPGRADE_RATE', '3'))
PHASE_RECORDS = int(os.environ.get('QUIVR_UPGRADE_PHASE_RECORDS', '10'))
BACKFILL_RATE = float(os.environ.get('QUIVR_UPGRADE_BACKFILL_RATE', '4'))
PLUGIN_DELAY = float(os.environ.get('QUIVR_UPGRADE_PLUGIN_DELAY', '0.25'))
SAMPLE_INTERVAL = .1
# Every process follows a plan change within plugin_plan_poll (200 ms on the
# local stack); work dispatched this long after a switch uses the new plan.
FOLLOW = 1.0
# A drain during which the worker is killed ends within this many seconds of the
# switch: every processing step heartbeats under a 10 s timeout, so a step the
# dead worker held is retried after it, then the 1 s retry interval and the
# worker restart (THE-835). Before, the dead step held the drain for its 30 s
# start-to-close timeout.
KILLED_DRAIN_BOUND = 20.0
A_SPACES = {'example.hash_embedder.small': 'served'}
B_SPACES = sample.SPACES
SEGMENT_ROUTE = '/v0/contributions/ingestion/segment_and_embed'


def call(base, token, method, path, body=None, timeout=10):
    """One API call: (status, JSON body or None, error text). Never raises."""
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method, headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
    try:
        response = urllib.request.urlopen(req, timeout=timeout)
    except urllib.error.HTTPError as error:
        response = error
    except OSError as error:
        return None, None, type(error).__name__ + ': ' + str(error)[:200]
    with response:
        raw = response.read()
    try:
        return response.status, json.loads(raw) if raw else None, ''
    except ValueError:
        return response.status, None, raw[:200].decode(errors='replace')


def expect(base, token, method, path, body=None, status=200):
    got, result, error = call(base, token, method, path, body)
    if got != status:
        raise RuntimeError(f'{method} {path} returned {got}, want {status}: {result or error}')
    return result


class ConditionTimeout(RuntimeError):
    """The measurement's condition stayed false until its deadline."""


def await_condition(what, read, done, deadline=120, interval=.2):
    """Poll read() until done(value), or fail naming what and the last value."""
    start = time.monotonic()
    while True:
        value = read()
        if done(value):
            return value
        if time.monotonic() - start > deadline:
            raise ConditionTimeout(f'{what} not reached within {deadline} s; last: {value}')
        time.sleep(interval)


class Proxy:
    """Forwards every request to a plugin, holding segment_and_embed for PLUGIN_DELAY first."""

    def __init__(self, target):
        class Handler(http.server.BaseHTTPRequestHandler):
            def forward(self):
                body = self.rfile.read(int(self.headers.get('Content-Length') or 0)) or None
                if self.path == SEGMENT_ROUTE:
                    time.sleep(PLUGIN_DELAY)
                headers = {k: v for k, v in self.headers.items() if k.lower() not in ('host', 'content-length', 'connection')}
                req = urllib.request.Request(target + self.path, data=body, method=self.command, headers=headers)
                try:
                    response = urllib.request.urlopen(req, timeout=60)
                except urllib.error.HTTPError as error:
                    response = error
                with response:
                    raw = response.read()
                    self.send_response(response.status)
                    for k, v in response.headers.items():
                        if k.lower() not in ('content-length', 'connection', 'transfer-encoding', 'date', 'server'):
                            self.send_header(k, v)
                self.send_header('Content-Length', str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)
            do_GET = do_POST = forward

            def log_message(self, *_):
                pass
        self.port = ports.allocate()
        self.server = http.server.ThreadingHTTPServer(('127.0.0.1', self.port), Handler)
        self.server.daemon_threads = True
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.endpoint = f'http://127.0.0.1:{self.port}'

    def close(self):
        self.server.shutdown()


class Clock:
    """The scenario's phase and api-restart windows, shared with the load threads."""

    def __init__(self):
        self.start = time.monotonic()
        self.phase = 'setup'
        self.restarting = False
        self.restarts = []

    def now(self):
        return round(time.monotonic() - self.start, 3)

    def set(self, phase):
        self.phase = phase
        return self.now()

    def restart_api(self, stack):
        began = self.now()
        self.restarting = True
        try:
            stack.stop_api()
            stack.start_api()
        finally:
            self.restarting = False
        self.restarts.append({'phase': self.phase, 'from': began, 'to': self.now(), 'seconds': round(self.now() - began, 3)})


class Client(threading.Thread):
    """Submits one unique Record after another, retrying a failed call with the same idempotency key."""

    def __init__(self, base, token, corpus, run, clock):
        super().__init__(daemon=True)
        self.base, self.token, self.corpus, self.run_id, self.clock = base, token, corpus, run, clock
        self.records, self.failures = [], []
        # halt ends the load after the current Record; abort also gives up retrying it.
        self.halt, self.abort = threading.Event(), threading.Event()

    def accepted(self, phase, since=0):
        return [r for r in list(self.records) if r.get('phase') == phase and r.get('receipt_id') and r['accepted'] >= since]

    def run(self):
        i = 0
        while not self.halt.is_set():
            tick = time.monotonic()
            word = f'zq{self.run_id}{i:04d}'
            record = {'key': f'upgrade-{self.run_id}-{i:04d}', 'word': word, 'submitted': self.clock.now()}
            command = {'idempotency_key': record['key'], 'source': {'corpus_id': self.corpus, 'namespace': 'upgrade', 'record_key': record['key']},
                       'content': {'kind': 'text', 'text': f'Harbour log {i}: the {word} buoy was checked at the pier before the tide.'}}
            self.records.append(record)
            while not self.abort.is_set():
                restarting = self.clock.restarting
                status, body, error = call(self.base, self.token, 'POST', '/v0/records', command)
                if status == 202:
                    record.update(receipt_id=body['receipt_id'], accepted=self.clock.now(), phase=self.clock.phase)
                    break
                self.failures.append({'at': self.clock.now(), 'phase': self.clock.phase, 'status': status, 'error': error or body, 'restart': restarting or self.clock.restarting})
                if self.clock.now() - record['submitted'] > 120:
                    break
                time.sleep(.2)
            i += 1
            time.sleep(max(0, 1 / RATE - (time.monotonic() - tick)))


class Sampler(threading.Thread):
    """Reads the Corpus and runs a semantic search, alternately, every SAMPLE_INTERVAL."""

    def __init__(self, base, token, corpus, clock):
        super().__init__(daemon=True)
        self.base, self.token, self.corpus, self.clock = base, token, corpus, clock
        self.samples, self.halt = [], threading.Event()

    def run(self):
        search = {'query': 'buoy checked at the pier', 'corpus_ids': [self.corpus], 'mode': 'semantic', 'limit': 5}
        n = 0
        while not self.halt.is_set():
            tick = time.monotonic()
            op = 'search' if n % 2 else 'read'
            restarting = self.clock.restarting
            if op == 'search':
                status, body, error = call(self.base, self.token, 'POST', '/v0/search', search)
            else:
                status, body, error = call(self.base, self.token, 'GET', '/v0/corpora/' + self.corpus)
            ms = round((time.monotonic() - tick) * 1000, 1)
            self.samples.append({'at': self.clock.now(), 'phase': self.clock.phase, 'op': op, 'ok': status == 200, 'status': status, 'ms': ms,
                                 'restart': restarting or self.clock.restarting, **({'error': error or body} if status != 200 else {})})
            n += 1
            time.sleep(max(0, SAMPLE_INTERVAL - (time.monotonic() - tick)))


def verdict(r):
    """The gate: one check per exit criterion, each {name, ok, detail}. Pure, so it is unit tested."""
    records, checks = r['records'], []

    def check(name, failing, detail):
        checks.append({'name': name, 'ok': not failing, 'detail': detail if failing else 'ok'})
    lost = [x['key'] for x in records if not x.get('receipt_id')]
    check('every_record_accepted', lost, f'{len(lost)} of {len(records)} never accepted: {lost[:5]}')
    missing = [x['key'] for x in records if x.get('receipt_id') and not (x.get('searchable') and x.get('current') and x.get('enriched'))]
    check('every_record_searchable', missing, f'{len(missing)} not current, searchable and enriched: {missing[:5]}')
    twice = [x['key'] for x in records if x.get('receipt_id') and (x.get('versions') != 1 or x.get('hits') != [x.get('record_id')])]
    check('exactly_once', twice, f'{len(twice)} without exactly one Version and one search hit: {twice[:5]}')
    check('nothing_quarantined', r['quarantined'], f"{r['quarantined']} Versions quarantined")
    down = [s for s in r['samples'] if not s['ok'] and not s['restart']] + [f for f in r['client_failures'] if not f['restart']]
    check('api_available', down, f'{len(down)} failed calls outside api restarts: {down[:3]}')
    idle = [d['name'] for d in r['drains'] if d['at_switch']['state'] != 'draining' or d['at_switch']['pinned_work'] < 1 or d.get('restart_while_draining') is False]
    if (r.get('backfill') or {}).get('state_at_restart') != 'running':
        idle.append('backfill')
    check('transitions_under_load', idle, f'no work in flight at the switch or restart: {idle}')
    stuck = [d['name'] for d in r['drains'] if not d.get('inactive')]
    check('drains_finish', stuck, f'still draining: {stuck}')
    slow = [f"{d['name']} {d.get('seconds')} s" for d in r['drains'] if d.get('worker_killed') and d.get('inactive') and d['seconds'] > KILLED_DRAIN_BOUND]
    check('drain_after_worker_kill_bounded', slow, f'a drain the worker was killed during took over {KILLED_DRAIN_BOUND} s: {slow}')
    wrong = [x['key'] for x in records if x.get('expected_version') and x.get('segmented_by') != x['expected_version']]
    unproven = [p for p in r['expectations'] if not any(x.get('expected_version') and x.get('phase') == p for x in records)]
    check('new_work_on_active_version', wrong or unproven, f'{len(wrong)} segmented by another version than the active one: {wrong[:5]}; phases without such work: {unproven}')
    b = r.get('backfill') or {}
    counters = b.get('counters', {})
    filled = b.get('state') == 'succeeded' and counters.get('versions_skipped', 0) == 0 and \
        counters.get('versions_in_scope') == counters.get('versions_done') == b.get('estimated_versions') == b.get('window_records')
    check('backfill_fills_its_window', not filled, f"backfill {b.get('state')}, counters {counters}, dry run {b.get('estimated_versions')}, window {b.get('window_records')}")
    reverted = [p for p in r['plans_after_restart'] if p['got'] != p['want']]
    check('plan_survives_api_restart', reverted, f'plan changed by a restart: {reverted}')
    return checks


def registration(base, operator, version, endpoint):
    items = expect(base, operator, 'GET', '/v0/admin/plugins')['items']
    found = [x for x in items if x['plugin_id'] == 'example.hash_embedder' and x['version'] == version and x['endpoint'] == endpoint and x['state'] != 'rejected']
    if not found:
        raise RuntimeError(f'no registration of example.hash_embedder@{version} at {endpoint}: {items}')
    return found[0]


def drain(base, operator, name, version, endpoint, clock, switched, during=None):
    """Follow a registration from the switch until it reads inactive; `during` runs while it drains."""
    first = registration(base, operator, version, endpoint)
    d = {'name': name, 'switched': switched, 'at_switch': {'state': first['state'], 'pinned_work': first['pinned_work']}, 'peak_pinned_work': first['pinned_work']}
    if during:
        d['restart_while_draining'] = first['state'] == 'draining'
        during()

    def read():
        reg = registration(base, operator, version, endpoint)
        d['peak_pinned_work'] = max(d['peak_pinned_work'], reg['pinned_work'])
        return reg
    try:
        await_condition(f'{name} inactive', read, lambda reg: reg['state'] == 'inactive' and reg['pinned_work'] == 0, deadline=180)
        d['inactive'] = True
    except RuntimeError as error:
        d['error'] = str(error)[:300]
    d['done'] = clock.now()
    d['seconds'] = round(d['done'] - d['switched'], 3)
    return d


def routing_plan(base, operator, path, body):
    operation = expect(base, operator, 'POST', path, body, status=202)
    operation = await_condition('routing operation',
        lambda: expect(base, operator, 'GET', '/v0/operations/' + operation['operation_id']),
        lambda value: value['state'] in ('succeeded', 'failed'), deadline=180)
    if operation['state'] != 'succeeded':
        raise RuntimeError(f'routing failed: {operation}')
    return expect(base, operator, 'GET', '/v0/admin/plugins/plans/' + operation['admin']['plan_id'])


def activate(base, operator, reg, version):
    plan = routing_plan(base, operator, f"/v0/admin/plugins/{reg['registration_id']}/activate", {})
    ingestion = [x for x in plan['roles'] if x['role'] == f"ingestion:{reg['plugin_id']}"]
    if len(ingestion) != 1 or ingestion[0]['registration_id'] != reg['registration_id'] or ingestion[0]['version'] != version:
        raise RuntimeError(f'activating {version}: {plan}')
    return plan


def active_plan(base, operator):
    return expect(base, operator, 'GET', '/v0/admin/plugins/plan')['plan_id']


def documents(base, admin, corpus):
    """Every Version of the Corpus from the admin documents list, keyed by record_key."""
    out, cursor = {}, ''
    while True:
        page = expect(base, admin, 'GET', '/v0/admin/documents?limit=100' + ('&page_cursor=' + urllib.parse.quote(cursor) if cursor else ''))
        for doc in page['items']:
            if doc['corpus_id'] == corpus:
                out.setdefault(doc['record_key'], []).append(doc)
        cursor = page.get('next_page_cursor')
        if not cursor:
            return out


def settled(base, admin, corpus, keys):
    """Keys whose Record has a current Version that is searchable and enriched."""
    docs = documents(base, admin, corpus)
    return {k for k in keys if any(d['is_current'] and d['state'] == 'retrieval_ready' and d['steps'].get('enriched_at') for d in docs.get(k, []))}


def scenario(stack, report, clock):
    stack.up()
    s = stack.state
    base, admin, operator, backfiller = f"http://127.0.0.1:{s['api_port']}", s['admin'], s['operator'], s['backfiller']
    run = uuid.uuid4().hex[:8]
    directory = stack.directory / 'measure-upgrade'
    directory.mkdir(exist_ok=True)
    binary = directory / 'hash-embedder'
    sample.subprocess.run([sample.GO, 'build', '-o', str(binary), './examples/hash-embedder'], cwd=sample.ROOT / 'sdks' / 'go', check=True)
    b_manifest = directory / 'quivr-plugin-0.2.0.yaml'
    b_manifest.write_text((sample.SAMPLE / 'quivr-plugin.yaml').read_text().replace('version: 0.1.0', 'version: 0.2.0', 1))
    a_port, b_port = ports.allocate(), ports.allocate()
    plugins = [sample.start_plugin(directory, binary, a_port, sample.SAMPLE / 'quivr-plugin.yaml', 'plugin-0.1.0.log'),
               sample.start_plugin(directory, binary, b_port, b_manifest, 'plugin-0.2.0.log')]
    proxies, client, sampler = [], None, None
    try:
        sample.await_healthy(plugins[0], a_port, directory / 'plugin-0.1.0.log')
        sample.await_healthy(plugins[1], b_port, directory / 'plugin-0.2.0.log')
        proxies = [Proxy(f'http://127.0.0.1:{a_port}'), Proxy(f'http://127.0.0.1:{b_port}')]
        a_endpoint, b_endpoint = proxies[0].endpoint, proxies[1].endpoint
        for name in ['config.json', 'worker.json']:
            cfg = json.loads((stack.directory / name).read_text())
            cfg['plugins'] = [p for p in cfg.get('plugins', []) if not p['manifest'].endswith('/core-ingest/quivr-plugin.yaml')] + [
                {'manifest': str(sample.SAMPLE / 'quivr-plugin.yaml'), 'endpoint': a_endpoint, 'spaces': A_SPACES}]
            cfg['backfill'] = {'rate': BACKFILL_RATE, 'poll': '200ms', 'max_cost_without_confirmation': 1000}
            (stack.directory / name).write_text(json.dumps(cfg))
            (stack.directory / name).chmod(0o600)
        stack.stop_processes()
        stack.start_processes()
        corpus = expect(base, admin, 'POST', '/v0/corpora', {'name': 'Live upgrade', 'idempotency_key': 'live-upgrade-' + run}, 201)['corpus_id']
        client, sampler = Client(base, admin, corpus, run, clock), Sampler(base, admin, corpus, clock)
        client.start()
        sampler.start()
        phases, expectations = report['phases'], report['expectations']

        def phase(name):
            phases.append({'name': name, 'from': clock.set(name)})

        def load(name, since=0):
            """Wait for PHASE_RECORDS Records accepted in the phase since its old version drained."""
            await_condition(f'{PHASE_RECORDS} Records accepted in {name}', lambda: len(client.accepted(name, since)), lambda n: n >= PHASE_RECORDS, deadline=120, interval=.1)

        def before_switch(name, old_version, old_endpoint):
            """Close a phase: every Record it accepted so far is dispatched, so it ran on the
            phase's plan, and the version about to leave the plan has work in flight."""
            cutoff = clock.now()
            keys = {x['key'] for x in client.records if x.get('receipt_id') and x['accepted'] <= cutoff}
            await_condition(f'the Records of {name} dispatched', lambda: keys - {k for k, docs in documents(base, admin, corpus).items() if any(d['state'] != 'received' for d in docs)},
                            lambda left: not left, deadline=120, interval=.2)
            if name in expectations:
                expectations[name]['cutoff'] = cutoff
            await_condition(f'work in flight on {old_version}', lambda: registration(base, operator, old_version, old_endpoint)['pinned_work'], lambda n: n >= 1, deadline=60, interval=.02)

        phase('a_alone')
        load('a_alone')

        # Upgrade: B runs next to A, is registered, checked and activated.
        phase('upgrade')
        accepted = expect(base, operator, 'POST', '/v0/admin/plugins', {'idempotency_key': 'upgrade-' + run, 'endpoint': b_endpoint, 'manifest': b_manifest.read_text(), 'spaces': B_SPACES}, 202)
        location = '/v0/admin/plugins/' + accepted['registration_id']
        b_reg = await_condition('the 0.2.0 check', lambda: expect(base, operator, 'GET', location), lambda reg: reg['state'] != 'registered')
        if b_reg['state'] != 'validated':
            raise RuntimeError(f'0.2.0 not validated: {b_reg}')
        before_switch('a_alone', '0.1.0', a_endpoint)
        switched = clock.now()
        activate(base, operator, b_reg, '0.2.0')
        d = drain(base, operator, 'a_after_upgrade', '0.1.0', a_endpoint, clock, switched, during=lambda: (stack.stop_worker(), stack.start_worker()))
        d['worker_killed'] = True
        report['drains'].append(d)
        expectations['upgrade'] = {'since': max(d['done'], switched + FOLLOW), 'version': '0.2.0'}
        load('upgrade', expectations['upgrade']['since'])

        # Rollback to A in one call; B's pinned work drains while the api restarts.
        before_switch('upgrade', '0.2.0', b_endpoint)
        phase('rollback')
        switched = clock.now()
        back = routing_plan(base, operator, '/v0/admin/plugins/plan/rollback', {'idempotency_key': 'rollback-' + run, 'pinned_work': 'drain'})
        if back['source'] != 'rollback' or [x['version'] for x in back['roles'] if x['role'] == f"ingestion:{b_reg['plugin_id']}"] != ['0.1.0']:
            raise RuntimeError(f'rollback: {back}')

        def restart_api():
            clock.restart_api(stack)
            report['plans_after_restart'].append({'phase': 'rollback', 'want': back['plan_id'], 'got': active_plan(base, operator)})
        d = drain(base, operator, 'b_after_rollback', '0.2.0', b_endpoint, clock, switched, during=restart_api)
        report['drains'].append(d)
        expectations['rollback'] = {'since': max(d['done'], switched + FOLLOW), 'version': '0.1.0'}
        load('rollback', expectations['rollback']['since'])

        # Upgrade again, then backfill B's large space for every Record accepted before this phase.
        before_switch('rollback', '0.1.0', a_endpoint)
        phase('backfill')
        switched = clock.now()
        plan_b = activate(base, operator, b_reg, '0.2.0')
        d = drain(base, operator, 'a_after_upgrade_again', '0.1.0', a_endpoint, clock, switched)
        report['drains'].append(d)
        expectations['backfill'] = {'since': max(d['done'], switched + FOLLOW), 'version': '0.2.0'}
        boundary = await_condition('a Record accepted in the backfill phase', lambda: client.accepted('backfill'), lambda xs: xs, interval=.05)[0]
        receipt = await_condition('the boundary Record resolved', lambda: expect(base, admin, 'GET', '/v0/ingestion-receipts/' + boundary['receipt_id']),
                                  lambda r: r['state'] == 'resolved', interval=.05)
        before = expect(base, admin, 'GET', f"/v0/records/{receipt['record_id']}/versions/{receipt['version_id']}")['accepted_at']
        # The client submits one Record at a time, so every Record before the boundary was accepted before it.
        window = [x['key'] for x in client.records[:client.records.index(boundary)] if x.get('receipt_id')]
        await_condition('the window enriched', lambda: len(settled(base, admin, corpus, window)), lambda n: n == len(window), deadline=180, interval=.5)
        body = {'idempotency_key': 'backfill-' + run, 'corpus_id': corpus, 'accepted_before': before, 'dry_run': True}
        estimate = expect(base, backfiller, 'POST', '/v0/admin/backfills', body)
        body.update(dry_run=False, confirm_cost=estimate['confirmation_required'])
        op = expect(base, backfiller, 'POST', '/v0/admin/backfills', body, 202)
        started = clock.now()
        op_path = '/v0/operations/' + op['operation_id']
        await_condition('the backfill running', lambda: expect(base, backfiller, 'GET', op_path), lambda o: o['state'] == 'running' and o['counters'].get('versions_done', 0) >= 1, interval=.1)
        # The worker, then the api, restart while the backfill runs.
        state_at_restart = expect(base, backfiller, 'GET', op_path)['state']
        stack.stop_worker()
        stack.start_worker()
        clock.restart_api(stack)
        report['plans_after_restart'].append({'phase': 'backfill', 'want': plan_b['plan_id'], 'got': active_plan(base, operator)})
        try:
            done = await_condition('the backfill finished', lambda: expect(base, backfiller, 'GET', op_path), lambda o: o['state'] not in ('queued', 'running', 'paused', 'cancel_requested'), deadline=600, interval=.2)
        except ConditionTimeout:
            # A blocked step keeps heartbeating. Preserve its goroutine stacks
            # in the redacted worker log before teardown cancels the call.
            if worker_pid := stack.state.get('worker_pid'):
                stack.signal_owned(worker_pid, signal.SIGQUIT)
                # Wait for the dump before teardown; zombies have already exited.
                from local import alive
                end = time.monotonic() + 10
                while alive(worker_pid) and time.monotonic() < end:
                    time.sleep(.05)
            raise
        report['backfill'] = {'state': done['state'], 'counters': done['counters'], 'estimated_versions': estimate['versions'], 'window_records': len(window),
                              'estimate': estimate, 'seconds': round(clock.now() - started, 3), 'state_at_restart': state_at_restart}
        load('backfill', expectations['backfill']['since'])

        # Stop the load, let every Record settle, and read what became of each.
        phase('settle')
        client.halt.set()
        client.join(timeout=150)
        keys = [x['key'] for x in client.records if x.get('receipt_id')]
        try:
            await_condition('every Record settled', lambda: len(settled(base, admin, corpus, keys)), lambda n: n == len(keys), deadline=180, interval=.5)
        except RuntimeError as error:
            report['settle_error'] = str(error)[:300]
        sampler.halt.set()
        sampler.join(timeout=15)
        docs = documents(base, admin, corpus)
        for x in client.records:
            if not x.get('receipt_id'):
                continue
            receipt = expect(base, admin, 'GET', '/v0/ingestion-receipts/' + x['receipt_id'])
            x['record_id'] = receipt.get('record_id')
            versions = docs.get(x['key'], [])
            x['versions'] = len(versions)
            current = [d for d in versions if d['is_current']]
            x['current'] = bool(current)
            x['searchable'] = bool(current) and current[0]['state'] == 'retrieval_ready'
            x['enriched'] = bool(current) and bool(current[0]['steps'].get('enriched_at'))
            if current:
                steps = expect(base, admin, 'GET', f"/v0/admin/documents/{current[0]['version_id']}/timeline")['steps']
                x['segmented_by'] = next((st.get('plugin_version') for st in steps if st['step'] == 'segmented'), None)
            hits = expect(base, admin, 'POST', '/v0/search', {'query': x['word'], 'corpus_ids': [corpus], 'mode': 'lexical', 'limit': 10})['items']
            x['hits'] = [h['record_id'] for h in hits]
            # Accepted after the old version drained and every process followed
            # the switch, and dispatched before the next one: only the phase's
            # active version may run it.
            e = expectations.get(x['phase'])
            if e and e['since'] <= x['accepted'] <= e.get('cutoff', float('inf')):
                x['expected_version'] = e['version']
        report['quarantined'] = len(expect(base, backfiller, 'GET', '/v0/admin/quarantine?corpus_id=' + corpus)['items'])
    finally:
        if client is not None:
            client.halt.set()
            client.abort.set()
            client.join(timeout=15)
            report['records'], report['client_failures'] = client.records, client.failures
        if sampler is not None:
            sampler.halt.set()
            sampler.join(timeout=15)
            report['samples'] = sampler.samples
        for proxy in proxies:
            proxy.close()
        for plugin in plugins:
            sample.stop_plugin(plugin)
        report['api_restarts'] = clock.restarts


def summary(r):
    """Per phase: Records accepted, API samples and latency, and who segmented the Records."""
    out = {}
    for p in r.get('phases', []):
        samples = [s for s in r['samples'] if s['phase'] == p['name']]
        ok = sorted(s['ms'] for s in samples if s['ok'])
        records = [x for x in r['records'] if x.get('phase') == p['name']]
        by = {}
        for x in records:
            by[x.get('segmented_by') or 'none'] = by.get(x.get('segmented_by') or 'none', 0) + 1
        out[p['name']] = {'records': len(records), 'samples': len(samples), 'failed': sum(1 for s in samples if not s['ok']),
                          'failed_during_api_restart': sum(1 for s in samples if not s['ok'] and s['restart']),
                          'p50_ms': statistics.median(ok) if ok else None, 'p95_ms': ok[max(0, int(len(ok) * .95) - 1)] if ok else None,
                          'max_ms': ok[-1] if ok else None, 'segmented_by': by}
    return out


def markdown(r):
    lines = ['# Live plugin upgrade under load', '',
             f"Status: {r['status']}. Client rate {RATE} Records/s, {PHASE_RECORDS} Records per phase after each drain, plugin delay {PLUGIN_DELAY} s, backfill rate {BACKFILL_RATE} Versions/s.", '']
    if r.get('checks'):
        lines += ['| Check | Result |', '| --- | --- |'] + [f"| {c['name']} | {'pass' if c['ok'] else 'FAIL: ' + c['detail']} |" for c in r['checks']] + ['']
    if r.get('phases_summary'):
        lines += ['| Phase | Records | API samples | failed (during api restart) | p50 ms | p95 ms | max ms | segmented by |', '| --- | --- | --- | --- | --- | --- | --- | --- |']
        for name, p in r['phases_summary'].items():
            lines.append(f"| {name} | {p['records']} | {p['samples']} | {p['failed']} ({p['failed_during_api_restart']}) | {p['p50_ms']} | {p['p95_ms']} | {p['max_ms']} | {p['segmented_by']} |")
        lines.append('')
    for d in r.get('drains', []):
        lines.append(f"- Drain {d['name']}: {'inactive' if d.get('inactive') else 'NOT inactive'} after {d.get('seconds')} s; at the switch {d.get('at_switch')}, peak pinned_work {d.get('peak_pinned_work')}, restart while draining: {d.get('restart_while_draining')}.")
    for x in r.get('api_restarts', []):
        lines.append(f"- api restart in {x['phase']}: {x['seconds']} s until ready.")
    if r.get('backfill'):
        b = r['backfill']
        lines.append(f"- Backfill: {b['state']}, counters {b['counters']}, dry run {b['estimated_versions']} Versions, window {b['window_records']} Records, {b['seconds']} s; {b['state_at_restart']} when the worker and the api restarted.")
    if r.get('error'):
        lines += ['', 'Error: ' + r['error']]
    return '\n'.join(lines) + '\n'


def main():
    if platform.system() != 'Linux' or platform.machine() != 'x86_64':
        sys.exit(f'measure-upgrade: unsupported platform {platform.system()}/{platform.machine()}; the stack requires Linux x86_64')
    from local import Stack

    def interrupted(*_):
        raise KeyboardInterrupt()
    signal.signal(signal.SIGTERM, interrupted)
    stack = Stack('quivr-measure-upgrade-' + uuid.uuid4().hex[:10])
    clock = Clock()
    report = {'status': 'failed', 'rate': RATE, 'phase_records': PHASE_RECORDS, 'backfill_rate': BACKFILL_RATE, 'plugin_delay': PLUGIN_DELAY,
              'phases': [], 'expectations': {}, 'drains': [], 'plans_after_restart': [], 'records': [], 'samples': [], 'client_failures': [], 'quarantined': 0}
    passed = False
    try:
        scenario(stack, report, clock)
        report['checks'] = verdict(report)
        passed = all(c['ok'] for c in report['checks'])
        report['status'] = 'passed' if passed else 'failed'
    except BaseException as error:
        report['error'] = type(error).__name__ + ': ' + str(error)[:500]
        raise
    finally:
        report['duration_seconds'] = clock.now()
        report['finished_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='seconds')
        report['phases_summary'] = summary(report)
        (stack.directory / 'upgrade-measurement.json').write_text(json.dumps(report, indent=2))
        (stack.directory / 'upgrade-measurement.md').write_text(markdown(report))
        print('Measurement artifacts:', stack.directory, flush=True)
        try:
            stack.capture()
        finally:
            stack.down(True)
    if not passed:
        sys.exit('measure-upgrade: a check failed; see upgrade-measurement.md')


if __name__ == '__main__':
    main()
