"""Pure arithmetic, Prometheus counter delta parsing, and Markdown delta rendering for perf_bench.

Stdlib-only. Keeps all statistical and formatting logic offline-testable without
Docker, live stacks or wall-clock sleeps.
"""

from __future__ import annotations

import collections
import math
import re

SCHEMA_VERSION = 1

PHASES: tuple[str, ...] = (
    "routing",
    "coverage",
    "query_encoding",
    "index_query",
    "hydration",
    "plugin_rounds",
)

PHASE_RESPONSE_KEYS: dict[str, str] = {phase: f"{phase}_ms" for phase in PHASES}

WEAVIATE_OPS: tuple[str, ...] = ("query", "write", "read", "delete", "schema", "ready", "other")
S3_OPS: tuple[str, ...] = ("get_object", "put_object", "head_object", "bucket")
POSTGRES_OPS: tuple[str, ...] = ("query", "batch", "batch_query")

_METRIC_LINE_RE = re.compile(
    r'^quivr_dependency_calls_total\{([^}]*)\}\s+([+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?)\s*$'
)
_LABEL_PAIR_RE = re.compile(r'([a-zA-Z_][a-zA-Z0-9_]*)="((?:\\.|[^"\\])*)"')


def _round(value: float | None, digits: int = 2) -> float | None:
    return None if value is None else round(value, digits)


def percentile(values: list[float], p: float) -> float | None:
    """Nearest-rank percentile over non-empty values; None when empty."""
    if not values:
        return None
    ordered = sorted(values)
    index = max(1, math.ceil(p / 100.0 * len(ordered))) - 1
    return ordered[min(index, len(ordered) - 1)]


def distribution(values: list[float]) -> dict:
    """Summarize latency values in ms with p50/p95/p99, mean and max."""
    if not values:
        return {
            "count": 0,
            "p50_ms": None,
            "p95_ms": None,
            "p99_ms": None,
            "mean_ms": None,
            "max_ms": None,
        }
    return {
        "count": len(values),
        "p50_ms": _round(percentile(values, 50)),
        "p95_ms": _round(percentile(values, 95)),
        "p99_ms": _round(percentile(values, 99)),
        "mean_ms": _round(sum(values) / len(values)),
        "max_ms": _round(max(values)),
    }


def extract_search_phases(body: dict | None) -> tuple[float | None, dict[str, float]]:
    """Extract engine elapsed_ms and per-phase ms from a POST /v0/search payload."""
    if not isinstance(body, dict):
        return None, {}
    usage = body.get("usage")
    if not isinstance(usage, dict):
        return None, {}
    elapsed = usage.get("elapsed_ms")
    elapsed_val = float(elapsed) if isinstance(elapsed, (int, float)) and not isinstance(elapsed, bool) else None
    raw_phases = usage.get("phases")
    phases: dict[str, float] = {}
    if isinstance(raw_phases, dict):
        for phase, key in PHASE_RESPONSE_KEYS.items():
            raw = raw_phases.get(key)
            if isinstance(raw, (int, float)) and not isinstance(raw, bool):
                phases[phase] = float(raw)
    return elapsed_val, phases


def summarize_search_samples(samples: list[dict]) -> dict:
    """Aggregate client wall-clock, engine elapsed, and per-phase latency distributions.

    Failures are preserved and counted, never dropped silently, and excluded from
    successful latency percentiles.
    """
    ok_samples = [s for s in samples if s.get("ok")]
    failures = len(samples) - len(ok_samples)
    error_counts = dict(collections.Counter(s.get("error") or "unknown" for s in samples if not s.get("ok")))

    wall_values = [float(s["wall_ms"]) for s in ok_samples if s.get("wall_ms") is not None]
    elapsed_values = [float(s["elapsed_ms"]) for s in ok_samples if s.get("elapsed_ms") is not None]
    phase_values: dict[str, list[float]] = {phase: [] for phase in PHASES}
    rounds: list[int] = []
    hits: list[int] = []
    for sample in ok_samples:
        sample_phases = sample.get("phases") or {}
        for phase in PHASES:
            if phase in sample_phases and sample_phases[phase] is not None:
                phase_values[phase].append(float(sample_phases[phase]))
        if isinstance(sample.get("rounds"), int):
            rounds.append(sample["rounds"])
        if isinstance(sample.get("hits"), int):
            hits.append(sample["hits"])

    return {
        "samples": len(samples),
        "succeeded": len(ok_samples),
        "failures": failures,
        "error_rate": _round(failures / len(samples), 6) if samples else None,
        "errors": error_counts,
        "wall": distribution(wall_values),
        "elapsed": distribution(elapsed_values),
        "phases": {phase: distribution(phase_values[phase]) for phase in PHASES},
        "mean_rounds": _round(sum(rounds) / len(rounds), 2) if rounds else None,
        "mean_hits": _round(sum(hits) / len(hits), 2) if hits else None,
    }


def _unescape_label(value: str) -> str:
    return value.replace(r"\\", "\\").replace(r"\"", '"').replace(r"\n", "\n")


def parse_dependency_counters(prometheus_text: str) -> dict[tuple[str, str, str], int]:
    """Parse quivr_dependency_calls_total samples from Prometheus text exposition."""
    counts: dict[tuple[str, str, str], int] = {}
    for line in prometheus_text.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        match = _METRIC_LINE_RE.match(line)
        if not match:
            continue
        labels = {k: _unescape_label(v) for k, v in _LABEL_PAIR_RE.findall(match.group(1))}
        dep = labels.get("dependency")
        proto = labels.get("protocol")
        op = labels.get("operation")
        if not (dep and proto and op):
            continue
        value = int(round(float(match.group(2))))
        key = (dep, proto, op)
        counts[key] = counts.get(key, 0) + value
    return counts


def merge_counter_maps(maps: list[dict[tuple[str, str, str], int]]) -> dict[tuple[str, str, str], int]:
    """Sum dependency counter maps across multiple processes (e.g., API + worker replicas)."""
    merged: dict[tuple[str, str, str], int] = {}
    for item in maps:
        for key, value in item.items():
            merged[key] = merged.get(key, 0) + value
    return merged


def counter_delta(
    before: dict[tuple[str, str, str], int],
    after: dict[tuple[str, str, str], int],
) -> tuple[dict[tuple[str, str, str], int], bool]:
    """Compute non-negative counter deltas; reports whether a process counter reset occurred."""
    delta: dict[tuple[str, str, str], int] = {}
    reset_detected = False
    for key in set(before) | set(after):
        start = before.get(key, 0)
        end = after.get(key, 0)
        if end < start:
            reset_detected = True
            delta[key] = max(0, end)
        else:
            delta[key] = end - start
    return delta, reset_detected


def endpoint_counter_delta(
    before_by_url: dict[str, dict[tuple[str, str, str], int]],
    after_by_url: dict[str, dict[tuple[str, str, str], int]],
) -> tuple[dict[tuple[str, str, str], int], bool]:
    """Compute reset-aware counter deltas per endpoint URL before summing across processes."""
    per_endpoint: list[dict[tuple[str, str, str], int]] = []
    any_reset = False
    for url in sorted(set(before_by_url) | set(after_by_url)):
        delta, reset = counter_delta(before_by_url.get(url, {}), after_by_url.get(url, {}))
        per_endpoint.append(delta)
        any_reset = any_reset or reset
    return merge_counter_maps(per_endpoint), any_reset


def summarize_dependency_calls(
    delta: dict[tuple[str, str, str], int] | None,
    unit_count: int = 0,
    unit_name: str = "unit",
    available: bool = True,
    reset_detected: bool = False,
    errors: list[str] | None = None,
) -> dict:
    """Build structured dependency call totals and per-unit ratios for Weaviate, S3 and Postgres."""
    if not available or delta is None:
        return {
            "available": False,
            "reset_detected": reset_detected,
            "errors": list(errors or []),
            "unit_name": unit_name,
            "unit_count": unit_count,
            "weaviate": None,
            "s3": None,
            "postgres": None,
        }

    def per_unit(total: int) -> float | None:
        return _round(total / unit_count, 3) if unit_count > 0 else None

    weaviate_http = {op: delta.get(("weaviate", "http", op), 0) for op in WEAVIATE_OPS}
    weaviate_http["total"] = sum(weaviate_http.values())
    weaviate_grpc = {op: delta.get(("weaviate", "grpc", op), 0) for op in WEAVIATE_OPS}
    weaviate_grpc["total"] = sum(weaviate_grpc.values())
    weaviate_total = weaviate_http["total"] + weaviate_grpc["total"]

    s3_counts = {op: delta.get(("s3", "http", op), 0) for op in S3_OPS}
    s3_total = sum(s3_counts.values())

    pg_counts = {op: delta.get(("postgres", "wire", op), 0) for op in POSTGRES_OPS}
    pg_rtts = pg_counts["query"] + pg_counts["batch"]
    pg_statements = pg_counts["query"] + pg_counts["batch_query"]

    return {
        "available": True,
        "reset_detected": reset_detected,
        "errors": list(errors or []),
        "unit_name": unit_name,
        "unit_count": unit_count,
        "weaviate": {
            "http": weaviate_http,
            "grpc": weaviate_grpc,
            "total_rtts": weaviate_total,
            f"rtts_per_{unit_name}": per_unit(weaviate_total),
        },
        "s3": {
            **s3_counts,
            "total": s3_total,
            f"get_object_per_{unit_name}": per_unit(s3_counts["get_object"]),
            f"put_object_per_{unit_name}": per_unit(s3_counts["put_object"]),
        },
        "postgres": {
            **pg_counts,
            "total_rtts": pg_rtts,
            "total_statements": pg_statements,
            f"rtts_per_{unit_name}": per_unit(pg_rtts),
        },
    }


def metric_delta(baseline: float | None, candidate: float | None) -> dict:
    """Compute baseline, candidate, absolute delta, and percentage delta."""
    if baseline is None or candidate is None:
        return {"baseline": baseline, "candidate": candidate, "delta": None, "delta_pct": None}
    diff = candidate - baseline
    pct = (diff / baseline * 100.0) if baseline != 0 else None
    return {
        "baseline": _round(baseline, 3),
        "candidate": _round(candidate, 3),
        "delta": _round(diff, 3),
        "delta_pct": _round(pct, 2),
    }


def compute_search_comparison(baseline_profile: dict | None, candidate_profile: dict | None) -> dict:
    """Compare two search profile summaries (e.g. cold vs warm, or baseline vs candidate)."""
    if (
        not baseline_profile
        or not candidate_profile
        or baseline_profile.get("available") is False
        or candidate_profile.get("available") is False
        or not baseline_profile.get("overall")
        or not candidate_profile.get("overall")
    ):
        return {}
    base_overall = baseline_profile.get("overall") or {}
    cand_overall = candidate_profile.get("overall") or {}

    def dist_delta(left: dict | None, right: dict | None) -> dict:
        left = left or {}
        right = right or {}
        return {stat: metric_delta(left.get(stat), right.get(stat)) for stat in ("p50_ms", "p95_ms", "p99_ms", "mean_ms")}

    overall = {
        "wall": dist_delta(base_overall.get("wall"), cand_overall.get("wall")),
        "elapsed": dist_delta(base_overall.get("elapsed"), cand_overall.get("elapsed")),
        "phases": {
            phase: dist_delta(
                (base_overall.get("phases") or {}).get(phase),
                (cand_overall.get("phases") or {}).get(phase),
            )
            for phase in PHASES
        },
    }
    modes = sorted(set((baseline_profile.get("by_mode") or {})) | set((candidate_profile.get("by_mode") or {})))
    by_mode = {}
    for mode in modes:
        left_m = (baseline_profile.get("by_mode") or {}).get(mode) or {}
        right_m = (candidate_profile.get("by_mode") or {}).get(mode) or {}
        by_mode[mode] = {
            "wall": dist_delta(left_m.get("wall"), right_m.get("wall")),
            "elapsed": dist_delta(left_m.get("elapsed"), right_m.get("elapsed")),
            "phases": {
                phase: dist_delta((left_m.get("phases") or {}).get(phase), (right_m.get("phases") or {}).get(phase))
                for phase in PHASES
            },
        }
    return {
        "overall": overall,
        "by_mode": by_mode,
        "dependency_calls": _dependency_delta(
            baseline_profile.get("dependency_calls"),
            candidate_profile.get("dependency_calls"),
        ),
    }


def _dependency_delta(left: dict | None, right: dict | None) -> dict:
    if not left or not right or not left.get("available") or not right.get("available"):
        return {}
    unit = right.get("unit_name") or left.get("unit_name") or "unit"
    return {
        "weaviate_http_total": metric_delta(left["weaviate"]["http"]["total"], right["weaviate"]["http"]["total"]),
        "weaviate_grpc_total": metric_delta(left["weaviate"]["grpc"]["total"], right["weaviate"]["grpc"]["total"]),
        "weaviate_total_rtts": metric_delta(left["weaviate"]["total_rtts"], right["weaviate"]["total_rtts"]),
        f"weaviate_rtts_per_{unit}": metric_delta(
            left["weaviate"].get(f"rtts_per_{unit}"),
            right["weaviate"].get(f"rtts_per_{unit}"),
        ),
        "s3_get_object": metric_delta(left["s3"]["get_object"], right["s3"]["get_object"]),
        "s3_put_object": metric_delta(left["s3"]["put_object"], right["s3"]["put_object"]),
        f"s3_get_object_per_{unit}": metric_delta(
            left["s3"].get(f"get_object_per_{unit}"),
            right["s3"].get(f"get_object_per_{unit}"),
        ),
        f"s3_put_object_per_{unit}": metric_delta(
            left["s3"].get(f"put_object_per_{unit}"),
            right["s3"].get(f"put_object_per_{unit}"),
        ),
        "postgres_rtts": metric_delta(left["postgres"]["total_rtts"], right["postgres"]["total_rtts"]),
        "postgres_statements": metric_delta(left["postgres"]["total_statements"], right["postgres"]["total_statements"]),
        f"postgres_rtts_per_{unit}": metric_delta(
            left["postgres"].get(f"rtts_per_{unit}"),
            right["postgres"].get(f"rtts_per_{unit}"),
        ),
    }


def compute_report_delta(baseline: dict, candidate: dict) -> dict:
    """Compute complete baseline-vs-candidate deltas across search and ingestion."""
    out: dict = {
        "baseline_revision": (baseline.get("metadata") or {}).get("git_revision"),
        "candidate_revision": (candidate.get("metadata") or {}).get("git_revision"),
        "cold_search": compute_search_comparison(baseline.get("cold_search"), candidate.get("cold_search")),
        "warm_search": compute_search_comparison(baseline.get("warm_search"), candidate.get("warm_search")),
        "ingestion": {},
    }
    base_ing = baseline.get("ingestion")
    cand_ing = candidate.get("ingestion")
    if base_ing and cand_ing:
        out["ingestion"] = {
            "accepted_docs_per_sec": metric_delta(base_ing.get("accepted_docs_per_sec"), cand_ing.get("accepted_docs_per_sec")),
            "searchable_docs_per_sec": metric_delta(base_ing.get("searchable_docs_per_sec"), cand_ing.get("searchable_docs_per_sec")),
            "enriched_docs_per_sec": metric_delta(base_ing.get("enriched_docs_per_sec"), cand_ing.get("enriched_docs_per_sec")),
            "accept_p50_ms": metric_delta(
                (base_ing.get("accept_latency") or {}).get("p50_ms"),
                (cand_ing.get("accept_latency") or {}).get("p50_ms"),
            ),
            "accept_p95_ms": metric_delta(
                (base_ing.get("accept_latency") or {}).get("p95_ms"),
                (cand_ing.get("accept_latency") or {}).get("p95_ms"),
            ),
            "accept_p99_ms": metric_delta(
                (base_ing.get("accept_latency") or {}).get("p99_ms"),
                (cand_ing.get("accept_latency") or {}).get("p99_ms"),
            ),
            "dependency_calls": _dependency_delta(
                base_ing.get("dependency_calls"),
                cand_ing.get("dependency_calls"),
            ),
        }
    return out


def _fmt(value: float | int | None, suffix: str = "") -> str:
    if value is None:
        return "-"
    if isinstance(value, float):
        text = f"{value:.2f}".rstrip("0").rstrip(".")
        return f"{text}{suffix}"
    return f"{value}{suffix}"


def _fmt_delta(item: dict | None, higher_is_better: bool = False) -> tuple[str, str, str, str]:
    if not item:
        return "-", "-", "-", "-"
    base = _fmt(item.get("baseline"))
    cand = _fmt(item.get("candidate"))
    diff = item.get("delta")
    pct = item.get("delta_pct")
    if diff is None:
        return base, cand, "-", "-"
    sign = "+" if diff > 0 else ""
    diff_str = f"{sign}{_fmt(diff)}"
    if pct is None:
        pct_str = "-"
    else:
        pct_sign = "+" if pct > 0 else ""
        tag = ""
        if pct != 0:
            improved = (pct > 0) if higher_is_better else (pct < 0)
            tag = " (better)" if improved else " (worse)"
        pct_str = f"{pct_sign}{pct:.1f}%{tag}"
    return base, cand, diff_str, pct_str


def _render_phase_table(profile_label: str, summary: dict) -> list[str]:
    lines = [
        f"### {profile_label} — Per-Phase Latency (ms)",
        "",
        "| Phase | Samples | p50 ms | p95 ms | p99 ms | mean ms | max ms |",
        "| --- | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    rows: list[tuple[str, dict]] = [("client_wall", summary["wall"]), ("engine_elapsed", summary["elapsed"])]
    rows.extend((phase, summary["phases"][phase]) for phase in PHASES)
    for name, dist in rows:
        lines.append(
            f"| `{name}` | {dist['count']} | {_fmt(dist['p50_ms'])} | {_fmt(dist['p95_ms'])} | "
            f"{_fmt(dist['p99_ms'])} | {_fmt(dist['mean_ms'])} | {_fmt(dist['max_ms'])} |"
        )
    lines.append("")
    return lines


def _render_dependency_table(rows: list[tuple[str, dict | None]]) -> list[str]:
    lines = [
        "## Dependency Call Counts",
        "",
        "| Profile | Unit | Weaviate HTTP | Weaviate gRPC | Weaviate RTTs/unit | S3 GetObject | S3 PutObject | S3 GET/unit | S3 PUT/unit | PG RTTs | PG Queries | PG RTTs/unit |",
        "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    any_available = False
    for label, deps in rows:
        if not deps:
            continue
        if not deps.get("available"):
            lines.append(f"| {label} | {deps.get('unit_count', 0)} | n/a | n/a | n/a | n/a | n/a | n/a | n/a | n/a | n/a | n/a |")
            continue
        any_available = True
        unit = deps["unit_name"]
        unit_plural = "searches" if unit == "search" else f"{unit}s"
        w = deps["weaviate"]
        s3 = deps["s3"]
        pg = deps["postgres"]
        lines.append(
            f"| {label} | {deps['unit_count']} {unit_plural} | {w['http']['total']} | {w['grpc']['total']} | "
            f"{_fmt(w.get(f'rtts_per_{unit}'))} | {s3['get_object']} | {s3['put_object']} | "
            f"{_fmt(s3.get(f'get_object_per_{unit}'))} | {_fmt(s3.get(f'put_object_per_{unit}'))} | "
            f"{pg['total_rtts']} | {pg['total_statements']} | {_fmt(pg.get(f'rtts_per_{unit}'))} |"
        )
    lines.append("")
    if not any_available:
        lines.append("_Probe `/metrics` endpoints were unavailable or omitted for this run._")
        lines.append("")
    return lines


def render_delta_markdown(baseline: dict, candidate: dict, title: str = "Benchmark Delta (Baseline vs Candidate)") -> str:
    """Render PR-ready Markdown delta tables comparing a baseline and candidate report."""
    delta = compute_report_delta(baseline, candidate)
    lines = [
        f"## {title}",
        "",
        f"Baseline `{delta['baseline_revision'] or 'unknown'}` -> Candidate `{delta['candidate_revision'] or 'unknown'}`",
        "",
    ]

    for key, label in (("cold_search", "Cold Search"), ("warm_search", "Warm Search")):
        section = delta.get(key) or {}
        overall = section.get("overall") or {}
        if not overall:
            continue
        lines += [
            f"### {label} Latency Delta (overall)",
            "",
            "| Metric / Phase | Baseline p50 | Candidate p50 | Delta p50 | Baseline p95 | Candidate p95 | Delta p95 | Baseline p99 | Candidate p99 | Delta p99 |",
            "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
        ]
        entries: list[tuple[str, dict]] = [("client_wall", overall["wall"]), ("engine_elapsed", overall["elapsed"])]
        entries.extend((phase, overall["phases"][phase]) for phase in PHASES)
        for name, stats in entries:
            b50, c50, d50, p50 = _fmt_delta(stats["p50_ms"])
            b95, c95, d95, p95 = _fmt_delta(stats["p95_ms"])
            b99, c99, d99, p99 = _fmt_delta(stats["p99_ms"])
            lines.append(
                f"| `{name}` | {b50} | {c50} | {d50} / {p50} | {b95} | {c95} | {d95} / {p95} | {b99} | {c99} | {d99} / {p99} |"
            )
        lines.append("")

        dep = section.get("dependency_calls") or {}
        if dep:
            lines += [
                f"### {label} Dependency Call Delta",
                "",
                "| Metric | Baseline | Candidate | Delta | Change |",
                "| --- | ---: | ---: | ---: | ---: |",
            ]
            for metric_name, item in dep.items():
                b, c, d, p = _fmt_delta(item, higher_is_better=False)
                lines.append(f"| `{metric_name}` | {b} | {c} | {d} | {p} |")
            lines.append("")

    ing = delta.get("ingestion") or {}
    if ing:
        lines += [
            "### Ingestion Throughput & Dependency Delta",
            "",
            "| Metric | Baseline | Candidate | Delta | Change |",
            "| --- | ---: | ---: | ---: | ---: |",
        ]
        for metric_name, higher in (
            ("accepted_docs_per_sec", True),
            ("searchable_docs_per_sec", True),
            ("enriched_docs_per_sec", True),
            ("accept_p50_ms", False),
            ("accept_p95_ms", False),
            ("accept_p99_ms", False),
        ):
            b, c, d, p = _fmt_delta(ing.get(metric_name), higher_is_better=higher)
            lines.append(f"| `{metric_name}` | {b} | {c} | {d} | {p} |")
        for metric_name, item in (ing.get("dependency_calls") or {}).items():
            b, c, d, p = _fmt_delta(item, higher_is_better=False)
            lines.append(f"| `{metric_name}` | {b} | {c} | {d} | {p} |")
        lines.append("")

    return "\n".join(lines).rstrip() + "\n"


def render_markdown(report: dict, baseline: dict | None = None) -> str:
    """Render the full Markdown report for a single benchmark run (plus optional baseline delta)."""
    meta = report.get("metadata") or {}
    lines = [
        "# Quivr Performance Benchmark Report",
        "",
        f"- Status: **{report.get('status', 'unknown')}**",
        f"- Mode: `{meta.get('mode', 'unknown')}` · Revision: `{meta.get('git_revision', 'unknown')}`"
        f"{' (dirty)' if meta.get('git_dirty') else ''}",
        f"- Finished: `{report.get('finished_at', '')}` · Duration: `{_fmt(report.get('duration_seconds'), 's')}`",
        f"- Seed: `{meta.get('seed')}` · Workload SHA-256: `{meta.get('workload_sha256', '')[:16]}`",
        "",
    ]
    if report.get("error"):
        lines += [f"Error: `{report['error']}`", ""]

    # Search summary table (cold + warm across all modes)
    search_profiles = [
        (k, label)
        for k, label in (("cold_search", "Cold"), ("warm_search", "Warm"))
        if report.get(k) and report[k].get("available", True) and report[k].get("overall")
    ]
    unavailable_search = [
        (k, label)
        for k, label in (("cold_search", "Cold"), ("warm_search", "Warm"))
        if report.get(k) and report[k].get("available") is False
    ]
    if search_profiles:
        lines += [
            "## Search Latency (client wall time, ms)",
            "",
            "| Profile | Mode | Samples | Failures | p50 ms | p95 ms | p99 ms | mean ms | max ms |",
            "| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
        ]
        for key, label in search_profiles:
            section = report[key]
            for mode_name, summary in [("overall", section["overall"]), *sorted((section.get("by_mode") or {}).items())]:
                w = summary["wall"]
                lines.append(
                    f"| {label} | `{mode_name}` | {summary['samples']} | {summary['failures']} | "
                    f"{_fmt(w['p50_ms'])} | {_fmt(w['p95_ms'])} | {_fmt(w['p99_ms'])} | {_fmt(w['mean_ms'])} | {_fmt(w['max_ms'])} |"
                )
        lines.append("")

        for key, label in search_profiles:
            lines.extend(_render_phase_table(f"{label} Search (overall)", report[key]["overall"]))

    for key, label in unavailable_search:
        reason = report[key].get("reason") or "unavailable"
        lines += [f"### {label} Search", "", f"_Unavailable: {reason}_", ""]

    # Built-in cold vs warm delta table
    cold_vs_warm = report.get("cold_vs_warm_delta") or {}
    if cold_vs_warm.get("overall"):
        overall = cold_vs_warm["overall"]
        lines += [
            "## Cold vs Warm Search Delta",
            "",
            "| Metric / Phase | Cold p50 | Warm p50 | Delta p50 | Cold p95 | Warm p95 | Delta p95 | Cold p99 | Warm p99 | Delta p99 |",
            "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
        ]
        entries: list[tuple[str, dict]] = [("client_wall", overall["wall"]), ("engine_elapsed", overall["elapsed"])]
        entries.extend((phase, overall["phases"][phase]) for phase in PHASES)
        for name, stats in entries:
            b50, c50, d50, p50 = _fmt_delta(stats["p50_ms"])
            b95, c95, d95, p95 = _fmt_delta(stats["p95_ms"])
            b99, c99, d99, p99 = _fmt_delta(stats["p99_ms"])
            lines.append(
                f"| `{name}` | {b50} | {c50} | {d50} / {p50} | {b95} | {c95} | {d95} / {p95} | {b99} | {c99} | {d99} / {p99} |"
            )
        lines.append("")

    # Ingestion table
    ing = report.get("ingestion")
    if ing:
        acc = ing.get("accept_latency") or {}
        lines += [
            "## Ingestion Throughput",
            "",
            "| Stage | Docs | Seconds | Throughput (docs/s) |",
            "| --- | ---: | ---: | ---: |",
            f"| Accepted (`POST /v0/records`) | {ing['accepted_count']}/{ing['documents_total']} | {_fmt(ing['accepted_seconds'])} | {_fmt(ing['accepted_docs_per_sec'])} |",
            f"| Searchable (`record.retrieval_ready`) | {ing['searchable_count']}/{ing['documents_total']} | {_fmt(ing['searchable_seconds'])} | {_fmt(ing['searchable_docs_per_sec'])} |",
            f"| Enriched (`record.enrichment_available`) | {ing['enriched_count']}/{ing['documents_total']} | {_fmt(ing['enriched_seconds'])} | {_fmt(ing['enriched_docs_per_sec'])} |",
            "",
            f"HTTP acceptance latency: p50 `{_fmt(acc.get('p50_ms'), ' ms')}`, p95 `{_fmt(acc.get('p95_ms'), ' ms')}`, "
            f"p99 `{_fmt(acc.get('p99_ms'), ' ms')}`, errors `{ing.get('errors', 0)}`.",
            "",
        ]

    # Dependency calls table
    dep_rows = [
        ("Ingestion", (report.get("ingestion") or {}).get("dependency_calls")),
        ("Cold Search", (report.get("cold_search") or {}).get("dependency_calls")),
        ("Warm Search", (report.get("warm_search") or {}).get("dependency_calls")),
    ]
    if any(deps is not None for _, deps in dep_rows):
        lines.extend(_render_dependency_table(dep_rows))

    if baseline is not None:
        lines.append(render_delta_markdown(baseline, report))

    return "\n".join(lines).rstrip() + "\n"
