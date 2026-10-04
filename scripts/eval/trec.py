"""The TREC/BEIR layout every evaluation set uses, public or private (THE-775).

A set is a directory, or a .zip / .tar.gz archive of one, holding:

- ``corpus.jsonl``: one ``{"_id", "title", "text"}`` object per document (``title`` optional);
- ``queries.jsonl``: one ``{"_id", "text"}`` object per query;
- ``qrels.tsv`` (or BEIR's ``qrels/test.tsv``): ``query-id<TAB>corpus-id<TAB>score`` with an
  optional header, or the 4-column TREC form ``qid Q0 docid rel``.

A set given by URL must come with its sha256; a local archive is checked when one is given.
Nothing here needs a third-party package.
"""
import hashlib
import json
import pathlib
import shutil
import tarfile
import urllib.request
import zipfile

QRELS = ['qrels.tsv', 'qrels/test.tsv']


class SetError(ValueError):
    """A set that cannot be used as given; the message names the file and the fix."""


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, 'rb') as f:
        for block in iter(lambda: f.read(1 << 20), b''):
            digest.update(block)
    return digest.hexdigest()


def is_url(source):
    return str(source).startswith(('http://', 'https://'))


def fetch(source, sha256, cache, headers=None):
    """Local path of source, downloaded once into cache/downloads/<sha256>/ when it is a URL.

    The digest is checked after every download and before every reuse, so a changed upstream
    file or a corrupted cache entry is refused instead of silently measured."""
    if is_url(source):
        if not sha256:
            raise SetError(f'{source}: a set given by URL needs its sha256')
        target = pathlib.Path(cache) / 'downloads' / sha256 / (str(source).rstrip('/').rsplit('/', 1)[-1].split('?')[0] or 'set')
        if not target.exists():
            target.parent.mkdir(parents=True, exist_ok=True)
            partial = target.with_name(target.name + '.partial')
            request = urllib.request.Request(str(source), headers=headers or {})
            with urllib.request.urlopen(request, timeout=300) as response, open(partial, 'wb') as out:
                shutil.copyfileobj(response, out, 1 << 20)
            partial.rename(target)
    else:
        target = pathlib.Path(source).expanduser()
        if not target.exists():
            raise SetError(f'{target}: no such file or directory')
        if target.is_dir():
            return target
    if sha256 and (actual := sha256_file(target)) != sha256:
        raise SetError(f'{source}: sha256 {actual} differs from the pinned {sha256}')
    return target


def materialize(path, cache):
    """Directory holding the set: path itself, or the archive extracted under cache/sets/."""
    path = pathlib.Path(path)
    if path.is_dir():
        return find_root(path)
    target = pathlib.Path(cache) / 'sets' / ('archive-' + sha256_file(path)[:16])
    if not target.exists():
        partial = target.with_name(target.name + '.partial')
        shutil.rmtree(partial, ignore_errors=True)
        partial.mkdir(parents=True)
        name = path.name.lower()
        if name.endswith('.zip'):
            with zipfile.ZipFile(path) as z:
                for member in z.namelist():
                    safe_member(partial, member)
                z.extractall(partial)
        elif name.endswith(('.tar.gz', '.tgz', '.tar')):
            with tarfile.open(path) as t:
                t.extractall(partial, filter='data')
        else:
            raise SetError(f'{path}: expected a directory, a .zip or a .tar.gz archive')
        partial.rename(target)
    return find_root(target)


def safe_member(root, member):
    if not (root / member).resolve().is_relative_to(root.resolve()):
        raise SetError(f'archive member {member!r} escapes the set directory')


def find_root(directory):
    """The directory holding corpus.jsonl: the given one or exactly one folder below it."""
    found = sorted(p.parent for p in pathlib.Path(directory).rglob('corpus.jsonl'))
    if len(found) != 1:
        raise SetError(f'{directory}: expected exactly one corpus.jsonl, found {len(found)}')
    return found[0]


def read_jsonl(path):
    with open(path, encoding='utf-8') as f:
        for number, line in enumerate(f, 1):
            if line.strip():
                try:
                    yield number, json.loads(line)
                except json.JSONDecodeError as error:
                    raise SetError(f'{path}:{number}: {error.msg}') from None


def read_qrels(path):
    with open(path, encoding='utf-8') as f:
        return parse_qrels(f, path)


def parse_qrels(lines, path):
    """{query_id: {doc_id: grade}} from 3-column BEIR or 4-column TREC rows; path names errors."""
    qrels = {}
    for number, line in enumerate(lines, 1):
        cols = line.split()
        if not cols:
            continue
        if len(cols) == 4:
            cols = [cols[0], cols[2], cols[3]]
        if len(cols) != 3:
            raise SetError(f'{path}:{number}: expected query-id, corpus-id, score')
        try:
            grade = int(float(cols[2]))
        except ValueError:
            if number == 1:
                continue  # header row
            raise SetError(f'{path}:{number}: score {cols[2]!r} is not a number') from None
        qrels.setdefault(cols[0], {})[cols[1]] = grade
    return qrels


def load(directory):
    """The set as {'corpus': {id: {title, text}}, 'queries': {id: text}, 'qrels': {qid: {doc: grade}}}.

    Every query keeps its judgements; a query with no positive judgement is dropped (it cannot
    be scored) and listed in 'dropped_queries'. A judgement naming an unknown query or document
    is an error: the set would be scored against documents Quivr never saw."""
    directory = pathlib.Path(directory)
    missing = [n for n in ['corpus.jsonl', 'queries.jsonl'] if not (directory / n).is_file()]
    qrels_file = next((directory / n for n in QRELS if (directory / n).is_file()), None)
    if missing or qrels_file is None:
        raise SetError(f'{directory}: missing {", ".join(missing + ([] if qrels_file else ["qrels.tsv"]))}')
    corpus, queries = {}, {}
    for number, row in read_jsonl(directory / 'corpus.jsonl'):
        key = str(row.get('_id', ''))
        if not key or not isinstance(row.get('text'), str):
            raise SetError(f'{directory / "corpus.jsonl"}:{number}: needs "_id" and a "text" string')
        if key in corpus:
            raise SetError(f'{directory / "corpus.jsonl"}:{number}: duplicate _id {key!r}')
        corpus[key] = {'title': row.get('title') or '', 'text': row['text']}
    for number, row in read_jsonl(directory / 'queries.jsonl'):
        key = str(row.get('_id', ''))
        if not key or not isinstance(row.get('text'), str) or not row['text'].strip():
            raise SetError(f'{directory / "queries.jsonl"}:{number}: needs "_id" and a non-empty "text"')
        if key in queries:
            raise SetError(f'{directory / "queries.jsonl"}:{number}: duplicate _id {key!r}')
        queries[key] = row['text']
    qrels = read_qrels(qrels_file)
    for qid, docs in qrels.items():
        if qid not in queries:
            raise SetError(f'{qrels_file}: query {qid!r} is judged but absent from queries.jsonl')
        unknown = sorted(d for d in docs if d not in corpus)
        if unknown:
            raise SetError(f'{qrels_file}: query {qid!r} judges {len(unknown)} document(s) absent from corpus.jsonl, e.g. {unknown[0]!r}')
    scorable = {q for q, docs in qrels.items() if any(g > 0 for g in docs.values())}
    dropped = sorted(q for q in queries if q not in scorable)
    return {'corpus': corpus, 'queries': {q: t for q, t in queries.items() if q in scorable},
            'qrels': {q: qrels[q] for q in sorted(scorable)}, 'dropped_queries': dropped}


def write(directory, corpus, queries, qrels):
    """Write a set in the layout load() reads, in sorted order so equal sets are equal bytes."""
    directory = pathlib.Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    with open(directory / 'corpus.jsonl', 'w', encoding='utf-8') as f:
        for key in sorted(corpus):
            f.write(json.dumps({'_id': key, 'title': corpus[key].get('title', ''), 'text': corpus[key]['text']}, ensure_ascii=False) + '\n')
    with open(directory / 'queries.jsonl', 'w', encoding='utf-8') as f:
        for key in sorted(queries):
            f.write(json.dumps({'_id': key, 'text': queries[key]}, ensure_ascii=False) + '\n')
    with open(directory / 'qrels.tsv', 'w', encoding='utf-8') as f:
        f.write('query-id\tcorpus-id\tscore\n')
        for qid in sorted(qrels):
            for doc in sorted(qrels[qid]):
                f.write(f'{qid}\t{doc}\t{qrels[qid][doc]}\n')


def fingerprint(directory):
    """sha256 of the set's files, so two runs compare only when they measured the same set."""
    directory = pathlib.Path(directory)
    qrels_file = next(directory / n for n in QRELS if (directory / n).is_file())
    digest = hashlib.sha256()
    for path in [directory / 'corpus.jsonl', directory / 'queries.jsonl', qrels_file]:
        digest.update(sha256_file(path).encode())
    return digest.hexdigest()
