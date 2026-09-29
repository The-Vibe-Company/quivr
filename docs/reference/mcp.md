<!-- Generated from internal/online/mcp_catalogue.go by `make generate`. Do not edit. -->

# MCP reference

> Generated from `internal/online/mcp_catalogue.go` by `make generate`. Do not edit this page: change the source and regenerate.

`quivr mcp --profile <profile>` serves the tools of one profile to an AI agent over MCP (Model Context Protocol) on stdin and stdout. It needs a running server and an API key, like every online command. How to start it, cite a source and who can see what: [Connect an AI agent](../connect-an-ai-agent.md). The command itself: [`quivr mcp`](cli.md#quivr-mcp).

## Profiles

`--profile` picks one profile. A profile only narrows which tools the agent sees; the API key decides, on the server, what every call may reach.

| Profile | Summary | Tools |
| --- | --- | --- |
| [`read`](#profile-read) | list reachable Corpora, search them and read Records; no tool changes data | [`list_corpora`](#tool-list_corpora), [`search`](#tool-search), [`read_record`](#tool-read_record) |
| [`ingest`](#profile-ingest) | the read tools, plus ingest text into a Corpus and follow its Ingestion Receipt; no tool withdraws or deletes | [`list_corpora`](#tool-list_corpora), [`search`](#tool-search), [`read_record`](#tool-read_record), [`ingest_text`](#tool-ingest_text), [`read_receipt`](#tool-read_receipt) |

## Profile read

List reachable Corpora, search them and read Records; no tool changes data.

```sh
quivr mcp --profile read
```

| Tool | Title | Read-only |
| --- | --- | --- |
| [`list_corpora`](#tool-list_corpora) | List reachable Corpora | yes |
| [`search`](#tool-search) | Search Corpora | yes |
| [`read_record`](#tool-read_record) | Read a Record | yes |

Instructions the agent receives when it connects:

```text
Quivr stores documents as Records in Corpora and searches them with exact provenance.
Workflow: call list_corpora to learn which Corpora this connection may search, call search with a question and those corpus_ids, then call read_record on a hit when you need its full context.
To cite a hit, give its record_id, version_id, part_key and excerpt offsets [start,end). Offsets count Unicode code points in that Part's text, and the excerpt text is copied exactly from it.
Access is decided by the server from the API key: a Corpus you cannot reach is refused, never silently skipped.
```

## Profile ingest

The read tools, plus ingest text into a Corpus and follow its Ingestion Receipt; no tool withdraws or deletes.

```sh
quivr mcp --profile ingest
```

| Tool | Title | Read-only |
| --- | --- | --- |
| [`list_corpora`](#tool-list_corpora) | List reachable Corpora | yes |
| [`search`](#tool-search) | Search Corpora | yes |
| [`read_record`](#tool-read_record) | Read a Record | yes |
| [`ingest_text`](#tool-ingest_text) | Ingest text into a Corpus | no |
| [`read_receipt`](#tool-read_receipt) | Read an Ingestion Receipt | yes |

Instructions the agent receives when it connects:

```text
Quivr stores documents as Records in Corpora and searches them with exact provenance.
Read workflow: call list_corpora to learn which Corpora this connection may use, call search with a question and those corpus_ids, then call read_record on a hit when you need its full context.
To cite a hit, give its record_id, version_id, part_key and excerpt offsets [start,end). Offsets count Unicode code points in that Part's text, and the excerpt text is copied exactly from it.
Ingest workflow: call ingest_text with a corpus_id, a record_key and the text. Ingestion is asynchronous: the call returns an Ingestion Receipt once the text is accepted, not once it is searchable. Call read_receipt with its receipt_id until state is resolved and availability.searchable is true; only then does search find the text. Stop following and report the diagnostics when outcome is conflict, availability.state is quarantined, processing.state is blocked, or availability.state is retrieval_ready while is_current is false (a newer text replaced it). Retrying ingest_text with the same arguments returns the same Receipt and never creates a duplicate.
Access is decided by the server from the API key: a Corpus you cannot reach, or an ingestion the key may not perform, is refused with a tool error, never silently skipped.
```

## Tools

### Tool list_corpora

**List reachable Corpora**

| Read-only | Idempotent | Profiles |
| --- | --- | --- |
| yes: the tool changes no data | yes: repeating the same call has no further effect | [`read`](#profile-read), [`ingest`](#profile-ingest) |

Description the agent receives:

```text
List the Corpora this connection's API key may read. Call it first to learn which corpus_ids you can pass to search.
Returns {"items": [{"corpus_id", "name", "effective_retrieval"}], "next_page_cursor"}. Only Corpora the key is authorized for appear. When next_page_cursor is present, call again with it as page_cursor to get the next page.
```

| Argument | Type | Required | Description |
| --- | --- | --- | --- |
| `page_cursor` | string | no | next_page_cursor from a previous list_corpora result; omit for the first page<br><br>At least 1 character. |

<details>
<summary>Input schema</summary>

```json
{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "page_cursor": {
      "type": "string",
      "minLength": 1,
      "description": "next_page_cursor from a previous list_corpora result; omit for the first page"
    }
  }
}
```

</details>

### Tool search

**Search Corpora**

| Read-only | Idempotent | Profiles |
| --- | --- | --- |
| yes: the tool changes no data | yes: repeating the same call has no further effect | [`read`](#profile-read), [`ingest`](#profile-ingest) |

Description the agent receives:

```text
Search one or more Corpora for passages that answer a question. Use it to find evidence before answering or citing.
Returns {"items": [...], "retrieval_profile": {"name", "version"}}. Each item is a ranked hit: rank, record_id, version_id (the Record Version the passage comes from), part_key (the Part of that Version), and excerpt {text, start, end}, where text is the exact slice [start,end) of that Part's text, counted in Unicode code points. Cite a hit with record_id, version_id, part_key and the offsets. An empty items list means nothing matched; an unauthorized corpus_id is an error, never silently dropped.
```

| Argument | Type | Required | Description |
| --- | --- | --- | --- |
| `query` | string | yes | what to look for, in natural language or keywords<br><br>At least 1 character. At most 8192 characters. |
| `corpus_ids` | array of string | yes | Corpora to search, from list_corpora<br><br>At least 1 item. At most 16 items. Items are unique. Each item: at least 1 character. |
| `mode` | string | no | lexical matches words, semantic matches meaning, hybrid combines both; the server default is hybrid<br><br>One of `lexical`, `semantic`, `hybrid`. |
| `profile` | string | no | retrieval profile; the server default is balanced<br><br>One of `fast`, `balanced`, `deep`. |
| `limit` | integer | no | maximum number of hits; the server default is 10<br><br>Minimum 1. Maximum 50. |

<details>
<summary>Input schema</summary>

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": [
    "query",
    "corpus_ids"
  ],
  "properties": {
    "query": {
      "type": "string",
      "minLength": 1,
      "maxLength": 8192,
      "description": "what to look for, in natural language or keywords"
    },
    "corpus_ids": {
      "type": "array",
      "minItems": 1,
      "maxItems": 16,
      "uniqueItems": true,
      "items": {
        "type": "string",
        "minLength": 1
      },
      "description": "Corpora to search, from list_corpora"
    },
    "mode": {
      "type": "string",
      "enum": [
        "lexical",
        "semantic",
        "hybrid"
      ],
      "description": "lexical matches words, semantic matches meaning, hybrid combines both; the server default is hybrid"
    },
    "profile": {
      "type": "string",
      "enum": [
        "fast",
        "balanced",
        "deep"
      ],
      "description": "retrieval profile; the server default is balanced"
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 50,
      "description": "maximum number of hits; the server default is 10"
    }
  }
}
```

</details>

### Tool read_record

**Read a Record**

| Read-only | Idempotent | Profiles |
| --- | --- | --- |
| yes: the tool changes no data | yes: repeating the same call has no further effect | [`read`](#profile-read), [`ingest`](#profile-ingest) |

Description the agent receives:

```text
Read a Record and one of its Versions in full, to expand a search hit into its context or check a citation.
Pass the record_id of a hit, and its version_id to read exactly the Version the excerpt came from; omit version_id to read the Record's current Version.
Returns {"record": {"record_id", "source", "current_version_id", "withdrawn"}, "version": {"version_id", "record_id", "availability", "manifest", "processing", "relations", ...}}. version.manifest.parts holds every Part with its key, role and content; a hit's excerpt offsets index the text of the Part whose key is the hit's part_key. When record.current_version_id differs from the Version you cite, a newer Version of the Record exists.
```

| Argument | Type | Required | Description |
| --- | --- | --- | --- |
| `record_id` | string | yes | record_id of a search hit<br><br>At least 1 character. |
| `version_id` | string | no | version_id to read; omit for the Record's current Version<br><br>At least 1 character. |

<details>
<summary>Input schema</summary>

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": [
    "record_id"
  ],
  "properties": {
    "record_id": {
      "type": "string",
      "minLength": 1,
      "description": "record_id of a search hit"
    },
    "version_id": {
      "type": "string",
      "minLength": 1,
      "description": "version_id to read; omit for the Record's current Version"
    }
  }
}
```

</details>

### Tool ingest_text

**Ingest text into a Corpus**

| Read-only | Idempotent | Profiles |
| --- | --- | --- |
| no: the tool can change data | yes: repeating the same call has no further effect | [`ingest`](#profile-ingest) |

Description the agent receives:

```text
Add text to a Corpus as a Record, so that search can find it later. Use it to store knowledge worth keeping, such as notes or a document you were given.
record_key is your stable name for the Record within the Corpus and namespace: sending another text with the same record_key corrects the Record with a new Record Version; earlier Versions stay readable. namespace groups your Records; it defaults to "mcp".
Ingestion is asynchronous. The call returns an Ingestion Receipt as soon as the text is accepted: {"receipt_id", "state", "outcome", "record_id", "version_id", "availability", "processing", "diagnostics", "source"}. state is pending, then resolved with an outcome (created, duplicate or conflict). The text is searchable only once availability.searchable is true: call read_receipt with the receipt_id until then.
Retries are safe: idempotency_key defaults to a digest of corpus_id, namespace, record_key and text, so repeating the same call returns the same Receipt instead of a duplicate. When that Receipt's Version has since been replaced by another text for the same record_key, the call ingests the text again so it becomes current once more; to go back while the replacing text is not searchable yet, pass your own idempotency_key. A key you pass controls replay yourself; reusing one with different arguments is refused with idempotency_conflict.
```

| Argument | Type | Required | Description |
| --- | --- | --- | --- |
| `corpus_id` | string | yes | Corpus to add the text to, from list_corpora<br><br>At least 1 character. |
| `record_key` | string | yes | your stable name for this Record; the same record_key corrects the same Record<br><br>At least 1 character. |
| `text` | string | yes | the full text of the Record<br><br>At least 1 character. |
| `namespace` | string | no | source namespace the record_key belongs to; omit to use mcp<br><br>At least 1 character. |
| `idempotency_key` | string | no | retry key; omit to derive it from corpus_id, namespace, record_key and text<br><br>At least 1 character. |

<details>
<summary>Input schema</summary>

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": [
    "corpus_id",
    "record_key",
    "text"
  ],
  "properties": {
    "corpus_id": {
      "type": "string",
      "minLength": 1,
      "description": "Corpus to add the text to, from list_corpora"
    },
    "record_key": {
      "type": "string",
      "minLength": 1,
      "description": "your stable name for this Record; the same record_key corrects the same Record"
    },
    "text": {
      "type": "string",
      "minLength": 1,
      "description": "the full text of the Record"
    },
    "namespace": {
      "type": "string",
      "minLength": 1,
      "description": "source namespace the record_key belongs to; omit to use mcp"
    },
    "idempotency_key": {
      "type": "string",
      "minLength": 1,
      "description": "retry key; omit to derive it from corpus_id, namespace, record_key and text"
    }
  }
}
```

</details>

### Tool read_receipt

**Read an Ingestion Receipt**

| Read-only | Idempotent | Profiles |
| --- | --- | --- |
| yes: the tool changes no data | yes: repeating the same call has no further effect | [`ingest`](#profile-ingest) |

Description the agent receives:

```text
Read the Ingestion Receipt that ingest_text returned, to follow its text until it is searchable.
Returns {"receipt_id", "state", "outcome", "record_id", "version_id", "availability", "processing", "diagnostics", "source"}. state pending means the text is accepted but not yet processed. state resolved carries an outcome: created (a new Record Version), duplicate (the same text was already the Record's content) or conflict (see diagnostics). availability {"state", "is_current", "searchable"} describes the Record Version and appears once it exists: call again until availability.searchable is true, then search finds the text. Stop calling and report the diagnostics when outcome is conflict, availability.state is quarantined, processing.state is blocked (the diagnostics explain what needs attention), or availability.state is retrieval_ready while is_current is false (a newer text for the same record_key replaced this Version). A Version still building reports is_current false until it becomes current: keep following it.
```

| Argument | Type | Required | Description |
| --- | --- | --- | --- |
| `receipt_id` | string | yes | receipt_id returned by ingest_text<br><br>At least 1 character. |

<details>
<summary>Input schema</summary>

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": [
    "receipt_id"
  ],
  "properties": {
    "receipt_id": {
      "type": "string",
      "minLength": 1,
      "description": "receipt_id returned by ingest_text"
    }
  }
}
```

</details>
