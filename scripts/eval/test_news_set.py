"""Owner contracts for private news-set construction; synthetic inputs, no providers."""
import csv
import io
import importlib.util
import json
import pathlib
import shutil
import sys
import subprocess
import tempfile
import unittest
import types
from unittest import mock

import news_set as news


def articles():
    return [news.Article(str(i), 'Le port ouvre une liaison',
                         f'Un ferry transporte {i + 10} voyageurs vers une île.',
                         f'2026-{1 + i % 2:02d}-02', 'transport') for i in range(8)]


def unavailable_scorer(qrels, rankings):
    """Scoring belongs to test_scoring; review/storage tests need only its shape."""
    metrics = ('ndcg@10', 'recall@10', 'mrr@10')
    return {'mean': dict.fromkeys(metrics, 0.0),
            'per_query': {m: dict.fromkeys(qrels, 0.0) for m in metrics}}


class NewsSet(unittest.TestCase):
    def test_operator_factory_supports_dataclasses_and_forward_annotations(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'adapters.py'
            path.write_text('''from __future__ import annotations
from dataclasses import dataclass
from news_set import FakeGenerator, fake_providers
@dataclass
class ConfiguredGenerator(FakeGenerator):
    limit: int = 20
def providers():
    result = fake_providers()
    result.generator = ConfiguredGenerator()
    return result
''')
            providers = news.load_providers(path)
            self.assertEqual(providers.generator.limit, 20)
            self.assertEqual(len(providers.generator.generate(articles()[:1], 'entity', 2, __import__('random').Random(1))), 2)

    def test_filters_generator_output_and_keeps_all_six_types(self):
        class Generator(news.FakeGenerator):
            def generate(self, sample, kind, count, rng):
                good = list(super().generate(sample, kind, count, rng))
                return [news.Question('LE PORT OUVRE UNE LIAISON !', kind, sample[0].date,
                                      (sample[0].id,)), good[0], good[0], *good[1:]]
        questions, rejected = news.generate(articles(), Generator(), 30, 42)
        self.assertEqual(len(questions), 30)
        self.assertEqual(len({news.normalize(q.text) for q in questions}), 30)
        self.assertEqual(set(q.kind for q in questions), set(news.KINDS))
        self.assertGreater(rejected['title_copy'], 0)
        self.assertGreater(rejected['duplicate'], 0)
        self.assertTrue(all(len(q.sources) >= 2 for q in questions if q.kind == 'multi_article'))
        self.assertTrue(all(not q.sources for q in questions if q.kind == 'no_answer'))

    def test_pool_majority_hard_negatives_and_independent_agreement_numbers(self):
        qs = [news.Question('Quand part le bateau ?', 'event', '2026-01-02', ('0',))]
        class Retriever:
            def __init__(self, ids):
                self.ids = ids
            def search(self, question, corpus, limit):
                return self.ids[:limit]
        class Judge:
            def __init__(self, family, values):
                self.family, self.values = family, values
            def grade(self, question, candidates):
                return {a.id: self.values[int(a.id)] for a in candidates}
        judges = [Judge('a', [3, 0, 1]), Judge('b', [3, 0, 2]), Judge('c', [2, 0, 3])]
        rows, ranks = news.judge_pool(qs, articles(), {'bm25': Retriever(['0', '1']),
                                                    'hybrid': Retriever(['2', '0'])}, judges)
        self.assertEqual([r['grade'] for r in rows], [3, 0, 2])
        self.assertEqual(len(rows), 3)  # union, with duplicate 0 judged only once
        self.assertEqual(rows[1]['ranks'], {'bm25': 2})
        self.assertAlmostEqual(news.agreement([[0, 0, 0], [1, 1, 2]])['fleiss_kappa'], 5 / 11)
        self.assertAlmostEqual(news.agreement([[0, 0, 0], [1, 1, 2]])['cohen_kappa'][0], 1)
        self.assertIsNone(news.agreement([[0, 0, 0]])['fleiss_kappa'])
        judges[1].values[1] = True
        with self.assertRaises(news.BuildError):
            news.judge_pool(qs, articles(), {'bm25': Retriever(['0', '1'])}, judges)
        judges[1].values[1] = 0
        judges[1].family = 'a'
        with self.assertRaises(news.BuildError):
            news.judge_pool(qs, articles(), {'bm25': Retriever(['0'])}, judges)

    def test_split_is_repeatable_balanced_and_baseline_only_scores_working(self):
        qs, rejected = news.generate(articles(), news.FakeGenerator(), 60, 42)
        dev, held = news.split(qs, 42)
        self.assertEqual((len(dev), len(held)), (36, 24))
        self.assertFalse(set(dev) & set(held))
        self.assertEqual((dev, held), news.split(qs, 42))
        for kind in news.KINDS:
            self.assertEqual(sum(qs[i].kind == kind for i in dev), 6)
        if importlib.util.find_spec('ranx') is None:
            self.skipTest('baseline needs scripts/eval/requirements.txt; pure split checks passed')
        result = news.build(articles(), news.fake_providers(), 60, 42, salt=b'x' * 32)
        self.assertEqual(result.report['split'], {'working': 36, 'held_out': 24})
        self.assertEqual(result.report['baseline']['queries'], 30)  # all-zero no-answer excluded
        self.assertEqual(result.report['baseline']['no_answer_queries'], 6)
        self.assertEqual(result.report['baseline']['metrics']['ndcg@10'], 1)
        self.assertTrue(all(len(qid) == 64 for qid in result.working['queries']))
        from trec import load
        with tempfile.TemporaryDirectory() as directory:
            news.write_trec(pathlib.Path(directory), result.working)
            parsed = load(directory)
            self.assertEqual(len(parsed['queries']), 30)
            self.assertEqual(len(parsed['dropped_queries']), 6)

    @mock.patch('scoring.score', side_effect=unavailable_scorer)
    def test_human_check_validates_the_sample_and_never_exports_text_in_report(self, scorer):
        result = news.build(articles(), news.fake_providers(), 150, 42, salt=b'x' * 32)
        review = list(csv.DictReader(io.StringIO(result.review_csv)))
        self.assertEqual(len(review), 100)
        self.assertEqual(set(row['kind'] for row in review), set(news.KINDS))
        for row in review:
            row['human_grade'] = row['grade']
        report = news.review_result(result.review_csv, news.csv_text(review))
        self.assertEqual(report, {'status': 'complete', 'sampled': 100, 'reviewed': 100,
                                  'agreement_rate': 1.0})
        review[0]['human_grade'] = str((int(review[0]['grade']) + 1) % 4)
        self.assertEqual(news.review_result(result.review_csv, news.csv_text(review))['agreement_rate'], .99)
        review[0]['human_grade'] = ''
        self.assertEqual(news.review_result(result.review_csv, news.csv_text(review))['status'], 'pending')
        review[0]['query'] = 'changed'
        with self.assertRaises(news.BuildError):
            news.review_result(result.review_csv, news.csv_text(review))
        public = json.dumps(result.report)
        for secret in ['ferry', 'voyageurs', 'liaison', 'Quand', 'transport', '2026-01-02']:
            self.assertNotIn(secret, public)
        self.assertEqual(result.report['human_check']['status'], 'pending')
        with self.assertRaises(news.BuildError):
            news.validate_report({**result.report, 'query_text': 'private input'})
        for change in [{'questions': 1}, {'split': {'working': 1, 'held_out': 1}},
                       {'judgments': 1}, {'human_check': {'status': 'complete', 'sampled': 100,
                                                        'reviewed': 1, 'agreement_rate': 1}}]:
            with self.subTest(change=change), self.assertRaises(news.BuildError):
                news.validate_report({**result.report, **change})

    def test_review_cells_cannot_be_interpreted_as_spreadsheet_formulas(self):
        row = dict.fromkeys(news.REVIEW_FIELDS, '')
        for value in ['=HYPERLINK("https://example.invalid")', '+1', '-1', '@SUM(1)', '\t=1', '  =1', '\n=1']:
            with self.subTest(value=value):
                row['query'] = value
                exported = next(csv.DictReader(io.StringIO(news.csv_text([row]))))
                self.assertEqual(exported['query'], "'" + value)

    def test_jev_grades_a_full_pool_within_client_bounds_and_aborts_failed_votes(self):
        from jev_rerank import client
        class FakeClient:
            fail = False
            def judge(self, query, passages, deadline, cost_limit):
                raw = json.dumps(client.payload(query, passages), ensure_ascii=False, separators=(',', ':')).encode()
                reason = 'transport failure' if self.fail else 'request size bound' if len(raw) > client.MAX_BYTES else ''
                return types.SimpleNamespace(reason=reason, cost_cents=.01,
                    scores={key: (int(key) % 4) / 4 for key in passages} if not reason else {})
        corpus = [news.Article(str(i), 'Un départ', 'Un événement. ' * 500, '2026-01-02') for i in range(80)]
        transport = FakeClient()
        adapter = news.JevJudge(transport)
        grades = adapter.grade(news.Question('Quelle évolution ?', 'event', '2026-01-02', ('0',)), corpus)
        self.assertEqual(grades, {str(i): i % 4 for i in range(80)})
        transport.fail = True
        with self.assertRaises(news.BuildError):
            adapter.grade(news.Question('Quelle évolution ?', 'event', '2026-01-02', ('0',)), corpus)

    def test_input_rejects_duplicates_and_bad_dates_without_echoing_text(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'articles.jsonl'
            row = {'id': 'private-id', 'title': 'private-title', 'text': 'private-text',
                   'published_at': '2026-01-02T12:34:00Z'}
            path.write_text(json.dumps(row) + '\n')
            self.assertEqual(news.read_articles(directory)[0].date, '2026-01-02')
            path.write_text(json.dumps(row) + '\n' + json.dumps(row) + '\n')
            with self.assertRaises(news.BuildError) as error:
                news.read_articles(directory)
            self.assertNotIn('private-', str(error.exception))
            path.write_text(json.dumps({**row, 'published_at': '2026-02-01T00:30:00+02:00'}))
            self.assertEqual(news.read_articles(directory)[0].date, '2026-01-31')
            path.write_text(json.dumps({**row, 'published_at': '2026-02-01T00:30:00'}))
            with self.assertRaises(news.BuildError):
                news.read_articles(directory)

    def test_wire_exports_filter_and_group_updates_before_pooling(self):
        rows = [
            {'id': 'early', 'title': 'Port update', 'text': 'A ferry will depart.', 'published_at': '2026-01-02T09:00:00Z', 'story_id': 'story-1', 'credit': 'wire'},
            {'id': 'latest', 'title': 'Port update', 'text': 'A ferry departed at noon.', 'published_at': '2026-01-02T12:00:00Z', 'story_id': 'story-1', 'credit': 'wire'},
            {'id': 'duplicate', 'title': 'Port update', 'text': 'A ferry departed at noon.', 'published_at': '2026-01-02T12:01:00Z', 'credit': 'wire'},
            {'id': 'other', 'title': 'Mountain update', 'text': 'Snow closed the mountain road.', 'published_at': '2026-01-02T10:00:00Z', 'credit': 'wire'},
            {'id': 'excluded', 'title': 'Comment', 'text': 'A personal opinion.', 'published_at': '2026-01-02', 'credit': 'opinion'}]
        with tempfile.TemporaryDirectory() as directory:
            pathlib.Path(directory, 'articles.json').write_text(json.dumps(rows))
            options = {'filter': {'field': 'credit', 'values': ['wire']}, 'group_versions': True,
                       'near_duplicate_threshold': .9, 'representative': 'latest'}
            grouped = news.read_articles(directory, options)
            self.assertEqual({a.id for a in grouped}, {'duplicate', 'other'})
            representative = next(a for a in grouped if a.id == 'duplicate')
            self.assertEqual([v['id'] for v in representative.previous_versions], ['early', 'latest'])
            self.assertEqual(representative.updated_at, '2026-01-02T12:01:00+00:00')
            rows[0]['text'] = 'The ferry timetable and fares are available. ' * 3
            pathlib.Path(directory, 'articles.json').write_text(json.dumps(rows))
            complete = news.read_articles(directory, {**options, 'representative': 'complete'})
            self.assertIn('early', {a.id for a in complete})
            self.assertEqual(len(news.read_articles(directory)), 5)  # options are opt-in
            with self.assertRaises(news.BuildError):
                news.read_articles(directory, {'filter': {'field': 'source', 'values': ['wire']}})


@unittest.skipUnless(shutil.which('age') and shutil.which('age-keygen'), 'needs age tools')
class EncryptedStorage(unittest.TestCase):
    @mock.patch('scoring.score', side_effect=unavailable_scorer)
    def test_separate_keys_ciphertext_only_no_overwrite_and_encryption_failure(self, scorer):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            keys = []
            for i in range(2):
                key = root / f'key{i}'
                subprocess.run(['age-keygen', '-o', str(key)], check=True, capture_output=True)
                recipient = subprocess.check_output(['age-keygen', '-y', str(key)], text=True).strip()
                keys.append((key, recipient))
            result = news.build(articles(), news.fake_providers(), 30, 42, salt=b'x' * 32)
            store = news.DirectoryStorage(root / 'private')
            manifest = news.publish(result, store, [keys[0][1]], [keys[1][1]], [keys[1][1]])
            cipher = root / 'private' / manifest['working']['object']
            self.assertNotIn(b'ferry', cipher.read_bytes())
            plain = subprocess.check_output(['age', '-d', '-i', str(keys[0][0]), str(cipher)])
            import tarfile
            with tarfile.open(fileobj=io.BytesIO(plain), mode='r:gz') as archive:
                self.assertIn('corpus.jsonl', archive.getnames())
            held = root / 'private' / manifest['held_out']['object']
            denied = subprocess.run(['age', '-d', '-i', str(keys[0][0]), str(held)], capture_output=True)
            self.assertNotEqual(denied.returncode, 0)
            import news_review
            review_artifact = root / 'private' / manifest['review']['object']
            raw_review = subprocess.check_output(['age', '-d', '-i', str(keys[1][0]), str(review_artifact)])
            reviewed = list(csv.DictReader(io.StringIO(raw_review.decode())))
            for row in reviewed:
                row['human_grade'] = row['grade']
            report = {**result.report, 'version': result.version, 'artifacts': manifest}
            updated = news_review.update(report, review_artifact.read_bytes(), news.csv_text(reviewed), keys[1][0])
            self.assertEqual(updated['human_check']['agreement_rate'], 1)
            with self.assertRaises(news.BuildError):
                news_review.update(report, b'corrupted', news.csv_text(reviewed), keys[1][0])
            reviewed[0]['human_grade'] = ''
            with self.assertRaises(news.BuildError):
                news_review.update(report, review_artifact.read_bytes(), news.csv_text(reviewed), keys[1][0])
            with self.assertRaises(FileExistsError):
                news.publish(result, store, [keys[0][1]], [keys[1][1]], [keys[1][1]])
            failed = news.DirectoryStorage(root / 'failed')
            with self.assertRaises(news.BuildError):
                news.publish(result, failed, ['invalid'], [keys[1][1]], [keys[1][1]])
            self.assertFalse((root / 'failed').exists())
            with self.assertRaises(news.BuildError):
                news.publish(result, failed, [keys[0][1]], [keys[0][1]], [keys[1][1]])
            class Interrupted(news.DirectoryStorage):
                writes = 0
                def put(self, name, ciphertext):
                    super().put(name, ciphertext)
                    self.writes += 1
                    if self.writes == 2:
                        raise OSError('lost response after writing')
            interrupted = Interrupted(root / 'interrupted')
            with self.assertRaises(OSError):
                news.publish(result, interrupted, [keys[0][1]], [keys[1][1]], [keys[1][1]])
            self.assertEqual(list((root / 'interrupted').iterdir()), [])
            recovered = news.publish(result, interrupted, [keys[0][1]], [keys[1][1]], [keys[1][1]])
            marker = json.loads((root / 'interrupted' / (result.version + '-manifest.json')).read_text())
            self.assertEqual(marker['artifacts'], recovered)
            class CommitReplyLost(news.DirectoryStorage):
                def put(self, name, ciphertext):
                    super().put(name, ciphertext)
                    if name.endswith('-manifest.json'):
                        raise OSError('lost commit response')
            uncertain = CommitReplyLost(root / 'uncertain')
            completed = news.publish(result, uncertain, [keys[0][1]], [keys[1][1]], [keys[1][1]])
            self.assertEqual(json.loads(uncertain.get(result.version + '-manifest.json'))['artifacts'], completed)


@unittest.skipUnless(importlib.util.find_spec('boto3'), 'needs scripts/eval/requirements-news.txt')
class BucketStorage(unittest.TestCase):
    def test_sdk_request_works_with_disabled_acls_and_preserves_immutable_ciphertext(self):
        from botocore.awsrequest import AWSResponse
        class Body(io.BytesIO):
            def stream(self, *args, **kwargs):
                yield self.read()
        saved, objects = [], {}
        def send(request):
            headers = {k.lower(): v for k, v in request.headers.items()}
            if 'x-amz-acl' in headers:
                return AWSResponse(request.url, 400, {}, Body(b'<Error><Code>AccessControlListNotSupported</Code></Error>'))
            if request.method == 'GET':
                if request.url not in objects:
                    return AWSResponse(request.url, 404, {}, Body(b'<Error><Code>NoSuchKey</Code></Error>'))
                return AWSResponse(request.url, 200, {}, Body(objects[request.url]))
            if request.method == 'DELETE':
                objects.pop(request.url, None)
                return AWSResponse(request.url, 204, {}, Body(b''))
            self.assertEqual(headers['if-none-match'], b'*')
            raw = request.body.read() if hasattr(request.body, 'read') else request.body
            objects[request.url] = raw
            saved.append(raw)
            return AWSResponse(request.url, 200, {}, Body(b''))
        with mock.patch.dict('os.environ', {'AWS_ACCESS_KEY_ID': 'fake', 'AWS_SECRET_ACCESS_KEY': 'fake',
                                            'AWS_DEFAULT_REGION': 'us-east-1', 'AWS_SESSION_TOKEN': '',
                                            'AWS_EC2_METADATA_DISABLED': 'true'}):
            storage = news.S3Storage('private-news-tests', 'sets')
            with mock.patch.object(storage.client._endpoint.http_session, 'send', side_effect=send):
                storage.put('opaque.age', b'encrypted-payload')
                self.assertEqual(storage.get('opaque.age'), b'encrypted-payload')
                storage.delete('opaque.age')
                self.assertIsNone(storage.get('opaque.age'))
        self.assertEqual(saved, [b'encrypted-payload'])


if __name__ == '__main__':
    unittest.main()
