# Dataset quality report: arguana

Date: 2026-10-03
Status: evidence

Language: en  
Description: counterarguments  
Licence: cc-by-sa-4.0  
Source: https://huggingface.co/datasets/BeIR/arguana  
Split: test  
Tier: default; promotion eligible: True

Version: `BeIR/arguana` `9bcf8fc0320c4e3860e25b8b85fa205fcdd7078f`, `BeIR/arguana-qrels` `ae5468c6f1c198109a8af5f0d4dc58bd18b6fea7`

## Counts

| Sample documents | Sample queries | Judgements | Source documents | Source queries | Source judgements |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 2000 | 200 | 200 | 8674 | 1406 | 1406 |

## Judgements and queries

Judged depth: 1–1 documents per query; mean 1.000; total 200.
Grades: 1: 200.

| Length | Count | Min | Median | Mean | Max |
| --- | ---: | ---: | ---: | ---: | ---: |
| chars | 200 | 251 | 1114.0 | 1230.295 | 5500 |
| words | 200 | 47 | 179.0 | 199.040 | 868 |

Query type source: counterarguments.
Heuristic: question if text ends '?' or starts with a recognized lexical question marker; statement otherwise.
Heuristic counts: question 2, statement 198.
Manual query-type labels: unavailable; judge agreement: unavailable.

## Embedding quality

| System | Mean nDCG@10 | Saturated queries | Saturation share |
| --- | ---: | ---: | ---: |
| E5 | 0.5703 | 63 / 200 | 0.3150 |
| Cohere Pro | — | — / — | — |

Joint E5/Cohere Pro saturation: — (None / 200).
awaiting coordinator hosted Cohere-Embed-V5-Pro run.

## Hosted input estimate

One hosted pass uses 2000 document windows and 200 query inputs for an estimated 2,333,983 input tokens and $0.2801 at $0.12/million (dated 2026-10-03; estimate only).
Per-set cap recommendation: 4,667,966 input tokens and $0.57.

## Known issues

- Counterargument retrieval; long queries; one relevant document each and sparse judgments.

Sample fingerprint: `de0f0e659ed9a61eacdef0916352612a930608886c4e9479870bda7d776a5426`

