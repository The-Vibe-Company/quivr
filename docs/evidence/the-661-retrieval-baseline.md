# THE-661: text retrieval baseline

Source: CI run [36403605116](https://github.com/The-Vibe-Company/quivr-v2/actions/runs/36403605116). It measured PR #20's merge ref `d5669a42`, which is branch head `d080248` merged onto `93e6d86`, with a clean tree. The protocol is [workload-v1](../../tests/measurement/workload-v1.json) and was frozen in its own commit before any result was observed. The machine-readable report is [the-661-retrieval-baseline.json](the-661-retrieval-baseline.json). To reproduce, run `make measure` on Linux x86_64 or the `Retrieval baseline` workflow.

Host: GitHub `ubuntu-24.04` runner (image 20260920.314.1), AMD EPYC 9V74 with **2 logical CPUs and 7.8 GiB**, Docker 28.0.4. Inference: CPU TEI float32 with the pinned E5 small snapshot. Retrieval profile: `balanced.e5-token-windows.v1`. The report pins image digests, model files, the tokenizer, the processing profile, the fixture and the workload.

## Quality

Measured on 24 CC0 FR/EN queries through public `POST /v0/search`, with limit 10 and title/body Manifests.

| Mode | MRR@10 | Recall@3 | Recall@10 |
| --- | --- | --- | --- |
| lexical | 0.7201 | 0.8333 | 0.9167 |
| semantic | 0.9583 | 1.0000 | 1.0000 |
| hybrid | 0.7760 | 0.9583 | 1.0000 |

The **hybrid deficit reproduces** through the public path:
- Hybrid ranks 10 queries below first place. `en-wind` is at rank 8.
- Semantic misses first place only on `fr-wind` and `en-wind`, both at rank 2.
- Semantic matches the prototype exactly (0.9583 / 1.0).
- Hybrid is 0.776 against the prototype's 0.7969 at the same Recall@3. The prototype had journey records as distractors; this run has an isolated Corpus.

The deficit is tracked by [THE-641](https://linear.app/thevibecompany/issue/THE-641). No weights or fixture were changed here.

## Latency

Client wall time in ms, 120 samples per cell, target p95 < 1000 ms.

| Condition | Mode | p50 | p95 | max | failures | target |
| --- | --- | --- | --- | --- | --- | --- |
| sequential | lexical | 855 | 899 | 957 | 0 | met |
| sequential | semantic | 882 | 919 | 953 | 0 | met |
| sequential | hybrid | 886 | 928 | 980 | 0 | met |
| concurrent-4 | lexical | 2721 | 3031 | 3255 | 0 | missed |
| concurrent-4 | semantic | 2765 | 3137 | 3299 | 0 | missed |
| concurrent-4 | hybrid | 2793 | 3105 | 3290 | 0 | missed |
| ingesting | lexical | 3742 | 4369 | 4643 | 0 | missed |
| ingesting | semantic | 3875 | 4472 | 4968 | 1 (503 `search_unavailable`) | missed |
| ingesting | hybrid | 3946 | 4375 | 4652 | 0 | missed |

- **Simple search meets the target** in all three modes, sequentially on this 2-CPU runner. The margin is only about 70–100 ms.
- The concurrent and ingesting conditions **miss it by 3–4.5×**. Those misses are recorded as they happened; the target was not relaxed.
- During the ingesting condition, 2,776 distractor Records were accepted at the declared 2 per second over 23 minutes. The worker therefore competed for CPU for that whole time.
- The one retryable 503 is kept in the report.

**Where the time goes.** This is a hypothesis; nothing was profiled.
- Lexical search, which never calls TEI, costs almost as much as semantic: about 855 ms against 882 ms. Query embedding therefore adds only about 25–30 ms.
- `NormalizeQuery` (`internal/processing/windows.go`) tokenizes every query through `internal/adapters/tokenizer`. That adapter starts a new Python `tokenizers` process for each call.
- This per-search process start is the likely constant cost, and also why latency grows with concurrency and ingestion on 2 CPUs. Ingestion segmentation spawns the same process.
- The first, cancelled attempt showed the same roughly 850 ms floor from the API's own request logs.

## Phases (seconds)

| Phase | Seconds |
| --- | --- |
| prepare tokenizer / model (958 MB downloaded) / go build / image pull | 3.9 / 7.7 / 2.9 / 20.0 |
| cold start (Compose up, migrations, API/worker readiness) | 9.2 |
| cold model ready (first semantic search) | 0.87 |
| warm start (cached images and model, fresh volumes) | 10.5 |
| warm model ready | 0.88 |
| 24 fixture Records accepted to vectors committed | 67.0 |

The runner cache was empty, so "prepare" is a cold preparation. The warm start is not faster than the cold start, because Compose images and model files were already local for both starts; only the preparation phase is cold.

## Resource peaks during the latency workload

| Component | CPU % peak | Memory MiB peak |
| --- | --- | --- |
| tei | 24.0 | 1198 |
| temporal | 30.3 | 226 |
| seaweed | 4.5 | 251 |
| postgres | 18.9 | 76 |
| weaviate | 3.5 | 105 |
| quivr-worker / quivr-api | lifetime-average CPU (from `ps`) | 131 / 41 RSS |

## Confirming run

A second run completed after the rebase: [run 36410913683](https://github.com/The-Vibe-Company/quivr-v2/actions/runs/36410913683). It measured merge ref `79211204` of head `87f6e4a`, with a clean tree, on an EPYC 7763 runner with 2 CPUs. Its artifact holds the report; it is not committed here.

| Mode | MRR@10 | Recall@3 | sequential p95 | concurrent-4 p95 | ingesting p95 |
| --- | --- | --- | --- | --- | --- |
| lexical | 0.6993 | 0.8333 | 871 | 2947 | 4061 |
| semantic | 0.9583 | 1.0000 | 895 | 2965 | 4117 |
| hybrid | 0.7969 | 0.9583 | 903 | 3013 | 4227 |

Same conclusions as the first run:
- Semantic is identical to the first run.
- The hybrid deficit reproduces. This run's hybrid score equals the prototype's 0.7969 exactly.
- Simple search meets the target, and the concurrent and ingesting conditions miss it. There were no failures.

Lexical and hybrid ranks vary between the two runs on the same fixture and pins:
- Hybrid `fr-train` ranked 2 in the first run and 1 in the second.
- Several lexical ranks moved; for example, `en-election` and `en-wind` dropped out of the top 10.

So one run does not fix lexical and hybrid scores to the fourth decimal. The variance is recorded for THE-641; its cause was not investigated here.

## Attempts and limits

- **Attempt 1** ([run 36400294008](https://github.com/The-Vibe-Company/quivr-v2/actions/runs/36400294008)) hit the 30-minute job timeout during the ingesting condition, so it wrote no report. The frozen protocol needs about 37 minutes at the observed latency. The job timeout was raised to 90 minutes and the protocol was left unchanged.
- **Scope of the numbers.** This is one run on shared hardware with 2 CPUs, a small 24-document fixture, and single-segment documents. It supports no claim at Agency scale, across architectures, about production relevance, or about stability across runs.
- **Hand-off.** Relevance work belongs to THE-641. The per-query tokenizer cost is a candidate follow-up and was not changed here.
