"""Owner tests for reranker pass accounting and installation pins; no paid service."""
import json
import pathlib
import tempfile
import types
import unittest

import jev


class RerankerHarness(unittest.TestCase):
    def test_pass_offsets_exclude_warmups_and_other_profiles_without_double_counting_spend(self):
        with tempfile.TemporaryDirectory() as directory:
            log = pathlib.Path(directory) / 'plugin.log'
            event = {'event': 'jev_rerank', 'profile': 'deep', 'paid_calls': 1, 'cost_cents': .2,
                     'input_tokens': 600, 'pairs': 30, 'cache_hits': 10, 'fallback': False}
            log.write_text(json.dumps(event) + '\n')
            begin = jev.offset(log)
            with log.open('a') as output:
                output.write('not JSON\n' + json.dumps({**event, 'profile': 'default'}) + '\n')
                output.write(json.dumps(event) + '\n')
                output.write(json.dumps({**event, 'input_tokens': 0, 'cache_hits': 30, 'paid_calls': 0,
                                         'cost_cents': 0, 'fallback': True, 'reason': 'provider_unavailable'}) + '\n')
            end = jev.offset(log)
            with log.open('a') as output:
                output.write(json.dumps(event) + '\n')
            usage = jev.summary([{'paid_calls': 1, 'cost_cents': .2}, {'paid_calls': 0, 'cost_cents': 0}],
                                jev.records(log, begin, end, 'deep'))
        self.assertEqual((usage['searches'], usage['log_records']), (2, 2))
        self.assertEqual((usage['paid_calls_per_search'], usage['cost_cents_per_search']), (.5, .1))
        self.assertEqual(usage['tokens_per_search'], 300)
        self.assertEqual(usage['cache_hit_rate'], 2 / 3)
        self.assertEqual(usage['fallback_rate'], .5)
        self.assertEqual(usage['fallback_reasons'], {'provider_unavailable': 1})
        failed = jev.summary([{}, {}], [{**event, 'input_tokens': 65536, 'estimated_tokens': 65536, 'cost_cents': .4}])
        self.assertEqual(failed['cost_cents_per_search'], .2)
        self.assertEqual(failed['tokens_per_search'], 0)
        self.assertEqual(failed['estimated_tokens_per_search'], 32768)
        self.assertTrue(failed['cost_is_upper_bound'])
        unknown = jev.summary([{}], [])
        self.assertIsNone(unknown['cost_cents_per_search'])
        self.assertIsNone(unknown['fallback_rate'])
        for reason in ['HTTP 402', 'provider refused (payment)']:
            refused = jev.summary([{}], [{**event, 'fallback': True, 'reason': reason}])
            self.assertEqual(refused['fallback_reasons'], {'provider refused (payment)': 1})

    def test_api_and_worker_keep_normal_search_when_jev_is_configured(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = pathlib.Path(directory)
            ingestion = {'manifest': '/plugins/core-ingest/quivr-plugin.yaml', 'configuration': {'tei_url': 'http://127.0.0.1:1234'}}
            retrieval = {'manifest': '/plugins/core-retrieve/quivr-plugin.yaml'}
            pin = {'manifest': '/plugins/jev-rerank/quivr-plugin.yaml', 'configuration': {'tokenizer_path': '/tokenizer.json'}}
            stack = types.SimpleNamespace(directory=directory, state={'jev_pin': pin})
            for name in ['config.json', 'worker.json']:
                path = directory / name
                path.write_text(json.dumps({'plugins': [ingestion, retrieval], 'listen': '127.0.0.1:4567'}))
            jev.configure(stack)
            jev.configure(stack)
            for name in ['config.json', 'worker.json']:
                config = json.loads((directory / name).read_text())
                self.assertEqual(config['plugins'], [ingestion, retrieval, pin])
                self.assertEqual(config['retrieval']['profiles'], {
                    'default': 'core.retrieve/default', 'deep': 'jev.rerank/deep'})
                self.assertEqual(config['listen'], '127.0.0.1:4567')
                self.assertEqual((directory / name).stat().st_mode & 0o777, 0o600)


if __name__ == '__main__':
    unittest.main()
