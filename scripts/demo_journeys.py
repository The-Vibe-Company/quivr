"""Walks the demo's main journeys on a throwaway stack (Spec 26; make demo-journeys).

One RSS source from the local fake feeds site (never the internet) gives the
Fil, search and the Explorer articles to read, then the TesterArmy e2e runner
in quivr-search/journeys runs every journey, the ones that write included.
"""
import os
import subprocess
import time
import urllib.parse
from datetime import datetime, timedelta, timezone
import ci_dependencies
from demo_perf import Demo

ARTICLES = 30


def seed(demo, feeds_url):
    corpus = demo.call('/demo/session')['corpus_id']
    demo.call('/v0/connectors', {
        'idempotency_key': 'demo-journeys-source', 'corpus_id': corpus, 'kind': 'rss',
        'source_namespace': 'Source exemple', 'schedule': {'interval_seconds': 3600},
        'config': {'url': f'{feeds_url}/feeds/bulk.xml?source=1&items={ARTICLES}'}})
    tomorrow = (datetime.now(timezone.utc) + timedelta(days=1)).replace(hour=0, minute=0, second=0, microsecond=0)
    bound = urllib.parse.quote(tomorrow.isoformat())
    deadline = time.monotonic() + 600
    while demo.call(f'/demo/feed/days?bounds={bound}')['total'] < ARTICLES:
        if time.monotonic() > deadline:
            raise RuntimeError(f'demo-journeys: {ARTICLES} articles not stored within 600 s')
        time.sleep(2)


def run(base, password, feeds_url, root):
    seed(Demo(base, password), feeds_url)
    journeys = root / 'quivr-search/journeys'
    ci_dependencies.run_dependency(['npm', 'ci', '--prefix', str(journeys)])
    # e2e keeps its results inside its own project: the report is not under the stack's artifacts.
    try:
        subprocess.run(['npm', 'run', 'journeys', '--prefix', str(journeys)], check=True, env={
            **os.environ, 'QUIVR_DEMO_URL': base, 'QUIVR_DEMO_PASSWORD': password, 'QUIVR_DEMO_FEEDS_URL': feeds_url,
            'E2E_TELEMETRY_DISABLED': '1'})
    finally:
        print('Journeys report:', journeys / '.e2e/report.json', flush=True)
