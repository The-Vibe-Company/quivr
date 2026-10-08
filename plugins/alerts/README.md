# alerts

Quivr's first-party alert rules, a `subscription` plugin built with the
[Python Plugin SDK](../../sdks/python/README.md). A Subscription pinned to
`{"plugin_id": "alerts", "version": "0.4.0"}` gets a Match when a new article
satisfies its Saved Query's expression. It requires Plugin API `>=0.10.0 <0.11.0`
and offers these alert kinds:

- **`keywords`**: a boolean keyword query over the article's title and body, with
  optional filters on its metadata;
- **`meaning`**: a plain-language description checked against stored vectors,
  entirely locally, with required `meaning_check: "vectors"`;
- **`described`**: the existing Jev check ([below](#described-alerts)). Omitted
  `meaning_check` or `"jev"` sends text to TypeSafe; `"vectors"` selects the local
  check where the installation permits this kind;
- **`keywords_or_meaning` / `keywords_and_meaning`**: a keyword tree combined with
  a description, with an explicit `meaning_check: "vectors"` or `"jev"`.

People writing alerts should start with the guides:
[keyword alerts](https://docs.quivr.thevibecompany.co/guides/keyword-alerts) and
[described alerts](https://docs.quivr.thevibecompany.co/guides/described-alerts). This README is the reference.

## Expression

A keyword alert's Saved Query expression:

```json
{
  "kind": "keywords",
  "match": {"all": [
    {"term": "Acme"},
    {"any": [{"term": "grève"}, {"term": "strike"}]},
    {"not": {"term": "sport"}}
  ]}
}
```

`match` is a tree of nodes. Each node is an object with exactly one of these shapes:

| Node | Satisfied when |
| --- | --- |
| `{"term": "Acme"}` | The word, or the exact phrase (`{"term": "city book fair"}`), appears in a searched Part |
| `{"field": "author", "equals": "Jane Doe"}` | The article's metadata field has this value ([fields](#field-filters)) |
| `{"all": [nodes]}` | Every node is satisfied |
| `{"any": [nodes]}` | At least one node is satisfied |
| `{"not": node}` | The node is not satisfied |

Bounds, which the schema enforces:
- `all`, `any` and `not` nest at most 6 levels deep;
- a group holds 1 to 64 nodes;
- a term or a value is at most 256 characters, and a term is not blank.

The core validates every Saved Query pinned to this plugin against the declared
`expression_schema` when the Subscription is created or versioned. A malformed tree is
`422 invalid_expression`, and the message names the JSON Pointer at fault.

### Why a structured expression, not a query string

The Plugin API lets the core validate an expression only against the plugin's
JSON Schema, when the Subscription is created. A query string such as
`"Acme" AND (grève OR strike)` cannot be checked by a schema: a typo would be
accepted, and the saved search would then either never alert or fail at every
article. The structured tree is checked in full at creation, so a mistake is a 422
that the user sees at once. It is also unambiguous: it has no operator precedence to
misread. And it maps directly onto an "all of / any of / none of / exact phrase" form.

People still write queries as text. The [notation](#text-notation) below
translates to the tree, and the plugin ships its reference parser.

## Matching

- **Parts.** Terms search the Parts whose role is in `text_roles`, which is `title` and
  `body` by default. Other roles, such as captions, are ignored.
- **Folding.** Text and terms are compared folded:
  - compatibility forms are decomposed (NFKD);
  - accents are removed;
  - `œ`, `æ` and a few other ligatures are spelled out;
  - case is folded.

  So `greve` finds "Grève", and `oeuvre` finds "Œuvre".
- **Whole words.** A word is a run of Unicode letters and digits. Everything else
  separates words: spaces, punctuation, apostrophes and hyphens. So:
  - `me` does not match "Acme";
  - `salarié` does not match "salariés", because there is no stemming;
  - `Acme` matches "l'Acme".
- **Phrases.** A term of several words matches them consecutively and in order, so
  `Saint-Denis` also finds "Saint Denis". A term without any letter or digit never
  matches.
- **`not`** is evaluated like any other node. A query made only of exclusions matches
  every article that lacks them.

## Field filters

`field` is a field name or a JSON Pointer into the article's metadata (`/source/…`,
`/provenance/…`, `/extensions/…`, `/accepted_at`). The values compare like text:
- folded;
- as whole values, with runs of spaces collapsed;
- a number or a boolean compares by its JSON form;
- a list matches when one of its elements does.

A filter on its own is a valid alert, for example "every new article from this
source".

Built-in names read the metadata Quivr sends with every article:

| Name | Pointer |
| --- | --- |
| `source` | `/source/namespace` |
| `producer` | `/provenance/producer` |
| `origin` | `/provenance/origin` (`client` or `connector`) |
| `connector` | `/provenance/connector/instance_id` |
| `connector_kind` | `/provenance/connector/kind` (for example `rss`) |

Other names, such as `author` or `category`, depend on where your sources put that
metadata, so the installer maps them in the plugin configuration (below). An unmapped
name has no value: its filter is never satisfied, and the plugin logs a warning.

## Configuration

**Installer configuration**: the plugin pin's `configuration` in the core's startup config.

| Field | Default | Meaning |
| --- | --- | --- |
| `fields` | `{}` | Name → JSON Pointer, added to or replacing the built-in names. Names match `[a-z][a-z0-9_]*` |
| `text_roles` | `["title", "body"]` | Part roles that terms search and Jev sees; vectors uses all supplied Part vectors |
| `described.threshold` | `0.5` | Default Jev score threshold, 0.2 to 0.95 |
| `vectors.thresholds` | required for local checks without a Subscription override | Map of vector-space IDs to calibrated cosine thresholds, 0.2 to 0.95 |

```json
{"manifest": "plugins/alerts/quivr-plugin.yaml", "endpoint": "http://127.0.0.1:9910",
 "configuration": {"fields": {"author": "/extensions/example.news/data/author",
                              "category": "/extensions/example.news/data/categories"}}}
```

**Subscription configuration** (`evaluator.configuration`):

| Field | Default | Meaning |
| --- | --- | --- |
| `wait_for_enrichment` | `false` for keywords, `true` for meaning checks | Answer `not_ready` until enriched. A decisive mixed-mode keyword check bypasses this wait. The core asks again on `record.enrichment_available` |
| `threshold` | `described.threshold` for Jev, `vectors.thresholds[vector_space_id]` for vectors | Inclusive meaning-check threshold, 0.2 to 0.95. Keyword alerts ignore it |

Rules run when an article becomes searchable. Keyword alerts need no enrichment, so
they decide at once by default. If a deployment never enriches articles, a Subscription
with `wait_for_enrichment: true` never decides.

**Pin `kinds`** (core startup config): the alert kinds this installation accepts.
For a local-only installation, pin `"kinds": ["keywords", "meaning",
"keywords_or_meaning", "keywords_and_meaning"]`. This leaves `described` out;
use `meaning` for a local description. A kind allowlist alone cannot restrict
the backend of a mixed expression: configure clients to select `vectors`.
An unresolved mixed Jev check without `TYPESAFE_API_KEY` returns
`described_unavailable`; it does not silently switch backends. Absent `kinds`,
all kinds are accepted, but Jev still needs the key.

**Secret:** `TYPESAFE_API_KEY`, read from the plugin's environment only and declared
in the manifest with `required: false`. `TYPESAFE_API_URL` optionally replaces the
System One endpoint. HTTPS is required except for numeric loopback test servers.

## Local meaning checks

This expression uses only stored embeddings, numeric summaries of the article
and description, without instantiating or calling Jev:

```json
{"kind": "meaning", "description": "Labour strikes at ports and harbours", "meaning_check": "vectors"}
```

`description` has the same bounds as described alerts. Optional `sources`
restricts the whole evaluation before either keyword or meaning checks. The
manifest opts into `subscription.vectors` with `parts: true`, `query: true` and
`query_text_pointer: "/description"`. Its `query_expression_schema` selects only
expressions with an explicit `meaning_check: "vectors"`, so only vector-backed
descriptions are embedded. Keyword expressions and default or explicit Jev
expressions do not require the query encoder. Every meaning expression places
its description at that pointer. The core owns query encoding and article
enrichment.

| Request field | Meaning |
| --- | --- |
| `record.vector_space_id` | Identity of the article's vector space |
| `record.vectors_ready` | Whether article vectors are available |
| `record.parts[].vectors[]` | Stored segments, each with `segment_id` and numeric `vector` |
| `evaluations[].query_vector` | Optional saved query vector with `vector_space_id` and numeric `vector` |

The maximum cosine similarity across all valid Part segments decides the
evaluation. Query and article spaces must match exactly. Missing, unready or
mismatched vectors return `not_ready`, even with `wait_for_enrichment: false`.
Zero, nonfinite and dimension-mismatched vectors cannot match; invalid segments
are skipped, and no valid comparable segment means `not_ready`.

A match names the winning Part in `evidence.part_keys`. Its details contain
`kind`, `meaning_check: "vectors"`, `similarity`, `threshold`, `vector_space_id`
and `segment_id`. The unrounded cosine decides; evidence rounds it to six
decimal places. A negative result explains the best similarity and threshold.

Local checks have no implicit threshold. Set `vectors.thresholds` on the
plugin pin, keyed by the exact vector-space ID supplied in the request, or set
each Subscription's `threshold`. For example, not run:

```json
{"vectors": {"thresholds": {"<vector-space-id>": 0.65}}}
```

A ready comparison without either setting fails with `vector_threshold_required`.
The [calibration notes](calibration/README.md) suggest 0.80 for the measured E5
space and 0.65 for the measured 768-dimensional EmbeddingGemma 2 space. Both
retain all 16 labeled positives in this small bilingual set, while matching
11 and 6 of 140 negatives respectively. Tune the threshold on your own traffic;
changing weights, dimensions, templates or segmentation requires recalibration.
The core re-evaluates `not_ready` after enrichment and owns Match uniqueness; this
plugin stores no delivery or deduplication state.

## Mixed keyword and meaning checks

Both mixed kinds require `match`, `description` and `meaning_check`:

```json
{"kind": "keywords_and_meaning", "match": {"term": "dockers"},
 "description": "Labour strikes at ports and harbours", "meaning_check": "vectors"}
```

| Kind | Early keyword decision | Otherwise |
| --- | --- | --- |
| `keywords_or_meaning` | A positive tree returns `match` | Meaning decides, or returns `not_ready` |
| `keywords_and_meaning` | A negative tree returns `no_match` | Meaning decides, or returns `not_ready` |

Early decisions need neither enrichment, query vectors nor a classifier key.
An OR keyword match carries keyword evidence and `matched_by: "keywords"`.
An AND match combines the meaning evidence with `details.keywords` and the
supporting Part keys in article order. Mixed Jev evaluations that need meaning
share the existing described batch call; local evaluations never join it.

## Decisions and evidence

- **Decisions.** Each evaluation answers `match`, `no_match` or `not_ready`. The
  decision depends only on the article, the expression and the configurations, so
  batches can be deduplicated and replayed.
- **Evidence.** A `match` carries evidence, which Quivr stores with the Match and
  exposes through `GET /v0/matches/{id}`. It names the terms and field values that
  support the match, taken from the satisfied branches, never from under a `not`:

```json
{
  "explanation": "Matched \"Acme\" in title, body; \"grève\" in body.",
  "part_keys": ["title", "body"],
  "details": {
    "kind": "keywords",
    "terms": [{"term": "Acme", "part_keys": ["title", "body"]}, {"term": "grève", "part_keys": ["body"]}],
    "fields": []
  }
}
```

- **Field filters.** A matched filter reads `Matched source "wire".` and appears in
  `details.fields` as `{"field": "source", "value": "wire"}`.
- **Exclusions only.** A match that rests only on exclusions explains
  `Matched: none of the excluded terms appear.` and carries no `part_keys`.
- **Bounds.** Terms appear in query order. `part_keys` follows the article's Part
  order, with at most 100 keys. `details` stays under 12 KiB; when it is trimmed,
  `details.truncated` is `true`.

## Described alerts

```json
{"kind": "described", "description": "Labour strikes at ports and harbours"}
```

Omitted `meaning_check` preserves Jev; explicit `"jev"` is equivalent. Use
`"vectors"` for the [local backend](#local-meaning-checks) when this kind is allowed.

The description has 3 to 1000 characters, with at least one character that is not a
space. An optional `sources` (1 to 64 distinct Source Namespaces) limits the alert to
those sources, compared like the keyword `source` filter: an article from another
source is `no_match` at once, without waiting for enrichment and without a classifier
question for that alert. Each batch is decided as follows.

1. **One call.** All Jev evaluations of the batch that are ready and watch the
   article's source share one
   classifier call. Their descriptions are deduplicated after runs of spaces are
   collapsed, then sorted, so each distinct description is asked once. Evaluations
   that differ only in threshold share their question. The core already
   deduplicates identical expression and configuration pairs across Subscriptions
   and owners. Pure described alerts have no keyword pre-filter.
2. **What the classifier sees** (`state`):
   - `title`: the `title` Parts;
   - `source`: the Source Namespace;
   - every installer-mapped field that has a value (`fields`, such as `author` or
     `category`), with strings bounded to 256 characters and lists to 16 items;
   - `text`: the other `text_roles` Parts in Part order, joined by blank lines.

   The text is cut at a word boundary so the serialized state stays under 48 KB.
   That is far under Jev's budget of 32k tokens for the state plus the longest
   question, and it keeps the start of the article, where news puts the
   essentials.
3. **Jev request.** `POST https://api.typesafe.ai/v1/systemone` with the pinned model
   `jev-1.13.0`, `{"article": state}` as the state, and one
   [Noul](https://docs.typesafe.ai/primitives/noul.md) question per description.
   The question's instructions hold the description as data (`alert`) and ask
   whether the article is about what `alert` describes, in other words or another
   language. A request is split only when its body would exceed 120,000 bytes,
   under TypeSafe's limit of about 128 KB. All batches and retries share a
   15-second deadline, within the declared `timeout_ms` of 20 seconds. The
   shared SDK client makes at most three attempts per batch, retrying 408, 429 and 5xx.
4. **Decision.** `match` when the Noul, the probability of "yes", is at or above the
   threshold; otherwise `no_match`, whose explanation gives the score. TypeSafe's
   `confidence` field is never used. Noul answers do not carry it, and it has no
   separating power for this decision.
5. **Evidence:**

   ```json
   {
     "explanation": "Jev (jev-1.13.0) judged that the article fits the description: score 0.97, threshold 0.50.",
     "part_keys": ["title", "body"],
     "details": {"kind": "described", "classifier": "Jev", "model": "jev-1.13.0", "score": 0.97, "threshold": 0.5, "truncated": false}
   }
   ```

   `part_keys` are the Parts sent, even partly. `truncated` says the text was cut.

**Default threshold 0.5.** It was calibrated on [`calibration/set.json`](calibration/set.json),
13 neutral French and English articles, each judged against 6 descriptions (78
pairs, including near misses such as a strike on the railways, or visa-free
tourism). With `jev-1.13.0` on 2026-09-29:
- the fitting pairs scored 0.94 to 0.99;
- the others scored 0.01 to 0.07;
- every threshold from 0.20 to 0.90 classified all 78 pairs correctly.

0.5 sits in the middle of that gap. The set is small and clear-cut, so tune the
threshold per alert on real traffic. Re-run the calibration with
`python3 calibration/calibrate.py` (it calls TypeSafe with `TYPESAFE_API_KEY`) before
moving to another model version.

**Errors.** The plugin never echoes TypeSafe's response body, and never logs the key.

| Situation | Plugin error | The core |
| --- | --- | --- |
| No `TYPESAFE_API_KEY` and a Jev check is due | `described_unavailable`, terminal | Isolates unavailable evaluations by halving the batch, decides the others, retries these with backoff |
| HTTP 401 or 403 | `classifier_unauthorized`, terminal | Same |
| Other 4xx, or an answer without a valid Noul | `classifier_refused_request`, `classifier_invalid_answer`, terminal | Same |
| Timeout, connection failure, HTTP 408, 429, 5xx or 529 | `classifier_unavailable`, retryable | Retries the whole batch with backoff; keyword evaluations of that batch wait too |

**Replaceable classifier.** `alerts/described.py` depends only on the `Classifier`
protocol: a `name`, a `model` and `judge(state, descriptions) -> {description: score}`.
`alerts/jev.py` implements it. Another classifier, such as an in-house model,
replaces `rule.classifier`, the factory that returns the classifier, or `None`
when described alerts cannot be decided.

## Text notation

`alerts.notation` translates the query text people write into the expression:

```text
"Acme" AND (grève OR strike) NOT sport
author:"Jane Doe" OR source:wire
```

```text
query   := or
or      := and ( "OR" and )*
and     := unary ( [ "AND" ] unary )*      juxtaposed items are all required
unary   := "NOT" unary | primary
primary := "(" or ")" | PHRASE | WORD | FIELD
PHRASE  := '"' characters '"'              \" and \\ escape a quote and a backslash
FIELD   := name ":" ( WORD | PHRASE )      name is [a-z][a-z0-9_]*
```

The notation follows these rules:
- `NOT` binds tighter than `AND`, which binds tighter than `OR`.
- Operators are upper case; `and`, `or` and `not` in lower case are ordinary words.
- `city book fair` without quotes requires the three words anywhere, and
  `"city book fair"` requires the phrase.
- A leading `-` is refused: write `NOT`.
- A word shaped `name:value` is a field filter, unless the value starts with `/`
  (a URL). Quote it to search it as text: `"re:Invent"`.
- Chains of the same operator are flattened, and parentheses around a single item add
  no level.

```bash
python3 -m alerts.notation '"Acme" AND (grève OR strike) NOT sport'   # prints the expression
```

It exits 1 and explains the mistake for an invalid query. From Python, use
`from alerts.notation import parse, NotationError`.

## Run and test

```bash
python3 -m venv .venv && . .venv/bin/activate
pip install -e <quivr checkout>/sdks/python -e .
python3 -m unittest discover -s tests            # grammar, matching, evidence, schema, Jev and local vectors
quivr plugin dev --fixture fixtures/sample.json  # replay the keyword sample batch
quivr plugin test .                              # Contract Runner certification (keyword fixture)
```

Tests never call TypeSafe. Described alerts are tested against
`alerts.fake_system_one`, a deterministic stand-in for System One that judges by
topic words in English and French. To certify both kinds, run it and point the plugin
at it:

```bash
python3 -m alerts.fake_system_one --port 8765 --key test-key &
TYPESAFE_API_KEY=test-key TYPESAFE_API_URL=http://127.0.0.1:8765/v1/systemone \
  quivr plugin test --fixture fixtures/sample.json --fixture tests/data/described.json .
```

One opt-in test calls the real API. It is skipped unless both
`QUIVR_ALERTS_LIVE=1` and `TYPESAFE_API_KEY` are set:
`QUIVR_ALERTS_LIVE=1 python3 -m unittest test_described.Live`, run from `tests/`.

`scripts/plugin_sdk.sh` (part of `make test`) runs the unit tests, the replay and the
certification with the fake server. CI publishes the Contract Runner report as the
`alerts-contract-report` artifact. The local stack pins
this plugin by default; `QUIVR_ALERTS=off make dev` leaves it out
([harness](../../docs/quivr-v2-local-harness.md)).
