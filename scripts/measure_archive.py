#!/usr/bin/env python3
"""Measure synthetic archive throughput on an existing local demo/dev stack.

The measurement observes the public change feed and connector health. It does
not reconstruct currentness or replay behaviour; those are owned by the
archive acceptance tests and the public API contract.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import pathlib
import platform
import re
import statistics
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

from archive_source import seed
from local import Stack


UTC = dt.timezone.utc
WORKER_LOG_RE = re.compile(r"worker\.log(?:\.\d+)?$")
ROLLUP_WINDOW = "1h"


def _now() -> dt.datetime:
    return dt.datetime.now(UTC)


def _iso(value: dt.datetime) -> str:
    return value.astimezone(UTC).isoformat().replace("+00:00", "Z")


def _parse_time(value) -> dt.datetime | None:
    if not isinstance(value, str):
        return None
    try:
        parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=UTC)
    return parsed.astimezone(UTC)


def _number(value):
    if isinstance(value, bool):
        return None
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


def _rounded(value):
    number = _number(value)
    return None if number is None else round(number, 3)


def _hardware_metadata() -> dict:
    memory_total = None
    try:
        for line in pathlib.Path("/proc/meminfo").read_text().splitlines():
            if line.startswith("MemTotal:"):
                memory_total = int(line.split()[1]) * 1024
                break
    except (OSError, ValueError, IndexError):
        pass
    return {
        "platform": platform.platform(),
        "system": platform.system(),
        "release": platform.release(),
        "machine": platform.machine(),
        "processor": platform.processor(),
        "python": platform.python_version(),
        "cpu_count": os.cpu_count(),
        "memory_total_bytes": memory_total,
    }


def _summary_only(summary: dict | None) -> dict:
    """Keep rollups comparable without pretending percentiles are subtractable."""
    summary = summary or {}
    return {
        "count": int(summary.get("count") or 0),
        "errors": int(summary.get("errors") or 0),
        # Keep the source rollup precision intact. Derived values are rounded
        # only after the weighted subtraction below.
        "mean_ms": _number(summary.get("mean_ms")),
    }


def _rollup_items(payload: dict, kind: str) -> list[dict]:
    items = []
    for raw in payload.get("items", []):
        if not isinstance(raw, dict):
            continue
        if kind == "steps":
            item = {"step": raw.get("step", "")}
        else:
            item = {
                "plugin_id": raw.get("plugin_id", ""),
                "plugin_version": raw.get("plugin_version", ""),
                "operation": raw.get("operation", ""),
            }
        item["summary"] = _summary_only(raw.get("summary"))
        items.append(item)
    return items


def _rollup_snapshot(api) -> dict:
    snapshot = {
        "captured_at": _iso(_now()),
        "window": ROLLUP_WINDOW,
        "steps": [],
        "plugins": [],
        "errors": [],
    }
    for kind, endpoint in (
        ("steps", "/v0/admin/stats/steps?window=" + ROLLUP_WINDOW),
        ("plugins", "/v0/admin/stats/plugins?window=" + ROLLUP_WINDOW),
    ):
        try:
            snapshot[kind] = _rollup_items(api("GET", endpoint), kind)
        except RuntimeError as error:
            # Rollups are supplementary evidence. Preserve the measurement
            # and make a missing permission or unavailable read explicit.
            snapshot["errors"].append({"endpoint": endpoint, "error": str(error)})
    return snapshot


def _rollup_key(item: dict, kind: str) -> tuple:
    if kind == "steps":
        return (item.get("step", ""),)
    return (item.get("plugin_id", ""), item.get("plugin_version", ""), item.get("operation", ""))


def _rollup_delta(before: dict, after: dict) -> dict:
    """Compare count/errors and derive a positive run mean when possible.

    A mean cannot be subtracted directly. The weighted difference of the two
    sums, divided by the count delta, is the run-local mean when the rollup
    window is monotonic and exclusive. Percentiles never enter this operation.
    """
    delta = {
        "captured_at": _iso(_now()),
        "fields": ["count", "errors", "mean_ms"],
        "steps": [],
        "plugins": [],
        "notes": [
            "mean_ms is derived from weighted before/after sums and the count delta; it is run-local only for an exclusive monotonic window",
            "mean_ms is null when the count delta is not positive or a source mean is unavailable",
            "percentiles are intentionally omitted because subtracting them is not meaningful",
        ],
    }
    if before.get("errors") or after.get("errors"):
        delta.update({
            "suppressed": True,
            "suppression_reason": "before or after rollup snapshot had read errors",
            "before_errors": before.get("errors", []),
            "after_errors": after.get("errors", []),
        })
        return delta
    for kind in ("steps", "plugins"):
        left = {_rollup_key(item, kind): item for item in before.get(kind, [])}
        right = {_rollup_key(item, kind): item for item in after.get(kind, [])}
        for key in sorted(set(left) | set(right)):
            previous = _summary_only(left.get(key, {}).get("summary"))
            current = _summary_only(right.get(key, {}).get("summary"))
            values = {}
            for field in ("count", "errors"):
                values[field] = current[field] - previous[field]
            count_delta = values["count"]
            if count_delta <= 0:
                values["mean_ms"] = None
            elif previous["count"] == 0 and current["mean_ms"] is not None:
                values["mean_ms"] = _rounded(current["mean_ms"])
            elif previous["mean_ms"] is None or current["mean_ms"] is None:
                values["mean_ms"] = None
            else:
                total_after = current["mean_ms"] * current["count"]
                total_before = previous["mean_ms"] * previous["count"]
                run_mean = (total_after - total_before) / count_delta
                values["mean_ms"] = _rounded(run_mean) if run_mean >= 0 else None
            identity = right.get(key, left.get(key, {})).copy()
            identity["summary"] = values
            delta[kind].append(identity)
    return delta


def _read_provider_log(path: pathlib.Path | None) -> dict | None:
    if path is None:
        return None
    evidence = {"path": str(path.resolve()), "requests": 0, "records": 0, "errors": 0}
    if not path.exists():
        evidence["error"] = "provider log was not found"
        return evidence
    try:
        raw = path.read_text()
    except OSError as error:
        evidence["error"] = f"provider log could not be read: {type(error).__name__}"
        return evidence
    records = []
    try:
        decoded = json.loads(raw)
        if isinstance(decoded, list):
            records = [item for item in decoded if isinstance(item, dict)]
    except json.JSONDecodeError:
        records = []
    if not records:
        for line in raw.splitlines():
            try:
                item = json.loads(line)
            except json.JSONDecodeError:
                continue
            if isinstance(item, dict):
                records.append(item)
    inputs = [_number(item.get("items")) for item in records]
    inputs = [value for value in inputs if value is not None]
    sizes = [_number(item.get("bytes")) for item in records]
    sizes = [value for value in sizes if value is not None]
    latencies = [_number(item.get("latency_ms")) for item in records]
    latencies = [value for value in latencies if value is not None]
    modes = {}
    for item in records:
        mode = item.get("mode") or ""
        modes[mode] = modes.get(mode, 0) + 1
    evidence.update({
        "requests": len(records),
        "input_items": int(sum(inputs)),
        "mean_inputs_per_request": _rounded(statistics.fmean(inputs)) if inputs else None,
        "max_inputs_per_request": int(max(inputs)) if inputs else 0,
        "input_bytes": int(sum(sizes)),
        "mean_latency_ms": _rounded(statistics.fmean(latencies)) if latencies else None,
        "modes": modes,
        "errors": sum(1 for item in records if item.get("status", 200) >= 400),
        "records": len(records),
    })
    return evidence


def _worker_log_paths(directory: pathlib.Path) -> list[pathlib.Path]:
    return sorted(
        path for path in directory.iterdir()
        if path.is_file() and WORKER_LOG_RE.fullmatch(path.name)
    )


def _worker_outcomes(directory: pathlib.Path, started_at: dt.datetime, finished_at: dt.datetime, record_ids: set[str]) -> dict:
    paths = _worker_log_paths(directory)
    records = []
    for path in paths:
        try:
            lines = path.read_text(errors="replace").splitlines()
        except OSError:
            continue
        for line in lines:
            try:
                item = json.loads(line)
            except json.JSONDecodeError:
                continue
            if item.get("msg") != "processing outcome" or item.get("stage") not in {"baseline", "enrichment"}:
                continue
            if item.get("record_id") not in record_ids:
                continue
            occurred = _parse_time(item.get("ts"))
            if occurred is None or occurred < started_at or occurred > finished_at:
                continue
            records.append(item)

    result = {
        "paths": [str(path.resolve()) for path in paths],
        "window": {"from": _iso(started_at), "to": _iso(finished_at)},
        "timing_basis": "worker processing outcome duration_ms; exact stage service duration, not a queue interval",
        "coverage_note": "Worker logs are bounded or rotated; retained records are exact, but a deque that dropped older lines makes these counts a lower bound.",
        "filter_basis": "record_id observed in this run's public change events",
        "observed_record_ids": len(record_ids),
        "baseline": {},
        "enrichment": {},
    }
    for stage in ("baseline", "enrichment"):
        stage_records = [item for item in records if item.get("stage") == stage]
        durations = [_number(item.get("duration_ms")) for item in stage_records]
        durations = [value for value in durations if value is not None]
        outcomes = {}
        codes = {}
        for item in stage_records:
            outcome = item.get("outcome", "")
            outcomes[outcome] = outcomes.get(outcome, 0) + 1
            code = item.get("code") or ""
            if code:
                codes[code] = codes.get(code, 0) + 1
        result[stage] = {
            "records": len(stage_records),
            "succeeded": outcomes.get("succeeded", 0),
            "errors": sum(count for outcome, count in outcomes.items() if outcome != "succeeded"),
            "outcomes": outcomes,
            "codes": codes,
            "mean_ms": _rounded(statistics.fmean(durations)) if durations else None,
        }
    return result


def _find_steps(items: list[dict], *names: str) -> list[dict]:
    wanted = set(names)
    return [item for item in items if item.get("step") in wanted]


def _find_plugins(items: list[dict], operation: str | None = None, prefix: str | None = None) -> list[dict]:
    found = []
    for item in items:
        if operation is not None and item.get("operation") != operation:
            continue
        if prefix is not None and not item.get("plugin_id", "").startswith(prefix):
            continue
        found.append(item)
    return found


def _stage_evidence(report: dict, worker: dict, provider: dict | None) -> dict:
    after = report["rollups"]["delta"]
    times = report["finish_times_seconds"]
    return {
        "acquisition": {
            "members": report["members"],
            "accepted_events": report["accepted_events"],
            "finish_seconds": times["accepted"],
            "connector_rollups": _find_plugins(after["plugins"], prefix="connector.object_storage_archive"),
            "timing_basis": "client wall clock from connector submission to all record.accepted events and source checkpoint completion",
        },
        "normalization/materialization": {
            "normalization": "built-in text/plain normalization; no external normalization plugin call is timed",
            "materialized_records": report["materialized_records"],
            "finish_seconds": times["materialized"],
            "step_rollups": _find_steps(after["steps"], "materialized"),
            "timing_basis": "materialized rollup follows the public document timeline and includes time waiting for the step; no CPU timing is inferred",
        },
        "segment_and_embed": {
            "plugin_rollups": _find_plugins(after["plugins"], operation="segment_and_embed"),
            "provider": provider,
            "timing_basis": "plugin rollup measures engine-to-plugin calls; provider log measures loopback fake request batches",
        },
        "baseline_projection": {
            "searchable_records": report["searchable_records"],
            "finish_seconds": times["searchable"],
            "step_rollups": _find_steps(after["steps"], "baseline", "segmented", "retrieval_ready", "accepted_to_searchable"),
            "worker_outcomes": worker["baseline"],
            "timing_basis": "worker baseline duration_ms plus public record.retrieval_ready completion; rollup timeline values include documented queue wait",
        },
        "enrichment": {
            "enriched_records": report["enriched_records"],
            "finish_seconds": times["enriched"],
            "step_rollups": _find_steps(after["steps"], "enrichment", "enriched"),
            "worker_outcomes": worker["enrichment"],
            "timing_basis": "worker enrichment duration_ms plus public record.enrichment_available completion; rollup timeline values include documented queue wait",
        },
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--stack", required=True, help="Running local stack name under .scratch/")
    parser.add_argument("--items", type=int, default=5000, help="Synthetic unique filler members, plus three revisions")
    parser.add_argument("--batch-size", type=int, default=100)
    parser.add_argument("--concurrency", type=int, default=8)
    parser.add_argument("--timeout", type=int, default=600)
    parser.add_argument(
        "--provider-log",
        type=pathlib.Path,
        help="Optional JSONL provider proxy log to summarize in stage evidence",
    )
    args = parser.parse_args(argv)
    if (
        not re.fullmatch(r"[a-z0-9-]+", args.stack)
        or args.items < 1
        or not 1 <= args.batch_size <= 1000
        or not 1 <= args.concurrency <= 32
        or args.timeout < 1
    ):
        parser.error("invalid stack, item count, batch size, concurrency or timeout")
    directory = pathlib.Path(__file__).resolve().parents[1] / ".scratch" / args.stack
    if not (directory / "config.json").exists():
        parser.error("start the named local stack first")
    stack = Stack(args.stack)
    run = uuid.uuid4().hex[:12]
    # Fresh bytes prevent repeated runs from measuring verified-Blob cache hits.
    private = seed(stack, bucket="synthetic-archive-" + run, count=args.items, marker=run)
    fixture = json.loads(private.read_text())
    token = stack.state["demo"]
    base = f"http://127.0.0.1:{stack.state['api_port']}"

    def api(method, path, body=None):
        req = urllib.request.Request(
            base + path,
            method=method,
            data=None if body is None else json.dumps(body).encode(),
            headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
        )
        try:
            with urllib.request.urlopen(req, timeout=30) as response:
                return json.load(response)
        except urllib.error.HTTPError as exc:
            raise RuntimeError(f"Archive measurement API {method} failed with HTTP {exc.code}") from None
        except Exception:
            raise RuntimeError(f"Archive measurement API {method} failed") from None

    corpus = api(
        "POST",
        "/v0/corpora",
        {"name": "Synthetic archive measurement", "idempotency_key": "archive-corpus-" + run},
    )["corpus_id"]
    path = "/v0/changes?" + urllib.parse.urlencode({"corpus_id": corpus, "limit": 100})
    cursor = api("GET", path)["next_cursor"]
    config = {**fixture["config"], "batch_size": args.batch_size, "concurrency": args.concurrency}
    before_rollups = _rollup_snapshot(api)
    run_started_wall = _now()
    started = time.monotonic()
    created = api(
        "POST",
        "/v0/connectors",
        {
            "idempotency_key": "archive-measure-" + run,
            "corpus_id": corpus,
            "source_namespace": "synthetic-archive",
            "kind": "object_storage_archive",
            "config": config,
            "schedule": {"interval_seconds": 1},
            "credential": {"secret": fixture["credential"]},
        },
    )
    connector = created["connector_id"]
    accepted_at = None
    materialized_at = None
    ready_at = None
    enriched_at = None
    seen_materialized = set()
    seen_searchable = set()
    seen_enriched = set()
    seen_accepted_records = set()
    accepted_events = set()
    samples = []
    expected = fixture["members_total"]
    expected_records = expected - 1
    health = {}
    failure = None
    try:
        while time.monotonic() - started < args.timeout:
            health = api("GET", "/v0/connectors/" + connector)["health"]
            done = (health.get("diagnostics") or {}).get("members_done", 0)
            # Drain full pages so the observer does not cap measured throughput.
            while True:
                page = api("GET", path + "&" + urllib.parse.urlencode({"cursor": cursor}))
                for event in page["items"]:
                    event_type = event.get("type")
                    resource = event.get("resource") or {}
                    resource_id = resource.get("id")
                    if event_type == "record.accepted":
                        accepted_events.add(event.get("event_id"))
                        if resource_id:
                            seen_accepted_records.add(resource_id)
                    elif event_type == "record.materialized" and resource_id:
                        seen_materialized.add(resource_id)
                    elif event_type == "record.retrieval_ready" and resource_id:
                        seen_searchable.add(resource_id)
                    elif event_type == "record.enrichment_available" and resource_id:
                        # The public contract names this event
                        # record.enrichment_available; it is the durable
                        # vector-enrichment completion oracle.
                        seen_enriched.add(resource_id)
                cursor = page["next_cursor"]
                if not page["has_more"]:
                    break
            elapsed = time.monotonic() - started
            if accepted_at is None and done == expected and len(accepted_events) == expected:
                accepted_at = elapsed
            if materialized_at is None and len(seen_materialized) >= expected_records:
                materialized_at = elapsed
            if ready_at is None and len(seen_searchable) >= expected_records:
                ready_at = elapsed
            if enriched_at is None and len(seen_enriched) >= expected_records:
                enriched_at = elapsed
            samples.append(
                {
                    "seconds": round(elapsed, 3),
                    "accepted": len(accepted_events),
                    "checkpoint_members_done": done,
                    "materialized_records": len(seen_materialized),
                    "searchable_records": len(seen_searchable),
                    "enriched_records": len(seen_enriched),
                }
            )
            if health.get("last_error"):
                raise RuntimeError("Archive measurement source failure: " + health["last_error"]["code"])
            if accepted_at is not None and ready_at is not None and enriched_at is not None:
                break
            time.sleep(0.2)
    except Exception as error:
        failure = str(error)
    finally:
        try:
            api(
                "POST",
                "/v0/connectors/" + connector + "/disable",
                {"idempotency_key": "archive-measure-stop-" + run},
            )
        except Exception as error:
            failure = failure or str(error)

    finished_wall = _now()
    after_rollups = _rollup_snapshot(api)
    run_record_ids = seen_accepted_records | seen_materialized | seen_searchable | seen_enriched
    worker = _worker_outcomes(directory, run_started_wall, finished_wall, run_record_ids)
    provider = _read_provider_log(args.provider_log.resolve() if args.provider_log else None)
    accepted_rate = expected / accepted_at if accepted_at else 0
    ready_rate = expected_records / ready_at if ready_at else 0
    enriched_rate = expected_records / enriched_at if enriched_at else 0
    bottleneck = None
    if accepted_rate < 50:
        bottleneck = "acquisition: source reading, Upload Session transfer/verification and durable submission; compare concurrency runs"
    elif ready_at is not None and accepted_at is not None and ready_at > accepted_at:
        bottleneck = "processing after durable acceptance: normalization, segmentation and search publication"
    status = "complete" if accepted_at and ready_at and enriched_at else "timed_out"
    if failure:
        status = "failed"
    report = {
        "stack": args.stack,
        "corpus_id": corpus,
        "connector_id": connector,
        "run_id": run,
        "fresh_source_bytes": True,
        "source_marker": run,
        "source_fixture_path": str(private.resolve()),
        "members": expected,
        "unique_records": expected_records,
        "batch_size": args.batch_size,
        "concurrency": args.concurrency,
        "timeout_seconds": args.timeout,
        "workload": {
            "kind": "synthetic object-storage archive",
            "requested_filler_members": args.items,
            "revision_members": 3,
            "members": expected,
            "unique_records": expected_records,
            "source_bytes_unique_per_run": True,
            "source_marker": run,
        },
        "hardware": _hardware_metadata(),
        "accepted_seconds": accepted_at,
        "searchable_seconds": ready_at,
        "enriched_seconds": enriched_at,
        "accepted_members_per_second": round(accepted_rate, 2),
        "searchable_records_per_second": round(ready_rate, 2),
        "enriched_records_per_second": round(enriched_rate, 2),
        "finish_times_seconds": {
            "accepted": accepted_at,
            "materialized": materialized_at,
            "searchable": ready_at,
            "enriched": enriched_at,
        },
        "accepted_events": len(accepted_events),
        "materialized_records": len(seen_materialized),
        "searchable_records": len(seen_searchable),
        "enriched_records": len(seen_enriched),
        "public_event_contract": {
            "accepted": "record.accepted",
            "materialized": "record.materialized",
            "searchable": "record.retrieval_ready",
            "enriched": "record.enrichment_available",
            "enrichment_required_before_finish": True,
            "currentness_and_replay_oracle": "owned by the public API and archive acceptance tests; this measurement only counts public events",
        },
        "target_accepted_members_per_second": 50,
        "bottleneck": bottleneck,
        "samples": samples,
        "status": status,
        "error": failure,
        "worker_outcomes": worker,
        "rollups": {
            "scope": "organization-wide for the measurement corpus's Organization",
            "exclusive_workload_required": True,
            "exclusivity_note": "Before/after rollup deltas are attributable to this run only when no other workload writes to the Organization during the measurement.",
            "window": ROLLUP_WINDOW,
            "before": before_rollups,
            "after": after_rollups,
            "delta": _rollup_delta(before_rollups, after_rollups),
        },
        "timings_window": "1h, Organization-wide; before/after deltas require an exclusive Organization workload",
    }
    report["stage_evidence"] = _stage_evidence(report, worker, provider)
    if provider is not None:
        report["provider_log"] = provider
    # Keep the old top-level names useful to existing consumers while exposing
    # the reduced (count/errors/mean_ms) representation mandated above.
    report["plugin_timings"] = after_rollups["plugins"]
    report["processing_timings"] = after_rollups["steps"]
    out = stack.directory / f"archive-measure-{run}.json"
    out.write_text(json.dumps(report, indent=2) + "\n")
    print(
        json.dumps(
            {key: value for key, value in report.items() if key not in ("samples", "rollups", "worker_outcomes", "stage_evidence")},
            indent=2,
        )
    )
    print("Stage evidence:", json.dumps(report["stage_evidence"], indent=2))
    print("Evidence:", out)
    if report["status"] != "complete":
        raise SystemExit(1)
    return report


if __name__ == "__main__":
    main()
