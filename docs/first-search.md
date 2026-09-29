# Your first search

Create a Corpus, add a text Record to it and search it through the `v0` HTTP API,
on a Quivr running on your machine. It takes a few minutes once the stack is up.

Every command and output on this page is replayed against a real Quivr by
`make verify`, so they work on this version. Outputs show only the fields this
guide relies on; `"..."` stands for a value that changes on every run, such as an
identifier. [`openapi.yaml`](../contracts/http/v0/openapi.yaml) is the complete
contract for every request and response, and the
[API walkthrough](api-walkthrough.md) covers what comes next: batches, uploads,
plugins, monitoring and connectors.

## Before you start

`make dev` builds a single `quivr` binary, starts PostgreSQL, Temporal, SeaweedFS
(S3), Weaviate and TEI through Docker Compose, applies migrations and runs the API
and worker as local processes. It prints the API address and the path of the
generated `config.json`. Ports are dynamic and bound to loopback. No hosted model
service or external key is required.

Throwaway keys, settings and logs live in the private `.scratch/quivr-dev-…`
directory. Never publish it: `state.json`, `config.json`, `worker.json` and `s3.json`
contain credentials. The `keys` field of `config.json` maps each Bearer token to an
Organization, a list of actions and a list of Corpora (`*` grants the whole
Organization). Export the address and the key of Organization `org_a` that may
create Corpora on `*`, add content and search:

```sh
export QUIVR_URL=http://127.0.0.1:<port printed by make dev>
export QUIVR_KEY=$(jq -r '.keys | to_entries[]
  | select(.value.organization == "org_a" and .value.corpora == ["*"]
           and (.value.actions | index("corpora:write"))
           and (.value.actions | index("search:query"))) | .key' <path printed by make dev>/config.json)
```

| Command | Effect |
| --- | --- |
| `make down` | Stop the stack, keep development volumes |
| `make reset` | Stop the stack and delete its volumes |
| `make migrate` | Apply versioned migrations to the running stack |
| `make generate` | Regenerate transport bindings and the Go client after a contract change |
| `GO=/path/to/go make …` | Use a specific Go toolchain |

Local logs are capped at four 1 MiB files per process; Compose services keep three
1 MiB files each. The private `/healthz` and `/readyz` probes use a separate port and
are not part of the public API. Hot migrations can break running processes during
evaluation; restart API and workers after migrating.

The commands use fixed idempotency keys, so running this page a second time on the
same stack replays the first run: start over with `make reset` and `make dev`.

## 1. Create a Corpus

A Corpus is the collection of Records you search together. Every command that
creates something takes an `idempotency_key`: replaying the same request under the
same key returns the same result instead of creating a second one.

```sh runnable
curl -s -X POST "$QUIVR_URL/v0/corpora" \
  -H "Authorization: Bearer $QUIVR_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name": "News", "idempotency_key": "news-corpus"}'
```

```json output
{"corpus_id": "{{CORPUS_ID}}", "name": "News"}
```

Keep the `corpus_id` for the next steps:

```sh
export CORPUS_ID=<the corpus_id above>
```

Run the same command again: you get the same Corpus back. Changing the request
under that key is a conflict.

```sh runnable
curl -s -X POST "$QUIVR_URL/v0/corpora" \
  -H "Authorization: Bearer $QUIVR_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name": "News", "idempotency_key": "news-corpus"}'
```

```json output
{"corpus_id": "{{CORPUS_ID}}", "name": "News"}
```

Creating a Corpus needs `corpora:write` and the `*` Corpus scope, so a key bound to
existing Corpora cannot create new ones.

## 2. Add a Record

A Record is one item from a source, identified in its Corpus by a Source Namespace
(`namespace`, where it comes from) and a Record Key (`record_key`, its identity
there). Submit its text:

```sh runnable
curl -s -X POST "$QUIVR_URL/v0/records" \
  -H "Authorization: Bearer $QUIVR_KEY" \
  -H "Content-Type: application/json" \
  -d @- <<EOF
{
  "idempotency_key": "eclipse-article-1",
  "source": {"corpus_id": "$CORPUS_ID", "namespace": "news", "record_key": "eclipse-article"},
  "content": {"kind": "text", "text": "A total solar eclipse crossed the Pacific on Tuesday."}
}
EOF
```

```json output
{"receipt_id": "{{RECEIPT_ID}}", "state": "..."}
```

```sh
export RECEIPT_ID=<the receipt_id above>
```

The API answers `202 Accepted` with an Ingestion Receipt as soon as the submission
is durably recorded, even if Temporal or S3 are down; its `Location` header points
at the Receipt. Processing is asynchronous. Sending the same command again under the
same `idempotency_key` returns the same Receipt, never a second Record. The Receipt never becomes "failed"
because of an infrastructure outage: the work is retried.

## 3. Follow the Receipt

Read the Receipt until its `state` is `resolved`, usually within a second or two:

```sh runnable retry
curl -s "$QUIVR_URL/v0/ingestion-receipts/$RECEIPT_ID" \
  -H "Authorization: Bearer $QUIVR_KEY"
```

```json output
{"receipt_id": "{{RECEIPT_ID}}", "state": "resolved", "outcome": "created",
 "record_id": "{{RECORD_ID}}", "version_id": "{{VERSION_ID}}"}
```

```sh
export RECORD_ID=<the record_id above> VERSION_ID=<the version_id above>
```

The outcome is `created` for new content. Other outcomes are `duplicate` (this
content is already the Record's Version), `withdrawal_applied` and `conflict`.

## 4. Read the Version

Each accepted content of a Record is an immutable Record Version. Read it with its
Manifest, the canonical text Quivr stores and searches:

```sh runnable
curl -s "$QUIVR_URL/v0/records/$RECORD_ID/versions/$VERSION_ID" \
  -H "Authorization: Bearer $QUIVR_KEY"
```

```json output
{"record_id": "{{RECORD_ID}}", "version_id": "{{VERSION_ID}}",
 "manifest": {"parts": [{"key": "body", "content": {"kind": "text",
   "text": "A total solar eclipse crossed the Pacific on Tuesday."}}]}}
```

Without a `source_revision`, the content itself identifies the Version: submitting
the same text again, even under a new idempotency key, resolves as a `duplicate` of
the same Version.

```sh runnable
curl -s -X POST "$QUIVR_URL/v0/records" \
  -H "Authorization: Bearer $QUIVR_KEY" \
  -H "Content-Type: application/json" \
  -d @- <<EOF
{
  "idempotency_key": "eclipse-article-again",
  "source": {"corpus_id": "$CORPUS_ID", "namespace": "news", "record_key": "eclipse-article"},
  "content": {"kind": "text", "text": "A total solar eclipse crossed the Pacific on Tuesday."}
}
EOF
```

```json output
{"receipt_id": "{{DUPLICATE_RECEIPT_ID}}"}
```

```sh
export DUPLICATE_RECEIPT_ID=<the receipt_id above>
```

```sh runnable retry
curl -s "$QUIVR_URL/v0/ingestion-receipts/$DUPLICATE_RECEIPT_ID" \
  -H "Authorization: Bearer $QUIVR_KEY"
```

```json output
{"state": "resolved", "outcome": "duplicate", "record_id": "{{RECORD_ID}}", "version_id": "{{VERSION_ID}}"}
```

## 5. Search

Search the Corpus. Lexical search is available as soon as the Version is published;
the default `hybrid` mode and `semantic` mode also use embeddings, which the worker
computes right after. Retry until the Record shows up:

```sh runnable retry
curl -s -X POST "$QUIVR_URL/v0/search" \
  -H "Authorization: Bearer $QUIVR_KEY" \
  -H "Content-Type: application/json" \
  -d @- <<EOF
{"query": "eclipse", "corpus_ids": ["$CORPUS_ID"], "mode": "lexical"}
EOF
```

```json output
{"items": [{"record_id": "{{RECORD_ID}}", "version_id": "{{VERSION_ID}}", "part_key": "body", "rank": 1,
  "excerpt": {"text": "A total solar eclipse crossed the Pacific on Tuesday.",
              "start": 0, "end": 53, "coordinate_system": "unicode_codepoint"}}],
 "retrieval_profile": {"name": "balanced", "version": "..."}}
```

Each hit names the Record, the Version and the Part it comes from, and its excerpt
is an exact slice of that Part, counted in Unicode code points. Search modes,
limits and profiles are described in the
[API walkthrough](api-walkthrough.md#search).

## 6. Correct the Record

Submitting different content for the same Record Key creates a new Version of the
same Record:

```sh runnable
curl -s -X POST "$QUIVR_URL/v0/records" \
  -H "Authorization: Bearer $QUIVR_KEY" \
  -H "Content-Type: application/json" \
  -d @- <<EOF
{
  "idempotency_key": "eclipse-article-2",
  "source": {"corpus_id": "$CORPUS_ID", "namespace": "news", "record_key": "eclipse-article"},
  "content": {"kind": "text", "text": "A total solar eclipse crossed the Pacific on Monday."}
}
EOF
```

```json output
{"receipt_id": "{{CORRECTION_RECEIPT_ID}}"}
```

```sh
export CORRECTION_RECEIPT_ID=<the receipt_id above>
```

```sh runnable retry
curl -s "$QUIVR_URL/v0/ingestion-receipts/$CORRECTION_RECEIPT_ID" \
  -H "Authorization: Bearer $QUIVR_KEY"
```

```json output
{"state": "resolved", "outcome": "created", "record_id": "{{RECORD_ID}}", "version_id": "{{NEW_VERSION_ID}}"}
```

Once the new Version is published it becomes the Record's current Version, and
search returns only the current Version of each Record:

```sh runnable retry
curl -s -X POST "$QUIVR_URL/v0/search" \
  -H "Authorization: Bearer $QUIVR_KEY" \
  -H "Content-Type: application/json" \
  -d @- <<EOF
{"query": "eclipse", "corpus_ids": ["$CORPUS_ID"], "mode": "lexical"}
EOF
```

```json output
{"items": [{"record_id": "{{RECORD_ID}}", "version_id": "{{NEW_VERSION_ID}}",
  "excerpt": {"text": "A total solar eclipse crossed the Pacific on Monday."}}]}
```

The earlier Version stays readable, no longer current:

```sh runnable retry
curl -s "$QUIVR_URL/v0/records/$RECORD_ID/versions/$VERSION_ID" \
  -H "Authorization: Bearer $QUIVR_KEY"
```

```json output
{"version_id": "{{VERSION_ID}}", "availability": {"is_current": false}}
```

## Next steps

- Send many Records at once, upload files, or route media types to a normalizer
  plugin: see the [API walkthrough](api-walkthrough.md).
- Pull content from a feed or a mailbox on a schedule: see the
  [connector guide](connectors/README.md).
- Get alerted when new content matches a query: see
  [changes, catalog and monitoring](api-walkthrough.md#changes-catalog-and-monitoring).
