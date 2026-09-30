"""The core.ingest plugin step of the local verification harness (THE-777).

core.ingest (plugins/core-ingest) needs the stack's TEI and the pinned
tokenizer, so `make check` only vets and unit-tests it; here, on the stack:

1. its parity test reproduces testdata/golden.json, captured from the engine's
   own segmentation and TEI embedding before they moved into the plugin: the
   same segments, offsets and derivations, and the same float32 vectors;
2. `quivr plugin test` certifies it with a fixture carrying the stack's pin
   configuration (report: core-ingest-contract-report.json).
"""
import json, os, pathlib, subprocess, time

import connector_plugin

ROOT = pathlib.Path(__file__).resolve().parents[1]
PLUGIN = ROOT / 'plugins' / 'core-ingest'
ARTICLE = ROOT / 'contracts' / 'plugins' / 'v0' / 'fixtures' / 'ingestion' / 'article.json'


def pin_configuration(stack):
    row = next(r for r in connector_plugin.FIRST_PARTY if r['id'] == 'core-ingest')
    return row['configuration'](stack)


def fixture(stack):
    """The normative article with the stack's configuration and the one window core.ingest cuts from it."""
    article = json.loads(ARTICLE.read_text())['ingestion']
    body = next(p for p in article['parts'] if p['role'] == 'body')
    doc = {'description': 'The normative article with this stack\'s core.ingest configuration: one window, the whole body.',
           'ingestion': {**article, 'configuration': pin_configuration(stack),
                         'expect': {'segments': [{'part_key': body['key'], 'start': 0, 'end': len(body['text'])}], 'lexical_text': False}}}
    path = stack.directory / 'core-ingest-fixture.json'
    path.write_text(json.dumps(doc))
    return path


def verify(stack):
    started = time.monotonic()
    config = stack.directory / 'core-ingest-parity.json'
    config.write_text(json.dumps(pin_configuration(stack)))
    stack.go_test(['-count=1', '-run', '^TestReproducesTheEngineGoldens$', '-v', '.'], {**os.environ, 'QUIVR_CORE_INGEST_CONFIG': str(config)}, 'core-ingest-parity', cwd=PLUGIN)
    report = stack.directory / 'core-ingest-contract-report.json'
    log = stack.directory / 'core-ingest-contract.log'
    with log.open('w') as out:
        code = subprocess.run([str(stack.directory / 'quivr'), 'plugin', 'test', '--startup-timeout', '120s', '--report', str(report), '--fixture', str(fixture(stack)), '.'],
                              cwd=PLUGIN, stdout=out, stderr=subprocess.STDOUT).returncode
    text = log.read_text()
    assert code == 0 and '\nCERTIFIED' in '\n' + text and 'PASS  segments_only' in text, f'quivr plugin test did not certify core.ingest; inspect {log}'
    (stack.directory / 'core-ingest.json').write_text(json.dumps({'parity': 'passed', 'certified': True, 'seconds': round(time.monotonic() - started, 1)}))
