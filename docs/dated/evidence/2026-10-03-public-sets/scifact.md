# Dataset quality report: scifact

Date: 2026-10-03
Status: evidence

Language: en  
Description: SciFact (BEIR) test claims over scientific abstracts  
Licence: claims CC BY 4.0, abstracts ODC-By 1.0  
Source: https://github.com/allenai/scifact  
Split: test  
Tier: default; promotion eligible: True

Version: `BEIR` `scifact archive sha256 536e14446a0ba56ed1398ab1055f39fe852686ecad24a6306c80c490fa8e0165`

## Counts

| Sample documents | Sample queries | Judgements | Source documents | Source queries | Source judgements |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 2000 | 300 | 339 | 5183 | 1109 | 339 |

## Judgements and queries

Judged depth: 1–5 documents per query; mean 1.130; total 339.
Grades: 1: 339.

| Length | Count | Min | Median | Mean | Max |
| --- | ---: | ---: | ---: | ---: | ---: |
| chars | 300 | 28 | 81.5 | 90.347 | 204 |
| words | 300 | 4 | 12.0 | 12.507 | 29 |

Query type source: scientific claims.
Heuristic: question if text ends '?' or starts with a recognized lexical question marker; statement otherwise.
Heuristic counts: question 0, statement 300.
Manual query-type labels: unavailable; judge agreement: unavailable.

## Embedding quality

| System | Mean nDCG@10 | Saturated queries | Saturation share |
| --- | ---: | ---: | ---: |
| E5 | 0.7526 | 185 / 300 | 0.6167 |
| Cohere Pro | — | — / — | — |

Joint E5/Cohere Pro saturation: — (None / 300).
awaiting coordinator hosted Cohere-Embed-V5-Pro run.

## Hosted input estimate

One hosted pass uses 2003 document windows and 300 query inputs for an estimated 3,097,293 input tokens and $0.3717 at $0.12/million (dated 2026-10-03; estimate only).
Per-set cap recommendation: 6,194,586 input tokens and $0.75.

## Known issues

- Sparse claims-to-abstract judgments; unjudged documents treated as nonrelevant.

Sample fingerprint: `55c00c954b6f7997aabbc8de46f72276a0bd5aad3d57ff9f5a01de6692bf4a13`

