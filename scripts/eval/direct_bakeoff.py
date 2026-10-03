"""Developer-local dense embedding comparison, ported from the 2026-10-03 tool.

Uses repository public samples and scoring, exact cosine and best document piece.
No engine or vector database is involved. Provider attempts share a fail-closed
input-token/USD budget; credentials are read only from the environment.
"""
import argparse
import datetime
import json
import hashlib
import socket
import subprocess
import os
import pathlib
import time
import urllib.error
import urllib.parse
import urllib.request

import embeddings
import public_sets
import scoring
import trec

ROOT = pathlib.Path(__file__).resolve().parents[2]
BASELINE = 'multilingual-e5-small (current)'
E5_MODEL = 'intfloat/multilingual-e5-small'
E5_REVISION = '614241f622f53c4eeff9890bdc4f31cfecc418b3'
# Historical USD / million input tokens, 2026-10-03; override when prices change.
PRICES = {'Cohere-Embed-V5-Pro': .12, 'Cohere-Embed-V5-Fast': .08, 'text-embedding-3-large': .13}
HOSTED_DIMENSIONS = {'Cohere-Embed-V5-Pro': 2048, 'Cohere-Embed-V5-Fast': 2048, 'text-embedding-3-large': 3072}
MODELS = [BASELINE, 'Cohere-Embed-V5-Fast', 'Cohere-Embed-V5-Pro', 'text-embedding-3-large',
          'Cohere-Embed-V5-Pro-1024']


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

    def post(self, path, body, texts, label, model, mode):
        # The shared gate's supported byte/subword bound, including special tokens.
        tokens = sum(len(text.encode('utf-8')) + 8 for text in texts)
        for attempt in range(8):
            call = self.budget.reserve(label, self.set_name, mode, tokens, self.prices[model])
            request = urllib.request.Request(self.endpoint + path, data=json.dumps(body).encode(),
                                             headers={'Content-Type': 'application/json', 'api-key': self.key}, method='POST')
            try:
                with self.opener.open(request, timeout=120) as response:
                    raw = response.read(embeddings.MAX_RESPONSE_BYTES + 1)
                if len(raw) > embeddings.MAX_RESPONSE_BYTES:
                    raise RuntimeError('provider response exceeds size limit')
                try:
                    result = json.loads(raw)
                    used = (result['meta']['billed_units'].get('input_tokens') if model.startswith('Cohere')
                            else result['usage'].get('prompt_tokens'))
                except (ValueError, KeyError, TypeError, AttributeError):
                    raise RuntimeError('invalid provider response') from None
                self.budget.settle(call, used)
                return result
            except urllib.error.HTTPError as error:
                code = error.code
                retry_after = error.headers.get('Retry-After', '')
                error.close()
                if code not in (429, 500, 502, 503, 504) or attempt == 7:
                    raise RuntimeError(f'provider HTTP {code}') from None
                delay = float(retry_after) if retry_after.isdigit() else 0
            except (urllib.error.URLError, TimeoutError):
                if attempt == 7:
                    raise RuntimeError('provider transport failed after 8 attempts') from None
                delay = 0
            # Failed and unknown attempts stay reserved; retries must reserve again.
            time.sleep(min(60, 2 ** attempt + delay))
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
    parser.add_argument('--models', nargs='+', choices=MODELS, default=MODELS[1:4],
                        help='e5 baseline always runs first; include Cohere-Embed-V5-Pro-1024 for reduced dimensions')
    parser.add_argument('--max-input-tokens', required=True, type=int)
    parser.add_argument('--max-usd', required=True)
    parser.add_argument('--price', action='append', default=[], metavar='MODEL=USD_PER_MILLION',
                        help='override the dated list-price estimate for a hosted deployment')
    parser.add_argument('--cache', type=pathlib.Path, default=ROOT / '.scratch/eval/cache')
    parser.add_argument('--out', required=True, type=pathlib.Path, help='new JSON file; refuses to overwrite evidence')
    args = parser.parse_args(argv)
    if args.out.exists():
        raise FileExistsError('output exists; choose a new evidence filename')
    if any(os.environ.get(name, '').lower() not in ('', '0', 'false') for name in ('CI', 'GITHUB_ACTIONS')):
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
    systems = list(dict.fromkeys([BASELINE] + args.models))
    hosted = None
    if any(name != BASELINE for name in systems):
        if not os.environ.get('AZURE_FOUNDRY_ENDPOINT') or not os.environ.get('AZURE_FOUNDRY_KEY'):
            parser.error('hosted models need AZURE_FOUNDRY_ENDPOINT and AZURE_FOUNDRY_KEY')
        hosted = Hosted(os.environ['AZURE_FOUNDRY_ENDPOINT'], os.environ['AZURE_FOUNDRY_KEY'], budget, args.set, prices)
    directory = public_sets.prepare(args.set, args.cache, include_restricted=args.include_restricted)
    data = trec.load(directory)
    doc_ids, query_ids = sorted(data['corpus']), sorted(data['queries'])
    docs = [((data['corpus'][d]['title'] + '\n') if data['corpus'][d]['title'] else '') + data['corpus'][d]['text'] for d in doc_ids]
    queries = [data['queries'][q] for q in query_ids]
    report = {'date': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'status': 'running',
              'source': {'git_sha': subprocess.run(['git', 'rev-parse', 'HEAD'], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip(),
                         'plugin_digest': 'sha256:' + hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest(),
                         'machine': socket.gethostname()},
              'set': args.set, 'documents': len(docs), 'queries': len(queries), 'results': {},
              'fingerprint': trec.fingerprint(directory), 'sample': json.loads((directory / 'manifest.json').read_text()),
              'promotion_eligible': public_sets.SETS[args.set]['promotion_eligible'],
              'settings': {'models': systems, 'e5_model': E5_MODEL, 'e5_revision': E5_REVISION, 'e5_window_chars': 1800, 'hosted_window_chars': 6000,
                           'overlap_chars': 200, 'retrieval': 'exact cosine, best piece, top 10',
                           'prices_usd_per_million': prices, 'price_reference_date': '2026-10-03',
                           'scoring': scoring.CONVENTION, 'paired_test': scoring.TEST}}
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
                started = time.monotonic()
                pieces, owners = split_documents(docs, 1800 if name == BASELINE else 6000)
                def embed(texts, mode):
                    if name == BASELINE:
                        return local.embed(texts, mode)
                    reduced = name.endswith('-1024')
                    model = name[:-5] if reduced else name
                    return hosted.embed(model, texts, mode, dimensions=1024 if reduced else None, label=name)
                document_vectors = normalize(embed(pieces, 'document'))
                indexed = time.monotonic()
                query_vectors = normalize(embed(queries, 'query'))
                queried = time.monotonic()
                scores = scoring.score(data['qrels'], rank(doc_ids, query_ids, query_vectors, document_vectors, owners))
                result = {'mean': scores['mean'], 'per_query': scores['per_query'],
                          'duration_seconds': round(time.monotonic() - started, 3), 'dims': int(document_vectors.shape[1]), 'pieces': len(pieces),
                          'index_s': round(indexed - started, 1), 'query_ms': round(1000 * (queried - indexed) / len(queries), 1)}
                if name != BASELINE:
                    result['index_usage'] = budget.summary(model=name, phase='document')
                    result['query_usage'] = budget.summary(model=name, phase='query')
                if base is None:
                    base = scores
                else:
                    result['vs_current'] = {metric: scoring.paired(scores['per_query'][metric], base['per_query'][metric])
                                            for metric in ('ndcg@10', 'recall@10')}
                report['results'][name] = result
                print(name, json.dumps(result['mean']), flush=True)
                save()
            report['status'] = 'complete'
            report.pop('active_model', None)
        except embeddings.BudgetExceeded:
            report['status'] = 'capped'
        except Exception:
            # Raw dependency/provider errors may contain URLs, input or credentials.
            report['status'] = 'failed'
            print('comparison failed; see partial results and budget in the output file', flush=True)
        finally:
            save()
    return 0 if report['status'] == 'complete' else 2


if __name__ == '__main__':
    raise SystemExit(main())
