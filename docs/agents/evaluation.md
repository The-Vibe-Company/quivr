# Measure search quality

`make eval` scores how well Quivr search ranks results on evaluation sets with human or
generated judgements. For each set it ingests the documents into a new Corpus through the
public API, waits until every Record has its vectors, runs every query in each search mode
(`lexical`, `semantic`, `hybrid`) and each profile the API serves, and reports nDCG@10,
Recall@10, MRR@10, latency and paid calls per query. Only harness or dependency errors fail a run.

## Run it

```sh
python3 -m pip install -r scripts/eval/requirements.txt
make eval                                   # every public set, on an isolated local stack
make eval args='--sets scifact'             # one set
make eval args='--baseline <report.json>'   # also compare with an earlier run, query by query
make eval args='--sets miracl-fr --compare-to main'  # also measure main, on this machine
```

The local stack needs Linux x86_64. For an existing installation, use `--api-url <url>` with a key granting `corpora:read`, `corpora:write`, `content:read`, `content:write`, `changes:read` and `search:query` in
`QUIVR_EVAL_API_KEY`. Each run creates new Corpora and never deletes them.

Reports go in `.scratch/eval/runs/<time>/` (`--out` overrides it); downloads in `.scratch/eval/cache`.

## Compare an evaluation plugin

On an existing installation with `ingestion.evaluation` configured, add
`--evaluation-plugin <plugin-id> --evaluation-space <space-id>` to `--api-url`.
Both fields are required together. For a local stack, use `--ingestion-config <pin.json>`
instead. That JSON holds `manifest` (relative to the JSON), `endpoint`, `configuration`,
`plugin`, `space` and `secret_names` (environment names only). Run the plugin yourself
with those secrets injected; the engine receives no secret values. The lane pins it beside
`core.ingest` under `ingestion.evaluation.text/plain` before creating Corpora. Each owner's
primary space keeps role `served`. This excludes `--api-url` and `--compare-to`.
The lane waits for that space to cover every
current document, then measures its default profile beside the served default
in all three modes on the same Corpus. Systems carry the evaluation plugin and
space ids; scoring deduplicates by Record, so different segmentation offsets
compare at document level. All three metrics also get a paired comparison against
core.ingest in the same mode. These options exclude live paid reranker evaluation.

## The public sets

The default suite contains five French sets (`miracl-fr`, `mldr-fr`, `xpqa-fr`,
`webfaq-fr`, `mkqa-fr`) and five English sets (`scifact`, `fiqa`, `trec-covid`,
`arguana`, `scidocs`). Only default sets count toward promotion gates.
`python3 scripts/eval/run.py --list-sets` prints pinned versions, licences and sizes offline.
The [registry](../../scripts/eval/public_sets.json) records each licence check and SHA-256.

`--include-restricted` opts into Alloprof and BSARD (CC BY-NC-SA 4.0) and NFCorpus
(academic-only). Use these only when your purpose meets their terms. Their reports have
`promotion_eligible: false`; they are diagnostics and stay out of gates. Data downloads
at run time into the ignored cache; never commit or redistribute restricted data.
Syntec remains excluded because its licence is unknown; mMARCO's inherited terms remain unresolved.

Every sample uses a fixed seed and retains all available judgments for its selected
queries. Distractors come from listed hard negatives first, then a seeded random draw.
Documents over `max_source_bytes` are excluded. Changing sources, size or seed changes
the sample fingerprint. Sampled scores are usually higher than full-corpus scores;
compare only runs with identical fingerprints. TREC-COVID samples five topics to keep
its deep judgments within 10,000 documents; that alone cannot establish significance.

## Add a set

A private set, for example one judged by an evaluation partner, stays out of this
repository. Give it in the TREC layout that `scripts/eval/trec.py` reads, as a directory
or a `.zip` / `.tar.gz` archive:

- `corpus.jsonl`: `{"_id": "d1", "title": "…", "text": "…"}` per document (title optional);
- `queries.jsonl`: `{"_id": "q1", "text": "…"}` per query;
- `qrels.tsv`: `query-id`, `corpus-id`, `score` per judgement (grades 0, 1, 2…; a header
  line is optional), or the 4-column TREC form `q1 Q0 d1 1`.

Run it with `make eval args='--sets "" --private <dir|archive|URL> --private-sha256 <hex>'`;
a URL needs its sha256. Set `QUIVR_EVAL_SET_AUTHORIZATION` for an authenticated download.
Reports hold identifiers and scores, never texts.

A new public set needs a licence that allows this use, checked at its source; add it to
`public_sets.json` with pinned files, a converter and a sample size, and record the
licence on its ticket.

## Read the report

- **Systems** are `mode/profile`. Profiles `GET /v0/search/profiles` lists but the API
  refuses are listed as not served. `hybrid/default`, the API default, is the baseline within
  a run. Baseline comparisons require identical system names; profile names are not remapped.
- **nDCG@10** uses linear gain (gain = grade) and a log2(rank + 1) discount, as trec_eval's
  `ndcg_cut_10`; the report states it. Hits are deduplicated by Record before scoring.
- **Δ** is the mean per-query difference with the p-value of a two-sided paired t-test;
  `*` marks p < 0.05, without correction for the number of comparisons. A hundred queries
  reliably detect only large differences (about 0.1 nDCG@10); 0.05 needs several hundred.
- **Against the baseline run**: the same system in an earlier run, only for sets whose
  sample fingerprint is identical.
- **Failures** are searches the API refused or could not answer; they score 0. Two `mldr-fr`
  queries are over the default profile's 256-token limit: they always fail, `query_too_long`.
- **Latency** is client wall time per search (p50, p95), one search at a time.
  **Paid calls** is 0 with only core plugins, and unknown (`—`) for unmanaged evaluation
  owners or `--api-url`. Hosted campaigns count attempts at the gate. **Ingestion to vectors** is the time from the first submission until
  every Record has its vectors.
- **Where search time goes** splits it by the engine's `usage.phases`, at limit 50 and, in a
  second pass timed only, limit 10. Encoding share is the part of the engine's time spent
  encoding queries: the most a query-vector cache could save.
- **Machine resources** (local stack): disk and memory after each step. A run stalled by Weaviate's
  90% disk guard quotes its switch to read-only, kept whole in `weaviate-full.log`.

## Compare speed before and after a change

Use `--compare-to <ref>` to measure both revisions on the same machine, alternating
query calls. It reports score and latency changes. Ingestion runs twice, so start with one set.

## Measurement machines and CI

Measurements run on your machine or the coordinator's compute, never GitHub Actions.
The `Search quality` manual workflow checks tiny fixtures and prints the default registry;
it downloads no benchmark data and makes no model/provider calls. Measurement commands refuse CI execution.

## Compare embeddings directly on your machine

Use `scripts/eval/direct_bakeoff.py` to compare dense embeddings without an engine or vector database. It uses public samples, exact cosine top ten and repository scoring,
with e5-small as the paired baseline. Long documents use the
reference windows: 1,800 characters for e5 and 6,000 for hosted models, with 200-character overlap. A document's best piece wins. The local encoder can truncate at its token limit.

Use Python 3.11+ and a virtual environment. The first run downloads a pinned `intfloat/multilingual-e5-small` revision.
Hosted runs need Azure AI Foundry serving the requested deployments, with its endpoint and key in environment
variables `AZURE_FOUNDRY_ENDPOINT` and `AZURE_FOUNDRY_KEY`. Keep keys out of Git. Paid measurements run locally; this command refuses CI execution.

For example, not run here with a provider key:

```sh
python3 -m venv .scratch/eval/venv
.scratch/eval/venv/bin/pip install -r scripts/eval/requirements-direct.txt
.scratch/eval/venv/bin/python scripts/eval/direct_bakeoff.py --set miracl-fr \
  --max-input-tokens 5000000 --max-usd 1 \
  --out .scratch/eval/direct/miracl-fr.json
```

Defaults: Cohere Pro/Fast and OpenAI 3-large. Add `--models Cohere-Embed-V5-Pro-1024 Cohere-Embed-V5-Pro`
for the dimensions comparison,
or `--models 'multilingual-e5-small (current)'` for an unpaid baseline. Run each
public set separately; token and USD caps apply across every model in one command.

Every provider attempt, including retries, is blocked before sending if it would exceed either cap.
It reserves one token per UTF-8 byte plus eight per input,
using the shared [budget guard](../../scripts/eval/embeddings.py). Failed or unknown calls stay reserved;
valid usage releases unused reservations; retries reserve again.
Prices are the 2026-10-03 estimates ($0.12/$0.08/$0.13 per million input tokens
for Pro/Fast/OpenAI); override them with `--price MODEL=USD_PER_MILLION` when needed.
These bounds use list prices, not billing receipts. The JSON records both confirmed
and reserved spend, sample fingerprint, settings, completed scores and timings.
`complete` exits 0; `capped` or `failed` exits 2 and retains completed models. Query timing is batch encoding time per query, not engine search latency.

Outputs refuse overwrites. Keep public measurements and summaries in a new dated evidence folder,
with `Date:` and `Status:` lines. Never commit query/document text or secrets. Preserve merged evidence. The [2026-10-03 comparison](../dated/evidence/2026-10-03-embedding-comparison/summary.md)
preserves the original results and their limitations.

## Inspect a set's quality

After a local baseline, run (example paths):

```sh
python3 scripts/eval/quality_reports.py --set xpqa-fr \
  --e5-run .scratch/eval/local-runs/xpqa-fr.json --out .scratch/eval/quality
```
The JSON and short Markdown hold judged depth, query lengths, heuristic question/statement
mix, source limitations and e5 saturation. Judge agreement and manual intent labels are
unavailable where the source supplies none. Joint saturation is the share both systems score nDCG@10=1.
It stays `null` until `--cohere-run` supplies a complete `direct_bakeoff.py` Cohere Pro JSON with matching queries/fingerprint.
Outputs refuse overwrites. Restricted reports contain aggregates only and remain ineligible
for gates. The [dated suite report](../dated/evidence/2026-10-03-public-sets/summary.md)
records local scores, licence exclusions, and per-set hosted commands with caps totaling under $20.
