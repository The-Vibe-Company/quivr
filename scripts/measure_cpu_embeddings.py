#!/usr/bin/env python3
"""Bounded throughput sample for a running CPU embedding HTTP service."""

import argparse
import json
import math
import os
import pathlib
import time
import urllib.error
import urllib.request


DEFAULT_URL = 'http://127.0.0.1:8080'
DEFAULT_TOKENS = 512
DEFAULT_WARMUP = 1
DEFAULT_REQUESTS = 8
DEFAULT_TIMEOUT = 10.0
DEFAULT_MAX_SECONDS = 55.0
MAX_REQUESTS = 120
MAX_TOKENS = 2048


def _url(base, path):
    return base.rstrip('/') + path


def _request_json(url, token, *, method='GET', body=None, timeout=DEFAULT_TIMEOUT):
    headers = {'Authorization': 'Bearer ' + token}
    raw = None
    if body is not None:
        raw = json.dumps(body, separators=(',', ':')).encode('utf-8')
        headers['Content-Type'] = 'application/json'
    request = urllib.request.Request(url, data=raw, headers=headers, method=method)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            status = response.status
            payload = json.loads(response.read())
    except (OSError, ValueError, TypeError, urllib.error.URLError) as exc:
        raise RuntimeError('embedding service request failed') from exc
    if status < 200 or status >= 300:
        raise RuntimeError('embedding service request failed')
    if not isinstance(payload, dict):
        raise RuntimeError('embedding service returned an invalid response')
    return payload


def _health(url, token, timeout):
    body = _request_json(_url(url, '/health'), token, timeout=timeout)
    if (body.get('status') != 'ok' or not isinstance(body.get('model'), str)
            or not isinstance(body.get('revision'), str)
            or type(body.get('dimensions')) is not int or body['dimensions'] <= 0):
        raise RuntimeError('embedding service readiness is invalid')
    # Preserve only metadata supplied by the service; never include request or
    # response vectors in a measurement record.
    return {
        key: body[key] for key in (
            'status', 'model', 'revision', 'model_revision', 'source_revision',
            'dimensions', 'threads', 'backend', 'precision', 'onnxruntime_version',
            'tokenizers_version', 'numpy_version', 'torch_version',
            'sentence_transformers_version', 'transformers_version', 'pillow_version',
        ) if key in body
    }


def _passage(tokens):
    # The shape mirrors hosted.embed's title/body input while keeping the
    # benchmark data synthetic and free of corpus content.
    if tokens < 4:
        raise ValueError('tokens must be at least 4')
    return 'title: CPU measurement | text: ' + ('sample-token ' * (tokens - 4)).strip()


def _cpu_allocation():
    host = os.cpu_count() or 1
    allocated = None
    cgroup_v2 = pathlib.Path('/sys/fs/cgroup/cpu.max')
    try:
        quota, period = cgroup_v2.read_text().split()[:2]
        if quota != 'max' and int(period) > 0:
            allocated = max(1, math.ceil(int(quota) / int(period)))
    except (OSError, ValueError):
        pass
    if allocated is None:
        cgroup_v1_quota = pathlib.Path('/sys/fs/cgroup/cpu/cpu.cfs_quota_us')
        cgroup_v1_period = pathlib.Path('/sys/fs/cgroup/cpu/cpu.cfs_period_us')
        try:
            quota = int(cgroup_v1_quota.read_text())
            period = int(cgroup_v1_period.read_text())
            if quota > 0 and period > 0:
                allocated = max(1, math.ceil(quota / period))
        except (OSError, ValueError):
            pass
    return {'host': host, 'allocated': allocated or host}


def _timed_request(url, token, model, dimensions, passage, timeout):
    body = _request_json(_url(url, '/v1/embeddings'), token, method='POST', body={
        'model': model,
        'input': [passage],
        'dimensions': dimensions,
        'encoding_format': 'float',
    }, timeout=timeout)
    rows = body.get('data')
    if (body.get('model') != model or not isinstance(rows, list) or len(rows) != 1
            or not isinstance(rows[0], dict) or rows[0].get('index') != 0):
        raise RuntimeError('embedding service returned an invalid vector response')


def measure(url, token, *, tokens=DEFAULT_TOKENS, warmup=DEFAULT_WARMUP,
            requests=DEFAULT_REQUESTS, timeout=DEFAULT_TIMEOUT,
            max_seconds=DEFAULT_MAX_SECONDS, batch_size=1):
    if not isinstance(token, str) or not token or not token.isascii() \
            or any(character.isspace() for character in token):
        raise ValueError('EMBED_API_KEY must be a nonempty ASCII bearer token without whitespace')
    if type(tokens) is not int or not 4 <= tokens <= MAX_TOKENS:
        raise ValueError('tokens must be from 4 to 2048')
    if type(warmup) is not int or not 0 <= warmup <= 32:
        raise ValueError('warmup must be from 0 to 32')
    if type(requests) is not int or not 1 <= requests <= MAX_REQUESTS:
        raise ValueError('requests must be from 1 to 120')
    if type(batch_size) is not int or batch_size != 1:
        raise ValueError('batch_size is fixed at 1 for the bounded sample')
    if type(timeout) not in (int, float) or not 0 < timeout <= 10:
        raise ValueError('timeout must be from 0 to 10 seconds')
    if type(max_seconds) not in (int, float) or not 0 < max_seconds < 60:
        raise ValueError('max_seconds must be below 60 seconds')

    health = _health(url, token, timeout)
    passage = _passage(tokens)
    dimensions = health['dimensions']
    warmup_started = time.monotonic()
    for _ in range(warmup):
        _timed_request(url, token, health['model'], dimensions, passage, timeout)
    warmup_elapsed = time.monotonic() - warmup_started

    started = time.monotonic()
    deadline = started + max_seconds
    completed = 0
    while completed < requests and time.monotonic() < deadline:
        remaining = deadline - time.monotonic()
        _timed_request(url, token, health['model'], dimensions, passage,
                       min(timeout, max(0.001, remaining)))
        completed += 1
    elapsed = time.monotonic() - started
    if completed == 0:
        raise RuntimeError('timed sample completed no requests')
    if elapsed >= 60:
        raise RuntimeError('timed sample exceeded one minute')
    return {
        'health': health,
        'cores': _cpu_allocation(),
        'input': {
            'batch_size': batch_size,
            'passages_per_request': 1,
            'assumed_tokens': tokens,
            'utf8_bytes': len(passage.encode('utf-8')),
            'shape': 'title: <title> | text: <body>',
        },
        'warmup': {
            'requests': warmup,
            'elapsed_seconds': round(warmup_elapsed, 6),
        },
        'timed': {
            'requests': completed,
            'elapsed_seconds': round(elapsed, 6),
            'passages_per_second': round(completed / elapsed, 6) if elapsed else None,
            'deadline_seconds': max_seconds,
        },
    }


def _parser():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--url', default=os.environ.get('EMBED_URL', DEFAULT_URL))
    parser.add_argument('--api-key', default=os.environ.get('EMBED_API_KEY'))
    parser.add_argument('--tokens', type=int, default=DEFAULT_TOKENS)
    parser.add_argument('--warmup', type=int, default=DEFAULT_WARMUP)
    parser.add_argument('--requests', type=int, default=DEFAULT_REQUESTS)
    parser.add_argument('--batch-size', type=int, default=1)
    parser.add_argument('--timeout', type=float, default=DEFAULT_TIMEOUT)
    parser.add_argument('--max-seconds', type=float, default=DEFAULT_MAX_SECONDS)
    parser.add_argument('--output', type=pathlib.Path)
    return parser


def main(argv=None):
    args = _parser().parse_args(argv)
    if not args.api_key:
        raise SystemExit('EMBED_API_KEY is required')
    report = measure(args.url, args.api_key, tokens=args.tokens, warmup=args.warmup,
                     requests=args.requests, timeout=args.timeout,
                     max_seconds=args.max_seconds, batch_size=args.batch_size)
    raw = json.dumps(report, indent=2, sort_keys=True) + '\n'
    if args.output is None:
        print(raw, end='')
    else:
        args.output.write_text(raw)
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
