# alerts

Quivr's first-party alert rules, a `subscription` plugin built with the
[Python Plugin SDK](../../sdks/python/README.md). A Subscription pinned to
`{"plugin_id": "alerts", "version": "0.1.0"}` gets a Match when a new article
satisfies its Saved Query's expression. This version implements one alert kind,
**`keywords`**: a boolean keyword query over the article's title and body, with
optional filters on its metadata. The kind `described` (plain-language alerts)
is reserved for a later version.

Operators writing alert queries should start with the
[keyword alerts guide](../../docs/keyword-alerts.md). This README is the reference.

## Expression

A Saved Query expression for this plugin:

```json
{
  "kind": "keywords",
  "match": {"all": [
    {"term": "Airbus"},
    {"any": [{"term": "grève"}, {"term": "strike"}]},
    {"not": {"term": "sport"}}
  ]}
}
```

`match` is a tree of nodes. Each node is an object with exactly one of these shapes:

| Node | Satisfied when |
| --- | --- |
| `{"term": "Airbus"}` | The word, or the exact phrase (`{"term": "Marine Le Pen"}`), appears in a searched Part |
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
`"Airbus" AND (grève OR strike)` cannot be checked by a schema: a typo would be
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
  - `bus` does not match "Airbus";
  - `salarié` does not match "salariés", because there is no stemming;
  - `Airbus` matches "l'Airbus".
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
| `text_roles` | `["title", "body"]` | Part roles that terms search |

```json
{"manifest": "plugins/alerts/quivr-plugin.yaml", "endpoint": "http://127.0.0.1:9910",
 "configuration": {"fields": {"author": "/extensions/example.news/data/author",
                              "category": "/extensions/example.news/data/categories"}}}
```

**Subscription configuration** (`evaluator.configuration`):

| Field | Default | Meaning |
| --- | --- | --- |
| `wait_for_enrichment` | `false` | Answer `not_ready` until the article is enriched (embeddings attached). The core asks again on `record.enrichment_available` |

Rules run when an article becomes searchable. Keyword alerts need no enrichment, so
they decide at once by default. If a deployment never enriches articles, a Subscription
with `wait_for_enrichment: true` never decides.

## Decisions and evidence

- **Decisions.** Each evaluation answers `match`, `no_match` or `not_ready`. The
  decision depends only on the article, the expression and the configurations, so
  batches can be deduplicated and replayed.
- **Evidence.** A `match` carries evidence, which Quivr stores with the Match and
  exposes through `GET /v0/matches/{id}`. It names the terms and field values that
  support the match, taken from the satisfied branches, never from under a `not`:

```json
{
  "explanation": "Matched \"Airbus\" in title, body; \"grève\" in body.",
  "part_keys": ["title", "body"],
  "details": {
    "kind": "keywords",
    "terms": [{"term": "Airbus", "part_keys": ["title", "body"]}, {"term": "grève", "part_keys": ["body"]}],
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

## Text notation

`alerts.notation` translates the query text people write into the expression:

```text
"Airbus" AND (grève OR strike) NOT sport
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
- `Marine Le Pen` without quotes requires the three words anywhere, and
  `"Marine Le Pen"` requires the phrase.
- A leading `-` is refused: write `NOT`.
- A word shaped `name:value` is a field filter, unless the value starts with `/`
  (a URL). Quote it to search it as text: `"re:Invent"`.
- Chains of the same operator are flattened, and parentheses around a single item add
  no level.

```bash
python3 -m alerts.notation '"Airbus" AND (grève OR strike) NOT sport'   # prints the expression
```

It exits 1 and explains the mistake for an invalid query. From Python, use
`from alerts.notation import parse, NotationError`.

## Run and test

```bash
python3 -m venv .venv && . .venv/bin/activate
pip install -e <quivr-v2 checkout>/sdks/python -e .
python3 -m unittest discover -s tests            # grammar, matching, evidence, schema
quivr plugin dev --fixture fixtures/sample.json  # replay the sample batch
quivr plugin test .                              # Contract Runner certification
```

`scripts/plugin_sdk.sh` (part of `make test`) runs all three, and CI publishes the
Contract Runner report as the `alerts-contract-report` artifact. The local stack pins
this plugin by default; `QUIVR_ALERTS=off make dev` leaves it out
([harness](../../docs/quivr-v2-local-harness.md)).
