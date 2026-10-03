"""Offline owner contracts for the developer-local direct comparison."""
import importlib.util
import io
import json
import os
import pathlib
import tempfile
import types
import unittest
import urllib.error
from unittest import mock

import embeddings
import trec

HAS_NUMPY = importlib.util.find_spec('numpy') is not None
HAS_RANX = importlib.util.find_spec('ranx') is not None
import direct_bakeoff as bakeoff


@unittest.skipUnless(HAS_NUMPY, 'local comparison needs numpy')
class Retrieval(unittest.TestCase):
    def test_cosine_best_piece_and_stable_document_ties(self):
        # b wins via its second piece; c and a tie despite different vector norms.
        ranking = bakeoff.rank(['a', 'b', 'c'], ['q'], [[1, 0]],
                               [[1, 1], [-1, 0], [4, 0], [2, 2]], [0, 1, 1, 2])
        self.assertEqual(ranking, {'q': ['b', 'a', 'c']})
        pieces, owners = bakeoff.split_documents(['abcdefghi', ''], 5, overlap=2)
        self.assertEqual(pieces, ['abcde', 'defgh', 'ghi', ''])
        self.assertEqual(owners, [0, 0, 0, 1])
        with self.assertRaises(ValueError):
            bakeoff.rank(['a'], ['q'], [[0, 0]], [[1, 0]], [0])


class Providers(unittest.TestCase):
    def test_formats_batching_dimensions_and_response_order(self):
        for model, dimension in [('Cohere-Embed-V5-Pro', 1024), ('text-embedding-3-large', None)]:
            with self.subTest(model=model):
                client = bakeoff.Hosted('https://example.com', 'fixture-key', embeddings.Budget(1000, 1), 'scifact')
                received = []
                def respond(request, timeout):
                    body = json.loads(request.data)
                    received.append((request.full_url, body))
                    count = len(body.get('texts', body.get('input', [])))
                    width = body.get('output_dimension', bakeoff.HOSTED_DIMENSIONS[model])
                    result = ({'embeddings': {'float': [[i] + [1] * (width - 1) for i in range(count)]},
                               'meta': {'billed_units': {'input_tokens': 1}}} if dimension else
                              {'data': [{'index': i, 'embedding': [i] + [1] * (width - 1)} for i in reversed(range(count))],
                               'usage': {'prompt_tokens': 1}})
                    return io.BytesIO(json.dumps(result).encode())
                with mock.patch.object(client.opener, 'open', side_effect=respond):
                    vectors = client.embed(model, ['a', 'b', 'c'], 'query', batch=2, dimensions=dimension)
                width = dimension or bakeoff.HOSTED_DIMENSIONS[model]
                self.assertEqual(vectors, [[i] + [1] * (width - 1) for i in [0, 1, 0]])
                self.assertEqual(len(received), 2)
                first = received[0]
                if dimension:
                    self.assertTrue(first[0].endswith('/providers/cohere/v2/embed'))
                    self.assertEqual(first[1]['input_type'], 'search_query')
                    self.assertEqual(first[1]['output_dimension'], 1024)
                else:
                    self.assertTrue(first[0].endswith('/openai/v1/embeddings'))
                    self.assertEqual(first[1]['input'], ['a', 'b'])
                malformed = ({'embeddings': {'float': [[1, 0]]},
                              'meta': {'billed_units': {'input_tokens': 1}}} if dimension else
                             {'data': [{'index': 0, 'embedding': [1, 0]}], 'usage': {'prompt_tokens': 1}})
                with mock.patch.object(client.opener, 'open', return_value=io.BytesIO(json.dumps(malformed).encode())):
                    with self.assertRaisesRegex(RuntimeError, 'invalid provider embeddings'):
                        client.embed(model, ['a'], 'document', dimensions=dimension)

    def test_retry_and_unknown_usage_cannot_escape_run_cap_or_leak_errors(self):
        client = bakeoff.Hosted('https://example.com', 'fixture-key', embeddings.Budget(20, 1), 'scifact')
        error = urllib.error.HTTPError('https://example.com', 429, 'reflected secret', {}, io.BytesIO(b'sensitive body'))
        with mock.patch.object(client.opener, 'open', side_effect=error) as network, mock.patch.object(bakeoff.time, 'sleep'):
            with self.assertRaises(embeddings.BudgetExceeded):
                client.embed('Cohere-Embed-V5-Pro', ['abc'], 'document')
            self.assertEqual(network.call_count, 1)  # 11 reserved, another 11 refused
        self.assertEqual(client.budget.summary()['reserved_input_tokens'], 11)
        client = bakeoff.Hosted('https://example.com', 'fixture-key', embeddings.Budget(100, 1), 'scifact')
        error.code = 401
        with mock.patch.object(client.opener, 'open', side_effect=error):
            with self.assertRaisesRegex(RuntimeError, '^provider HTTP 401$'):
                client.embed('Cohere-Embed-V5-Pro', ['abc'], 'document')

    def test_e5_applies_query_and_passage_prefixes_without_download(self):
        encoder = mock.Mock()
        with mock.patch.dict('sys.modules', {'sentence_transformers': types.SimpleNamespace(SentenceTransformer=mock.Mock(return_value=encoder))}):
            local = bakeoff.E5()
            local.embed(['question'], 'query')
            local.embed(['article'], 'document')
        self.assertEqual(encoder.encode.call_args_list[0].args[0], ['query: question'])
        self.assertEqual(encoder.encode.call_args_list[1].args[0], ['passage: article'])

    def test_ci_refuses_before_any_download_or_provider(self):
        for flags in ({'CI': 'true', 'GITHUB_ACTIONS': ''}, {'CI': '', 'GITHUB_ACTIONS': 'true'}):
            with self.subTest(flags=flags), mock.patch.dict(os.environ, flags), mock.patch.object(bakeoff.public_sets, 'prepare') as prepare:
                with self.assertRaisesRegex(SystemExit, 'local'):
                    bakeoff.main(['--set', 'scifact', '--max-input-tokens', '100', '--max-usd', '1', '--out', 'unused.json'])
            prepare.assert_not_called()


@unittest.skipUnless(HAS_NUMPY and HAS_RANX, 'local comparison needs numpy and ranx')
class Run(unittest.TestCase):
    def test_completed_scores_and_capped_partial_evidence_survive(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            trec.write(root / 'set', {'a': {'title': 'title', 'text': 'body'}, 'b': {'text': 'other'}},
                       {'q': 'question'}, {'q': {'a': 1}})
            (root / 'set' / 'manifest.json').write_text('{"sample":{"seed":775}}')
            for capped in (False, True):
                output = root / ('capped.json' if capped else 'complete.json')
                def hosted(*args, **kwargs):
                    if capped:
                        raise embeddings.BudgetExceeded('run embedding input-token or USD budget exhausted')
                    return [[1, 0], [0, 1]] if args[2] == 'document' else [[1, 0]]
                with mock.patch.dict(os.environ, {'AZURE_FOUNDRY_ENDPOINT': 'https://example.com', 'AZURE_FOUNDRY_KEY': 'fixture-key', 'CI': '', 'GITHUB_ACTIONS': ''}), \
                     mock.patch.object(bakeoff.public_sets, 'prepare', return_value=root / 'set'), \
                     mock.patch.object(bakeoff.E5, 'embed', side_effect=[[[1, 0], [0, 1]], [[1, 0]]]), \
                     mock.patch.object(bakeoff.Hosted, 'embed', side_effect=hosted):
                    code = bakeoff.main(['--set', 'scifact', '--models', 'Cohere-Embed-V5-Pro-1024',
                                         '--max-input-tokens', '1000', '--max-usd', '1', '--out', str(output)])
                report = json.loads(output.read_text())
                self.assertEqual(code, 2 if capped else 0)
                self.assertEqual(report['status'], 'capped' if capped else 'complete')
                self.assertEqual(report['fingerprint'], trec.fingerprint(root / 'set'))
                self.assertEqual(report['results'][bakeoff.BASELINE]['mean']['ndcg@10'], 1)
                self.assertEqual(report['results'][bakeoff.BASELINE]['per_query']['ndcg@10'], {'q': 1.0})
                self.assertNotIn('fixture-key', output.read_text())
                self.assertEqual(len(report['source']['git_sha']), 40)
                self.assertTrue(report['source']['plugin_digest'].startswith('sha256:'))
                if not capped:
                    self.assertEqual(report['results']['Cohere-Embed-V5-Pro-1024']['vs_current']['ndcg@10']['delta'], 0)
                with self.assertRaises(FileExistsError):
                    bakeoff.main(['--set', 'scifact', '--max-input-tokens', '100', '--max-usd', '1', '--out', str(output)])


if __name__ == '__main__':
    unittest.main()
