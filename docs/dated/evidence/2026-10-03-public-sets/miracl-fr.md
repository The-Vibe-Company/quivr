# Dataset quality report: miracl-fr

Date: 2026-10-03
Status: evidence

Language: fr  
Description: MIRACL French dev queries over Wikipedia passages (MTEB hard-negative pool), native-speaker judgements  
Licence: Apache-2.0 (MIRACL); passages CC BY-SA 4.0 (Wikipedia)  
Source: https://huggingface.co/datasets/mteb/MIRACLRetrievalHardNegatives  
Split: dev  
Tier: default; promotion eligible: True

Version: `mteb/MIRACLRetrievalHardNegatives` `332a9acb49f5e83d5397683f79d23e588f685916`

## Counts

| Sample documents | Sample queries | Judgements | Source documents | Source queries | Source judgements |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 5000 | 343 | 2978 | 75357 | 343 | 3429 |

## Judgements and queries

Judged depth: 1–10 documents per query; mean 8.682; total 2978.
Grades: 0: 2247, 1: 731.

| Length | Count | Min | Median | Mean | Max |
| --- | ---: | ---: | ---: | ---: | ---: |
| chars | 343 | 16 | 44 | 43.883 | 83 |
| words | 343 | 3 | 7 | 7.169 | 14 |

Query type source: Wikipedia information questions.
Heuristic: question if text ends '?' or starts with a recognized lexical question marker; statement otherwise.
Heuristic counts: question 343, statement 0.
Manual query-type labels: unavailable; judge agreement: unavailable.

## Embedding quality

| System | Mean nDCG@10 | Saturated queries | Saturation share |
| --- | ---: | ---: | ---: |
| E5 | 0.7116 | 79 / 343 | 0.2303 |
| Cohere Pro | — | — / — | — |

Joint E5/Cohere Pro saturation: — (None / 343).
awaiting coordinator hosted Cohere-Embed-V5-Pro run.

## Hosted input estimate

One hosted pass uses 5001 document windows and 343 query inputs for an estimated 2,714,516 input tokens and $0.3257 at $0.12/million (dated 2026-10-03; estimate only).
Per-set cap recommendation: 5,429,032 input tokens and $0.66.

## Known issues

- Hard-negative pool and sampled distractors, not full Wikipedia retrieval.
- This sample drops 451 nonpositive judgments for documents absent from the source corpus or exceeding the engine text-size limit; every sampled positive judgment is retained.

Sample fingerprint: `0f86cc8028b8c49ce56d1f2442f21d18bdd05dd62c3da05eb8604bb5955d4e5f`

