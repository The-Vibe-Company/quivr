# Reach the search index through the Weaviate Go client

Date: 2026-11-19

Status: accepted

## Decision

The Weaviate adapter uses the official Go client, `github.com/weaviate/weaviate-go-client/v6`, for every request: schema, writes, reads, search and purge. It replaces hand-written REST and GraphQL requests. Weaviate is deprecating its GraphQL endpoint, which carried every search; the client sends searches, inserts and filtered deletes over gRPC and schema changes over REST.

Every request carries `X-Weaviate-Client-Integration: quivr/<version>` so the index can tell which integration is calling.

## What the engine now requires

- A reachable gRPC endpoint next to the REST one. `weaviate_grpc_address` names it and defaults to the `weaviate_url` host on port 50051. Both use the scheme of `weaviate_url`.
- Weaviate 1.38.8 or newer, the client's minimum.
- A `weaviate_url` without a path: the client builds request paths from scheme, host and port.

## What is given up until the client allows it

The client builds its own HTTP client and gRPC channel. Until it accepts a custom HTTP client and gRPC dial options ([weaviate-go-client#532](https://github.com/weaviate/weaviate-go-client/pull/532)):

- `tls.weaviate` accepts only `enabled`. A private CA, a client certificate or a server name refuses startup, in configuration validation, instead of connecting with weaker trust than asked for.
- Index requests follow redirects without the same-origin check the other dependencies keep, and forward the API key on them.
- The `weaviate.request` trace span is opened by the adapter around each client call. It covers the whole request and no longer records an HTTP method or status.

When the client release with those options is adopted, restore the three and supersede this section.

## Consequences in the adapter

- An object is read by a search filtered on its id; the client has no read-by-id call. That search can miss an object for a moment while another writer replaces the same identity, so the read that verifies a write reports absence only after it has held for two seconds.
- A filtered delete reports how many objects matched and which failed, not the index's per-request cap. A generation purge counts with a dry run, lists a window of ids with a search, and deletes that window under the same ownership filter. A version purge deletes, then counts again; only a zero count proves completion.
- A reply is bounded by the index's gRPC message limit (`GRPC_MAX_MESSAGE_SIZE`), not by the adapter's former 2 MiB and 16 MiB decode limits.
- Adding a named vector to an existing collection, and reading back the compression in use, needed changes in the client ([weaviate-go-client#534](https://github.com/weaviate/weaviate-go-client/pull/534)).

Data written by earlier releases is read as is: the physical schema, object identities and named vectors are unchanged.
