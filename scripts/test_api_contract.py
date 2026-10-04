"""Owner test for rejecting schema-invalid Python fake API responses."""
import unittest

from api_contract import check_response


class ResponseContract(unittest.TestCase):
    def test_fake_response_must_match_its_operation(self):
        with self.assertRaisesRegex(AssertionError, r"POST /v0/search.*retrieval_profile"):
            check_response('POST', '/v0/search', 200, {'items': []})
        check_response('POST', '/v0/search', 200,
                       {'items': [], 'retrieval_profile': {'name': 'default', 'version': 'v1'}})
        # A templated route with a query string must choose the default error
        # schema, not the successful Corpus schema.
        check_response('GET', '/v0/corpora/corpus_example?limit=1', 404,
                       {'code': 'not_found', 'message': 'Missing corpus', 'retryable': False})
        check_response('POST', '/v0/connectors/connector_example/api/events/news?tag=1',
                       202, {'receipts': []})
        path = '/v0/connectors/connector_example/api/events/news'
        with self.assertRaisesRegex(AssertionError, 'media'):
            check_response('POST', path, 503, 'unavailable', 'text/plain')
        with self.assertRaisesRegex(AssertionError, 'retryable'):
            check_response('POST', path, 503, {'code': 'ingestion_unavailable', 'message': 'unavailable'})
        headers = {'Quivr-Response-Origin': 'plugin'}
        check_response('POST', path, 429, 'provider refusal', 'text/plain', headers)
        with self.assertRaisesRegex(AssertionError, 'status absent'):
            check_response('POST', path, 503, 'provider refusal', 'text/plain', headers)
        with self.assertRaises(AssertionError):
            check_response('POST', '/v0/search', 503, 'provider refusal', 'text/plain', headers)
        challenge = '/v0/connectors/connector_example/api/challenge'
        with self.assertRaisesRegex(AssertionError, '204 cannot carry'):
            check_response('GET', challenge, 204, 'body', 'text/plain', headers)
        check_response('GET', challenge, 204, '', 'text/plain', headers)


if __name__ == '__main__':
    unittest.main()
