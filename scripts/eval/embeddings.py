"""Configured evaluation owners and a runner-owned, fail-closed embedding gate.

The gate supports text requests to byte/subword OpenAI and Cohere models. It
reserves one token per UTF-8 byte plus eight special tokens per input before
any provider attempt. Only validated usage from a successful response releases
unused reservations. No request text, credential, URL or provider error body
is recorded in accounting.
"""
import copy
import decimal
import http.server
import json
import os
import pathlib
import re
import threading
import urllib.error
import urllib.parse
import urllib.request

MAX_REQUEST_BYTES = 2 << 20
MAX_RESPONSE_BYTES = 32 << 20


class BudgetExceeded(RuntimeError):
    pass


class Budget:
    def __init__(self, max_input_tokens, max_usd):
        if type(max_input_tokens) is not int or max_input_tokens <= 0:
            raise ValueError('max_input_tokens must be a positive integer')
        self.max_usd = decimal.Decimal(str(max_usd))
        if not self.max_usd.is_finite() or self.max_usd <= 0:
            raise ValueError('max_usd must be finite and positive')
        self.max_input_tokens = max_input_tokens
        self.lock = threading.Lock()
        self.stopped = threading.Event()
        self.reason = None
        self.calls = []
        self.blocked = 0
        self.used = 0
        self.cost = decimal.Decimal(0)

    def check(self):
        if self.stopped.is_set():
            raise BudgetExceeded(self.reason or 'embedding campaign stopped')

    def reserve(self, model, set_name, phase, tokens, price):
        rate = decimal.Decimal(str(price)) / 1_000_000
        if type(tokens) is not int or tokens <= 0 or not rate.is_finite() or rate < 0:
            raise ValueError('invalid embedding reservation')
        with self.lock:
            if self.stopped.is_set() or self.used + tokens > self.max_input_tokens or self.cost + tokens * rate > self.max_usd:
                self.blocked += 1
                self.reason = self.reason or 'run embedding input-token or USD budget exhausted'
                self.stopped.set()
                raise BudgetExceeded(self.reason)
            call = {'model': model, 'set': set_name, 'phase': phase, 'reserved': tokens,
                    'charged': tokens, 'confirmed': None, 'rate': rate}
            self.calls.append(call)
            self.used += tokens
            self.cost += tokens * rate
            return call

    def settle(self, call, tokens):
        with self.lock:
            if type(tokens) is int and tokens > call['reserved']:
                # A provider violating the supported tokenizer bound invalidates
                # the estimate. Stop immediately and report the actual charge.
                self.used += tokens - call['charged']
                self.cost += (tokens - call['charged']) * call['rate']
                call.update(charged=tokens, confirmed=tokens)
                self.reason = 'provider usage exceeded conservative input-token reservation'
                self.stopped.set()
                raise BudgetExceeded(self.reason)
            if type(tokens) is int and 0 <= tokens <= call['reserved']:
                self.used += tokens - call['charged']
                self.cost += (tokens - call['charged']) * call['rate']
                call.update(charged=tokens, confirmed=tokens)

    def summary(self, model=None, set_name=None, phase=None):
        with self.lock:
            calls = [c for c in self.calls if (model is None or c['model'] == model)
                     and (set_name is None or c['set'] == set_name) and (phase is None or c['phase'] == phase)]
            confirmed = sum(c['confirmed'] or 0 for c in calls)
            reserved = sum(c['charged'] for c in calls if c['confirmed'] is None)
            cost = sum(c['charged'] * c['rate'] for c in calls)
            actual_cost = sum((c['confirmed'] or 0) * c['rate'] for c in calls)
            return {'max_input_tokens': self.max_input_tokens, 'max_usd': float(self.max_usd),
                    'confirmed_input_tokens': confirmed, 'reserved_input_tokens': reserved,
                    'budgeted_input_tokens': confirmed + reserved,
                    'confirmed_cost_usd': float(actual_cost), 'cost_upper_bound_usd': float(cost),
                    'admitted_calls': len(calls), 'blocked_calls': self.blocked,
                    'stopped': self.stopped.is_set(), 'reason': self.reason}


def load_pin(path):
    """Validate a pin and discard plugin secrets before any engine child starts.

    The operator has already injected these into the separately running plugin.
    The evaluator needs only their names; neither its pin nor its children need
    the values. Clearing them also covers subprocesses used during stack setup.
    """
    path = pathlib.Path(path).resolve()
    selection = json.loads(path.read_text())
    allowed = {'manifest', 'endpoint', 'configuration', 'spaces', 'plugin', 'space', 'secret_names'}
    if not isinstance(selection, dict) or set(selection) - allowed:
        raise ValueError('ingestion config has unknown fields')
    for key in ('manifest', 'endpoint', 'plugin', 'space'):
        if not isinstance(selection.get(key), str) or not selection[key]:
            raise ValueError('ingestion config requires manifest, endpoint, plugin and space')
    if selection['plugin'] == 'core.ingest':
        raise ValueError('evaluation owner must differ from baseline core.ingest')
    manifest = pathlib.Path(selection['manifest'])
    selection['manifest'] = str((path.parent / manifest).resolve())
    if not pathlib.Path(selection['manifest']).is_file():
        raise ValueError('ingestion manifest does not exist')
    endpoint = urllib.parse.urlsplit(selection['endpoint'])
    if endpoint.scheme not in ('http', 'https') or not endpoint.netloc or endpoint.username or endpoint.password or endpoint.query or endpoint.fragment:
        raise ValueError('plugin endpoint must be HTTP(S) without credentials, query or fragment')
    names = selection.get('secret_names', [])
    if not isinstance(names, list) or any(not isinstance(n, str) or not re.fullmatch(r'[A-Z][A-Z0-9_]*', n) for n in names):
        raise ValueError('secret_names must contain environment variable names')
    if any(not os.environ.get(n) for n in names):
        raise ValueError('an ingestion plugin secret environment variable is absent')
    if not isinstance(selection.get('configuration', {}), dict):
        raise ValueError('ingestion configuration must be an object')
    for name in names:
        os.environ.pop(name, None)
    return selection


def install(directory, selection):
    """Install before migration/startup; each new Corpus carries both owners."""
    pin = {k: copy.deepcopy(selection[k]) for k in ('manifest', 'endpoint', 'configuration', 'spaces') if k in selection}
    pin.setdefault('spaces', {selection['space'].rsplit('@', 1)[0]: 'served'})
    for name in ('config.json', 'worker.json'):
        path = pathlib.Path(directory) / name
        config = json.loads(path.read_text())
        config.setdefault('plugins', []).append(pin)
        routing = config.setdefault('ingestion', {'default': 'core.ingest'})
        routing.setdefault('evaluation', {}).setdefault('text/plain', []).append(selection['plugin'])
        path.write_text(json.dumps(config))
        path.chmod(0o600)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


class Gate:
    """Loopback forwarder; the hosted plugin only knows this URL, never a key."""
    def __init__(self, budget, model, format, endpoint, key, price, dimensions, label=None):
        self.budget, self.model, self.format = budget, model, format
        self.price, self.dimensions = price, dimensions
        self.label = label or model
        self.set_name, self.phase = 'startup', 'indexing'
        target = urllib.parse.urlsplit(endpoint)
        if (format not in ('openai', 'cohere') or target.scheme not in ('http', 'https')
                or not target.netloc or target.username or target.password or target.query or target.fragment
                or (target.scheme == 'http' and target.hostname not in ('127.0.0.1', 'localhost', '::1'))):
            raise ValueError('provider endpoint must be HTTPS without credentials, query or fragment')
        self.endpoint, self.key = endpoint.rstrip('/'), key
        self.opener = urllib.request.build_opener(NoRedirect())
        gate = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_POST(self):
                try:
                    size = int(self.headers.get('Content-Length', '0'))
                    if size <= 0 or size > MAX_REQUEST_BYTES:
                        self.answer(400)
                        return
                    body = json.loads(self.rfile.read(size))
                    code, response, retry_after = gate.forward(self.path, body)
                    self.answer(code, response, retry_after)
                except BudgetExceeded:
                    self.answer(402)
                except (ValueError, TypeError, KeyError, UnicodeError):
                    self.answer(400)
                except Exception:
                    # Never copy exception text: it may carry a provider URL,
                    # key, request text or reflected provider error body.
                    self.answer(502)

            def answer(self, code, body=None, retry_after=None):
                raw = body if body is not None else b'{"error":"embedding gate refused request"}'
                try:
                    self.send_response(code)
                    self.send_header('Content-Type', 'application/json')
                    self.send_header('Content-Length', str(len(raw)))
                    if retry_after:
                        self.send_header('Retry-After', retry_after)
                    self.end_headers()
                    self.wfile.write(raw)
                except (BrokenPipeError, ConnectionResetError):
                    pass

        self.server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.server.daemon_threads = False  # close waits for in-flight accounting
        self.thread = threading.Thread(target=self.server.serve_forever, kwargs={'poll_interval': .01}, daemon=True)
        self.url = f'http://127.0.0.1:{self.server.server_port}'

    def __enter__(self):
        self.thread.start()
        return self

    def __exit__(self, *args):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()

    def forward(self, path, body):
        route, inputs, dimension = (('/embeddings', 'input', 'dimensions') if self.format == 'openai'
                                    else ('/embed', 'texts', 'output_dimension'))
        if (path != route or not isinstance(body, dict) or body.get('model') != self.model
                or body.get(dimension) != self.dimensions):
            raise ValueError('request does not match configured model')
        allowed = ({'model', 'input', 'dimensions', 'encoding_format'} if self.format == 'openai'
                   else {'model', 'texts', 'input_type', 'embedding_types', 'output_dimension'})
        if set(body) - allowed:
            raise ValueError('only bounded text embedding requests are supported')
        texts = body.get(inputs)
        if not isinstance(texts, list) or not texts or any(not isinstance(text, str) for text in texts):
            raise ValueError('only text embedding batches are supported')
        # Eight special tokens are the hosted plugin's bound for these models.
        tokens = sum(len(text.encode('utf-8')) + 8 for text in texts)
        call = self.budget.reserve(self.label, self.set_name, self.phase, tokens, self.price)
        request = urllib.request.Request(self.endpoint + route, data=json.dumps(body).encode(),
                                         headers={'Content-Type': 'application/json', 'api-key': self.key}, method='POST')
        try:
            try:
                response = self.opener.open(request, timeout=10)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                code = response.status
                raw = response.read(MAX_RESPONSE_BYTES + 1)
                retry_after = response.headers.get('Retry-After', '')
            if code != 200 or len(raw) > MAX_RESPONSE_BYTES:
                # Failed/unknown attempts remain reserved even for 429/5xx.
                retry_after = retry_after if re.fullmatch(r'[0-9]{1,3}', retry_after) else None
                return code if code != 200 else 502, None, retry_after
            try:
                result = json.loads(raw)
                usage = (result.get('usage', {}).get('prompt_tokens') if self.format == 'openai'
                         else result.get('meta', {}).get('billed_units', {}).get('input_tokens'))
            except (ValueError, AttributeError, TypeError):
                usage = None
            self.budget.settle(call, usage)
            return 200, raw, None
        except BudgetExceeded:
            raise
        except Exception:
            return 502, None, None
