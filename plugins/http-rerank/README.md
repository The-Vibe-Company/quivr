# HTTP cross-encoder reranking

`http.rerank/default` and `http.rerank/deep` ask a base retrieval profile
for candidates, send the query and candidate text to a configured cross-encoder,
and rank the same segments. The base profile inherits the search mode and
scope. Both profiles use the same plugin configuration.

| Setting | Default | Behavior |
| --- | --- | --- |
| `endpoint` | Required | Full HTTP(S) rerank URL, such as `http://localhost:8085/rerank`. |
| `protocol` | `tei` | TEI accepts `query`, `texts`, and `truncate`. Use `cohere` for APIs accepting `model`, `query`, `documents`, and `top_n`. |
| `model` | Unset | Required for `cohere`. Configure the model on the service for TEI. |
| `base_profile` | `core.retrieve/default` | Use either of the declared `core.retrieve` profiles, `default` or `deep`. |
| `candidate_count` | 50 | Accepts 1–100. The Plugin API caps a composed profile request at 50. |
| `ranking` | `rerank_only` | Sort by reranker score, use `rrf` to sum reciprocal ranks with constant 60, or use `linear` to blend min-max normalized scores. A constant score list contributes zero to the linear blend. Equal final scores keep base order. |
| `rerank_weight` | 0.7 | Reranker weight for `linear`. The base weight is the remainder. |
| `max_doc_chars` | 4000 | Unicode code points per document after composing title and passage, from 200 to 32000. |
| `include_title` | true | Prepend a title anchor when the candidate supplies one and has separate passage text. Existing passage text stays intact. The retrieval contract exposes no separate record title for ordinary body candidates. |
| `timeout_ms` | 2500 | HTTP deadline, from 100 to 10000 ms, bounded by the search budget with time reserved for the response. |
| `fallback_on_error` | true | Keep base order and scores on HTTP, timeout, or response errors. Set false to report a retryable search error. |

The plugin sends authorized candidate text and the query to the configured
endpoint. Use HTTPS for every remote endpoint. Use plain HTTP only for a
trusted local service without credentials. Set the optional `RERANKER_API_KEY`
secret in the plugin environment for Bearer authentication over HTTPS.
The service must return one valid indexed score per document.

Run `quivr plugin inspect plugins/http-rerank` and
`quivr plugin test plugins/http-rerank` from the repository root.
The mode fixtures exercise offline fallback. Run `go test -race ./...` from
the plugin directory to exercise successful service calls and failure handling
with local HTTP test servers.
