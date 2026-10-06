# Read NewsML-G2 text items

`newsml-g2` turns one IPTC NewsML-G2 XML news item into searchable text and metadata.
It accepts `application/vnd.iptc.g2.newsitem+xml` and
`application/vnd.iptc.g2.newsmessage+xml`. A message must contain exactly one item;
split a multi-item message before submitting it. The plugin uses the Python SDK
and [defusedxml](https://github.com/tiran/defusedxml) (PSF license).

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
  --report .scratch/newsml-g2/contract-report.json plugins/newsml-g2
```

Certification prints `CERTIFIED`. `sample.json` exercises the news-item media type;
`message.json` exercises the news-message media type. Both appear in the report.
Fixtures are synthetic Arabic, French and English text under the MIT license.
The golden response in `tests/data/` records the mapping independently.

Run `python3 -m newsml_g2` as a separate service. Pin its manifest and endpoint
with required routes for both media types; [Pin a plugin](https://docs.quivr.thevibecompany.co/run-quivr/pin)
explains the installation configuration. The plugin does not fetch catalog URLs,
media references or provider links.

## Source identity

Before uploading, the submitter extracts `newsItem/@guid` as its Record Key and
`newsItem/@version` as its Source Position, submitting each revision in order.
The normalizer cannot change the identity of an accepted Record Version.
It exposes both strings in `newsml-g2.document` for reconciliation.
Submit each split item as a bare `newsItem` or a separate single-item message.
[Add content](https://docs.quivr.thevibecompany.co/guides/add-content#upload-a-file)
explains Blob upload and submission.
Corrections remain source versions; withdrawal submission is the acquirer's job.

## Field mapping

| NewsML-G2 value | Output |
| --- | --- |
| `contentMeta/headline` | `title` Parts keyed `headline-1`, `headline-2`, … |
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

Only XHTML, NITF and unqualified paragraphs become body text. Part context
describes the whole element; span overrides and foreign elements stay in the
XML tree. All extensions use schema version `1`. Subject QCodes, catalogs and
scheme declarations remain as supplied. Missing optional values are omitted.

## Common metadata

`quivr.metadata` schema `1` carries string `language`, `published_at`,
`source_type` (`news_item`), `source`, and string arrays `author`, `subjects`,
`tags`, `country`, `place`. These fields answer `metadata.<field>` filters in
[search and document lists](https://docs.quivr.thevibecompany.co/guides/search#filter-by-document-metadata)
across sources and corpora. The reserved namespace needs no manifest ownership.

Publication uses a valid RFC 3339 `firstCreated`, otherwise
`versionCreated`, converted to UTC with its subsecond precision retained.
A correction therefore keeps the original publication date when available.
Dates without a timezone, malformed dates and leap seconds are omitted.
Provider/creator/subject identifiers prefer URI, then QCode, then name or value.
Place uses names from `located` and subjects typed `*:geoArea`, falling back to
that identifier order. Country uses their `iso3166-1a2` QCodes, including broader
locations. Keywords become tags; no remote vocabulary lookup takes place.

Unknown and blank values are omitted. Common strings are trimmed and shortened
to 200 characters; arrays keep the first 50 distinct bounded values in source
order. Original dates, identifiers and full arrays remain in `newsml-g2.document`
and `newsml-g2.xml`. The temporary `newsml-g2.metadata` extension is removed.
Stored Versions keep their original metadata; a projection rebuild cannot add
these fields. New source revisions run the updated normalizer.

## Settings and limits

| Setting | Default | Meaning |
| --- | --- | --- |
| `max_input_bytes` | `1048576` | XML bytes allowed, from 1024 to 1 MiB; checked before downloading |
| `max_text_bytes` | `262144` | Total UTF-8 text bytes allowed, from 1024 to 256 KiB |
| `header_paths` | `[]` | Up to 32 literal child paths from the XML root; foreign names use `{namespace-uri}name` |

For example, not run as an installation: `{"header_paths": ["itemMeta/{urn:example:wire}header"]}`.
Unqualified path names mean the IPTC NewsML namespace. Wildcards, predicates,
parent steps and recursive searches are rejected. Missing paths produce empty arrays.
The full XML tree is retained regardless of this list.

All input refusals are terminal: forbidden DTDs/entities use `unsafe_xml`, malformed
XML uses `invalid_xml`, and excessive depth or element count uses `xml_too_complex`.
XML is limited to 10000 elements and 64 levels. Adjacent body Parts with equal
language and direction are grouped to fit 64 text Parts (`paragraphs_grouped`).
Omitted direction means `ltr`. More than 64 incompatible text groups use
`too_many_text_parts`. The source Part is additional. Items without text keep
the source Blob and metadata, with a `no_text_parts` warning.

Text beyond its budget and serialized responses of 2 MiB or more are refused
with terminal diagnostics (`text_too_large`, `manifest_too_large`); full source
metadata is never silently truncated. The engine's ingestion plugin may apply a smaller
segment budget. Keep the raw Blob to inspect a quarantined item.
See [diagnostics and reprocessing](https://docs.quivr.thevibecompany.co/run-quivr/reprocess-quarantined-versions)
for inspecting failures and processing them again after a fix.
