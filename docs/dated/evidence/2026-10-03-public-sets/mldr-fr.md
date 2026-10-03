# Dataset quality report: mldr-fr

Date: 2026-10-03
Status: evidence

Language: fr  
Description: MLDR French test queries over long Wikipedia and mC4 documents, one positive each, with their listed negatives first among distractors  
Licence: MIT  
Source: https://huggingface.co/datasets/Shitao/MLDR  
Split: test  
Tier: default; promotion eligible: True

Version: `Shitao/MLDR` `d67138e705d963e346253a80e59676ddb418810a`

## Counts

| Sample documents | Sample queries | Judgements | Source documents | Source queries | Source judgements |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 600 | 200 | 200 | 10000 | 200 | 200 |

## Judgements and queries

Judged depth: 1–1 documents per query; mean 1.000; total 200.
Grades: 1: 200.

| Length | Count | Min | Median | Mean | Max |
| --- | ---: | ---: | ---: | ---: | ---: |
| chars | 200 | 33 | 118.0 | 149.795 | 2589 |
| words | 200 | 6 | 20.0 | 24.320 | 377 |

Query type source: synthetic long-document questions.
Heuristic: question if text ends '?' or starts with a recognized lexical question marker; statement otherwise.
Heuristic counts: question 188, statement 12.
Manual query-type labels: unavailable; judge agreement: unavailable.

## Embedding quality

| System | Mean nDCG@10 | Saturated queries | Saturation share |
| --- | ---: | ---: | ---: |
| E5 | 0.9167 | 174 / 200 | 0.8700 |
| Cohere Pro | — | — / — | — |

Joint E5/Cohere Pro saturation: — (None / 200).
awaiting coordinator hosted Cohere-Embed-V5-Pro run.

## Hosted input estimate

One hosted pass uses 1781 document windows and 200 query inputs for an estimated 9,443,691 input tokens and $1.1332 at $0.12/million (dated 2026-10-03; estimate only).
Per-set cap recommendation: 18,887,382 input tokens and $2.27.

## Known issues

- GPT-3.5 generated queries; one positive per query; document sampling and e5 character windows change task difficulty.

Sample fingerprint: `d607d8439c9e6a8279b996ab08983f65666f17cd3a3bd8b4c458ce7fe8c2bdad`

