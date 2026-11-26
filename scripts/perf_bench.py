#!/usr/bin/env python3
"""Reproducible local + remote performance benchmark harness for Quivr.

Single command measuring:
- Ingestion throughput (docs/s for accepted, searchable, enriched) and HTTP accept latency
- Cold search p50/p95/p99 latency by phase (routing, coverage, query_encoding,
  index_query, hydration, plugin_rounds)
- Warm search p50/p95/p99 latency by phase
- Dependency call count deltas (Weaviate HTTP/gRPC, S3 GetObject/PutObject, PG queries)
- JSON report + Markdown report + Cold-vs-Warm & Baseline-vs-Candidate delta tables

Examples:
  make perf-bench
  python3 scripts/perf_bench.py --stack dev --ingest-docs 100
  python3 scripts/perf_bench.py --base-url https://api.example --token "$TOKEN" \\
      --api-metrics-url http://api-probe:8081/metrics \\
      --worker-metrics-url http://worker-probe:8082/metrics
  python3 scripts/perf_bench.py --compare baseline/bench.json candidate/bench.json
"""

from __future__ import annotations

import argparse
import concurrent.futures
import datetime
import json
import os
import pathlib
import platform
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

SCRIPT_DIR = pathlib.Path(__file__).resolve().parent
ROOT = SCRIPT_DIR.parent
for _path in (str(SCRIPT_DIR), str(ROOT)):
    if _path not in sys.path:
        sys.path.insert(0, _path)

import perf_bench_corpus as corpus_gen
import perf_bench_metrics as metrics

ALL_PROFILES = ("ingestion", "cold_search", "warm_search")
DEFAULT_MODES = ("hybrid", "lexical", "semantic")


class BenchClient:
    """Minimal stdlib HTTP client for Quivr public API and Prometheus /metrics probes."""

    def __init__(self, base_url: str, token: str, timeout: float = 30.0, metrics_token: str = ""):
        self.base_url = base_url.rstrip("/")
        self.token = token
        self.timeout = timeout
        self.metrics_token = metrics_token

    def call(self, method: str, path: str, body: dict | None = None, expected: int | tuple[int, ...] = 200) -> dict:
        expected_codes = (expected,) if isinstance(expected, int) else tuple(expected)
        data = json.dumps(body).encode() if body is not None else None
        headers = {"Authorization": f"Bearer {self.token}", "Content-Type": "application/json"}
        req = urllib.request.Request(self.base_url + path, data=data, method=method, headers=headers)
        try:
            response = urllib.request.urlopen(req, timeout=self.timeout)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read()
            payload = json.loads(raw) if raw else {}
            if response.status not in expected_codes:
                code = payload.get("code", "") if isinstance(payload, dict) else ""
                raise RuntimeError(f"{method} {path} returned HTTP {response.status} ({code or raw[:160]!r})")
            return payload

    def timed_post_record(self, command: dict) -> dict:
        started = time.perf_counter()
        try:
            payload = self.call("POST", "/v0/records", command, expected=(200, 202))
            return {
                "ok": True,
                "ms": (time.perf_counter() - started) * 1000.0,
                "receipt_id": payload.get("receipt_id"),
            }
        except Exception as error:
            return {
                "ok": False,
                "ms": (time.perf_counter() - started) * 1000.0,
                "error": f"{type(error).__name__}: {str(error)[:160]}",
            }

    def timed_search(self, body: dict) -> dict:
        started = time.perf_counter()
        try:
            payload = self.call("POST", "/v0/search", body, expected=200)
            wall_ms = (time.perf_counter() - started) * 1000.0
            elapsed_ms, phases = metrics.extract_search_phases(payload)
            usage = payload.get("usage") if isinstance(payload, dict) else None
            rounds = usage.get("rounds") if isinstance(usage, dict) else None
            items = payload.get("items") if isinstance(payload, dict) else None
            return {
                "ok": True,
                "wall_ms": wall_ms,
                "elapsed_ms": elapsed_ms,
                "phases": phases,
                "rounds": rounds if isinstance(rounds, int) else None,
                "hits": len(items) if isinstance(items, list) else None,
            }
        except Exception as error:
            return {
                "ok": False,
                "wall_ms": (time.perf_counter() - started) * 1000.0,
                "error": f"{type(error).__name__}: {str(error)[:160]}",
            }

    def scrape_metrics(self, urls: list[str]) -> tuple[dict[str, dict[tuple[str, str, str], int]] | None, list[str]]:
        if not urls:
            return None, []
        snapshots: dict[str, dict[tuple[str, str, str], int]] = {}
        errors: list[str] = []
        headers = {"Authorization": f"Bearer {self.metrics_token}"} if self.metrics_token else {}
        for url in urls:
            req = urllib.request.Request(url, method="GET", headers=headers)
            try:
                with urllib.request.urlopen(req, timeout=min(self.timeout, 10.0)) as response:
                    text = response.read().decode("utf-8", errors="replace")
                    snapshots[url] = metrics.parse_dependency_counters(text)
            except Exception as error:
                errors.append(f"{url}: {type(error).__name__}: {str(error)[:120]}")
        if not snapshots or errors:
            return None, errors
        return snapshots, []


def _git_output(*args: str) -> str:
    try:
        return subprocess.run(
            ["git", *args],
            cwd=ROOT,
            capture_output=True,
            text=True,
            check=True,
        ).stdout.strip()
    except Exception:
        return ""


def _host_info() -> dict:
    return {
        "platform": platform.platform(),
        "machine": platform.machine(),
        "logical_cpus": os.cpu_count(),
        "python": platform.python_version(),
    }


def _dedupe_urls(urls: list[str]) -> list[str]:
    seen: set[str] = set()
    out: list[str] = []
    for url in urls:
        if url and url not in seen:
            seen.add(url)
            out.append(url)
    return out


def _snapshot_deps(
    client: BenchClient,
    urls: list[str],
) -> tuple[dict[str, dict[tuple[str, str, str], int]] | None, list[str]]:
    return client.scrape_metrics(_dedupe_urls(urls))


def _finalize_deps(
    before: dict[str, dict[tuple[str, str, str], int]] | None,
    before_errors: list[str],
    client: BenchClient,
    urls: list[str],
    unit_count: int,
    unit_name: str,
) -> dict:
    if not urls:
        return metrics.summarize_dependency_calls(None, unit_count=unit_count, unit_name=unit_name, available=False)
    after, after_errors = _snapshot_deps(client, urls)
    errors = before_errors + after_errors
    if before is None or after is None or errors:
        return metrics.summarize_dependency_calls(
            None,
            unit_count=unit_count,
            unit_name=unit_name,
            available=False,
            errors=errors,
        )
    delta, reset = metrics.endpoint_counter_delta(before, after)
    return metrics.summarize_dependency_calls(
        delta,
        unit_count=unit_count,
        unit_name=unit_name,
        available=True,
        reset_detected=reset,
    )


def run_ingestion_profile(
    client: BenchClient,
    corpus_id: str,
    docs: list[dict],
    concurrency: int,
    timeout_seconds: float,
    metrics_urls: list[str],
    poll_interval: float = 0.2,
) -> dict:
    """Ingest synthetic documents and observe accepted, searchable and enriched throughput."""
    changes_base = "/v0/changes?" + urllib.parse.urlencode({"corpus_id": corpus_id, "limit": 100})
    cursor = client.call("GET", changes_base)["next_cursor"]

    before_deps, before_errors = _snapshot_deps(client, metrics_urls)
    started = time.perf_counter()

    def submit_one(doc: dict) -> dict:
        command = {
            "idempotency_key": doc["record_key"],
            "source": {
                "corpus_id": corpus_id,
                "namespace": "perf-bench",
                "record_key": doc["record_key"],
            },
            "content": {"kind": "manifest", "parts": doc["parts"]},
        }
        return client.timed_post_record(command)

    if concurrency <= 1:
        submissions = [submit_one(doc) for doc in docs]
    else:
        with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
            submissions = list(pool.map(submit_one, docs))

    accepted_seconds = time.perf_counter() - started
    accepted = [s for s in submissions if s["ok"]]
    errors = len(submissions) - len(accepted)
    expected = len(accepted)

    seen_searchable: set[str] = set()
    seen_enriched: set[str] = set()
    searchable_seconds: float | None = None
    enriched_seconds: float | None = None
    deadline = started + timeout_seconds

    while expected > 0 and time.perf_counter() < deadline:
        while True:
            page = client.call("GET", changes_base + "&" + urllib.parse.urlencode({"cursor": cursor}))
            for event in page.get("items", []):
                event_type = event.get("type")
                resource_id = (event.get("resource") or {}).get("id")
                if not resource_id:
                    continue
                if event_type == "record.retrieval_ready":
                    seen_searchable.add(resource_id)
                elif event_type == "record.enrichment_available":
                    seen_enriched.add(resource_id)
            cursor = page.get("next_cursor", cursor)
            if not page.get("has_more"):
                break
        now_elapsed = time.perf_counter() - started
        if searchable_seconds is None and len(seen_searchable) >= expected:
            searchable_seconds = now_elapsed
        if enriched_seconds is None and len(seen_enriched) >= expected:
            enriched_seconds = now_elapsed
        if searchable_seconds is not None and enriched_seconds is not None:
            break
        if poll_interval > 0:
            time.sleep(poll_interval)

    dependency_calls = _finalize_deps(
        before_deps,
        before_errors,
        client,
        metrics_urls,
        unit_count=expected,
        unit_name="doc",
    )

    def rate(count: int, seconds: float | None) -> float | None:
        if seconds is None or seconds <= 0:
            return None
        return round(count / seconds, 2)

    accept_ms = [s["ms"] for s in accepted]
    timed_out = expected > 0 and (searchable_seconds is None or enriched_seconds is None)
    return {
        "status": "complete" if errors == 0 and not timed_out else "incomplete",
        "documents_total": len(docs),
        "accepted_count": expected,
        "searchable_count": len(seen_searchable),
        "enriched_count": len(seen_enriched),
        "errors": errors,
        "error_samples": [s["error"] for s in submissions if not s["ok"]][:5],
        "accepted_seconds": round(accepted_seconds, 3),
        "searchable_seconds": round(searchable_seconds, 3) if searchable_seconds is not None else None,
        "enriched_seconds": round(enriched_seconds, 3) if enriched_seconds is not None else None,
        "accepted_docs_per_sec": rate(expected, accepted_seconds),
        "searchable_docs_per_sec": rate(len(seen_searchable), searchable_seconds),
        "enriched_docs_per_sec": rate(len(seen_enriched), enriched_seconds),
        "accept_latency": metrics.distribution(accept_ms),
        "dependency_calls": dependency_calls,
    }


def _execute_search_batch(
    client: BenchClient,
    corpus_id: str,
    plan: list[tuple[str, str]],
    search_profile: str,
    limit: int,
    concurrency: int,
) -> dict[str, list[dict]]:
    def run_one(item: tuple[str, str]) -> tuple[str, dict]:
        query_text, mode = item
        body = {
            "query": query_text,
            "corpus_ids": [corpus_id],
            "mode": mode,
            "profile": search_profile,
            "limit": limit,
        }
        return mode, client.timed_search(body)

    if concurrency <= 1:
        results = [run_one(item) for item in plan]
    else:
        with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
            results = list(pool.map(run_one, plan))

    by_mode: dict[str, list[dict]] = {}
    for mode, sample in results:
        by_mode.setdefault(mode, []).append(sample)
    return by_mode


def run_search_profile(
    client: BenchClient,
    corpus_id: str,
    queries: list[dict],
    modes: list[str],
    search_profile: str,
    limit: int,
    concurrency: int,
    passes: int,
    warmup_passes: int,
    metrics_urls: list[str],
    distinct_per_mode: bool = False,
) -> dict:
    """Run optional warmup passes, then measured search passes with per-phase + dependency capture."""
    if distinct_per_mode:
        # Partition strictly unique queries across modes so cold runs never share
        # query-encoding cache entries between modes.
        per_mode = len(queries) // len(modes) if modes else 0
        single_pass = [
            (queries[mode_idx * per_mode + q_idx]["query"], mode)
            for q_idx in range(per_mode)
            for mode_idx, mode in enumerate(modes)
        ]
        queries_per_mode = per_mode
    else:
        single_pass = [(q["query"], mode) for q in queries for mode in modes]
        queries_per_mode = len(queries)
    warmup_failures = 0
    for _ in range(warmup_passes):
        warm = _execute_search_batch(client, corpus_id, single_pass, search_profile, limit, 1)
        warmup_failures += sum(1 for samples in warm.values() for s in samples if not s["ok"])

    measured_plan = single_pass * passes
    before_deps, before_errors = _snapshot_deps(client, metrics_urls)
    started = time.perf_counter()
    by_mode_samples = _execute_search_batch(client, corpus_id, measured_plan, search_profile, limit, concurrency)
    wall_seconds = time.perf_counter() - started

    all_samples = [sample for mode in modes for sample in by_mode_samples.get(mode, [])]
    dependency_calls = _finalize_deps(
        before_deps,
        before_errors,
        client,
        metrics_urls,
        unit_count=len(all_samples),
        unit_name="search",
    )
    return {
        "passes": passes,
        "warmup_passes": warmup_passes,
        "warmup_failures": warmup_failures,
        "concurrency": concurrency,
        "queries_per_mode": queries_per_mode,
        "wall_seconds": round(wall_seconds, 3),
        "overall": metrics.summarize_search_samples(all_samples),
        "by_mode": {mode: metrics.summarize_search_samples(by_mode_samples.get(mode, [])) for mode in modes},
        "dependency_calls": dependency_calls,
    }


def _restart_local_api(stack) -> None:
    """Restart only the local API process so in-process caches start cold."""
    pid = stack.state.get("api_pid")
    if pid:
        stack.signal_owned(pid, signal.SIGTERM)
        deadline = time.monotonic() + 15.0
        from local import alive
        while stack.owns_process(pid) and alive(pid) and time.monotonic() < deadline:
            time.sleep(0.1)
        if pid in stack.state.get("pids", []):
            stack.state["pids"].remove(pid)
            stack.save()
    stack.spawn("api", "config.json")
    stack.await_ready("probe_port", timeout=30)


def _start_local_stack(stack_name: str | None) -> tuple[object, bool]:
    """Attach to an existing local stack or start a fresh isolated stack."""
    from local import GO, Stack, run
    import connector_plugin

    if stack_name:
        stack = Stack(stack_name)
        if not stack.running():
            raise RuntimeError(f"local stack {stack_name!r} is not running; start it with `make dev` or omit --stack")
        return stack, False

    if platform.system() != "Linux" or platform.machine() != "x86_64":
        raise RuntimeError(
            f"isolated local stack requires Linux x86_64 (got {platform.system()}/{platform.machine()}); "
            "pass --stack <name> or --base-url <url> instead"
        )
    from prepare_embeddings import prepare as prepare_embeddings
    from prepare_tokenizer import prepare as prepare_tokenizer

    stack = Stack("quivr-perf-bench-" + uuid.uuid4().hex[:10])
    connector_plugin.select_first_party(stack, connector_plugin.CORE)
    prepare_tokenizer()
    prepare_embeddings()
    run([GO, "build", "-o", str(stack.directory / "quivr"), "./cmd/quivr"])
    stack.compose("pull", "--quiet")
    stack.compose("up", "-d", "--wait", "--wait-timeout", "300")
    stack.migrate()
    connector_plugin.start_first_party(stack)
    stack.start_processes()
    return stack, True


def _await_model_ready(
    client: BenchClient,
    marker: str,
    deadline_seconds: float = 180.0,
    poll_interval: float = 0.25,
) -> None:
    """Verify the embedding model end-to-end on a disposable non-empty probe Corpus.

    Empty-corpus semantic searches short-circuit in the engine without invoking
    the query encoder, so readiness requires ingesting one probe Record, waiting
    for vector enrichment, and receiving at least one semantic hit.
    """
    probe_key = f"perf-bench-ready-{marker}"
    probe_corpus = client.call(
        "POST",
        "/v0/corpora",
        {"name": f"Perf readiness probe {marker}", "idempotency_key": probe_key},
        expected=(200, 201),
    )["corpus_id"]
    changes_base = "/v0/changes?" + urllib.parse.urlencode({"corpus_id": probe_corpus, "limit": 50})
    cursor = client.call("GET", changes_base)["next_cursor"]
    client.call(
        "POST",
        "/v0/records",
        {
            "idempotency_key": probe_key,
            "source": {"corpus_id": probe_corpus, "namespace": "perf-bench-probe", "record_key": probe_key},
            "content": {
                "kind": "text",
                "text": "Hydrology watershed readiness probe verifying vector enrichment and query encoding.",
            },
        },
        expected=(200, 202),
    )

    deadline = time.monotonic() + deadline_seconds
    enriched = False
    while not enriched:
        while True:
            page = client.call("GET", changes_base + "&" + urllib.parse.urlencode({"cursor": cursor}))
            for event in page.get("items", []):
                if event.get("type") == "record.enrichment_available":
                    enriched = True
            cursor = page.get("next_cursor", cursor)
            if not page.get("has_more"):
                break
        if enriched:
            break
        if time.monotonic() >= deadline:
            raise RuntimeError("semantic readiness probe record never finished vector enrichment")
        if poll_interval > 0:
            time.sleep(poll_interval)

    while True:
        sample = client.timed_search(
            {
                "query": "hydrology watershed readiness probe",
                "corpus_ids": [probe_corpus],
                "mode": "semantic",
                "limit": 1,
            }
        )
        if sample["ok"] and (sample.get("hits") or 0) >= 1:
            return
        if time.monotonic() >= deadline:
            detail = sample.get("error") or f"semantic probe returned {sample.get('hits', 0)} hits"
            raise RuntimeError(f"semantic search never became ready: {detail}")
        if poll_interval > 0:
            time.sleep(poll_interval)


def run_benchmark(
    client: BenchClient,
    *,
    mode: str,
    profiles: list[str],
    modes: list[str],
    search_profile: str,
    limit: int,
    ingest_docs: int,
    words_per_doc: int,
    ingest_concurrency: int,
    ingest_timeout: float,
    cold_query_count: int,
    warm_query_count: int,
    warm_passes: int,
    search_concurrency: int,
    seed: int,
    content_marker: str,
    corpus_id: str | None = None,
    api_metrics_urls: list[str] | None = None,
    worker_metrics_urls: list[str] | None = None,
    before_cold_hook=None,
    cold_available: bool = True,
    cold_unavailable_reason: str | None = None,
    cold_reset_strategy: str = "none",
    poll_interval: float = 0.2,
    await_semantic_ready: bool = False,
) -> dict:
    """Execute the configured benchmark profiles against a local or remote Quivr API."""
    started = time.perf_counter()
    docs = corpus_gen.generate_documents(ingest_docs if "ingestion" in profiles else 0, words_per_doc, seed, content_marker)
    cold_total = cold_query_count * len(modes) if ("cold_search" in profiles and cold_available) else 0
    cold_queries = corpus_gen.generate_cold_queries(cold_total, seed)
    warm_queries = corpus_gen.generate_warm_queries(warm_query_count if "warm_search" in profiles else 0, seed)

    api_urls = list(api_metrics_urls or [])
    worker_urls = list(worker_metrics_urls or [])
    all_metrics_urls = _dedupe_urls(api_urls + worker_urls)

    report: dict = {
        "schema_version": metrics.SCHEMA_VERSION,
        "status": "failed",
        "metadata": {
            "mode": mode,
            "base_url": client.base_url,
            "git_revision": _git_output("rev-parse", "HEAD") or "unknown",
            "git_dirty": bool(_git_output("status", "--porcelain", "--untracked-files=no")),
            "host": _host_info(),
            "seed": seed,
            "content_marker": content_marker,
            "workload_sha256": corpus_gen.workload_digest(docs, cold_queries, warm_queries),
            "profiles": profiles,
            "search_modes": modes,
            "search_profile": search_profile,
            "limit": limit,
            "ingest_docs": ingest_docs,
            "words_per_doc": words_per_doc,
            "ingest_concurrency": ingest_concurrency,
            "cold_queries": cold_query_count,
            "warm_queries": warm_query_count,
            "warm_passes": warm_passes,
            "search_concurrency": search_concurrency,
            "cold_reset_strategy": cold_reset_strategy,
            "api_metrics_urls": api_urls,
            "worker_metrics_urls": worker_urls,
        },
    }

    target_corpus = corpus_id
    if not target_corpus:
        created = client.call(
            "POST",
            "/v0/corpora",
            {"name": f"Perf benchmark {content_marker}", "idempotency_key": f"perf-bench-{content_marker}"},
            expected=(200, 201),
        )
        target_corpus = created["corpus_id"]
    report["metadata"]["corpus_id"] = target_corpus

    if await_semantic_ready and any(m in ("semantic", "hybrid") for m in modes):
        _await_model_ready(client, content_marker, poll_interval=poll_interval)

    if "ingestion" in profiles:
        report["ingestion"] = run_ingestion_profile(
            client,
            target_corpus,
            docs,
            concurrency=ingest_concurrency,
            timeout_seconds=ingest_timeout,
            metrics_urls=all_metrics_urls,
            poll_interval=poll_interval,
        )

    if "cold_search" in profiles:
        if not cold_available:
            report["cold_search"] = {
                "available": False,
                "reason": cold_unavailable_reason
                or "cold state could not be guaranteed before cold_search; skipped to avoid mislabeling warm results",
            }
        else:
            if before_cold_hook is not None:
                before_cold_hook()
            cold_result = run_search_profile(
                client,
                target_corpus,
                cold_queries,
                modes=modes,
                search_profile=search_profile,
                limit=limit,
                concurrency=search_concurrency,
                passes=1,
                warmup_passes=0,
                metrics_urls=api_urls,
                distinct_per_mode=True,
            )
            cold_result["available"] = True
            cold_result["reset_strategy"] = cold_reset_strategy
            report["cold_search"] = cold_result

    if "warm_search" in profiles:
        warm_result = run_search_profile(
            client,
            target_corpus,
            warm_queries,
            modes=modes,
            search_profile=search_profile,
            limit=limit,
            concurrency=search_concurrency,
            passes=warm_passes,
            warmup_passes=1,
            metrics_urls=api_urls,
        )
        warm_result["available"] = True
        report["warm_search"] = warm_result

    if (
        report.get("cold_search")
        and report["cold_search"].get("available", True)
        and report.get("warm_search")
        and report["warm_search"].get("available", True)
    ):
        report["cold_vs_warm_delta"] = metrics.compute_search_comparison(
            report["cold_search"],
            report["warm_search"],
        )

    ing_ok = not report.get("ingestion") or report["ingestion"]["status"] == "complete"
    cold_section = report.get("cold_search")
    cold_ok = not cold_section or not cold_section.get("available", True) or cold_section["overall"]["failures"] == 0
    warm_section = report.get("warm_search")
    warm_ok = not warm_section or warm_section["overall"]["failures"] == 0
    report["status"] = "completed" if (ing_ok and cold_ok and warm_ok) else "degraded"
    report["duration_seconds"] = round(time.perf_counter() - started, 3)
    report["finished_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds")
    return report


def _parse_csv(raw: str, allowed: tuple[str, ...], label: str) -> list[str]:
    values = [item.strip() for item in raw.split(",") if item.strip()]
    if not values:
        raise ValueError(f"{label} must not be empty")
    invalid = [v for v in values if v not in allowed]
    if invalid:
        raise ValueError(f"unsupported {label}: {', '.join(invalid)} (allowed: {', '.join(allowed)})")
    return list(dict.fromkeys(values))


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("--mode", choices=("local", "remote"), default=None, help="target mode (defaults to remote when --base-url or QUIVR_BENCH_URL is set, else local)")
    parser.add_argument("--stack", default=None, help="reuse an existing local stack in .scratch/<NAME> instead of starting an isolated stack")
    parser.add_argument("--base-url", default=os.environ.get("QUIVR_BENCH_URL"), help="remote Quivr API base URL (or QUIVR_BENCH_URL)")
    parser.add_argument("--token", default=os.environ.get("QUIVR_BENCH_TOKEN"), help="remote Bearer API token (or QUIVR_BENCH_TOKEN)")
    parser.add_argument("--api-metrics-url", action="append", default=[], help="API /metrics probe URL (repeatable)")
    parser.add_argument("--worker-metrics-url", action="append", default=[], help="worker /metrics probe URL (repeatable)")
    parser.add_argument("--metrics-token", default=os.environ.get("QUIVR_BENCH_METRICS_TOKEN", ""), help="optional Bearer token for /metrics probes")
    parser.add_argument("--corpus-id", default=None, help="reuse an existing Corpus ID instead of creating a fresh benchmark Corpus")
    parser.add_argument("--profiles", default=",".join(ALL_PROFILES), help="comma-separated profiles: ingestion,cold_search,warm_search")
    parser.add_argument("--skip-ingestion", action="store_true", help="skip ingestion profile (useful with --corpus-id)")
    parser.add_argument("--modes", default=",".join(DEFAULT_MODES), help="comma-separated search modes: hybrid,lexical,semantic")
    parser.add_argument("--search-profile", default="default", help="retrieval profile name (e.g. default, deep)")
    parser.add_argument("--limit", type=int, default=10, help="search result limit (1..50)")
    parser.add_argument("--ingest-docs", type=int, default=50, help="number of synthetic documents to ingest")
    parser.add_argument("--words-per-doc", type=int, default=240, help="approximate words per synthetic document")
    parser.add_argument("--ingest-concurrency", type=int, default=8, help="concurrent POST /v0/records workers")
    parser.add_argument("--ingest-timeout", type=float, default=300.0, help="seconds to wait for ingestion searchable+enriched drain")
    parser.add_argument("--cold-queries", type=int, default=24, help="unique cold queries per mode")
    parser.add_argument("--warm-queries", type=int, default=16, help="unique warm working-set queries per mode")
    parser.add_argument("--warm-passes", type=int, default=3, help="measured warm passes after 1 warmup pass")
    parser.add_argument("--search-concurrency", type=int, default=1, help="concurrent search workers")
    parser.add_argument("--request-timeout", type=float, default=30.0, help="per-request HTTP timeout in seconds")
    parser.add_argument("--no-restart-cold", action="store_true", help="do not restart local API process before cold_search")
    parser.add_argument("--cold-reset-cmd", default=None, help="shell command run before cold_search to reset/flush remote target caches")
    parser.add_argument("--assume-cold", action="store_true", help="acknowledge remote target caches were reset externally before cold_search")
    parser.add_argument("--seed", type=int, default=610, help="deterministic PRNG seed")
    parser.add_argument("--content-marker", default=None, help="override per-run synthetic content marker")
    parser.add_argument("--baseline", type=pathlib.Path, default=None, help="previous bench.json to emit baseline-vs-candidate delta tables")
    parser.add_argument("--compare", nargs=2, metavar=("BASELINE_JSON", "CANDIDATE_JSON"), type=pathlib.Path, default=None, help="offline comparison of two bench.json files")
    parser.add_argument("--out", type=pathlib.Path, default=None, help="output directory (default: .scratch/perf-bench/<UTC>)")
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)

    if args.compare:
        baseline = json.loads(args.compare[0].read_text())
        candidate = json.loads(args.compare[1].read_text())
        delta_json = metrics.compute_report_delta(baseline, candidate)
        delta_md = metrics.render_delta_markdown(baseline, candidate)
        if args.out:
            args.out.mkdir(parents=True, exist_ok=True)
            (args.out / "delta.json").write_text(json.dumps(delta_json, indent=2) + "\n")
            (args.out / "delta.md").write_text(delta_md)
            print(f"Delta artifacts: {args.out}", flush=True)
        else:
            print(delta_md, end="")
        return 0

    try:
        profiles = _parse_csv(args.profiles, ALL_PROFILES, "profiles")
        if args.skip_ingestion:
            profiles = [p for p in profiles if p != "ingestion"]
        if not profiles:
            raise ValueError("at least one profile must be selected")
        modes = _parse_csv(args.modes, DEFAULT_MODES, "modes")
    except ValueError as error:
        parser.error(str(error))

    target_mode = args.mode or ("remote" if args.base_url else "local")
    if target_mode == "remote":
        if not args.base_url or not args.token:
            parser.error("remote mode requires --base-url (or QUIVR_BENCH_URL) and --token (or QUIVR_BENCH_TOKEN)")

    baseline_data = json.loads(args.baseline.read_text()) if args.baseline else None
    out_dir = args.out or (
        ROOT / ".scratch" / "perf-bench" / datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    )
    out_dir.mkdir(parents=True, exist_ok=True)
    content_marker = args.content_marker or uuid.uuid4().hex[:8]

    stack = None
    owns_stack = False
    previous_sigterm = signal.getsignal(signal.SIGTERM)
    report: dict = {"schema_version": metrics.SCHEMA_VERSION, "status": "failed", "metadata": {"mode": target_mode}}
    try:
        def _interrupted(*_):
            raise KeyboardInterrupt()

        signal.signal(signal.SIGTERM, _interrupted)

        cold_available = True
        cold_unavailable_reason = None
        cold_reset_strategy = "none"

        if target_mode == "local":
            stack, owns_stack = _start_local_stack(args.stack)
            base_url = f"http://127.0.0.1:{stack.state['api_port']}"
            token = stack.state["admin"]
            api_metrics_urls = args.api_metrics_url or [f"http://127.0.0.1:{stack.state['probe_port']}/metrics"]
            worker_metrics_urls = args.worker_metrics_url or [f"http://127.0.0.1:{stack.state['worker_probe_port']}/metrics"]
            if args.cold_reset_cmd:
                before_cold_hook = lambda: subprocess.run(args.cold_reset_cmd, shell=True, check=True)
                cold_reset_strategy = "command"
            elif args.no_restart_cold:
                before_cold_hook = None
                cold_reset_strategy = "disabled_by_flag"
            else:
                before_cold_hook = lambda: _restart_local_api(stack)
                cold_reset_strategy = "local_api_restart"
        else:
            base_url = args.base_url
            token = args.token
            api_metrics_urls = list(args.api_metrics_url)
            worker_metrics_urls = list(args.worker_metrics_url)
            if args.cold_reset_cmd:
                before_cold_hook = lambda: subprocess.run(args.cold_reset_cmd, shell=True, check=True)
                cold_reset_strategy = "command"
            elif args.assume_cold:
                before_cold_hook = None
                cold_reset_strategy = "caller_acknowledged"
            else:
                before_cold_hook = None
                cold_available = False
                cold_reset_strategy = "unavailable"
                cold_unavailable_reason = (
                    "remote mode does not reset target caches automatically; pass "
                    "--cold-reset-cmd <cmd> or --assume-cold to measure cold_search"
                )

        client = BenchClient(base_url, token, timeout=args.request_timeout, metrics_token=args.metrics_token)
        report = run_benchmark(
            client,
            mode=target_mode,
            profiles=profiles,
            modes=modes,
            search_profile=args.search_profile,
            limit=args.limit,
            ingest_docs=args.ingest_docs,
            words_per_doc=args.words_per_doc,
            ingest_concurrency=args.ingest_concurrency,
            ingest_timeout=args.ingest_timeout,
            cold_query_count=args.cold_queries,
            warm_query_count=args.warm_queries,
            warm_passes=args.warm_passes,
            search_concurrency=args.search_concurrency,
            seed=args.seed,
            content_marker=content_marker,
            corpus_id=args.corpus_id,
            api_metrics_urls=api_metrics_urls,
            worker_metrics_urls=worker_metrics_urls,
            before_cold_hook=before_cold_hook,
            cold_available=cold_available,
            cold_unavailable_reason=cold_unavailable_reason,
            cold_reset_strategy=cold_reset_strategy,
            await_semantic_ready=(target_mode == "local" and owns_stack),
        )
    except BaseException as error:
        report["error"] = f"{type(error).__name__}: {str(error)[:500]}"
        report.setdefault("finished_at", datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"))
        raise
    finally:
        signal.signal(signal.SIGTERM, previous_sigterm)
        (out_dir / "bench.json").write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n")
        (out_dir / "bench.md").write_text(metrics.render_markdown(report, baseline=baseline_data))
        if baseline_data is not None and report.get("status") != "failed":
            (out_dir / "delta.json").write_text(
                json.dumps(metrics.compute_report_delta(baseline_data, report), indent=2) + "\n"
            )
            (out_dir / "delta.md").write_text(metrics.render_delta_markdown(baseline_data, report))
        print(f"Benchmark artifacts: {out_dir}", flush=True)
        if stack is not None and owns_stack:
            try:
                stack.capture()
            finally:
                stack.down(True)

    return 0 if report.get("status") == "completed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
