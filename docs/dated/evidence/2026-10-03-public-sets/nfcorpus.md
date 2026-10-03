# Dataset quality report: nfcorpus

Date: 2026-10-03
Status: evidence

Language: en  
Description: nutrition and medical questions  
Licence: academic-only  
Source: https://huggingface.co/datasets/BeIR/nfcorpus  
Split: test  
Tier: restricted; promotion eligible: False

Version: `BeIR/nfcorpus` `b5026a0e96e8a7ac4f95f482a596389289d46269`, `BeIR/nfcorpus-qrels` `a451b3b26d3ae1358f259c1a3a4dd61fcea35a65`

## Counts

| Sample documents | Sample queries | Judgements | Source documents | Source queries | Source judgements |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 3600 | 200 | 8239 | 3633 | 3237 | 12334 |

## Judgements and queries

Judged depth: 1–475 documents per query; mean 41.195; total 8239.
Grades: 1: 7926, 2: 313.

| Length | Count | Min | Median | Mean | Max |
| --- | ---: | ---: | ---: | ---: | ---: |
| chars | 200 | 4 | 15.0 | 20.430 | 66 |
| words | 200 | 1 | 2.0 | 3.125 | 10 |

Query type source: nutrition and medical questions.
Heuristic: question if text ends '?' or starts with a recognized lexical question marker; statement otherwise.
Heuristic counts: question 26, statement 174.
Manual query-type labels: unavailable; judge agreement: unavailable.

## Embedding quality

| System | Mean nDCG@10 | Saturated queries | Saturation share |
| --- | ---: | ---: | ---: |
| E5 | 0.3167 | 11 / 200 | 0.0550 |
| Cohere Pro | — | — / — | — |

Joint E5/Cohere Pro saturation: — (None / 200).
awaiting coordinator hosted Cohere-Embed-V5-Pro run.

## Hosted input estimate

One hosted pass uses 3606 document windows and 200 query inputs for an estimated 5,772,420 input tokens and $0.6927 at $0.12/million (dated 2026-10-03; estimate only).
Per-set cap recommendation: 11,544,840 input tokens and $1.39.

## Known issues

- Academic-only source terms override generic BEIR mirror metadata; excluded from promotion gates.
- Judgments derived from website links, not independent human relevance assessors.

Sample fingerprint: `17c5fec9d0636ddce6e5b9f9a479bf181a3df228a57c086669111cdefe312f02`

