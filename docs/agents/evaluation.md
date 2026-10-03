# Measure search quality

`make eval` scores how well Quivr search ranks results on evaluation sets with human or
generated judgements. For each set it ingests the documents into a new Corpus through the
public API, waits until every Record has its vectors, runs every query in each search mode
(`lexical`, `semantic`, `hybrid`) and each profile the API serves, and reports nDCG@10,
Recall@10, MRR@10, latency and paid calls per query. It is a measurement, not a test: scores
never fail a run, only harness or dependency errors do.

## Run it

```sh
python3 -m pip install -r scripts/eval/requirements.txt
make eval                                   # every public set, on an isolated local stack
make eval args='--sets scifact'             # one set
make eval args='--baseline <report.json>'   # also compare with an earlier run, query by query
make eval args='--sets miracl-fr --compare-to main'  # also measure main, on this machine
```

The local stack needs Linux x86_64, like `make measure`; it runs the engine with only its
core plugins pinned, core.ingest and core.retrieve. To measure an existing installation instead, pass `--api-url <url>` and put a key
with `corpora:read`, `corpora:write`, `content:read`, `content:write`, `changes:read` and `search:query` in
`QUIVR_EVAL_API_KEY`. Each run creates new Corpora and never deletes them.

The report is written to `.scratch/eval/runs/<time>/report.md` and `report.json` (`--out`
changes the folder). Downloads are cached in `.scratch/eval/cache`.

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

| Set | Language | Sample (queries / documents) | Licence |
| --- | --- | --- | --- |
| `miracl-fr` | French | 343 / 5,000 Wikipedia passages | Apache-2.0; passages CC BY-SA 4.0 |
| `mldr-fr` | French | 200 / 600 long documents | MIT |
| `scifact` | English | 300 / 2,000 scientific abstracts | claims CC BY 4.0, abstracts ODC-By 1.0 |

`scripts/eval/public_sets.py` pins every file by URL and sha256 and records each licence
and where it was checked. Every judged query is kept; the documents are sampled so that a
nightly run fits a 2-CPU runner (about 90 minutes, mostly ingestion; `mldr-fr` alone takes
40): every judged document is kept, and a fixed seed picks distractors for the rest (the
source's own hard negatives first when it lists them). Documents over the engine's
`max_source_bytes` are left out. Sampled corpora are much smaller than the
originals, so scores are higher than published full-corpus numbers; compare runs with each
other, not with papers.

NFCorpus (academic use only), mMARCO, FQuAD, BSARD and Alloprof are not used: their
licences do not allow it.

## Add a set

A private set, for example one judged by an evaluation partner, stays out of this
repository. Give it in the TREC layout that `scripts/eval/trec.py` reads, as a directory
or a `.zip` / `.tar.gz` archive:

- `corpus.jsonl`: `{"_id": "d1", "title": "…", "text": "…"}` per document (title optional);
- `queries.jsonl`: `{"_id": "q1", "text": "…"}` per query;
- `qrels.tsv`: `query-id`, `corpus-id`, `score` per judgement (grades 0, 1, 2…; a header
  line is optional), or the 4-column TREC form `q1 Q0 d1 1`.

Run it with `make eval args='--sets "" --private <dir|archive|URL> --private-sha256 <hex>'`;
a URL needs its sha256. The nightly lane measures a private set when the repository secrets
`EVAL_SET_URL` and `EVAL_SET_SHA256` are set, and sends `EVAL_SET_AUTHORIZATION`, when set,
as the download's `Authorization` header. Reports hold identifiers and scores, never texts.

A new public set needs a licence that allows this use, checked at its source; add it to
`SETS` in `public_sets.py` with pinned files, a converter and a sample size, and record the
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

Latency differs between runners, even with the same CPU model, by more than most changes.
`--compare-to <ref>` (a branch, tag or full commit SHA) checks it out in a worktree and starts
a second local stack from it in the same run; each set is ingested into both, and each query
is searched on both, alternating which goes first. The report opens with base, branch and
Δ p50 and p95 per system, under the CPU model, and compares scores with the base query by
query. Ingestion takes twice as long, so one set is usually enough.

## The lane

The `Search quality` workflow (`.github/workflows/measure-search.yml`) runs nightly and on
demand, never on pull requests: `gh workflow run measure-search.yml --ref <branch>`,
optionally with `-f baseline_run_id=<run>`; by default it compares with the latest successful
run on `main`. `-f compare_to=main -f sets=miracl-fr` compares in one job instead. Its artifact
`search-quality` holds `report.md`, `report.json` and the stack logs; the report is on the run page.

## Compare hosted embeddings with a cap

The coordinator dispatches `Embedding quality` (`.github/workflows/measure-embeddings.yml`)
after the harness merges. It never runs on PRs or schedules. The bakeoff measures
Cohere-Embed-V5-Pro/Fast (2048 dimensions) and text-embedding-3-large (3072), paired
against core.ingest on all three public sets, in semantic and hybrid modes. Fusion stays
at core.retrieve's default. Prepare it offline with no key or provider request:

```sh
python3 scripts/eval/campaign.py --dry-run --max-input-tokens 15000000 --max-usd 8
```

Illustrative paid dispatch, run only by the coordinator: `gh workflow run measure-embeddings.yml
--ref main -f campaign=bakeoff -f max_input_tokens=15000000 -f max_usd=8`.
Expected input is 4–5 million tokens per model; every retry and query call also counts.
The runner keeps the key in its forwarding gate; hosted.embed gets only a loopback URL.
Before each attempt, the gate atomically reserves one token per UTF-8 byte plus eight
special tokens per input. Valid provider usage releases unused reservations; unknown,
failed or timed-out calls remain reserved. A token or USD cap stops forwarding and writes
status `capped` with completed cuts, unfinished indexing and spend. Missing cuts have no scores.

Then dispatch `campaign=winner`, `winner=<measured deployment>`, `prior_run_id=<bakeoff run>`,
`max_input_tokens=5000000`, `max_usd=2`. The completed bakeoff's upper-bound spend is
subtracted from $10; admission refuses a per-run USD cap above the remainder.
The smaller SciFact matrix measures segment limits 512/2048 with full/1024 dimensions.
The token sizes are conservative byte estimates, as described in the
[hosted plugin reference](../../plugins/hosted-embed/README.md).
The `embedding-quality` artifact holds report JSON/Markdown: deployment, dimensions,
segment limit, paired quality, query p50/p95, paired indexing time, confirmed/reserved
input tokens, indexing USD per 1,000 documents and total USD. Reports link the dated list
price sources; estimates are not Azure billing receipts. Secrets AZURE_FOUNDRY_KEY and
AZURE_FOUNDRY_ENDPOINT enter only the measurement step. Private sets are excluded.
