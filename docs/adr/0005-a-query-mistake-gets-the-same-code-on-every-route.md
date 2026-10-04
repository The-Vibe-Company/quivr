# A query mistake gets the same public error code on every route

Date: 2026-10-01

Status: accepted

Client developers write one error handler per code, so a code must mean the same mistake on every route. An audit of the ingestion API tests ([THE-743](https://linear.app/thevibecompany/issue/THE-743)) found that it did not. Fixing that is [THE-802](https://linear.app/thevibecompany/issue/THE-802):

- An empty `limit=` was accepted with the default page size on the change feed and the plugin plan history. It was `422 invalid_query` on connectors and quarantine, and `422 invalid_limit` everywhere else.
- An empty `page_cursor=` was `invalid_query` on connectors and quarantine, and `invalid_cursor` everywhere else.
- A Subscription owner that was empty, longer than 128 characters or not a string was `invalid_schema` when creating a Subscription. The request schema refused it before the service did. The same owner was `invalid_owner` (or `invalid_query` when empty) in the listing filter.
- An upload request refused by the upload service was sent as `invalid_schema`. Its error was not a `publicerr` sentinel, so the rule that a code never comes from error text ([THE-692](https://linear.app/thevibecompany/issue/THE-692)) did not cover it.

## Decision

- A query parameter that is unknown, repeated, or required but missing is `422 invalid_query`.
- A present value that a parameter refuses, the empty value included, gets that parameter's own code: `invalid_limit` for `limit`, `invalid_cursor` for a page or change cursor, `invalid_owner` for a Subscription owner. A value that has its own code answers that code in a request body too, even when the request schema is what refused it.
- A body that does not match its request schema is `invalid_schema`. A body that matches but is refused by the service is `invalid_input`, or a more specific code. Upload errors are `publicerr` sentinels, so the code comes from the sentinel and never from error text.

Every list route reads `limit` through one parser, so the rule cannot drift again route by route.

## Considered Options

- **Accept an empty `limit=` as the default everywhere.** This would match how the change feed already behaved. But an empty value is almost always a client bug, such as a template variable left unset, and most routes already refused it. Refusing it everywhere changes fewer answers and shows the bug.
- **Answer `invalid_schema` for every owner refused on creation.** This would change no answer on creation. But the listing filter has no request schema, so the same owner would still get two codes, which is the problem this decision fixes.
- **Drop the length bounds from the owner in the contract, so only the service refuses it.** This would give the same code without any transport mapping. But the bounds are part of the contract that generated clients read, so they stay.

## Consequences

- Changed answers: `limit=` on `/v0/changes` and `/v0/admin/plugins/plans` is `422 invalid_limit` instead of `200`. `limit=` on `/v0/connectors` and `/v0/admin/quarantine` is `invalid_limit` instead of `invalid_query`. `page_cursor=` on the same two routes is `invalid_cursor` instead of `invalid_query`. A refused owner on `POST /v0/subscriptions` and an empty `owner=` on `GET /v0/subscriptions` are `invalid_owner`. An upload refused by the upload service is `invalid_input`; the request schema already refuses every such body, so no client sees this one in practice.
- An `owner` member on a route that does not accept one, such as a new Subscription Version, is still `invalid_schema`. It is an unexpected member, not a refused owner.
- The OpenAPI contract states the code on every `limit` parameter and on the owner, and the API overview states the rule.
