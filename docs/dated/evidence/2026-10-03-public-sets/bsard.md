# Dataset quality report: bsard

Date: 2026-10-03
Status: evidence

Language: fr  
Description: legal questions to statutory articles  
Licence: cc-by-nc-sa-4.0  
Source: https://huggingface.co/datasets/mteb/BSARDRetrieval  
Split: test  
Tier: restricted; promotion eligible: False

Version: `mteb/BSARDRetrieval` `8c492add6a14ac188f2debdaf6cbdfb406fd6be3`

## Counts

| Sample documents | Sample queries | Judgements | Source documents | Source queries | Source judgements |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 2000 | 200 | 200 | 22633 | 222 | 222 |

## Judgements and queries

Judged depth: 1–1 documents per query; mean 1.000; total 200.
Grades: 1: 200.

| Length | Count | Min | Median | Mean | Max |
| --- | ---: | ---: | ---: | ---: | ---: |
| chars | 200 | 36 | 140.0 | 144.690 | 275 |
| words | 200 | 7 | 22.0 | 23.110 | 47 |

Query type source: legal questions to statutory articles.
Heuristic: question if text ends '?' or starts with a recognized lexical question marker; statement otherwise.
Heuristic counts: question 56, statement 144.
Manual query-type labels: unavailable; judge agreement: unavailable.

## Embedding quality

| System | Mean nDCG@10 | Saturated queries | Saturation share |
| --- | ---: | ---: | ---: |
| E5 | 0.3112 | 34 / 200 | 0.1700 |
| Cohere Pro | — | — / — | — |

Joint E5/Cohere Pro saturation: — (None / 200).
awaiting coordinator hosted Cohere-Embed-V5-Pro run.

## Hosted input estimate

One hosted pass uses 2026 document windows and 200 query inputs for an estimated 1,958,099 input tokens and $0.2350 at $0.12/million (dated 2026-10-03; estimate only).
Per-set cap recommendation: 3,916,198 input tokens and $0.47.

## Known issues

- Noncommercial-only: diagnostic use must comply with upstream terms; excluded from promotion gates.
- Expert legal judgments are sparse and cannot represent all relevant articles.

Sample fingerprint: `a97b69da3c499a7af75e1716f34192d341afb5883857423da3b2e37dbc443230`

