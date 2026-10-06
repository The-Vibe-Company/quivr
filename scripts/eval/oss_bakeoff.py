"""Preview or run ephemeral Modal CPU/GPU comparisons (paid runs: coordinator only).

No deployment, scheduled job, volume or public endpoint is created. References
are cached as scored artifacts on the operator machine. TEI listens
on loopback inside each short-lived measurement container. `plan` needs only
Python's standard library; `run` imports Modal only after explicit cost consent.
"""
import argparse
import concurrent.futures
import datetime
import json
import hashlib
import os
import pathlib
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

import direct_bakeoff
import public_sets

import ci_guard
import embeddinggemma_server

ROOT = pathlib.Path(__file__).resolve().parents[2]
TEI = 'ghcr.io/huggingface/text-embeddings-inference:'
CANDIDATES = {
    'e5-small': ('tei', direct_bakeoff.E5_REVISION),
    'qwen3': ('qwen3', '97b0c614be4d77ee51c0cef4e5f07c00f9eb65b3'),
    'granite-r2': ('granite-r2', '44399559930365213510b1ee2eb15ded83374f0e'),
    'arctic-v2': ('arctic-v2', 'ac6544c8a46e00af67e330e85a9028c66b8cfd9a'),
    'embeddinggemma-2': (None, embeddinggemma_server.REVISION),
    'embeddinggemma-2-256': (None, embeddinggemma_server.REVISION),
}
# Modal function (not Sandbox) list rates checked 2026-10-03.
RATES = {'cpu_core_second': .0000131, 'gib_second': .00000222, 'L4_second': .000222}
CPU_CORES, MEMORY_GIB = 4, 8
# Transformers 5.19 exercises the accelerator API while importing E5 on CPU.
# PyTorch 2.6 raises on accelerator-less hosts; 2.8 passes the offline import check.
TORCH_PACKAGE = 'torch==2.8.0'
TEXT_PACKAGES = ['transformers==5.19.0', 'sentence-transformers==6.1.0']


def image_for(hardware, label=None):
    if label in embeddinggemma_server.LABELS:
        requirements = ['requirements-oss.txt', 'requirements-direct.txt', 'requirements.txt']
        return {'base': 'debian_slim', 'python': '3.12', 'torch': TORCH_PACKAGE,
                'torch_index_url': 'https://download.pytorch.org/whl/' + ('cpu' if hardware == 'cpu' else 'cu126'),
                'packages': TEXT_PACKAGES,
                'requirements': {name: hashlib.sha256((ROOT / 'scripts/eval' / name).read_bytes()).hexdigest()
                                 for name in requirements}}
    return TEI + ('cpu-1.9.3' if hardware == 'cpu' else '1.9.3')


def hourly_rate(hardware):
    return 3600 * (CPU_CORES * RATES['cpu_core_second'] + MEMORY_GIB * RATES['gib_second']
                   + (RATES['L4_second'] if hardware == 'L4' else 0))


def configuration(label):
    if label in embeddinggemma_server.LABELS:
        return {'format': 'openai', 'base_url': 'http://127.0.0.1:8080/v1', 'auth': 'none',
                'model': embeddinggemma_server.MODEL, 'dimensions': embeddinggemma_server.LABELS[label],
                'model_revision': embeddinggemma_server.REVISION[:16],
                'query_prefix': 'task: search result | query: ', 'document_prefix': 'title: none | text: ',
                'max_tokens_per_segment': 8192, 'overlap': 200, 'batch_size': 16}
    filename, revision = CANDIDATES[label]
    config = json.loads((ROOT / 'plugins/hosted-embed/examples' / (filename + '.json')).read_text())
    # The plugin uses a bounded logical version; TEI uses the full HF commit.
    config['model_revision'] = revision[:16]
    return config


def write_new(path, value):
    path = pathlib.Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open('x', encoding='utf-8') as output:
        json.dump(value, output, indent=2, allow_nan=False)
        output.write('\n')


def wait_ready(process, timeout=300, server='TEI'):
    deadline = time.monotonic() + timeout
    opener = urllib.request.build_opener(direct_bakeoff.embeddings.NoRedirect())
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(server + ' exited before readiness')
        try:
            with opener.open('http://127.0.0.1:8080/health', timeout=2) as response:
                if response.status == 200:
                    return
        except (urllib.error.URLError, TimeoutError):
            pass
        time.sleep(.1)  # bounded runtime readiness; never exercised by sleeping tests
    raise TimeoutError(server + ' readiness deadline exceeded')


def stop_reason(error, process=None, stderr=None, server='tei'):
    code = process.poll() if process is not None else None
    kind = ('tei_exit' if server == 'tei' else 'server_exit') if code is not None else 'timeout' if isinstance(error, (TimeoutError, subprocess.TimeoutExpired)) else 'provider_error'
    reason = {'kind': kind, **direct_bakeoff.embeddings.error_identity(error)}
    if code is not None:
        reason['exit_code'] = code
        reason['server'] = server
    if stderr is not None:
        stderr.seek(0, os.SEEK_END)
        stderr.seek(max(0, stderr.tell() - 8192))
        # Keep bounded diagnostics in private artifacts only; never progress logs.
        reason['stderr_tail'] = stderr.read().decode('utf-8', errors='replace').splitlines()[-20:]
    return reason


def run_comparison(directory, label, sets, git_sha, max_tokens, timeout, restricted, started, reference=None):
    """Bound the subprocess, retaining a partial report and its stop reason."""
    os.environ['QUIVR_EVAL_GIT_SHA'] = git_sha
    name = sets[0]
    out = directory / (name + '.json')
    arguments = ['--set', name, '--max-input-tokens', str(max_tokens), '--max-usd', '1',
                 '--query-latency', '--out', str(out)]
    if label == 'reference':
        arguments += ['--models', direct_bakeoff.BASELINE]
    else:
        config_path = directory / 'configuration.json'
        write_new(config_path, configuration(label))
        arguments += ['--openai-config', label + '=' + str(config_path)]
    if reference is not None:
        path = directory / 'reference.json'
        write_new(path, reference)
        arguments += ['--e5-reference', str(path)]
    if restricted:
        arguments.append('--include-restricted')
    reason = None
    try:
        child = subprocess.run([sys.executable, str(ROOT / 'scripts/eval/direct_bakeoff.py')] + arguments,
                               timeout=max(1, timeout - (time.monotonic() - started) - 10),
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if child.returncode:
            reason = {'kind': 'provider_error', 'exit_code': child.returncode}
    except subprocess.TimeoutExpired:
        reason = {'kind': 'timeout', 'phase': 'measurement'}
    reports = []
    if out.exists():
        try:
            report = json.loads(out.read_text())
        except (ValueError, OSError) as error:
            return [], reason or {'kind': 'provider_error', 'phase': 'invalid_report', 'error_type': type(error).__name__}
        if report['status'] == 'running':
            report['status'] = 'timed_out'
            reason = reason or {'kind': 'timeout', 'phase': 'measurement'}
        if report['status'] == 'capped':
            reason = {'kind': 'token_cap'}
        elif report['status'] != 'complete':
            reason = report.get('reason', reason or {'kind': 'provider_error'})
        if reason is not None:
            report['reason'] = reason
        reports.append(report)
    elif reason is None:
        reason = {'kind': 'provider_error', 'phase': 'missing_report'}
    return reports, reason


def campaign_record(label, hardware, sets, reports, elapsed, reason=None):
    complete = len(reports) == len(sets) and all(r['status'] == 'complete' for r in reports) and reason is None
    campaign = {'model': label, 'hardware': hardware, 'elapsed_seconds': elapsed,
                'estimated_usd': elapsed * hourly_rate(hardware) / 3600, 'estimate_only': True,
                'scope': 'whole function body; excludes image build, scheduling and cached reference computation',
                'requested_sets': sets, 'completed_sets': [r['set'] for r in reports if r['status'] == 'complete'],
                'status': 'complete' if complete else 'partial' if reports else 'failed'}
    if not complete:
        campaign['reason'] = reason or {'kind': 'provider_error'}
    return campaign


def report_campaign(value):
    """Scored reports may be imported publicly; retain diagnostics in job files."""
    if isinstance(value, dict):
        return {key: report_campaign(item) for key, item in value.items() if key != 'stderr_tail'}
    if isinstance(value, list):
        return [report_campaign(item) for item in value]
    return value


def measure(label, hardware, sets, git_sha, max_tokens, timeout, restricted, reference=None):
    """One independently bounded model/hardware/set job; always reap TEI."""
    if len(sets) != 1:
        raise ValueError('measurement jobs require exactly one set')
    started = time.monotonic()
    reports, reason, cold = [], None, None
    with tempfile.TemporaryDirectory() as temporary:
        directory = pathlib.Path(temporary)
        if label == 'reference':
            try:
                reports, reason = run_comparison(directory, label, sets, git_sha, max_tokens, timeout, restricted, started)
            except Exception as error:
                reason = stop_reason(error)
        else:
            config = configuration(label)
            revision = CANDIDATES[label][1]
            command = ['text-embeddings-router', '--model-id', config['model'], '--revision', revision,
                       '--hostname', '127.0.0.1', '--port', '8080', '--auto-truncate', '--max-client-batch-size', '32',
                       '--dtype', 'float32' if hardware == 'cpu' else 'float16']
            gemma = label in embeddinggemma_server.LABELS
            server = 'embeddinggemma' if gemma else 'tei'
            if gemma:
                command = [sys.executable, str(ROOT / 'scripts/eval/embeddinggemma_server.py'), hardware]
            process = None
            # File-backed stderr avoids a pipe deadlock. Only its bounded tail
            # is returned in private job evidence, never in progress output.
            with (directory / 'tei-stderr').open('w+b') as stderr:
                try:
                    process = subprocess.Popen(command, stdout=subprocess.DEVNULL, stderr=stderr)
                    try:
                        wait_ready(process, min(300, max(1, timeout - 10)), server)
                    except Exception as error:
                        reason = stop_reason(error, process, stderr, server)
                    if reason is None:
                        cold = time.monotonic() - started
                        reports, reason = run_comparison(directory, label, sets, git_sha, max_tokens, timeout, restricted, started, reference)
                        if process.poll() is not None:
                            reason = stop_reason(RuntimeError(), process, stderr, server)
                except Exception as error:
                    reason = stop_reason(error, process, stderr, server)
                finally:
                    if process is not None:
                        process.terminate()
                        try:
                            process.wait(timeout=10)
                        except subprocess.TimeoutExpired:
                            process.kill()
                            process.wait(timeout=10)
            for report in reports:
                result = report['results'].get(label)
                if result:
                    usage = report['by_model'][label]
                    tokens = usage['confirmed_input_tokens'] if not usage['reserved_input_tokens'] else None
                    seconds = result['duration_seconds']
                    estimated = seconds * hourly_rate(hardware) / 3600
                    result['serving'] = {'hardware': hardware, 'image': image_for(hardware, label),
                                         'model_revision': revision, 'cpu_cores': CPU_CORES, 'memory_gib': MEMORY_GIB,
                                         'dtype': 'float32' if hardware == 'cpu' else 'bfloat16' if gemma else 'float16',
                                         'hourly_usd': hourly_rate(hardware), 'seconds': seconds,
                                         'estimated_usd': estimated, 'input_tokens': tokens,
                                         'usd_per_million_tokens': estimated * 1e6 / tokens if tokens else None,
                                         'cold_start_seconds': cold, 'estimate_only': True,
                                         'scope': 'candidate encoding and scoring; excludes baseline and preparation'}
                    # Document throughput includes splitting, transport and normalization,
                    # matching the existing indexing timer rather than scoring duration.
                    index_seconds = result['index_s']
                    count = report['documents']
                    result['serving']['documents_per_second'] = count / index_seconds if index_seconds > 0 else None
                    result['serving']['document_pieces_per_second'] = result['pieces'] / index_seconds if index_seconds > 0 else None
    campaign = campaign_record(label, hardware, sets, reports, time.monotonic() - started, reason)
    for report in reports:
        report['serving_campaign'] = report_campaign(campaign)
    return {'campaign': campaign, 'reports': reports}


def unsupported_reason(label, hardware):
    if label == 'qwen3' and hardware == 'cpu':
        return {'kind': 'unsupported', 'detail': 'Pinned Qwen3/TEI CPU serving combination has not passed startup validation: '
                'operator run exited before readiness. Upstream includes CPU support; this campaign excludes the combination pending validation.'}
    return None


def execute_jobs(jobs, concurrency, dispatch):
    """Collect each independent outcome; progress reveals phase names/counts only."""
    def run(job):
        reason = job.get('reason')
        if reason is None:
            try:
                return dispatch(job)
            except Exception as error:
                reason = getattr(error, 'reason', None) or stop_reason(error)
                app_id = getattr(error, 'app_id', None)
        else:
            app_id = None
        campaign = {'status': 'failed', 'model': job['model'], 'hardware': job['hardware'],
                    'requested_sets': [job['set']], 'completed_sets': [], 'estimated_usd': None,
                    'reason': reason, 'estimate_only': True, 'modal_app_id': app_id}
        return {'campaign': campaign, 'reports': []}
    outputs = {}
    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
        futures = {pool.submit(run, job): index for index, job in enumerate(jobs)}
        for future in concurrent.futures.as_completed(futures):
            outputs[futures[future]] = future.result()
            print('completed', len(outputs), len(jobs), flush=True)
    return [outputs[index] for index in range(len(jobs))]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=('plan', 'run'))
    parser.add_argument('--out', required=True, type=pathlib.Path, help='new plan file or new campaign directory')
    parser.add_argument('--models', nargs='+', choices=sorted(CANDIDATES), default=list(CANDIDATES))
    parser.add_argument('--hardware', nargs='+', choices=('cpu', 'L4'), default=['cpu', 'L4'])
    parser.add_argument('--sets', nargs='+', choices=sorted(public_sets.SETS))
    parser.add_argument('--include-restricted', action='store_true')
    parser.add_argument('--max-input-tokens', type=int, default=200_000_000, help='per model/hardware allowance, divided equally across selected sets')
    parser.add_argument('--timeout', type=int, default=3600, help='per set job, including data/model preparation')
    parser.add_argument('--concurrency', type=int, default=4, help='maximum simultaneous reference or candidate jobs (1..32)')
    parser.add_argument('--reference-cache', type=pathlib.Path, default=ROOT / '.scratch/eval/e5-references')
    parser.add_argument('--acknowledge-cost', action='store_true', help='coordinator consent to paid Modal dispatch')
    args = parser.parse_args(argv)
    if args.out.exists():
        raise FileExistsError('output exists; choose a new evidence path')
    if not 30 <= args.timeout <= 86400 or args.max_input_tokens <= 0:
        parser.error('timeout must be 30..86400 seconds and token allowance must be positive')
    if not 1 <= args.concurrency <= 32:
        parser.error('concurrency must be 1..32')
    sets = list(dict.fromkeys(args.sets or public_sets.names(args.include_restricted)))
    if not args.include_restricted and any(not public_sets.SETS[name]['promotion_eligible'] for name in sets):
        parser.error('restricted diagnostic sets require --include-restricted')
    if args.max_input_tokens < len(sets):
        parser.error('token allowance must provide at least one token per set')
    jobs = [{'set': name, 'max_input_tokens': args.max_input_tokens // len(sets), 'model': label, 'hardware': hardware, 'image': image_for(hardware, label),
             'configuration': configuration(label), 'model_revision': CANDIDATES[label][1],
             'hourly_usd': hourly_rate(hardware), 'timeout_seconds': args.timeout,
             'estimated_compute_usd': hourly_rate(hardware) * args.timeout / 3600}
            for label in dict.fromkeys(args.models) for hardware in dict.fromkeys(args.hardware) for name in sets]
    for job in jobs:
        reason = unsupported_reason(job['model'], job['hardware'])
        if reason:
            job['reason'] = reason
            job['estimated_compute_usd'] = 0
    supported_sets = {job['set'] for job in jobs if 'reason' not in job}
    reference_jobs = [{'model': 'reference', 'hardware': 'cpu', 'set': name,
                       'estimated_compute_usd': hourly_rate('cpu') * args.timeout / 3600} for name in sets if name in supported_sets]
    if args.command == 'plan':
        write_new(args.out, {'date': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                            'sets': sets, 'include_restricted': args.include_restricted, 'jobs': jobs,
                            'reference_jobs': reference_jobs, 'concurrency': args.concurrency,
                            'estimated_compute_usd': sum(j['estimated_compute_usd'] for j in jobs + reference_jobs),
                            'estimate_only': True, 'price_date': '2026-10-03', 'price_source': 'https://modal.com/pricing',
                            'rates': RATES, 'excluded_costs': 'image builds, scheduling/startup, egress, credits; metered usage can differ'})
        return 0
    if ci_guard.in_ci():
        raise SystemExit('paid Modal dispatch is refused in CI')
    if not args.acknowledge_cost:
        parser.error('run requires coordinator --acknowledge-cost; inspect plan first')
    from oss_modal import dispatch
    args.out.mkdir(parents=True)
    git_sha = subprocess.run(['git', 'rev-parse', 'HEAD'], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip()
    identity = direct_bakeoff.reference_identity()
    cache = args.reference_cache / identity
    references, missing, reference_failures = {}, [], {}
    for job in reference_jobs:
        path = cache / (job['set'] + '.json')
        if path.exists():
            try:
                report = json.loads(path.read_text())
                if (report.get('status') == 'complete' and report.get('set') == job['set']
                        and report.get('settings', {}).get('e5_reference_identity') == identity):
                    references[job['set']] = report
                    continue
            except (ValueError, OSError, AttributeError, TypeError):
                pass
            reference_failures[job['set']] = {'kind': 'reference_mismatch', 'phase': 'cache'}
            continue
        missing.append(job)
    print('reference', len(references), len(sets), flush=True)
    reference_outputs = execute_jobs(missing, args.concurrency,
        lambda job: dispatch('reference', 'cpu', [job['set']], git_sha,
                             args.max_input_tokens, args.timeout, args.include_restricted))
    for job, output in zip(missing, reference_outputs):
        name = job['set']
        write_new(args.out / 'references' / (name + '-campaign.json'), output['campaign'])
        if output['campaign']['status'] == 'complete' and output['reports']:
            report = output['reports'][0]
            write_new(cache / (name + '.json'), report)
            references[name] = report
        else:
            reference_failures[name] = output['campaign'].get('reason', {'kind': 'provider_error'})
    for job in jobs:
        if job['set'] not in references and 'reason' not in job:
            job['reason'] = {'kind': 'reference_failed', 'cause': reference_failures[job['set']]}
    print('measurement', 0, len(jobs), flush=True)
    outputs = execute_jobs(jobs, args.concurrency,
        lambda job: dispatch(job['model'], job['hardware'], [job['set']], git_sha,
                             job['max_input_tokens'], args.timeout, args.include_restricted, references[job['set']]))
    grouped = {}
    for job, output in zip(jobs, outputs):
        stem = job['model'] + '-' + job['hardware']
        grouped.setdefault(stem, []).append(output['campaign'])
        write_new(args.out / 'jobs' / (stem + '-' + job['set'] + '-campaign.json'), output['campaign'])
        for report in output['reports']:
            write_new(args.out / (stem + '-' + report['set'] + '.json'), report)
    failed = False
    for stem, campaigns in grouped.items():
        completed = [name for c in campaigns for name in c['completed_sets']]
        failures = [c for c in campaigns if c['status'] != 'complete']
        aggregate = {'model': campaigns[0]['model'], 'hardware': campaigns[0]['hardware'],
                     'requested_sets': sets, 'completed_sets': completed,
                     'status': 'complete' if not failures else 'partial' if completed else 'failed',
                     'estimated_usd': sum(c.get('estimated_usd') or 0 for c in campaigns)
                                      if all(c.get('estimated_usd') is not None for c in campaigns) else None,
                     'elapsed_seconds': sum(c.get('elapsed_seconds', 0) for c in campaigns),
                     'estimate_only': True, 'jobs': campaigns}
        if failures:
            aggregate['reason'] = {'kind': 'incomplete_jobs', 'count': len(failures)}
        write_new(args.out / (stem + '-campaign.json'), aggregate)
        failed |= bool(failures)
    return 2 if failed else 0



if __name__ == '__main__':
    raise SystemExit(main())
