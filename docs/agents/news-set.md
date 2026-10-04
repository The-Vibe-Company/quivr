# Build a private French news search set

Use the builder to turn news articles from RSS feeds into an encrypted search evaluation set.
You get working/held-out sets, a human review sheet and an aggregate quality report.

## Prerequisites

- Python 3.12+, `age` and `age-keygen` (`python3 --version`, `age --version`).
- Install `scripts/eval/requirements{,-direct,-news}.txt`; for the live factory, run `python3 -m pip install --no-deps -e sdks/python -e plugins/jev-rerank`.
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
age-keygen -o .scratch/news-demo/review.key
export QUIVR_NEWS_WORKING_RECIPIENTS="$(age-keygen -y .scratch/news-demo/working.key)"
export QUIVR_NEWS_HOLDOUT_RECIPIENTS="$(age-keygen -y .scratch/news-demo/confirmation.key)"
export QUIVR_NEWS_REVIEW_RECIPIENTS="$(age-keygen -y .scratch/news-demo/review.key)"
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
A `cluster` groups distinct related articles for multi-article questions; `story_id` groups versions of the same dispatch.
After version grouping, a multi-article cluster needs at least two selected articles. Setting `cluster` to `story_id`
leaves one article per cluster and fails generation. Omit `cluster` to sample within calendar months.

Use the built-in [`news_providers.py`](../../scripts/eval/news_providers.py) factory,
or a trusted Python file outside Git exporting `providers()` returning
[`Providers`](../../scripts/eval/news_set.py). The contracts are:

| Adapter | Contract |
| --- | --- |
| Generator | `generate(sample, kind, count, rng)` returns `Question` objects, source ids and evidence dates. |
| Retriever | `search(question, corpus, limit)` returns distinct article ids, in ranking order. |
| Judge | `grade(question, candidates)` returns every candidate's grade, 0–3, or `None` for an isolated content-filter refusal; exposes `family`. |
| Storage | `put` creates immutable objects, `get` reads commit manifests, `delete` cleans only aborted attempt objects. |

Generators receive sampled articles, or up to three related articles for multi-article questions.
Generate natural French newsroom searches, with no copied title or paraphrase content keywords.
Use the six types `entity`, `event`, `recent`, `paraphrase`, `multi_article`, `no_answer`.
Questions must cite evidence from their sample, with its latest date; no-answer questions cite no evidence.
The builder deduplicates normalized text, filters copies and fills equal type quotas with bounded attempts.

Live adapters provide retrievers named `bm25`, `e5_small`, `cohere_pro`, `hybrid`.
Set `Providers.baseline` to the key for the current system; it is not a fifth retriever.
Each contributes up to 10 candidates, giving at most 40 articles per question. Retained candidates receive three complete, independent judgments. Use `JevJudge` with the existing [Jev client](../../plugins/jev-rerank/jev_rerank/client.py)
and two chat judges from distinct families on approved OpenAI-compatible endpoints. Jev supplies binary relevance probabilities: its adapter maps probability quartiles to 0–3.
This is a proxy for ordinal relevance; the report records `jev_grade_mapping: probability_quartiles`. Jev batches the full pool within its byte/token bounds, sharing one deadline and cost allowance.
The other judges use 0 unrelated, 1 marginal, 2 partial answer, 3 direct answer. Treat source/query text as untrusted; adapters must bound cost before each provider attempt and never log input or secrets.

## Select and group dispatches

Exports may include `source`, `credit`, `story_id` and `updated_at` (a timezone-qualified timestamp).
The optional `articles` configuration controls selection before generation and retrieval.
For example, not run with real exports, to keep one operator-defined credit:

```json
{"articles":{"filter":{"field":"credit","values":["wire"]},"group_versions":true,
"near_duplicate_threshold":0.9,"near_duplicate_window_hours":48,
"representative":"latest","max_previous_versions":3}}
```

Merge this section into the full provider configuration; `filter.field` is `source` or `credit`.
Matching is exact; missing fields do not match. One filter is supported; combine fields during export if needed.
The default filter keeps all articles.
`group_versions` groups equal nonempty `story_id` values. Near-duplicates use normalized five-word
shingle-set Jaccard similarity (intersection size divided by union size) within the time window; zero disables it.
Check story ids and repeated text in the private export before a paid run; the heuristic can merge distinct stories
or miss rewritten updates. The longest text is a length proxy, not a guarantee of factual completeness.
At least two selected articles must remain. Operators should prefer explicit story ids when available.
`latest` selects by update timestamp (publication time when absent); `complete` selects the longest text,
then the newest timestamp. An older complete selection carries the latest known story timestamp,
so judges do not treat its text as the latest state. Each story contributes one selected version to every retrieval system.
The generator sees up to three prior versions as context and cites only the selected version's id.
Recent/event prompts ask about datelines, latest developments and follow-ups that the selected text supports.
Judges receive the selected version and update time, and apply the same rubric to all dispatches.
The six question types and 60/40 split remain unchanged; grouping reduces repeated versions in both sets.

## Configure live adapters

The coordinator exports articles through their deployment API into a private folder.
For example, not run with a real export, one JSONL row is:

```json
{"id":"article-1","title":"A ferry opens","text":"The ferry sails Monday.","published_at":"2026-01-02"}
```

Create a private configuration using the factory CLI. For example, not run against private storage:

```sh
python3 scripts/eval/news_providers.py --write-example /private/news-providers.json
```

Edit the generator and two judges' `model` and judge `family`, token/USD caps and contracted prices.
The template prices are placeholders. Set `baseline` to the current retrieval system you want to measure. `build_max_usd` limits the sum of generation, two chat judges and Jev caps; retrieval has a separate cap.
`endpoint_env` and `key_env` name environment variables, never literal endpoints or secrets.
The template uses `NEWS_ENDPOINT`/`NEWS_KEY` for chat, `AZURE_FOUNDRY_ENDPOINT`/`AZURE_FOUNDRY_KEY`
for Cohere and `TYPESAFE_API_KEY` for Jev. Provision their values through your private runner environment.
For Azure AI Foundry, set the chat endpoint variable to the resource URL followed by `/openai/v1`;
the adapter appends `/chat/completions` and sends the key in `api-key`.
For Bearer-token endpoints, set `auth_header` to `bearer`; the default is `api-key`.
Configure `output_token_field` as `max_completion_tokens` (default) or `max_tokens` for the endpoint.
Chat attempts reserve final UTF-8 request bytes plus framing and the maximum completion, at separate rates.
429/5xx retry at most twice by default, with at most ten seconds between attempts. HTTP 400 `content_filter`
rejects a generator batch and resamples. Chat judges split refused batches to isolate filtered candidates;
`None` means filtered, never grade zero. Set top-level `max_filtered_candidate_share` (default `0.1`, range `(0, 1]`).
Divide candidates filtered by any judge by the original pool size. A question drops at that limit, or if filtering
removes every positive majority grade for an answerable question. Below the limit, filtered candidates leave the pool. Dropped questions
are replaced within generation's existing attempt bound; count, type balance and spend caps still apply.
Failed attempts keep their reservations as an upper bound, not a claim about actual provider billing.
Malformed generator batches resample; malformed judge batches retry within configured bounds. Large pools are batched within input bounds.
Jev reserves all three client attempts before calling it. Usage that exceeds a reservation stops the adapter.

Retrieval reuses the direct comparison's pinned local E5, Cohere client, token/USD gate and character windows.
Install the direct requirements before the live run; E5 downloads its pinned model on first use.
BM25 is indexed once; vectors and query embeddings are cached only in memory for this build.
Hybrid uses weighted reciprocal rank fusion of BM25 and Cohere top-ten lists (`dense_weight`, default 0.5).
This local comparison baseline is not a measurement of a running Quivr deployment.

For example, not run with private keys, articles or credentials; use fresh output paths on every attempt:

```sh
umask 077
age-keygen -o /private/working.key
age-keygen -o /private/confirmation.key
age-keygen -o /private/review.key
export QUIVR_NEWS_WORKING_RECIPIENTS="$(age-keygen -y /private/working.key)"
export QUIVR_NEWS_HOLDOUT_RECIPIENTS="$(age-keygen -y /private/confirmation.key)"
export QUIVR_NEWS_REVIEW_RECIPIENTS="$(age-keygen -y /private/review.key)"
export QUIVR_NEWS_CONFIG=/private/news-providers.json
python3 scripts/eval/news_providers.py --config "$QUIVR_NEWS_CONFIG" --questions 1500
python3 scripts/eval/news_set.py --articles /private/articles \
  --providers scripts/eval/news_providers.py --questions 1500 \
  --storage-dir /private/news-volume --report .scratch/news-quality.json \
  --usage-report .scratch/news-usage.json
```

The estimate is offline: it shows worst bounded chat attempts, pool size and spend ceilings. Worst-case estimates can exceed the caps; caps stop calls and do not guarantee 1,500 accepted questions.
The report includes aggregate provider usage; `--usage-report` also saves usage after a failed build.
Its `content_filter` block counts generation batches, judged query/candidate pairs (including dropped questions),
filtered batches/candidates, their shares, dropped questions and the configured limit. A candidate filtered
by multiple judges counts once per question. Provider usage counts every refused HTTP attempt, including splits.
Failures print only exception class, build phase, HTTP status and known provider code when available; messages, text, URLs and keys are omitted.
Use `confirmed_cost_usd` for confirmed usage and `cost_upper_bound_usd` for confirmed plus unknown reservations. The `totals` block sums all provider spend and records the generation/judging and separate retrieval ceilings.
Always supply the ledger path for a paid build: a failed build otherwise has no saved spend report.
Counts and phases go to stderr; exceptions and provider bodies are sanitised. Keep keys, configuration, exports and encrypted artifacts in private storage, outside Git.

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
