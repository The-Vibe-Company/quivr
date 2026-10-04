"""Optional local reranker lifecycle and safe, per-pass measurement accounting."""
import json
import os
import re
import signal
import subprocess
import threading
import time

MAX_RUN_INPUT_TOKENS = 5_000_000
MAX_RUN_PAID_SEARCHES = 150
MAX_SEARCH_INPUT_TOKENS = 3 * 65536
CENTS_PER_TOKEN = .042 * 100 / 1_000_000


class Budget:
    def __init__(self):
        self.lock = threading.Lock()
        self.request_lock = threading.Lock()
        self.used_tokens = 0
        self.actual_tokens = 0
        self.blocked_searches = 0
        self.searches = 0

    def admit(self):
        with self.lock:
            if self.searches >= MAX_RUN_PAID_SEARCHES or self.used_tokens + MAX_SEARCH_INPUT_TOKENS > MAX_RUN_INPUT_TOKENS:
                self.blocked_searches += 1
                raise RuntimeError('run input token budget exhausted')
            self.used_tokens += MAX_SEARCH_INPUT_TOKENS
            self.searches += 1
        self.print()

    def settle(self, path, begin):
        try:
            events = records(path, begin, offset(path), 'deep')
        except OSError:
            events = []
        if len(events) == 1:
            tokens = events[0].get('input_tokens')
            estimated = events[0].get('estimated_tokens')
            if type(tokens) is int and type(estimated) is int and 0 <= estimated <= tokens <= MAX_SEARCH_INPUT_TOKENS:
                with self.lock:
                    if events[0].get('reason') != 'deadline':
                        self.used_tokens -= MAX_SEARCH_INPUT_TOKENS - tokens
                    self.actual_tokens += tokens - estimated
        self.print()

    def summary(self):
        with self.lock:
            return {'max_input_tokens': MAX_RUN_INPUT_TOKENS, 'budgeted_input_tokens': self.used_tokens,
                    'max_paid_searches': MAX_RUN_PAID_SEARCHES, 'admitted_searches': self.searches,
                    'actual_input_tokens': self.actual_tokens, 'reserved_input_tokens': self.used_tokens - self.actual_tokens,
                    'actual_cost_cents': self.actual_tokens * CENTS_PER_TOKEN,
                    'cost_upper_bound_cents': self.used_tokens * CENTS_PER_TOKEN,
                    'blocked_searches': self.blocked_searches}

    def print(self):
        print('[eval] Jev run budget: ' + json.dumps(self.summary(), sort_keys=True), flush=True)


def start(stack, tokenizer, key):
    import ports
    directory = stack.source / 'plugins' / 'jev-rerank'
    python = tokenizer['python']
    subprocess.run([python, '-m', 'pip', 'install', '-q', str(stack.source / 'sdks' / 'python')], check=True)
    subprocess.run([python, '-m', 'pip', 'install', '-q', '--no-deps', str(directory)], check=True)
    port = ports.allocate()
    manifest = directory / 'quivr-plugin.yaml'
    stack.jev_key = key
    stack.state.update(jev_python=python, jev_port=port,
                       jev_pin={'manifest': str(manifest), 'endpoint': f'http://127.0.0.1:{port}',
                                'configuration': {'tokenizer_path': tokenizer['model'], 'candidate_count': 30,
                                                  'trim_tokens': '256', 'ranking': 'noul'}})
    launch(stack)


def launch(stack):
    from connector_plugin import healthy_port
    directory = stack.source / 'plugins' / 'jev-rerank'
    port = stack.state['jev_port']
    python = stack.state['jev_python']
    manifest = stack.state['jev_pin']['manifest']
    env = {**os.environ, 'PYTHONPATH': os.pathsep.join([str(stack.source / 'sdks' / 'python' / 'src'), str(directory)]),
           'QUIVR_PLUGIN_HOST': '127.0.0.1', 'QUIVR_PLUGIN_PORT': str(port), 'QUIVR_PLUGIN_MANIFEST': str(manifest),
           'TYPESAFE_API_KEY': stack.jev_key}
    stack.jev_log = stack.directory / 'jev-rerank-plugin.log'
    with stack.jev_log.open('ab') as log:
        stack.jev_process = subprocess.Popen([python, '-m', 'jev_rerank'], cwd=stack.source, env=env,
                                            stdout=log, stderr=log, start_new_session=True)
    stack.state['jev_plugin_pid'] = stack.jev_process.pid
    stack.save()
    deadline = time.monotonic() + 30
    while not healthy_port(port):
        if stack.jev_process.poll() is not None or time.monotonic() > deadline:
            raise RuntimeError('reranker not healthy; inspect ' + str(stack.jev_log))
        time.sleep(.1)


def configure(stack):
    pin = stack.state.get('jev_pin')
    if pin is None:
        return
    for name in ['config.json', 'worker.json']:
        path = stack.directory / name
        config = json.loads(path.read_text())
        config['plugins'] = [entry for entry in config['plugins']
                             if entry['manifest'] != pin['manifest']] + [pin]
        config.setdefault('retrieval', {}).setdefault('profiles', {}).update(
            default='core.retrieve/default', deep='jev.rerank/deep')
        path.write_text(json.dumps(config))
        path.chmod(0o600)


def stop(stack):
    process = getattr(stack, 'jev_process', None)
    if process is not None and process.poll() is None:
        try:
            os.killpg(process.pid, signal.SIGTERM)
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait(timeout=5)
        except ProcessLookupError:
            pass


def matrix():
    yield 'deep-k30-t256-noul', {'candidate_count': 30, 'trim_tokens': '256', 'ranking': 'noul'}


def restart(stack):
    stop(stack)
    launch(stack)


def select(stack, configuration):
    """Apply a new startup registration on the existing corpus with a cold sidecar cache."""
    stack.stop_processes()
    pin = stack.state['jev_pin']
    stack.state['jev_pin'] = {**pin, 'configuration': {**pin['configuration'], **configuration}}
    stack.save()
    restart(stack)
    stack.config()
    stack.start_processes()


def offset(path):
    return path.stat().st_size if path is not None and path.exists() else 0


def variant(profile):
    if profile == 'deep':
        return {'k': 30, 'trim': 'full', 'ranking': 'noul'}
    match = re.fullmatch(r'deep-k(20|30|50)-t(128|256|full)-(noul|rrf)', profile)
    if match:
        return {'k': int(match[1]), 'trim': match[2], 'ranking': match[3]}
    return None


def records(path, begin, end, profile):
    if path is None:
        return []
    with path.open('rb') as log:
        log.seek(begin)
        lines = log.read(max(0, end - begin)).splitlines()
    result = []
    for line in lines:
        try:
            row = json.loads(line)
        except (ValueError, UnicodeDecodeError):
            continue
        if isinstance(row, dict) and row.get('event') == 'jev_rerank' and row.get('profile') == profile:
            result.append(row)
    return result


def summary(attempts, events, unpaid=False):
    """Usage is authoritative; log totals also capture spend on failed HTTP searches."""
    searches = len(attempts)
    result = {'searches': searches, 'log_records': len(events)}
    for field in ['paid_calls', 'cost_cents']:
        api = [attempt[field] for attempt in attempts if attempt.get(field) is not None]
        logged = [event[field] for event in events if event.get(field) is not None]
        total = max(sum(api), sum(logged)) if api or logged else (0 if unpaid else None)
        result[field] = total
        result[field + '_per_search'] = total / searches if total is not None and searches else None
    for field in ['input_tokens', 'estimated_tokens', 'pairs', 'cache_hits']:
        result[field] = sum(event.get(field, 0) for event in events) if events else None
    result['actual_input_tokens'] = max(0, result['input_tokens'] - result['estimated_tokens']) if events else None
    result['tokens_per_search'] = result['actual_input_tokens'] / searches if events and searches else None
    result['estimated_tokens_per_search'] = result['estimated_tokens'] / searches if events and searches else None
    result['cost_is_upper_bound'] = bool(result['estimated_tokens']) if events else None
    fallbacks = sum(event.get('fallback') is True for event in events)
    result['fallbacks'] = fallbacks if events else None
    result['fallback_rate'] = fallbacks / searches if events and searches else None
    result['cache_hit_rate'] = result['cache_hits'] / result['pairs'] if result['pairs'] else None
    reasons = {}
    for event in events:
        if event.get('fallback') is True:
            reason = event.get('reason') or 'unspecified'
            if reason == 'HTTP 402':
                reason = 'provider refused (payment)'
            known = {'API key not configured', 'tokenizer unavailable', 'request size bound', 'cost bound',
                     'response size bound', 'invalid answer', 'transport failure', 'provider refused (payment)'}
            if not isinstance(reason, str) or not (reason in known or re.fullmatch(r'[a-zA-Z0-9_.-]{1,80}|HTTP [0-9]{3}', reason)):
                reason = 'unclassified'
            reasons[reason] = reasons.get(reason, 0) + 1
    result['fallback_reasons'] = reasons
    return result
