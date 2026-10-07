"""Offline correspondence with the real core.retrieve process; no provider call."""
import copy
import http.client
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import unittest
from unittest.mock import patch

from test_retrieval import call, invocation

from quivr_plugin import Plugin
from quivr_plugin.server import RETRIEVAL_PATH
from jev_rerank.retriever import Retriever

ROOT = Path(__file__).resolve().parents[3]


class CoreCandidateCorrespondence(unittest.TestCase):
    def test_core_shortlist_matches_legacy_hybrid_fixture(self):
        # The fake-provider tests cannot catch changed core candidate parameters,
        # cutoff or tie ordering. Execute both plugins against an index fixture.
        fixture = json.loads((Path(__file__).parent / 'data/hybrid-ranking.json').read_text())
        with tempfile.TemporaryDirectory() as tmp:
            binary = str(Path(tmp) / 'core-retrieve')
            subprocess.run([os.environ.get('GO', 'go'), 'build', '-o', binary, '.'],
                           cwd=ROOT / 'plugins/core-retrieve', check=True, capture_output=True)
            with socket.socket() as available:
                available.bind(('127.0.0.1', 0))
                port = available.getsockname()[1]
            env = {'PATH': os.environ['PATH'], 'QUIVR_PLUGIN_HOST': '127.0.0.1',
                   'QUIVR_PLUGIN_PORT': str(port),
                   'QUIVR_PLUGIN_MANIFEST': str(ROOT / 'plugins/core-retrieve/quivr-plugin.yaml')}
            with subprocess.Popen([binary], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL) as process:
                try:
                    deadline = time.monotonic() + 5
                    while True:
                        connection = http.client.HTTPConnection('127.0.0.1', port, timeout=0.1)
                        try:
                            connection.request('GET', '/healthz')
                            reply = connection.getresponse()
                            reply.read()
                            break
                        except OSError:
                            if process.poll() is not None or time.monotonic() >= deadline:
                                self.fail('core.retrieve did not start')
                        finally:
                            connection.close()

                    def core(document):
                        connection = http.client.HTTPConnection('127.0.0.1', port, timeout=2)
                        try:
                            connection.request('POST', RETRIEVAL_PATH, json.dumps(document), {'Content-Type': 'application/json'})
                            reply = connection.getresponse()
                            body = json.loads(reply.read())
                            self.assertEqual(reply.status, 200, body)
                            return body
                        finally:
                            connection.close()

                    plugin = Plugin(ROOT / 'plugins/jev-rerank/quivr-plugin.yaml')
                    plugin.retrieval(Retriever().search)
                    deep = invocation()
                    deep.update(round=1, served=[], limit=50)
                    deep['query']['text'] = fixture['query']
                    for mode in ['lexical', 'semantic', 'hybrid']:
                        deep['query']['mode'] = mode
                        requested = call(plugin, deep)['requests'][0]['profile']
                        normal = copy.deepcopy(deep)
                        normal.update(profile='default', limit=requested['limit'])
                        normal['query']['mode'] = requested['mode']
                        normal['spaces'] = [{'id': fixture['space'], 'owner': {'kind': 'engine'}, 'model': 'fixture',
                            'dimensions': 3, 'metric': 'cosine', 'indexes': ['hybrid'], 'query_modalities': ['text'],
                            'role': 'served', 'coverage': {'segments': 35, 'total': 35}}]
                        primitive = core(normal)['requests'][0]
                        # Frozen legacy Jev request: one space, K=30, alpha=.5,
                        # relative-score hybrid fusion even for non-hybrid callers,
                        # with the default core profile now selecting one hit per Record.
                        self.assertEqual(primitive, {'primitive': 'hybrid', 'query_text': fixture['query'],
                            'space': fixture['space'], 'field': 'source', 'alpha': 0.5,
                            'fusion': 'relative_score', 'k': fixture['candidate_count'],
                            'group_by': 'record'})
                        catalogue = fixture['candidates'][:primitive['k']]
                        normal.update(round=2, served=[{'round': 1, 'request_index': 0,
                            'request': primitive, 'candidates': catalogue}])
                        hits = core(normal)['ranking']['hits']
                        self.assertEqual([hit['segment_id'] for hit in hits], fixture['legacy_hybrid_top'])
                        by_id = {candidate['segment_id']: candidate for candidate in catalogue}
                        deep.update(round=2, served=[{'round': 1, 'request_index': 0,
                            'request': {'primitive': 'profile', 'profile': requested},
                            'candidates': [{**by_id[hit['segment_id']], 'score': hit['score'],
                                            'explanation': hit['explanation']} for hit in hits]}])
                        with patch.dict(os.environ, {}, clear=True):
                            fallback = call(plugin, deep)
                        self.assertEqual([hit['segment_id'] for hit in fallback['ranking']['hits']], fixture['legacy_hybrid_top'])
                        self.assertEqual([hit['score'] for hit in fallback['ranking']['hits']], [hit['score'] for hit in hits])
                        self.assertTrue(all('API key not configured' in hit['explanation'] for hit in fallback['ranking']['hits']))
                        self.assertEqual(fallback['usage'], {'paid_calls': 0, 'cost_cents': 0})
                        deep.update(round=1, served=[])
                finally:
                    process.terminate()
                    process.wait(timeout=2)
