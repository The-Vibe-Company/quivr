"""Load evidence arithmetic and a human-readable companion to the JSON report."""
import collections
import math


def distribution(values):
    ordered = sorted(values)
    return {'count': len(ordered), **{key: round(ordered[math.ceil(len(ordered)*q)-1], 3)
                                    if ordered else None for key, q in
                                    [('p50_ms', .5), ('p95_ms', .95), ('max_ms', 1)]}}


def request_summary(rows, seconds, expected):
    errors = sum(row['status'] != expected for row in rows)
    return {'attempts': len(rows), 'errors': errors,
            'error_rate': round(errors / len(rows), 6) if rows else None,
            'attempts_per_second': round(len(rows) / seconds, 3),
            'successes_per_second': round((len(rows)-errors) / seconds, 3),
            'statuses': dict(collections.Counter(str(row['status']) if row['status'] is not None
                                                else 'transport_error' for row in rows)),
            'error_codes': dict(collections.Counter(row['error'] for row in rows
                               if row['status'] != expected and row.get('error'))),
            'latency': distribution([row['ms'] for row in rows])}


def markdown(report):
    machine = report['machine']
    lines = ['# Local load report', '', f"Status: {report['status']}",
             f"UTC: {report['started_at']}", f"Git: {report['revision']} (dirty: {report['dirty']})",
             f"Machine: {machine['system']} {machine['architecture']}, {machine['cpus']} CPUs, "
             f"{machine['memory_bytes']} bytes RAM", '',
             'Synthetic providers measure engine behaviour; they do not measure model quality.', '',
             'Latency includes failed requests. Percentiles use nearest rank. Lag is first observed',
             'public hybrid search / received webhook, measured from the scheduled arrival.', '',
             '| Scenario / operation | Attempts | Errors | Success/s | p50 ms | p95 ms | max ms |',
             '| --- | ---: | ---: | ---: | ---: | ---: | ---: |']
    for run in report['runs']:
        for operation, row in run.get('requests', {}).items():
            latency = row['latency']
            lines.append(f"| {run['scenario']['name']} / {operation} | {row['attempts']} | "
                         f"{row['errors']} | {row['successes_per_second']} | {latency['p50_ms']} | "
                         f"{latency['p95_ms']} | {latency['max_ms']} |")
        lines += ['', f"## {run['scenario']['name']}", '',
                  f"Status: {run['status']}. Scenario SHA256: {run['scenario_sha256']}.",
                  f"Duration: {run.get('elapsed_seconds')} s. Users exercised: {run.get('users_exercised')}.",
                  f"Work: {run.get('work')}", f"Faults: {run.get('faults', [])}"]
        if run.get('driver_errors'):
            lines.append(f"Driver errors: {run['driver_errors']}")
        if run.get('probes'):
            lines += [f"Observer probes (separate from workload): {run['probes']}",
                      'Probes share the APIs and can influence measured request latency.']
        for operation, row in run.get('lag', {}).items():
            lines.append(f"{operation}: {row}")
        if run.get('fault_windows'):
            lines += ['', 'Requests grouped by start time relative to the kill:', '', '```json',
                      __import__('json').dumps(run['fault_windows'], indent=2), '```']
        if run.get('error'):
            lines.append(f"Error: {run['error']}")
    lines += ['', '## Reproduction metadata', '', '```json', __import__('json').dumps(
        {'versions': report['versions'], 'source_sha256': report.get('source_sha256', {}),
         'runs': [r['scenario'] for r in report['runs']]}, indent=2), '```', '']
    return '\n'.join(lines)
