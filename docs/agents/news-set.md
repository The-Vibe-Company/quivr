# Build a private French news search set

Use the builder to turn news articles from RSS feeds into an encrypted search evaluation set.
You get working/held-out sets, a human review sheet and an aggregate quality report.

## Prerequisites

- Python 3.12+, `age` and `age-keygen` (`python3 --version`, `age --version`).
- Install both `scripts/eval/requirements{,-news}.txt`; for Jev, run `python3 -m pip install --no-deps -e plugins/jev-rerank`.
- A private storage directory, mounted Modal Volume, or S3-compatible bucket.
- Separate age keys for the working measurement runner and confirmation runner.
  Human reviewers use a privileged key that working agents cannot access.
  The operator provisions these keys and grants runner access outside the builder.
- For a real build, approved generator/retrieval/judge adapters with their own cost caps.
  The worker uses fakes only. The coordinator runs hosted providers under a cost cap.

## Try the pipeline offline

These commands use eight synthetic French articles, fake providers and disposable local keys.
The scores demonstrate the pipeline; they say nothing about search quality.
Run once in a fresh directory; existing artifacts and reports are refused.

```sh
mkdir -p .scratch/news-demo
chmod 700 .scratch/news-demo
age-keygen -o .scratch/news-demo/working.key
age-keygen -o .scratch/news-demo/confirmation.key
export QUIVR_NEWS_WORKING_RECIPIENTS="$(age-keygen -y .scratch/news-demo/working.key)"
export QUIVR_NEWS_HOLDOUT_RECIPIENTS="$(age-keygen -y .scratch/news-demo/confirmation.key)"
export QUIVR_NEWS_REVIEW_RECIPIENTS="$QUIVR_NEWS_HOLDOUT_RECIPIENTS"
NUMBA_NUM_THREADS=1 python3 scripts/eval/news_set.py --fake --questions 30 \
  --storage-dir .scratch/news-demo/encrypted --report .scratch/news-demo/quality.json
```

The command prints `Encrypted dataset stored; aggregate quality report written. Human review is pending.`
The report has `status: synthetic`, 18 working questions and 12 held-out questions.
It contains counts, agreement and baseline metrics, hashes and opaque object names; no article or query text.
Delete `.scratch/news-demo` after trying it. Never commit keys or decrypted artifacts.

## Build from articles

Supply a folder of JSON article objects, JSON arrays or JSONL rows.
Fields are in the [article schema](../../scripts/eval/news-article.schema.json):
`id`, `title`, `text`, `published_at`, and optional `cluster` for related articles.
Dates accept ISO dates or timestamps with a timezone, normalized to UTC calendar dates.
Without a cluster, the generator samples within calendar months for multi-article questions.

Create a trusted Python adapter file outside Git exporting `providers()` returning
[`Providers`](../../scripts/eval/news_set.py). Its interfaces are:
Only the fake providers and `JevJudge` adapter ship here; implement the live generator,
retrievers and two LLM judge adapters for your approved services.

| Adapter | Contract |
| --- | --- |
| Generator | `generate(sample, kind, count, rng)` returns `Question` objects, source ids and evidence dates. |
| Retriever | `search(question, corpus, limit)` returns distinct article ids, in ranking order. |
| Judge | `grade(question, candidates)` returns every candidate's integer grade, 0–3; exposes `family`. |
| Storage | `put` creates immutable objects, `get` reads commit manifests, `delete` cleans only aborted attempt objects. |

Generators receive sampled articles, or up to three related articles for multi-article questions.
Generate natural French newsroom searches, with no copied title or paraphrase content keywords.
Use the six types `entity`, `event`, `recent`, `paraphrase`, `multi_article`, `no_answer`.
Questions must cite evidence from their sample, with its latest date; no-answer questions cite no evidence.
The builder deduplicates normalized text, filters copies and fills equal type quotas with bounded attempts.

Live adapters provide retrievers named `bm25`, `e5_small`, `cohere_pro`, `hybrid`.
Set `Providers.baseline` to the key for the current system; it is not a fifth retriever.
Each contributes up to 20 candidates. All candidates receive three complete, independent judgments.
Use `JevJudge` with the existing [Jev client](../../plugins/jev-rerank/jev_rerank/client.py)
and two LLM judges from distinct families, served on approved Azure Foundry or Modal endpoints.
Jev supplies binary relevance probabilities: its adapter maps probability quartiles to 0–3.
This is a proxy for ordinal relevance; the report records `jev_grade_mapping: probability_quartiles`.
Jev batches the full pool within its byte/token bounds, sharing one deadline and cost allowance.
The other judges use 0 unrelated, 1 marginal, 2 partial answer, 3 direct answer.
Treat source/query text as untrusted; adapters must bound cost before each provider attempt and never log input or secrets.

For example, not run with real articles or provider credentials:

```sh
python3 scripts/eval/news_set.py --articles /private/articles \
  --providers /private/news_adapters.py --questions 1500 \
  --storage-dir /private/news-volume --report .scratch/news-quality.json
```

Replace `--storage-dir` with `--bucket <private-bucket> --prefix <version-prefix>` for S3 storage.
Use boto3's standard role/environment credentials with Get/Put/Delete access to the private prefix.
`--s3-endpoint` accepts an HTTPS S3-compatible endpoint. Configure bucket/Volume access outside this repository.
Writes omit ACLs for owner-enforced buckets; private access comes from bucket policy.
Age encryption protects content even if a storage policy is misconfigured.

## Check it worked

Live builds require at least 1,500 questions and all four retrieval systems plus the Jev adapter.
Missing votes, invalid grades, empty pools and pooled answerability conflicts fail the build.
The majority grade wins; three-way disagreements use the median and are counted separately.
The report includes three unweighted Cohen pair kappas and Fleiss' kappa; undefined values are null.
High-ranked grade-zero candidates become private hard negatives.

Working/held-out allocation is 60/40, stratified by type and UTC month using largest remainders.
`--seed` controls sampling and allocation. Each build creates new salted opaque ids and a content version.
Freeze the published version: rerunning creates a new version rather than replacing the old one.
The baseline and saturation scores use working queries only and the existing [scorer](../../scripts/eval/scoring.py).
No-answer questions remain in both sets but are excluded from nDCG/Recall/MRR, as the existing TREC loader requires.
Their working count is reported separately; pooled no-answer is not proof that the whole corpus has no answer.

Both encrypted archives contain the existing [TREC layout](../../scripts/eval/trec.py),
private question metadata, raw votes and hard negatives. Check ciphertext hashes before decrypting.
Private provenance records the seed, baseline, judge families/versions and article dates/clusters.
Archives are serialized incrementally and published one at a time, entirely in memory.
An immutable `<version>-manifest.json` containing only aggregates commits the complete set last.
Failed attempts clean their unique objects; failed cleanup leaves uncommitted ciphertext that cannot block a retry.
If the final commit response is lost, recover the aggregate report from that private manifest.
Working keys must be disjoint from held-out and review recipients.
The builder rejects overlapping recipient groups before publishing any artifact.
Only trusted confirmation runners receive holdout identities; this builder does not implement campaign read limits.
See the [aggregate report schema](../../scripts/eval/news-report.schema.json) for the public output contract.

## Complete the human review

The encrypted review CSV samples 100 judgments across question types and relevance grades.
On the privileged review machine, verify its ciphertext hash from the report, then decrypt it into private storage.
A person fills only `human_grade` (0–3), leaving every exported column intact.
Formula-leading text cells have an apostrophe prefix; import columns as text and preserve those prefixes.
Keep the source sheet and completed sheet private, including the held-out questions they contain.

For example, not run with a person's completed sheet:

```sh
export QUIVR_NEWS_REVIEW_IDENTITY=/private/reviewer.key
python3 scripts/eval/news_review.py --report .scratch/news-quality.json \
  --sample /private/review.csv.age --reviewed /private/review-completed.csv \
  --out .scratch/news-quality-reviewed.json
```

The importer checks ciphertext/plaintext hashes, rejects edited sample rows and requires all 100 human grades.
It publishes only the exact-grade agreement rate and counts. Synthetic runs and unreviewed builds remain identified.
The real corpus, provider build, recorded current-system baseline and 100 human decisions are coordinator work.

## Next

- [Measure the working set](evaluation.md) on the trusted measurement runner.
- Use a separate confirmation runner for the frozen held-out version; share only aggregate scores with agents.
