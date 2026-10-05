"""Private working-set contracts at policy and trusted runner boundaries."""
import copy
import unittest

import modal_search


def policy():
    return {'experiment': 'public/example', 'price_revision': 'fixture-v1',
            'modal_usd_per_second': .001,
            'sets': {'private-example': {'split': 'dev', 'input': {
                'name': 'private-example', 'version': 'v1', 'split': 'working',
                'digest': 'a' * 64, 'fingerprint': 'b' * 64, 'privacy': 'private'}}}}


class Policy(unittest.TestCase):
    def test_private_descriptor_accepts_working_only_and_rejects_runtime_secrets(self):
        value = policy()
        self.assertEqual(modal_search.policy(value)['sets']['private-example']['input']['split'], 'working')
        for edit in (
            lambda d: d.update(split='heldout'),
            lambda d: d.update(split='test'),
            lambda d: d.update(privacy='public'),
            lambda d: d.update(fingerprint='bad'),
            lambda d: d.update(age_identity='secret'),
            lambda d: d.update(name='other'),
        ):
            bad = copy.deepcopy(value)
            edit(bad['sets']['private-example']['input'])
            with self.subTest(edit=edit), self.assertRaises((ValueError, PermissionError)):
                modal_search.policy(bad)
        for name in ('.', '..'):
            bad = copy.deepcopy(value)
            bad['sets'] = {name: bad['sets']['private-example']}
            bad['sets'][name]['input']['name'] = name
            with self.subTest(name=name), self.assertRaises((ValueError, PermissionError)):
                modal_search.policy(bad)


# SQL is real so publication and replay prove actual canonical privacy. Only
# Modal mounts and external provider/tracking HTTP are substituted.
import importlib.util
import io
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tarfile
import tempfile
import types
import uuid
import collections
from unittest import mock

import control_store
import gates
import private_working
import results
import search_trial
import trec


@unittest.skipUnless(os.environ.get('EVAL_CONTROL_TEST_DSN') and shutil.which('age')
                     and importlib.util.find_spec('ranx'), 'needs disposable SQL, age and scorer')
class Runner(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        self.name = 'private-example'
        self.store = control_store.Store(os.environ['EVAL_CONTROL_TEST_DSN'])
        self.campaign = uuid.uuid4().hex
        self.mount = self.root / 'mount' / self.name
        self.mount.mkdir(parents=True)
        self.identity = self.root / 'identity.txt'
        subprocess.run(['age-keygen', '-o', str(self.identity)], check=True, capture_output=True)
        self.recipient = subprocess.check_output(['age-keygen', '-y', str(self.identity)], text=True).strip()
        self.value = policy()
        self.value['baseline'] = {'model': 'Cohere-Embed-V5-Fast', 'revision': 'fixture-v1',
                                  'dimensions': 2, 'dense_weight': 1}
        self.encrypt_fixture({'secret-doc-a': {'text': 'apple private-passage-sentinel'},
                              'secret-doc-b': {'text': 'pear other-passage-sentinel'}},
                   {f'secret-query-{i}': 'apple private-question-sentinel ' + str(i) for i in range(20)},
                   {f'secret-query-{i}': {'secret-doc-a': 1} for i in range(20)})
        self.policy = modal_search.policy(self.value)
        self.config = search_trial.configuration({**self.policy['baseline'], 'dense_weight': .5})
        self.reference = {'volume': 'working-fixture', 'artifact': self.artifact.name, 'secret': 'working-fixture',
                          'identity_env': 'EVAL_WORKING_AGE_EXAMPLE', 'provider_consent': True}
        self.calls = []
        self.ephemeral_paths = []
        self.environment = {'EVAL_WORKING_RUNTIME': json.dumps({self.name: self.reference}),
                            'EVAL_WORKING_AGE_EXAMPLE': self.identity.read_text(),
                            'EVAL_CONTROL_DATABASE_URL': os.environ['EVAL_CONTROL_TEST_DSN'],
                            'AZURE_FOUNDRY_ENDPOINT': 'https://example.com', 'AZURE_FOUNDRY_KEY': 'fixture-key',
                            'MLFLOW_TRACKING_URI': '', 'MLFLOW_PRIVATE_TRACKING_URI': ''}

    def encrypt_fixture(self, corpus, queries, qrels):
        data = self.root / 'data'
        trec.write(data, corpus, queries, qrels)
        archive = self.root / 'working.tar.gz'
        with tarfile.open(archive, 'w:gz') as out:
            for path in data.iterdir():
                out.add(path, arcname=path.name)
        self.artifact = self.mount / 'working.tar.gz.age'
        subprocess.run(['age', '-r', self.recipient, '-o', str(self.artifact), str(archive)],
                       check=True, capture_output=True)
        self.value['sets'][self.name]['input'].update(
            digest=trec.sha256_file(self.artifact), fingerprint=trec.fingerprint(data))

    def test_fresh_samples_alternate_sides_and_export_timing_components(self):
        # Own paired scheduling at the encrypted runner boundary. Spy on real
        # ranking only to label the configuration served by each fake request.
        original_rank = search_trial.SearchIndex.rank
        served = []
        clock = [0.]
        def rank(index, query, *args, **kwargs):
            served.append((index.cfg['dense_weight'], query))
            clock[0] += .02
            return original_rank(index, query, *args, **kwargs)
        def provider(request, timeout):
            # Serial provider latency drifts across the invocation. Adjacent
            # A/B requests see nearly the same drift rather than separate eras.
            clock[0] += .1 + .001 * len(self.calls)
            return self.provider(request, timeout)
        with mock.patch.object(search_trial.SearchIndex, 'rank', rank), \
                mock.patch.object(search_trial.time, 'monotonic', side_effect=lambda: clock[0]):
            outcome = self.run_trial(provider=provider)
        self.assertEqual(outcome['status'], 'complete')
        # The quality passes precede 21 alternating fresh calls per side,
        # including each side's identical first-query warmup.
        fresh = served[40:]
        self.assertEqual([weight for weight, _ in fresh], [1, .5] * 21)
        self.assertTrue(all(a[1] == b[1] for a, b in zip(fresh[::2], fresh[1::2])))
        for row in (outcome['record'], outcome['baseline_record']):
            cost = row['cost']
            self.assertEqual(cost['search_timing_ms']['samples'], 20)
            self.assertGreaterEqual(cost['search_timing_ms']['provider_p95'], 0)
            self.assertGreaterEqual(cost['search_timing_ms']['local_p95'], 0)
            self.assertAlmostEqual(row['metrics']['cost_per_search_usd'],
                cost['search_provider_usd'] + cost['search_compute_usd'])
            self.assertNotIn('secret-query-', json.dumps(cost))
        self.assertAlmostEqual(outcome['record']['cost']['search_timing_ms']['local_p95'], 20)
        self.assertLess(outcome['record']['metrics']['latency_p95_ms'] /
                        outcome['baseline_record']['metrics']['latency_p95_ms'], 1.02)

    def test_private_reuse_requires_full_embedding_identity_and_ends_with_invocation(self):
        for field, value in (('revision', 'fixture-v2'), ('dimensions', 3),
                ('model', 'Cohere-Embed-V5-Pro'), ('window_chars', 1900), ('overlap_chars', 100), (None, None)):
            with self.subTest(field=field):
                self.campaign = uuid.uuid4().hex
                self.calls.clear()
                if not field:
                    # Private request efficiency has its own branch; the
                    # public scheduler already owns the concurrency barrier.
                    self.encrypt_fixture({f'secret-doc-{i:03}': {'text': f'private-passage-sentinel {i}'} for i in range(513)},
                        {f'secret-query-{i}': f'private-question-sentinel {i}' for i in range(61)},
                        {f'secret-query-{i}': {'secret-doc-000': 1} for i in range(61)})
                    self.policy = modal_search.policy(self.value)
                self.config = search_trial.configuration({**self.policy['baseline'],
                    'dense_weight': .7, 'candidate_count': 40, **({field: value} if field else {})})
                def provider(request, timeout):
                    self.assertTrue(self.ephemeral_paths)
                    self.assertFalse(any((path / 'vectors').exists() for path in self.ephemeral_paths))
                    body = json.loads(request.data)
                    self.calls.append(body)
                    count, dimensions = len(body['texts']), body['output_dimension']
                    return io.BytesIO(json.dumps({'embeddings': {'float': [[1] + [0] * (dimensions - 1)] * count},
                        'meta': {'billed_units': {'input_tokens': count}}}).encode())
                outcome = self.run_trial(provider=provider)
                self.assertEqual(outcome['status'], 'complete')
                self.assertEqual(outcome['record']['provenance']['private_pair']['statistics']['queries'], 20 if field else 61)
                counts = collections.Counter(c['input_type'] for c in self.calls)
                if field:
                    self.assertEqual(counts['search_document'], 2)
                else:
                    documents = [c for c in self.calls if c['input_type'] == 'search_document']
                    self.assertEqual(sum(len(c['texts']) for c in documents), 513)
                    self.assertLessEqual(len(documents), 9)
                self.assertEqual(counts['search_query'], 44 if field else 103)
                # Statistics count all quality queries, including those beyond
                # the 50-query fresh sample. Identical candidates pay only
                # their fresh requests; baseline owns the document/query fill.
                baseline_tokens, candidate_tokens = 43 if field else 625, 43 if field else 51
                self.assertEqual(sum(len(c['texts']) for c in self.calls), baseline_tokens + candidate_tokens)
                for side, tokens in (('baseline_record', baseline_tokens), ('record', candidate_tokens)):
                    usage = outcome[side]['cost']['provider']
                    self.assertEqual(usage['confirmed_input_tokens'], tokens)
                    self.assertEqual(usage['reserved_input_tokens'], 0)

    def provider(self, request, timeout):
        self.assertTrue(self.ephemeral_paths)
        self.assertFalse(any((path / 'vectors').exists() for path in self.ephemeral_paths))
        self.calls.append(json.loads(request.data))
        body = self.calls[-1]
        vectors = [[0, 1] if 'pear' in t or 'question' in t else [1, 0] for t in body['texts']]
        return io.BytesIO(json.dumps({'embeddings': {'float': vectors},
            'meta': {'billed_units': {'input_tokens': len(vectors)}}}).encode())

    def run_trial(self, **patches):
        modal = types.SimpleNamespace(Volume=mock.Mock())
        # Results uses an isolated outbox; its implementation remains real.
        original = results.Results
        def outbox(*args, **kwargs):
            kwargs['directory'] = self.root / 'remote-outbox'
            return original(*args, **kwargs)
        temporary_directory = tempfile.TemporaryDirectory
        def ephemeral(*args, **kwargs):
            directory = temporary_directory(*args, **kwargs)
            self.ephemeral_paths.append(pathlib.Path(directory.name))
            return directory
        # Replace only Modal: patch.dict restores the whole module registry and
        # unloads lazily imported scorer/JIT modules between paired invocations.
        previous_modal = sys.modules.get('modal')
        sys.modules['modal'] = modal
        try:
            with mock.patch.object(private_working.tempfile, 'TemporaryDirectory', side_effect=ephemeral), \
                    mock.patch.dict(os.environ, self.environment), \
                    mock.patch.object(private_working, 'MOUNT_ROOT', self.root / 'mount'), \
                    mock.patch.object(results, 'Results', side_effect=outbox), \
                    mock.patch('urllib.request.OpenerDirector.open', side_effect=patches.get('provider', self.provider)):
                return modal_search.dispatch(self.store, self.campaign, self.policy, self.config, self.name,
                    'a' * 40, 'sha256:fixture', modal_search.remote_trial, self.root / 'local-outbox', True)
        finally:
            if previous_modal is None:
                sys.modules.pop('modal', None)
            else:
                sys.modules['modal'] = previous_modal

    def test_encrypted_pair_exports_only_aggregates_and_replays_without_input(self):
        with self.assertLogs(level='INFO') as logs:
            outcome = self.run_trial()
        self.assertEqual(outcome['status'], 'complete')
        candidate, baseline = outcome['record'], outcome['baseline_record']
        self.assertEqual(candidate['metrics']['ndcg@10'], 1)
        self.assertAlmostEqual(baseline['metrics']['ndcg@10'], .6309297535714575)
        report = gates.evaluate({self.name: {'candidate': candidate, 'baseline': baseline}}, self.policy)
        self.assertTrue(report['gates']['quality']['passed'])
        self.assertTrue(report['gates']['no_loss']['passed'])
        self.assertTrue(report['gates']['latency']['details'][self.name]['comparable'])
        self.assertEqual(report['sets'][self.name]['queries'], 20)
        # Pairing must fail closed if another baseline's aggregate is substituted.
        for side, field in (('baseline', 'metrics'), ('candidate', 'metrics'), ('candidate', 'config')):
            pair = copy.deepcopy({'candidate': candidate, 'baseline': baseline})
            if field == 'metrics':
                pair[side][field]['ndcg@10'] = .9
            else:
                pair[side][field]['dense_weight'] = 1
            with self.subTest(side=side, field=field):
                invalid = gates.evaluate({self.name: pair}, self.policy)
                self.assertEqual(invalid['missing_or_incompatible_sets'], [self.name])
        with self.store.transaction() as db:
            rows = db.execute('SELECT key,payload FROM eval_control.leases WHERE campaign=%s', (self.campaign,)).fetchall()
            self.assertEqual(len(rows), 2)  # no private text-derived cache leases
            self.assertTrue(all(row[1]['per_query'] == {} for row in rows))
            self.assertEqual(db.execute("SELECT count(*) FROM eval_control.reservations WHERE campaign=%s AND kind='provider' AND settled",
                                       (self.campaign,)).fetchone()[0], len(self.calls))
        for phase in ('decrypting', 'embedding', 'indexing', 'search', 'scoring'):
            self.assertTrue(any(phase in line for line in logs.output), phase)
        exported = json.dumps(rows) + json.dumps(report) + '\n'.join(logs.output)
        exported += ''.join(p.read_text() for p in self.root.rglob('*.json') if 'outbox' in str(p))
        sent = []
        run = {'info': {'run_id': 'fixture-run', 'status': 'RUNNING',
                        'artifact_uri': 'mlflow-artifacts:/1/fixture-run/artifacts'}}
        def tracking(request, timeout):
            sent.append((request.full_url, request.data))
            if '/experiments/search' in request.full_url:
                answer = {'experiments': [{'experiment_id': '1'}]}
            elif '/runs/search' in request.full_url:
                answer = {'runs': []}
            elif '/runs/create' in request.full_url:
                answer = {'run': run}
            else:
                answer = {}
            return io.BytesIO(json.dumps(answer).encode())
        tracker = results.Results(directory=self.root / 'tracking-outbox', tracking_uri='https://example.com')
        with mock.patch('urllib.request.OpenerDirector.open', side_effect=tracking):
            for row in (candidate, baseline):
                self.assertEqual(tracker.log(row)['status'], 'synced')
        exported += str(sent)
        self.assertFalse(any(url.endswith('/per_query.json') for url, _ in sent))
        for private in ('secret-query-', 'secret-doc-', 'private-passage-sentinel', 'private-question-sentinel',
                        'other-passage-sentinel', self.identity.read_text().strip(), str(self.mount), 'identity.txt'):
            self.assertNotIn(private, exported)
        self.assertTrue(self.ephemeral_paths)
        self.assertFalse(any((path / 'vectors').exists() for path in self.ephemeral_paths))
        self.assertTrue(all(not path.exists() for path in self.ephemeral_paths))
        before = len(self.calls)
        self.artifact.unlink()
        replay = self.run_trial()
        self.assertEqual(replay['status'], 'reused')
        self.assertEqual(len(self.calls), before)
        self.assertEqual(replay['record'], candidate)

    def test_wrong_fingerprint_and_heldout_key_fail_before_provider_or_publication(self):
        held_key = self.root / 'heldout-key'
        subprocess.run(['age-keygen', '-o', str(held_key)], check=True, capture_output=True)
        held_recipient = subprocess.check_output(['age-keygen', '-y', str(held_key)], text=True).strip()
        held_artifact = self.mount / 'heldout.tar.gz.age'
        subprocess.run(['age', '-r', held_recipient, '-o', str(held_artifact), str(self.root / 'working.tar.gz')],
                       check=True, capture_output=True)
        for problem in ('ciphertext', 'fingerprint', 'heldout-key'):
            with self.subTest(problem=problem):
                self.campaign = uuid.uuid4().hex
                value = copy.deepcopy(self.value)
                ref = dict(self.reference)
                if problem == 'ciphertext':
                    value['sets'][self.name]['input']['digest'] = 'c' * 64
                elif problem == 'fingerprint':
                    value['sets'][self.name]['input']['fingerprint'] = 'c' * 64
                else:
                    ref['artifact'] = held_artifact.name
                    value['sets'][self.name]['input']['digest'] = trec.sha256_file(held_artifact)
                self.policy = modal_search.policy(value)
                self.environment['EVAL_WORKING_RUNTIME'] = json.dumps({self.name: ref})
                with self.assertLogs('modal_search', level='INFO') as logs:
                    outcome = self.run_trial()
                self.assertEqual(outcome['status'], 'failed')
                self.assertEqual(self.calls, [])
                self.assertTrue(all(not path.exists() for path in self.ephemeral_paths))
                self.assertNotIn('secret-query', '\n'.join(logs.output))
                self.assertNotIn(str(self.mount), '\n'.join(logs.output))
                if problem == 'heldout-key':
                    self.assertIn('error=DecryptionError', '\n'.join(logs.output))
                with self.store.transaction() as db:
                    count = db.execute('SELECT count(*) FROM eval_control.leases WHERE campaign=%s AND payload IS NOT NULL',
                                       (self.campaign,)).fetchone()[0]
                    self.assertEqual(count, 0)
