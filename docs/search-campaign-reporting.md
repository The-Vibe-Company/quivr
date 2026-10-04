# Report and review campaign candidates

Use the campaign's aggregate leaderboard to propose settings, report exact agent
usage and send daily summaries. A promotion needs trusted full-engine and held-out
confirmation; a tier-1 win alone cannot change deployed defaults.

## Prerequisites

Start a [bounded campaign](search-campaigns.md) first. Use its frozen checkout,
Python 3.12 and `EVAL_CONTROL_DATABASE_URL`. Keep one lead-agent session on its
campaign ticket. It reads `status`; it must never read private per-query artifacts.

Your operator provisions these environment variables outside the repository:

- `EVAL_LINEAR_TOKEN`: a Linear API token allowed to read and comment on the ticket.
- `EVAL_SLACK_BOT_TOKEN`: a Slack bot token with `chat:write`.
- `EVAL_SLACK_CHANNEL`: the selected channel ID; invite the bot to that channel.

The campaign's `ticket` must identify that Linear issue, using its identifier or UUID.
No credentials go in YAML, proposals, receipts or source control. The coordinator
chooses where the supervisor and watchdog run and provisions access after review.

## Give the lead a bounded proposal

The lead reads aggregate scores, gate outcomes and spending with `status`.
`status.space` gives the current authorized distributions and grids; `baseline` stays frozen. It can
submit a full candidate configuration within the authorized distributions. Use a
stable `id`: identical replay is harmless; different content for that ID is refused.
Optional `next_plan` records the lead's next step for the digest.

A proposal may expand an existing numeric range on the same grid. It cannot change
choices, add settings, shrink ranges, change the baseline or raise any budget.
Expansions have durable revisions. An `idea` records structural work for a future
campaign; it never executes code. There is no experiment-plugin branch executor.

Example proposal excerpt, not run; `config` must contain the full validated candidate:

```json
{"id":"weight-idea-1","idea":"Try a different chunker in a future campaign.",
 "next_plan":"Review the next exploration wave before requesting confirmation."}
```

Examples requiring an existing campaign and database credentials; not run against a
live campaign during development:

```sh
python3 scripts/eval/search_campaign.py propose public-search-pilot .scratch/proposal.json
python3 scripts/eval/search_campaign.py status public-search-pilot
```

Optuna queues accepted configuration proposals durably. Restarting the lead does
not enqueue them twice. The campaign's trial limit still applies; proposals that
cannot fit stay recorded without running.

## Ingest exact token receipts

The runtime usage source must report exact counts. Normalize cached input as a
subset of `input_tokens`, and provide all three counts, including a reported zero.
Deduplication uses `(provider, session, turn)`. Reusing that identity with different
counts is refused. Receipts can arrive after the campaign stops.

Example receipt, not a measured runtime record:

```json
{"provider":"runtime","session":"lead-1","turn":"turn-1",
 "input_tokens":123,"output_tokens":17,"cached_input_tokens":30}
```

Example requiring database credentials, not run against a live campaign:

```sh
python3 scripts/eval/search_campaign.py usage public-search-pilot .scratch/usage.json
```

`status.agent_token_usage` contains exact cumulative counts and receipt count.
With no receipts it is `null`, and the digest says **unknown**. Imported counts cover
only supplied receipts. Automatic runtime receipt collection is a follow-up;
there are no estimates. Agent tokens are separate from the measurement API ledger.

## Check daily delivery

The managed supervisor prepares one snapshot per UTC day after a completed wave,
while paused, or from its independent guard during long measurements. The snapshot
includes up to ten Pareto points, baseline deltas, gate outcomes, confirmed and
uncertain spend, trial states, held-out reads remaining, exact or unknown agent
usage and the next plan. Aggregate result keys identify evidence in the results store.

Each destination has its own durable ID, acknowledgement and retry state. Automatic
failures retry after at least a minute. Acknowledged destinations are not resent.
A retry reconciles Linear's comment ID; Slack receives a stable `client_msg_id`.
Ambiguous provider timeouts may still duplicate a Slack message. The snapshot is saved
before any delivery attempt and stays frozen when later measurements change the leaderboard.

Examples requiring database access; `--send` additionally requires notification
credentials. These commands were not run against live services:

```sh
python3 scripts/eval/search_campaign.py digest public-search-pilot
python3 scripts/eval/search_campaign.py digest public-search-pilot --send
```

Without `--send`, the command previews the current aggregates without delivering or
freezing them. With it, the command also retries older pending days. `status.notifications`
shows each destination's state. Delivery failure does not cancel measurements or
bypass admission and cleanup. Keep the supervisor managed; after a terminal exit,
a scheduled `digest --send` can finish retries.

## Confirm and promote

Full-engine confirmation is unavailable until the trusted runner integration lands.
Its native request/result aliases require an explicit trusted translator; the campaign
hook does not wire the native runner automatically.
The existing engine smoke command cannot qualify a candidate.
`confirm <campaign> <trial>` reports the disabled adapter; it launches no measurement. Finalists remain
pending; no held-out read is charged by the campaign supervisor.

The confirmation adapter must bind frozen code/scorer, dataset/split fingerprints,
baseline and candidate settings, all four gates, held-out pass and synced aggregate
results-store evidence. The trusted runner owns confirmation-read admission and
registers its compute through the campaign lifecycle. Arbitrary success JSON is
never an approval source.

The promotion adapter supports hosted Pro/Fast model and dimensions, plus
`core.retrieve` 1.2.0 weight, depth and fusion settings. Changed character windows,
reranker settings and depth outside 10–100 are blocked with a reason. Direct exploration
uses ranked fusion; full-engine confirmation must bind the actual engine baseline
and candidate. Token/byte segmentation is not inferred from character windows. After trusted
confirmation passes, the adapter opens one settings PR for human approval; it never merges.
`promote <campaign> <trial> --open-pr` retries publication from a trusted stored receipt
only, outside CI. Blocked confirmation/promotion and incomplete `digest --send` return
a nonzero exit code. Promotion requires Git, Go, `gh` authentication and repository write access.
Relevant main drift blocks promotion; unrelated changes are allowed after settings checks.

## Next

[Record and compare evidence](eval-results.md), then use the existing evaluation,
backfill and promotion path after a human reviews and merges a settings PR.
