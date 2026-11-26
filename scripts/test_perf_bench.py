"""Offline owner unit tests for the reproducible perf benchmark harness.

Zero Docker, zero network outside loopback, zero wall-clock sleeps.
"""

from __future__ import annotations

import http.server
import json
import os
import pathlib
import signal
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest import mock
from urllib.parse import parse_qs, urlsplit

import perf_bench
import perf_bench_corpus as corpus_gen
import perf_bench_metrics as metrics


class StartupContract(unittest.TestCase):
    def test_help_starts_from_outside_repository_without_pythonpath(self):
        script = pathlib.Path(__file__).resolve().with_name("perf_bench.py")
        with tempfile.TemporaryDirectory() as folder:
            result = subprocess.run(
                [sys.executable, str(script), "--help"],
                cwd=folder,
                env={**os.environ, "PYTHONPATH": ""},
                capture_output=True,
                text=True,
            )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("--baseline", result.stdout)
        self.assertIn("--compare", result.stdout)
        self.assertIn("--api-metrics-url", result.stdout)


class CorpusGeneratorContract(unittest.TestCase):
    def test_deterministic_documents_and_strictly_unique_cold_queries(self):
        docs_a = corpus_gen.generate_documents(12, 80, seed=610, content_marker="m1")
        docs_b = corpus_gen.generate_documents(12, 80, seed=610, content_marker="m1")
        self.assertEqual(docs_a, docs_b)
        self.assertEqual(len({d["record_key"] for d in docs_a}), 12)
        for doc in docs_a:
            self.assertEqual([p["key"] for p in doc["parts"]], ["title", "body"])
            self.assertGreaterEqual(len(doc["body"].split()), 70)

        cold = corpus_gen.generate_cold_queries(40, seed=610)
        self.assertEqual(len(cold), 40)
        self.assertEqual(len({q["query"] for q in cold}), 40)

        warm = corpus_gen.generate_warm_queries(16, seed=610)
        self.assertEqual(len(warm), 16)
        self.assertEqual(len({q["query"] for q in warm}), 16)
        self.assertEqual(
            corpus_gen.workload_digest(docs_a, cold, warm),
            corpus_gen.workload_digest(docs_b, cold, warm),
        )


class MetricsAndDeltaContract(unittest.TestCase):
    def test_nearest_rank_percentiles_and_failure_exclusion(self):
        values = [float(i) for i in range(1, 101)]
        dist = metrics.distribution(values)
        self.assertEqual(dist["p50_ms"], 50.0)
        self.assertEqual(dist["p95_ms"], 95.0)
        self.assertEqual(dist["p99_ms"], 99.0)
        self.assertEqual(dist["max_ms"], 100.0)
        self.assertIsNone(metrics.distribution([])["p99_ms"])

        samples = [
            {
                "ok": True,
                "wall_ms": 20.0,
                "elapsed_ms": 18.0,
                "phases": {
                    "routing": 1.0,
                    "coverage": 2.0,
                    "query_encoding": 5.0,
                    "index_query": 6.0,
                    "hydration": 3.0,
                    "plugin_rounds": 1.0,
                },
                "rounds": 2,
                "hits": 5,
            },
            {
                "ok": True,
                "wall_ms": 40.0,
                "elapsed_ms": 36.0,
                "phases": {
                    "routing": 2.0,
                    "coverage": 3.0,
                    "query_encoding": 10.0,
                    "index_query": 12.0,
                    "hydration": 7.0,
                    "plugin_rounds": 2.0,
                },
                "rounds": 2,
                "hits": 7,
            },
            {"ok": False, "wall_ms": 9999.0, "error": "TimeoutError: deadline"},
        ]
        summary = metrics.summarize_search_samples(samples)
        self.assertEqual(summary["samples"], 3)
        self.assertEqual(summary["succeeded"], 2)
        self.assertEqual(summary["failures"], 1)
        self.assertEqual(summary["wall"]["p99_ms"], 40.0)
        self.assertEqual(summary["phases"]["hydration"]["p50_ms"], 3.0)
        self.assertEqual(summary["phases"]["hydration"]["p99_ms"], 7.0)

    def test_prometheus_counter_parsing_merging_and_reset_safety(self):
        api_before = """
# HELP quivr_dependency_calls_total Dependency calls.
# TYPE quivr_dependency_calls_total counter
quivr_dependency_calls_total{dependency="weaviate",protocol="http",operation="query"} 10
quivr_dependency_calls_total{operation="query",protocol="grpc",dependency="weaviate"} 2
quivr_dependency_calls_total{dependency="s3",protocol="http",operation="get_object"} 5
quivr_dependency_calls_total{dependency="postgres",protocol="wire",operation="query"} 20
"""
        api_after = """
quivr_dependency_calls_total{dependency="weaviate",protocol="http",operation="query"} 24
quivr_dependency_calls_total{dependency="weaviate",protocol="grpc",operation="query"} 6
quivr_dependency_calls_total{dependency="s3",protocol="http",operation="get_object"} 17
quivr_dependency_calls_total{dependency="s3",protocol="http",operation="put_object"} 4
quivr_dependency_calls_total{dependency="postgres",protocol="wire",operation="query"} 32
quivr_dependency_calls_total{dependency="postgres",protocol="wire",operation="batch"} 3
quivr_dependency_calls_total{dependency="postgres",protocol="wire",operation="batch_query"} 9
"""
        worker_delta = {
            ("weaviate", "http", "write"): 8,
            ("s3", "http", "put_object"): 6,
            ("postgres", "wire", "query"): 10,
        }
        before = metrics.parse_dependency_counters(api_before)
        after = metrics.merge_counter_maps([metrics.parse_dependency_counters(api_after), worker_delta])
        delta, reset = metrics.counter_delta(before, after)
        self.assertFalse(reset)
        summary = metrics.summarize_dependency_calls(delta, unit_count=10, unit_name="doc")
        self.assertEqual(summary["weaviate"]["http"]["query"], 14)
        self.assertEqual(summary["weaviate"]["http"]["write"], 8)
        self.assertEqual(summary["weaviate"]["grpc"]["query"], 4)
        self.assertEqual(summary["weaviate"]["total_rtts"], 26)
        self.assertEqual(summary["weaviate"]["rtts_per_doc"], 2.6)
        self.assertEqual(summary["s3"]["get_object"], 12)
        self.assertEqual(summary["s3"]["put_object"], 10)
        self.assertEqual(summary["postgres"]["total_rtts"], 25)  # (12 + 10) queries + 3 batches
        self.assertEqual(summary["postgres"]["total_statements"], 31)  # 22 queries + 9 batch_queries

        # Counter reset (e.g. process restarted mid-window) stays non-negative and flags reset.
        reset_delta, reset_flag = metrics.counter_delta(
            {("s3", "http", "get_object"): 50},
            {("s3", "http", "get_object"): 7},
        )
        self.assertTrue(reset_flag)
        self.assertEqual(reset_delta[("s3", "http", "get_object")], 7)

    def test_per_endpoint_counter_delta_preserves_single_process_reset(self):
        # API counter grows by +10 while worker counter restarts from 50 -> 3.
        # Merging before differencing would see 60 -> 23 and report bogus delta 23.
        # Per-endpoint deltas compute API (20 - 10 = 10) + worker reset (3) = 13.
        key = ("postgres", "wire", "query")
        before = {
            "http://api/metrics": {key: 10},
            "http://worker/metrics": {key: 50},
        }
        after = {
            "http://api/metrics": {key: 20},
            "http://worker/metrics": {key: 3},
        }
        delta, reset = metrics.endpoint_counter_delta(before, after)
        self.assertTrue(reset)
        self.assertEqual(delta[key], 13)


class _FakeQuivrCluster:
    """In-process loopback Quivr API + API/worker /metrics probes with deterministic phases."""

    def __init__(self, phase_scale: float = 1.0):
        self.lock = threading.Lock()
        self.phase_scale = phase_scale
        self.records: list[str] = []
        self.seen_queries: set[str] = set()
        self.cold_query_texts: list[str] = []
        self.api_counts: dict[tuple[str, str, str], int] = {
            ("weaviate", "http", "query"): 0,
            ("weaviate", "grpc", "query"): 0,
            ("s3", "http", "get_object"): 0,
            ("s3", "http", "put_object"): 0,
            ("postgres", "wire", "query"): 0,
            ("postgres", "wire", "batch"): 0,
            ("postgres", "wire", "batch_query"): 0,
        }
        self.worker_counts: dict[tuple[str, str, str], int] = {
            ("weaviate", "http", "write"): 0,
            ("s3", "http", "get_object"): 0,
            ("s3", "http", "put_object"): 0,
            ("postgres", "wire", "query"): 0,
        }
        cluster = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def _send_json(self, status: int, payload: dict):
                raw = json.dumps(payload).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def _send_text(self, text: str):
                raw = text.encode()
                self.send_response(200)
                self.send_header("Content-Type", "text/plain; version=0.0.4")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def do_GET(self):
                parsed = urlsplit(self.path)
                if parsed.path == "/api-metrics":
                    with cluster.lock:
                        body = "\n".join(
                            f'quivr_dependency_calls_total{{dependency="{d}",protocol="{p}",operation="{o}"}} {v}'
                            for (d, p, o), v in sorted(cluster.api_counts.items())
                        )
                    self._send_text(body + "\n")
                    return
                if parsed.path == "/worker-metrics":
                    with cluster.lock:
                        body = "\n".join(
                            f'quivr_dependency_calls_total{{dependency="{d}",protocol="{p}",operation="{o}"}} {v}'
                            for (d, p, o), v in sorted(cluster.worker_counts.items())
                        )
                    self._send_text(body + "\n")
                    return
                if parsed.path == "/v0/changes":
                    query = parse_qs(parsed.query)
                    if "cursor" not in query:
                        self._send_json(200, {"items": [], "next_cursor": "0", "has_more": False})
                        return
                    cursor = int(query["cursor"][0])
                    with cluster.lock:
                        events = []
                        for rec_id in cluster.records[cursor:]:
                            events.append({"type": "record.retrieval_ready", "resource": {"id": rec_id}})
                            events.append({"type": "record.enrichment_available", "resource": {"id": rec_id}})
                        next_cursor = str(len(cluster.records))
                    self._send_json(200, {"items": events, "next_cursor": next_cursor, "has_more": False})
                    return
                self._send_json(404, {"code": "not_found"})

            def do_POST(self):
                length = int(self.headers.get("Content-Length", "0"))
                body = json.loads(self.rfile.read(length)) if length else {}
                if self.path == "/v0/corpora":
                    self._send_json(201, {"corpus_id": "corpus-bench-1"})
                    return
                if self.path == "/v0/records":
                    with cluster.lock:
                        rec_id = f"rec-{len(cluster.records) + 1}"
                        cluster.records.append(rec_id)
                        cluster.api_counts[("s3", "http", "put_object")] += 1
                        cluster.api_counts[("s3", "http", "get_object")] += 1
                        cluster.api_counts[("postgres", "wire", "query")] += 2
                        cluster.worker_counts[("weaviate", "http", "write")] += 4
                        cluster.worker_counts[("s3", "http", "put_object")] += 1
                        cluster.worker_counts[("postgres", "wire", "query")] += 3
                    self._send_json(202, {"receipt_id": f"rcpt-{rec_id}"})
                    return
                if self.path == "/v0/search":
                    q_text = body.get("query", "")
                    with cluster.lock:
                        warm = q_text in cluster.seen_queries
                        cluster.seen_queries.add(q_text)
                        if not warm:
                            cluster.cold_query_texts.append(q_text)
                        s = cluster.phase_scale
                        cluster.api_counts[("weaviate", "http", "query")] += 1
                        cluster.api_counts[("s3", "http", "get_object")] += 0 if warm else 3
                        cluster.api_counts[("postgres", "wire", "query")] += 2
                    enc = (2.0 if warm else 40.0) * s
                    hyd = (4.0 if warm else 30.0) * s
                    idx = 18.0 * s
                    phases = {
                        "routing_ms": int(2 * s),
                        "coverage_ms": int(3 * s),
                        "query_encoding_ms": int(enc),
                        "index_query_ms": int(idx),
                        "hydration_ms": int(hyd),
                        "plugin_rounds_ms": int(5 * s),
                    }
                    self._send_json(
                        200,
                        {
                            "items": [{"record_id": "rec-1"}],
                            "usage": {
                                "rounds": 2,
                                "elapsed_ms": sum(phases.values()),
                                "phases": phases,
                            },
                        },
                    )
                    return
                self._send_json(404, {"code": "not_found"})

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        host, port = self.server.server_address
        self.base_url = f"http://{host}:{port}"

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)


class EndToEndHarnessContract(unittest.TestCase):
    def test_single_command_emits_json_markdown_and_delta_tables(self):
        baseline_cluster = _FakeQuivrCluster(phase_scale=1.0)
        candidate_cluster = _FakeQuivrCluster(phase_scale=0.5)
        self.addCleanup(baseline_cluster.close)
        self.addCleanup(candidate_cluster.close)

        with tempfile.TemporaryDirectory() as tmp:
            base_dir = pathlib.Path(tmp) / "baseline"
            cand_dir = pathlib.Path(tmp) / "candidate"
            cmp_dir = pathlib.Path(tmp) / "compared"

            rc = perf_bench.main(
                [
                    "--mode", "remote",
                    "--base-url", baseline_cluster.base_url,
                    "--token", "bench-token",
                    "--api-metrics-url", baseline_cluster.base_url + "/api-metrics",
                    "--worker-metrics-url", baseline_cluster.base_url + "/worker-metrics",
                    "--assume-cold",
                    "--ingest-docs", "6",
                    "--words-per-doc", "32",
                    "--ingest-concurrency", "2",
                    "--cold-queries", "4",
                    "--warm-queries", "4",
                    "--warm-passes", "2",
                    "--modes", "hybrid,lexical",
                    "--seed", "610",
                    "--content-marker", "run1",
                    "--out", str(base_dir),
                ]
            )
            self.assertEqual(rc, 0)

            base_report = json.loads((base_dir / "bench.json").read_text())
            base_md = (base_dir / "bench.md").read_text()
            self.assertEqual(base_report["status"], "completed")
            self.assertTrue(base_report["cold_search"]["available"])
            self.assertEqual(base_report["ingestion"]["accepted_count"], 6)
            self.assertEqual(base_report["ingestion"]["searchable_count"], 6)
            self.assertEqual(base_report["ingestion"]["enriched_count"], 6)
            self.assertEqual(base_report["ingestion"]["dependency_calls"]["weaviate"]["http"]["write"], 24)
            self.assertEqual(base_report["ingestion"]["dependency_calls"]["s3"]["put_object"], 12)
            self.assertEqual(base_report["ingestion"]["dependency_calls"]["s3"]["get_object"], 6)
            self.assertEqual(base_report["ingestion"]["dependency_calls"]["postgres"]["query"], 30)

            # Cold search hit uncached S3 hydration (3 GETs/search * 8 searches = 24);
            # each (mode, query) in cold_search used a distinct query text with zero cross-mode reuse.
            self.assertEqual(base_report["cold_search"]["dependency_calls"]["s3"]["get_object"], 24)
            self.assertEqual(base_report["warm_search"]["dependency_calls"]["s3"]["get_object"], 0)
            self.assertEqual(len(set(baseline_cluster.cold_query_texts[:8])), 8)
            for phase in metrics.PHASES:
                self.assertIsNotNone(base_report["cold_search"]["overall"]["phases"][phase]["p99_ms"])
                self.assertIsNotNone(base_report["warm_search"]["overall"]["phases"][phase]["p99_ms"])
            self.assertIn("## Cold vs Warm Search Delta", base_md)
            self.assertIn("## Dependency Call Counts", base_md)

            # Second run with --baseline produces delta.json + delta.md and embeds delta table in bench.md.
            rc2 = perf_bench.main(
                [
                    "--mode", "remote",
                    "--base-url", candidate_cluster.base_url,
                    "--token", "bench-token",
                    "--api-metrics-url", candidate_cluster.base_url + "/api-metrics",
                    "--worker-metrics-url", candidate_cluster.base_url + "/worker-metrics",
                    "--assume-cold",
                    "--ingest-docs", "6",
                    "--words-per-doc", "32",
                    "--cold-queries", "4",
                    "--warm-queries", "4",
                    "--warm-passes", "2",
                    "--modes", "hybrid,lexical",
                    "--seed", "610",
                    "--content-marker", "run2",
                    "--baseline", str(base_dir / "bench.json"),
                    "--out", str(cand_dir),
                ]
            )
            self.assertEqual(rc2, 0)
            delta_json = json.loads((cand_dir / "delta.json").read_text())
            delta_md = (cand_dir / "delta.md").read_text()
            cand_md = (cand_dir / "bench.md").read_text()
            self.assertLess(delta_json["cold_search"]["overall"]["phases"]["hydration"]["p95_ms"]["delta"], 0)
            self.assertIn("Benchmark Delta (Baseline vs Candidate)", delta_md)
            self.assertIn("(better)", delta_md)
            self.assertIn("## Benchmark Delta (Baseline vs Candidate)", cand_md)
            self.assertIn("(better)", cand_md)

            # Offline --compare command reproduces delta artifacts without touching the network.
            rc3 = perf_bench.main(["--compare", str(base_dir / "bench.json"), str(cand_dir / "bench.json"), "--out", str(cmp_dir)])
            self.assertEqual(rc3, 0)
            self.assertEqual((cmp_dir / "delta.md").read_text(), delta_md)

    def test_remote_mode_marks_cold_unavailable_without_reset_or_ack(self):
        cluster = _FakeQuivrCluster(phase_scale=1.0)
        self.addCleanup(cluster.close)
        with tempfile.TemporaryDirectory() as tmp:
            out_dir = pathlib.Path(tmp) / "remote-default"
            rc = perf_bench.main(
                [
                    "--mode", "remote",
                    "--base-url", cluster.base_url,
                    "--token", "bench-token",
                    "--ingest-docs", "2",
                    "--words-per-doc", "24",
                    "--cold-queries", "2",
                    "--warm-queries", "2",
                    "--warm-passes", "1",
                    "--modes", "hybrid",
                    "--out", str(out_dir),
                ]
            )
            self.assertEqual(rc, 0)
            report = json.loads((out_dir / "bench.json").read_text())
            md = (out_dir / "bench.md").read_text()
            self.assertEqual(report["status"], "completed")
            self.assertFalse(report["cold_search"]["available"])
            self.assertIn("--cold-reset-cmd", report["cold_search"]["reason"])
            self.assertIn("_Unavailable:", md)
            self.assertTrue(report["warm_search"]["available"])
            self.assertFalse(report["ingestion"]["dependency_calls"]["available"])
            self.assertIsNotNone(report["warm_search"]["overall"]["phases"]["index_query"]["p95_ms"])

    def test_remote_cold_reset_cmd_runs_before_cold_search(self):
        cluster = _FakeQuivrCluster(phase_scale=1.0)
        self.addCleanup(cluster.close)
        with tempfile.TemporaryDirectory() as tmp:
            marker_file = pathlib.Path(tmp) / "reset.marker"
            out_dir = pathlib.Path(tmp) / "remote-reset"
            rc = perf_bench.main(
                [
                    "--mode", "remote",
                    "--base-url", cluster.base_url,
                    "--token", "bench-token",
                    "--cold-reset-cmd", f"{sys.executable} -c \"import pathlib; pathlib.Path({str(marker_file)!r}).write_text('ok')\"",
                    "--ingest-docs", "2",
                    "--words-per-doc", "24",
                    "--cold-queries", "2",
                    "--warm-queries", "2",
                    "--warm-passes", "1",
                    "--modes", "hybrid",
                    "--out", str(out_dir),
                ]
            )
            self.assertEqual(rc, 0)
            self.assertEqual(marker_file.read_text(), "ok")
            report = json.loads((out_dir / "bench.json").read_text())
            self.assertTrue(report["cold_search"]["available"])
            self.assertEqual(report["cold_search"]["reset_strategy"], "command")


class LocalSafetyAndReadinessContract(unittest.TestCase):
    def test_restart_local_api_uses_signal_owned_and_never_raw_kill(self):
        events: list[tuple] = []

        class FakeStack:
            def __init__(self):
                self.state = {"api_pid": 4242, "pids": [4242, 9999]}
                self.owned_checks = 0

            def signal_owned(self, pid, sig):
                events.append(("signal_owned", pid, sig))

            def owns_process(self, pid):
                self.owned_checks += 1
                return False

            def save(self):
                events.append(("save", list(self.state["pids"])))

            def spawn(self, command, config):
                events.append(("spawn", command, config))

            def await_ready(self, key, timeout=30):
                events.append(("await_ready", key, timeout))

        stack = FakeStack()
        with mock.patch("os.kill", side_effect=AssertionError("must use stack.signal_owned")):
            perf_bench._restart_local_api(stack)

        self.assertEqual(
            events,
            [
                ("signal_owned", 4242, signal.SIGTERM),
                ("save", [9999]),
                ("spawn", "api", "config.json"),
                ("await_ready", "probe_port", 30),
            ],
        )

    def test_await_model_ready_requires_enriched_record_and_non_empty_semantic_hit(self):
        calls: list[tuple[str, str]] = []
        search_attempts = [0]

        class FakeClient:
            def call(self, method, path, body=None, expected=200):
                calls.append((method, path))
                if path == "/v0/corpora":
                    return {"corpus_id": "probe-corpus-1"}
                if path == "/v0/records":
                    return {"receipt_id": "probe-receipt-1"}
                if path.startswith("/v0/changes") and "cursor=" not in path:
                    return {"items": [], "next_cursor": "0", "has_more": False}
                if path.startswith("/v0/changes") and "cursor=0" in path:
                    return {
                        "items": [{"type": "record.enrichment_available", "resource": {"id": "probe-rec-1"}}],
                        "next_cursor": "1",
                        "has_more": False,
                    }
                raise AssertionError(f"unexpected call: {method} {path}")

            def timed_search(self, body):
                self.last_search = body
                search_attempts[0] += 1
                # First attempt returns 0 hits (empty/unindexed); readiness must keep waiting.
                if search_attempts[0] == 1:
                    return {"ok": True, "hits": 0}
                return {"ok": True, "hits": 1}

        client = FakeClient()
        perf_bench._await_model_ready(client, "m1", deadline_seconds=5.0, poll_interval=0.0)
        self.assertEqual(search_attempts[0], 2)
        self.assertEqual(client.last_search["corpus_ids"], ["probe-corpus-1"])
        self.assertEqual(client.last_search["mode"], "semantic")
        self.assertIn(("POST", "/v0/records"), calls)


if __name__ == "__main__":
    unittest.main()
