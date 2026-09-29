# Described alerts: alerts written in plain language

A described alert sends a webhook when a new article fits a plain-language
description, such as "Diplomatic tensions between two countries over visa rules".
A classifier reads each new article and decides whether it fits, even when the
article uses other words or another language. The classifier is TypeSafe's Jev.

This guide is for the people who write described alerts and the operators who
turn them on. For queries that must contain given words, use
[keyword alerts](keyword-alerts.md). The [`alerts` plugin README](../plugins/alerts/README.md)
is the reference.

## Privacy: article text goes to TypeSafe

To judge an article, the `alerts` plugin sends it to TypeSafe's API
(`api.typesafe.ai`), with each description it is judged against. What it sends:
- the title and body, up to about 48 KB of text;
- the Source Namespace;
- the metadata the operator mapped for filters, such as the author or categories.

TypeSafe states that Jev is not trained on customer requests. Its
[legal page](https://docs.typesafe.ai/legal.md) covers data processing and retention.

**Without a TypeSafe key, nothing is ever sent.** An installation without a key
should also pin `"kinds": ["keywords"]` ([below](#turn-described-alerts-on-operators)):
creating a described alert then fails at once with `422 invalid_expression`. If the
pin omits `kinds`, described alerts are accepted but never decided, and the plugin
logs that it has no key. Keyword alerts work either way and send nothing.

## Write a description

Describe the subject of the articles you want, in one or two plain sentences:

| Good | Why |
| --- | --- |
| `Labour strikes at ports and harbours` | One subject, stated directly |
| `Diplomatic tensions between two countries over visa rules` | Names what makes an article fit |
| `New rules on electric scooters in cities` | Specific enough to exclude other transport news |

Tips:

- **One subject per alert.** For two subjects, write two alerts.
- **Say what fits, not what does not.** The classifier reads questions literally,
  and negations make it less reliable.
- **Language.** English descriptions work best. An English description also finds
  articles in French and in other languages.
- **Bounds.** A description has 3 to 1000 characters, with at least one character
  that is not a space.

The classifier answers with a score from 0 to 1: the probability that the article
fits. An article fits when its score reaches the alert's threshold, which is 0.5
by default. For fewer, surer alerts, raise the threshold in the Subscription, for
example `{"threshold": 0.8}` (the allowed range is 0.2 to 0.95).

## Save it as an alert

The expression holds the description:

```json
{"kind": "described", "description": "Labour strikes at ports and harbours"}
```

Create a Saved Query with it, then a Subscription pinned to the `alerts` evaluator,
exactly as for [keyword alerts](keyword-alerts.md#save-it-as-an-alert):

```bash
curl -s -X POST "$QUIVR_API/v0/subscriptions" -H "Authorization: Bearer $QUIVR_KEY" -H 'Content-Type: application/json' -d '{
  "idempotency_key": "port-strikes-alert", "name": "Port strikes",
  "saved_query_id": "<saved_query_id>", "saved_query_version_id": "<current_version.version_id>",
  "evaluator": {"plugin_id": "alerts", "version": "0.2.0", "configuration": {"threshold": 0.6}},
  "destination_id": "<destination id>"}'
```

## Read what matched

`GET /v0/matches/{match_id}` shows who decided, the score, and the Parts the
classifier saw:

```json
{
  "explanation": "Jev (jev-1.13.0) judged that the article fits the description: score 0.97, threshold 0.60.",
  "part_keys": ["title", "body"],
  "details": {"kind": "described", "classifier": "Jev", "model": "jev-1.13.0", "score": 0.97, "threshold": 0.6, "truncated": false}
}
```

`truncated: true` means the article was longer than what is sent, so the
classifier saw only its beginning.

## Timing and cost

- **Timing.** By default a described alert waits until the article is enriched,
  that is, until its embeddings are attached. To decide as soon as the article is
  searchable, set `{"wait_for_enrichment": false}`. If a deployment never enriches
  articles, keep that setting, or described alerts never decide.
- **Cost.** Each new article costs one TypeSafe request for all described alerts
  together, whatever their number or owners. Quivr asks each distinct description
  once. It splits the request, in parallel, only when it would exceed TypeSafe's
  size limit, for example with about 50 descriptions of 1000 characters next to a
  long article. It makes one request per group of up to 64 distinct alerts.
- **No pre-filter.** Every new article is judged against every active described
  alert.

## Turn described alerts on (operators)

1. Give the `alerts` plugin process the key in its environment:
   `TYPESAFE_API_KEY=<your key>`. Never put the key in the core configuration, in
   logs, or in the repository.
2. Offer the kind in the `alerts` pin of the core's startup config:

   ```json
   {"plugins": [{"manifest": "plugins/alerts/quivr-plugin.yaml", "endpoint": "http://127.0.0.1:9910",
     "kinds": ["keywords", "described"]}]}
   ```

   The pin's `kinds` lists the alert kinds this installation accepts. Without a
   key, list only `["keywords"]`: a described alert is then refused at creation
   with a clear 422. The core never sees the plugin's environment, so it cannot
   check the key itself.
3. Restart the plugin and the core. At startup the plugin logs whether described
   alerts are enabled.

**To keep them off**, leave `TYPESAFE_API_KEY` unset and pin `"kinds": ["keywords"]`.
The local stack does this for you ([harness](quivr-v2-local-harness.md#plugin-substitution-and-handoff)):
- `make dev` offers described alerts only when `TYPESAFE_API_KEY` is set in its
  environment;
- `make verify` uses a fake TypeSafe server and never calls TypeSafe.

## When TypeSafe is unavailable

A described alert is never dropped. It waits.
- **TypeSafe down or rate limited.** A timeout, a 429 or a 5xx makes Quivr retry
  the article's alerts with backoff. Keyword alerts on the same article wait too,
  because Quivr retries the plugin's batch as one.
- **Key refused or missing.** A 401, or a described alert in an installation
  without a key, leaves only the described alerts pending. Quivr isolates them and
  decides the article's keyword alerts. It keeps retrying the described ones until
  the key is fixed, and the plugin log says why.

The plugin never logs or returns TypeSafe's response body.
