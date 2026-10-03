# Dataset quality report: mkqa-fr

Date: 2026-10-03
Status: evidence

Language: fr  
Description: knowledge questions to short answers  
Licence: cc-by-3.0  
Source: https://huggingface.co/datasets/mteb/MKQARetrieval  
Split: train  
Tier: default; promotion eligible: True

Version: `mteb/MKQARetrieval` `3e069e7a30079d214a859741f5a0de75cf878867`

## Counts

| Sample documents | Sample queries | Judgements | Source documents | Source queries | Source judgements |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 2000 | 200 | 299 | 6617 | 6751 | 9929 |

## Judgements and queries

Judged depth: 1–6 documents per query; mean 1.495; total 299.
Grades: 1: 299.

| Length | Count | Min | Median | Mean | Max |
| --- | ---: | ---: | ---: | ---: | ---: |
| chars | 200 | 30 | 49.0 | 50.375 | 105 |
| words | 200 | 6 | 9.0 | 9.595 | 20 |

Query type source: knowledge questions to short answers.
Heuristic: question if text ends '?' or starts with a recognized lexical question marker; statement otherwise.
Heuristic counts: question 150, statement 50.
Manual query-type labels: unavailable; judge agreement: unavailable.

## Embedding quality

| System | Mean nDCG@10 | Saturated queries | Saturation share |
| --- | ---: | ---: | ---: |
| E5 | 0.0966 | 8 / 200 | 0.0400 |
| Cohere Pro | — | — / — | — |

Joint E5/Cohere Pro saturation: — (None / 200).
awaiting coordinator hosted Cohere-Embed-V5-Pro run.

## Hosted input estimate

One hosted pass uses 2000 document windows and 200 query inputs for an estimated 54,552 input tokens and $0.0065 at $0.12/million (dated 2026-10-03; estimate only).
Per-set cap recommendation: 109,104 input tokens and $0.02.

## Known issues

- MTEB conversion retrieves short answers, not source passages; train is the only available split; translated questions and ambiguous answers limit realism.

Sample fingerprint: `208a8e4a385677bd1d731ce61b34bf8425a557422fd6e0e84d71295259d3e9d4`

