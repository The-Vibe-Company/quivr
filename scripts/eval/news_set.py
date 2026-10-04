"""Build a private news-search set; provider adapters are supplied by the operator.

Live adapters ship in news_providers; offline fakes need no credentials. Articles, generated questions,
judgments and human review rows stay in memory until separately age-encrypted.
"""
from __future__ import annotations

import argparse
import collections
import concurrent.futures
import contextlib
import csv
import dataclasses
import datetime
import hashlib
import hmac
import importlib.util
import io
import itertools
import json
import math
import os
import pathlib
import random
import re
import subprocess
import sys
import tarfile
import time
import unicodedata
import uuid
import urllib.error
from typing import Protocol

KINDS = ('entity', 'event', 'recent', 'paraphrase', 'multi_article', 'no_answer')
SYSTEMS = ('bm25', 'e5_small', 'cohere_pro', 'hybrid')
POOL_DEPTH = 10
# Provider bodies may echo credentials even in code fields. Publish known codes only.
PROVIDER_CODES = frozenset({
    'invalid_request_error', 'invalid_value', 'unsupported_value', 'invalid_api_key',
    'context_length_exceeded', 'model_not_found', 'rate_limit_exceeded', 'insufficient_quota',
    'content_filter', 'content_policy_violation', 'server_error', 'service_unavailable',
    'BadRequest', 'InvalidRequest', 'Unauthorized', 'DeploymentNotFound', 'InternalServerError',
})


def ordered_calls(executor, operation, items):
    """Bound pending work as well as threads; cancel queued work on failure."""
    if executor is None:
        yield from map(operation, items)
        return
    items, pending = iter(items), collections.deque()
    def fill():
        for item in itertools.islice(items, 8 - len(pending)):
            pending.append(executor.submit(operation, item))
    fill()
    try:
        while pending:
            value = pending.popleft().result()
            yield value
            fill()
    finally:
        for future in pending:
            future.cancel()


def progress(phase, count):
    print(f'[news-set] {phase}: {count}', file=sys.stderr, flush=True)
STOPWORDS = set('a au aux avec ce ces dans de des du en et la le les leur par pour que qui un une'.split())


class BuildError(ValueError):
    """A safe diagnostic that contains no input/provider text."""

    def __init__(self, message, *, diagnostic=None):
        super().__init__(message)
        self.diagnostic = diagnostic


class InvalidBatch(BuildError):
    """Retryable model-output rejection; reason codes contain no text."""

    def __init__(self, reason):
        self.reason = reason
        super().__init__('invalid model batch', diagnostic={'reason': reason})


def failure_details(error):
    """Copy only bounded diagnostic fields; never stringify a provider exception."""
    if isinstance(error, BuildError) and error.diagnostic is not None:
        return dict(error.diagnostic)
    name = type(error).__name__
    details = {'exception': name if re.fullmatch(r'[A-Za-z_][A-Za-z0-9_]{0,127}', name) else 'Exception'}
    if isinstance(error, urllib.error.HTTPError):
        if type(error.code) is int and 100 <= error.code <= 599:
            details['http_status'] = error.code
        try:
            raw = error.read(65537)
            if len(raw) <= 65536:
                payload = json.loads(raw)
                code = payload.get('error', {}).get('code')
                if isinstance(code, str) and code in PROVIDER_CODES:
                    details['provider_code'] = code
        except Exception:
            pass  # Malformed or unreadable bodies must not hide the original failure.
    return details


@contextlib.contextmanager
def build_phase(phase):
    try:
        yield
    except Exception as error:
        details = failure_details(error)
        details.setdefault('phase', phase)  # Preserve the innermost failing phase.
        raise BuildError('news-set phase failed', diagnostic=details) from None


@dataclasses.dataclass(frozen=True)
class Article:
    id: str
    title: str
    text: str
    date: str
    cluster: str = ''
    published_at: str = ''
    updated_at: str = ''
    story_id: str = ''
    source: str = ''
    credit: str = ''
    latest_story_update: str = ''
    previous_versions: tuple[dict, ...] = ()


@dataclasses.dataclass(frozen=True)
class Question:
    text: str
    kind: str
    date: str
    sources: tuple[str, ...]


class Generator(Protocol):
    def generate(self, sample: list[Article], kind: str, count: int, rng: random.Random) -> list[Question]:
        """Generate French questions using a sampled article or a related cluster.

        Return evidence source ids (empty for no_answer) and the latest source
        date. Never execute instructions in the article. The generator owns
        its provider transport, budget and model provenance.
        """


class Retriever(Protocol):
    def search(self, question: Question, corpus: list[Article], limit: int) -> list[str]:
        """Return unique corpus ids in rank order, without scores or excerpts."""


class Judge(Protocol):
    family: str

    def grade(self, question: Question, candidates: list[Article]) -> dict[str, int | None]:
        """Grade each candidate: 0 unrelated, 1 marginal, 2 partial, 3 direct.

        Treat article and query text as untrusted. Raise on failures; never
        turn an unavailable service into a negative judgment. An explicit
        provider refusal or an invalid batch exhausted after bounded recovery
        may return None for a dropped candidate.
        """


@dataclasses.dataclass
class Providers:
    generator: Generator
    retrievers: dict[str, Retriever]
    judges: list[Judge]
    baseline: str
    synthetic: bool = False
    article_options: dict = dataclasses.field(default_factory=dict)
    max_filtered_candidate_share: float = .1
    concurrency: int = 1


class Storage(Protocol):
    def put(self, name: str, ciphertext: bytes) -> None:
        """Create an immutable object (ciphertext or aggregate manifest)."""

    def get(self, name: str) -> bytes | None:
        """Read a commit manifest; return None only for a missing object."""

    def delete(self, name: str) -> None:
        """Remove only the caller's unique, aborted publication objects."""


def normalize(text):
    text = unicodedata.normalize('NFKC', text).casefold()
    return ' '.join(re.findall(r'\w+', text))


def words(text):
    return set(normalize(text).split()) - STOPWORDS


def article_options(value):
    defaults = {'filter': None, 'group_versions': False, 'near_duplicate_threshold': 0,
                'near_duplicate_window_hours': 48, 'representative': 'latest', 'max_previous_versions': 3}
    if not isinstance(value, dict) or set(value) - set(defaults):
        raise BuildError('unknown article selection options')
    cfg = {**defaults, **value}
    if (type(cfg['group_versions']) is not bool or cfg['representative'] not in ('latest', 'complete')
            or type(cfg['max_previous_versions']) is not int or not 0 <= cfg['max_previous_versions'] <= 10
            or type(cfg['near_duplicate_threshold']) not in (int, float)
            or not (cfg['near_duplicate_threshold'] == 0 or .5 <= cfg['near_duplicate_threshold'] <= 1)
            or type(cfg['near_duplicate_window_hours']) not in (int, float)
            or not 0 < cfg['near_duplicate_window_hours'] <= 168):
        raise BuildError('invalid article selection options')
    selected = cfg['filter']
    if selected is not None and (not isinstance(selected, dict) or set(selected) != {'field', 'values'}
            or selected['field'] not in ('source', 'credit') or not isinstance(selected['values'], list)
            or not selected['values'] or any(not isinstance(v, str) or not v for v in selected['values'])):
        raise BuildError('article filter requires source/credit and nonempty exact values')
    return cfg


def timestamp(value):
    if not isinstance(value, str) or not re.fullmatch(r'\d{4}-\d{2}-\d{2}(?:[Tt]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:[Zz]|[+-]\d{2}:\d{2}))?', value):
        raise BuildError('article timestamp must include a timezone, or be an ISO date')
    parsed = datetime.datetime.fromisoformat(value.replace('Z', '+00:00').replace('z', '+00:00'))
    return parsed.replace(tzinfo=datetime.timezone.utc) if parsed.tzinfo is None else parsed.astimezone(datetime.timezone.utc)


def group_articles(articles, cfg):
    """Explicit story ids or time-bounded shingle similarity group dispatch versions.

    An inverted shingle index restricts comparisons to overlapping texts. Union
    groups include transitive duplicates; all output/evidence stays private.
    """
    parents = list(range(len(articles)))
    def root(i):
        while parents[i] != i:
            parents[i] = parents[parents[i]]
            i = parents[i]
        return i
    def join(i, j):
        parents[root(i)] = root(j)
    stories, inverted, shingles, dates = {}, collections.defaultdict(list), [], []
    threshold = cfg['near_duplicate_threshold']
    for i, article in enumerate(articles):
        if cfg['group_versions'] and article.story_id:
            if article.story_id in stories:
                join(i, stories[article.story_id])
            stories[article.story_id] = i
        tokens = normalize(article.text).split()
        grams = {tuple(tokens[j:j + 5]) for j in range(max(1, len(tokens) - 4))}
        date = timestamp(article.updated_at)
        if threshold:
            possible = set(j for gram in grams for j in inverted[gram])
            for j in possible:
                if (abs((date - dates[j]).total_seconds()) <= cfg['near_duplicate_window_hours'] * 3600
                        and len(grams & shingles[j]) / len(grams | shingles[j]) >= threshold):
                    join(i, j)
            for gram in grams:
                inverted[gram].append(i)
        shingles.append(grams)
        dates.append(date)
    groups = collections.defaultdict(list)
    for i, article in enumerate(articles):
        groups[root(i)].append(article)
    selected = []
    for group in groups.values():
        newest = lambda a: (a.updated_at, a.id)
        key = newest if cfg['representative'] == 'latest' else lambda a: (len(a.text), *newest(a))
        chosen = max(group, key=key)
        previous = sorted((a for a in group if a.id != chosen.id and a.updated_at <= chosen.updated_at), key=newest)
        previous = previous[-cfg['max_previous_versions']:] if cfg['max_previous_versions'] else []
        context = tuple({'id': a.id, 'title': a.title, 'text': a.text, 'updated_at': a.updated_at} for a in previous)
        selected.append(dataclasses.replace(chosen, previous_versions=context,
                         latest_story_update=max(a.updated_at for a in group),
                         story_id=chosen.story_id or next((a.story_id for a in group if a.story_id), '')))
    progress('selected_articles', len(selected))
    return selected


def read_articles(directory, options=None):
    """Private JSON/JSONL exports, with opt-in credit filtering and version grouping."""
    result, seen, cfg = [], set(), article_options({} if options is None else options)
    try:
        for path in sorted(pathlib.Path(directory).rglob('*')):
            if path.suffix not in ('.json', '.jsonl') or not path.is_file():
                continue
            if path.suffix == '.jsonl':
                rows = [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
            else:
                rows = json.loads(path.read_text())
                rows = rows if isinstance(rows, list) else [rows]
            for row in rows:
                selected = cfg['filter']
                if selected and row.get(selected['field']) not in selected['values']:
                    continue
                if any(not isinstance(row.get(k), str) or not row[k].strip()
                       for k in ('id', 'title', 'text', 'published_at')):
                    raise BuildError('article fields must be nonempty strings')
                if row['id'] in seen:
                    raise BuildError('duplicate article id')
                published = timestamp(row['published_at'])
                updated = timestamp(row.get('updated_at', row['published_at']))
                if updated < published:
                    raise BuildError('article update precedes publication')
                extras = {k: row.get(k, '') for k in ('cluster', 'story_id', 'source', 'credit')}
                if any(not isinstance(v, str) for v in extras.values()):
                    raise BuildError('article grouping and credit fields must be strings')
                result.append(Article(row['id'], row['title'], row['text'], updated.date().isoformat(),
                                      published_at=published.isoformat(), updated_at=updated.isoformat(), **extras))
                seen.add(row['id'])
    except BuildError:
        raise
    except (ValueError, OSError, TypeError, AttributeError, KeyError):
        raise BuildError('invalid article input; check the input schema') from None
    if not result:
        raise BuildError('no articles found after selection')
    return group_articles(result, cfg) if cfg['group_versions'] or cfg['near_duplicate_threshold'] else result


def filtered_candidate_share(value):
    if type(value) not in (int, float) or not 0 < value <= 1:
        raise BuildError('max filtered candidate share must be in (0, 1]')
    return value


def generate(corpus, generator, count, seed, accept_question=None, filter_counts=None, executor=None, accept_questions=None):
    if count < len(KINDS) or len(corpus) < 2:
        raise BuildError('need at least six questions and two articles')
    by_id = {a.id: a for a in corpus}
    clusters = collections.defaultdict(list)
    for article in corpus:
        clusters[article.cluster or article.date[:7]].append(article)
    groups = [group for group in clusters.values() if len(group) >= 2]
    if not groups:
        raise BuildError('multi-article generation needs a cluster with two articles')
    filter_counts = filter_counts if filter_counts is not None else collections.Counter()
    rng, seen, questions, rejected = random.Random(seed), set(), [], collections.Counter()
    titles = [normalize(a.title) for a in corpus if normalize(a.title)]
    for index, kind in enumerate(KINDS):
        target = count // len(KINDS) + (index < count % len(KINDS))
        accepted = []
        attempts, limit = 0, max(10, target * 5)
        while len(accepted) < target and attempts < limit:
            requests, remaining = [], target - len(accepted)
            # Fixed waves make the request sequence independent of worker count.
            while remaining and len(requests) < 8 and attempts < limit:
                if kind == 'multi_article':
                    group = rng.choice(groups)
                    sample = rng.sample(group, min(3, len(group)))
                else:
                    sample = [rng.choice(corpus)]
                size = min(25, remaining)
                requests.append((sample, size, random.Random(rng.getrandbits(64))))
                remaining -= size
                attempts += 1
                filter_counts['generation_batches'] += 1
            def request(item):
                sample, size, request_rng = item
                try:
                    return generator.generate(sample, kind, size, request_rng)
                except InvalidBatch as error:
                    return error
            replies = list(ordered_calls(executor, request, requests))
            valid_questions = []
            for (sample, _, _), generated in zip(requests, replies):
                if isinstance(generated, InvalidBatch):
                    rejected[generated.reason] += 1
                    if generated.reason == 'content_filter':
                        filter_counts['filtered_generation_batches'] += 1
                    continue
                for q in generated:
                    if len(accepted) + len(valid_questions) == target:
                        break
                    if not isinstance(q, Question) or not isinstance(q.text, str) or not q.text.strip() or q.kind != kind:
                        raise BuildError('generator returned an invalid question')
                    key = normalize(q.text)
                    if any(title in key for title in titles):
                        rejected['title_copy'] += 1
                        continue
                    if key in seen:
                        rejected['duplicate'] += 1
                        continue
                    if (not isinstance(q.sources, tuple) or len(set(q.sources)) != len(q.sources)
                            or any(s not in {a.id for a in sample} for s in q.sources)
                            or (kind == 'no_answer' and q.sources)
                            or (kind != 'no_answer' and not q.sources)
                            or (kind == 'multi_article' and len(q.sources) < 2)):
                        raise BuildError('generator evidence does not match its sample')
                    expected_date = max(by_id[s].date for s in q.sources) if q.sources else sample[0].date
                    if q.date != expected_date:
                        raise BuildError('question date must match its source evidence')
                    if kind == 'paraphrase' and any(words(q.text) & words(by_id[s].title + ' ' + by_id[s].text)
                                                   for s in q.sources):
                        rejected['shared_keywords'] += 1
                        continue
                    seen.add(key)
                    valid_questions.append(q)
            decisions = (accept_questions(valid_questions) if accept_questions else
                         [accept_question(q) if accept_question else True for q in valid_questions])
            for q, keep in zip(valid_questions, decisions):
                if not keep:
                    rejected['judge_content_filter'] += 1
                else:
                    accepted.append(q)
            progress('accepted', len(questions) + len(accepted))
        if len(accepted) != target:
            raise BuildError('generator exhausted attempts before filling every question type', diagnostic={
                'reason': 'generation_attempts_exhausted', 'kind': kind,
                'attempts': max(10, target * 5), 'accepted': len(accepted), 'target': target,
                'rejected': dict(rejected)})
        questions.extend(accepted)
        progress('generated', len(questions))
    return questions, dict(rejected)


def judge_pool(questions, corpus, retrievers, judges, max_filtered_candidate_share=.1, filter_counts=None, executor=None):
    threshold = filtered_candidate_share(max_filtered_candidate_share)
    filter_counts = filter_counts if filter_counts is not None else collections.Counter()
    if len(judges) != 3 or len({j.family for j in judges}) != 3:
        raise BuildError('need three judges from distinct families')
    if not retrievers or set(retrievers) - set(SYSTEMS):
        raise BuildError('unsupported retrieval system')
    by_id, rows, rankings = {a.id: a for a in corpus}, [], {}
    prepared = []
    for qi, question in enumerate(questions):
        pool = collections.defaultdict(dict)
        rankings[qi] = {}
        for system, retriever in retrievers.items():
            with build_phase('retrieval'):
                ids = retriever.search(question, corpus, POOL_DEPTH)
                if not isinstance(ids, list) or len(ids) > POOL_DEPTH or len(ids) != len(set(ids)) or any(d not in by_id for d in ids):
                    raise BuildError('retriever returned invalid candidate ids')
            rankings[qi][system] = ids
            for rank, doc in enumerate(ids, 1):
                pool[doc][system] = rank
        if not pool:
            raise BuildError('empty candidate pool')
        candidates = [by_id[d] for d in sorted(pool)]
        prepared.append((question, pool, candidates))
    def grade(item):
        judge, question, candidates = item
        with build_phase('judging'):
            return judge.grade(question, candidates)
    tasks = [(judge, question, candidates) for question, _, candidates in prepared for judge in judges]
    results = iter(ordered_calls(executor, grade, tasks))
    for qi, (question, pool, candidates) in enumerate(prepared):
        votes = [next(results) for judge in judges]
        if any(not isinstance(v, dict) or set(v) != set(pool)
               or any(g is not None and (type(g) is not int or not 0 <= g <= 3) for g in v.values()) for v in votes):
            raise BuildError('judge returned missing or invalid grades')
        filtered = {doc for doc in pool if any(v[doc] is None for v in votes)}
        filter_counts['judged_candidates'] += len(pool)
        filter_counts['filtered_candidates'] += len(filtered)
        if len(filtered) / len(pool) >= threshold:
            filter_counts['dropped_questions'] += 1
            progress('dropped', filter_counts['dropped_questions'])
            del rankings[qi]
            continue
        query_rows = []
        for doc in sorted(set(pool) - filtered):
            grades = [v[doc] for v in votes]
            # With three votes, the median is the majority if one exists;
            # a three-way tie uses the median and is counted in the report.
            query_rows.append({'query': qi, 'doc': doc, 'votes': grades,
                               'grade': sorted(grades)[1], 'ranks': pool[doc]})
        has_positive = any(r['grade'] > 0 for r in query_rows)
        if has_positive == (question.kind == 'no_answer'):
            if filtered and question.kind != 'no_answer':
                filter_counts['dropped_questions'] += 1
                progress('dropped', filter_counts['dropped_questions'])
                del rankings[qi]
                continue
            raise BuildError('answerability conflicts with pooled judgments')
        rows.extend(query_rows)
        if executor is None and ((qi + 1) % 25 == 0 or (len(questions) > 1 and qi + 1 == len(questions))):
            progress('judged', qi + 1)
    return rows, rankings


def agreement(votes):
    """Unweighted Cohen pairs (0/1, 0/2, 1/2) and Fleiss over ordinal grades.

    Undefined kappa (all grades identical) is null, never fabricated as 1.
    """
    n = len(votes)
    if not n:
        return {'cohen_kappa': [None] * 3, 'fleiss_kappa': None}
    def kappa(observed, expected):
        return (observed - expected) / (1 - expected) if expected < 1 else None
    pairs = []
    for a, b in itertools.combinations(range(3), 2):
        ca, cb = collections.Counter(row[a] for row in votes), collections.Counter(row[b] for row in votes)
        observed = sum(row[a] == row[b] for row in votes) / n
        expected = sum(ca[g] * cb[g] for g in range(4)) / n ** 2
        pairs.append(kappa(observed, expected))
    counts = [collections.Counter(row) for row in votes]
    observed = sum(sum(v * (v - 1) for v in c.values()) / 6 for c in counts) / n
    expected = sum((sum(c[g] for c in counts) / (3 * n)) ** 2 for g in range(4))
    return {'cohen_kappa': pairs, 'fleiss_kappa': kappa(observed, expected)}


def split(questions, seed):
    """Largest-remainder 60/40 allocation within type and UTC calendar month."""
    groups = collections.defaultdict(list)
    for i, q in enumerate(questions):
        groups[(q.kind, q.date[:7])].append(i)
    rng, allocations = random.Random(seed), {}
    def allocate(sizes, target):
        counts = {key: size * 3 // 5 for key, size in sizes.items()}
        order = sorted(sizes, key=lambda key: (-(sizes[key] * 3 % 5), key))
        for key in order[:target - sum(counts.values())]:
            counts[key] += 1
        return counts
    type_sizes = collections.Counter(q.kind for q in questions)
    type_counts = allocate(type_sizes, len(questions) * 3 // 5)
    for kind in sorted(type_sizes):
        sizes = {group: len(ids) for group, ids in groups.items() if group[0] == kind}
        allocations.update(allocate(sizes, type_counts[kind]))
    for group in sorted(groups):
        rng.shuffle(groups[group])
    dev, held = [], []
    for group, ids in groups.items():
        dev.extend(ids[:allocations[group]])
        held.extend(ids[allocations[group]:])
    return sorted(dev), sorted(held)


REVIEW_FIELDS = ('query_id', 'doc_id', 'kind', 'date', 'query', 'title', 'text', 'grade', 'human_grade')


def csv_text(rows):
    output = io.StringIO()
    writer = csv.DictWriter(output, REVIEW_FIELDS)
    writer.writeheader()
    def literal(value):
        if isinstance(value, str) and (value.lstrip().startswith(('=', '+', '-', '@'))
                                       or value.startswith(('\t', '\r', '\n'))):
            return "'" + value
        return value
    writer.writerows({key: literal(value) for key, value in row.items()} for row in rows)
    return output.getvalue()


def review_result(original, completed):
    """Validate unchanged sample rows before accepting human grades."""
    try:
        expected = list(csv.DictReader(io.StringIO(original)))
        actual = list(csv.DictReader(io.StringIO(completed)))
        key = lambda row: (row['query_id'], row['doc_id'])
        expected_by_id, actual_by_id = {key(r): r for r in expected}, {key(r): r for r in actual}
        if not expected or len(actual_by_id) != len(actual) or set(expected_by_id) != set(actual_by_id):
            raise BuildError('human review does not match the exported sample')
        reviewed, matches = 0, 0
        for pair, row in actual_by_id.items():
            if set(row) != set(REVIEW_FIELDS) or any(row[k] != expected_by_id[pair][k]
                                                    for k in REVIEW_FIELDS if k != 'human_grade'):
                raise BuildError('human review changed an exported judgment')
            grade = row['human_grade']
            if grade == '':
                continue
            if grade not in ('0', '1', '2', '3'):
                raise BuildError('human grade must be blank or an integer from 0 to 3')
            reviewed += 1
            matches += grade == row['grade']
        return {'status': 'complete' if reviewed == len(expected) else 'pending',
                'sampled': len(expected), 'reviewed': reviewed,
                'agreement_rate': matches / reviewed if reviewed else None}
    except (KeyError, TypeError, csv.Error):
        raise BuildError('invalid human review CSV') from None


def review_sample(rows, questions, corpus, qids, count, seed):
    by_id = {a.id: a for a in corpus}
    groups = collections.defaultdict(list)
    for row in rows:
        groups[(questions[row['query']].kind, row['grade'])].append(row)
    rng, selected = random.Random(seed), []
    for group in sorted(groups):
        rng.shuffle(groups[group])
    # Round robin prevents the most common type/grade from hiding smaller strata.
    queues = [collections.deque(groups[g]) for g in sorted(groups)]
    while queues and len(selected) < count:
        for queue in queues:
            if len(selected) == count:
                break
            selected.append(queue.popleft())
        queues = [q for q in queues if q]
    out = []
    for row in selected:
        q, doc = questions[row['query']], by_id[row['doc']]
        out.append(dict(zip(REVIEW_FIELDS, [qids[row['query']], doc.id, q.kind, q.date,
                                           q.text, doc.title, doc.text, row['grade'], ''])))
    return csv_text(out)


@dataclasses.dataclass
class Build:
    working: dict
    held_out: dict
    review_csv: str
    report: dict
    version: str


def validate_report(report, published=False):
    """Allow only the public aggregate schema, including on review import."""
    import jsonschema
    schema = json.loads(pathlib.Path(__file__).with_name('news-report.schema.json').read_text())
    try:
        jsonschema.Draft202012Validator(schema).validate(report)
    except jsonschema.ValidationError:
        raise BuildError('report violates the aggregate-only schema') from None
    types, split_counts, depth = report['question_types'], report['split'], report['judged_depth']
    human, baseline, saturation = report['human_check'], report['baseline'], report['saturation']
    valid = (report['articles'] >= 2 and min(types.values()) > 0
             and (report['status'] == 'synthetic' or report['questions'] >= 1500)
             and sum(types.values()) == report['questions'] and max(types.values()) - min(types.values()) <= 1
             and sum(split_counts.values()) == report['questions']
             and split_counts['working'] == report['questions'] * 3 // 5
             and sum(depth.values()) == report['questions']
             and all(int(k) > 0 for k in depth)
             and sum(int(k) * v for k, v in depth.items()) == report['judgments']
             and report['hard_negatives'] <= report['judgments']
             and report['three_way_ties'] <= report['judgments']
             and human['sampled'] == min(100, report['judgments'])
             and human['reviewed'] <= human['sampled']
             and (human['agreement_rate'] is None) == (human['reviewed'] == 0)
             and (human['status'] == 'complete') == (human['reviewed'] == human['sampled'] == 100)
             and baseline['queries'] + baseline['no_answer_queries'] == split_counts['working']
             and saturation['scorable_working_queries'] == baseline['queries']
             and saturation['working_queries_perfect_in_all_systems'] <= baseline['queries'])
    if 'content_filter' in report:
        filters = report['content_filter']
        valid = (valid and filters['judged_candidates'] >= report['judgments']
                 and filters['filtered_candidates'] <= filters['judged_candidates']
                 and filters['filtered_generation_batches'] <= filters['generation_batches']
                 and filters['dropped_questions'] == report['rejected'].get('judge_content_filter', 0)
                 and filters['filtered_generation_batches'] == report['rejected'].get('content_filter', 0)
                 and filters['filtered_candidate_share'] == filters['filtered_candidates'] / max(1, filters['judged_candidates'])
                 and filters['filtered_generation_share'] == filters['filtered_generation_batches'] / max(1, filters['generation_batches']))
    numbers = list(baseline['metrics'].values()) + report['agreement']['cohen_kappa']
    numbers += [report['agreement']['fleiss_kappa'], human['agreement_rate']]
    if not valid or any(n is not None and not math.isfinite(n) for n in numbers):
        raise BuildError('report aggregate counts or metrics are inconsistent')
    if published or 'version' in report or 'artifacts' in report:
        if not {'version', 'artifacts'} <= set(report):
            raise BuildError('published report needs a version and all artifact hashes')
        if any(not a['object'].startswith(report['version'] + '-') for a in report['artifacts'].values()):
            raise BuildError('artifact names must match the published version')


def validate_usage(usage):
    import jsonschema
    schema = json.loads(pathlib.Path(__file__).with_name('news-report.schema.json').read_text())
    try:
        jsonschema.Draft202012Validator({'$defs': schema['$defs'], **schema['properties']['provider_usage']}).validate(usage)
    except jsonschema.ValidationError:
        raise BuildError('usage violates the aggregate-only schema') from None


def build(corpus, providers, count=1500, seed=992, salt=None):
    if hasattr(providers, 'prepare_resume'):
        resumed_salt = providers.prepare_resume(corpus, count, seed)
        salt = salt or resumed_salt
    salt = salt or os.urandom(32)
    if len(salt) < 32:
        raise BuildError('id salt must have at least 32 bytes')
    if not providers.synthetic and set(providers.retrievers) != set(SYSTEMS):
        raise BuildError('live build needs all four retrieval systems')
    if not providers.synthetic and (count < 1500 or not any(isinstance(j, JevJudge) for j in providers.judges)):
        raise BuildError('live build needs 1500 questions and the Jev judge adapter')
    if providers.baseline not in providers.retrievers:
        raise BuildError('baseline must name a pooled retrieval system')
    opaque = lambda value: hmac.new(salt, value.encode(), hashlib.sha256).hexdigest()
    corpus = [dataclasses.replace(a, id=opaque('article:' + a.id)) for a in sorted(corpus, key=lambda a: a.id)]
    threshold = filtered_candidate_share(providers.max_filtered_candidate_share)
    rows, rankings, filter_counts = [], {}, collections.Counter()
    def accept_questions(batch):
        with build_phase('judging'):
            query_rows, query_rankings = judge_pool(batch, corpus, providers.retrievers, providers.judges,
                                                   threshold, filter_counts, executor)
        by_query = collections.defaultdict(list)
        for row in query_rows:
            by_query[row['query']].append(row)
        decisions = []
        for i in range(len(batch)):
            keep = bool(by_query[i])
            decisions.append(keep)
            if keep:
                qi = len(rankings)
                rows.extend({**row, 'query': qi} for row in by_query[i])
                rankings[qi] = query_rankings[i]
        progress('judged', len(rankings) + filter_counts['dropped_questions'])
        return decisions
    if type(providers.concurrency) is not int or not 1 <= providers.concurrency <= 8:
        raise BuildError('concurrency must be an integer from 1 to 8')
    with concurrent.futures.ThreadPoolExecutor(max_workers=providers.concurrency) as executor:
        with build_phase('generation'):
            questions, rejected = generate(corpus, providers.generator, count, seed,
                                           filter_counts=filter_counts, executor=executor,
                                           accept_questions=accept_questions)
    qids = {i: opaque('query:' + normalize(q.text)) for i, q in enumerate(questions)}
    dev, held = split(questions, seed)
    docs = {a.id: {'title': a.title, 'text': a.text} for a in corpus}
    rows_by_query = collections.defaultdict(list)
    for row in rows:
        rows_by_query[row['query']].append(row)
    def partition(ids):
        return {'corpus': docs, 'queries': {qids[i]: questions[i].text for i in ids},
                'qrels': {qids[i]: {r['doc']: r['grade'] for r in rows_by_query[i]} for i in ids},
                'metadata': {qids[i]: {'kind': questions[i].kind, 'date': questions[i].date,
                                     'sources': list(questions[i].sources)} for i in ids},
                'judgments': [{**r, 'query': qids[i]} for i in ids for r in rows_by_query[i]],
                'hard_negatives': [{'query': qids[r['query']], 'doc': r['doc'], 'ranks': r['ranks']}
                                   for i in ids for r in rows_by_query[i] if r['grade'] == 0],
                'provenance': {'seed': seed, 'baseline': providers.baseline, 'synthetic': providers.synthetic,
                               'generator': type(providers.generator).__name__,
                               'generator_version': getattr(providers.generator, 'version', ''),
                               'judges': [{'family': j.family, 'version': getattr(j, 'version', '')}
                                          for j in providers.judges],
                               'article_metadata': {a.id: {'date': a.date, 'cluster': a.cluster, 'published_at': a.published_at,
                                     'updated_at': a.updated_at, 'story_id': a.story_id} for a in corpus}}}
    working, held_out = partition(dev), partition(held)
    scorable = {q: judgments for q, judgments in working['qrels'].items() if any(judgments.values())}
    # Only working query ids are ever passed to the scorer, including saturation.
    import scoring
    per_system = {system: scoring.score(scorable, {qids[i]: rankings[i][system] for i in dev})
                  for system in providers.retrievers}
    baseline = per_system[providers.baseline]
    depths = collections.Counter(len(rows_by_query[i]) for i in range(len(questions)))
    review = review_sample(rows, questions, corpus, qids, 100, seed)
    report = {'schema_version': 1, 'status': 'synthetic' if providers.synthetic else 'built',
              'questions': len(questions), 'articles': len(corpus),
              'question_types': dict(collections.Counter(q.kind for q in questions)),
              'rejected': {**dict.fromkeys(('title_copy', 'duplicate', 'shared_keywords'), 0), **rejected},
              'content_filter': {**dict.fromkeys(('generation_batches', 'filtered_generation_batches',
                                                   'judged_candidates', 'filtered_candidates', 'dropped_questions'), 0),
                                 **filter_counts, 'max_filtered_candidate_share': threshold,
                                 'filtered_generation_share': filter_counts['filtered_generation_batches'] / filter_counts['generation_batches'],
                                 'filtered_candidate_share': filter_counts['filtered_candidates'] / filter_counts['judged_candidates']},
              'split': {'working': len(dev), 'held_out': len(held)},
              'judgments': len(rows), 'judged_depth': {str(k): v for k, v in sorted(depths.items())},
              'agreement': agreement([r['votes'] for r in rows]),
              'jev_grade_mapping': 'probability_quartiles' if any(isinstance(j, JevJudge) for j in providers.judges) else 'not_used',
              'three_way_ties': sum(len(set(r['votes'])) == 3 for r in rows),
              'hard_negatives': sum(r['grade'] == 0 for r in rows),
              'human_check': review_result(review, review),
              'baseline': {'system': providers.baseline, 'queries': len(scorable),
                           'no_answer_queries': sum(questions[i].kind == 'no_answer' for i in dev),
                           'metrics': baseline['mean']},
              'saturation': {'working_queries_perfect_in_all_systems': sum(
                  all(scores['per_query']['ndcg@10'][qid] >= 1 - 1e-12 for scores in per_system.values())
                  for qid in scorable), 'scorable_working_queries': len(scorable)}}
    validate_report(report)
    digest = hashlib.sha256()
    for chunk in json.JSONEncoder(sort_keys=True, ensure_ascii=False).iterencode([working, held_out]):
        digest.update(chunk.encode())
    version = digest.hexdigest()
    if hasattr(providers, 'usage'):
        report['provider_usage'] = providers.usage()
        validate_report(report)
    return Build(working, held_out, review, report, version)


def write_trec(directory, data):
    import trec
    trec.write(directory, data['corpus'], data['queries'], data['qrels'])


def archive(data):
    """TREC files and private provenance, assembled without plaintext disk writes."""
    encoder = json.JSONEncoder(ensure_ascii=False, sort_keys=True)
    def json_lines(rows):
        for row in rows:
            yield from encoder.iterencode(row)
            yield '\n'
    def qrels():
        yield 'query-id\tcorpus-id\tscore\n'
        for q, docs in sorted(data['qrels'].items()):
            for d, grade in sorted(docs.items()):
                yield f'{q}\t{d}\t{grade}\n'
    files = {
        'corpus.jsonl': lambda: json_lines({'_id': k, **v} for k, v in sorted(data['corpus'].items())),
        'queries.jsonl': lambda: json_lines({'_id': k, 'text': v} for k, v in sorted(data['queries'].items())),
        'qrels.tsv': qrels,
        'metadata.json': lambda: encoder.iterencode(data['metadata']),
        'judgments.jsonl': lambda: json_lines(data['judgments']),
        'hard-negatives.jsonl': lambda: json_lines(data['hard_negatives']),
        'provenance.json': lambda: encoder.iterencode(data['provenance']),
    }
    class Chunks:
        def __init__(self, chunks):
            self.chunks, self.pending = iter(chunks), b''

        def read(self, size):
            parts, remaining = [], size
            while remaining:
                if not self.pending:
                    self.pending = memoryview(next(self.chunks, '').encode())
                    if not self.pending:
                        break
                parts.append(self.pending[:remaining])
                self.pending = self.pending[remaining:]
                remaining -= len(parts[-1])
            return b''.join(parts)
    output = io.BytesIO()
    # gzip header mtime=0 makes plaintext fingerprints reproducible.
    import gzip
    with gzip.GzipFile(fileobj=output, mode='wb', mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode='w') as tar:
            for name, chunks in sorted(files.items()):
                entry = tarfile.TarInfo(name)
                entry.size, entry.mode = sum(len(chunk.encode()) for chunk in chunks()), 0o600
                tar.addfile(entry, Chunks(chunks()))
    return output.getvalue()


def encrypt(raw, recipients):
    if not recipients or any(not isinstance(r, str) or not r.startswith('age1') for r in recipients):
        raise BuildError('age recipients must be native public keys')
    command = ['age', '--encrypt']
    for recipient in recipients:
        command.extend(['--recipient', recipient])
    try:
        result = subprocess.run(command, input=raw, capture_output=True, timeout=60)
    except (OSError, subprocess.TimeoutExpired):
        raise BuildError('age encryption unavailable') from None
    if result.returncode or not result.stdout.startswith(b'age-encryption.org/v1\n'):
        raise BuildError('age encryption failed')
    return result.stdout


class DirectoryStorage:
    """Encrypted sets and aggregate commit manifests on a private mounted volume."""
    def __init__(self, directory):
        self.directory = pathlib.Path(directory)

    def put(self, name, ciphertext):
        self.directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        path = self.directory / name
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        try:
            with os.fdopen(fd, 'wb') as output:
                output.write(ciphertext)
        except BaseException:
            path.unlink(missing_ok=True)
            raise

    def get(self, name):
        try:
            return (self.directory / name).read_bytes()
        except FileNotFoundError:
            return None

    def delete(self, name):
        (self.directory / name).unlink(missing_ok=True)


class S3Storage:
    """Private bucket policies, conditional creation and encrypted content."""
    def __init__(self, bucket, prefix='', endpoint=None):
        import boto3  # optional; credentials come from the standard environment/role
        from botocore.config import Config
        if endpoint and not endpoint.startswith('https://'):
            raise BuildError('S3 endpoint must use HTTPS')
        self.client = boto3.client('s3', endpoint_url=endpoint, config=Config(
            request_checksum_calculation='when_required', response_checksum_validation='when_required'))
        self.bucket, self.prefix = bucket, prefix.strip('/')

    def key(self, name):
        return '/'.join(filter(None, [self.prefix, name]))

    def put(self, name, ciphertext):
        self.client.put_object(Bucket=self.bucket, Key=self.key(name),
                               Body=ciphertext, IfNoneMatch='*',
                               ContentType='application/octet-stream')

    def get(self, name):
        from botocore.exceptions import ClientError
        try:
            response = self.client.get_object(Bucket=self.bucket, Key=self.key(name))
        except ClientError as error:
            if error.response.get('Error', {}).get('Code') in ('NoSuchKey', '404', 'NotFound'):
                return None
            raise
        body = response['Body']
        try:
            raw = body.read(1_000_001)
            if len(raw) > 1_000_000:
                raise BuildError('publication manifest exceeds size bound')
            return raw
        finally:
            body.close()

    def delete(self, name):
        self.client.delete_object(Bucket=self.bucket, Key=self.key(name))


def publish(result, storage, working_recipients, held_recipients, review_recipients):
    if set(working_recipients) & (set(held_recipients) | set(review_recipients)):
        raise BuildError('working keys must not decrypt held-out data or the human review sample')
    payloads = {'working': lambda: archive(result.working), 'held_out': lambda: archive(result.held_out),
                'review': lambda: result.review_csv.encode()}
    recipient_sets = [working_recipients, held_recipients, review_recipients]
    # Validate every key before any mutation; retain at most one archive/ciphertext pair.
    for recipients in recipient_sets:
        encrypt(b'', recipients)
    marker = result.version + '-manifest.json'
    if storage.get(marker) is not None:
        raise FileExistsError('dataset version is already published')
    attempt, manifest, owned, committing = uuid.uuid4().hex, {}, [], False
    try:
        for (name, payload), recipients in zip(payloads.items(), recipient_sets):
            raw = payload()
            ciphertext = encrypt(raw, recipients)
            obj = result.version + '-' + attempt + '-' + name + ('.csv.age' if name == 'review' else '.tar.gz.age')
            manifest[name] = {'object': obj, 'sha256': hashlib.sha256(ciphertext).hexdigest(),
                              'plaintext_sha256': hashlib.sha256(raw).hexdigest()}
            owned.append(obj)  # includes a PUT that commits then loses its response
            storage.put(obj, ciphertext)
            del raw, ciphertext
        report = {**result.report, 'version': result.version, 'artifacts': manifest}
        validate_report(report, published=True)
        receipt = json.dumps(report, sort_keys=True, allow_nan=False).encode()
        committing = True
        storage.put(marker, receipt)  # atomic immutable marker; contains only public aggregates
        return manifest
    except BaseException:
        if committing:
            try:
                existing = storage.get(marker)
            except Exception:
                # An uncertain final PUT may have committed. Preserve its referenced
                # ciphertext; the marker can recover the report once storage returns.
                raise BuildError('publication outcome unknown; inspect the private version manifest') from None
            if existing == receipt:
                return manifest
        for obj in owned:
            try:
                storage.delete(obj)
            except Exception:
                pass  # uncommitted ciphertext may need cleanup; unique attempt ids permit retry
        raise


class JevJudge:
    """Reuse Jev's pinned binary rubric. Map relevance probability to four bins.

    This is a probability proxy for ordinal relevance: [0,.25), [.25,.5),
    [.5,.75), [.75,1]. The two LLM adapters should apply the ordinal rubric.
    """
    family = 'jev'
    version = 'jev-1.13.0/answers-query-v1/probability-quartiles'

    def __init__(self, client, timeout=30, cost_limit_cents=1):
        from jev_rerank.client import MODEL, RUBRIC_VERSION
        self.client, self.timeout, self.cost_limit = client, timeout, cost_limit_cents
        self.version = MODEL + '/' + RUBRIC_VERSION + '/probability-quartiles'

    def grade(self, question, candidates):
        from jev_rerank.client import MAX_BYTES, MAX_TOKENS, payload
        deadline, remaining, grades, batch = time.monotonic() + self.timeout, self.cost_limit, {}, {}
        def fits(passages):
            raw = json.dumps(payload(question.text, passages), ensure_ascii=False, separators=(',', ':')).encode()
            # One UTF-8 byte per input token is a conservative admission bound.
            return len(raw) <= MAX_BYTES and len(raw) + 8 * (len(passages) + 1) <= MAX_TOKENS
        def submit(passages, split=True):
            nonlocal remaining
            try:
                result = self.client.judge(question.text, passages, deadline, cost_limit=remaining)
            except InvalidBatch as error:
                if error.reason != 'request_refused':
                    raise
                if len(passages) > 1 and split:
                    items = list(passages.items())
                    middle = len(items) // 2
                    submit(dict(items[:middle]), False)
                    submit(dict(items[middle:]), False)
                else:
                    grades.update(dict.fromkeys(passages))
                return
            remaining -= result.cost_cents
            if result.reason or set(result.scores) != set(passages) or remaining < 0:
                raise BuildError('Jev judgment unavailable')
            grades.update({key: min(3, int(value * 4)) for key, value in result.scores.items()})
        for article in candidates:
            item = {article.id: article.title + '\n' + article.text}
            if not fits(item):
                raise BuildError('article exceeds the Jev input bound')
            if batch and not fits({**batch, **item}):
                submit(batch)
                batch = {}
            batch.update(item)
        if batch:
            submit(batch)
        return grades


class FakeGenerator:
    """Synthetic questions only; useful for exercising the complete pipeline."""
    def generate(self, sample, kind, count, rng):
        out = []
        for _ in range(count):
            tag = rng.getrandbits(64)
            sources = () if kind == 'no_answer' else tuple(a.id for a in sample)
            # Paraphrases use disjoint content words; numbers ensure uniqueness.
            text = f'Quelle évolution maritime concerne le dossier {tag} ?'
            out.append(Question(text, kind, max(a.date for a in sample), sources))
        return out


class FakeRetriever:
    def search(self, question, corpus, limit):
        return (list(question.sources) + [a.id for a in corpus if a.id not in question.sources])[:limit]


class FakeJudge:
    def __init__(self, family):
        self.family = family

    def grade(self, question, candidates):
        return {a.id: 3 if a.id in question.sources else 0 for a in candidates}


def fake_providers():
    return Providers(FakeGenerator(), {s: FakeRetriever() for s in SYSTEMS},
                     [FakeJudge(f'fake-{i}') for i in range(3)], 'hybrid', synthetic=True)


def load_providers(path):
    """Load the operator's trusted factory with normal Python module metadata."""
    name = 'news_providers_' + hashlib.sha256(str(pathlib.Path(path).resolve()).encode()).hexdigest()[:16]
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module  # dataclasses and forward annotations require this registry entry
    spec.loader.exec_module(module)
    return module.providers()


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--articles', type=pathlib.Path)
    parser.add_argument('--providers', type=pathlib.Path, help='trusted Python adapter file exporting providers()')
    parser.add_argument('--fake', action='store_true', help='offline synthetic demo, never a real baseline')
    parser.add_argument('--questions', type=int, default=1500)
    parser.add_argument('--seed', type=int, default=992)
    parser.add_argument('--storage-dir', type=pathlib.Path)
    parser.add_argument('--response-cache', action='store_true', help='reuse local private responses under --storage-dir, outside the checkout')
    parser.add_argument('--concurrency', type=int, help='maximum simultaneous generator/judge calls, 1..8')
    parser.add_argument('--bucket')
    parser.add_argument('--prefix', default='')
    parser.add_argument('--s3-endpoint')
    parser.add_argument('--report', required=True, type=pathlib.Path)
    parser.add_argument('--usage-report', type=pathlib.Path, help='aggregate spend ledger, also written on build failure')
    args = parser.parse_args(argv)
    if args.usage_report and (args.usage_report.exists() or args.usage_report.is_symlink()
                              or args.usage_report.resolve() == args.report.resolve()):
        parser.error('usage report must name a separate new file')
    if args.report.exists() or args.report.is_symlink():
        parser.error('report exists; choose a new filename')
    if bool(args.storage_dir) == bool(args.bucket):
        parser.error('choose exactly one storage directory or private bucket')
    if args.fake == bool(args.providers):
        parser.error('choose exactly one --fake or --providers')
    if not args.fake and (not args.articles or args.questions < 1500):
        parser.error('live builds require articles and at least 1500 questions')
    if not args.fake and any(os.environ.get(k, '').lower() not in ('', '0', 'false') for k in ('CI', 'GITHUB_ACTIONS')):
        parser.error('live builds are refused in CI')
    if args.response_cache and not args.storage_dir:
        parser.error('response cache requires a local private storage directory')
    if args.concurrency is not None and not 1 <= args.concurrency <= 8:
        parser.error('concurrency must be from 1 to 8')
    providers = None
    phase = 'setup'
    try:
        if args.fake:
            providers = fake_providers()
            corpus = [Article(str(i), 'Une liaison ouvre au port',
                              f'Le ferry dessert une île et accueille {i + 10} voyageurs.',
                              f'2026-{1 + i % 2:02d}-02', 'maritime') for i in range(8)]
        else:
            providers = load_providers(args.providers)
            phase = 'input'
            corpus = read_articles(args.articles, providers.article_options)
            if providers.synthetic:
                raise BuildError('live build cannot use synthetic providers')
        phase = 'setup'
        if args.concurrency is not None:
            providers.concurrency = args.concurrency
        if args.response_cache:
            if not hasattr(providers, 'enable_cache'):
                raise BuildError('response cache requires the built-in live adapters')
            providers.enable_cache(args.storage_dir / 'responses')
        recipients = [os.environ.get(k, '').split() for k in
                      ('QUIVR_NEWS_WORKING_RECIPIENTS', 'QUIVR_NEWS_HOLDOUT_RECIPIENTS', 'QUIVR_NEWS_REVIEW_RECIPIENTS')]
        if not all(recipients) or set(recipients[0]) & set(recipients[1]):
            raise BuildError('provide three recipient groups with separate working and held-out keys')
        phase = 'build'
        result = build(corpus, providers, args.questions, args.seed)
        phase = 'publication'
        store = DirectoryStorage(args.storage_dir) if args.storage_dir else S3Storage(args.bucket, args.prefix, args.s3_endpoint)
        manifest = publish(result, store, *recipients)
        phase = 'report'
        report = {**result.report, 'version': result.version, 'artifacts': manifest}
        validate_report(report)
        args.report.parent.mkdir(parents=True, exist_ok=True)
        with args.report.open('x') as output:
            json.dump(report, output, indent=2, sort_keys=True, allow_nan=False)
            output.write('\n')
        print('Encrypted dataset stored; aggregate quality report written. Human review is pending.')
        return 0
    except Exception as error:
        # Provider/SDK exceptions can contain inputs, credentials and URLs.
        details = failure_details(error)
        details.setdefault('phase', phase)
        print('News-set build failed; diagnostic: ' + json.dumps(details, sort_keys=True))
        return 2
    finally:
        if providers is not None and getattr(providers, 'response_cache', None):
            providers.response_cache.close()
        if args.usage_report and providers is not None and hasattr(providers, 'usage'):
            try:
                usage = providers.usage()
                validate_usage(usage)
                args.usage_report.parent.mkdir(parents=True, exist_ok=True)
                with args.usage_report.open('x') as output:
                    json.dump(usage, output, indent=2, allow_nan=False)
                    output.write('\n')
            except Exception:
                print('Aggregate usage ledger could not be written.', file=sys.stderr)


if __name__ == '__main__':
    sys.modules['news_set'] = sys.modules[__name__]
    raise SystemExit(main())
