"""Promotion owner proof: real SQL and isolated Git; fake provider boundaries."""
import copy
import importlib.util
import os
import subprocess
import uuid
import json
import pathlib
import shutil
import tempfile
import unittest


class UnavailableConfirmation(unittest.TestCase):
    def test_smoke_runner_cannot_start_confirmation_or_enable_promotion(self):
        import campaign_promotion as promotion
        # Missing integration must not even inspect state, consume a read or launch.
        result = promotion.confirm(None, 'campaign', 0, None)
        self.assertEqual(result['status'], 'pending_confirmation')
        self.assertIn('smoke', result['reason'])


@unittest.skipUnless(importlib.util.find_spec('yaml'), 'requires PyYAML')
class ConfigurationMapping(unittest.TestCase):
    def test_applies_settings_and_checks_manifest_preserving_runtime_references(self):
        import campaign_promotion as promotion
        baseline = {'model': 'Cohere-Embed-V5-Pro', 'revision': '2026-10-03', 'dimensions': 1024,
                    'window_chars': 6000, 'overlap_chars': 200, 'dense_weight': .5,
                    'candidate_count': 30, 'reranker': 'none'}
        candidate = {**baseline, 'model': 'Cohere-Embed-V5-Fast', 'dimensions': 512,
                     'dense_weight': .7, 'candidate_count': 50}
        with tempfile.TemporaryDirectory() as directory:
            checkout = pathlib.Path(directory)
            target = checkout / 'deploy/railway/core-entrypoint.py'
            target.parent.mkdir(parents=True)
            shutil.copyfile(promotion.ROOT / 'deploy/railway/core-entrypoint.py', target)
            retrieve_manifest = checkout / 'plugins/core-retrieve/quivr-plugin.yaml'
            retrieve_manifest.parent.mkdir(parents=True)
            shutil.copyfile(promotion.ROOT / 'plugins/core-retrieve/quivr-plugin.yaml', retrieve_manifest)
            manifest = {'configuration': {'schema': {'properties': {
                key: {'const': value} for key, value in {
                    'format': 'cohere', 'base_url': 'https://runtime.invalid/providers/cohere/v2',
                    'auth': 'api-key', 'model': 'Cohere-Embed-V5-Fast', 'dimensions': 512,
                    'document_input_type': 'search_document', 'query_input_type': 'search_query',
                    'max_tokens_per_segment': 6144, 'overlap': 192, 'max_batch_tokens': 98304,
                    'usd_per_million_tokens': .08}.items()}}},
                'contributions': {'ingestion': {'spaces': {'hosted.text': {
                    'model': 'Cohere-Embed-V5-Fast', 'dimensions': 512,
                    'input_price': {'usd_per_million_tokens': .08}}}}}}
            effective = promotion.promotion_apply(checkout, candidate, baseline,
                                                   builder=lambda *_: manifest)
            self.assertEqual(effective['retrieve'], {'dense_weight': .7, 'candidate_count': 50,
                                                    'hybrid_fusion': 'ranked'})
            self.assertEqual(effective['hosted']['model'], 'Cohere-Embed-V5-Fast')
            self.assertEqual(effective['hosted']['auth'], 'api-key')
            self.assertEqual(effective['hosted']['base_url'],
                             'https://runtime.invalid/providers/cohere/v2')
            self.assertEqual(effective['hosted']['max_tokens_per_segment'], 6144)
            # A configure transport returning the wrong locked manifest is blocked.
            manifest['configuration']['schema']['properties']['dimensions']['const'] = 1024
            with self.assertRaisesRegex(ValueError, 'manifest'):
                promotion.promotion_apply(checkout, candidate, baseline, builder=lambda *_: manifest)
            for changed in ({'window_chars': 4000}, {'reranker': 'jev'}, {'candidate_count': 101}):
                with self.subTest(changed=changed), self.assertRaises(ValueError):
                    promotion.promotion_apply(checkout, {**candidate, **changed}, baseline,
                                              builder=lambda *_: manifest)
            import yaml
            obsolete = yaml.safe_load(retrieve_manifest.read_text())
            obsolete.pop('configuration')
            retrieve_manifest.write_text(yaml.safe_dump(obsolete))
            with self.assertRaisesRegex(ValueError, 'retrieval configuration'):
                promotion.promotion_apply(checkout, candidate, baseline, builder=lambda *_: manifest)


@unittest.skipUnless(os.environ.get('EVAL_CONTROL_TEST_DSN') and importlib.util.find_spec('yaml'),
                     'requires disposable PostgreSQL and PyYAML')
class TrustedPromotion(unittest.TestCase):
    def setUp(self):
        import yaml
        import psycopg
        import campaign_store
        import campaign_promotion as promotion
        from unittest import mock
        patch = mock.patch.dict(os.environ, MLFLOW_TRACKING_URI='https://results.example.invalid',
                                MLFLOW_PRIVATE_TRACKING_URI='https://private-results.example.invalid')
        patch.start()
        self.addCleanup(patch.stop)
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.repository = pathlib.Path(self.directory.name) / 'repository'
        self.repository.mkdir()
        for folder in ('scripts/eval', 'plugins/hosted-embed', 'plugins/core-retrieve', 'plugins/core-ingest'):
            shutil.copytree(promotion.ROOT / folder, self.repository / folder,
                            ignore=shutil.ignore_patterns('__pycache__', 'results'))
        target = self.repository / promotion.TARGET
        target.parent.mkdir(parents=True)
        shutil.copyfile(promotion.ROOT / promotion.TARGET, target)
        self.git('init', '-b', 'main')
        self.git('config', 'user.name', 'Evaluation Test')
        self.git('config', 'user.email', 'evaluation@example.invalid')
        self.git('add', '.')
        self.git('commit', '-qm', 'feat(eval): establish neutral baseline')
        self.sha = self.git('rev-parse', 'HEAD').strip()
        self.spec = yaml.safe_load((promotion.ROOT / 'scripts/eval/examples/search-campaign.yaml').read_text())
        self.name = self.spec['name'] = uuid.uuid4().hex
        self.dsn = os.environ['EVAL_CONTROL_TEST_DSN']
        with psycopg.connect(self.dsn) as db:
            db.execute((promotion.ROOT / 'deploy/mlflow/eval-control.sql').read_text())
        self.store = campaign_store.CampaignStore(self.dsn)
        scorer = 'sha256:' + __import__('search_trial').digest({
            name: (self.repository / 'scripts/eval' / name).read_text()
            for name in ('scoring.py', 'gates.py', 'search_trial.py', 'embeddings.py', 'direct_bakeoff.py')})
        self.store.register(self.spec, self.sha, scorer)
        self.owner = self.store.acquire(self.name)
        self.candidate = {**self.spec['policy']['baseline'], 'model': 'Cohere-Embed-V5-Fast',
                          'dimensions': 512, 'dense_weight': .7, 'candidate_count': 50}
        report = {'status': 'exploration_finalist', 'verdict': 'better',
                  'gates': {k: {'passed': True} for k in promotion.GATES},
                  'aggregate_sets': {}, 'evidence': [{'result_key': 'e' * 64, 'run_id': 'dev-run', 'status': 'synced'}]}
        self.store.trial(self.name, self.owner, 0, {'config': self.candidate, 'report': report})
        self.running = set()
        self.stopped = []
        parent = self
        class Compute:
            def stop(self, app):
                parent.stopped.append(app)
                parent.running.discard(app)
            def running(self, app):
                return app in parent.running
        self.compute = Compute()

    def git(self, *args):
        return subprocess.check_output(['git', *args], cwd=self.repository, text=True,
                                       stderr=subprocess.DEVNULL)

    def adapter(self, mutate=None):
        parent = self
        class Adapter:
            heldout_fingerprint = 'c' * 64
            heldout_family = {'version': 1, 'name': 'heldout-family', 'split': 'heldout',
                              'digest': 'sha256:' + 'c' * 64, 'privacy': 'private',
                              'sets': {'heldout-1': {'version': 'v1', 'split': 'heldout',
                                                    'fingerprint': 'a' * 64, 'private': True}}}
            engine_git_sha = parent.sha
            engine_scorer_digest = 'sha256:' + 'a' * 64
            confirmation_policy_digest = 'f' * 64
            def __call__(self, request, resource):
                ordinal = parent.store.confirmation(parent.name)
                parent.running.add('ap-confirmation')
                resource.bind('ap-confirmation')
                resource.check()
                result = {'status': 'confirmed', 'confirmation_available': True,
                          'bindings': copy.deepcopy(request), 'aggregate_sets': {},
                          'gates': {k: {'passed': True} for k in ('quality', 'no_loss', 'latency', 'price')},
                          'heldout': {'passed': True, 'fingerprint': self.heldout_fingerprint},
                          'receipts': [{'result_key': 'd' * 64, 'run_id': 'aggregate-run', 'status': 'synced'}],
                          'compute_ids': ['ap-confirmation'],
                          'confirmation_key': 'confirmation/' + 'b' * 64, 'read_ordinal': ordinal,
                          'cleanup_verified': True}
                if mutate:
                    mutate(result)
                return result
        return Adapter()

    def test_confirmation_binds_actual_engine_and_rejects_stale_private_or_partial_results(self):
        import campaign_promotion as promotion
        for mutate in (
            lambda r: r['bindings'].update(candidate_hash='f' * 64),
            lambda r: r['gates']['quality'].update(passed=False),
            lambda r: r['heldout'].update(passed=False),
            lambda r: r['receipts'][0].update(status='pending'),
            lambda r: r.update(per_query={'private-query': .7}),
            lambda r: r.update(compute_ids=['ap-unregistered']),
            lambda r: r.pop('heldout'),
            lambda r: r.update(read_ordinal=True),
        ):
            with self.subTest(mutation=mutate):
                result = promotion.confirm(self.store, self.name, 0, self.owner,
                    adapter=self.adapter(mutate), repository=self.repository, compute=self.compute)
                self.assertEqual(result['status'], 'pending_confirmation')
                self.assertNotIn('private-query', json.dumps(result))
                self.assertFalse(self.running)
        result = promotion.confirm(self.store, self.name, 0, self.owner,
            adapter=self.adapter(), repository=self.repository, compute=self.compute)
        self.assertEqual(result['status'], 'confirmed')
        saved = self.store.snapshot(self.name)['confirmations']['0']['receipt']
        self.assertEqual(saved['bindings']['effective_baseline']['retrieve']['hybrid_fusion'], 'relative_score')
        self.assertEqual(saved['bindings']['effective_candidate']['retrieve']['hybrid_fusion'], 'ranked')
        self.assertFalse(self.running)
        self.assertEqual(self.store.availability(self.name)['confirmation_reads_left'], 1)
        self.assertTrue(all(r['status'] == 'closed' for r in self.store.snapshot(self.name)['resources'].values()))


    def test_promotion_recovers_lost_github_response_and_blocks_relevant_main_drift(self):
        import campaign_store
        import campaign_promotion as promotion
        parent = self
        class GitHub:
            calls = 0
            found = None
            lose_response = True
            def find(self, branch):
                return self.found
            def create(self, checkout, branch, title, body):
                self.calls += 1
                self.found = {'url': 'https://github.com/example/engine/pull/1', 'body': body}
                self.effective = promotion.effective_settings(checkout)
                self.assert_title = title
                concurrent = promotion.promote(parent.store, parent.name, 0,
                    repository=parent.repository, github=self, builder=parent.builder)
                parent.assertEqual(concurrent['status'], 'preparing')
                if self.lose_response:
                    self.lose_response = False
                    raise TimeoutError('private-response')
                return self.found
        github = GitHub()
        self.builder = lambda *_: {'configuration': {'schema': {'properties': {
            key: {'const': value} for key, value in {
                'format': 'cohere', 'base_url': 'https://runtime.invalid/providers/cohere/v2',
                'auth': 'api-key', 'model': 'Cohere-Embed-V5-Fast', 'dimensions': 512,
                'document_input_type': 'search_document', 'query_input_type': 'search_query',
                'max_tokens_per_segment': 6144, 'overlap': 192, 'max_batch_tokens': 98304,
                'usd_per_million_tokens': .08}.items()}}},
            'contributions': {'ingestion': {'spaces': {'hosted.text': {
                'model': 'Cohere-Embed-V5-Fast', 'dimensions': 512,
                'input_price': {'usd_per_million_tokens': .08}}}}}}
        missing = promotion.promote(self.store, self.name, 0, repository=self.repository, github=github)
        self.assertEqual(missing['status'], 'blocked')
        self.assertEqual(github.calls, 0)
        # The full runner can live at a later revision than exploration. Its
        # measured engine files, rather than tier-1 source, own promotion drift.
        engine = self.repository / 'scripts/eval/engine_stack.py'
        engine.write_text(engine.read_text() + '\n# New confirmed engine runner revision.\n')
        self.git('add', 'scripts/eval/engine_stack.py')
        self.git('commit', '-qm', 'feat(eval): configure trusted runner')
        adapter = self.adapter()
        adapter.engine_git_sha = self.git('rev-parse', 'HEAD').strip()
        promotion.confirm(self.store, self.name, 0, self.owner, adapter=adapter,
                          repository=self.repository, compute=self.compute)
        # Unrelated main drift can be included without invalidating engine proof.
        (self.repository / 'notice.txt').write_text('Neutral operator note.\n')
        self.git('add', 'notice.txt')
        self.git('commit', '-qm', 'docs: add neutral notice')
        first = promotion.promote(self.store, self.name, 0, repository=self.repository,
                                  github=github, builder=self.builder)
        self.assertEqual(first['status'], 'retry')
        self.assertNotIn('private-response', json.dumps(first))
        restarted = campaign_store.CampaignStore(self.dsn)
        second = promotion.promote(restarted, self.name, 0, repository=self.repository,
                                   github=github, builder=self.builder)
        self.assertEqual(second['status'], 'opened')
        self.assertEqual(github.calls, 1)
        self.assertEqual(github.effective['retrieve']['hybrid_fusion'], 'ranked')
        self.assertTrue(github.assert_title.startswith('feat(search):'))
        self.assertIn('quality=pass', github.found['body'])
        self.assertIn('held-out=pass', github.found['body'])
        self.assertIn('d' * 64, github.found['body'])
        self.assertIn('https://results.example.invalid/api/2.0/mlflow/runs/get?run_id=aggregate-run', github.found['body'])
        for bad in ('https://private-results.example.invalid', 'https://user:secret@results.example.invalid',
                    'https://results.example.invalid/private'):
            denied = promotion.promote(restarted, self.name, 0, repository=self.repository,
                                       github=github, builder=self.builder, aggregate_tracking_uri=bad)
            self.assertEqual(denied['status'], 'blocked')
            self.assertIn('evidence link unavailable', denied['reason'])
            self.assertNotIn('secret', json.dumps(denied))
        # Ingestion changes invalidate proof even with unchanged retrieval/scorer.
        ingestion = self.repository / 'plugins/core-ingest/quivr-plugin.yaml'
        ingestion.write_text(ingestion.read_text() + '\n# Changed ingestion defaults.\n')
        self.git('add', 'plugins/core-ingest/quivr-plugin.yaml')
        self.git('commit', '-qm', 'fix(ingestion): change ingestion settings')
        stale = promotion.promote(restarted, self.name, 0, repository=self.repository,
                                  github=github, builder=self.builder)
        self.assertEqual(stale['status'], 'blocked')
        self.assertIn('remeasure', stale['reason'])
        self.assertEqual(github.calls, 1)


    def test_default_advancement_persists_pending_and_cleanup_failure_cannot_publish_proof(self):
        from unittest import mock
        import campaign_promotion as promotion
        import campaign_store
        outcomes = promotion.advance(self.store, self.name, self.owner,
                                     repository='/unavailable-repository')
        self.assertEqual(outcomes[0]['status'], 'pending_confirmation')
        restarted = campaign_store.CampaignStore(self.dsn)
        self.assertEqual(promotion.confirmation_status(restarted, self.name, 0)['status'],
                         'pending_confirmation')
        self.assertEqual(restarted.snapshot(self.name)['resources'], {})
        self.assertEqual(restarted.availability(self.name)['confirmation_reads_left'], 10)
        with mock.patch.object(self.compute, 'stop', side_effect=TimeoutError('private-provider-error')):
            outcome = promotion.confirm(restarted, self.name, 0, self.owner, adapter=self.adapter(),
                                        repository=self.repository, compute=self.compute)
        self.assertEqual(outcome['status'], 'pending_confirmation')
        self.assertIn('cleanup pending', outcome['reason'])
        self.assertNotIn('private-provider-error', json.dumps(promotion.public_status(restarted.snapshot(self.name))))
        self.assertNotIn('receipt', restarted.snapshot(self.name)['confirmations']['0'])
        self.assertEqual(promotion.promote(restarted, self.name, 0)['status'], 'blocked')
        self.assertEqual(restarted.availability(self.name)['confirmation_reads_left'], 9)
        # The usual stopped-campaign lifecycle retries termination; no special
        # confirmation cleanup path or second held-out admission is needed.
        self.store.stop(self.name)
        self.compute.find = lambda label: ['ap-confirmation']
        campaign_store.cleanup(restarted, self.name, self.compute)
        self.assertTrue(all(r['status'] == 'closed' for r in restarted.snapshot(self.name)['resources'].values()))
        self.assertFalse(self.running)
        self.assertEqual(restarted.availability(self.name)['confirmation_reads_left'], 9)


    def test_canonical_replay_and_unavailable_before_invoke_leave_no_orphan_intent(self):
        import campaign_promotion as promotion
        saved = {}
        adapter = self.adapter()
        def unavailable(request, resource):
            return {'status': 'unavailable', 'confirmation_available': False}
        for key in ('heldout_fingerprint', 'heldout_family', 'engine_git_sha',
                    'engine_scorer_digest', 'confirmation_policy_digest'):
            setattr(unavailable, key, getattr(adapter, key))
        outcome = promotion.confirm(self.store, self.name, 0, self.owner, adapter=unavailable,
                                    repository=self.repository, compute=self.compute)
        self.assertEqual(outcome['status'], 'pending_confirmation')
        self.assertEqual(self.store.snapshot(self.name)['resources'], {})
        self.assertEqual(self.store.availability(self.name)['confirmation_reads_left'], 10)
        # Simulate canonical engine publication followed by supervisor proof loss:
        # a replay uses the already closed registered app and the same read ordinal.
        def publish_then_lose(result):
            saved.update(copy.deepcopy(result))
            result.pop('heldout')
        first = promotion.confirm(self.store, self.name, 0, self.owner,
            adapter=self.adapter(publish_then_lose), repository=self.repository, compute=self.compute)
        self.assertEqual(first['status'], 'pending_confirmation')
        before = self.store.snapshot(self.name)['resources']
        def replay(request, resource):
            resource.check()  # checking ownership does not create a compute intent
            return copy.deepcopy(saved)
        for key in ('heldout_fingerprint', 'heldout_family', 'engine_git_sha',
                    'engine_scorer_digest', 'confirmation_policy_digest'):
            setattr(replay, key, getattr(adapter, key))
        second = promotion.confirm(self.store, self.name, 0, self.owner, adapter=replay,
                                   repository=self.repository, compute=self.compute)
        self.assertEqual(second['status'], 'confirmed')
        self.assertEqual(self.store.snapshot(self.name)['resources'], before)
        self.assertEqual(self.store.availability(self.name)['confirmation_reads_left'], 9)


if __name__ == '__main__':
    unittest.main()
