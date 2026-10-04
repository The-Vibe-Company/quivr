# Synthetic validation of the private news-set builder

Date: 2026-10-03
Status: final

The [aggregate report](quality.json) comes from the offline builder's eight synthetic
French articles and fake generation/retrieval/judge adapters. It validates the pipeline,
not an embedding model or the quality of live search. No provider was called.

The command was `NUMBA_NUM_THREADS=1 python3 scripts/eval/news_set.py --fake --questions 1500`
with separate working and confirmation age recipients, an ignored local ciphertext
directory and an ignored quality-report output. It produced 1,500 questions (250 per type),
900 working questions, 600 held-out questions, 12,000 pooled judgments and a 100-row review sheet.
All three fake judges agree by construction; the perfect scores are synthetic.

Only this aggregate report is retained publicly. No article text, generated question,
judgment row, plaintext archive or key is included. The report's object names are opaque;
the ciphertext artifacts are disposable validation artifacts, not a published live dataset.

The ten owner tests exercise adapter loading, filtering, pooling and agreement, stratified splitting,
working-only scoring, human review, Jev batching, literal spreadsheet cells and real age encryption.
Storage tests cover interrupted publication, commit-response loss and ACL-disabled S3 request serialization.
The focused suite passed in about 5.7 seconds after the scorer's initial JIT compilation.

A live dataset remains coordinator work: approved capped adapters, real news articles
from RSS feeds, durable private storage, the actual current-system baseline and 100
human-reviewed judgments. `human_check.status` in this report is `pending`.
