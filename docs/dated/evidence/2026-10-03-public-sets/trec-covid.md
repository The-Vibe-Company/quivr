# Dataset quality report: trec-covid

Date: 2026-10-03
Status: evidence

Language: en  
Description: COVID research topics  
Licence: cc-by-sa-4.0  
Source: https://huggingface.co/datasets/BeIR/trec-covid  
Split: test  
Tier: default; promotion eligible: True

Version: `BeIR/trec-covid` `7e16fde3016c639c7f856e803f4bab92645562c4`, `BeIR/trec-covid-qrels` `532ac68ee6756ac22c9346eebf65bd3c6a042e10`

## Counts

| Sample documents | Sample queries | Judgements | Source documents | Source queries | Source judgements |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 10000 | 5 | 6301 | 171332 | 50 | 66336 |

## Judgements and queries

Judged depth: 979–1657 documents per query; mean 1260.200; total 6301.
Grades: 0: 3550, 1: 723, 2: 2028.

| Length | Count | Min | Median | Mean | Max |
| --- | ---: | ---: | ---: | ---: | ---: |
| chars | 5 | 42 | 58 | 55.800 | 64 |
| words | 5 | 7 | 10 | 9.200 | 10 |

Query type source: COVID research topics.
Heuristic: question if text ends '?' or starts with a recognized lexical question marker; statement otherwise.
Heuristic counts: question 5, statement 0.
Manual query-type labels: unavailable; judge agreement: unavailable.

## Embedding quality

| System | Mean nDCG@10 | Saturated queries | Saturation share |
| --- | ---: | ---: | ---: |
| E5 | 0.8629 | 2 / 5 | 0.4000 |
| Cohere Pro | — | — / — | — |

Joint E5/Cohere Pro saturation: — (None / 5).
awaiting coordinator hosted Cohere-Embed-V5-Pro run.

## Hosted input estimate

One hosted pass uses 10043 document windows and 5 query inputs for an estimated 12,817,889 input tokens and $1.5381 at $0.12/million (dated 2026-10-03; estimate only).
Per-set cap recommendation: 25,635,778 input tokens and $3.08.

## Known issues

- Only five seeded topics to preserve deep judgments within document budget; inadequate alone for significance.
- Pooled graded judgments retain explicit nonrelevant documents; sampled corpus differs from full CORD-19.

Sample fingerprint: `d2941377d1cf93df296d84bcfe41ccda90837bf9dee9480e6bc1aab3ceff4b12`

