#!/usr/bin/env python3
"""Prepare/run coordinator-dispatched hosted embedding measurements; never called by CI.

The live gate is enabled only with --allow-paid in a workflow_dispatch job.
--dry-run prints deployments, price sources, planned cuts and caps without
reading a key, starting a plugin or making a provider request.
"""
import argparse
import contextlib
import json
import os
import pathlib
import signal
import subprocess
import sys
import time
import types
import uuid

import embeddings
import report as render
import run

PRICES = {
    'Cohere-Embed-V5-Pro': {'usd_per_million_tokens': .12, 'date': '2026-10-03',
                          'source': 'https://cohere.com/blog/embed-5'},
    'Cohere-Embed-V5-Fast': {'usd_per_million_tokens': .08, 'date': '2026-10-03',
                           'source': 'https://cohere.com/blog/embed-5'},
    'text-embedding-3-large': {'usd_per_million_tokens': .13, 'date': '2026-10-03',
                             'source': 'https://developers.openai.com/api/docs/models/text-embedding-3-large'},
}
DIMENSIONS = {'Cohere-Embed-V5-Pro': 2048, 'Cohere-Embed-V5-Fast': 2048, 'text-embedding-3-large': 3072}


def candidates(kind, winner=None):
    models = list(DIMENSIONS) if kind == 'bakeoff' else [winner]
    if any(model not in DIMENSIONS for model in models):
        raise ValueError('winner must be one of the measured deployments')
    return [{'model': model, 'dimensions': dims, 'segment_tokens': segment, 'price': PRICES[model],
             'label': f'{model}-d{dims}-s{segment}'}
            for model in models for segment in ([2048] if kind == 'bakeoff' else [512, 2048])
            for dims in ([DIMENSIONS[model]] if kind == 'bakeoff' else [DIMENSIONS[model], 1024])]


def prior_spend(path, winner):
    """The winner dispatch debits the bakeoff's conservative charge from $10."""
    prior = json.loads(pathlib.Path(path).read_text())
    campaign = prior.get('embedding_campaign', {})
    if (prior.get('status') != 'completed' or campaign.get('kind') != 'bakeoff'
            or winner not in {c['model'] for c in campaign.get('candidates', [])}):
        raise ValueError('winner requires a completed bakeoff report containing that model')
    spent = campaign['budget']['cost_upper_bound_usd']
    if not isinstance(spent, (int, float)) or not 0 <= spent < 10:
        raise ValueError('invalid prior campaign spend')
    return spent, prior['run']['id']


@contextlib.contextmanager
def hosted(binary, directory, candidate, gate):
    """Generate the dependency's manifest offline and run it without a provider key."""
    sys.path.insert(0, str(run.ROOT / 'scripts'))
    import ingestion_plugin
    import ports
    directory.mkdir(parents=True, exist_ok=True)
    directory.chmod(0o700)
    config = {'format': gate.format, 'base_url': gate.url, 'auth': 'none', 'model': candidate['model'],
              'dimensions': candidate['dimensions'], 'max_tokens_per_segment': candidate['segment_tokens'],
              'max_batch_tokens': 32768, 'batch_size': 16, 'overlap': 48,
              'request_timeout_ms': 10000, 'call_budget_ms': 90000, 'max_retries': 2,
              'usd_per_million_tokens': candidate['price']['usd_per_million_tokens']}
    configuration = directory / 'configuration.json'
    configuration.write_text(json.dumps(config))
    manifest = directory / 'quivr-plugin.yaml'
    with manifest.open('w') as output:
        subprocess.run([str(binary), 'configure', str(configuration)], stdout=output, check=True)
    declared = json.loads(manifest.read_text())
    spaces = declared['contributions']['ingestion']['spaces']
    space = next(iter(spaces))
    port = ports.allocate()
    env = {k: v for k, v in os.environ.items() if k not in ('AZURE_FOUNDRY_KEY', 'AZURE_FOUNDRY_ENDPOINT', 'TYPESAFE_API_KEY')}
    env.update(QUIVR_PLUGIN_HOST='127.0.0.1', QUIVR_PLUGIN_PORT=str(port), QUIVR_PLUGIN_MANIFEST=str(manifest))
    log = directory / 'hosted-embedding.log'
    with log.open('ab') as output:
        process = subprocess.Popen([str(binary)], env=env, stdout=output, stderr=output, start_new_session=True)
    try:
        ingestion_plugin.await_healthy(process, port, log)
        yield {'plugin': declared['id'], 'space': f"{space}@{spaces[space]['version']}",
               'manifest': str(manifest), 'endpoint': f'http://127.0.0.1:{port}',
               'configuration': config, 'spaces': {space: 'served'}, 'secret_names': []}
    finally:
        try:
            os.killpg(process.pid, signal.SIGTERM)
            process.wait(timeout=5)
        except ProcessLookupError:
            pass
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait(timeout=5)


def execute(campaign, options, endpoint, key, budget, out):
    import scoring
    sets = [(name, run.public_sets.prepare(name, options.cache)) for name in campaign['sets']]
    binary = out / 'hosted-embed'
    subprocess.run([os.environ.get('GO', 'go'), 'build', '-o', str(binary), '.'],
                   cwd=run.ROOT / 'plugins' / 'hosted-embed', check=True)
    for candidate in campaign['candidates']:
        budget.check()
        label, model = candidate['label'], candidate['model']
        format = 'openai' if model == 'text-embedding-3-large' else 'cohere'
        base = endpoint.rstrip('/') + ('/openai/v1' if format == 'openai' else '/providers/cohere/v2')
        stacks = []
        with embeddings.Gate(budget, model, format, base, key,
                             candidate['price']['usd_per_million_tokens'], candidate['dimensions'], label) as gate:
            try:
                with hosted(binary, out / label, candidate, gate) as selection:
                    phases = {}
                    client = run.start_stack(phases, stacks, ingestion=selection)
                    client.embedding_gate = gate
                    client.deep_enabled = False
                    campaign.setdefault('phases_seconds', {})[label] = phases
                    measured = types.SimpleNamespace(ingest_timeout=options.ingest_timeout, stall=options.stall,
                                                      paid_calls=0, live_paid=False, evaluation=selection,
                                                      modes=['semantic', 'hybrid'])
                    for name, directory in sets:
                        def publish(set_name, result):
                            result['embedding_model'] = dict(candidate)
                            result['embedding_set'] = set_name
                            options.report['sets'][label + '/' + set_name] = result
                        measured.publish_partial = publish
                        try:
                            run.measure_set([client], name, directory, options.report['run']['id'] + label, measured,
                                            allow_paid=False, evaluation=selection)
                        finally:
                            result = options.report['sets'].get(label + '/' + name)
                            if result:
                                run.compare_within(result)
                                result['embedding_total'] = budget.summary(label, name)
                                # Save after each set, including a stopped one.
                                write_report(options.report, budget, out)
            finally:
                for stack in stacks:
                    try:
                        stack.capture()
                    finally:
                        stack.down(True)
    options.report['convention'], options.report['test'] = scoring.CONVENTION, scoring.TEST


def write_report(report, budget, out):
    if budget is not None:
        report['embedding_campaign']['budget'] = budget.summary()
        # Teardown waits for in-flight provider responses. Refresh snapshots
        # from the final ledger so a late settlement is reflected per variant
        # and set, as well as in the campaign total.
        for result in report['sets'].values():
            if 'embedding_model' in result and 'embedding_set' in result:
                label, name = result['embedding_model']['label'], result['embedding_set']
                result['embedding_indexing'] = budget.summary(label, name, 'indexing')
                result['embedding_total'] = budget.summary(label, name)
    report['run']['finished_at'] = run.now()
    (out / 'report.json').write_text(json.dumps(report, indent=1))
    (out / 'report.md').write_text(render.markdown(report))


def main():
    parser = argparse.ArgumentParser(description=__doc__.split('\n\n')[0])
    parser.add_argument('--campaign', choices=['bakeoff', 'winner'], default='bakeoff')
    parser.add_argument('--winner', choices=list(DIMENSIONS))
    parser.add_argument('--prior-report', help='completed bakeoff report.json, required for winner')
    parser.add_argument('--max-input-tokens', type=int, required=True)
    parser.add_argument('--max-usd', type=float, required=True, help='explicit per-dispatch USD cap, at most remaining campaign $10')
    parser.add_argument('--dry-run', action='store_true')
    parser.add_argument('--allow-paid', action='store_true')
    parser.add_argument('--out', default=str(run.ROOT / '.scratch/eval/embeddings'))
    parser.add_argument('--cache', default=str(run.ROOT / '.scratch/eval/cache'))
    parser.add_argument('--ingest-timeout', type=int, default=5400)
    parser.add_argument('--stall', type=int, default=600)
    options = parser.parse_args()
    if options.campaign == 'winner' and (not options.winner or not options.prior_report):
        parser.error('winner requires --winner and --prior-report')
    if options.campaign == 'bakeoff' and (options.winner or options.prior_report):
        parser.error('bakeoff excludes --winner and --prior-report')
    spent, prior_id = prior_spend(options.prior_report, options.winner) if options.prior_report else (0, None)
    budget = embeddings.Budget(options.max_input_tokens, options.max_usd)
    if budget.max_usd + embeddings.decimal.Decimal(str(spent)) > 10:
        parser.error('per-run USD cap exceeds the remaining campaign $10')
    campaign = {'kind': options.campaign, 'candidates': candidates(options.campaign, options.winner),
                'sets': ['miracl-fr', 'mldr-fr', 'scifact'] if options.campaign == 'bakeoff' else ['scifact'],
                'prior_run_id': prior_id, 'prior_cost_upper_bound_usd': spent, 'campaign_cap_usd': 10,
                'hybrid_fusion': 'unchanged core.retrieve/default', 'price_basis': 'dated provider list prices; estimates, not Azure invoices',
                'expected_input_tokens_per_model': '4–5 million across three sets; retries and query calls also count',
                'budget': budget.summary()}
    if options.dry_run:
        print(json.dumps(campaign, indent=2))
        return
    if not options.allow_paid or os.environ.get('GITHUB_EVENT_NAME') != 'workflow_dispatch':
        parser.error('paid campaign requires --allow-paid in a coordinator workflow_dispatch job')
    endpoint = os.environ.pop('AZURE_FOUNDRY_ENDPOINT', '')
    key = os.environ.pop('AZURE_FOUNDRY_KEY', '')
    os.environ.pop('TYPESAFE_API_KEY', None)
    if not endpoint or not key:
        parser.error('AZURE_FOUNDRY_ENDPOINT and AZURE_FOUNDRY_KEY must be injected')
    out = pathlib.Path(options.out).resolve()
    out.mkdir(parents=True, exist_ok=True)
    out.chmod(0o700)
    def interrupted(*_):
        raise KeyboardInterrupt()
    signal.signal(signal.SIGTERM, interrupted)
    report = {'status': 'failed', 'run': {'id': uuid.uuid4().hex[:10], 'started_at': run.now(),
                                        'source_revision': run.git('rev-parse', 'HEAD'), 'host': run.host(),
                                        'target': 'paired core.ingest and hosted evaluation owners'},
              'convention': None, 'test': None, 'baseline_system': run.BASELINE_SYSTEM,
              'limit': run.LIMIT, 'sets': {}, 'embedding_campaign': campaign}
    import scoring
    report.update(convention=scoring.CONVENTION, test=scoring.TEST)
    options.report = report
    started = time.monotonic()
    try:
        execute(campaign, options, endpoint, key, budget, out)
        report['status'] = 'completed'
    except embeddings.BudgetExceeded as error:
        report.update(status='capped', error=str(error))
    except BaseException as error:
        report['error'] = type(error).__name__ + ': embedding campaign failed; completed cuts are preserved'
        raise
    finally:
        report['run']['duration_seconds'] = round(time.monotonic() - started, 3)
        write_report(report, budget, out)
        print('Embedding campaign report:', out / 'report.md', flush=True)


if __name__ == '__main__':
    main()
