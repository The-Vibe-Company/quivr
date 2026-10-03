# Dataset quality report: webfaq-fr

Date: 2026-10-03
Status: evidence

Language: fr  
Description: web frequently asked questions  
Licence: cc-by-4.0  
Source: https://huggingface.co/datasets/mteb/WebFAQRetrieval  
Split: test  
Tier: default; promotion eligible: True

Version: `mteb/WebFAQRetrieval` `f64f483ad0f31d2e78209d524c14a4a867965959`

## Counts

| Sample documents | Sample queries | Judgements | Source documents | Source queries | Source judgements |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 2500 | 200 | 200 | 569505 | 10000 | 10000 |

## Judgements and queries

Judged depth: 1–1 documents per query; mean 1.000; total 200.
Grades: 1: 200.

| Length | Count | Min | Median | Mean | Max |
| --- | ---: | ---: | ---: | ---: | ---: |
| chars | 200 | 17 | 54.0 | 57.445 | 133 |
| words | 200 | 3 | 9.0 | 9.625 | 24 |

Query type source: web frequently asked questions.
Heuristic: question if text ends '?' or starts with a recognized lexical question marker; statement otherwise.
Heuristic counts: question 191, statement 9.
Manual query-type labels: unavailable; judge agreement: unavailable.

## Embedding quality

| System | Mean nDCG@10 | Saturated queries | Saturation share |
| --- | ---: | ---: | ---: |
| E5 | 0.9254 | 172 / 200 | 0.8600 |
| Cohere Pro | — | — / — | — |

Joint E5/Cohere Pro saturation: — (None / 200).
awaiting coordinator hosted Cohere-Embed-V5-Pro run.

## Hosted input estimate

One hosted pass uses 2500 document windows and 200 query inputs for an estimated 955,384 input tokens and $0.1146 at $0.12/million (dated 2026-10-03; estimate only).
Per-set cap recommendation: 1,910,768 input tokens and $0.23.

## Known issues

- Web-scraped question/answer pairs; sparse relevance and possible duplicate answers; sampled corpus is easier than full corpus.

Sample fingerprint: `d66f4e1617d06d566722696fab1061062f13c38a9e00dd54c04bc9ba21a76b96`

