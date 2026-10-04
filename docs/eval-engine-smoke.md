# Run a temporary Quivr stack on Modal

This contributor guide starts a CPU VM on Modal, ingests three fixed public
records through Quivr, checks lexical, semantic and hybrid search, and removes
the stack. The result contains counts and infrastructure lineage. It is a smoke
check, with no candidate, held-out data, quality scores or promotion verdict.
Use the [finalist confirmation runner](eval-engine-confirmation.md) for held-out measurement and gates.

## Prepare

Use Python 3.12, a committed checkout, an authenticated Modal account with VM Sandboxes enabled,
and the [shared evaluation control store](eval-modal.md#prerequisites). Set
`EVAL_CONTROL_DATABASE_URL` locally with verified TLS, and `EVAL_CONTROL_CA_PEM`
for a private CA. There is no local fallback if the control store is unavailable.
Tracking uses local `MLFLOW_TRACKING_URI`, `MLFLOW_TRACKING_USERNAME` and
`MLFLOW_TRACKING_PASSWORD`; without them the aggregate result stays in the outbox
for later sync. The remote smoke receives no provider or tracking secrets.

Install the small smoke dependency set (example, not run in a fresh environment):

```sh
python -m pip install -r scripts/eval/requirements-engine.txt
modal setup
```

The image uses Go from `go.mod`, Python 3.12, Debian Docker, checksum-pinned
Compose 2.39.4, the pinned e5 model/tokenizer, and the existing local-stack images:
Postgres, Temporal, SeaweedFS, Weaviate and CPU TEI. Core ingestion/retrieval
plugins and the API/worker run beside those containers. No API tunnel, snapshot
or persistent data volume is exported. VM networking, disk capacity and runtime
availability need the operator's first live check; local tests cannot prove them.

This keyless command was exercised locally and does not build an image:

```sh
python scripts/eval/modal_engine.py --dry-run
```

An optional `--policy path.json` overrides only resource bounds, rates and the
aggregate experiment name. For example, not run:

```json
{"modal_daily_usd": 10, "modal_usd_per_second": 0.001,
 "startup_seconds": 900, "max_seconds": 1800,
 "cleanup_seconds": 60, "reaper_seconds": 90,
 "keepalive_seconds": 10, "cpu": 8, "memory_mib": 16384}
```

Review the assumed compute rate for the chosen CPU/memory before running.
The default maximum reservation is 2,850 seconds × $0.001/second = $2.85. It covers startup, execution,
cleanup and reaper grace. Image builds, storage and other account charges
require separate budgets; this is a runner compute cap, not a full invoice cap.
No automatic measurement retries run. An incomplete or uncertain invocation
keeps its conservative reservation.

## Run the live smoke

Coordinator-only example, not run by the worker; CI refuses live dispatch:

```sh
python scripts/eval/modal_engine.py --smoke --allow-paid \
  --campaign example-engine-smoke --outbox .scratch/eval/engine-smoke
```

Success exits 0 with `status=complete`, three documents, three searches, all
three modes, a tracking receipt and `confirmation_available=false`. It publishes
only after remote stack cleanup and observed Sandbox termination, then closes
the ephemeral App. Failures, capped work, cancellation and leased work exit 2.
Failure diagnostics contain the exception class and a fixed sanitized message;
raw errors and child logs stay remote. Progress logs show phases/counts only.

The campaign freezes code, resource policy, fixture and dependency lineage.
Changing them needs a new smoke campaign. Completed work returns `reused`
without another VM, and replay retries tracking from the canonical SQL payload.
For pending tracking receipts, rerun the identical command with tracking
credentials, or use the [results CLI](eval-results.md#recover-an-offline-run) with
`scripts/eval/results --directory .scratch/eval/engine-smoke sync`.

## Check cancellation

Coordinator-only example, not run on Modal by the worker. It sends SIGTERM once
the remote runner starts, waits for exit, and checks observed termination:

```sh
python - <<'PY'
import json, pathlib, signal, subprocess, sys, time
name = 'example-engine-cancel'
outbox = pathlib.Path('.scratch/eval/engine-smoke')
state = outbox / 'active' / (name + '.json')
process = subprocess.Popen([sys.executable, 'scripts/eval/modal_engine.py',
    '--smoke', '--allow-paid', '--campaign', name, '--outbox', str(outbox)])
deadline = time.monotonic() + 1800
try:
    while process.poll() is None:
        row = json.loads(state.read_text()) if state.exists() else {}
        if row.get('phase') in ('starting', 'ingesting', 'searching'):
            process.send_signal(signal.SIGTERM)
            break
        if time.monotonic() >= deadline:
            raise TimeoutError('remote smoke did not start')
        time.sleep(.1)
    assert process.wait(timeout=300) == 2
    row = json.loads(state.read_text())
    assert row['state'] == 'terminated', row
    print('Verified Sandbox termination; inspect the App:', row['app_id'])
finally:
    if process.poll() is None:
        process.send_signal(signal.SIGTERM)
        process.wait(timeout=300)
PY
```

Use a fresh campaign name for each cancellation check. Run `modal app info <app-id>`
for the printed identity and verify its state is stopped. Record smoke duration, ledger and cancellation
evidence in the campaign ticket. Worker tests cover the local Docker stack,
SQL admission and SDK lifecycle; the coordinator supplies live Modal evidence.

## Recover a stopped dispatcher

The Sandbox entrypoint supervises Docker and the runner. Dispatcher keepalives
renew ownership; a missed keepalive, lease loss, remote failure or deadline stops
the work. SIGINT/SIGTERM requests explicit termination. Abrupt dispatcher death
or loss of the creation response is reaped remotely after the keepalive grace,
with the Sandbox lifetime as a final bound. Idle timeout alone is insufficient.

Private recovery files under `outbox/active/` hold App/Sandbox identities and the
last observed state. `creating` means the creation response was not observed;
inspect that App in the Modal dashboard to find its Sandbox.
`running` or `unknown` requires inspection in Modal. Never treat these as clean
success or delete uncertain reservations. On teardown transport failure the
command fails, retaining recovery evidence; stop only that recorded App/Sandbox.
Raw logs are ephemeral and may disappear on termination. Capture them privately
in the operator console before stopping if needed; never copy them into git.

For an unpaid local stack check, this command was exercised in 37.2 seconds:

```sh
mkdir -p .scratch
python scripts/eval/engine_stack.py --local --output .scratch/engine-smoke.json \
  > .scratch/engine-smoke.log 2>&1
```

It requires local Docker/Compose and Go from `go.mod`. Its JSON contains aggregate
counts and cleanup status; the separate log may contain internal record IDs.
