"""Preview or run ephemeral Modal CPU/GPU comparisons (paid runs: coordinator only).

No deployment, scheduled job, volume or public endpoint is created. TEI listens
on loopback inside each short-lived measurement container. `plan` needs only
Python's standard library; `run` imports Modal only after explicit cost consent.
"""
import argparse
import datetime
import json
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

ROOT = pathlib.Path(__file__).resolve().parents[2]
TEI = 'ghcr.io/huggingface/text-embeddings-inference:'
CANDIDATES = {
    'e5-small': ('tei', direct_bakeoff.E5_REVISION),
    'qwen3': ('qwen3', '97b0c614be4d77ee51c0cef4e5f07c00f9eb65b3'),
    'granite-r2': ('granite-r2', '44399559930365213510b1ee2eb15ded83374f0e'),
    'arctic-v2': ('arctic-v2', 'ac6544c8a46e00af67e330e85a9028c66b8cfd9a'),
}
# Modal function (not Sandbox) list rates checked 2026-10-03.
RATES = {'cpu_core_second': .0000131, 'gib_second': .00000222, 'L4_second': .000222}
CPU_CORES, MEMORY_GIB = 4, 8


def image_for(hardware):
    return TEI + ('cpu-1.9.3' if hardware == 'cpu' else '1.9.3')


def hourly_rate(hardware):
    return 3600 * (CPU_CORES * RATES['cpu_core_second'] + MEMORY_GIB * RATES['gib_second']
                   + (RATES['L4_second'] if hardware == 'L4' else 0))


def configuration(label):
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


def wait_ready(process, timeout=300):
    deadline = time.monotonic() + timeout
    opener = urllib.request.build_opener(direct_bakeoff.embeddings.NoRedirect())
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError('TEI exited before readiness')
        try:
            with opener.open('http://127.0.0.1:8080/health', timeout=2) as response:
                if response.status == 200:
                    return
        except (urllib.error.URLError, TimeoutError):
            pass
        time.sleep(.1)  # bounded runtime readiness; never exercised by sleeping tests
    raise RuntimeError('TEI readiness deadline exceeded')


def measure(label, hardware, sets, git_sha, max_tokens, timeout, restricted):
    """One Modal job, serial sets, shared token allowance; always reap TEI."""
    started = time.monotonic()
    config = configuration(label)
    revision = CANDIDATES[label][1]
    command = ['text-embeddings-router', '--model-id', config['model'], '--revision', revision,
               '--hostname', '127.0.0.1', '--port', '8080', '--auto-truncate', '--max-client-batch-size', '32']
    # TEI GPU float16, CPU float32; precision is recorded alongside hardware.
    command += ['--dtype', 'float32' if hardware == 'cpu' else 'float16']
    reports = []
    with tempfile.TemporaryDirectory() as temporary:
        directory = pathlib.Path(temporary)
        config_path = directory / 'configuration.json'
        write_new(config_path, config)
        # Provider logs may contain inputs; keep them out of job output/artifacts.
        with open(os.devnull, 'wb') as silence:
            process = subprocess.Popen(command, stdout=silence, stderr=silence)
        try:
            wait_ready(process, min(300, timeout))
            cold = time.monotonic() - started
            remaining = max_tokens
            os.environ['QUIVR_EVAL_GIT_SHA'] = git_sha
            for name in sets:
                if time.monotonic() - started >= timeout - 10 or remaining <= 0:
                    break
                out = directory / (name + '.json')
                arguments = ['--set', name, '--openai-config', label + '=' + str(config_path),
                             '--max-input-tokens', str(remaining), '--max-usd', '1',
                             '--query-latency', '--out', str(out)]
                if restricted:
                    arguments.append('--include-restricted')
                try:
                    child = subprocess.run([sys.executable, str(ROOT / 'scripts/eval/direct_bakeoff.py')] + arguments,
                                           timeout=max(1, timeout - (time.monotonic() - started) - 10),
                                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                    code = child.returncode
                except subprocess.TimeoutExpired:
                    code = 2
                if not out.exists():
                    break
                report = json.loads(out.read_text())
                if report['status'] == 'running':
                    report['status'] = 'timed_out'
                remaining -= report['budget']['budgeted_input_tokens']
                result = report['results'].get(label)
                if result:
                    usage = report['by_model'][label]
                    tokens = usage['confirmed_input_tokens'] if not usage['reserved_input_tokens'] else None
                    seconds = result['duration_seconds']
                    estimated = seconds * hourly_rate(hardware) / 3600
                    result['serving'] = {'hardware': hardware, 'image': image_for(hardware),
                                         'model_revision': revision, 'cpu_cores': CPU_CORES, 'memory_gib': MEMORY_GIB,
                                         'dtype': 'float32' if hardware == 'cpu' else 'float16',
                                         'hourly_usd': hourly_rate(hardware), 'seconds': seconds,
                                         'estimated_usd': estimated, 'input_tokens': tokens,
                                         'usd_per_million_tokens': estimated * 1e6 / tokens if tokens else None,
                                         'cold_start_seconds': cold, 'estimate_only': True,
                                         'scope': 'candidate encoding and scoring; excludes baseline and preparation'}
                reports.append(report)
                if code:
                    break
        finally:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=10)
    elapsed = time.monotonic() - started
    campaign = {'model': label, 'hardware': hardware, 'elapsed_seconds': elapsed,
                'estimated_usd': elapsed * hourly_rate(hardware) / 3600, 'estimate_only': True,
                'scope': 'whole function body, including downloads, baseline and scoring; excludes image build and scheduling',
                'requested_sets': sets, 'completed_sets': [r['set'] for r in reports if r['status'] == 'complete'],
                'status': 'complete' if len(reports) == len(sets) and all(r['status'] == 'complete' for r in reports) else 'partial'}
    for report in reports:
        report['serving_campaign'] = campaign
    return {'campaign': campaign, 'reports': reports}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=('plan', 'run'))
    parser.add_argument('--out', required=True, type=pathlib.Path, help='new plan file or new campaign directory')
    parser.add_argument('--models', nargs='+', choices=sorted(CANDIDATES), default=list(CANDIDATES))
    parser.add_argument('--hardware', nargs='+', choices=('cpu', 'L4'), default=['cpu', 'L4'])
    parser.add_argument('--sets', nargs='+', choices=sorted(public_sets.SETS))
    parser.add_argument('--include-restricted', action='store_true')
    parser.add_argument('--max-input-tokens', type=int, default=200_000_000, help='per model/hardware, shared across sets')
    parser.add_argument('--timeout', type=int, default=3600, help='per model/hardware job, including data/model preparation')
    parser.add_argument('--acknowledge-cost', action='store_true', help='coordinator consent to paid Modal dispatch')
    args = parser.parse_args(argv)
    if args.out.exists():
        raise FileExistsError('output exists; choose a new evidence path')
    if not 30 <= args.timeout <= 86400 or args.max_input_tokens <= 0:
        parser.error('timeout must be 30..86400 seconds and token allowance must be positive')
    sets = list(dict.fromkeys(args.sets or public_sets.names(args.include_restricted)))
    if not args.include_restricted and any(not public_sets.SETS[name]['promotion_eligible'] for name in sets):
        parser.error('restricted diagnostic sets require --include-restricted')
    jobs = [{'model': label, 'hardware': hardware, 'image': image_for(hardware),
             'configuration': configuration(label), 'model_revision': CANDIDATES[label][1],
             'hourly_usd': hourly_rate(hardware), 'timeout_seconds': args.timeout,
             'estimated_compute_usd': hourly_rate(hardware) * args.timeout / 3600}
            for label in dict.fromkeys(args.models) for hardware in dict.fromkeys(args.hardware)]
    if args.command == 'plan':
        write_new(args.out, {'date': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                            'sets': sets, 'include_restricted': args.include_restricted, 'jobs': jobs,
                            'estimated_compute_usd': sum(j['estimated_compute_usd'] for j in jobs),
                            'estimate_only': True, 'price_date': '2026-10-03', 'price_source': 'https://modal.com/pricing',
                            'rates': RATES, 'excluded_costs': 'image builds, scheduling/startup, egress, credits; metered usage can differ'})
        return 0
    if any(os.environ.get(n, '').lower() not in ('', '0', 'false') for n in ('CI', 'GITHUB_ACTIONS')):
        raise SystemExit('paid Modal dispatch is refused in CI')
    if not args.acknowledge_cost:
        parser.error('run requires coordinator --acknowledge-cost; inspect plan first')
    from oss_modal import dispatch, JobFailed
    args.out.mkdir(parents=True)
    git_sha = subprocess.run(['git', 'rev-parse', 'HEAD'], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip()
    # Serial dispatch avoids an accidental fleet of GPU containers.
    failed = False
    for job in jobs:
        try:
            output = dispatch(job['model'], job['hardware'], sets, git_sha,
                              args.max_input_tokens, args.timeout, args.include_restricted)
        except JobFailed as error:
            # SDK errors can include inputs or credentials. Preserve a safe job
            # failure and stop; already returned set evidence remains intact.
            write_new(args.out / (job['model'] + '-' + job['hardware'] + '-campaign.json'),
                      {'status': 'failed', 'model': job['model'], 'hardware': job['hardware'],
                       'requested_sets': sets, 'completed_sets': [], 'estimated_usd': None,
                       'reason': 'Modal job failed; inspect operator console', 'estimate_only': True,
                       'modal_app_id': error.app_id})
            return 2
        stem = job['model'] + '-' + job['hardware']
        write_new(args.out / (stem + '-campaign.json'), output['campaign'])
        for report in output['reports']:
            write_new(args.out / (stem + '-' + report['set'] + '.json'), report)
        failed |= output['campaign']['status'] != 'complete'
    return 2 if failed else 0


if __name__ == '__main__':
    raise SystemExit(main())
