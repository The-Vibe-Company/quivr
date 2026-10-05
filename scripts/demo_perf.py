"""Seeds the demo with a synthetic corpus, then measures it (THE-1041; make demo-perf).

The corpus: SOURCES RSS sources of ARTICLES articles each from the local fake feeds
site (never the internet), keyword alerts, then one more source that the alerts
catch articles of. Then quivr-search/perf/measure.mjs measures every page, the
interactions and the facade's endpoints against quivr-search/perf/budgets.json.
"""
import http.cookiejar
import json
import os
import subprocess
import time
import urllib.parse
import urllib.request
from datetime import datetime, timedelta, timezone

SOURCES, ARTICLES, LATE = 12, 100, 60
ALERTS = ('orage', 'port', 'grève', 'énergie', 'musée', 'élection')


class Demo:
    """The demo facade, as a browser signed in to it."""

    def __init__(self, base, password):
        self.base = base
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        if password:
            self.call('/demo/login', {'password': password})

    def call(self, path, body=None):
        request = urllib.request.Request(
            self.base + path, data=None if body is None else json.dumps(body).encode(),
            headers={'Content-Type': 'application/json', 'Origin': self.base})
        with self.opener.open(request, timeout=30) as response:
            return json.loads(response.read())


def wait_for(what, check, seconds):
    print(f'demo-perf: waiting for {what} (at most {seconds} s)', flush=True)
    deadline = time.monotonic() + seconds
    while not check():
        if time.monotonic() > deadline:
            raise RuntimeError(f'demo-perf: {what} not reached within {seconds} s')
        time.sleep(2)


def seed(demo, feeds_url):
    corpus = demo.call('/demo/session')['corpus_id']
    tomorrow = (datetime.now(timezone.utc) + timedelta(days=1)).replace(hour=0, minute=0, second=0, microsecond=0)
    bound = urllib.parse.quote(tomorrow.isoformat())
    stored = lambda: demo.call(f'/demo/feed/days?bounds={bound}')['total']

    def source(n, items):
        demo.call('/v0/connectors', {
            'idempotency_key': f'demo-perf-source-{n}', 'corpus_id': corpus, 'kind': 'rss',
            'source_namespace': f'Source exemple {n}', 'schedule': {'interval_seconds': 3600},
            'config': {'url': f'{feeds_url}/feeds/bulk.xml?source={n}&items={items}'}})

    for n in range(SOURCES):
        source(n, ARTICLES)
    wait_for(f'{SOURCES * ARTICLES} articles stored', lambda: stored() >= SOURCES * ARTICLES, 1200)
    # Alerts catch what arrives after them: one more source once they exist.
    for term in ALERTS:
        demo.call('/demo/alerts', {'idempotency_key': f'demo-perf-alert-{term.encode().hex()}', 'name': f'Alerte {term}',
                                   'expression': {'kind': 'keywords', 'match': {'term': term}}})
    source(SOURCES, LATE)
    wait_for('the last source stored', lambda: stored() >= SOURCES * ARTICLES + LATE, 600)
    # The alerts plugin judges each new article: wait until what they caught stops growing.
    caught = lambda: sum(a['match_count'] for a in demo.call('/demo/alerts')['items'])
    last = [-1]

    def settled():
        now = caught()
        done, last[0] = now == last[0] and now > 0, now
        if not done:
            time.sleep(10)
        return done
    wait_for('alerts catching the last source', settled, 900)
    # Measured on a quiet machine: nothing left waiting at any step (the
    # embedding service shares the CPU with the browser).
    idle = lambda: not any(step.get('count') for step in
                           (demo.call('/demo/admin').get('stats') or {}).get('waiting_by_step', {}).values())
    wait_for('the pipeline to be idle', idle, 900)


def run(base, password, feeds_url, directory, root):
    seed(Demo(base, password), feeds_url)
    report = directory / 'perf.json'
    subprocess.run(['npm', 'run', 'perf', '--prefix', str(root / 'quivr-search')], check=True, env={
        **os.environ, 'QUIVR_DEMO_URL': base, 'QUIVR_DEMO_PASSWORD': password,
        'PERF_ALERT_LAG': '1', 'PERF_REPORT': str(report)})
    print('Performance report:', report, flush=True)
