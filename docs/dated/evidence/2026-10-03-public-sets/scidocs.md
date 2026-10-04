# Dataset quality report: scidocs

Date: 2026-10-03
Status: evidence

Language: en  
Description: scientific citation-related titles  
Licence: cc-by-sa-4.0  
Source: https://huggingface.co/datasets/BeIR/scidocs  
Split: test  
Tier: default; promotion eligible: True

Version: `BeIR/scidocs` `acebceec772628c6679d76d6cff30ad517d17f4e`, `BeIR/scidocs-qrels` `735ea1048e37b1ebce14c6dc3d33a5edaf66d3dc`

## Counts

| Sample documents | Sample queries | Judgements | Source documents | Source queries | Source judgements |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 6000 | 200 | 5988 | 25657 | 1000 | 29928 |

## Judgements and queries

Judged depth: 28–30 documents per query; mean 29.940; total 5988.
Grades: 0: 5000, 1: 988.

| Length | Count | Min | Median | Mean | Max |
| --- | ---: | ---: | ---: | ---: | ---: |
| chars | 200 | 19 | 70.5 | 73.320 | 150 |
| words | 200 | 3 | 9.0 | 9.645 | 20 |

Query type source: scientific citation-related titles.
Heuristic: question if text ends '?' or starts with a recognized lexical question marker; statement otherwise.
Heuristic counts: question 3, statement 197.
Manual query-type labels: unavailable; judge agreement: unavailable.

## Embedding quality

| System | Mean nDCG@10 | Saturated queries | Saturation share |
| --- | ---: | ---: | ---: |
| E5 | 0.2425 | 2 / 200 | 0.0100 |
| Cohere Pro | — | — / — | — |

Joint E5/Cohere Pro saturation: — (None / 200).
awaiting coordinator hosted Cohere-Embed-V5-Pro run.

## Hosted input estimate

One hosted pass uses 6025 document windows and 200 query inputs for an estimated 7,344,613 input tokens and $0.8814 at $0.12/million (dated 2026-10-03; estimate only).
Per-set cap recommendation: 14,689,226 input tokens and $1.77.

## Known issues

- Citation-based relevance is a proxy rather than independent judgments; only a few relevant papers per title.

Sample fingerprint: `4aa61a6cc3210a8ccee30c54f885d1f3a2bc5e84c5f47518f8701d243bed80fb`

