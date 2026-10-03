# Measure open-source embeddings

Compare self-hosted models with hosted models on the same public samples, using
`hosted.embed` configuration and the dense `direct_bakeoff.py` scoring path.
You need Python 3.11+, the dependencies below, and an authenticated Modal profile
for paid runs. The coordinator dispatches paid runs. CI uses offline fixtures only.

## Preview the campaign

```sh
python3 -m venv .scratch/eval/oss-venv
.scratch/eval/oss-venv/bin/pip install -r scripts/eval/requirements-oss.txt
.scratch/eval/oss-venv/bin/python scripts/eval/oss_bakeoff.py plan \
  --out .scratch/eval/oss-plan.json
```

The plan writes ten default sets, four models and two hardware choices without
contacting Modal. Add `--include-restricted` for three diagnostic sets; those
sets never count toward promotion. Outputs refuse overwrites. Use new paths on reruns.

Candidates use Apache-2.0 weights: [Qwen3 0.6B](https://huggingface.co/Qwen/Qwen3-Embedding-0.6B),
[Granite 311M multilingual r2](https://huggingface.co/ibm-granite/granite-embedding-311m-multilingual-r2)
and [Arctic l v2](https://huggingface.co/Snowflake/snowflake-arctic-embed-l-v2.0).
The fourth model is the pinned e5-small baseline served through TEI.
The configuration examples in `plugins/hosted-embed/examples/` set model, dimensions
and query/document prefixes. Full weight revisions live in `oss_bakeoff.py`;
plugin `model_revision` uses the first sixteen characters because its field is bounded.
TEI 1.9.3 serves all four through its OpenAI-compatible API. CPU uses float32; L4 uses float16.

## Run a measurement

Paid example, not run here; first authenticate the Modal CLI with your operator profile:

```sh
.scratch/eval/oss-venv/bin/python scripts/eval/oss_bakeoff.py run \
  --models e5-small qwen3 granite-r2 arctic-v2 --hardware cpu L4 \
  --timeout 3600 --max-input-tokens 200000000 --acknowledge-cost \
  --out .scratch/eval/oss-runs
```

Jobs run serially and create no deployment, public endpoint or persistent volume.
Each TEI server listens on container loopback and stops when the job ends.
The timeout covers each model/hardware job, including preparation and the local e5
comparison. Campaigns record Modal app and call IDs for billing reconciliation.
The token allowance is shared across its sets; byte-based reservations
remain charged when serving usage is absent. Interrupted jobs may have no returned
report; use smaller `--sets` selections if preparation or inference hits the timeout.

The reference windows stay at 1,800 characters for e5 and 6,000 for other models,
with 200-character overlap, title plus text, and best-piece cosine top ten.
TEI can truncate at its model limit, as the original local e5 encoder does.
Each set includes the pinned local CPU e5 comparison; configured e5 rows separately
measure TEI resource costs. Query p50/p95 measures individual encoding calls,
including loopback HTTP; it is not engine search latency or external network latency.
The direct benchmark requires cosine and omits plugin timeout/retry/concurrency/batch-token
controls. Its recorded execution is serial, one attempt, with a 120-second HTTP timeout.

The dated [Modal function rates](https://modal.com/pricing) checked 2026-10-03 give
$0.252576/hour for four physical CPU cores and 8 GiB, or $1.051776/hour with an L4.
Eight one-hour jobs estimate $5.217408 in function compute. Builds, scheduling,
startup and egress are excluded; this estimate is not an invoice or a guaranteed cap.
Candidate cost per million tokens uses confirmed OpenAI usage and candidate elapsed
time. Whole-job cost also includes preparation, local baseline and scoring, so keep
it separate. Missing token usage means unavailable per-token cost, never zero.

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
is resource metadata rather than a scored set. Include the coordinator's Cohere Fast
and Pro evidence from the same samples. Historical batch latency cannot supply query
percentiles; rerun with `direct_bakeoff.py --query-latency` if they are required.
The comparison checks sample lineage and query IDs before paired statistics.
Restricted sets remain diagnostic and raw per-query data stays outside public reports.
Keep the hosted winner until measured quality, latency and serving cost justify a
replacement on named hardware. A nonsignificant difference alone does not prove equivalence.
