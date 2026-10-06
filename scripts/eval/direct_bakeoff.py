"""Developer-local dense embedding comparison, ported from the 2026-10-03 tool.

Uses repository public samples and scoring, exact cosine and best document piece.
No engine or vector database is involved. Provider attempts share a fail-closed
input-token/USD budget; credentials are read only from the environment.
"""
import argparse
import contextlib
import datetime
import email.utils
import http.client
import json
import hashlib
import socket
import subprocess
import os
import pathlib
import math
import re
import random
import time
import threading
import urllib.error
import urllib.parse
import urllib.request

import embeddings
import public_sets
import scoring
import trec

import ci_guard

ROOT = pathlib.Path(__file__).resolve().parents[2]
BASELINE = 'multilingual-e5-small (current)'
E5_MODEL = 'intfloat/multilingual-e5-small'
E5_REVISION = '614241f622f53c4eeff9890bdc4f31cfecc418b3'
# Historical USD / million input tokens, 2026-10-03; override when prices change.
PRICES = {'Cohere-Embed-V5-Pro': .12, 'Cohere-Embed-V5-Fast': .08, 'text-embedding-3-large': .13}
HOSTED_DIMENSIONS = {'Cohere-Embed-V5-Pro': 2048, 'Cohere-Embed-V5-Fast': 2048, 'text-embedding-3-large': 3072}
MODELS = [BASELINE, 'Cohere-Embed-V5-Fast', 'Cohere-Embed-V5-Pro', 'text-embedding-3-large',
          'Cohere-Embed-V5-Pro-1024']


def reference_identity():
    """Invalidate cached scores when the pinned encoder, sample or scoring changes."""
    digest = hashlib.sha256()
    for module in (__file__, public_sets.__file__, trec.__file__, scoring.__file__):
        digest.update(pathlib.Path(module).read_bytes())
    return digest.hexdigest()


class OpenAI:
    """Self-hosted endpoint using the hosted.embed plugin's configuration."""
    def __init__(self, config, budget, set_name, label):
        fields = {'plugin_id', 'format', 'base_url', 'auth', 'model', 'dimensions', 'send_dimensions',
                  'metric', 'model_revision', 'plugin_version', 'query_prefix', 'document_prefix',
                  'query_input_type', 'document_input_type', 'max_tokens_per_segment', 'overlap',
                  'batch_size', 'max_batch_tokens', 'request_timeout_ms', 'call_budget_ms',
                  'max_concurrent_requests', 'max_retries', 'usd_per_million_tokens'}
        if not isinstance(config, dict) or set(config) - fields:
            raise ValueError('configuration must use declared hosted.embed fields only')
        operational = {'request_timeout_ms', 'call_budget_ms', 'max_concurrent_requests', 'max_retries', 'max_batch_tokens'}
        if config.get('metric', 'cosine') != 'cosine' or set(config) & operational:
            raise ValueError('direct benchmark requires cosine and omits plugin operational controls')
        target = urllib.parse.urlsplit(config.get('base_url', ''))
        if (config.get('format') != 'openai' or config.get('auth') != 'none'
                or target.scheme not in ('http', 'https') or not target.netloc
                or target.username or target.password or target.query or target.fragment
                or target.scheme == 'http' and target.hostname not in ('localhost', '127.0.0.1', '::1')):
            raise ValueError('self-hosted configuration requires OpenAI format, auth none and HTTPS or loopback')
        if (not isinstance(config.get('model'), str) or not config['model']
                or type(config.get('dimensions')) is not int or not 1 <= config['dimensions'] <= 4096
                or type(config.get('batch_size', 16)) is not int or not 1 <= config.get('batch_size', 16) <= 32
                or any(not isinstance(config.get(k, ''), str) for k in ('query_prefix', 'document_prefix'))):
            raise ValueError('invalid self-hosted model, dimensions, batch size or prefixes')
        self.config, self.budget, self.set_name, self.label = config, budget, set_name, label
        self.opener = urllib.request.build_opener(embeddings.NoRedirect())

    def embed(self, texts, mode):
        vectors = []
        batch = self.config.get('batch_size', 16)
        prefix = self.config.get('query_prefix' if mode == 'query' else 'document_prefix', '')
        for start in range(0, len(texts), batch):
            chunk = [prefix + text for text in texts[start:start + batch]]
            # Zero provider price does not mean free serving. Modal resource costs
            # are attached separately, with actual token counts when available.
            call = self.budget.reserve(self.label, self.set_name, mode,
                                       sum(len(text.encode('utf-8')) + 8 for text in chunk), 0)
            body = {'model': self.config['model'], 'input': chunk}
            if self.config.get('send_dimensions', True):
                body['dimensions'] = self.config['dimensions']
            request = urllib.request.Request(self.config['base_url'].rstrip('/') + '/embeddings',
                                             data=json.dumps(body).encode(), method='POST',
                                             headers={'Content-Type': 'application/json'})
            try:
                with self.opener.open(request, timeout=120) as response:
                    raw = response.read(embeddings.MAX_RESPONSE_BYTES + 1)
                if len(raw) > embeddings.MAX_RESPONSE_BYTES:
                    raise ValueError('response size')
                result = json.loads(raw)
                self.budget.settle(call, result.get('usage', {}).get('prompt_tokens'))
                if not isinstance(result['data'], list) or any(type(item['index']) is not int for item in result['data']):
                    raise ValueError('indices')
                ordered = sorted(result['data'], key=lambda item: item['index'])
                if [item['index'] for item in ordered] != list(range(len(chunk))):
                    raise ValueError('indices')
                received = [item['embedding'] for item in ordered]
                if any(len(v) != self.config['dimensions'] or
                       any(type(x) not in (int, float) or not math.isfinite(x) for x in v) or
                       not any(x != 0 for x in v) for v in received):
                    raise ValueError('vectors')
                vectors.extend(received)
            except embeddings.BudgetExceeded:
                raise
            except urllib.error.HTTPError as error:
                code = error.code
                error.close()
                raise RuntimeError(f'self-hosted HTTP {code}') from None
            except (urllib.error.URLError, TimeoutError):
                raise RuntimeError('self-hosted transport failed') from None
            except (ValueError, KeyError, TypeError, AttributeError):
                raise RuntimeError('invalid self-hosted embeddings response') from None
        return vectors


class DocumentAdmission:
    """Shared request admission: halve on throttling, recover one slot slowly."""
    def __init__(self, maximum=4):
        self.maximum = self.limit = maximum
        self.active = self.clean = self.generation = 0
        self.condition = threading.Condition()

    @contextlib.contextmanager
    def request(self):
        with self.condition:
            self.condition.wait_for(lambda: self.active < self.limit)
            self.active += 1
            generation = self.generation
        try:
            yield
        except urllib.error.HTTPError as error:
            with self.condition:
                self.clean = 0
                if error.code == 429:
                    self.limit = max(1, self.limit // 2)
                    self.generation += 1
            raise
        except BaseException:
            with self.condition:
                self.clean = 0
            raise
        else:
            with self.condition:
                # A success already in flight when throttling happened is
                # not evidence that the reduced rate can safely grow.
                if generation == self.generation:
                    self.clean += 1
                    if self.clean >= 16 * self.limit and self.limit < self.maximum:
                        self.limit += 1
                        self.clean = 0
        finally:
            with self.condition:
                self.active -= 1
                self.condition.notify_all()


class Hosted:
    def __init__(self, endpoint, key, budget, set_name, prices=None):
        target = urllib.parse.urlsplit(endpoint)
        if (target.scheme != 'https' or not target.netloc or target.username or target.password
                or target.query or target.fragment):
            raise ValueError('AZURE_FOUNDRY_ENDPOINT must be HTTPS without credentials, query or fragment')
        self.endpoint, self.key = endpoint.rstrip('/'), key
        self.budget, self.set_name = budget, set_name
        self.prices = PRICES if prices is None else prices
        self.opener = urllib.request.build_opener(embeddings.NoRedirect())
        # Shallow task copies retain this limiter across all document batches.
        self.documents = DocumentAdmission()
        self.provider_gate = None
        self.blocked_seconds = self.http_seconds = self.service_seconds = 0.
        self.ledger_seconds = self.backoff_seconds = self.admission_seconds = 0.
        self.retry_attempts = 0

    @contextlib.contextmanager
    def blocked(self, phase=None):
        started = time.monotonic()
        try:
            yield
        finally:
            elapsed = time.monotonic() - started
            self.blocked_seconds += elapsed
            if phase:
                setattr(self, phase + '_seconds', getattr(self, phase + '_seconds') + elapsed)

    def read(self, request, mode):
        admission = (self.provider_gate.request() if self.provider_gate else
                     self.documents.request() if mode == 'document' else contextlib.nullcontext())
        with self.blocked():
            admitted, http_elapsed = time.monotonic(), 0.
            try:
                with admission:
                    started = time.monotonic()
                    try:
                        with self.opener.open(request, timeout=120) as response:
                            raw = response.read(embeddings.MAX_RESPONSE_BYTES + 1)
                        self.service_seconds += time.monotonic() - started
                        return raw
                    finally:
                        http_elapsed = time.monotonic() - started
                        self.http_seconds += http_elapsed
            finally:
                self.admission_seconds += time.monotonic() - admitted - http_elapsed

    def post(self, path, body, texts, label, model, mode):
        # The shared gate's supported byte/subword bound, including special tokens.
        tokens = embeddings.estimate_tokens(texts)
        for attempt in range(8):
            self.retry_attempts += int(attempt > 0)
            with self.blocked('ledger'):
                call = self.budget.reserve(label, self.set_name, mode, tokens, self.prices[model])
            request = urllib.request.Request(self.endpoint + path, data=json.dumps(body).encode(),
                                             headers={'Content-Type': 'application/json', 'api-key': self.key}, method='POST')
            try:
                raw = self.read(request, mode)
                if len(raw) > embeddings.MAX_RESPONSE_BYTES:
                    raise RuntimeError('provider response exceeds size limit')
                try:
                    result = json.loads(raw)
                    used = (result.get('meta', {}).get('billed_units', {}).get('input_tokens') if model.startswith('Cohere')
                            else result.get('usage', {}).get('prompt_tokens'))
                except (ValueError, KeyError, TypeError, AttributeError):
                    raise RuntimeError('invalid provider response') from None
                if type(used) is not int or used < 0:
                    raise RuntimeError('provider omitted confirmed usage; measurement rejected')
                with self.blocked('ledger'):
                    self.budget.settle(call, used)
                return result
            except urllib.error.HTTPError as error:
                code = error.code
                retry_after = error.headers.get('Retry-After', '')
                error.close()
                # This hosted adapter's rate-limit rejection is unbilled.
                # Do not infer the billing outcome of other error statuses.
                if code == 429:
                    with self.blocked('ledger'):
                        self.budget.settle(call, 0)
                if code not in (429, 500, 502, 503, 504) or attempt == 7:
                    raise RuntimeError(f'provider HTTP {code}') from None
                try:
                    delay = (float(retry_after) if retry_after.isdigit() else
                             email.utils.parsedate_to_datetime(retry_after).timestamp() - time.time())
                except (ValueError, TypeError, OverflowError):
                    delay = 0
            except (urllib.error.URLError, TimeoutError, ConnectionError, http.client.IncompleteRead):
                if attempt == 7:
                    raise RuntimeError('provider transport failed after 8 attempts') from None
                delay = 0
            # Unknown attempts stay reserved; retries must reserve again.
            with self.blocked('backoff'):
                time.sleep(min(60, max(2 ** attempt, delay) + random.uniform(0, 1)))
        raise RuntimeError('provider attempts exhausted')

    def embed(self, model, texts, mode, batch=None, dimensions=None, label=None):
        cohere = model.startswith('Cohere')
        batch = batch or (64 if cohere else 128)
        vectors = []
        for start in range(0, len(texts), batch):
            chunk = texts[start:start + batch]
            if cohere:
                body = {'model': model, 'texts': chunk, 'embedding_types': ['float'],
                        'input_type': 'search_query' if mode == 'query' else 'search_document'}
                if dimensions is not None:
                    body['output_dimension'] = dimensions
                path = '/providers/cohere/v2/embed'
            else:
                body, path = {'model': model, 'input': chunk}, '/openai/v1/embeddings'
                if dimensions is not None:
                    body['dimensions'] = dimensions
            result = self.post(path, body, chunk, label or model, model, mode)
            try:
                if cohere:
                    received = result['embeddings']['float']
                else:
                    ordered = sorted(result['data'], key=lambda item: item['index'])
                    if [item['index'] for item in ordered] != list(range(len(chunk))):
                        raise ValueError('invalid indices')
                    received = [item['embedding'] for item in ordered]
                if len(received) != len(chunk):
                    raise ValueError('invalid count')
                expected_dimensions = dimensions if dimensions is not None else HOSTED_DIMENSIONS[model]
                if any(len(vector) != expected_dimensions for vector in received):
                    raise ValueError('invalid dimensions')
                vectors.extend(received)
            except (KeyError, TypeError, ValueError):
                raise RuntimeError('invalid provider embeddings') from None
        return vectors


class E5:
    def __init__(self):
        self.model = None

    def embed(self, texts, mode):
        from sentence_transformers import SentenceTransformer
        if self.model is None:
            self.model = SentenceTransformer(E5_MODEL, revision=E5_REVISION, device='cpu')
        prefix = 'query: ' if mode == 'query' else 'passage: '
        return self.model.encode([prefix + text for text in texts], batch_size=64,
                                 normalize_embeddings=True, show_progress_bar=False)


def split_documents(docs, width, overlap=200):
    """Reference character windows (~450/~1500 tokens), retaining empty docs."""
    if width <= overlap or overlap < 0:
        raise ValueError('window width must exceed non-negative overlap')
    pieces, owners = [], []
    for owner, text in enumerate(docs):
        for start in range(0, max(len(text), 1), width - overlap):
            pieces.append(text[start:start + width])
            owners.append(owner)
            if start + width >= len(text):
                break
    return pieces, owners


def normalize(vectors):
    import numpy as np
    matrix = np.asarray(vectors, dtype=np.float32)
    if matrix.ndim != 2 or not np.isfinite(matrix).all():
        raise ValueError('embeddings must be a finite matrix')
    lengths = np.linalg.norm(matrix, axis=1, keepdims=True)
    if not np.isfinite(lengths).all() or (lengths == 0).any():
        raise ValueError('embeddings must have finite nonzero norms')
    return matrix / lengths


def rank(doc_ids, query_ids, query_vectors, piece_vectors, owners):
    """Exact cosine top ten, best piece per document, id order breaks ties."""
    import numpy as np
    queries, pieces = normalize(query_vectors), normalize(piece_vectors)
    if len(queries) != len(query_ids) or len(pieces) != len(owners):
        raise ValueError('embedding counts do not match inputs')
    ranking = {}
    # Bound temporary score memory for long-document samples.
    for start in range(0, len(queries), 32):
        scores = queries[start:start + 32] @ pieces.T
        best = np.full((len(scores), len(doc_ids)), -np.inf, dtype=np.float32)
        np.maximum.at(best.T, owners, scores.T)
        top = np.argsort(-best, axis=1, kind='stable')[:, :10]
        ranking.update({query_ids[start + i]: [doc_ids[j] for j in row] for i, row in enumerate(top)})
    return ranking


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--set', required=True, choices=sorted(public_sets.SETS))
    parser.add_argument('--include-restricted', action='store_true', help='opt into diagnostic-only sets under their restricted licences')
    parser.add_argument('--models', nargs='+', default=None,
                        help='e5 baseline always runs first; include Cohere-Embed-V5-Pro-1024 for reduced dimensions')
    parser.add_argument('--openai-config', action='append', default=[], metavar='LABEL=FILE',
                        help='self-hosted candidate with the same JSON configuration as hosted.embed (auth none)')
    parser.add_argument('--e5-reference', type=pathlib.Path, help='reuse a completed e5 report with matching sample and code')
    parser.add_argument('--query-latency', action='store_true',
                        help='encode queries individually to measure warm query p50/p95, instead of batch throughput')
    parser.add_argument('--max-input-tokens', required=True, type=int)
    parser.add_argument('--max-usd', required=True)
    parser.add_argument('--price', action='append', default=[], metavar='MODEL=USD_PER_MILLION',
                        help='override the dated list-price estimate for a hosted deployment')
    parser.add_argument('--cache', type=pathlib.Path, default=ROOT / '.scratch/eval/cache')
    parser.add_argument('--out', required=True, type=pathlib.Path, help='new JSON file; refuses to overwrite evidence')
    args = parser.parse_args(argv)
    if args.out.exists():
        raise FileExistsError('output exists; choose a new evidence filename')
    if ci_guard.in_ci():
        raise SystemExit('direct comparison runs locally only; CI execution is refused')
    budget = embeddings.Budget(args.max_input_tokens, args.max_usd)
    prices = dict(PRICES)
    import decimal
    for override in args.price:
        try:
            model, value = override.split('=', 1)
            value = decimal.Decimal(value)
            if model not in PRICES or not value.is_finite() or value < 0:
                raise ValueError()
            prices[model] = float(value)
        except (ValueError, decimal.InvalidOperation):
            parser.error('--price requires a supported deployment and finite non-negative USD rate')
    configs = {}
    for selection in args.openai_config:
        label, separator, filename = selection.partition('=')
        if not separator or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]*', label) or label in MODELS or label in configs:
            parser.error('--openai-config requires a unique LABEL=FILE')
        configs[label] = json.loads(pathlib.Path(filename).read_text())
        OpenAI(configs[label], budget, args.set, label)  # fail before any download
    models = args.models if args.models is not None else list(configs) if configs else MODELS[1:4]
    if any(name not in MODELS and name not in configs for name in models):
        parser.error('--models must name hosted deployments or configured self-hosted labels')
    systems = list(dict.fromkeys([BASELINE] + models))
    hosted = None
    if any(name != BASELINE and name not in configs for name in systems):
        if not os.environ.get('AZURE_FOUNDRY_ENDPOINT') or not os.environ.get('AZURE_FOUNDRY_KEY'):
            parser.error('hosted models need AZURE_FOUNDRY_ENDPOINT and AZURE_FOUNDRY_KEY')
        hosted = Hosted(os.environ['AZURE_FOUNDRY_ENDPOINT'], os.environ['AZURE_FOUNDRY_KEY'], budget, args.set, prices)
    directory = public_sets.prepare(args.set, args.cache, include_restricted=args.include_restricted)
    data = trec.load(directory)
    doc_ids, query_ids = sorted(data['corpus']), sorted(data['queries'])
    docs = [((data['corpus'][d]['title'] + '\n') if data['corpus'][d]['title'] else '') + data['corpus'][d]['text'] for d in doc_ids]
    queries = [data['queries'][q] for q in query_ids]
    git_sha = os.environ.get('QUIVR_EVAL_GIT_SHA') or subprocess.run(['git', 'rev-parse', 'HEAD'], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip()
    if not re.fullmatch('[0-9a-f]{40}', git_sha):
        raise ValueError('measurement requires a full git SHA')
    report = {'date': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'status': 'running',
              'source': {'git_sha': git_sha,
                         'plugin_digest': 'sha256:' + hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest(),
                         'machine': socket.gethostname()},
              'set': args.set, 'documents': len(docs), 'queries': len(queries), 'results': {},
              'fingerprint': trec.fingerprint(directory), 'sample': json.loads((directory / 'manifest.json').read_text()),
              'promotion_eligible': public_sets.SETS[args.set]['promotion_eligible'],
              'settings': {'e5_reference_identity': reference_identity(), 'models': systems, 'e5_model': E5_MODEL, 'e5_revision': E5_REVISION, 'e5_window_chars': 1800, 'hosted_window_chars': 6000,
                           'overlap_chars': 200, 'retrieval': 'exact cosine, best piece, top 10',
                           'prices_usd_per_million': prices, 'price_reference_date': '2026-10-03',
                           'scoring': scoring.CONVENTION, 'paired_test': scoring.TEST}}
    if configs:
        report['settings']['openai_configs'] = configs
        report['settings']['self_hosted_execution'] = {'request_timeout_seconds': 120, 'attempts': 1,
                                                     'concurrency': 1, 'segmentation': 'reference character windows',
                                                     'batch_size': 'configured; query latency uses one input'}
    args.out.parent.mkdir(parents=True, exist_ok=True)
    # Exclusive creation protects previously saved results, including concurrent runs.
    with args.out.open('x', encoding='utf-8') as output:
        def save():
            report['budget'] = budget.summary()
            report['by_model'] = {name: budget.summary(model=name) for name in systems if name != BASELINE}
            output.seek(0)
            output.write(json.dumps(report, indent=2, allow_nan=False) + '\n')
            output.truncate()
            output.flush()
        save()
        base, local = None, E5()
        try:
            for name in systems:
                report['active_model'] = name
                save()
                if name == BASELINE and args.e5_reference:
                    reference = json.loads(args.e5_reference.read_text())
                    if (reference.get('status') != 'complete' or reference.get('set') != args.set
                            or reference.get('fingerprint') != report['fingerprint']
                            or reference.get('settings', {}).get('e5_reference_identity') != reference_identity()
                            or BASELINE not in reference.get('results', {})):
                        report['reason'] = {'kind': 'reference_mismatch'}
                        raise ValueError('e5 reference provenance differs')
                    result = reference['results'][BASELINE]
                    base = {'mean': result['mean'], 'per_query': result['per_query']}
                    report['results'][BASELINE] = result
                    report['settings'].setdefault('window_chars', {})[BASELINE] = 1800
                    report['e5_reference'] = {'source': reference['source'], 'fingerprint': reference['fingerprint']}
                    save()
                    continue
                started = time.monotonic()
                width = 1800 if name == BASELINE or configs.get(name, {}).get('model') == E5_MODEL else 6000
                report['settings'].setdefault('window_chars', {})[name] = width
                pieces, owners = split_documents(docs, width)
                compatible = OpenAI(configs[name], budget, args.set, name) if name in configs else None
                def embed(texts, mode):
                    if name == BASELINE:
                        return local.embed(texts, mode)
                    if compatible:
                        return compatible.embed(texts, mode)
                    reduced = name.endswith('-1024')
                    model = name[:-5] if reduced else name
                    return hosted.embed(model, texts, mode, dimensions=1024 if reduced else None, label=name)
                document_vectors = normalize(embed(pieces, 'document'))
                indexed = time.monotonic()
                latencies = []
                if args.query_latency:
                    encoded = []
                    for query in queries:
                        before = time.monotonic()
                        encoded.extend(embed([query], 'query'))
                        latencies.append((time.monotonic() - before) * 1000)
                    query_vectors = normalize(encoded)
                else:
                    query_vectors = normalize(embed(queries, 'query'))
                queried = time.monotonic()
                scores = scoring.score(data['qrels'], rank(doc_ids, query_ids, query_vectors, document_vectors, owners))
                result = {'mean': scores['mean'], 'per_query': scores['per_query'],
                          'duration_seconds': round(time.monotonic() - started, 3), 'dims': int(document_vectors.shape[1]), 'pieces': len(pieces),
                          'index_s': round(indexed - started, 1), 'query_ms': round(1000 * (queried - indexed) / len(queries), 1)}
                if latencies:
                    import numpy as np
                    result['latency_ms'] = {'p50': float(np.percentile(latencies, 50)),
                                            'p95': float(np.percentile(latencies, 95)), 'samples': len(latencies),
                                            'scope': 'single query encoding; first query may include initialization'}
                if name != BASELINE:
                    result['index_usage'] = budget.summary(model=name, phase='document')
                    result['query_usage'] = budget.summary(model=name, phase='query')
                if base is None:
                    base = scores
                else:
                    result['vs_current'] = {metric: scoring.paired(scores['per_query'][metric], base['per_query'][metric])
                                            for metric in ('ndcg@10', 'recall@10')}
                report['results'][name] = result
                print('scoring', len(report['results']), len(systems), flush=True)
                save()
            report['status'] = 'complete'
            report.pop('active_model', None)
        except embeddings.BudgetExceeded:
            report['status'] = 'capped'
            report['reason'] = {'kind': 'token_cap'}
        except Exception as error:
            # Raw dependency/provider errors may contain URLs, input or credentials.
            report['status'] = 'failed'
            report.setdefault('reason', {'kind': 'provider_error', **embeddings.error_identity(error)})
            print('failed', len(report['results']), len(systems), flush=True)
        finally:
            save()
    return 0 if report['status'] == 'complete' else 2


if __name__ == '__main__':
    raise SystemExit(main())
