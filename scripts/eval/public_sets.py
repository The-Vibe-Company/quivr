"""Public evaluation sets: pinned sources, licences, conversion to the TREC layout and seeded sampling.

Data is downloaded at run time and never redistributed. The default suite permits
commercial-development benchmarking; noncommercial/academic sets require explicit
include_restricted=True and never count toward promotion gates. Sources are pinned
by immutable revision and sha256. Existing samples keep their original fingerprints.
New registry entries and licence evidence live in public_sets.json.
"""
import gzip
import hashlib
import json
import pathlib
import random
import shutil
import zipfile

import trec

ROOT = pathlib.Path(__file__).resolve().parents[2]
# Licence evidence and immutable source/sample pins; no downloaded text is committed.
SETS = json.loads(pathlib.Path(__file__).with_suffix('.json').read_text())
DEFAULT_SETS = tuple(name for name, spec in SETS.items() if spec["tier"] == "default")
RESTRICTED_SETS = tuple(name for name, spec in SETS.items() if spec["tier"] == "restricted")


def names(include_restricted=False):
    """Default promotion suite, optionally plus diagnostic-only restricted sets."""
    return DEFAULT_SETS + (RESTRICTED_SETS if include_restricted else ())


def max_source_bytes():
    """The engine's per-Record text limit; larger documents cannot be ingested and are left out."""
    return json.loads((ROOT / 'plugins/core-ingest/profile.json').read_text())['max_source_bytes']


def size(doc):
    return len(doc.get('title', '').encode()) + len(doc['text'].encode())


def sample(qrels, sizes, n_queries, n_documents, seed, max_bytes, priority=None):
    """Seeded subset: n_queries queries, every judged document they have, then distractors.

    A query is eligible when it has a positive judgement and every positive fits max_bytes.
    Distractors come first from priority (per-query hard negatives listed by the source), then
    uniformly from every other document that fits. Returns the chosen query ids, document ids,
    the restricted qrels and counts for the manifest."""
    rng = random.Random(seed)
    fits = lambda d: d in sizes and sizes[d] <= max_bytes  # noqa: E731
    eligible = sorted(q for q, docs in qrels.items()
                      if any(g > 0 for g in docs.values()) and all(fits(d) for d, g in docs.items() if g > 0))
    chosen = sorted(rng.sample(eligible, min(n_queries, len(eligible))))
    kept = {q: {d: g for d, g in qrels[q].items() if fits(d)} for q in chosen}
    judged = sorted({d for docs in kept.values() for d in docs})
    if len(judged) > n_documents:
        raise ValueError(f'{len(judged)} judged documents for {len(chosen)} queries exceed the {n_documents}-document sample; raise documents')
    taken = set(judged)
    hard = sorted({d for q in chosen for d in (priority or {}).get(q, []) if fits(d)} - taken)
    hard = sorted(rng.sample(hard, min(len(hard), n_documents - len(taken))))
    taken.update(hard)
    pool = sorted(d for d in sizes if fits(d) and d not in taken)
    rest = rng.sample(pool, min(len(pool), n_documents - len(taken)))
    taken.update(rest)
    stats = {'eligible_queries': len(eligible), 'queries': len(chosen), 'documents': len(taken), 'judged_documents': len(judged),
             'listed_negatives': len(hard), 'random_distractors': len(rest),
             'judgements_dropped_absent_or_too_long': sum(len(qrels[q]) - len(kept[q]) for q in chosen)}
    return chosen, sorted(taken), kept, stats


def read_parquet(path):
    import pyarrow.parquet as pq  # runtime conversion only (scripts/eval/requirements.txt)
    return pq.read_table(path).to_pylist()


def parquet(files):
    corpus = {str(r.get('id', r.get('_id'))): {'title': r.get('title') or '', 'text': r['text']}
              for r in read_parquet(files['corpus'])}
    queries = {str(r.get('id', r.get('_id'))): r['text'] for r in read_parquet(files['queries'])}
    qrels = {}
    if files['qrels'].suffix == '.tsv':
        qrels = trec.read_qrels(files['qrels'])
    else:
        for r in read_parquet(files['qrels']):
            qrels.setdefault(str(r['query-id']), {})[str(r['corpus-id'])] = int(r['score'])
    return {'sizes': {k: size(v) for k, v in corpus.items()}, 'texts': lambda ids: {i: corpus[i] for i in ids},
            'queries': queries, 'qrels': qrels, 'priority': None}


def jsonl_gz(path):
    with gzip.open(path, 'rt', encoding='utf-8') as f:
        for line in f:
            if line.strip():
                yield json.loads(line)


def mldr(files):
    # The corpus is 360 M characters: read sizes first, then only the sampled texts.
    sizes = {r['docid']: len(r['text'].encode()) for r in jsonl_gz(files['corpus'])}
    queries, priority = {}, {}
    for r in jsonl_gz(files['queries']):
        queries[r['query_id']] = r['query']
        priority[r['query_id']] = [p['docid'] for p in r.get('negative_passages', [])]
    return {'sizes': sizes, 'texts': lambda ids: {r['docid']: {'title': '', 'text': r['text']} for r in jsonl_gz(files['corpus']) if r['docid'] in ids},
            'queries': queries, 'qrels': trec.read_qrels(files['qrels']), 'priority': priority}


def beir(files):
    with zipfile.ZipFile(files['archive']) as z:
        root = next(n.split('/')[0] for n in z.namelist() if n.endswith('corpus.jsonl'))
        corpus = {}
        for line in z.read(f'{root}/corpus.jsonl').decode().splitlines():
            if line.strip():
                r = json.loads(line)
                corpus[str(r['_id'])] = {'title': r.get('title') or '', 'text': r['text']}
        queries = {str(r['_id']): r['text'] for r in map(json.loads, filter(str.strip, z.read(f'{root}/queries.jsonl').decode().splitlines()))}
        qrels = trec.parse_qrels(z.read(f'{root}/qrels/test.tsv').decode().splitlines(), f'{root}/qrels/test.tsv')
    return {'sizes': {k: size(v) for k, v in corpus.items()}, 'texts': lambda ids: {i: corpus[i] for i in ids},
            'queries': queries, 'qrels': qrels, 'priority': None}


CONVERTERS = {'miracl-fr': parquet, 'mldr-fr': mldr, 'scifact': beir,
              **{name: parquet for name in SETS if name not in ('miracl-fr', 'mldr-fr', 'scifact')}}


def prepare(name, cache, include_restricted=False):
    """Directory of the sampled set, built once per (sources, sample) and reused from the cache."""
    spec = SETS[name]
    if spec['tier'] == 'restricted' and not include_restricted:
        raise ValueError(f'{name}: restricted licence; explicitly use --include-restricted for permitted diagnostic use')
    limit = max_source_bytes()
    # Metadata changes must not reuse a manifest with stale licence or eligibility.
    key = hashlib.sha256(json.dumps({'spec': spec, 'max_bytes': limit, 'layout_version': 3}, sort_keys=True).encode()).hexdigest()[:12]
    target = pathlib.Path(cache) / 'sets' / f'{name}-{key}'
    if (target / 'manifest.json').exists():
        return target
    files = {role: trec.fetch(url, digest, cache) for role, (url, digest) in spec['files'].items()}
    source = CONVERTERS[name](files)
    s = spec['sample']
    chosen, docs, qrels, stats = sample(source['qrels'], source['sizes'], s['queries'], s['documents'], s['seed'], limit, source['priority'])
    partial = target.with_name(target.name + '.partial')
    shutil.rmtree(partial, ignore_errors=True)
    trec.write(partial, source['texts'](set(docs)), {q: source['queries'][q] for q in chosen}, qrels)
    manifest = {'name': name, **{k: spec[k] for k in ['language', 'description', 'licence', 'source', 'version', 'split', 'tier',
                                                        'promotion_eligible', 'licence_checked', 'licence_sources', 'query_type', 'known_issues']},
                'files': {role: {'url': url, 'sha256': digest} for role, (url, digest) in spec['files'].items()},
                'source_counts': {'documents': len(source['sizes']), 'queries': len(source['queries']),
                                  'judgments': sum(len(v) for v in source['qrels'].values())},
                'sample': {**s, 'max_source_bytes': limit, **stats}, 'fingerprint': trec.fingerprint(partial)}
    (partial / 'manifest.json').write_text(json.dumps(manifest, indent=2, ensure_ascii=False))
    partial.rename(target)
    return target
