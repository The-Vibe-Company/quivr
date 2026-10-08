"""The set layout shared by public and private sets: what is accepted, what is refused and why."""
import hashlib
import json
import pathlib
import tempfile
import unittest
import zipfile

import trec


def write_set(directory, qrels='q1\td1\t2\nq1\td2\t0\nq2\td2\t1\nq4\td1\t0\n', header=True, corpus=None):
    directory = pathlib.Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    rows = corpus or [{'_id': 'd1', 'title': 'Titre', 'text': 'premier'}, {'_id': 'd2', 'text': 'second'}, {'_id': 'd3', 'text': 'autre'}]
    (directory / 'corpus.jsonl').write_text(''.join(json.dumps(r) + '\n' for r in rows))
    (directory / 'queries.jsonl').write_text(''.join(json.dumps({'_id': q, 'text': 'question ' + q}) + '\n' for q in ['q1', 'q2', 'q3', 'q4']))
    (directory / 'qrels.tsv').write_text(('query-id\tcorpus-id\tscore\n' if header else '') + qrels)
    return directory


class Load(unittest.TestCase):
    def test_reads_layout_and_drops_unscorable_queries(self):
        with tempfile.TemporaryDirectory() as d:
            s = trec.load(write_set(d))
        self.assertEqual(s['qrels'], {'q1': {'d1': 2, 'd2': 0}, 'q2': {'d2': 1}})
        self.assertEqual(sorted(s['queries']), ['q1', 'q2'])
        self.assertEqual(s['dropped_queries'], ['q3', 'q4'])  # missing or all-zero judgments
        self.assertEqual(s['corpus']['d2'], {'title': '', 'text': 'second'})

    def test_trec_four_columns_without_header(self):
        with tempfile.TemporaryDirectory() as d:
            s = trec.load(write_set(d, qrels='q1 Q0 d1 1\n', header=False))
        self.assertEqual(s['qrels'], {'q1': {'d1': 1}})

    def test_judgement_of_unknown_document_is_refused(self):
        with tempfile.TemporaryDirectory() as d:
            with self.assertRaisesRegex(trec.SetError, "absent from corpus.jsonl, e.g. 'd9'"):
                trec.load(write_set(d, qrels='q1\td9\t1\n'))

    def test_duplicate_document_is_refused(self):
        with tempfile.TemporaryDirectory() as d:
            with self.assertRaisesRegex(trec.SetError, 'corpus.jsonl:2: duplicate _id'):
                trec.load(write_set(d, corpus=[{'_id': 'd1', 'text': 'a'}, {'_id': 'd1', 'text': 'b'}]))

    def test_written_set_reads_back_identically(self):
        with tempfile.TemporaryDirectory() as d:
            s = trec.load(write_set(pathlib.Path(d) / 'a'))
            trec.write(pathlib.Path(d) / 'b', s['corpus'], s['queries'], s['qrels'])
            self.assertEqual({k: v for k, v in trec.load(pathlib.Path(d) / 'b').items() if k != 'dropped_queries'},
                             {k: v for k, v in s.items() if k != 'dropped_queries'})


class Sources(unittest.TestCase):
    def test_archive_is_checked_then_extracted(self):
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            write_set(root / 'src' / 'my-set')
            archive = root / 'my-set.zip'
            with zipfile.ZipFile(archive, 'w') as z:
                for f in (root / 'src' / 'my-set').iterdir():
                    z.write(f, f'my-set/{f.name}')
            digest = hashlib.sha256(archive.read_bytes()).hexdigest()
            with self.assertRaisesRegex(trec.SetError, 'differs from the pinned'):
                trec.fetch(archive, '0' * 64, root / 'cache')
            directory = trec.materialize(trec.fetch(archive, digest, root / 'cache'), root / 'cache')
            self.assertEqual(sorted(trec.load(directory)['queries']), ['q1', 'q2'])

    def test_url_without_checksum_is_refused(self):
        with self.assertRaisesRegex(trec.SetError, 'needs its sha256'):
            trec.fetch('https://example.org/set.zip', '', '/nonexistent')


if __name__ == '__main__':
    unittest.main()
