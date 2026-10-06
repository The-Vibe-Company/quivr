"""Offline campaign cost, serving lifecycle and local-only dispatch contracts."""
import contextlib
import io
import json
import os
import threading
import types
import pathlib
import tempfile
import unittest
from unittest import mock

import oss_bakeoff
import embeddinggemma_server


class Campaign(unittest.TestCase):
    def test_gemma_plan_and_client_apply_retrieval_prompts_once(self):
        # External prompt/dimension contract, through the real benchmark client.
        with tempfile.TemporaryDirectory() as directory, mock.patch.dict('sys.modules', {'modal': None}):
            out = pathlib.Path(directory) / 'plan.json'
            oss_bakeoff.main(['plan', '--out', str(out), '--models', 'embeddinggemma-2',
                'embeddinggemma-2-256', '--hardware', 'L4', '--sets', 'scifact'])
            jobs = json.loads(out.read_text())['jobs']
        self.assertEqual([job['configuration']['dimensions'] for job in jobs], [768, 256])
        for job in jobs:
            config = job['configuration']
            self.assertEqual(config['model'], 'google/embeddinggemma-2')
            self.assertEqual(job['model_revision'], '914f7f89142e33e77833254d9c9b90c3cef7303b')
            self.assertEqual(config['model_revision'], job['model_revision'][:16])
            self.assertEqual(config['max_tokens_per_segment'], 8192)
            self.assertIn('sentence-transformers==6.1.0', job['image'])
            budget = oss_bakeoff.direct_bakeoff.embeddings.Budget(1000, 1)
            client = oss_bakeoff.direct_bakeoff.OpenAI(config, budget, 'scifact', job['model'])
            model = mock.Mock()
            import numpy as np
            model.encode.return_value = np.tile([3., 4., 0.], 256).reshape(1, 768)
            def open_request(request, **kwargs):
                body = json.loads(request.data)
                return contextlib.closing(io.BytesIO(json.dumps(
                    embeddinggemma_server.encode_request(model, body)).encode()))
            with mock.patch.object(client.opener, 'open', side_effect=open_request):
                for mode, expected in [('query', 'task: search result | query: example'),
                                       ('document', 'title: none | text: example')]:
                    vector = client.embed(['example'], mode)[0]
                    self.assertEqual(len(vector), config['dimensions'])
                    self.assertAlmostEqual(sum(x * x for x in vector), 1., places=6)
                    norm = np.linalg.norm(model.encode.return_value[0, :config['dimensions']])
                    np.testing.assert_allclose(vector, model.encode.return_value[0, :config['dimensions']] / norm)
                    self.assertEqual(model.encode.call_args.args[0], [expected])
                    self.assertEqual(model.encode.call_args.kwargs['prompt'], '')
            with self.assertRaises(ValueError):
                embeddinggemma_server.encode_request(model, {'model': config['model'], 'input': ['example'], 'dimensions': 384})

    def test_gemma_load_checks_pinned_licence_and_disables_unused_encoders(self):
        loader = mock.Mock()
        card = mock.Mock()
        torch = types.SimpleNamespace(float32='float32', bfloat16='bfloat16')
        with mock.patch.dict('sys.modules', {'huggingface_hub': types.SimpleNamespace(ModelCard=card),
                'sentence_transformers': types.SimpleNamespace(SentenceTransformer=loader), 'torch': torch}):
            for licence in ('gemma', None, 'cc-by-4.0'):
                card.load.return_value.data.license = licence
                with self.assertRaisesRegex(ValueError, 'licence'):
                    embeddinggemma_server.load_model('cpu')
                loader.assert_not_called()
            for hardware, device, dtype in [('cpu', 'cpu', 'float32'), ('L4', 'cuda', 'bfloat16')]:
                card.load.return_value.data.license = 'apache-2.0'
                embeddinggemma_server.load_model(hardware)
                card.load.assert_called_with('google/embeddinggemma-2', revision='914f7f89142e33e77833254d9c9b90c3cef7303b')
                self.assertEqual(loader.call_args.kwargs['config_kwargs'], {'vision_config': None, 'audio_config': None})
                self.assertEqual(loader.call_args.kwargs['device'], device)
                self.assertEqual(loader.call_args.kwargs['model_kwargs'], {'torch_dtype': dtype})

    def test_gemma_measure_records_index_throughput_and_cold_start(self):
        process = mock.Mock()
        process.poll.return_value = None
        def child(command, **kwargs):
            out = pathlib.Path(command[command.index('--out') + 1])
            out.write_text(json.dumps({'set': 'scifact', 'status': 'complete', 'documents': 10,
                'results': {'embeddinggemma-2-256': {'duration_seconds': 5, 'index_s': 2, 'pieces': 12}},
                'by_model': {'embeddinggemma-2-256': {'confirmed_input_tokens': 0, 'reserved_input_tokens': 20}}}))
            return types.SimpleNamespace(returncode=0)
        with mock.patch.object(oss_bakeoff.subprocess, 'Popen', return_value=process) as start, \
             mock.patch.object(oss_bakeoff.subprocess, 'run', side_effect=child), \
             mock.patch.object(oss_bakeoff, 'wait_ready'):
            result = oss_bakeoff.measure('embeddinggemma-2-256', 'L4', ['scifact'], 'a' * 40, 1000, 600, False)
        self.assertEqual(result['campaign']['status'], 'complete')
        serving = result['reports'][0]['results']['embeddinggemma-2-256']['serving']
        self.assertEqual(serving['documents_per_second'], 5)
        self.assertEqual(serving['document_pieces_per_second'], 6)
        self.assertIsNotNone(serving['cold_start_seconds'])
        self.assertEqual(serving['dtype'], 'bfloat16')
        self.assertIsNone(serving['input_tokens'])  # adapter does not claim tokenizer usage
        self.assertIn('embeddinggemma_server.py', start.call_args.args[0][1])
        process.terminate.assert_called_once()
        process.wait.assert_called_once()

    def test_dry_run_needs_no_modal_and_accounts_both_resource_types(self):
        # Owner boundary: an operator gets a finite resource estimate before dispatch.
        with tempfile.TemporaryDirectory() as directory:
            out = pathlib.Path(directory) / 'plan.json'
            with mock.patch.dict('sys.modules', {'modal': None}):
                code = oss_bakeoff.main(['plan', '--out', str(out), '--models', 'qwen3',
                                         '--hardware', 'cpu', 'L4', '--sets', 'scifact', '--timeout', '600'])
            plan = json.loads(out.read_text())
            self.assertEqual(code, 0)
            self.assertEqual(len(plan['jobs']), 2)
            self.assertAlmostEqual(plan['jobs'][0]['hourly_usd'], .252576)
            self.assertAlmostEqual(plan['jobs'][1]['hourly_usd'], 1.051776)
            self.assertAlmostEqual(plan['estimated_compute_usd'], .217392)
            self.assertEqual(plan['concurrency'], 4)
            self.assertEqual(plan['jobs'][0]['set'], 'scifact')
            self.assertEqual(plan['sets'], ['scifact'])
            self.assertFalse(plan['include_restricted'])
            with self.assertRaises(FileExistsError):
                oss_bakeoff.main(['plan', '--out', str(out)])
            with mock.patch.dict(os.environ, {'CI': 'false', 'GITHUB_ACTIONS': ''}, clear=True), \
                 mock.patch.object(oss_bakeoff.subprocess, 'run', side_effect=AssertionError('work reached without consent')), \
                 contextlib.redirect_stderr(io.StringIO()) as diagnostic:
                with self.assertRaises(SystemExit) as refusal:
                    oss_bakeoff.main(['run', '--out', str(pathlib.Path(directory) / 'unapproved')])
            self.assertEqual(refusal.exception.code, 2)
            self.assertIn('--acknowledge-cost', diagnostic.getvalue())

    def test_parallel_jobs_isolate_failures_and_reuse_reference_artifacts(self):
        # CLI owns scheduling and evidence; fake only the paid transport.
        references, active, peak = [], 0, 0
        lock = threading.Lock()
        barrier = threading.Barrier(2)
        def dispatch(label, hardware, sets, sha, tokens, timeout, restricted, reference=None):
            nonlocal active, peak
            name = sets[0]
            if label == 'reference':
                references.append(name)
                report = {'status': 'complete', 'set': name, 'fingerprint': name,
                          'settings': {'e5_reference_identity': oss_bakeoff.direct_bakeoff.reference_identity()}, 'results': {oss_bakeoff.direct_bakeoff.BASELINE: {'mean': {}, 'per_query': {}}}}
            else:
                self.assertIsNotNone(reference)
                self.assertEqual(tokens, 50)  # original pair budget stays bounded
                with lock:
                    active += 1
                    peak = max(peak, active)
                try:
                    barrier.wait(timeout=5)
                finally:
                    with lock:
                        active -= 1
                if label == 'granite-r2' and name == 'scifact':
                    raise RuntimeError('private provider input')
                report = {'status': 'complete', 'set': name}
            return {'campaign': {'status': 'complete', 'model': label, 'hardware': hardware,
                    'requested_sets': sets, 'completed_sets': sets, 'elapsed_seconds': 1,
                    'estimated_usd': .01, 'modal_app_id': 'fixture-app'}, 'reports': [report]}
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            transport = types.SimpleNamespace(dispatch=dispatch)
            for attempt in range(4):
                if attempt >= 2:
                    cached = root / 'cache' / oss_bakeoff.direct_bakeoff.reference_identity() / 'scifact.json'
                    cached.write_text('{}' if attempt == 2 else '[]')
                out = root / str(attempt)
                with mock.patch.dict(os.environ, {'CI': '', 'GITHUB_ACTIONS': ''}), \
                     mock.patch.dict('sys.modules', {'oss_modal': transport}), \
                     contextlib.redirect_stdout(io.StringIO()) as progress:
                    code = oss_bakeoff.main(['run', '--out', str(out), '--models', 'granite-r2', 'arctic-v2',
                        '--hardware', 'L4', '--sets', 'scifact', 'miracl-fr', '--concurrency', '2',
                        '--max-input-tokens', '100', '--reference-cache', str(root / 'cache'), '--acknowledge-cost'])
                self.assertEqual(code, 2)
                campaign = json.loads((out / 'granite-r2-L4-campaign.json').read_text())
                self.assertEqual(campaign['completed_sets'], ['miracl-fr'])
                self.assertEqual(campaign['status'], 'partial')
                self.assertEqual(campaign['jobs'][0]['reason']['kind'], 'provider_error' if attempt < 2 else 'reference_failed')
                self.assertEqual((out / 'arctic-v2-L4-scifact.json').exists(), attempt < 2)
                self.assertTrue((out / 'arctic-v2-L4-miracl-fr.json').exists())
                self.assertNotIn('private provider input', json.dumps(campaign) + progress.getvalue())
                self.assertNotIn('granite', progress.getvalue())
            self.assertCountEqual(references, ['scifact', 'miracl-fr'])
            self.assertEqual(peak, 2)

    def test_server_cleanup_and_reasons_for_incomplete_jobs(self):
        import subprocess
        cases = [('exit', 'tei_exit'), ('timeout', 'timeout'), ('timeout_json', 'timeout'), ('cap', 'token_cap'), ('provider', 'provider_error')]
        for failure, kind in cases:
            with self.subTest(failure=failure):
                process = mock.Mock()
                process.poll.return_value = 17 if failure == 'exit' else None
                def popen(command, **kwargs):
                    kwargs['stderr'].write(b'backend initialization failed\n')
                    kwargs['stderr'].flush()
                    return process
                def child(command, **kwargs):
                    if failure in ('timeout', 'timeout_json'):
                        if failure == 'timeout_json':
                            output = pathlib.Path(command[command.index('--out') + 1])
                            output.write_text('{"status":')
                        raise subprocess.TimeoutExpired(command, 1)
                    output = pathlib.Path(command[command.index('--out') + 1])
                    output.write_text(json.dumps({'set': 'scifact', 'status': 'capped' if failure == 'cap' else 'failed',
                        'results': {}, 'budget': {'budgeted_input_tokens': 10}}))
                    return types.SimpleNamespace(returncode=2)
                with mock.patch.object(oss_bakeoff.subprocess, 'Popen', side_effect=popen), \
                     mock.patch.object(oss_bakeoff.subprocess, 'run', side_effect=child), \
                     mock.patch.object(oss_bakeoff, 'wait_ready', side_effect=RuntimeError('startup') if failure == 'exit' else None):
                    output = oss_bakeoff.measure('granite-r2', 'cpu', ['scifact'], 'a' * 40, 1000, 600, False)
                self.assertEqual(output['campaign']['reason']['kind'], kind)
                if failure == 'exit':
                    self.assertEqual(output['campaign']['reason']['exit_code'], 17)
                    self.assertIn('backend initialization failed', output['campaign']['reason']['stderr_tail'])
                process.terminate.assert_called_once()
                process.wait.assert_called_once()


if __name__ == '__main__':
    unittest.main()
