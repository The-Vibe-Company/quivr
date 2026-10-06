"""CI admission owner: environment values and executable work boundaries."""
import contextlib
import io
import os
import json
import pathlib
import signal
import tempfile
import unittest
from unittest import mock

import ci_guard
import engine_confirmation as confirmation


class WorkReached(BaseException):
    """Stop before any paid work, even inside broad dependency catches."""


class Admission(unittest.TestCase):
    def test_environment_flags_share_one_false_value_policy(self):
        for flags, expected in (
            ({}, False), ({'CI': ''}, False), ({'CI': '0'}, False),
            ({'CI': 'false'}, False), ({'CI': 'FaLsE'}, False),
            ({'GITHUB_ACTIONS': ''}, False), ({'GITHUB_ACTIONS': '0'}, False),
            ({'GITHUB_ACTIONS': 'false'}, False), ({'GITHUB_ACTIONS': 'FALSE'}, False),
            ({'CI': 'true'}, True), ({'CI': 'TRUE'}, True), ({'CI': '1'}, True),
            ({'GITHUB_ACTIONS': 'true'}, True), ({'GITHUB_ACTIONS': '1'}, True),
            ({'CI': 'false', 'GITHUB_ACTIONS': 'true'}, True),
            ({'CI': 'true', 'GITHUB_ACTIONS': 'false'}, True),
        ):
            with self.subTest(flags=flags), mock.patch.dict(os.environ, flags, clear=True):
                self.assertEqual(ci_guard.in_ci(), expected)

    def test_entrypoints_refuse_ci_before_work_and_allow_local_or_fake_work(self):
        for flags, blocked in (({'CI': 'true', 'GITHUB_ACTIONS': ''}, True),
                               ({'CI': '', 'GITHUB_ACTIONS': 'true'}, True),
                               ({'CI': 'false', 'GITHUB_ACTIONS': ''}, False)):
            with tempfile.TemporaryDirectory() as temporary:
                root = pathlib.Path(temporary)
                for label, invoke, owner, attribute, refusal_type in self.entrypoints(root):
                    with self.subTest(flags=flags, entrypoint=label), \
                         mock.patch.dict(os.environ, {**flags, 'EVAL_CONTROL_DATABASE_URL': 'fixture',
                             'AZURE_FOUNDRY_ENDPOINT': 'https://example.org', 'AZURE_FOUNDRY_KEY': 'fixture'}, clear=True), \
                         mock.patch.object(owner, attribute, side_effect=WorkReached), \
                         contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()) as diagnostic:
                        previous = signal.getsignal(signal.SIGTERM)
                        try:
                            expected = refusal_type if blocked and label != 'news fake' else WorkReached
                            with self.assertRaises(expected) as refusal:
                                invoke()
                            if expected is not WorkReached:
                                self.assertIn('CI', diagnostic.getvalue() + str(refusal.exception))
                                if label in ('campaign', 'run') or label.startswith('search campaign '):
                                    self.assertEqual(refusal.exception.code, 2)
                        finally:
                            signal.signal(signal.SIGTERM, previous)

    def entrypoints(self, root):
        import campaign
        import direct_bakeoff
        import modal_engine
        import modal_search
        import news_set
        import oss_bakeoff
        import quality_reports
        import run
        import search_campaign
        import campaign_store
        policy = root / 'policy.json'
        policy.write_text(json.dumps({'experiment': 'public/example', 'sets': {'scifact': {'split': 'dev'}},
            'modal_usd_per_second': .001, 'price_revision': '2026-10-03'}))
        candidate = root / 'candidate.json'
        candidate.write_text('{}')
        def command(module, args):
            with mock.patch('sys.argv', [module.__name__, *args]):
                return module.main()
        modal_args = ['--policy', str(policy), '--candidate', str(candidate), '--allow-paid', '--campaign', 'example']
        news_args = ['--report', str(root / 'news.json'), '--storage-dir', str(root / 'private')]
        cases = [
            ('campaign', lambda: command(campaign, ['--allow-paid', '--max-input-tokens', '100', '--max-usd', '8',
                '--out', str(root / 'campaign')]), run, 'git', SystemExit),
            ('direct', lambda: direct_bakeoff.main(['--set', 'scifact', '--max-input-tokens', '100', '--max-usd', '1',
                '--out', str(root / 'direct.json')]), direct_bakeoff.public_sets, 'prepare', SystemExit),
            ('modal search', lambda: modal_search.main(modal_args), modal_search, 'launch', SystemExit),
            ('modal engine', lambda: modal_engine.main(['--smoke', '--allow-paid', '--campaign', 'example']),
                modal_engine, 'launch', SystemExit),
            ('news', lambda: news_set.main(news_args + ['--providers', 'unused.py', '--articles', 'unused.json']),
                news_set, 'load_providers', SystemExit),
            ('news fake', lambda: news_set.main(news_args + ['--fake']), news_set, 'fake_providers', SystemExit),
            ('oss', lambda: oss_bakeoff.main(['run', '--out', str(root / 'oss'), '--acknowledge-cost']),
                oss_bakeoff.subprocess, 'run', SystemExit),
            ('quality', lambda: quality_reports.main(['--set', 'scifact', '--out', str(root / 'quality')]),
                quality_reports.public_sets, 'prepare', SystemExit),
            ('run', lambda: command(run, ['--out', str(root / 'run')]), run, 'git', SystemExit),
            ('confirmation', lambda: confirmation.main(['--allow-paid', '--campaign', 'example', '--trial', '0',
                '--candidate', str(candidate), '--configuration', 'unused.json']), confirmation.control_store, 'Store', SystemExit),
            ('confirmation adapter', lambda: confirmation.ModalAdapter(None, root / 'unused')({}, None),
                confirmation.subprocess, 'check_output', PermissionError),
        ]
        for args in (['start', 'unused.yaml', '--allow-paid'], ['resume', 'example', '--allow-paid'],
                     ['stop', 'example', '--allow-paid'], ['watchdog', 'example', '--allow-paid'],
                     ['promote', 'example', '0', '--open-pr'], ['confirm', 'example', '0', '--allow-paid'],
                     ['digest', 'example', '--send']):
            cases.append(('search campaign ' + args[0], lambda args=args: search_campaign.main(args),
                          campaign_store, 'CampaignStore', SystemExit))
        return cases


if __name__ == '__main__':
    unittest.main()
