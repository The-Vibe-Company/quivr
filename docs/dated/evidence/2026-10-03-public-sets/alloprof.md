# Dataset quality report: alloprof

Date: 2026-10-03
Status: evidence

Language: fr  
Description: student questions to educational pages  
Licence: cc-by-nc-sa-4.0  
Source: https://huggingface.co/datasets/mteb/AlloprofRetrieval  
Split: test  
Tier: restricted; promotion eligible: False

Version: `mteb/AlloprofRetrieval` `96a52a1744e2d1981a66d93ff3b5a595f9a150d5`

## Counts

| Sample documents | Sample queries | Judgements | Source documents | Source queries | Source judgements |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 2000 | 200 | 200 | 2556 | 2316 | 2316 |

## Judgements and queries

Judged depth: 1–1 documents per query; mean 1.000; total 200.
Grades: 1: 200.

| Length | Count | Min | Median | Mean | Max |
| --- | ---: | ---: | ---: | ---: | ---: |
| chars | 200 | 11 | 121.0 | 163.985 | 758 |
| words | 200 | 1 | 20.0 | 27.060 | 124 |

Query type source: student questions to educational pages.
Heuristic: question if text ends '?' or starts with a recognized lexical question marker; statement otherwise.
Heuristic counts: question 74, statement 126.
Manual query-type labels: unavailable; judge agreement: unavailable.

## Embedding quality

| System | Mean nDCG@10 | Saturated queries | Saturation share |
| --- | ---: | ---: | ---: |
| E5 | 0.3244 | 37 / 200 | 0.1850 |
| Cohere Pro | — | — / — | — |

Joint E5/Cohere Pro saturation: — (None / 200).
awaiting coordinator hosted Cohere-Embed-V5-Pro run.

## Hosted input estimate

One hosted pass uses 2424 document windows and 200 query inputs for an estimated 7,473,914 input tokens and $0.8969 at $0.12/million (dated 2026-10-03; estimate only).
Per-set cap recommendation: 14,947,828 input tokens and $1.80.

## Known issues

- Noncommercial-only: diagnostic use must comply with upstream terms; excluded from promotion gates.
- Original card MIT metadata conflicts with MTEB noncommercial licence; use the restrictive MTEB licence.
- One judged page per student question.

Sample fingerprint: `8a496391f5862c7bd59a26c5afbdf8d93c054e8bd716cadd075ffd71eb0040e6`

