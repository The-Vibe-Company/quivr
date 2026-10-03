# Dataset quality report: xpqa-fr

Date: 2026-10-03
Status: evidence

Language: fr  
Description: product questions  
Licence: cdla-sharing-1.0  
Source: https://huggingface.co/datasets/mteb/XPQARetrieval  
Split: test  
Tier: default; promotion eligible: True

Version: `mteb/XPQARetrieval` `fc4624be978945a0ceebbc4b85737258fe26330b`

## Counts

| Sample documents | Sample queries | Judgements | Source documents | Source queries | Source judgements |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 1548 | 200 | 410 | 1548 | 749 | 1550 |

## Judgements and queries

Judged depth: 1–5 documents per query; mean 2.050; total 410.
Grades: 1: 410.

| Length | Count | Min | Median | Mean | Max |
| --- | ---: | ---: | ---: | ---: | ---: |
| chars | 200 | 16 | 57.0 | 57.830 | 107 |
| words | 200 | 2 | 10.0 | 10.375 | 17 |

Query type source: product questions.
Heuristic: question if text ends '?' or starts with a recognized lexical question marker; statement otherwise.
Heuristic counts: question 134, statement 66.
Manual query-type labels: unavailable; judge agreement: unavailable.

## Embedding quality

| System | Mean nDCG@10 | Saturated queries | Saturation share |
| --- | ---: | ---: | ---: |
| E5 | 0.5553 | 59 / 200 | 0.2950 |
| Cohere Pro | — | — / — | — |

Joint E5/Cohere Pro saturation: — (None / 200).
awaiting coordinator hosted Cohere-Embed-V5-Pro run.

## Hosted input estimate

One hosted pass uses 1548 document windows and 200 query inputs for an estimated 146,667 input tokens and $0.0176 at $0.12/million (dated 2026-10-03; estimate only).
Per-set cap recommendation: 293,334 input tokens and $0.04.

## Known issues

- French-to-French product question/evidence pairs; sparse judgments; no manual query-intent annotations.

Sample fingerprint: `577c2fd4b1522fd742e9c48afcdbda6c1d32842b125865513ebe9744f64a0446`

