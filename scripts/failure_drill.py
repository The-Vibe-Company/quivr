"""A deliberately failed journey explained by diagnostics alone (THE-662).

The journey's worker-stopped phase is a real outage: the worker is killed while
the API keeps accepting work. This drill checks that an operator who sees only
probes, metrics and logs can tell what happened:

- during the outage, the API is ready (durable acceptance works) while the worker
  probe does not answer; the ingestion backlog gauges show pending work growing
  older; the API log correlates the accepted command's request ID with its Receipt;
- after the restart, the worker's processing counters and the acceptance-to-searchable
  histogram record the delay, and the worker log names the same Receipt.

It asserts on diagnostics only; public correctness stays with the acceptance tests.
Results go to failure-drill.json with the metric snapshots it read.
"""
import json, re, time, urllib.error, urllib.request


def _get(url):
    try:
        with urllib.request.urlopen(url, timeout=3) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as error:
        return error.code, error.read().decode(errors='replace')
    except OSError as error:
        return None, type(error).__name__


def sample(text, name, labels=''):
    """Value of one metric sample, or None."""
    match = re.search(r'^' + re.escape(name + labels) + r' (\S+)$', text, re.M)
    return float(match.group(1)) if match else None


def _receipt(stack):
    return json.loads((stack.directory / 'journey-state.json').read_text())['ReceiptD']


def _lines(path, receipt, message):
    """Log entries about one Receipt, across the current file and its rotations."""
    found = []
    text = ''.join(f.read_text(errors='replace') + '\n' for f in sorted(path.parent.glob(path.name + '*')) if f.is_file()) if path.parent.exists() else ''
    for line in text.splitlines():
        try:
            entry = json.loads(line)
        except ValueError:
            continue
        if entry.get('receipt_id') == receipt and entry.get('msg') == message:
            found.append(entry)
    return found


def during(stack):
    """While the worker is stopped: the API is ready, the backlog is visible and correlated."""
    s, d = stack.state, stack.directory
    receipt = _receipt(stack)
    api_ready, _ = _get(f"http://127.0.0.1:{s['probe_port']}/readyz")
    worker_probe, worker_answer = _get(f"http://127.0.0.1:{s['worker_probe_port']}/readyz")
    status, metrics = _get(f"http://127.0.0.1:{s['probe_port']}/metrics")
    (d / 'metrics-api-during-outage.txt').write_text(metrics)
    pending = sample(metrics, 'quivr_ingestion_pending')
    age = sample(metrics, 'quivr_ingestion_oldest_pending_age_seconds')
    accepted = sample(metrics, 'quivr_commands_accepted_total', '{command="record"}')
    logged = _lines(d / 'api.log', receipt, 'command accepted')
    result = {'receipt': receipt, 'api_ready': api_ready, 'worker_probe': worker_probe or worker_answer,
              'ingestion_pending': pending, 'oldest_pending_age_seconds': age, 'records_accepted': accepted,
              'api_log_request_ids': sorted({e.get('request_id') for e in logged})}
    assert api_ready == 204, f'API must stay ready while the worker is down: {api_ready}'
    assert worker_probe is None, f'the stopped worker must not answer its probe: {worker_probe}'
    assert status == 200 and pending is not None and pending >= 1, f'backlog gauge must show the pending Receipt: {pending}'
    assert age is not None and age >= 0, f'oldest pending age must describe the queued Receipt: {age}'
    assert accepted and accepted >= 1, 'accepted-command counter missing'
    assert logged and all(e.get('request_id') for e in logged), 'API log must correlate the request with its Receipt'
    return result


def after(stack, during_result, deadline=60):
    """After the restart: processing metrics and the worker log explain the delay."""
    s, d = stack.state, stack.directory
    receipt = during_result['receipt']
    start = time.monotonic()
    while True:
        status, metrics = _get(f"http://127.0.0.1:{s['worker_probe_port']}/metrics")
        logged = _lines(d / 'worker.log', receipt, 'processing outcome')
        count = sample(metrics, 'quivr_acceptance_to_searchable_seconds_count') if status == 200 else None
        fast = sample(metrics, 'quivr_acceptance_to_searchable_seconds_bucket', '{le="1"}') if status == 200 else None
        if logged and count and fast is not None:
            break
        assert time.monotonic() - start < deadline, f'worker diagnostics never explained the outage: count={count} le1={fast} logged={len(logged)}'
        time.sleep(.5)
    (d / 'metrics-worker-after-restart.txt').write_text(metrics)
    # The backlog drains: the Receipt that waited through the outage is no longer pending.
    status, api_metrics = _get(f"http://127.0.0.1:{s['probe_port']}/metrics")
    (d / 'metrics-api-after-restart.txt').write_text(api_metrics)
    pending_after = sample(api_metrics, 'quivr_ingestion_pending')
    assert status == 200 and pending_after is not None and pending_after < during_result['ingestion_pending'], f'backlog did not drain: {pending_after}'

    succeeded = sample(metrics, 'quivr_processing_outcomes_total', '{stage="baseline",outcome="succeeded"}')
    assert succeeded and succeeded >= 1, 'baseline outcome counter missing'
    baseline = [e for e in logged if e.get('stage') == 'baseline' and e.get('outcome') == 'succeeded']
    assert baseline and baseline[0].get('record_id') and baseline[0].get('version_id'), 'worker log must name the Record and Version'
    result = {**during_result, 'worker_log_outcomes': [f"{e.get('stage')}:{e.get('outcome')}" for e in logged],
              'ingestion_pending_after_restart': pending_after, 'searchable_observations': count, 'searchable_over_1s': count - fast, 'baseline_succeeded': succeeded,
              'explanation': 'API ready and accepting while the worker probe was down; the ingestion backlog gauge showed the '
                             'Receipt pending and ageing; after restart the worker processed it and the acceptance-to-searchable '
                             'histogram recorded ingestion latency, correlated by receipt_id in the API and worker logs.'}
    (d / 'failure-drill.json').write_text(json.dumps(result, indent=2))
    return result
