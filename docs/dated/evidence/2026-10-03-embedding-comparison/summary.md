# Direct embedding comparison

Date: 2026-10-03
Status: final

These four JSON files preserve the coordinator's original public-set measurements,
provided with THE-983 and reported on THE-952. Numbers and fields are unchanged;
missing historical metadata has not been reconstructed. The original script ran
from a temporary folder. Its repository port is `scripts/eval/direct_bakeoff.py`;
new runs add fingerprints, settings and conservative per-attempt budget accounting.
The original code commit, package/model revisions, machine details, token caps and
sample fingerprints were not captured. Exact numerical reproduction is not promised.

| Model | Dimensions | MIRACL-fr nDCG@10 | SciFact nDCG@10 | MLDR-fr nDCG@10 |
| --- | ---: | ---: | ---: | ---: |
| multilingual-e5-small | 384 | 0.711701 | 0.752598 | 0.916696 |
| Cohere Embed v5 Fast | 2048 | 0.767264 | 0.831182 | 0.931607 |
| Cohere Embed v5 Pro | 2048 | 0.777669 | 0.849028 | 0.936028 |
| text-embedding-3-large | 3072 | 0.754484 | 0.856754 | 0.898156 |

The dimensions rerun on MIRACL-fr measured Pro at 1024: 0.774465 nDCG@10, versus
0.777590 at 2048 and 0.711584 for e5. It did not measure 1024 on the other sets.
The coordinator selected Pro at 1024: its French scores led these candidates,
and the MIRACL reduction cost about 0.003 nDCG while halving vector storage.
Pro's improvements over e5 are significant on MIRACL-fr and SciFact; MLDR-fr
has no significant differences at p < 0.05. Paired tests are two-sided with no
correction for multiple comparisons. The small, sampled MLDR set is near its ceiling.

Method: the repository's `public_sets.prepare` sampling and `trec.load` qrels;
title + text; local e5 with query/passage prefixes; Foundry Cohere v2 with search
input types and OpenAI v1; exact cosine top ten and repository scoring.
Long documents use overlapping character pieces, roughly 450 tokens for e5 and
1,500 for hosted models, with each document scored by its best piece.
The first MIRACL file has no piece counts; the dimensions rerun has slightly
different baseline scores. Preserve these separate runs rather than combine them.

Files: `miracl-fr.json` (5,000 documents / 343 queries), `scifact.json` (2,000 / 300),
`mldr-fr.json` (600 / 200), and `miracl-fr-dims.json` (5,000 / 343).
Their rounded list-price estimates sum to $1.3930. Pro's historical price was
$0.12 per million input tokens; Fast $0.08 and OpenAI $0.13. These are estimates,
not receipts, and the raw totals exclude failed engine-path runs.
Timings measure local indexing and batched query encoding, not engine search
latency. Hybrid fusion, deployment throughput and rollback were not measured here.

This dated evidence location uses the repository's existing frozen-document rule.
Future runs add new files with a dated summary; merged results stay unchanged.
The long-term evidence store remains a separate research decision.
