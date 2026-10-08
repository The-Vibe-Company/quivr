# Measure open-source embeddings

Compare embedding models on public samples with `hosted.embed` and `direct_bakeoff.py`; operators can start with [Choose an embedding model](../../docs-site/run-quivr/choose-an-embedding-model.mdx).
Python 3.12+ and the requirements below install the Modal CLI; paid runs need operator authentication.
The coordinator dispatches paid runs; CI uses offline fixtures only.

## Preview the campaign

```sh
python3 -m venv .scratch/eval/oss-venv
.scratch/eval/oss-venv/bin/pip install -r scripts/eval/requirements-oss.txt
.scratch/eval/oss-venv/bin/python scripts/eval/oss_bakeoff.py plan \
  --out .scratch/eval/oss-plan.json
```

The plan previews ten sets, four models and two hardware choices without Modal.
`--include-restricted` adds three diagnostic sets, ineligible for promotion. Outputs refuse overwrites.

Candidates use Apache-2.0 weights: [Qwen3 0.6B](https://huggingface.co/Qwen/Qwen3-Embedding-0.6B),
[Granite 311M multilingual r2](https://huggingface.co/ibm-granite/granite-embedding-311m-multilingual-r2)
and [Arctic l v2](https://huggingface.co/Snowflake/snowflake-arctic-embed-l-v2.0).
Configurations live in `plugins/hosted-embed/examples/`; `oss_bakeoff.py` pins weight revisions.
TEI 1.9.3 uses its OpenAI-compatible API. CPU uses float32; L4 uses float16.
Qwen3 CPU is excluded pending validation: its pinned serving combination exited
before readiness in the operator run. Upstream includes CPU architecture support.

## Run a measurement

Paid example, not run here; authenticate with `.scratch/eval/oss-venv/bin/modal token new` first:

```sh
.scratch/eval/oss-venv/bin/python scripts/eval/oss_bakeoff.py run \
  --models e5-small qwen3 granite-r2 arctic-v2 --hardware cpu L4 \
  --timeout 3600 --concurrency 4 --max-input-tokens 200000000 --acknowledge-cost \
  --out .scratch/eval/oss-runs
```

Each model/hardware/set runs independently; `--concurrency` bounds both phases (default 4, 1..32).
Failures leave other jobs running; exit 2 means incomplete jobs. Loopback TEI servers are reaped.
The timeout covers each set job, including preparation. The model/hardware token
allowance is divided equally across sets, with any remainder unused; reservations
remain charged when usage is absent. Select fewer `--sets` for a larger allowance.

A separate CPU phase computes e5 once per set. Scored reference artifacts live in
`--reference-cache` (default `.scratch/eval/e5-references`), keyed by measurement
code and pinned encoder. Candidates validate the sample fingerprint before reuse.
A failed reference blocks only its set; other sets continue. Corrupt cache entries
record a reference mismatch; choose a new cache directory to recompute them.

Reference windows use 1,800 characters for e5 and 6,000 for others, 200-character
overlap, title plus text, and best-piece cosine top ten. TEI can truncate at its limit.
Set reports include cached CPU e5 scores; configured e5 rows measure TEI costs.
Query p50/p95 measures individual encoding over loopback, not engine search latency.
The direct benchmark omits plugin operational controls: each job sends serial
requests with one attempt and a 120-second HTTP timeout.

The dated [Modal function rates](https://modal.com/pricing) checked 2026-10-03 give
$0.252576/hour for four physical CPU cores and 8 GiB, or $1.051776/hour with an L4.
The plan sums per-set candidate and uncached reference compute, excluding builds,
scheduling, startup and egress; it is not an invoice or guaranteed cap. Candidate
per-token cost uses confirmed usage and encoding time; job/reference costs are separate.

## Compare and retain evidence

Examples, not run with real measurement evidence:

```sh
python3 scripts/eval/results log-direct .scratch/eval/oss-runs/qwen3-L4-miracl-fr.json \
  --experiment public/oss-embedding-comparison
python3 scripts/eval/oss_report.py --out .scratch/eval/oss-comparison.md \
  .scratch/eval/oss-runs/qwen3-L4-miracl-fr.json \
  .scratch/eval/hosted/miracl-fr.json
```

Import each completed set report through `scripts/eval/results`; `*-campaign.json`
is resource metadata rather than a scored set. Aggregate model/hardware campaigns
keep their existing filenames; `jobs/` holds individual stop reasons and Modal IDs,
and `references/` holds newly computed reference costs. Reasons distinguish timeout,
token cap, TEI exit code with the last 20 stderr lines (at most 8 KiB), provider error,
and unavailable reference. Job artifacts retain stderr; scored reports omit it; logs contain counts and phases.
Include hosted evidence from the same samples; use `--query-latency` for missing percentiles.
The comparison checks sample lineage and query IDs before paired statistics.
Restricted sets remain diagnostic and raw per-query data stays outside public reports.
Keep the hosted winner until quality, latency and serving cost justify replacement;
a nonsignificant difference alone does not prove equivalence.
