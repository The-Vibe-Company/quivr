#!/usr/bin/env python3
"""Compare an offline fp32 encoder with the remote model using frozen vectors.

Evaluation data and reports belong outside git. This gate measures parity and
encoder latency; demo query_encoding remains a separate deployment measurement.
Run long comparisons through the coordinator's Armada job runner.
"""
import argparse
import base64
import concurrent.futures
import hashlib
import hmac
import json
import math
import multiprocessing
import os
import pathlib
import platform
import signal
import statistics
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

import numpy as np

MODEL = 'google/embeddinggemma-2'
REVISION = '914f7f89142e33e77833254d9c9b90c3cef7303b'
DIMENSIONS = 768
PREFIX = 'task: search result | query: '


class RefuseRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def engine_token(raw, url, audience):
    """Sign the exact plugin request using a privately supplied verification ring."""
    ring = json.loads(os.environ['QUIVR_PLUGIN_SIGNING_KEYS'])
    keys = ring['keys']
    if not isinstance(keys, list) or not 1 <= len(keys) <= 16:
        raise ValueError('invalid signing ring')
    selected = [key for key in keys if key['id'] == ring['active']]
    if len(selected) != 1:
        raise ValueError('invalid signing ring')
    key = selected[0]
    encoded = key['secret']
    secret = base64.b64decode(encoded + '=' * (-len(encoded) % 4), altchars=b'-_', validate=True)
    now = int(time.time())
    if (len(secret) < 32 or now < key.get('not_before', 0)
            or (key.get('not_after', 0) and now >= key['not_after'])):
        raise ValueError('inactive signing key')
    target = urllib.parse.urlsplit(url).path
    header = {'alg': 'HS256', 'typ': 'quivr-engine+jwt', 'kid': key['id']}
    claims = {'aud': audience, 'plugin_id': audience, 'contribution': 'ingestion',
              'method': 'POST', 'target': target, 'iat': now, 'exp': now + 60,
              'body_sha256': hashlib.sha256(raw).hexdigest()}
    def part(value):
        return base64.urlsafe_b64encode(json.dumps(value).encode()).decode().rstrip('=')
    unsigned = part(header) + '.' + part(claims)
    signature = base64.urlsafe_b64encode(hmac.digest(secret, unsigned.encode(), 'sha256')).decode().rstrip('=')
    return unsigned + '.' + signature


def request_json(url, body=None, token='', timeout=10, signing_audience=''):
    deadline = time.monotonic() + timeout
    headers = {'Content-Type': 'application/json'}
    raw = json.dumps(body).encode() if body is not None else None
    if signing_audience:
        token = engine_token(raw, url, signing_audience)
    if token:
        headers['Authorization'] = 'Bearer ' + token
    req = urllib.request.Request(url, data=raw, headers=headers)
    # Do not inherit proxy credentials, and never send bearer auth to a redirect.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), RefuseRedirect())
    with opener.open(req, timeout=timeout) as response:
        raw = bytearray()
        while True:
            if time.monotonic() >= deadline:
                raise TimeoutError('response transfer deadline exceeded')
            # read1 returns after an available chunk, allowing a total-deadline
            # check even when a peer keeps sending within its idle timeout.
            chunk = response.read1(min(64 * 1024, 2 * 1024 * 1024 + 1 - len(raw)))
            if not chunk:
                break
            raw.extend(chunk)
            if len(raw) > 2 * 1024 * 1024:
                raise ValueError('response exceeds limit')
    return json.loads(raw)


def valid_vector(vector):
    if (not isinstance(vector, list) or len(vector) != DIMENSIONS
            or any(type(x) not in (int, float) or not math.isfinite(x) for x in vector)
            or any(abs(x) > 3.4e38 for x in vector) or not any(vector)):
        raise ValueError('invalid embedding vector')
    return vector


def encode(url, text, *, token='', timeout=10, plugin=None):
    if plugin is None:
        body = {'model': MODEL, 'dimensions': DIMENSIONS, 'input': [PREFIX + text]}
        path = '/embeddings'
    else:
        body = {'invocation_id': 'query-encoder-parity-' + uuid.uuid4().hex, 'contribution': 'ingestion',
                'organization_id': 'evaluation', 'configuration': plugin['configuration'],
                'space': plugin['space'], 'query': {'modality': 'text', 'text': text}}
        path = '/v0/contributions/ingestion/embed_query'
    started = time.monotonic()
    audience = plugin['configuration'].get('plugin_id', 'hosted.embed') if plugin else ''
    result = request_json(url.rstrip('/') + path, body, token, timeout, signing_audience=audience)
    elapsed = (time.monotonic() - started) * 1000
    if plugin is not None:
        vector = result.get('vector')
    else:
        data = result.get('data')
        if not isinstance(data, list) or len(data) != 1 or data[0].get('index') != 0:
            raise ValueError('unexpected response order')
        vector = data[0].get('embedding')
    return valid_vector(vector), elapsed


def check_readiness(url, timeout=10):
    parsed = urllib.parse.urlsplit(url)
    base = urllib.parse.urlunsplit((parsed.scheme, parsed.netloc, '/health', '', ''))
    result = request_json(base, timeout=timeout)
    identity = {'status': 'ok', 'model': MODEL, 'model_revision': REVISION[:16],
                'source_revision': REVISION, 'dimensions': DIMENSIONS}
    if not isinstance(result, dict) or any(result.get(k) != v for k, v in identity.items()):
        raise ValueError('local readiness identity mismatch')
    return result


def cosine(a, b):
    dot = sum(x * y for x, y in zip(a, b))
    return dot / math.sqrt(sum(x*x for x in a) * sum(y*y for y in b))


class CandidateRanking:
    """Normalize frozen candidates once; keep scoring outside HTTP latency."""

    def __init__(self, candidates):
        self.ids = np.asarray([item['id'] for item in candidates])
        matrix = np.asarray([item['vector'] for item in candidates], dtype=np.float64)
        norms = np.linalg.norm(matrix, axis=1, keepdims=True)
        if not np.all(np.isfinite(norms)) or np.any(norms == 0):
            raise ValueError('invalid candidate vector norm')
        self.normalized = matrix / norms

    def top10(self, vector, deadline):
        if time.monotonic() >= deadline:
            raise TimeoutError('overall deadline exceeded')
        query = np.asarray(vector, dtype=np.float64)
        scores = self.normalized @ (query / np.linalg.norm(query))
        if not np.all(np.isfinite(scores)):
            raise ValueError('invalid ranking scores')
        if time.monotonic() >= deadline:
            raise TimeoutError('overall deadline exceeded')
        # Stable IDs make exact ties deterministic across backends.
        order = np.lexsort((self.ids, -scores))[:10]
        return self.ids[order].tolist()


def latency(values):
    ordered = sorted(values)
    return {'p50': statistics.median(ordered),
            'p95': ordered[max(0, math.ceil(0.95 * len(ordered)) - 1)]}


def inputs(raw):
    obj = json.loads(raw)
    queries, candidates = obj['queries'], obj['candidates']
    if not isinstance(queries, list) or not 200 <= len(queries) <= 2000:
        raise ValueError('acceptance requires 200 to 2000 queries')
    if not isinstance(candidates, list) or not 10 <= len(candidates) <= 10000:
        raise ValueError('acceptance requires 10 to 10000 frozen document candidates')
    for collection in (queries, candidates):
        ids = set()
        for item in collection:
            if not isinstance(item, dict) or not isinstance(item.get('id'), str) or not item['id'] or item['id'] in ids:
                raise ValueError('unique nonempty IDs required')
            ids.add(item['id'])
    texts = set()
    for item in queries:
        text = item.get('text')
        if not isinstance(text, str) or not text.strip() or '\x00' in text or len((PREFIX + text).encode()) > 2044:
            raise ValueError('invalid query text')
        if text in texts:
            raise ValueError('acceptance queries must be distinct')
        texts.add(text)
    for item in candidates:
        valid_vector(item.get('vector'))
    return obj


def arguments(argv):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=pathlib.Path, required=True)
    parser.add_argument('--output', type=pathlib.Path, required=True)
    parser.add_argument('--local-url', required=True, help='loopback encoder base ending /v1')
    parser.add_argument('--remote-url', required=True, help='remote OpenAI base ending /v1')
    parser.add_argument('--plugin-url', help='optional hosted.embed SDK HTTP origin; input includes plugin.configuration and plugin.space')
    parser.add_argument('--cpu-cores', type=int, required=True, help='allocated service CPU cores, not machine count')
    parser.add_argument('--threads', type=int, required=True)
    parser.add_argument('--concurrency', type=int, default=4)
    parser.add_argument('--timeout', type=float, default=10)
    parser.add_argument('--max-seconds', type=float, default=1800)
    return parser.parse_args(argv)


def main(argv=None):
    args = arguments(argv)
    report = {'passed': False, 'demo_acceptance_measured': False}
    try:
        if not 1 <= args.cpu_cores <= 128 or not 1 <= args.threads <= 32 or not 1 <= args.concurrency <= 32 or not 0 < args.timeout <= 30 or not 0 < args.max_seconds <= 3600:
            raise ValueError('invalid CPU, concurrency or timeout bounds')
        for value in (args.local_url, args.remote_url, args.plugin_url):
            if value is None:
                continue
            parsed = urllib.parse.urlsplit(value)
            if parsed.scheme not in ('http', 'https') or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment:
                raise ValueError('endpoint must be an HTTP(S) URL without credentials, query or fragment')
        if urllib.parse.urlsplit(args.remote_url).scheme != 'https':
            raise ValueError('remote endpoint must use HTTPS')
        if args.input.stat().st_size > 128 * 1024 * 1024:
            raise ValueError('input exceeds 128 MiB')
        raw = args.input.read_bytes()
        data = inputs(raw)
        plugin = data.get('plugin') if args.plugin_url else None
        if args.plugin_url and (not isinstance(plugin, dict) or not isinstance(plugin.get('configuration'), dict) or not isinstance(plugin.get('space'), str)):
            raise ValueError('plugin measurement requires configuration and space')
        started = time.monotonic()
        deadline = started + args.max_seconds
        ready = check_readiness(args.local_url, min(args.timeout, args.max_seconds))
        if 'threads' in ready and ready['threads'] != args.threads:
            raise ValueError('reported CPU threads differ from encoder')
        report.update({'input_sha256': hashlib.sha256(raw).hexdigest(), 'model': MODEL,
                       'source_revision': REVISION, 'dtype': 'float32', 'runtime': ready,
                       'machine': platform.machine(), 'cpu_cores': args.cpu_cores,
                       'threads': args.threads, 'concurrency': args.concurrency,
                       'query_count': len(data['queries']), 'candidate_count': len(data['candidates']),
                       'query_utf8_bytes': latency([len(q['text'].encode()) for q in data['queries']]),
                       'plugin_boundary_measured': plugin is not None})
        token = os.environ.get('EMBED_API_KEY', '')
        def call(url, text, **kwargs):
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError('overall deadline exceeded')
            return encode(url, text, timeout=min(args.timeout, remaining), **kwargs)
        details, sequential, remote_times = [], [], []
        remote_vectors, remote_ranks = [], []
        ranking = CandidateRanking(data['candidates'])
        for n, query in enumerate(data['queries']):
            local, elapsed = call(args.local_url, query['text'])
            remote, remote_elapsed = call(args.remote_url, query['text'], token=token)
            remote_vectors.append(remote)
            remote_ranks.append(ranking.top10(remote, deadline))
            sequential.append(elapsed)
            remote_times.append(remote_elapsed)
            details.append({'query_index': n, 'cosine': cosine(local, remote),
                            'top10_equal': ranking.top10(local, deadline) == remote_ranks[n]})
        # executor.map bounds outstanding work instead of materializing 2000 tasks;
        # each wave contains exactly the intended traffic concurrency.
        concurrent_times, plugin_times = [], []
        with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as pool:
            for start in range(0, len(data['queries']), args.concurrency):
                wave = data['queries'][start:start + args.concurrency]
                futures = [pool.submit(call, args.local_url, q['text']) for q in wave]
                for offset, future in enumerate(futures):
                    vector, elapsed = future.result(timeout=max(0.001, deadline - time.monotonic()))
                    n = start + offset
                    details[n]['concurrent_cosine'] = cosine(vector, remote_vectors[n])
                    details[n]['concurrent_top10_equal'] = ranking.top10(vector, deadline) == remote_ranks[n]
                    concurrent_times.append(elapsed)
                if plugin is not None:
                    futures = [pool.submit(call, args.plugin_url, q['text'], plugin=plugin) for q in wave]
                    for offset, future in enumerate(futures):
                        vector, elapsed = future.result(timeout=max(0.001, deadline - time.monotonic()))
                        n = start + offset
                        details[n]['plugin_cosine'] = cosine(vector, remote_vectors[n])
                        details[n]['plugin_top10_equal'] = ranking.top10(vector, deadline) == remote_ranks[n]
                        plugin_times.append(elapsed)
        report.update({'queries': details, 'local_sequential_ms': latency(sequential),
                       'local_concurrent_ms': latency(concurrent_times), 'remote_ms': latency(remote_times),
                       'first_local_ms': sequential[0], 'elapsed_seconds': time.monotonic() - started,
                       'minimum_cosine': min(d[k] for d in details for k in d if k.endswith('cosine')),
                       'top10_mismatches': sum(not d[k] for d in details for k in d if k.endswith('top10_equal'))})
        if plugin_times:
            report['plugin_concurrent_ms'] = latency(plugin_times)
        report['passed'] = (report['minimum_cosine'] >= 0.999 and report['top10_mismatches'] == 0
                            and report['local_sequential_ms']['p95'] < 100
                            and report['local_concurrent_ms']['p95'] < 100
                            and (not plugin_times or report['plugin_concurrent_ms']['p95'] < 100))
    except Exception:
        # Avoid provider payloads, input text, URLs or auth material in diagnostics.
        report['error'] = 'comparison failed or evidence is incomplete; check input, readiness and endpoint availability'
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2, allow_nan=False) + '\n')
    if not report['passed']:
        print('Query encoder gate failed; see the private report.', file=sys.stderr)
    return 0 if report['passed'] else 1


def comparison_worker(argv):
    raise SystemExit(main(argv))


def stop_worker(worker):
    if worker.is_alive():
        worker.terminate()
        worker.join(1)
    if worker.is_alive():
        worker.kill()
        worker.join(1)


def run_bounded(argv=None):
    argv = list(sys.argv[1:] if argv is None else argv)
    args = arguments(argv)
    if not 0 < args.max_seconds <= 3600:
        return main(argv)  # Emit the same sanitized invalid-input report.
    # Socket idle timeouts do not bound DNS, headers or executor shutdown.
    # Isolate the whole comparison so the parent can stop all request threads
    # at the job deadline without adding IPC overhead to measured HTTP calls.
    worker = multiprocessing.get_context('spawn').Process(
        target=comparison_worker, args=(argv,))
    worker.start()

    def interrupted(_signum, _frame):
        raise KeyboardInterrupt

    previous = {s: signal.signal(s, interrupted) for s in (signal.SIGTERM, signal.SIGINT)}
    try:
        worker.join(args.max_seconds)
        if not worker.is_alive():
            return worker.exitcode or 0
    except KeyboardInterrupt:
        pass
    finally:
        stop_worker(worker)
        for s, handler in previous.items():
            signal.signal(s, handler)
    report = {'passed': False, 'demo_acceptance_measured': False,
              'error': 'comparison exceeded its overall deadline or was interrupted'}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + '\n')
    print('Query encoder gate failed; see the private report.', file=sys.stderr)
    return 1


if __name__ == '__main__':
    sys.exit(run_bounded())
