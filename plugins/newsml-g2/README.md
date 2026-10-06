# Read NewsML-G2 text items

`newsml-g2` turns IPTC NewsML-G2 XML news items and messages into searchable text and metadata.
It accepts `application/vnd.iptc.g2.newsitem+xml` and
`application/vnd.iptc.g2.newsmessage+xml`. A message contains one or more `newsItem`
elements; all their text is indexed in source order. Other item types are refused.
The plugin uses the Python SDK and [defusedxml](https://github.com/tiran/defusedxml) (PSF license).

## Install and certify

From the repository root, with Python 3.12+ and Go matching `go.mod`:

```sh
python3 -m venv .scratch/newsml-g2/venv
. .scratch/newsml-g2/venv/bin/activate
pip install -e sdks/python -e plugins/newsml-g2
go build -o .scratch/newsml-g2/quivr ./cmd/quivr
python3 -m unittest discover -s plugins/newsml-g2/tests
.scratch/newsml-g2/quivr plugin inspect plugins/newsml-g2
.scratch/newsml-g2/quivr plugin test \
  --fixture plugins/newsml-g2/fixtures/sample.json \
  --fixture plugins/newsml-g2/fixtures/message.json \
  --fixture plugins/newsml-g2/fixtures/multi-item.json \
  --fixture plugins/newsml-g2/fixtures/oversized-header.json \
  --report .scratch/newsml-g2/contract-report.json plugins/newsml-g2
```

Certification prints `CERTIFIED`. `sample.json` exercises the news-item media type;
`message.json` exercises NITF text; `multi-item.json` and `oversized-header.json`
exercise multiple items and extension truncation. Fixtures are synthetic and MIT licensed.

Run `python3 -m newsml_g2` as a separate service with required routes for both types;
[Pin a plugin](https://docs.quivr.thevibecompany.co/run-quivr/pin) explains installation.
The plugin does not fetch catalog URLs, media references or provider links.

## Source identity

Use `newsItem/@guid` as the Record Key and `newsItem/@version` as its Source Position.
For a message, choose a stable message-level identity before submission, or split
it into separately versioned items. One Blob becomes one Version; its advisory
`newsml-g2.document` and `quivr.metadata` describe the first item.
The normalizer cannot change the submitted identity.
[Add content](https://docs.quivr.thevibecompany.co/guides/add-content#upload-a-file) explains submission.

## Field mapping

| NewsML-G2 value | Output |
| --- | --- |
| `contentMeta/headline` | First nonblank headline is the sole `title`; all later headlines become `body` text |
| `contentMeta/slugline` | `body` Parts keyed `slugline-1`, … |
| `contentSet/inlineXML` paragraphs | `body` Parts keyed `paragraph-1`, …; XHTML and NITF mixed text in reading order |
| `xml:lang`, inherited from ancestors | Version language and each Part's `newsml-g2.text.language` |
| `dir`, inherited from ancestors | Each Part's `newsml-g2.text.dir`; Unicode stays in logical order |
| `contentMeta/language/@tag` | Fallback language when the item has no `xml:lang`; otherwise the first tagged text Part |
| Item `guid`, `version` | `newsml-g2.document.guid`, `.version` |
| `itemMeta/firstCreated`, `versionCreated` | `.first_created`, `.version_created` |
| Provider, signal | `.provider`, `.signals`, retaining attributes and names |
| Urgency, genre, subject, located, creator, keyword | `.urgency`, `.genres`, `.subjects`, `.located`, `.creators`, `.keywords` |
| Element roles | `.roles`, distinct in source order |
| Every element, attribute and mixed text | `newsml-g2.xml.root`, with namespace-expanded names and ordered children |
| Configured header paths | `newsml-g2.headers.paths`, arrays of matching element trees |
| Original XML | `source` Blob Part, referencing the input without rewriting it |

Only XHTML, NITF and unqualified paragraphs become body text. Span overrides
and foreign elements stay in the XML tree. Multi-item Part keys start with `item-1-`, `item-2-`, …; single-item keys
stay unchanged. Each item supplies its own fallback language. All extensions use
schema version `1`. Subject QCodes, catalogs and scheme declarations stay as supplied.

## Common metadata

`quivr.metadata` schema `1` carries string `language`, `published_at`,
`source_type` (`news_item`), `source`, and string arrays `author`, `subjects`,
`tags`, `country`, `place`. These fields answer `metadata.<field>` filters in
[search and document lists](https://docs.quivr.thevibecompany.co/guides/search#filter-by-document-metadata)
across sources and corpora. The reserved namespace needs no manifest ownership.

Publication uses an RFC 3339 `firstCreated` with up to 9 fractional digits, otherwise
`versionCreated`, converted to UTC with its subsecond precision retained.
A correction therefore keeps the original publication date when available.
Dates without a timezone, malformed dates and leap seconds are omitted.
Provider/creator/subject identifiers prefer nonblank URI, QCode, name, then value.
Place uses names from `located` and subjects typed `*:geoArea`, falling back to
that identifier order. Country uses their `iso3166-1a2` QCodes, including broader
locations. Keywords become tags; no remote vocabulary lookup takes place.

Unknown and blank values are omitted. Common strings are trimmed and shortened
to 200 characters; arrays keep the first 50 distinct bounded values in source
order. NewsML extensions retain source metadata within the extension budget;
the source Blob retains every value. Only new source revisions run the updated normalizer.

## Settings and limits

| Setting | Default | Meaning |
| --- | --- | --- |
| `max_input_bytes` | `1048576` | XML bytes allowed, from 1024 to 1 MiB; checked before downloading |
| `max_text_bytes` | `262144` | Total UTF-8 text bytes allowed, from 1024 to 256 KiB |
| `header_paths` | `[]` | Up to 32 literal child paths from the XML root; foreign names use `{namespace-uri}name` |

For example, not run as an installation: `{"header_paths": ["itemMeta/{urn:example:wire}header"]}`.
Unqualified path names mean the IPTC NewsML namespace. Wildcards, predicates,
parent steps and recursive searches are rejected. Missing paths produce empty arrays.

All input refusals are terminal: forbidden DTDs/entities use `unsafe_xml`, malformed
XML uses `invalid_xml`, and excessive depth or element count uses `xml_too_complex`.
XML is limited to 10000 elements and 64 levels. Adjacent body Parts with equal
language and direction are grouped to fit 64 text Parts (`paragraphs_grouped`).
Omitted direction means `ltr`. More than 64 incompatible text groups use
`too_many_text_parts`. The source Part is additional. Items without text keep
the source Blob and metadata, with a `no_text_parts` warning.

Extensions fit 65,536 JSON bytes including envelopes and Go's HTML escaping.
Small sets stay complete. On overflow, shared metadata gets 24 KiB and each owned
Version namespace gets 12 KiB: metadata drops fields from the end in insertion order;
oversize XML/header views become empty `root`/`paths` objects with `truncated: true`.
Owned metadata also marks omissions; shared metadata uses only its declared keys.
Part context gets 256 bytes in the same field order. Any omission emits `extensions_truncated`;
searchable text and the original source Blob stay intact.

Text or responses over their declared budgets are refused (`text_too_large`,
`manifest_too_large`). The ingestion plugin may apply a smaller segment budget.
See [diagnostics and reprocessing](https://docs.quivr.thevibecompany.co/run-quivr/reprocess-quarantined-versions)
for inspecting failures and processing them again after a fix.
