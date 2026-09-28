# THE-675: removing the per-query floor from public search

THE-661 measured a floor of about 850 ms on every public search, whatever the mode. This PR removes it. Search results are unchanged.

Each search tokenized the query by starting the pinned `scripts/token_offsets.py` in a new Python process, and that process parsed the 17 MB `tokenizer.json` again every time. Ingestion segmentation started the same process. Now one pinned tokenizer process is started per binary and stays alive (`tokenizer.Server`).

Both measurements below come from the `Retrieval baseline` workflow:
- same runner class: GitHub `ubuntu-24.04`, AMD EPYC 7763, 2 logical CPUs, 7.8 GiB;
- same day and same harness;
- the unchanged [workload-v1](../../tests/measurement/workload-v1.json) protocol, sha256 `95fc3c96…2b7304f`.

| Run | Source | Report |
| --- | --- | --- |
| before | [36419945626](https://github.com/The-Vibe-Company/quivr-v2/actions/runs/36419945626): `main` at `adec136` plus the child-RSS sampler only (`716c659`) | [the-675-retrieval-before.json](the-675-retrieval-before.json) |
| after | [36420407035](https://github.com/The-Vibe-Company/quivr-v2/actions/runs/36420407035): this branch at `f10319f` | [the-675-retrieval-after.json](the-675-retrieval-after.json) |

In the committed reports, one caveat string under `limits` is generalized; every measured value is as the CI artifact produced it.

## Profile (before any code change)

`TestTokenizerQueryCost` in `internal/adapters/tokenizer` sends the exact batch that `NormalizeQuery` sends for one search. It ran on the same Linux runner class, inside `make verify` ([run 36418843413](https://github.com/The-Vibe-Company/quivr-v2/actions/runs/36418843413), `adapters.log`).

| Phase inside one tokenizer call | ms |
| --- | --- |
| Python start + `import tokenizers` | 34–35 |
| read + sha256 of `tokenizer.json` | 13–30 |
| **`Tokenizer.from_str`** | **806–857** |
| encode the query | 0.16 |
| whole call, as seen from Go (p50 / p95, n=20) | 1190 / 1269 |

`from_str` on its own matches the ~850 ms search floor. Query embedding and vector search add about 25–30 ms. Go pprof would only show the Go side blocked in `cmd.Wait`, so the per-phase wall clock above is the profile.

After the fix the same test on Linux measures ([run 36421179653](https://github.com/The-Vibe-Company/quivr-v2/actions/runs/36421179653)):
- starting the persistent tokenizer once: 806 ms;
- each query afterwards: **p50 0.20 ms, p95 0.29 ms** (n=200).

## Latency (client wall time, ms; 120 samples per cell; target p95 < 1000 ms)

| Condition | Mode | before p50 | before p95 | after p50 | after p95 | after target |
| --- | --- | --- | --- | --- | --- | --- |
| sequential | lexical | 876 | 939 | 33 | 48 | met |
| sequential | semantic | 902 | 988 | 60 | 98 | met |
| sequential | hybrid | 905 | 981 | 61 | 105 | met |
| concurrent-4 | lexical | 2750 | 3233 | 61 | 118 | met |
| concurrent-4 | semantic | 2807 | 3215 | 200 | 295 | met |
| concurrent-4 | hybrid | 2851 | 3256 | 203 | 305 | met |
| ingesting | lexical | 3525 | 4161 | 39 | 64 | met |
| ingesting | semantic | 3711 | 4320 | 86 | 160 | met |
| ingesting | hybrid | 3690 | 4322 | 72 | 133 | met |

- **Before:** 2 of 360 ingesting samples failed with a retryable 503 `search_unavailable`. The before run misses the target under concurrency and ingestion, as THE-661 and the THE-658 run did.
- **After:** no failures; all nine cells meet the target.
- The worst after cell is concurrent-4 semantic/hybrid at about 0.3 s p95. There the query embeddings compete for CPU with TEI, which runs on the same 2 CPUs.

**Caveat on the ingesting condition.** The protocol fixes the ingestion rate (2 Records/s) and the number of samples, not how long the condition lasts, so it lasts as long as its 360 searches take:
- before: 1314 s and 2633 distractor Records;
- after: 26 s and 56 distractor Records.

The after run therefore spent less total time under ingestion load, although the load per second was the same. The fixture's own ingestion also got faster, because segmentation no longer starts a process: 24 Records to committed vectors took 70.6 s before and 5.3 s after. The whole run took 36 min before and 2.3 min after.

## Results unchanged

**Byte-identical encodings.**
- `TestServerMatchesPinnedReference` runs in `make verify` on Linux. It compares the persistent tokenizer with the pinned one-shot helper over 669 items and finds them identical: `reflect.DeepEqual` on token counts and every offset.
  - Inputs: all 24 fixture titles, bodies and queries, their `query:` and `passage:` model inputs, and edge cases (CRLF, astral characters, RTL, combining marks, the 255–512-token boundaries, 8192 code points).
  - Batches: one of 512 items and one of 1.9 MB.
- It also compares segmentation and `NormalizeQuery` end to end.
- `processing.Recipe` is unchanged, because `profile.json` and `windows.go` are untouched, so segmentation IDs are the same.
- `NormalizeQuery` only validates token counts. Tokenizer output never reaches the retrieval request.

**Ranks in the after run.**

| Mode | MRR@10 before | MRR@10 after | Relevant-Record ranks vs the THE-661 confirming run |
| --- | --- | --- | --- |
| lexical | 0.7250 | 0.6993 | identical for all 24 queries |
| semantic | 0.9583 | 0.9583 | identical (also identical to the before run and to THE-661's first run) |
| hybrid | 0.7969 | 0.7969 | identical for all 24 queries; 24/24 top-10 lists identical |

Lexical MRR already varies between runs on unchanged code: 0.7201 and 0.6993 in THE-661, and 0.7250 in this before run on `main`. The after run reproduced the THE-661 confirming run's lexical ranks exactly. So the lexical difference from the before run is that existing variance, not an effect of this change: the encodings are byte-identical and tokenizer output never reaches retrieval. The cause of the lexical variance is still open under THE-641.

## Memory

The harness now sums the RSS of the child processes of api and worker, which are the tokenizer processes. These are peaks during the latency workload.

| Process | before | after |
| --- | --- | --- |
| quivr-api (Go) | 39 MiB | 38 MiB |
| quivr-api children | **1312 MiB** (several concurrent one-shot tokenizers) | **321 MiB** (one resident tokenizer) |
| quivr-worker (Go) | 132 MiB | 51 MiB |
| quivr-worker children | **1393 MiB** | **321 MiB** |

- **Trade-off:** each binary now holds about 321 MiB permanently. Before, that memory was held only while a tokenizer process was running, but concurrent requests pushed the peak above 1.3 GiB per binary.
- **Idle footprint** goes from about 0 to about 0.64 GiB for api plus worker.
- **Peak footprint under load** goes down by about 2 GiB.

## Limits

- One run per side on shared 2-CPU runners.
- A small fixture of 24 single-segment documents.
- No claim about large-scale corpora, other architectures, or stability across runs.
- Hybrid relevance still belongs to THE-641.
