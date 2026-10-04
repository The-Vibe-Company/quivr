# Dataset quality report: fiqa

Date: 2026-10-03
Status: evidence

Language: en  
Description: financial questions  
Licence: cc-by-sa-4.0  
Source: https://huggingface.co/datasets/BeIR/fiqa  
Split: test  
Tier: default; promotion eligible: True

Version: `BeIR/fiqa` `979c07a7cb5ccc6ca009792241fa1250b98055dd`, `BeIR/fiqa-qrels` `252958f2d646e22cab6d0c72dd3f0d5de6d0655a`

## Counts

| Sample documents | Sample queries | Judgements | Source documents | Source queries | Source judgements |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 2000 | 200 | 530 | 57638 | 6648 | 1706 |

## Judgements and queries

Judged depth: 1–12 documents per query; mean 2.650; total 530.
Grades: 1: 530.

| Length | Count | Min | Median | Mean | Max |
| --- | ---: | ---: | ---: | ---: | ---: |
| chars | 200 | 18 | 61.5 | 62.860 | 147 |
| words | 200 | 2 | 11.0 | 11.095 | 31 |

Query type source: financial questions.
Heuristic: question if text ends '?' or starts with a recognized lexical question marker; statement otherwise.
Heuristic counts: question 155, statement 45.
Manual query-type labels: unavailable; judge agreement: unavailable.

## Embedding quality

| System | Mean nDCG@10 | Saturated queries | Saturation share |
| --- | ---: | ---: | ---: |
| E5 | 0.6421 | 57 / 200 | 0.2850 |
| Cohere Pro | — | — / — | — |

Joint E5/Cohere Pro saturation: — (None / 200).
awaiting coordinator hosted Cohere-Embed-V5-Pro run.

## Hosted input estimate

One hosted pass uses 2004 document windows and 200 query inputs for an estimated 1,767,103 input tokens and $0.2121 at $0.12/million (dated 2026-10-03; estimate only).
Per-set cap recommendation: 3,534,206 input tokens and $0.43.

## Known issues

- Sparse financial question/answer judgments; unjudged answers treated as nonrelevant.

Sample fingerprint: `5b350679b929743fa0a2d33420d1e1c54e2bbe2d1269ad6d7f9bc29d29f6611b`

