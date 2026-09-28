# RSS and Atom feeds (`rss`)

The `rss` kind polls one feed URL and turns each feed item into a Record in the
instance's Corpus and Source Namespace. It reads RSS 0.9x/2.0, RSS 1.0 (RDF) and
Atom. JSON Feed documents are also accepted (`feed.format: "json"`). Read the [common guide](README.md) first: it covers credentials, health,
events and disabling for every kind.

## Create an instance

```http
POST /v0/connectors
{
  "idempotency_key": "tech-news-feed",
  "corpus_id": "corpus_…",
  "source_namespace": "tech-news",
  "kind": "rss",
  "config": { "url": "https://news.example.org/rss.xml", "honor_ttl": true },
  "schedule": { "interval_seconds": 300 }
}
```

| Field | Required | Meaning |
| --- | --- | --- |
| `config.url` | yes | The feed URL: `http` or `https`, at most 2048 characters. Credentials inside the URL (`user:pass@host`) are refused; deposit them instead. |
| `config.honor_ttl` | no (default `true`) | Respect the feed's `<ttl>`, capped at 60 minutes. See [Polling](#polling). |
| `schedule.interval_seconds` | no (default 300) | How often to poll. The interval lives in `schedule`, not in `config`. It cannot go below the deployment floor (`connector_min_interval`, 30 s by default). |

Use one instance per feed. Collection starts right after creation and ingests
every item currently in the feed.

## Protected feeds

Some feeds need HTTP authentication. Deposit one of these as the credential
secret:

```json
{ "username": "reader", "password": "…" }
```

This is sent as HTTP Basic authentication.

```json
{ "token": "…" }
```

This is sent as `Authorization: Bearer …`.

The credential is sent to the feed URL. On a redirect it follows Go's rule: it
is kept only for the same host or its subdomains and dropped for any other
domain. A redirect from `https` to `http` is refused (`insecure_redirect`), so a
credential never travels in clear text. Rotate it with `PUT /v0/connectors/{id}/credential`, and
set `expires_at` if the provider's credential expires. Never collect paid
content, and never use this to bypass the publisher's access controls.

## Polling

- **Conditional requests.** Each poll sends `If-None-Match` / `If-Modified-Since`
  from the previous response. A `304 Not Modified` is a successful poll with
  nothing new.
- **Feed `ttl`.** When `honor_ttl` is true and the feed declares `<ttl>`
  (minutes), polls are skipped until that time has passed since the last fetch.
  The ttl counts for at most 60 minutes. The effective wait is roughly
  `max(interval, min(ttl, 60 min))`, rounded up to the next scheduled run. A
  skipped run does not touch the feed and does not update `last_success_at`. Set
  `honor_ttl: false` to poll at the interval regardless.
- **Bounds.**
  - Each request times out after 20 seconds.
  - Responses larger than 10 MiB (after decompression) are refused.
  - At most 5 redirects are followed.
  - At most the first 1,000 items of a feed are considered, and at most 250
    new or changed items are submitted per page.
- **Network policy.** Loopback, private, link-local, carrier-grade NAT, other
  special-purpose ranges and IPv6 prefixes that embed an IPv4 address (NAT64,
  6to4, Teredo) are refused (`address_not_allowed`). The check runs on the resolved
  address. Only a local test deployment should set
  `connector_rss_allow_private_addresses: true`.

## What is collected

Only the feed document is fetched. Linked article pages and enclosures are never
downloaded; enrichment belongs to plugins.

Each item becomes one Record Version whose Manifest has these Parts:

| Part key | Role | Content |
| --- | --- | --- |
| `title` | `title` | Item title as plain text. |
| `body` | `body` | Full content (`content:encoded`, Atom `content`) as plain text, or the description/summary when there is no full content. |
| `summary` | `summary` | The description/summary when it differs from the body. |
| `body_html`, `summary_html` | `source_html` | The original markup, preserved verbatim when the source contained HTML. |

HTML is converted to text. Scripts, styles and embedded objects are dropped,
block elements become line breaks, and entities are decoded. Only `title` and
`body` are indexed for search. The other Parts are kept but not searched.

The `connector.rss` extension (schema version `1`) carries the rest:

- **`item`:** `guid`, `link`, other `links`, `authors` (`name`, `email`),
  `published` and `updated` (RFC 3339), `categories` (up to 50) and
  `enclosures` (`url`, `type`, `length`; references only, up to 20).
- **`feed`:** `format` (`rss`, `atom`, `json`), `version`, `title`, `link`
  and `language`. Volatile values such as the feed's build date are left out
  so that a re-fetched item stays byte-identical.
- **`item.truncated`:** `true` when a bound below was applied.

Bounds per item:

- Metadata strings longer than 2 KiB are truncated.
- The body text is kept up to 384 KiB, the summary up to 64 KiB, and the whole
  item's text up to 768 KiB. Original markup is dropped first when an item
  would exceed that.
- Metadata lists are halved until the extension fits in 48 KiB.

Items with neither a title nor any text are skipped.

## Identity, corrections and disappearance

- **Record Key.** The key is the item's `guid` (RSS) or `id` (Atom). Without one,
  it is the item link, and without a link, a hash of the title and text. Keys
  longer than 1 KiB are replaced by their hash.
- **Revision.** The revision is a hash of the item's own fields: title, content,
  summary, links, authors, dates, categories and enclosures. Changes to feed
  metadata (such as a renamed feed) do not change it.
- **Edited items.** A republished or edited item gets a new revision and becomes
  a correction: a new Version of the same Record. Items without guid or link
  cannot be followed across edits; an edit creates a new Record.
- **Unchanged items.** They are not submitted again and create no Version. A
  feed that keeps returning the same items therefore counts toward `silent`.
- **Crash window.** If the worker stops after submitting new items but before
  recording the poll, the retry replays those items. They keep a single
  Version, but they no longer refresh `last_item_at`. The next new item does.
- **Items dropping out.** An item that drops out of the feed (feeds only list
  their latest items) is not withdrawn. Its Record stays searchable.

## Health and error codes

| Situation | Health | `last_error.code` |
| --- | --- | --- |
| HTTP 401 / 403 / 410 | `access_error` until a later successful poll | `unauthorized` / `forbidden` / `gone` |
| Name resolution failure | unchanged | `dns_error` |
| TLS handshake or certificate failure | unchanged | `tls_error` |
| Connection refused or reset | unchanged | `connection_error` |
| No response within 20 s | unchanged | `timeout` |
| HTTP 5xx / 429 | unchanged | `server_error` / `rate_limited` |
| HTTP 404 / other 4xx | unchanged | `not_found` / `http_status` |
| Not a feed, or unparsable XML | unchanged, no Record created | `malformed_feed` |
| Response over 10 MiB | unchanged | `response_too_large` |
| Redirect loop or more than 5 redirects | unchanged | `too_many_redirects` |
| Private or loopback destination | unchanged | `address_not_allowed` |
| Valid feed with no new item | `silent` after `silent_after_seconds` | none |

A malformed feed is rejected as a whole, so a broken document never creates
partial Records.

## Troubleshooting

- **`access_error` with `unauthorized` or `forbidden`.** Check the feed in a
  browser or with `curl -I`. Deposit or rotate the credential if the feed is
  protected.
- **`access_error` with `gone`.** The publisher retired the feed. Disable the
  instance and create a new one for the replacement URL, reusing the Source
  Namespace only if the new feed keeps the same item guids.
- **`malformed_feed`.** The URL may point at an HTML page instead of the feed.
  Look for a `<link rel="alternate" type="application/rss+xml">` on the page.
- **`silent` but the site publishes.** The feed may lag the site, or the
  publisher may reuse guids. Compare the feed's newest item with the site.
- **Items appear late.** Check `schedule.interval_seconds` and the feed's
  `<ttl>`. Set `honor_ttl: false` if the ttl is too conservative.

## Checking a real feed (optional)

CI uses only a local fake feed server. To try a real public feed, create an
instance on a scratch Corpus pointing at a feed you are allowed to read. Wait for
`last_success_at`, then list the Corpus Records. This check is manual and never
part of `make verify`.
