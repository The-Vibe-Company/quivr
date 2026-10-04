"""Durable lead receipts, proposals and daily notification outbox."""
import json
import math
import os
import re
import uuid
import urllib.request

import search_trial


def ingest_usage(store, name, receipt):
    from search_campaign import IDENTIFIER
    fields = {'provider', 'session', 'turn', 'input_tokens', 'output_tokens', 'cached_input_tokens'}
    if not isinstance(receipt, dict) or set(receipt) != fields:
        raise ValueError('usage requires an exact runtime receipt')
    for key in ('provider', 'session', 'turn'):
        if not isinstance(receipt[key], str) or not IDENTIFIER.fullmatch(receipt[key]):
            raise ValueError('invalid usage identity')
    for key in ('input_tokens', 'output_tokens', 'cached_input_tokens'):
        if type(receipt[key]) is not int or not 0 <= receipt[key] <= 2**63 - 1:
            raise ValueError('invalid exact token count')
    if receipt['cached_input_tokens'] > receipt['input_tokens']:
        raise ValueError('cached tokens must be part of input tokens')
    key = search_trial.digest([receipt[k] for k in ('provider', 'session', 'turn')])
    with store.edit(name) as (_, state):
        receipts = state.setdefault('usage', {})
        if key in receipts and receipts[key] != receipt:
            raise ValueError('usage identity has conflicting counts')
        receipts[key] = dict(receipt)
    return usage(store.snapshot(name))


def usage(state):
    receipts = state.get('usage', {})
    if not receipts:
        return None
    return {**{key: sum(item[key] for item in receipts.values())
               for key in ('input_tokens', 'output_tokens', 'cached_input_tokens')}, 'receipts': len(receipts)}


def distribution_step(key, distribution):
    return distribution.get('step', 1 if key in ('dimensions', 'window_chars', 'overlap_chars', 'candidate_count') else .01)


def inside(key, value, distribution):
    if 'choices' in distribution:
        return any(type(value) is type(choice) and value == choice for choice in distribution['choices'])
    low, high = distribution['low'], distribution['high']
    step = distribution_step(key, distribution)
    return low <= value <= high and math.isclose((value - low) / step, round((value - low) / step), abs_tol=1e-8)


def propose(store, name, proposal):
    """Only expand numeric ranges; safety and the measured baseline stay frozen."""
    from search_campaign import IDENTIFIER, specification
    if (not isinstance(proposal, dict) or set(proposal) - {'id', 'config', 'space', 'idea', 'next_plan'}
            or not isinstance(proposal.get('id'), str) or not IDENTIFIER.fullmatch(proposal['id'])
            or not ('config' in proposal or 'idea' in proposal or 'space' in proposal)):
        raise ValueError('proposal requires a stable identity and configuration, range or idea')
    for key in ('idea', 'next_plan'):
        if key in proposal and (not isinstance(proposal[key], str) or not 1 <= len(proposal[key]) <= 2000):
            raise ValueError('lead notes must be bounded text')
    snapshot = store.snapshot(name)
    with store.edit(name) as (db, state):
        existing = state.setdefault('proposals', {}).get(proposal['id'])
        if existing:
            if existing != proposal:
                raise ValueError('proposal identity has conflicting content')
            return existing
        if store.terminal(db, name, *store.lock(db, name)):
            raise ValueError('campaign stopped; use a new campaign for proposals')
        space = dict(state.get('space', snapshot['spec']['space']))
        if 'space' in proposal:
            extension = proposal['space']
            if not isinstance(extension, dict) or not extension or set(extension) - set(space):
                raise ValueError('range revisions can only extend authorized numeric keys')
            for key, dist in extension.items():
                old = space[key]
                if ('choices' in old or not isinstance(dist, dict) or 'choices' in dist
                        or dist.get('low', math.inf) > old['low'] or dist.get('high', -math.inf) < old['high']
                        or distribution_step(key, dist) != distribution_step(key, old)):
                    raise ValueError('range revisions must preserve the grid and expand bounds')
                step = distribution_step(key, old)
                if not math.isclose((old['low'] - dist['low']) / step, round((old['low'] - dist['low']) / step), abs_tol=1e-8):
                    raise ValueError('range revision must preserve the original grid')
            space.update(extension)
            specification({**snapshot['spec'], 'space': space})
        if 'config' in proposal:
            config = search_trial.configuration(proposal['config'])
            baseline = snapshot['spec']['policy']['baseline']
            for key, value in config.items():
                if (key in space and not inside(key, value, space[key])) or (key not in space and value != baseline[key]):
                    raise ValueError('proposal configuration is outside the authorized search space')
            proposal = {**proposal, 'config': config}
        if 'space' in proposal:
            state.setdefault('space_revisions', []).append({'proposal': proposal['id'], 'space': space})
            state['space'] = space
        state['proposals'][proposal['id']] = proposal
        if 'next_plan' in proposal:
            state['next_plan'] = proposal['next_plan']
    return proposal


def enqueue(study, state):
    """Optuna persists the proposal identity before ask, closing the replay gap."""
    seen = {trial.user_attrs.get('proposal_id') for trial in study.trials}
    for identity, proposal in state.get('proposals', {}).items():
        if len(study.trials) >= state['spec']['max_trials']:
            break
        if 'config' in proposal and identity not in seen:
            study.enqueue_trial({k: proposal['config'][k] for k in state.get('space', state['spec']['space'])},
                                user_attrs={'proposal_id': identity})


def export_report(report, sets):
    from search_campaign import aggregate
    if not report:
        return None
    source = {**report, 'work': {str(i): {'receipt': receipt} for i, receipt in enumerate(report.get('evidence', []))}}
    return aggregate(source, sets)


def leaderboard(state):
    """Re-export stored measurements; even legacy/malformed state stays aggregate."""
    from search_campaign import objectives
    points = []
    for number, trial in state['trials'].items():
        report = trial.get('report')
        if not report:
            continue
        clean = export_report(report, state['spec']['policy']['sets'])
        values = objectives(clean, state['spec']['goal']['weights'])
        if values is not None and clean['status'] in ('exploration_finalist', 'rejected'):
            points.append({'number': int(number), 'objectives': values, 'report': clean})
    def dominates(a, b):
        return (a[0] >= b[0] and a[1] <= b[1] and a[2] <= b[2]
                and a != b)
    return sorted([p for p in points if not any(dominates(q['objectives'], p['objectives']) for q in points)],
                  key=lambda p: (-p['objectives'][0], p['objectives'][1], p['objectives'][2], p['number']))


def digest_body(state, available, ledger):
    from search_campaign import objectives
    lines = ['Agent status: implementing — daily search campaign digest',
             f"Campaign {state['spec']['name']} · UTC {available['day']}",
             f"State: {available['stopped'] or ('daily cap paused' if available['paused'] else 'exploring')}",
             'Pareto points (exploration; confirmation required):']
    for point in leaderboard(state)[:10]:
        q, cost, latency = point['objectives']
        baseline_report = {**point['report'], 'aggregate_sets': {
            n: {'candidate': sides['baseline']} for n, sides in point['report']['aggregate_sets'].items()}}
        baseline = objectives(baseline_report, state['spec']['goal']['weights'])
        delta = (f'; deltas quality {q-baseline[0]:+.4g}, cost {cost-baseline[1]:+.4g} USD, latency {latency-baseline[2]:+.4g} ms'
                 if baseline else '; baseline deltas unavailable')
        gates = ', '.join(k + '=' + ('pass' if v['passed'] else 'fail') for k, v in point['report']['gates'].items())
        lines.append(f"Trial {point['number']}: nDCG {q:.4g}, serving ${cost:.4g}, p95 {latency:.4g} ms{delta}; {gates}")
        for receipt in point['report']['evidence']:
            lines.append(f"Evidence result {receipt['result_key']} ({receipt['status']}), run {receipt['run_id'] or 'unknown'}")
    if not leaderboard(state):
        lines.append('No complete Pareto points yet.')
    for kind in ('provider', 'modal'):
        spend = ledger.get(kind, {'charged_usd': 0, 'unknown_usd': 0})
        lines.append(f"{kind}: confirmed ${spend['charged_usd']-spend['unknown_usd']:.6g}, uncertain ${spend['unknown_usd']:.6g}, charged ${spend['charged_usd']:.6g}")
    counts = {}
    for trial in state['trials'].values():
        clean = export_report(trial.get('report'), state['spec']['policy']['sets'])
        status = clean['status'] if clean else 'running'
        counts[status] = counts.get(status, 0) + 1
    lines.append('Trials: ' + json.dumps(counts, sort_keys=True))
    lines.append(f"Held-out reads left: {available['confirmation_reads_left']}")
    tokens = usage(state)
    lines.append('Agent tokens: ' + (json.dumps(tokens, sort_keys=True) if tokens else 'unknown (no exact receipts)'))
    lines.append('Next plan: ' + state.get('next_plan', 'Continue bounded exploration; full-engine confirmation adapter is unavailable.'))
    lines.append('Compute charges cover reserved runner compute, not a full account invoice.')
    return '\n'.join(lines)


class Notifications:
    """Fixed public endpoints, bounded responses; credentials only in headers."""
    def request(self, url, token, payload):
        import embeddings
        request = urllib.request.Request(url, json.dumps(payload).encode(),
            {'Authorization': token, 'Content-Type': 'application/json'}, method='POST')
        with urllib.request.build_opener(embeddings.NoRedirect()).open(request, timeout=30) as response:
            raw = response.read(1024 * 1024 + 1)
        if len(raw) > 1024 * 1024:
            raise ValueError('notification response too large')
        return json.loads(raw)

    def send(self, destination, item, identity):
        if destination == 'linear':
            url = 'https://api.linear.app/graphql'
            token = os.environ['EVAL_LINEAR_TOKEN']
            # Reconcile a previous committed mutation with a lost response.
            found = self.request(url, token, {'query': 'query($id:String!){comment(id:$id){id body issue{id identifier}}}',
                                             'variables': {'id': identity}})
            comment = found.get('data', {}).get('comment')
            if comment:
                issue = comment.get('issue', {})
                if comment.get('body') != item['body'] or item['ticket'] not in (issue.get('id'), issue.get('identifier')):
                    raise ValueError('Linear comment identity mismatch')
                return identity
            payload = {'query': 'mutation($input: CommentCreateInput!){commentCreate(input:$input){success comment{id}}}',
                       'variables': {'input': {'id': identity, 'issueId': item['ticket'], 'body': item['body']}}}
        else:
            url = 'https://slack.com/api/chat.postMessage'
            token = 'Bearer ' + os.environ['EVAL_SLACK_BOT_TOKEN']
            payload = {'channel': item['slack_channel'], 'text': item['body'], 'client_msg_id': identity,
                       'unfurl_links': False, 'unfurl_media': False}
        answer = self.request(url, token, payload)
        if destination == 'linear':
            result = answer.get('data', {}).get('commentCreate', {})
            if answer.get('errors') or result.get('success') is not True:
                raise ValueError('Linear acknowledgement missing')
            receipt = result['comment']['id']
            if receipt != identity:
                raise ValueError('Linear acknowledgement identity mismatch')
        else:
            if answer.get('ok') is not True:
                raise ValueError('Slack acknowledgement missing')
            receipt = answer['ts']
            if not isinstance(receipt, str) or not re.fullmatch(r'[0-9]+\.[0-9]+', receipt):
                raise ValueError('Slack timestamp acknowledgement missing')
        if not isinstance(receipt, str) or not receipt or len(receipt) > 200:
            raise ValueError('notification receipt missing')
        return receipt


def digest(store, name, *, automatic=False):
    """Freeze once per UTC day and retry each destination with a durable ID."""
    snapshot = store.snapshot(name)
    available, ledger = store.availability(name), store.summary(name)
    body = digest_body(snapshot, available, ledger)
    with store.edit(name) as (_, state):
        days = state.setdefault('digests', {})
        days.setdefault(available['day'], {'body': body, 'ticket': snapshot['spec']['ticket'],
            'slack_channel': os.environ.get('EVAL_SLACK_CHANNEL'),
            'deliveries': {d: {'id': str(uuid.uuid4()), 'status': 'pending'} for d in ('linear', 'slack')}})
    # Retry old days too. A completed destination never sends again.
    for day in sorted(store.snapshot(name)['digests']):
        for destination in ('linear', 'slack'):
            owner = uuid.uuid4().hex
            with store.edit(name) as (db, state):
                item = state['digests'][day]
                delivery = item['deliveries'][destination]
                if delivery['status'] == 'delivered':
                    continue
                now = db.execute('SELECT extract(epoch FROM clock_timestamp())').fetchone()[0]
                if delivery.get('expires', 0) > now or (automatic and delivery.get('retry_after', 0) > now):
                    continue
                # Channel may be provisioned after the first wave. Freeze it
                # when first available; retries keep the original destination.
                if not item['slack_channel']:
                    item['slack_channel'] = os.environ.get('EVAL_SLACK_CHANNEL')
                delivery.update(owner=owner, expires=float(now) + 120, status='sending')
            try:
                if destination == 'slack' and not item['slack_channel']:
                    raise ValueError('Slack channel not provisioned')
                receipt = Notifications().send(destination, item, delivery['id'])
            except Exception:
                receipt = None
            with store.edit(name) as (db, state):
                delivery = state['digests'][day]['deliveries'][destination]
                if delivery.get('owner') == owner:
                    now = db.execute('SELECT extract(epoch FROM clock_timestamp())').fetchone()[0]
                    delivery.update(status='delivered' if receipt else 'retry', receipt=receipt, expires=0,
                                    retry_after=float(now) + 60)
    return store.snapshot(name)['digests'][available['day']]['deliveries']


def notify(store, name):
    """Notification failure must not bypass measurement admission or cleanup."""
    try:
        digest(store, name, automatic=True)
    except Exception:
        return False
    return True
