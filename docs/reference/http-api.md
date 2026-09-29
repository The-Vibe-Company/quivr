<!-- Generated from contracts/http/v0/openapi.yaml by `make generate`. Do not edit. -->

# HTTP API reference

> Generated from `contracts/http/v0/openapi.yaml` by `make generate`. Do not edit this page: change the source and regenerate.

Quivr V2 public text foundation contract, version `0.0.0-draft`.

THE-543 and THE-547 evaluation contracts; endpoint implementations are separate work. Matching criterion is plugin-owned and deferred. One configured webhook destination, immutable Matches, independent at-least-once Delivery and reference-only notifications. OpenAPI is authoritative for transport shapes. THE-640 adds text search and asynchronous Corpus projection rebuild initiation.

## Authentication

Every endpoint requires `ApiKey` unless it says otherwise.

| Scheme | Type | Description |
| --- | --- | --- |
| `ApiKey` | HTTP `bearer` | API key, not necessarily a JWT. Server derives Organization, permitted actions and Corpus scope; every resource access is authorized. |

## Endpoints

| Endpoint | Operation | Permissions |
| --- | --- | --- |
| [`POST /v0/records`](#post-v0records) | `ingestRecord` | `content:write` |
| [`GET /v0/records`](#get-v0records) | `listRecords` | `content:read` |
| [`POST /v0/records/batch`](#post-v0recordsbatch) | `ingestBatch` | `content:write` |
| [`POST /v0/records/withdrawals`](#post-v0recordswithdrawals) | `withdrawRecord` | `content:write` |
| [`GET /v0/records/{record_id}`](#get-v0recordsrecord_id) | `getRecord` | `content:read` |
| [`GET /v0/records/{record_id}/versions/{version_id}`](#get-v0recordsrecord_idversionsversion_id) | `getVersion` | `content:read` |
| [`GET /v0/ingestion-receipts/{receipt_id}`](#get-v0ingestion-receiptsreceipt_id) | `getReceipt` | `content:read` |
| [`POST /v0/uploads`](#post-v0uploads) | `createUpload` | `blobs:write` |
| [`POST /v0/uploads/{upload_id}/confirm`](#post-v0uploadsupload_idconfirm) | `confirmUpload` | `blobs:write` |
| [`GET /v0/uploads/{upload_id}`](#get-v0uploadsupload_id) | `getUpload` | `blobs:read` |
| [`GET /v0/blobs/{blob_id}`](#get-v0blobsblob_id) | `getBlob` | `blobs:read` |
| [`GET /v0/operations/{operation_id}`](#get-v0operationsoperation_id) | `getOperation` | `operations:read` |
| [`POST /v0/operations/{operation_id}/cancel`](#post-v0operationsoperation_idcancel) | `cancelOperation` | `operations:write` |
| [`POST /v0/operations/{operation_id}/rerun`](#post-v0operationsoperation_idrerun) | `rerunOperation` | `operations:write` |
| [`POST /v0/corpora`](#post-v0corpora) | `createCorpus` | `corpora:write` |
| [`GET /v0/corpora`](#get-v0corpora) | `listCorpora` | `corpora:read` |
| [`GET /v0/corpora/{corpus_id}`](#get-v0corporacorpus_id) | `getCorpus` | `corpora:read` |
| [`PUT /v0/corpora/{corpus_id}/retrieval`](#put-v0corporacorpus_idretrieval) | `configureRetrieval` | `corpora:write`, `operations:write` |
| [`POST /v0/corpora/{corpus_id}/rebuilds`](#post-v0corporacorpus_idrebuilds) | `rebuildCorpusProjection` | `projections:rebuild` |
| [`GET /v0/changes`](#get-v0changes) | `pollChanges` | `changes:read` |
| [`GET /v0/changes/stream`](#get-v0changesstream) | `streamChanges` | `changes:read` |
| [`POST /v0/saved-queries`](#post-v0saved-queries) | `createSavedQuery` | `monitoring:write` |
| [`GET /v0/saved-queries/{saved_query_id}`](#get-v0saved-queriessaved_query_id) | `getSavedQuery` | `monitoring:read` |
| [`GET /v0/saved-queries/{saved_query_id}/versions/{version_id}`](#get-v0saved-queriessaved_query_idversionsversion_id) | `getSavedQueryVersion` | `monitoring:read` |
| [`POST /v0/saved-queries/{saved_query_id}/versions`](#post-v0saved-queriessaved_query_idversions) | `createSavedQueryVersion` | `monitoring:write` |
| [`POST /v0/saved-queries/{saved_query_id}/delete`](#post-v0saved-queriessaved_query_iddelete) | `deleteSavedQuery` | `monitoring:write` |
| [`GET /v0/subscriptions`](#get-v0subscriptions) | `listSubscriptions` | `monitoring:read` |
| [`POST /v0/subscriptions`](#post-v0subscriptions) | `createSubscription` | `monitoring:write` |
| [`GET /v0/subscriptions/{subscription_id}`](#get-v0subscriptionssubscription_id) | `getSubscription` | `monitoring:read` |
| [`GET /v0/subscriptions/{subscription_id}/versions/{version_id}`](#get-v0subscriptionssubscription_idversionsversion_id) | `getSubscriptionVersion` | `monitoring:read` |
| [`POST /v0/subscriptions/{subscription_id}/versions`](#post-v0subscriptionssubscription_idversions) | `createSubscriptionVersion` | `monitoring:write` |
| [`POST /v0/subscriptions/{subscription_id}/delete`](#post-v0subscriptionssubscription_iddelete) | `deleteSubscription` | `monitoring:write` |
| [`POST /v0/subscriptions/{subscription_id}/disable`](#post-v0subscriptionssubscription_iddisable) | `disableSubscription` | `monitoring:write` |
| [`POST /v0/subscriptions/{subscription_id}/enable`](#post-v0subscriptionssubscription_idenable) | `enableSubscription` | `monitoring:write` |
| [`GET /v0/matches`](#get-v0matches) | `listMatches` | `monitoring:read` |
| [`GET /v0/matches/{match_id}`](#get-v0matchesmatch_id) | `getMatch` | `monitoring:read` |
| [`GET /v0/deliveries/{delivery_id}`](#get-v0deliveriesdelivery_id) | `getDelivery` | `monitoring:read` |
| [`GET /v0/deliveries/{delivery_id}/attempts`](#get-v0deliveriesdelivery_idattempts) | `listDeliveryAttempts` | `monitoring:read` |
| [`POST /v0/connectors`](#post-v0connectors) | `createConnector` | `connectors:write` |
| [`GET /v0/connectors`](#get-v0connectors) | `listConnectors` | `connectors:read` |
| [`GET /v0/connectors/{connector_id}`](#get-v0connectorsconnector_id) | `getConnector` | `connectors:read` |
| [`POST /v0/connectors/{connector_id}/disable`](#post-v0connectorsconnector_iddisable) | `disableConnector` | `connectors:write` |
| [`PUT /v0/connectors/{connector_id}/credential`](#put-v0connectorsconnector_idcredential) | `replaceConnectorCredential` | `connectors:write` |
| [`PUT /v0/connectors/{connector_id}/schedule`](#put-v0connectorsconnector_idschedule) | `changeConnectorSchedule` | `connectors:write` |
| [`GET /v0/connector-kinds`](#get-v0connector-kinds) | `listConnectorKinds` | `connectors:read` |
| [`POST /v0/search`](#post-v0search) | `searchRecords` | `content:read`, `search:query` |

### Records

#### `POST /v0/records`

Operation `ingestRecord`. Requires `content:write`.

Commit durable input, Receipt and dispatch intent before responding. Same key and canonical request returns same Receipt; changed request conflicts. A new source revision corrects the Record. All accepted/replayed submissions use 202, even when a replayed Receipt has resolved.

**Request body** (required): `application/json` [`IngestCommand`](#ingestcommand)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `202` | `application/json` [`Receipt`](#receipt) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. 400 malformed; 401 unauthenticated; 403 forbidden action; 404 absent or inaccessible; 409 conflict; 413 oversized; 422 invalid input; 429 throttled; 503 temporary failure. |

#### `GET /v0/records`

Operation `listRecords`. Requires `content:read`.

Stable keyset traversal of one Corpus's authorized canonical Records in Record ID order, including withdrawn Records. Each page is an independent read, not an atomic historical snapshot. Capture a start-now Change Cursor before scanning, then consume changes after it as invalidations by rereading current resources; see resynchronization procedure. The opaque page cursor is not a Change Cursor and binds the Corpus filter and authorization scope; a page cursor for another filter or scope is 409 cursor_scope_changed with resync_url. This route, relative to the API base, is the resync_url of change-feed and catalog cursor errors.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `corpus_id` | query | string | yes | Minimum length `1`. |
| `page_cursor` | query | string |  | Minimum length `1`. |
| `limit` | query | integer |  | Default `100`. Minimum `1`. Maximum `100`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`RecordPage`](#recordpage) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

#### `POST /v0/records/batch`

Operation `ingestBatch`. Requires `content:write`.

Initial bound: 100 entries (413 batch_too_large), envelope 10 MiB (413 request_too_large). Validate envelope structure/size first, then each entry independently against IngestCommand, including wrong types or missing fields. Each raw entry is also held to the 1 MiB single-request bound (entry error entry_too_large), so the same entry bytes are never refused for size alone. HTTP 200 carries Receipt or Error per entry, with the same error codes as single submission. Same ingestion family/key as single submission. Reuse entry keys after unknown transport outcomes.

**Request body** (required): `application/json` [`BatchRequest`](#batchrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`BatchResult`](#batchresult) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. 400 malformed; 401 unauthenticated; 403 forbidden action; 404 absent or inaccessible; 409 conflict; 413 oversized; 422 invalid input; 429 throttled; 503 temporary failure. |

#### `POST /v0/records/withdrawals`

Operation `withdrawRecord`. Requires `content:write`.

Durably commit withdrawal and Receipt before responding. Terminal identity fence, including before first materialization; no physical purge and no cascade. Uses a distinct withdrawal route family.

**Request body** (required): `application/json` [`WithdrawalCommand`](#withdrawalcommand)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `202` | `application/json` [`Receipt`](#receipt) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. 400 malformed; 401 unauthenticated; 403 forbidden action; 404 absent or inaccessible; 409 conflict; 413 oversized; 422 invalid input; 429 throttled; 503 temporary failure. |

#### `GET /v0/records/{record_id}`

Operation `getRecord`. Requires `content:read`.

Authorized canonical currentness and withdrawal view.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `record_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`Record`](#record) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. 400 malformed; 401 unauthenticated; 403 forbidden action; 404 absent or inaccessible; 409 conflict; 413 oversized; 422 invalid input; 429 throttled; 503 temporary failure. |

#### `GET /v0/records/{record_id}/versions/{version_id}`

Operation `getVersion`. Requires `content:read`.

Authorized immutable source Manifest plus separate live availability and relation expansion. Available links supply current eligible target Record and Version IDs; unavailable links reveal no resolved target IDs or absence/access reason. Historical lookup grants no new rights.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `record_id` | path | string | yes | Minimum length `1`. |
| `version_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`Version`](#version) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. 400 malformed; 401 unauthenticated; 403 forbidden action; 404 absent or inaccessible; 409 conflict; 413 oversized; 422 invalid input; 429 throttled; 503 temporary failure. |

### Ingestion receipts

#### `GET /v0/ingestion-receipts/{receipt_id}`

Operation `getReceipt`. Requires `content:read`.

Return outcome and separately authorized linked availability/diagnostics. Omit unavailable linked content rather than bypassing rights through the Receipt. Linked Version/Record details require current content:read permission and target Corpus authorization.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `receipt_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`Receipt`](#receipt) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. 400 malformed; 401 unauthenticated; 403 forbidden action; 404 absent or inaccessible; 409 conflict; 413 oversized; 422 invalid input; 429 throttled; 503 temporary failure. |

### Uploads

#### `POST /v0/uploads`

Operation `createUpload`. Requires `blobs:write`.

Create a transfer session with a single presigned PUT URL, required headers and expiry. Initial limits: 1 GiB Blob, configurable; oversized uploads are rejected before a session is issued.

**Request body** (required): `application/json` [`UploadRequest`](#uploadrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `201` | `application/json` [`Upload`](#upload) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. 400 malformed; 401 unauthenticated; 403 forbidden action; 404 absent or inaccessible; 409 conflict; 413 oversized; 422 invalid input; 429 throttled; 503 temporary failure. |

#### `POST /v0/uploads/{upload_id}/confirm`

Operation `confirmUpload`. Requires `blobs:write`.

Start or observe checksum/size verification; SDK polls until verified before referencing the Blob in ingestion. Confirmation is repeatable for this session.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `upload_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `202` | `application/json` [`Upload`](#upload) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. 400 malformed; 401 unauthenticated; 403 forbidden action; 404 absent or inaccessible; 409 conflict; 413 oversized; 422 invalid input; 429 throttled; 503 temporary failure. |

#### `GET /v0/uploads/{upload_id}`

Operation `getUpload`. Requires `blobs:read`.

Observe upload verification independently of Ingestion Receipt.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `upload_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`Upload`](#upload) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. 400 malformed; 401 unauthenticated; 403 forbidden action; 404 absent or inaccessible; 409 conflict; 413 oversized; 422 invalid input; 429 throttled; 503 temporary failure. |

### Blobs

#### `GET /v0/blobs/{blob_id}`

Operation `getBlob`. Requires `blobs:read`.

Inspect verified Blob metadata within authorized Organization scope; ID possession does not grant access.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `blob_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`Blob`](#blob) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. 400 malformed; 401 unauthenticated; 403 forbidden action; 404 absent or inaccessible; 409 conflict; 413 oversized; 422 invalid input; 429 throttled; 503 temporary failure. |

### Operations

#### `GET /v0/operations/{operation_id}`

Operation `getOperation`. Requires `operations:read`.

Administrative progress only. This read schema does not specify every administrative command.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `operation_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`Operation`](#operation) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. 400 malformed; 401 unauthenticated; 403 forbidden action; 404 absent or inaccessible; 409 conflict; 413 oversized; 422 invalid input; 429 throttled; 503 temporary failure. |

#### `POST /v0/operations/{operation_id}/cancel`

Operation `cancelOperation`. Requires `operations:write`.

Idempotent cancellation request; does not undo committed effects. Terminal operation returns its existing state. A racing completion may win. Cancellation becomes terminal only after work stops safely; no partial active projection cutover.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `operation_id` | path | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`ActionRequest`](#actionrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `202` | `application/json` [`Operation`](#operation) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

#### `POST /v0/operations/{operation_id}/rerun`

Operation `rerunOperation`. Requires `operations:write`.

Only terminal Operations can be intentionally rerun; otherwise 409 operation_not_terminal. A request key replays the same new linked Operation. Revalidate current scope and command eligibility; completed effects remain subject to domain idempotency.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `operation_id` | path | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`ActionRequest`](#actionrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `202` | `application/json` [`Operation`](#operation) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

### Corpora

#### `POST /v0/corpora`

Operation `createCorpus`. Requires `corpora:write`.

Explicit Corpus creation. Same creation route-family key and canonical request replays the same Corpus. Requires corpora:write.

**Request body** (required): `application/json` [`CorpusRequest`](#corpusrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `201` | `application/json` [`Corpus`](#corpus) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

#### `GET /v0/corpora`

Operation `listCorpora`. Requires `corpora:read`.

Authorized Corpora only. Opaque page cursor bound to action/filter/scope; not a Change Cursor or a Record page cursor (either is 422 invalid_cursor).

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `page_cursor` | query | string |  | Minimum length `1`. |
| `limit` | query | integer |  | Default `100`. Minimum `1`. Maximum `100`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`CorpusPage`](#corpuspage) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

#### `GET /v0/corpora/{corpus_id}`

Operation `getCorpus`. Requires `corpora:read`.

Read effective resolved configuration.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `corpus_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`Corpus`](#corpus) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

#### `PUT /v0/corpora/{corpus_id}/retrieval`

Operation `configureRetrieval`. Requires `corpora:write`, `operations:write`.

Resolve mapping and schedule a new immutable Projection Generation through a retrieval_configuration Operation (202 with Location). Existing active config remains in effect, and is what getCorpus returns, until validated cutover; the Operation reports the pending config's progress and outcome but not its content. A newer accepted config supersedes older pending ones, and a generation pinned to an older config than the effective one fails with retrieval_configuration_superseded instead of reverting it. Same key and canonical request replay the Operation; a changed request is 409 idempotency_conflict. Does not require a separate per-Corpus physical collection.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `corpus_id` | path | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`ConfigUpdate`](#configupdate)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `202` | `application/json` [`Operation`](#operation) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

#### `POST /v0/corpora/{corpus_id}/rebuilds`

Operation `rebuildCorpusProjection`. Requires `projections:rebuild`.

Durably commit a projection_rebuild Operation and dispatch intent before returning. Rebuild the requested Corpus from canonical text and durable artifacts, then activate its validated logical generation. Same Organization + Corpus + rebuild route + idempotency key and canonical request returns the same Operation, including after terminal completion; changed request conflicts. HTTP does not wait for reconstruction. Retries/restarts keep identity and target generation. Preserve other Corpora when physical storage is shared. Normal ingestion/withdrawal guards still apply. Live evaluation schema migrations remain a separate mechanism. Activation follows acceptance order, so a rebuild accepted before one that already activated for the same Corpus and retrieval configuration fails with operation_superseded instead of replacing it.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `corpus_id` | path | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`ActionRequest`](#actionrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `202` | `application/json` [`Operation`](#operation)<br><br>Header `Location`: string (uri-reference). Authorized Operation read URL. | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 400 malformed, 401 unauthenticated, 403 unauthorized scope/action, 404 absent/inaccessible, 409 idempotency conflict, 422 unsupported profile/input, 503 dependency unavailable. |

### Changes

#### `GET /v0/changes`

Operation `pollChanges`. Requires `changes:read`.

Same durable journal as SSE. Without cursor, return empty items and current committed position as next_cursor (start now). With cursor, return authorized events after it, in commit order; duplicates possible. Cursor binds Organization, Corpus filter and authorization scope. Expiry is 410 cursor_expired with resync_url, never silent reset; changed scope/filter is 409 cursor_scope_changed with resync_url. resync_url is the API-relative Record catalog route for the Corpus (/v0/records?corpus_id=...). next_cursor advances over scanned events even when none are visible. Public events are invalidations; reread current state, do not replay historical content into a current mirror.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `cursor` | query | string |  | Minimum length `1`. |
| `corpus_id` | query | string | yes | Minimum length `1`. |
| `limit` | query | integer |  | Default `100`. Minimum `1`. Maximum `100`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`ChangePage`](#changepage) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

#### `GET /v0/changes/stream`

Operation `streamChanges`. Requires `changes:read`.

SSE over the same journal. Last-Event-ID takes precedence over query cursor on reconnect. id is an opaque Change Cursor; data.event_id is deduplication identity. Named change events carry ChangeEvent JSON. checkpoint events carry a cursor when no visible event is emitted, including initial start-now checkpoint. Resume cursor expires or scope changes: before headers use HTTP 410/409; after headers send stream_error with Error JSON then close, without advancing id. Use authenticated streaming fetch/http client. See contract for checkpoint framing and resync.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `cursor` | query | string |  | Minimum length `1`. |
| `corpus_id` | query | string | yes | Minimum length `1`. |
| `Last-Event-ID` | header | string |  | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `text/event-stream` string | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

Example `200` response `text/event-stream`:

```
id: opaque-cursor
event: change
data: {"event_id":"event_1","type":"record.searchable","schema_version":"1","occurred_at":"2026-09-14T12:00:00Z","resource":{"kind":"record","id":"record_1","corpus_id":"corpus_1"},"cursor":"opaque-cursor"}
```

### Saved queries

#### `POST /v0/saved-queries`

Operation `createSavedQuery`. Requires `monitoring:write`.

Persist definition and first immutable version. Replay same key/request returns same IDs; conflict on changed request. Query creation alone evaluates nothing.

**Request body** (required): `application/json` [`SavedQueryCreate`](#savedquerycreate)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `201` | `application/json` [`SavedQuery`](#savedquery) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. Existing /v0 authentication, scope, pagination and idempotency semantics apply. |

#### `GET /v0/saved-queries/{saved_query_id}`

Operation `getSavedQuery`. Requires `monitoring:read`.

Authorized current query view.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `saved_query_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`SavedQuery`](#savedquery) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. Existing /v0 authentication, scope, pagination and idempotency semantics apply. |

#### `GET /v0/saved-queries/{saved_query_id}/versions/{version_id}`

Operation `getSavedQueryVersion`. Requires `monitoring:read`.

Read any immutable Version of the Saved Query, current or earlier, such as the one a Subscription Version or Match pins. The key must grant the Corpora of the Saved Query's current Version and of this Version. A deleted Saved Query's Versions stay readable.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `saved_query_id` | path | string | yes | Minimum length `1`. |
| `version_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`SavedQueryVersion`](#savedqueryversion) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. Existing /v0 authentication, scope, pagination and idempotency semantics apply. |

#### `POST /v0/saved-queries/{saved_query_id}/versions`

Operation `createSavedQueryVersion`. Requires `monitoring:write`.

Edit a Saved Query by committing a new immutable Version that becomes its current Version, with saved_query.updated in every Corpus of the previous and new scope. Subscriptions keep the Version they pin; a new Subscription Version moves one. The key must grant every Corpus of the current and new definitions. Replay of the same key and request returns the same Version; a changed request is 409 idempotency_conflict. A deleted Saved Query is 409 saved_query_deleted.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `saved_query_id` | path | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`SavedQueryVersionCreate`](#savedqueryversioncreate)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `201` | `application/json` [`SavedQueryVersion`](#savedqueryversion) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. Existing /v0 authentication, scope, pagination and idempotency semantics apply. |

#### `POST /v0/saved-queries/{saved_query_id}/delete`

Operation `deleteSavedQuery`. Requires `monitoring:write`.

Logically delete a Saved Query that no Subscription which is not deleted belongs to (409 saved_query_in_use otherwise), with saved_query.deleted per Corpus. It and its Versions stay readable with deleted true; it gets no new Version or Subscription. Repeat is idempotent and commits no event.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `saved_query_id` | path | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`ActionRequest`](#actionrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`SavedQuery`](#savedquery) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. Existing /v0 authentication, scope, pagination and idempotency semantics apply. |

### Subscriptions

#### `GET /v0/subscriptions`

Operation `listSubscriptions`. Requires `monitoring:read`.

Active (enabled, not deleted) Subscriptions of one Subscription Owner, or the global ones with owner=none, in stable Subscription ID order. Lists only Subscriptions the key sees, like every Subscription read. Stable keyset page cursor bound to the owner filter and key scope, not a Change Cursor. Quivr applies no per-owner rule; a layer above can use this listing to enforce its own.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `owner` | query | string | yes | A Subscription Owner, or none for global Subscriptions. Minimum length `1`. Maximum length `128`. |
| `page_cursor` | query | string |  | Minimum length `1`. |
| `limit` | query | integer |  | Default `100`. Minimum `1`. Maximum `100`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`SubscriptionPage`](#subscriptionpage) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. Existing /v0 authentication, scope, pagination and idempotency semantics apply. |

#### `POST /v0/subscriptions`

Operation `createSubscription`. Requires `monitoring:write`.

Atomically create enabled Subscription/Version and activation boundary in commit-ordered journal. Evaluate future eligible transitions, not existing history. Replay same key/request returns same identity and does not reactivate a disabled Subscription. The evaluator must be installed (422 unsupported_evaluator); the pinned Saved Query Version expression and the evaluator configuration must satisfy the evaluator's declared schemas (422 invalid_expression with field /saved_query_version_id, or invalid_subscription_configuration with field /evaluator/configuration/...; message names the first schema issue).

**Request body** (required): `application/json` [`SubscriptionCreate`](#subscriptioncreate)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `201` | `application/json` [`Subscription`](#subscription) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. Existing /v0 authentication, scope, pagination and idempotency semantics apply. |

#### `GET /v0/subscriptions/{subscription_id}`

Operation `getSubscription`. Requires `monitoring:read`.

Read enabled state and pinned current configuration.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `subscription_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`Subscription`](#subscription) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. Existing /v0 authentication, scope, pagination and idempotency semantics apply. |

#### `GET /v0/subscriptions/{subscription_id}/versions/{version_id}`

Operation `getSubscriptionVersion`. Requires `monitoring:read`.

Read any immutable Version of the Subscription, current or earlier, such as the one a historical Match names. As for every read of a Subscription, its Matches and Deliveries, the key must grant every Corpus any of its Versions pinned. A deleted Subscription's Versions stay readable.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `subscription_id` | path | string | yes | Minimum length `1`. |
| `version_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`SubscriptionVersion`](#subscriptionversion) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. Existing /v0 authentication, scope, pagination and idempotency semantics apply. |

#### `POST /v0/subscriptions/{subscription_id}/versions`

Operation `createSubscriptionVersion`. Requires `monitoring:write`.

Edit a Subscription by committing a new immutable Version that becomes current. It pins the current Version of the Subscription's own Saved Query (422 unknown_saved_query otherwise), an evaluator and a destination. It takes effect from its commit, recorded as its activation position with subscription.updated in every Corpus of the previous and new scope. Each later change is judged by the new Version, each earlier one by the Version effective before it. Nothing is backfilled and Matches keep the Version that produced them. Enabled state is unchanged. The evaluator, expression and configuration are checked as on creation (422 unsupported_evaluator, invalid_expression or invalid_subscription_configuration). Replay of the same key and request returns the same Version; a changed request is 409 idempotency_conflict. A deleted Subscription is 409 subscription_deleted.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `subscription_id` | path | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`SubscriptionVersionCreate`](#subscriptionversioncreate)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `201` | `application/json` [`SubscriptionVersion`](#subscriptionversion) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. Existing /v0 authentication, scope, pagination and idempotency semantics apply. |

#### `POST /v0/subscriptions/{subscription_id}/delete`

Operation `deleteSubscription`. Requires `monitoring:write`.

Logically delete a Subscription for good, with subscription.deleted per Corpus. It is also disabled, with every disable guarantee - no new evaluation commit and no new Delivery Attempt admission (reason subscription_deleted); an in-flight attempt may complete. As for a disabled Subscription, a match.withdrawn notice for one of its Matches is still committed with its pending Delivery, which is never attempted. The Subscription, its Versions, Matches and Deliveries stay readable with deleted true. Enable or edit is then 409 subscription_deleted. Repeat is idempotent and commits no event.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `subscription_id` | path | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`ActionRequest`](#actionrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`Subscription`](#subscription) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. Existing /v0 authentication, scope, pagination and idempotency semantics apply. |

#### `POST /v0/subscriptions/{subscription_id}/disable`

Operation `disableSubscription`. Requires `monitoring:write`.

Commit disable. Block new evaluation commits and new Delivery Attempt admissions, including queued retries and update notices. In-flight attempts may complete. A match.withdrawn notice for a Match of this Subscription is still committed with its pending Delivery, which makes no attempt until re-enable. Repeat is idempotent; history remains.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `subscription_id` | path | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`ActionRequest`](#actionrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`Subscription`](#subscription) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. Existing /v0 authentication, scope, pagination and idempotency semantics apply. |

#### `POST /v0/subscriptions/{subscription_id}/enable`

Operation `enableSubscription`. Requires `monitoring:write`.

Commit re-enable of a disabled Subscription on the same Subscription Version. Evaluation resumes from this commit; changes made while disabled are never evaluated. Pending Deliveries parked by the disable become eligible again under the usual admission checks and keep their delivery window, except a match.withdrawn notice committed while disabled, whose window starts at this re-enable. Enabling an enabled Subscription, or repeating the request, is idempotent and commits no event. A deleted Subscription is 409 subscription_deleted.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `subscription_id` | path | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`ActionRequest`](#actionrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`Subscription`](#subscription) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. Existing /v0 authentication, scope, pagination and idempotency semantics apply. |

### Matches

#### `GET /v0/matches`

Operation `listMatches`. Requires `monitoring:read`.

Authorized historical Matches for a Subscription. Stable keyset page cursor, not a Change Cursor. Absence of a Match does not distinguish an evaluator still working from a negative result.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `subscription_id` | query | string | yes | Minimum length `1`. |
| `page_cursor` | query | string |  | Minimum length `1`. |
| `limit` | query | integer |  | Default `100`. Minimum `1`. Maximum `100`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`MatchPage`](#matchpage) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. Existing /v0 authentication, scope, pagination and idempotency semantics apply. |

#### `GET /v0/matches/{match_id}`

Operation `getMatch`. Requires `monitoring:read`.

Authorized historical positive result, explanation and provenance. Recheck current access to Subscription and content; disabled state alone does not erase history. Historical content retention remains separate from search eligibility.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `match_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`Match`](#match) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. Existing /v0 authentication, scope, pagination and idempotency semantics apply. |

### Deliveries

#### `GET /v0/deliveries/{delivery_id}`

Operation `getDelivery`. Requires `monitoring:read`.

Read logical notification status and current admission view. Rights on the referenced Subscription/Corpus are still required for withdrawal-notice metadata.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `delivery_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`Delivery`](#delivery) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. Existing /v0 authentication, scope, pagination and idempotency semantics apply. |

#### `GET /v0/deliveries/{delivery_id}/attempts`

Operation `listDeliveryAttempts`. Requires `monitoring:read`.

Paginated append-only transport history, without secrets or receiver bodies.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `delivery_id` | path | string | yes | Minimum length `1`. |
| `page_cursor` | query | string |  | Minimum length `1`. |
| `limit` | query | integer |  | Default `100`. Minimum `1`. Maximum `100`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`DeliveryAttemptPage`](#deliveryattemptpage) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. Existing /v0 authentication, scope, pagination and idempotency semantics apply. |

### Connectors

#### `POST /v0/connectors`

Operation `createConnector`. Requires `connectors:write`.

Create a Connector Instance bound to exactly one authorized Corpus and one Source Namespace. Kind-specific config and credential secret are validated against the kind's JSON Schema (see listConnectorKinds); a failure is 422 invalid_config or invalid_credential with field pointing at the offending member. Replaying the same idempotency key and request returns the same instance (without re-enabling a disabled one); a different request under the same key is 409 idempotency_conflict. Another enabled instance on the same Corpus and Source Namespace is 409 source_namespace_in_use. The interval defaults per kind and is refused below the deployment floor (30 s by default) with 422 invalid_interval. Deposited credentials are write-only and never returned. A deployment without a credential key refuses any request carrying a credential with 503 credentials_unavailable (retryable false) before storing or digesting it; instances without a credential are unaffected. Commits connector.created in the change feed.

**Request body** (required): `application/json` [`ConnectorCreate`](#connectorcreate)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `201` | `application/json` [`Connector`](#connector) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

#### `GET /v0/connectors`

Operation `listConnectors`. Requires `connectors:read`.

Connector Instances of authorized Corpora, optionally filtered to one Corpus, in stable identifier order. Opaque page cursor bound to filter and scope; not a Change Cursor or another list page cursor (422 invalid_cursor).

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `corpus_id` | query | string |  | Minimum length `1`. |
| `page_cursor` | query | string |  | Minimum length `1`. |
| `limit` | query | integer |  | Default `100`. Minimum `1`. Maximum `100`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`ConnectorPage`](#connectorpage) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

#### `GET /v0/connectors/{connector_id}`

Operation `getConnector`. Requires `connectors:read`.

Read configuration, credential metadata (never the secret) and the last evaluated Connector Health. Instances of other Organizations or unauthorized Corpora are 404.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `connector_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`Connector`](#connector) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

#### `POST /v0/connectors/{connector_id}/disable`

Operation `disableConnector`. Requires `connectors:write`.

Commit disable. No new acquisition run is scheduled; an in-flight run cannot advance the Acquisition Checkpoint afterwards. Repeat is idempotent and disable is absorbing (no re-enable). Commits connector.disabled and connector.health_changed.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `connector_id` | path | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`ActionRequest`](#actionrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`Connector`](#connector) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

#### `PUT /v0/connectors/{connector_id}/credential`

Operation `replaceConnectorCredential`. Requires `connectors:write`.

Deposit a new credential version to rotate the current one. The secret is encrypted at rest and never returned. Replaying the same key and request is idempotent; a different request under the same key is 409 idempotency_conflict. A disabled instance is 409 connector_disabled. Commits connector.credential_replaced, and connector.health_changed when the evaluated health changes. A deployment without a credential key refuses every rotation with 503 credentials_unavailable (retryable false) before storing or digesting it.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `connector_id` | path | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`CredentialReplace`](#credentialreplace)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`Connector`](#connector) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

#### `PUT /v0/connectors/{connector_id}/schedule`

Operation `changeConnectorSchedule`. Requires `connectors:write`.

Set the polling interval of an enabled instance. Setting the current value commits nothing, so repeating the request is harmless. A shorter interval pulls the next scheduled run in; a longer one applies after the run already scheduled. A disabled instance is 409 connector_disabled; an interval below the deployment floor (30 s by default) or above 24 h is 422 invalid_interval with field /interval_seconds. Commits connector.schedule_changed only when the interval changes.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `connector_id` | path | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`ScheduleChange`](#schedulechange)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`Connector`](#connector) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

### Connector kinds

#### `GET /v0/connector-kinds`

Operation `listConnectorKinds`. Requires `connectors:read`.

Connector kinds enabled in this deployment, with the JSON Schemas that validate their config and credential secret, so clients can render configuration forms without knowing the kinds. credential_deposits tells whether this deployment accepts Deposited Credentials at all; when unavailable, any create carrying a credential and every rotation is 503 credentials_unavailable.

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`ConnectorKindCatalog`](#connectorkindcatalog) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

### Search

#### `POST /v0/search`

Operation `searchRecords`. Requires `content:read`, `search:query`.

Resolve the requested profile, compile mandatory Corpus/Organization prefilters, obtain candidates, then canonically hydrate and reauthorize every returned segment. Lexical-first records remain eligible without embeddings; semantic-only queries require vector coverage. Profile selection does not change access/currentness rules.

**Request body** (required): `application/json` [`SearchRequest`](#searchrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`SearchResponse`](#searchresponse) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 400 malformed, 401 unauthenticated, 403 unauthorized scope/action, 404 absent/inaccessible, 409 idempotency conflict, 422 unsupported profile/input, 503 dependency unavailable. |

## Webhooks

Requests the server sends to a receiver you run; they are not routes of this API.

### `monitoringNotification` (POST)

Operation `receiveMonitoringNotification`. No authentication.

Receiver endpoint, not a Quivr API route. Verify Standard Webhooks v1 HMAC-SHA256 over webhook-id + dot + webhook-timestamp + dot + raw body before parsing. Timestamp refreshed per attempt; body event_id equals webhook-id. See monitoring contract for retry defaults.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `webhook-id` | header | string | yes | Minimum length `1`. |
| `webhook-timestamp` | header | string | yes | Minimum length `1`. |
| `webhook-signature` | header | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`WebhookEvent`](#webhookevent)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `2XX` |  | Receiver durably accepted/deduplicated this event. |
| `default` |  | Transport retry policy applies; do not create another Match. |

## Schemas

### `SourceIdentity`

Defined in `contracts/shared/v0/manifest.schema.json`, which other contracts share.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `corpus_id` | string | yes | Minimum length `1`. |
| `namespace` | string | yes | Minimum length `1`. |
| `record_key` | string | yes | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  corpus_id:
    type: string
    minLength: 1
  namespace:
    type: string
    minLength: 1
  record_key:
    type: string
    minLength: 1
required:
  - corpus_id
  - namespace
  - record_key
```

</details>

### `Error`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `code` | string | yes | Minimum length `1`. |
| `message` | string | yes | Minimum length `1`. |
| `retryable` | boolean | yes |  |
| `field` | string |  | JSON Pointer (RFC 6901) to the request member that caused a 422, when known (for example /config/url or /credential/secret/token on connector commands). Minimum length `1`. |
| `resync_url` | string (uri-reference) |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  code:
    type: string
    minLength: 1
  message:
    type: string
    minLength: 1
  retryable:
    type: boolean
  field:
    type: string
    minLength: 1
    description: JSON Pointer (RFC 6901) to the request member that caused a 422, when known (for example /config/url or /credential/secret/token on connector commands).
  resync_url:
    type: string
    format: uri-reference
required:
  - code
  - message
  - retryable
```

</details>

### `Diagnostic`

A structured processing diagnostic. plugin, contribution and invocation_id name the external
invocation a normalization diagnostic concerns. Codes of external normalization:

- normalizer_failed: the normalizer answered a terminal error. The Version is quarantined.
- normalizer_invalid_output: the output broke the Plugin Protocol or the Manifest rules (schema,
  malformed or duplicate Part, a Blob Part that is not the input Blob or has another checksum,
  an undeclared extension namespace, too many Parts, a response over the size bound). Nothing
  from it is published; the Version is quarantined.
- normalizer_timeout: the invocations kept exceeding the timeout until the retry budget (the
  manifest's retry.max_attempts, capped by the engine at 5) was spent. The Version is quarantined.
- normalizer_retries_exhausted: the normalizer kept answering retryable errors until the retry
  budget was spent. The Version is quarantined.
- input_unverified: the input Blob was no longer the verified accepted input. The Version is
  quarantined.
- normalizer_unrouted: the media type's route was removed after acceptance and the built-in text
  path cannot read it (a text/* Blob takes the built-in text path instead). The Version is
  quarantined.
- normalization_superseded: a newer revision of the Record was accepted before this Version was
  normalized, so the normalizer was not invoked. The Version is quarantined and never current.
- normalizer_conflict: a later invocation with the same idempotency key returned a different
  output. The first recorded output is kept and published; nothing is overwritten.

On an optional route every quarantining code above that comes from the normalizer is instead
listed on a searchable Version published through the built-in text path. A plugin that is
unavailable (connection failure, 5xx without an error envelope, discovery that does not match the
pinned manifest) is retried with backoff and never produces a diagnostic here; the Receipt shows
plugin_unavailable while it retries. Quarantined Versions keep their input reference and reason;
reprocessing them is not available yet.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `code` | string | yes | Minimum length `1`. |
| `message` | string | yes | Minimum length `1`. |
| `retryable` | boolean | yes | Whether the same input may succeed if processed again. |
| `plugin` | string |  | Plugin id of the invocation. Minimum length `1`. |
| `contribution` | string |  | Minimum length `1`. |
| `invocation_id` | string |  | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
description: |-
  A structured processing diagnostic. plugin, contribution and invocation_id name the external
  invocation a normalization diagnostic concerns. Codes of external normalization:

  - normalizer_failed: the normalizer answered a terminal error. The Version is quarantined.
  - normalizer_invalid_output: the output broke the Plugin Protocol or the Manifest rules (schema,
    malformed or duplicate Part, a Blob Part that is not the input Blob or has another checksum,
    an undeclared extension namespace, too many Parts, a response over the size bound). Nothing
    from it is published; the Version is quarantined.
  - normalizer_timeout: the invocations kept exceeding the timeout until the retry budget (the
    manifest's retry.max_attempts, capped by the engine at 5) was spent. The Version is quarantined.
  - normalizer_retries_exhausted: the normalizer kept answering retryable errors until the retry
    budget was spent. The Version is quarantined.
  - input_unverified: the input Blob was no longer the verified accepted input. The Version is
    quarantined.
  - normalizer_unrouted: the media type's route was removed after acceptance and the built-in text
    path cannot read it (a text/* Blob takes the built-in text path instead). The Version is
    quarantined.
  - normalization_superseded: a newer revision of the Record was accepted before this Version was
    normalized, so the normalizer was not invoked. The Version is quarantined and never current.
  - normalizer_conflict: a later invocation with the same idempotency key returned a different
    output. The first recorded output is kept and published; nothing is overwritten.

  On an optional route every quarantining code above that comes from the normalizer is instead
  listed on a searchable Version published through the built-in text path. A plugin that is
  unavailable (connection failure, 5xx without an error envelope, discovery that does not match the
  pinned manifest) is retried with backoff and never produces a diagnostic here; the Receipt shows
  plugin_unavailable while it retries. Quarantined Versions keep their input reference and reason;
  reprocessing them is not available yet.
properties:
  code:
    type: string
    minLength: 1
  message:
    type: string
    minLength: 1
  retryable:
    type: boolean
    description: Whether the same input may succeed if processed again.
  plugin:
    type: string
    minLength: 1
    description: Plugin id of the invocation.
  contribution:
    type: string
    minLength: 1
  invocation_id:
    type: string
    minLength: 1
required:
  - code
  - message
  - retryable
```

</details>

### `Extensions`

Defined in `contracts/shared/v0/manifest.schema.json`, which other contracts share.

Keys are plugin namespaces. Data is validated against the installed schema version; source data is not a computed Annotation.

Type: map of object.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `*.schema_version` | string | yes | Minimum length `1`. |
| `*.data` | object | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties:
  type: object
  additionalProperties: false
  properties:
    schema_version:
      type: string
      minLength: 1
    data:
      type: object
      additionalProperties: true
  required:
    - schema_version
    - data
description: Keys are plugin namespaces. Data is validated against the installed schema version; source data is not a computed Annotation.
```

</details>

### `TextContent`

Defined in `contracts/shared/v0/manifest.schema.json`, which other contracts share.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `kind` | string | yes | One of `text`. |
| `text` | string | yes | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  kind:
    type: string
    enum:
      - text
  text:
    type: string
    minLength: 1
required:
  - kind
  - text
```

</details>

### `BlobContent`

Defined in `contracts/shared/v0/manifest.schema.json`, which other contracts share.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `kind` | string | yes | One of `blob`. |
| `blob_id` | string | yes | Minimum length `1`. |
| `media_type` | string | yes | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  kind:
    type: string
    enum:
      - blob
  blob_id:
    type: string
    minLength: 1
  media_type:
    type: string
    minLength: 1
required:
  - kind
  - blob_id
  - media_type
```

</details>

### `Part`

Defined in `contracts/shared/v0/manifest.schema.json`, which other contracts share.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `key` | string | yes | Minimum length `1`. |
| `parent_key` | string |  | Minimum length `1`. |
| `role` | string | yes | Minimum length `1`. |
| `content` | one of [`TextContent`](#textcontent), [`BlobContent`](#blobcontent) | yes |  |
| `extensions` | [`Extensions`](#extensions) |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  key:
    type: string
    minLength: 1
  parent_key:
    type: string
    minLength: 1
  role:
    type: string
    minLength: 1
  content:
    oneOf:
      - $ref: '#/components/schemas/TextContent'
      - $ref: '#/components/schemas/BlobContent'
  extensions:
    $ref: '#/components/schemas/Extensions'
required:
  - key
  - role
  - content
```

</details>

### `RelationInput`

Defined in `contracts/shared/v0/manifest.schema.json`, which other contracts share.

Source-provided link to an independently identified Record in the same Organization. Optional source revision preserves provenance; ordinary expansion resolves the current eligible target. Missing targets do not block readiness. Every expansion reauthorizes the target. Precise Part-target syntax remains outside this initial draft.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `type` | string | yes | Minimum length `1`. |
| `target` | [`SourceIdentity`](#sourceidentity) | yes |  |
| `source_target_revision` | string |  | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  type:
    type: string
    minLength: 1
  target:
    $ref: '#/components/schemas/SourceIdentity'
  source_target_revision:
    type: string
    minLength: 1
required:
  - type
  - target
description: Source-provided link to an independently identified Record in the same Organization. Optional source revision preserves provenance; ordinary expansion resolves the current eligible target. Missing targets do not block readiness. Every expansion reauthorizes the target. Precise Part-target syntax remains outside this initial draft.
```

</details>

### `ManifestContent`

Defined in `contracts/shared/v0/manifest.schema.json`, which other contracts share.

Unique Part keys, acyclic parent references within this Manifest, and verified same-Organization Blobs are checked before atomic publication. These semantic constraints need server validation in addition to JSON Schema.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `kind` | string | yes | One of `manifest`. |
| `parts` | array of [`Part`](#part) | yes | At least `1` items. |
| `relations` | array of [`RelationInput`](#relationinput) |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  kind:
    type: string
    enum:
      - manifest
  parts:
    type: array
    items:
      $ref: '#/components/schemas/Part'
    minItems: 1
  relations:
    type: array
    items:
      $ref: '#/components/schemas/RelationInput'
required:
  - kind
  - parts
description: Unique Part keys, acyclic parent references within this Manifest, and verified same-Organization Blobs are checked before atomic publication. These semantic constraints need server validation in addition to JSON Schema.
```

</details>

### `Provenance`

Defined in `contracts/shared/v0/manifest.schema.json`, which other contracts share.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `source_blob_ids` | array of string |  | Each item: Minimum length `1`. |
| `producer` | string |  | Minimum length `1`. |
| `producer_version` | string |  | Minimum length `1`. |
| `normalization` | [`NormalizationProvenance`](#normalizationprovenance) |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  source_blob_ids:
    type: array
    items:
      type: string
      minLength: 1
  producer:
    type: string
    minLength: 1
  producer_version:
    type: string
    minLength: 1
  normalization:
    $ref: '#/components/schemas/NormalizationProvenance'
required: []
```

</details>

### `NormalizationProvenance`

Defined in `contracts/shared/v0/manifest.schema.json`, which other contracts share.

Engine-owned record of the external normalizer invocation whose output a Record Version publishes. Present only on read; a submission that sets it is rejected. producer and producer_version keep naming the acquirer, and source_blob_ids keeps the input Blob.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `plugin_id` | string | yes | Minimum length `1`. Maximum length `64`. |
| `plugin_version` | string | yes | Minimum length `1`. Maximum length `64`. |
| `plugin_api` | string | yes | Plugin API version the engine invoked. Minimum length `1`. Maximum length `32`. |
| `contribution` | string | yes | One of `normalizer`. |
| `invocation_id` | string | yes | Minimum length `1`. Maximum length `128`. |
| `idempotency_key` | string | yes | Minimum length `1`. Maximum length `256`. |
| `input_sha256` | string | yes | Pattern `^[0-9a-f]{64}$`. |
| `fallback` | object |  | Present when the route is optional and the normalizer failed: the published Manifest comes from the built-in text path, and invocation_id names the failed invocation. |
| `fallback.code` | string | yes | Minimum length `1`. Maximum length `64`. |
| `fallback.message` | string | yes | Minimum length `1`. Maximum length `1000`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
description: Engine-owned record of the external normalizer invocation whose output a Record Version publishes. Present only on read; a submission that sets it is rejected. producer and producer_version keep naming the acquirer, and source_blob_ids keeps the input Blob.
properties:
  plugin_id:
    type: string
    minLength: 1
    maxLength: 64
  plugin_version:
    type: string
    minLength: 1
    maxLength: 64
  plugin_api:
    type: string
    minLength: 1
    maxLength: 32
    description: Plugin API version the engine invoked.
  contribution:
    type: string
    enum:
      - normalizer
  invocation_id:
    type: string
    minLength: 1
    maxLength: 128
  idempotency_key:
    type: string
    minLength: 1
    maxLength: 256
  input_sha256:
    type: string
    pattern: ^[0-9a-f]{64}$
  fallback:
    type: object
    additionalProperties: false
    description: 'Present when the route is optional and the normalizer failed: the published Manifest comes from the built-in text path, and invocation_id names the failed invocation.'
    properties:
      code:
        type: string
        minLength: 1
        maxLength: 64
      message:
        type: string
        minLength: 1
        maxLength: 1000
    required:
      - code
      - message
required:
  - plugin_id
  - plugin_version
  - plugin_api
  - contribution
  - invocation_id
  - idempotency_key
  - input_sha256
```

</details>

### `IngestCommand`

Initial request shape. Same source identity creates or corrects a Record. Same external revision with different canonical content conflicts. No revision means canonical Manifest digest identity; no source position means durable acceptance order. Single and batch entry replay share route_family=ingestion. A blob content accepts a verified text/* Blob, read at acceptance, or a Blob whose media type the installation routes to an external normalizer; that normalizer runs after acceptance and its output is the published Manifest, while the Version identity still derives from the submitted Blob. When the normalizer fails, the Version is quarantined with a Diagnostic on the Version read (or, on an optional text/* route, published through the built-in text path with provenance.normalization.fallback). Other media types are rejected with unverified_blob. provenance.normalization is engine-owned and rejected on input. Extension namespaces owned by the pinned plugin are written only by its normalizer output, published on the Version; a submission writing one, top-level or on a Part, is rejected with 422 extension_namespace_owned.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `idempotency_key` | string | yes | Minimum length `1`. |
| `source` | [`SourceIdentity`](#sourceidentity) | yes |  |
| `source_revision` | string |  | Minimum length `1`. |
| `source_position` | string |  | Optional monotonic source position, encoded as decimal text to avoid JSON numeric precision loss. Pattern `^[0-9]+$`. |
| `content` | one of [`TextContent`](#textcontent), [`BlobContent`](#blobcontent), [`ManifestContent`](#manifestcontent) | yes |  |
| `extensions` | [`Extensions`](#extensions) |  |  |
| `provenance` | [`Provenance`](#provenance) |  |  |

Example `structured_inline`:

```json
{
  "idempotency_key": "import-42:item-7",
  "source": {
    "corpus_id": "corpus_news",
    "namespace": "example-feed",
    "record_key": "article-123"
  },
  "source_revision": "2",
  "content": {
    "kind": "text",
    "text": "Le texte de la dépêche…"
  },
  "extensions": {
    "example.editorial": {
      "schema_version": "1",
      "data": {
        "headline": "Titre échantillon",
        "subjects": [
          {
            "code": "science",
            "score": 0.75
          }
        ],
        "flags": {
          "urgent": true
        },
        "extra": null
      }
    }
  }
}
```

Example `structured_manifest`:

```json
{
  "idempotency_key": "import-42:item-7",
  "source": {
    "corpus_id": "corpus_news",
    "namespace": "example-feed",
    "record_key": "article-123"
  },
  "source_revision": "2",
  "content": {
    "kind": "manifest",
    "parts": [
      {
        "key": "body",
        "role": "body",
        "content": {
          "kind": "text",
          "text": "Texte"
        },
        "extensions": {
          "example.editorial": {
            "schema_version": "1",
            "data": {
              "headline": "Titre échantillon",
              "subjects": [
                {
                  "code": "science",
                  "score": 0.75
                }
              ],
              "flags": {
                "urgent": true
              },
              "extra": null
            }
          }
        }
      },
      {
        "key": "source",
        "role": "source",
        "content": {
          "kind": "blob",
          "blob_id": "blob_original",
          "media_type": "application/xml"
        }
      }
    ],
    "relations": [
      {
        "type": "illustrated_by",
        "target": {
          "corpus_id": "corpus_news",
          "namespace": "example-feed",
          "record_key": "photo-8"
        },
        "source_target_revision": "1"
      }
    ]
  },
  "extensions": {
    "example.editorial": {
      "schema_version": "1",
      "data": {
        "headline": "Titre échantillon",
        "subjects": [
          {
            "code": "science",
            "score": 0.75
          }
        ],
        "flags": {
          "urgent": true
        },
        "extra": null
      }
    }
  },
  "provenance": {
    "source_blob_ids": [
      "blob_original"
    ],
    "producer": "example.plugin",
    "producer_version": "sha256:pinned"
  }
}
```

Example `blob_input`:

```json
{
  "idempotency_key": "import-42:item-7",
  "source": {
    "corpus_id": "corpus_news",
    "namespace": "example-feed",
    "record_key": "article-123"
  },
  "source_revision": "2",
  "content": {
    "kind": "blob",
    "blob_id": "blob_text",
    "media_type": "text/plain"
  },
  "extensions": {
    "example.editorial": {
      "schema_version": "1",
      "data": {
        "headline": "Titre échantillon",
        "subjects": [
          {
            "code": "science",
            "score": 0.75
          }
        ],
        "flags": {
          "urgent": true
        },
        "extra": null
      }
    }
  }
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  idempotency_key:
    type: string
    minLength: 1
  source:
    $ref: '#/components/schemas/SourceIdentity'
  source_revision:
    type: string
    minLength: 1
  source_position:
    type: string
    pattern: ^[0-9]+$
    description: Optional monotonic source position, encoded as decimal text to avoid JSON numeric precision loss.
  content:
    oneOf:
      - $ref: '#/components/schemas/TextContent'
      - $ref: '#/components/schemas/BlobContent'
      - $ref: '#/components/schemas/ManifestContent'
    discriminator:
      propertyName: kind
      mapping:
        text: '#/components/schemas/TextContent'
        blob: '#/components/schemas/BlobContent'
        manifest: '#/components/schemas/ManifestContent'
  extensions:
    $ref: '#/components/schemas/Extensions'
  provenance:
    $ref: '#/components/schemas/Provenance'
required:
  - idempotency_key
  - source
  - content
description: Initial request shape. Same source identity creates or corrects a Record. Same external revision with different canonical content conflicts. No revision means canonical Manifest digest identity; no source position means durable acceptance order. Single and batch entry replay share route_family=ingestion. A blob content accepts a verified text/* Blob, read at acceptance, or a Blob whose media type the installation routes to an external normalizer; that normalizer runs after acceptance and its output is the published Manifest, while the Version identity still derives from the submitted Blob. When the normalizer fails, the Version is quarantined with a Diagnostic on the Version read (or, on an optional text/* route, published through the built-in text path with provenance.normalization.fallback). Other media types are rejected with unverified_blob. provenance.normalization is engine-owned and rejected on input. Extension namespaces owned by the pinned plugin are written only by its normalizer output, published on the Version; a submission writing one, top-level or on a Part, is rejected with 422 extension_namespace_owned.
```

</details>

### `WithdrawalCommand`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `idempotency_key` | string | yes | Minimum length `1`. |
| `source` | [`SourceIdentity`](#sourceidentity) | yes |  |
| `reason` | string |  | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  idempotency_key:
    type: string
    minLength: 1
  source:
    $ref: '#/components/schemas/SourceIdentity'
  reason:
    type: string
    minLength: 1
required:
  - idempotency_key
  - source
```

</details>

### `Availability`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `state` | string | yes | One of `materialized`, `building_baseline`, `retrieval_ready`, `quarantined`. |
| `is_current` | boolean | yes |  |
| `searchable` | boolean | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  state:
    type: string
    enum:
      - materialized
      - building_baseline
      - retrieval_ready
      - quarantined
  is_current:
    type: boolean
  searchable:
    type: boolean
required:
  - state
  - is_current
  - searchable
```

</details>

### `Receipt`

Durable acceptance outcome, not workflow state. Availability is a separate authorized live read view; omitted before a linked version exists. Infrastructure retry never resolves a Receipt as failed.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `receipt_id` | string | yes | Minimum length `1`. |
| `state` | string | yes | One of `pending`, `resolved`. |
| `outcome` | string |  | One of `created`, `duplicate`, `withdrawal_applied`, `conflict`. |
| `record_id` | string |  | Minimum length `1`. |
| `version_id` | string |  | Minimum length `1`. |
| `availability` | [`Availability`](#availability) |  |  |
| `diagnostics` | array of [`Error`](#error) | yes | At most `20` items. |
| `source` | [`SourceIdentity`](#sourceidentity) | yes |  |
| `processing` | [`ProcessingSummary`](#processingsummary) | yes |  |

Further rules (conditional requirements or combinations) are in the full schema below.

Example `pending_receipt`:

```json
{
  "receipt_id": "receipt_1",
  "source": {
    "corpus_id": "corpus_news",
    "namespace": "example-feed",
    "record_key": "article-123"
  },
  "state": "pending",
  "processing": {
    "state": "queued",
    "phase": "materialization"
  },
  "diagnostics": []
}
```

Example `resolved_receipt`:

```json
{
  "receipt_id": "receipt_1",
  "source": {
    "corpus_id": "corpus_news",
    "namespace": "example-feed",
    "record_key": "article-123"
  },
  "state": "resolved",
  "processing": {
    "state": "running",
    "phase": "baseline"
  },
  "diagnostics": [],
  "outcome": "created",
  "record_id": "record_1",
  "version_id": "version_2",
  "availability": {
    "state": "building_baseline",
    "is_current": false,
    "searchable": false
  }
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  receipt_id:
    type: string
    minLength: 1
  state:
    type: string
    enum:
      - pending
      - resolved
  outcome:
    type: string
    enum:
      - created
      - duplicate
      - withdrawal_applied
      - conflict
  record_id:
    type: string
    minLength: 1
  version_id:
    type: string
    minLength: 1
  availability:
    $ref: '#/components/schemas/Availability'
  diagnostics:
    type: array
    items:
      $ref: '#/components/schemas/Error'
    maxItems: 20
  source:
    $ref: '#/components/schemas/SourceIdentity'
  processing:
    $ref: '#/components/schemas/ProcessingSummary'
required:
  - receipt_id
  - state
  - diagnostics
  - source
  - processing
description: Durable acceptance outcome, not workflow state. Availability is a separate authorized live read view; omitted before a linked version exists. Infrastructure retry never resolves a Receipt as failed.
if:
  properties:
    state:
      const: pending
then:
  not:
    required:
      - outcome
else:
  required:
    - outcome
```

</details>

### `BatchRequest`

Envelope validation checks items array and batch/body limits only. Each raw entry is independently validated as IngestCommand; even a missing required field or wrong JSON type returns an entry error. SDK convenience builders may type valid entries as IngestCommand, but server envelope validation must not reject the entire batch for an invalid entry.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `items` | array of any | yes | At least `1` items. At most `100` items. |

Example `mixed_batch_input`:

```json
{
  "items": [
    {
      "idempotency_key": "import-42:item-7",
      "source": {
        "corpus_id": "corpus_news",
        "namespace": "example-feed",
        "record_key": "article-123"
      },
      "source_revision": "2",
      "content": {
        "kind": "text",
        "text": "Le texte de la dépêche…"
      },
      "extensions": {
        "example.editorial": {
          "schema_version": "1",
          "data": {
            "headline": "Titre échantillon",
            "subjects": [
              {
                "code": "science",
                "score": 0.75
              }
            ],
            "flags": {
              "urgent": true
            },
            "extra": null
          }
        }
      }
    },
    {
      "source": {
        "corpus_id": "corpus_news",
        "namespace": "example-feed",
        "record_key": "article-123"
      }
    },
    42
  ]
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  items:
    type: array
    items: {}
    minItems: 1
    maxItems: 100
required:
  - items
description: Envelope validation checks items array and batch/body limits only. Each raw entry is independently validated as IngestCommand; even a missing required field or wrong JSON type returns an entry error. SDK convenience builders may type valid entries as IngestCommand, but server envelope validation must not reject the entire batch for an invalid entry.
```

</details>

### `BatchItem`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `index` | integer | yes | Minimum `0`. |
| `receipt` | [`Receipt`](#receipt) |  |  |
| `error` | [`Error`](#error) |  |  |

Further rules (conditional requirements or combinations) are in the full schema below.

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  index:
    type: integer
    minimum: 0
  receipt:
    $ref: '#/components/schemas/Receipt'
  error:
    $ref: '#/components/schemas/Error'
required:
  - index
if:
  required:
    - receipt
then:
  not:
    required:
      - error
else:
  required:
    - error
```

</details>

### `BatchResult`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `items` | array of [`BatchItem`](#batchitem) | yes | At most `100` items. |

Example `mixed_batch_result`:

```json
{
  "items": [
    {
      "index": 0,
      "receipt": {
        "receipt_id": "receipt_1",
        "source": {
          "corpus_id": "corpus_news",
          "namespace": "example-feed",
          "record_key": "article-123"
        },
        "state": "pending",
        "processing": {
          "state": "queued",
          "phase": "materialization"
        },
        "diagnostics": []
      }
    },
    {
      "index": 1,
      "error": {
        "code": "invalid_schema",
        "message": "invalid schema",
        "retryable": false
      }
    },
    {
      "index": 2,
      "error": {
        "code": "invalid_schema",
        "message": "invalid schema",
        "retryable": false
      }
    }
  ]
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  items:
    type: array
    items:
      $ref: '#/components/schemas/BatchItem'
    maxItems: 100
required:
  - items
```

</details>

### `Record`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `record_id` | string | yes | Minimum length `1`. |
| `source` | [`SourceIdentity`](#sourceidentity) | yes |  |
| `withdrawn` | boolean | yes |  |
| `current_version_id` | string |  | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  record_id:
    type: string
    minLength: 1
  source:
    $ref: '#/components/schemas/SourceIdentity'
  withdrawn:
    type: boolean
  current_version_id:
    type: string
    minLength: 1
required:
  - record_id
  - source
  - withdrawn
```

</details>

### `Version`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `record_id` | string | yes | Minimum length `1`. |
| `version_id` | string | yes | Minimum length `1`. |
| `manifest` | [`ManifestContent`](#manifestcontent) | yes |  |
| `extensions` | [`Extensions`](#extensions) |  |  |
| `provenance` | [`Provenance`](#provenance) |  |  |
| `availability` | [`Availability`](#availability) | yes |  |
| `relations` | array of [`ResolvedRelation`](#resolvedrelation) | yes |  |
| `processing` | [`ProcessingSummary`](#processingsummary) | yes |  |
| `diagnostics` | array of [`Diagnostic`](#diagnostic) |  | Why the Version needs attention. A quarantined Version lists its reason first. A Version published through an optional route's fallback lists the normalizer failure it fell back from, and a recorded normalizer_conflict is listed on the Version whose output was kept. Omitted when there is nothing to report. At most `20` items. |

Example `version_relations`:

```json
{
  "record_id": "record_1",
  "version_id": "version_2",
  "manifest": {
    "kind": "manifest",
    "parts": [
      {
        "key": "body",
        "role": "body",
        "content": {
          "kind": "text",
          "text": "Texte"
        },
        "extensions": {
          "example.editorial": {
            "schema_version": "1",
            "data": {
              "headline": "Titre échantillon",
              "subjects": [
                {
                  "code": "science",
                  "score": 0.75
                }
              ],
              "flags": {
                "urgent": true
              },
              "extra": null
            }
          }
        }
      },
      {
        "key": "source",
        "role": "source",
        "content": {
          "kind": "blob",
          "blob_id": "blob_original",
          "media_type": "application/xml"
        }
      }
    ],
    "relations": [
      {
        "type": "illustrated_by",
        "target": {
          "corpus_id": "corpus_news",
          "namespace": "example-feed",
          "record_key": "photo-8"
        },
        "source_target_revision": "1"
      }
    ]
  },
  "extensions": {
    "example.editorial": {
      "schema_version": "1",
      "data": {
        "headline": "Titre échantillon",
        "subjects": [
          {
            "code": "science",
            "score": 0.75
          }
        ],
        "flags": {
          "urgent": true
        },
        "extra": null
      }
    }
  },
  "availability": {
    "state": "retrieval_ready",
    "is_current": true,
    "searchable": true
  },
  "processing": {
    "state": "idle"
  },
  "relations": [
    {
      "source_reference": {
        "type": "illustrated_by",
        "target": {
          "corpus_id": "corpus_news",
          "namespace": "example-feed",
          "record_key": "photo-8"
        },
        "source_target_revision": "1"
      },
      "status": "available",
      "target_record_id": "photo_record",
      "target_version_id": "photo_version_2"
    },
    {
      "source_reference": {
        "type": "illustrated_by",
        "target": {
          "corpus_id": "corpus_news",
          "namespace": "example-feed",
          "record_key": "photo-missing"
        },
        "source_target_revision": "1"
      },
      "status": "unavailable"
    }
  ]
}
```

Example `quarantined_version`:

```json
{
  "record_id": "record_2",
  "version_id": "version_3",
  "manifest": {
    "kind": "manifest",
    "parts": [
      {
        "key": "source",
        "role": "source",
        "content": {
          "kind": "blob",
          "blob_id": "blob_markdown",
          "media_type": "text/markdown"
        }
      }
    ]
  },
  "provenance": {
    "source_blob_ids": [
      "blob_markdown"
    ]
  },
  "availability": {
    "state": "quarantined",
    "is_current": false,
    "searchable": false
  },
  "processing": {
    "state": "blocked",
    "phase": "baseline"
  },
  "relations": [],
  "diagnostics": [
    {
      "code": "normalizer_invalid_output",
      "message": "invalid normalizer output: duplicate Part key",
      "retryable": false,
      "plugin": "example.markdown",
      "contribution": "normalizer",
      "invocation_id": "inv_1"
    }
  ]
}
```

Example `fallback_version`:

```json
{
  "record_id": "record_3",
  "version_id": "version_4",
  "manifest": {
    "kind": "manifest",
    "parts": [
      {
        "key": "body",
        "role": "body",
        "content": {
          "kind": "text",
          "text": "# Title\n\nBody"
        }
      }
    ]
  },
  "provenance": {
    "source_blob_ids": [
      "blob_markdown_2"
    ],
    "normalization": {
      "plugin_id": "example.markdown",
      "plugin_version": "1.0.0",
      "plugin_api": "0.1.0",
      "contribution": "normalizer",
      "invocation_id": "inv_2",
      "idempotency_key": "nk_1",
      "input_sha256": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
      "fallback": {
        "code": "normalizer_failed",
        "message": "The normalizer refused the input with unreadable: cannot read"
      }
    }
  },
  "availability": {
    "state": "retrieval_ready",
    "is_current": true,
    "searchable": true
  },
  "processing": {
    "state": "idle"
  },
  "relations": [],
  "diagnostics": [
    {
      "code": "normalizer_failed",
      "message": "The normalizer refused the input with unreadable: cannot read",
      "retryable": false,
      "plugin": "example.markdown",
      "contribution": "normalizer",
      "invocation_id": "inv_2"
    }
  ]
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  record_id:
    type: string
    minLength: 1
  version_id:
    type: string
    minLength: 1
  manifest:
    $ref: '#/components/schemas/ManifestContent'
  extensions:
    $ref: '#/components/schemas/Extensions'
  provenance:
    $ref: '#/components/schemas/Provenance'
  availability:
    $ref: '#/components/schemas/Availability'
  relations:
    type: array
    items:
      $ref: '#/components/schemas/ResolvedRelation'
  processing:
    $ref: '#/components/schemas/ProcessingSummary'
  diagnostics:
    type: array
    items:
      $ref: '#/components/schemas/Diagnostic'
    maxItems: 20
    description: Why the Version needs attention. A quarantined Version lists its reason first. A Version published through an optional route's fallback lists the normalizer failure it fell back from, and a recorded normalizer_conflict is listed on the Version whose output was kept. Omitted when there is nothing to report.
required:
  - record_id
  - version_id
  - manifest
  - availability
  - relations
  - processing
```

</details>

### `UploadRequest`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `size_bytes` | integer | yes | Minimum `1`. Maximum `1073741824`. |
| `sha256` | string | yes | Pattern `^[a-f0-9]{64}$`. |
| `media_type` | string | yes | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  size_bytes:
    type: integer
    minimum: 1
    maximum: 1073741824
  sha256:
    type: string
    pattern: ^[a-f0-9]{64}$
  media_type:
    type: string
    minLength: 1
required:
  - size_bytes
  - sha256
  - media_type
```

</details>

### `Upload`

Upload URL and headers are transfer capabilities. Only verified uploads expose a usable Blob ID. Repeated confirmation of the same session observes the same verification, never a second upload.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `upload_id` | string | yes | Minimum length `1`. |
| `state` | string | yes | One of `awaiting_upload`, `verifying`, `verified`, `rejected`, `expired`. |
| `upload_url` | string (uri) |  |  |
| `upload_headers` | map of string |  |  |
| `expires_at` | string (date-time) |  |  |
| `blob_id` | string |  | Minimum length `1`. |
| `error` | [`Error`](#error) |  |  |
| `upload_method` | string |  | One of `PUT`. |

Further rules (conditional requirements or combinations) are in the full schema below.

Example `verified_upload`:

```json
{
  "upload_id": "upload_1",
  "state": "verified",
  "blob_id": "blob_text"
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  upload_id:
    type: string
    minLength: 1
  state:
    type: string
    enum:
      - awaiting_upload
      - verifying
      - verified
      - rejected
      - expired
  upload_url:
    type: string
    format: uri
  upload_headers:
    type: object
    additionalProperties:
      type: string
  expires_at:
    type: string
    format: date-time
  blob_id:
    type: string
    minLength: 1
  error:
    $ref: '#/components/schemas/Error'
  upload_method:
    type: string
    enum:
      - PUT
required:
  - upload_id
  - state
description: Upload URL and headers are transfer capabilities. Only verified uploads expose a usable Blob ID. Repeated confirmation of the same session observes the same verification, never a second upload.
allOf:
  - if:
      properties:
        state:
          const: verified
    then:
      required:
        - blob_id
    else:
      not:
        required:
          - blob_id
```

</details>

### `Blob`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `blob_id` | string | yes | Minimum length `1`. |
| `size_bytes` | integer | yes | Minimum `1`. |
| `sha256` | string | yes | Pattern `^[a-f0-9]{64}$`. |
| `media_type` | string | yes | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  blob_id:
    type: string
    minLength: 1
  size_bytes:
    type: integer
    minimum: 1
  sha256:
    type: string
    pattern: ^[a-f0-9]{64}$
  media_type:
    type: string
    minLength: 1
required:
  - blob_id
  - size_bytes
  - sha256
  - media_type
```

</details>

### `Operation`

Administrative execution only. Retries keep identity. Intentional terminal rerun has a new ID and previous_operation_id. Cancellation does not promise universal rollback; already-terminal state and racing completion may win. projection_rebuild and retrieval_configuration Operations require corpus_id; when succeeded they require result naming the activated logical generation. This result shape covers those two command kinds only.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `operation_id` | string | yes | Minimum length `1`. |
| `kind` | string | yes | Minimum length `1`. |
| `state` | string | yes | One of `queued`, `running`, `succeeded`, `failed`, `cancel_requested`, `canceled`. |
| `progress` | number |  | Approximate fraction, omitted when unknown. Minimum `0`. Maximum `1`. |
| `counters` | map of integer | yes |  |
| `errors` | array of [`Error`](#error) | yes | At most `20` items. |
| `previous_operation_id` | string |  | Minimum length `1`. |
| `corpus_id` | string |  | Minimum length `1`. |
| `result` | [`ProjectionRebuildResult`](#projectionrebuildresult) |  |  |

Further rules (conditional requirements or combinations) are in the full schema below.

Example `administrative_operation`:

```json
{
  "operation_id": "op_1",
  "kind": "projection_rebuild",
  "state": "cancel_requested",
  "counters": {
    "processed": 42
  },
  "errors": [],
  "corpus_id": "corpus_news"
}
```

Example `rebuild_queued`:

```json
{
  "operation_id": "operation_1",
  "kind": "projection_rebuild",
  "corpus_id": "corpus_news",
  "state": "queued",
  "counters": {},
  "errors": []
}
```

Example `rebuild_succeeded`:

```json
{
  "operation_id": "operation_1",
  "kind": "projection_rebuild",
  "corpus_id": "corpus_news",
  "state": "succeeded",
  "counters": {
    "indexed": 24
  },
  "errors": [],
  "result": {
    "projection_generation_id": "generation_2"
  }
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  operation_id:
    type: string
    minLength: 1
  kind:
    type: string
    minLength: 1
  state:
    type: string
    enum:
      - queued
      - running
      - succeeded
      - failed
      - cancel_requested
      - canceled
  progress:
    type: number
    minimum: 0
    maximum: 1
    description: Approximate fraction, omitted when unknown.
  counters:
    type: object
    additionalProperties:
      type: integer
      minimum: 0
  errors:
    type: array
    items:
      $ref: '#/components/schemas/Error'
    maxItems: 20
  previous_operation_id:
    type: string
    minLength: 1
  corpus_id:
    type: string
    minLength: 1
  result:
    $ref: '#/components/schemas/ProjectionRebuildResult'
required:
  - operation_id
  - kind
  - state
  - counters
  - errors
description: Administrative execution only. Retries keep identity. Intentional terminal rerun has a new ID and previous_operation_id. Cancellation does not promise universal rollback; already-terminal state and racing completion may win. projection_rebuild and retrieval_configuration Operations require corpus_id; when succeeded they require result naming the activated logical generation. This result shape covers those two command kinds only.
if:
  properties:
    kind:
      enum:
        - projection_rebuild
        - retrieval_configuration
  required:
    - kind
then:
  required:
    - corpus_id
  if:
    properties:
      state:
        const: succeeded
    required:
      - state
  then:
    required:
      - result
```

</details>

### `FieldMapping`

v0 logical field mapping. name is a logical name matching ^[a-z][a-z0-9_]{0,63}$, never a search-engine field name. source_pointer is an RFC 6901 JSON Pointer into the canonical source view of a Version, rooted at /manifest, /provenance or /extensions/{namespace} with a declared namespace (built in, or owned by the startup-pinned plugin); other roots are rejected as invalid_mapping. Core validates role/type compatibility (search requires string or string_array). A search field named title replaces the projected title; other search fields add text once per Record Version. Filter roles are validated and preserved; no public filter API consumes them in v0.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | yes | Minimum length `1`. |
| `source_pointer` | string | yes | Minimum length `1`. |
| `type` | string | yes | One of `string`, `number`, `boolean`, `datetime`, `string_array`. |
| `roles` | array of string | yes | At least `1` items. Each item: One of `search`, `filter`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  name:
    type: string
    minLength: 1
  source_pointer:
    type: string
    minLength: 1
  type:
    type: string
    enum:
      - string
      - number
      - boolean
      - datetime
      - string_array
  roles:
    type: array
    items:
      type: string
      enum:
        - search
        - filter
    minItems: 1
required:
  - name
  - source_pointer
  - type
  - roles
description: v0 logical field mapping. name is a logical name matching ^[a-z][a-z0-9_]{0,63}$, never a search-engine field name. source_pointer is an RFC 6901 JSON Pointer into the canonical source view of a Version, rooted at /manifest, /provenance or /extensions/{namespace} with a declared namespace (built in, or owned by the startup-pinned plugin); other roots are rejected as invalid_mapping. Core validates role/type compatibility (search requires string or string_array). A search field named title replaces the projected title; other search fields add text once per Record Version. Filter roles are validated and preserved; no public filter API consumes them in v0.
```

</details>

### `RetrievalConfig`

Pin a plugin-provided profile when resolving config. Explicit fields override default fields by logical name; unmapped source data remains preserved. getCorpus returns the effective resolved fields. The only built-in profile, example.editorial, is illustrative (paired with the example extension namespace), not a product default; an uninstalled profile is 422 unsupported_profile.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `plugin_profile` | string |  | Minimum length `1`. |
| `fields` | array of [`FieldMapping`](#fieldmapping) |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  plugin_profile:
    type: string
    minLength: 1
  fields:
    type: array
    items:
      $ref: '#/components/schemas/FieldMapping'
required: []
description: Pin a plugin-provided profile when resolving config. Explicit fields override default fields by logical name; unmapped source data remains preserved. getCorpus returns the effective resolved fields. The only built-in profile, example.editorial, is illustrative (paired with the example extension namespace), not a product default; an uninstalled profile is 422 unsupported_profile.
```

</details>

### `ConnectorKind`

Built-in connector kind. fixture is a deterministic test connector available only when the deployment enables it. rss collects RSS 2.0, RSS 1.0, Atom and JSON Feed documents (config url, optional honor_ttl; optional credential username+password or token). m365_mail collects Microsoft 365 mailboxes. x_list polls an X list. Other kinds are refused with 422 unsupported_connector_kind until they are delivered.

Type: string. One of `fixture`, `rss`, `m365_mail`, `x_list`.

<details>
<summary>Full schema</summary>

```yaml
type: string
enum:
  - fixture
  - rss
  - m365_mail
  - x_list
description: Built-in connector kind. fixture is a deterministic test connector available only when the deployment enables it. rss collects RSS 2.0, RSS 1.0, Atom and JSON Feed documents (config url, optional honor_ttl; optional credential username+password or token). m365_mail collects Microsoft 365 mailboxes. x_list polls an X list. Other kinds are refused with 422 unsupported_connector_kind until they are delivered.
```

</details>

### `CredentialDeposit`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `secret` | object | yes | Kind-specific secret, validated by the kind's credential JSON Schema, encrypted at rest with the deployment credential key and never returned or logged. Without a configured credential key the request is refused with 503 credentials_unavailable. Write-only. |
| `expires_at` | string (date-time) |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  secret:
    type: object
    writeOnly: true
    description: Kind-specific secret, validated by the kind's credential JSON Schema, encrypted at rest with the deployment credential key and never returned or logged. Without a configured credential key the request is refused with 503 credentials_unavailable.
  expires_at:
    type: string
    format: date-time
required:
  - secret
```

</details>

### `CredentialReplace`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `idempotency_key` | string | yes | Minimum length `1`. |
| `secret` | object | yes | Write-only. |
| `expires_at` | string (date-time) |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  idempotency_key:
    type: string
    minLength: 1
  secret:
    type: object
    writeOnly: true
  expires_at:
    type: string
    format: date-time
required:
  - idempotency_key
  - secret
```

</details>

### `ConnectorSchedule`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `interval_seconds` | integer |  | Polling interval. Defaults per kind (fixture/rss 300, m365_mail 60, x_list 120); values below the deployment floor (30 s by default) are 422 invalid_interval. Minimum `1`. Maximum `86400`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  interval_seconds:
    type: integer
    minimum: 1
    maximum: 86400
    description: Polling interval. Defaults per kind (fixture/rss 300, m365_mail 60, x_list 120); values below the deployment floor (30 s by default) are 422 invalid_interval.
```

</details>

### `ScheduleChange`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `interval_seconds` | integer | yes | Seconds between runs. Bounds are the deployment floor and 86400; values outside them are 422 invalid_interval with field /interval_seconds. |

Example `connector_schedule_change`:

```json
{
  "interval_seconds": 600
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  interval_seconds:
    type: integer
    description: Seconds between runs. Bounds are the deployment floor and 86400; values outside them are 422 invalid_interval with field /interval_seconds.
required:
  - interval_seconds
```

</details>

### `ConnectorKindDescription`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `kind` | string | yes | Kind identifier accepted by createConnector for this deployment. A plain string so kinds added by future providers need no contract change here. Minimum length `1`. |
| `title` | string | yes | Display name, from the config schema's title annotation (the kind when absent). Minimum length `1`. |
| `description` | string |  | Short explanation, from the config schema's description annotation. |
| `config_schema` | object | yes | JSON Schema (2020-12) validating config. Annotations (title, description, examples) are informational. |
| `credential_schema` | object |  | JSON Schema validating credential.secret. Members annotated writeOnly are secrets. Absent when the kind takes no credential. |
| `credential` | string | yes | Whether an instance of this kind takes, may take or needs a Deposited Credential. One of `none`, `optional`, `required`. |
| `default_interval_seconds` | integer | yes | Minimum `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  kind:
    type: string
    minLength: 1
    description: Kind identifier accepted by createConnector for this deployment. A plain string so kinds added by future providers need no contract change here.
  title:
    type: string
    minLength: 1
    description: Display name, from the config schema's title annotation (the kind when absent).
  description:
    type: string
    description: Short explanation, from the config schema's description annotation.
  config_schema:
    type: object
    description: JSON Schema (2020-12) validating config. Annotations (title, description, examples) are informational.
  credential_schema:
    type: object
    description: JSON Schema validating credential.secret. Members annotated writeOnly are secrets. Absent when the kind takes no credential.
  credential:
    type: string
    enum:
      - none
      - optional
      - required
    description: Whether an instance of this kind takes, may take or needs a Deposited Credential.
  default_interval_seconds:
    type: integer
    minimum: 1
required:
  - kind
  - title
  - config_schema
  - credential
  - default_interval_seconds
```

</details>

### `ConnectorKindCatalog`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `credential_deposits` | string | yes | unavailable on a deployment without a credential key, where credential deposits and rotations are refused with 503 credentials_unavailable. One of `available`, `unavailable`. |
| `min_interval_seconds` | integer | yes | Shortest polling interval this deployment accepts. Minimum `1`. |
| `items` | array of [`ConnectorKindDescription`](#connectorkinddescription) | yes |  |

Example `connector_kinds`:

```json
{
  "credential_deposits": "available",
  "min_interval_seconds": 30,
  "items": [
    {
      "kind": "rss",
      "title": "RSS or Atom feed",
      "description": "Collects the entries of an RSS 2.0, RSS 1.0, Atom or JSON Feed document.",
      "config_schema": {
        "type": "object",
        "additionalProperties": false,
        "required": [
          "url"
        ],
        "properties": {
          "url": {
            "type": "string",
            "title": "Feed URL",
            "examples": [
              "https://example.org/feed.xml"
            ]
          },
          "honor_ttl": {
            "type": "boolean",
            "title": "Honor the feed's TTL"
          }
        }
      },
      "credential_schema": {
        "oneOf": [
          {
            "title": "Bearer token",
            "type": "object",
            "required": [
              "token"
            ],
            "properties": {
              "token": {
                "type": "string",
                "writeOnly": true
              }
            }
          }
        ]
      },
      "credential": "optional",
      "default_interval_seconds": 300
    }
  ]
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  credential_deposits:
    type: string
    enum:
      - available
      - unavailable
    description: unavailable on a deployment without a credential key, where credential deposits and rotations are refused with 503 credentials_unavailable.
  min_interval_seconds:
    type: integer
    minimum: 1
    description: Shortest polling interval this deployment accepts.
  items:
    type: array
    items:
      $ref: '#/components/schemas/ConnectorKindDescription'
required:
  - credential_deposits
  - min_interval_seconds
  - items
```

</details>

### `ConnectorHealthPolicy`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `silent_after_seconds` | integer |  | No new item for this long makes the source silent. Default 86400. Minimum `1`. Maximum `2592000`. |
| `credential_warning_seconds` | integer |  | A credential expiring within this window is credential_expiring. Default 1209600. Minimum `0`. Maximum `31536000`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  silent_after_seconds:
    type: integer
    minimum: 1
    maximum: 2592000
    description: No new item for this long makes the source silent. Default 86400.
  credential_warning_seconds:
    type: integer
    minimum: 0
    maximum: 31536000
    description: A credential expiring within this window is credential_expiring. Default 1209600.
```

</details>

### `ConnectorCreate`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `idempotency_key` | string | yes | Minimum length `1`. |
| `corpus_id` | string | yes | Minimum length `1`. |
| `source_namespace` | string | yes | Minimum length `1`. Maximum length `200`. |
| `kind` | [`ConnectorKind`](#connectorkind) | yes |  |
| `config` | object | yes | Kind-specific configuration validated by the kind's JSON Schema. Holds no secret. |
| `schedule` | [`ConnectorSchedule`](#connectorschedule) |  |  |
| `health_policy` | [`ConnectorHealthPolicy`](#connectorhealthpolicy) |  |  |
| `credential` | [`CredentialDeposit`](#credentialdeposit) |  |  |

Example `connector_create`:

```json
{
  "idempotency_key": "newsroom-wire-feed",
  "corpus_id": "corpus_news",
  "source_namespace": "newsroom-wire",
  "kind": "fixture",
  "config": {
    "script": []
  },
  "schedule": {
    "interval_seconds": 300
  },
  "health_policy": {
    "silent_after_seconds": 86400,
    "credential_warning_seconds": 1209600
  },
  "credential": {
    "secret": {
      "token": "fixture-test-token"
    },
    "expires_at": "2027-01-01T00:00:00Z"
  }
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  idempotency_key:
    type: string
    minLength: 1
  corpus_id:
    type: string
    minLength: 1
  source_namespace:
    type: string
    minLength: 1
    maxLength: 200
  kind:
    $ref: '#/components/schemas/ConnectorKind'
  config:
    type: object
    description: Kind-specific configuration validated by the kind's JSON Schema. Holds no secret.
  schedule:
    $ref: '#/components/schemas/ConnectorSchedule'
  health_policy:
    $ref: '#/components/schemas/ConnectorHealthPolicy'
  credential:
    $ref: '#/components/schemas/CredentialDeposit'
required:
  - idempotency_key
  - corpus_id
  - source_namespace
  - kind
  - config
```

</details>

### `CredentialMetadata`

Metadata of the current Deposited Credential; the secret itself is never returned.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `version` | integer | yes | Minimum `1`. |
| `deposited_at` | string (date-time) | yes |  |
| `expires_at` | string (date-time) |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  version:
    type: integer
    minimum: 1
  deposited_at:
    type: string
    format: date-time
  expires_at:
    type: string
    format: date-time
required:
  - version
  - deposited_at
description: Metadata of the current Deposited Credential; the secret itself is never returned.
```

</details>

### `ConnectorError`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `code` | string | yes | Minimum length `1`. |
| `at` | string (date-time) | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  code:
    type: string
    minLength: 1
  at:
    type: string
    format: date-time
required:
  - code
  - at
```

</details>

### `ConnectorUsage`

Per-UTC-day source read counters, present only for kinds that report reads.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `day` | string | yes | Current UTC calendar day (YYYY-MM-DD). Pattern `^[0-9]{4}-[0-9]{2}-[0-9]{2}$`. |
| `items_read` | integer | yes | Source resources read during the current UTC day, counted as the source bills them (for x_list, an estimate of billed post reads after X's per-UTC-day deduplication). Minimum `0`. |
| `previous_day_items_read` | integer | yes | Minimum `0`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  day:
    type: string
    pattern: ^[0-9]{4}-[0-9]{2}-[0-9]{2}$
    description: Current UTC calendar day (YYYY-MM-DD).
  items_read:
    type: integer
    minimum: 0
    description: Source resources read during the current UTC day, counted as the source bills them (for x_list, an estimate of billed post reads after X's per-UTC-day deduplication).
  previous_day_items_read:
    type: integer
    minimum: 0
required:
  - day
  - items_read
  - previous_day_items_read
description: Per-UTC-day source read counters, present only for kinds that report reads.
```

</details>

### `ConnectorHealth`

Last committed Connector Health, evaluated at each acquisition run, credential replacement and disable; evaluated_at shows its age. Precedence disabled, access_error, credential_expiring, silent, active. access_error means the source refused access (distinct from silent, which means no new item within the threshold). Other failures appear only as last_error.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `state` | string | yes | One of `active`, `silent`, `access_error`, `credential_expiring`, `disabled`. |
| `evaluated_at` | string (date-time) | yes |  |
| `last_success_at` | string (date-time) |  |  |
| `last_item_at` | string (date-time) |  |  |
| `last_error` | [`ConnectorError`](#connectorerror) |  |  |
| `usage` | [`ConnectorUsage`](#connectorusage) |  |  |
| `diagnostics` | object |  | Kind-defined diagnostics from the latest acquisition page, documented on the kind's operator guide page (for x_list, the deletion recheck coverage). Informational; never holds a secret or source content. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  state:
    type: string
    enum:
      - active
      - silent
      - access_error
      - credential_expiring
      - disabled
  evaluated_at:
    type: string
    format: date-time
  last_success_at:
    type: string
    format: date-time
  last_item_at:
    type: string
    format: date-time
  last_error:
    $ref: '#/components/schemas/ConnectorError'
  usage:
    $ref: '#/components/schemas/ConnectorUsage'
  diagnostics:
    type: object
    description: Kind-defined diagnostics from the latest acquisition page, documented on the kind's operator guide page (for x_list, the deletion recheck coverage). Informational; never holds a secret or source content.
required:
  - state
  - evaluated_at
description: Last committed Connector Health, evaluated at each acquisition run, credential replacement and disable; evaluated_at shows its age. Precedence disabled, access_error, credential_expiring, silent, active. access_error means the source refused access (distinct from silent, which means no new item within the threshold). Other failures appear only as last_error.
```

</details>

### `Connector`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `connector_id` | string | yes | Minimum length `1`. |
| `corpus_id` | string | yes | Minimum length `1`. |
| `source_namespace` | string | yes | Minimum length `1`. |
| `kind` | [`ConnectorKind`](#connectorkind) | yes |  |
| `config` | object | yes |  |
| `schedule` | object | yes |  |
| `schedule.interval_seconds` | integer | yes | Minimum `1`. |
| `health_policy` | object | yes |  |
| `health_policy.silent_after_seconds` | integer | yes | Minimum `1`. |
| `health_policy.credential_warning_seconds` | integer | yes | Minimum `0`. |
| `enabled` | boolean | yes |  |
| `created_at` | string (date-time) | yes |  |
| `disabled_at` | string (date-time) |  |  |
| `credential` | [`CredentialMetadata`](#credentialmetadata) |  |  |
| `health` | [`ConnectorHealth`](#connectorhealth) | yes |  |

Example `connector`:

```json
{
  "connector_id": "connector_1",
  "corpus_id": "corpus_news",
  "source_namespace": "newsroom-wire",
  "kind": "fixture",
  "config": {
    "script": []
  },
  "schedule": {
    "interval_seconds": 300
  },
  "health_policy": {
    "silent_after_seconds": 86400,
    "credential_warning_seconds": 1209600
  },
  "enabled": true,
  "created_at": "2026-09-28T10:00:00Z",
  "credential": {
    "version": 1,
    "deposited_at": "2026-09-28T10:00:00Z",
    "expires_at": "2027-01-01T00:00:00Z"
  },
  "health": {
    "state": "access_error",
    "evaluated_at": "2026-09-28T10:05:00Z",
    "last_success_at": "2026-09-28T10:01:00Z",
    "last_item_at": "2026-09-28T10:01:00Z",
    "last_error": {
      "code": "unauthorized",
      "at": "2026-09-28T10:05:00Z"
    }
  }
}
```

Example `connector_x_list`:

```json
{
  "connector_id": "connector_2",
  "corpus_id": "corpus_news",
  "source_namespace": "x-watchlist",
  "kind": "x_list",
  "config": {
    "list_id": "1234567890123456789",
    "recheck_window_seconds": 86400
  },
  "schedule": {
    "interval_seconds": 120
  },
  "health_policy": {
    "silent_after_seconds": 86400,
    "credential_warning_seconds": 1209600
  },
  "enabled": true,
  "created_at": "2026-09-28T10:00:00Z",
  "credential": {
    "version": 1,
    "deposited_at": "2026-09-28T10:00:00Z",
    "expires_at": "2027-06-01T00:00:00Z"
  },
  "health": {
    "state": "active",
    "evaluated_at": "2026-09-28T10:06:00Z",
    "last_success_at": "2026-09-28T10:06:00Z",
    "last_item_at": "2026-09-28T10:04:00Z",
    "last_error": {
      "code": "rate_limited",
      "at": "2026-09-28T10:02:00Z"
    },
    "usage": {
      "day": "2026-09-28",
      "items_read": 412,
      "previous_day_items_read": 1830
    },
    "diagnostics": {
      "recheck_window_seconds": 86400,
      "recheck_interval_seconds": 600,
      "recheck_tracked_posts": 37,
      "recheck_dropped_posts": 0,
      "last_recheck_at": "2026-09-28T10:00:00Z"
    }
  }
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  connector_id:
    type: string
    minLength: 1
  corpus_id:
    type: string
    minLength: 1
  source_namespace:
    type: string
    minLength: 1
  kind:
    $ref: '#/components/schemas/ConnectorKind'
  config:
    type: object
  schedule:
    type: object
    additionalProperties: false
    properties:
      interval_seconds:
        type: integer
        minimum: 1
    required:
      - interval_seconds
  health_policy:
    type: object
    additionalProperties: false
    properties:
      silent_after_seconds:
        type: integer
        minimum: 1
      credential_warning_seconds:
        type: integer
        minimum: 0
    required:
      - silent_after_seconds
      - credential_warning_seconds
  enabled:
    type: boolean
  created_at:
    type: string
    format: date-time
  disabled_at:
    type: string
    format: date-time
  credential:
    $ref: '#/components/schemas/CredentialMetadata'
  health:
    $ref: '#/components/schemas/ConnectorHealth'
required:
  - connector_id
  - corpus_id
  - source_namespace
  - kind
  - config
  - schedule
  - health_policy
  - enabled
  - created_at
  - health
```

</details>

### `ConnectorPage`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `items` | array of [`Connector`](#connector) | yes |  |
| `next_page_cursor` | string |  | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  items:
    type: array
    items:
      $ref: '#/components/schemas/Connector'
  next_page_cursor:
    type: string
    minLength: 1
required:
  - items
```

</details>

### `CorpusRequest`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `idempotency_key` | string | yes | Minimum length `1`. |
| `name` | string | yes | Minimum length `1`. |
| `retrieval` | [`RetrievalConfig`](#retrievalconfig) |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  idempotency_key:
    type: string
    minLength: 1
  name:
    type: string
    minLength: 1
  retrieval:
    $ref: '#/components/schemas/RetrievalConfig'
required:
  - idempotency_key
  - name
```

</details>

### `Corpus`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `corpus_id` | string | yes | Minimum length `1`. |
| `name` | string | yes | Minimum length `1`. |
| `effective_retrieval` | [`RetrievalConfig`](#retrievalconfig) | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  corpus_id:
    type: string
    minLength: 1
  name:
    type: string
    minLength: 1
  effective_retrieval:
    $ref: '#/components/schemas/RetrievalConfig'
required:
  - corpus_id
  - name
  - effective_retrieval
```

</details>

### `CorpusPage`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `items` | array of [`Corpus`](#corpus) | yes |  |
| `next_page_cursor` | string |  | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  items:
    type: array
    items:
      $ref: '#/components/schemas/Corpus'
  next_page_cursor:
    type: string
    minLength: 1
required:
  - items
```

</details>

### `RecordPage`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `items` | array of [`Record`](#record) | yes |  |
| `next_page_cursor` | string |  | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  items:
    type: array
    items:
      $ref: '#/components/schemas/Record'
  next_page_cursor:
    type: string
    minLength: 1
required:
  - items
```

</details>

### `ConfigUpdate`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `idempotency_key` | string | yes | Minimum length `1`. |
| `retrieval` | [`RetrievalConfig`](#retrievalconfig) | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  idempotency_key:
    type: string
    minLength: 1
  retrieval:
    $ref: '#/components/schemas/RetrievalConfig'
required:
  - idempotency_key
  - retrieval
```

</details>

### `ActionRequest`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `idempotency_key` | string | yes | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  idempotency_key:
    type: string
    minLength: 1
required:
  - idempotency_key
```

</details>

### `ResourceReference`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `kind` | string | yes | Minimum length `1`. |
| `id` | string | yes | Minimum length `1`. |
| `corpus_id` | string |  | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  kind:
    type: string
    minLength: 1
  id:
    type: string
    minLength: 1
  corpus_id:
    type: string
    minLength: 1
required:
  - kind
  - id
```

</details>

### `ChangeEvent`

Every change to a Record catalog entry emits an event with resource.kind=record and resource.id=the affected Record ID. Additional resource-specific events do not replace this invalidation. Consumers reread current state; payload detail belongs to THE-547. Monitoring notice types mirror WebhookEvent and include monitoring references; event_id identifies that same committed notice. Delivery status changes emit delivery.updated events only to the feed, never recursive webhooks.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `event_id` | string | yes | Minimum length `1`. |
| `type` | string | yes | Minimum length `1`. |
| `schema_version` | string | yes | Minimum length `1`. |
| `occurred_at` | string (date-time) | yes |  |
| `resource` | [`ResourceReference`](#resourcereference) | yes |  |
| `cursor` | string | yes | Minimum length `1`. |
| `monitoring` | [`MonitoringReferences`](#monitoringreferences) |  |  |

Example `monitoring_change`:

```json
{
  "event_id": "event_match_1",
  "type": "match.created",
  "schema_version": "1",
  "occurred_at": "2026-09-14T12:00:00Z",
  "resource": {
    "kind": "match",
    "id": "match_1",
    "corpus_id": "corpus_news"
  },
  "cursor": "opaque_cursor",
  "monitoring": {
    "match_id": "match_1",
    "record_id": "record_1",
    "record_version_id": "version_1",
    "subscription_id": "subscription_1",
    "subscription_version_id": "subscription_version_1",
    "delivery_id": "delivery_1"
  }
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  event_id:
    type: string
    minLength: 1
  type:
    type: string
    minLength: 1
  schema_version:
    type: string
    minLength: 1
  occurred_at:
    type: string
    format: date-time
  resource:
    $ref: '#/components/schemas/ResourceReference'
  cursor:
    type: string
    minLength: 1
  monitoring:
    $ref: '#/components/schemas/MonitoringReferences'
required:
  - event_id
  - type
  - schema_version
  - occurred_at
  - resource
  - cursor
description: Every change to a Record catalog entry emits an event with resource.kind=record and resource.id=the affected Record ID. Additional resource-specific events do not replace this invalidation. Consumers reread current state; payload detail belongs to THE-547. Monitoring notice types mirror WebhookEvent and include monitoring references; event_id identifies that same committed notice. Delivery status changes emit delivery.updated events only to the feed, never recursive webhooks.
```

</details>

### `ChangePage`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `items` | array of [`ChangeEvent`](#changeevent) | yes |  |
| `next_cursor` | string | yes | Minimum length `1`. |
| `has_more` | boolean | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  items:
    type: array
    items:
      $ref: '#/components/schemas/ChangeEvent'
  next_cursor:
    type: string
    minLength: 1
  has_more:
    type: boolean
required:
  - items
  - next_cursor
  - has_more
```

</details>

### `ResolvedRelation`

Separate live view, not a mutation of the source Manifest. Unavailable covers missing, unready, withdrawn and inaccessible without distinguishing existence or revealing resolved target IDs.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `source_reference` | [`RelationInput`](#relationinput) | yes |  |
| `status` | string | yes | One of `available`, `unavailable`. |
| `target_record_id` | string |  |  |
| `target_version_id` | string |  |  |

Further rules (conditional requirements or combinations) are in the full schema below.

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required:
  - source_reference
  - status
properties:
  source_reference:
    $ref: '#/components/schemas/RelationInput'
  status:
    type: string
    enum:
      - available
      - unavailable
  target_record_id:
    type: string
  target_version_id:
    type: string
description: Separate live view, not a mutation of the source Manifest. Unavailable covers missing, unready, withdrawn and inaccessible without distinguishing existence or revealing resolved target IDs.
if:
  properties:
    status:
      const: available
then:
  required:
    - target_record_id
    - target_version_id
else:
  not:
    anyOf:
      - required:
          - target_record_id
      - required:
          - target_version_id
```

</details>

### `ProcessingSummary`

Live read view, not a Receipt lifecycle or public workflow identifier. blocked means an outstanding contribution needs intervention; diagnostics describe why. idle means no work currently pending, not a promise of final enrichment. Phase is omitted when idle; required and optional progress do not override Version Availability.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `state` | string | yes | One of `queued`, `running`, `retrying`, `blocked`, `idle`. |
| `phase` | string |  | One of `materialization`, `baseline`, `enrichment`. |
| `message` | string |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required:
  - state
properties:
  state:
    type: string
    enum:
      - queued
      - running
      - retrying
      - blocked
      - idle
  phase:
    type: string
    enum:
      - materialization
      - baseline
      - enrichment
  message:
    type: string
description: Live read view, not a Receipt lifecycle or public workflow identifier. blocked means an outstanding contribution needs intervention; diagnostics describe why. idle means no work currently pending, not a promise of final enrichment. Phase is omitted when idle; required and optional progress do not override Version Availability.
```

</details>

### `SavedQueryDefinition`

Immutable query definition. Expression semantics belong to the evaluator plugin; no core keyword or semantic threshold is implied. All Corpora belong to the authorized Organization.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `corpus_ids` | array of string | yes | At least `1` items. Items are unique. Each item: Minimum length `1`. |
| `expression` | object | yes |  |
| `retrieval_profile` | string | yes | Minimum length `1`. |
| `temporal_policy` | string | yes | One of `from_activation`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  corpus_ids:
    type: array
    items:
      type: string
      minLength: 1
    minItems: 1
    uniqueItems: true
  expression:
    type: object
    additionalProperties: true
  retrieval_profile:
    type: string
    minLength: 1
  temporal_policy:
    type: string
    enum:
      - from_activation
required:
  - corpus_ids
  - expression
  - retrieval_profile
  - temporal_policy
description: Immutable query definition. Expression semantics belong to the evaluator plugin; no core keyword or semantic threshold is implied. All Corpora belong to the authorized Organization.
```

</details>

### `SavedQueryCreate`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `idempotency_key` | string | yes | Minimum length `1`. |
| `name` | string | yes | Minimum length `1`. |
| `definition` | [`SavedQueryDefinition`](#savedquerydefinition) | yes |  |

Example `saved_query_create`:

```json
{
  "idempotency_key": "create-query-1",
  "name": "Example query",
  "definition": {
    "corpus_ids": [
      "corpus_news"
    ],
    "expression": {
      "fixture": "positive"
    },
    "retrieval_profile": "balanced",
    "temporal_policy": "from_activation"
  }
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  idempotency_key:
    type: string
    minLength: 1
  name:
    type: string
    minLength: 1
  definition:
    $ref: '#/components/schemas/SavedQueryDefinition'
required:
  - idempotency_key
  - name
  - definition
```

</details>

### `SavedQueryVersionCreate`

New immutable definition of an existing Saved Query. The name is unchanged.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `idempotency_key` | string | yes | Minimum length `1`. |
| `definition` | [`SavedQueryDefinition`](#savedquerydefinition) | yes |  |

Example `saved_query_version_create`:

```json
{
  "idempotency_key": "edit-query-1",
  "definition": {
    "corpus_ids": [
      "corpus_news"
    ],
    "expression": {
      "fixture": "negative"
    },
    "retrieval_profile": "balanced",
    "temporal_policy": "from_activation"
  }
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  idempotency_key:
    type: string
    minLength: 1
  definition:
    $ref: '#/components/schemas/SavedQueryDefinition'
required:
  - idempotency_key
  - definition
description: New immutable definition of an existing Saved Query. The name is unchanged.
```

</details>

### `SavedQueryVersion`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `saved_query_id` | string | yes | Minimum length `1`. |
| `version_id` | string | yes | Minimum length `1`. |
| `definition` | [`SavedQueryDefinition`](#savedquerydefinition) | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  saved_query_id:
    type: string
    minLength: 1
  version_id:
    type: string
    minLength: 1
  definition:
    $ref: '#/components/schemas/SavedQueryDefinition'
required:
  - saved_query_id
  - version_id
  - definition
```

</details>

### `SavedQuery`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `saved_query_id` | string | yes | Minimum length `1`. |
| `name` | string | yes | Minimum length `1`. |
| `deleted` | boolean | yes | Logically deleted; the Saved Query and its Versions stay readable. |
| `current_version` | [`SavedQueryVersion`](#savedqueryversion) | yes |  |

Example `saved_query`:

```json
{
  "saved_query_id": "query_1",
  "name": "Example query",
  "deleted": false,
  "current_version": {
    "saved_query_id": "query_1",
    "version_id": "query_version_1",
    "definition": {
      "corpus_ids": [
        "corpus_news"
      ],
      "expression": {
        "fixture": "positive"
      },
      "retrieval_profile": "balanced",
      "temporal_policy": "from_activation"
    }
  }
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  saved_query_id:
    type: string
    minLength: 1
  name:
    type: string
    minLength: 1
  deleted:
    type: boolean
    description: Logically deleted; the Saved Query and its Versions stay readable.
  current_version:
    $ref: '#/components/schemas/SavedQueryVersion'
required:
  - saved_query_id
  - name
  - deleted
  - current_version
```

</details>

### `EvaluatorConfig`

Pins an installed evaluator by plugin id and version, and its configuration. Evaluators are the subscription Contributions of the plugins pinned at startup (Plugin Protocol v0); test deployments may also install the deterministic fixture quivr.fixture@1. The configuration must satisfy the evaluator's declared configuration schema.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `plugin_id` | string | yes | Minimum length `1`. |
| `version` | string | yes | Minimum length `1`. |
| `configuration` | object | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  plugin_id:
    type: string
    minLength: 1
  version:
    type: string
    minLength: 1
  configuration:
    type: object
    additionalProperties: true
required:
  - plugin_id
  - version
  - configuration
description: Pins an installed evaluator by plugin id and version, and its configuration. Evaluators are the subscription Contributions of the plugins pinned at startup (Plugin Protocol v0); test deployments may also install the deterministic fixture quivr.fixture@1. The configuration must satisfy the evaluator's declared configuration schema.
```

</details>

### `SubscriptionCreate`

Create enabled from-now Subscription. An optional owner makes it the Subscription of one end user of the client application; without one it is global to the Organization. The owner is fixed for the Subscription's life and part of the idempotent request. One deployment-configured destination per version; destination belongs to this Organization. URL and signing key are provisioned outside this API and not returned. No inline secret or dynamic destination registry in the tracer.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `idempotency_key` | string | yes | Minimum length `1`. |
| `name` | string | yes | Minimum length `1`. |
| `saved_query_id` | string | yes | Minimum length `1`. |
| `saved_query_version_id` | string | yes | Minimum length `1`. |
| `evaluator` | [`EvaluatorConfig`](#evaluatorconfig) | yes |  |
| `destination_id` | string | yes | Minimum length `1`. |
| `owner` | [`SubscriptionOwner`](#subscriptionowner) |  |  |

Example `subscription_create`:

```json
{
  "idempotency_key": "create-subscription-1",
  "name": "Example subscription",
  "saved_query_id": "query_1",
  "saved_query_version_id": "query_version_1",
  "evaluator": {
    "plugin_id": "fixture-evaluator",
    "version": "1",
    "configuration": {
      "scenario": "positive",
      "parameters": {
        "sample": true
      }
    }
  },
  "destination_id": "fixture_webhook"
}
```

Example `owned_subscription_create`:

```json
{
  "idempotency_key": "create-subscription-user-123",
  "name": "Alert for one end user",
  "saved_query_id": "query_1",
  "saved_query_version_id": "query_version_1",
  "evaluator": {
    "plugin_id": "fixture-evaluator",
    "version": "1",
    "configuration": {
      "scenario": "positive",
      "parameters": {
        "sample": true
      }
    }
  },
  "destination_id": "fixture_webhook",
  "owner": "user-123"
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  idempotency_key:
    type: string
    minLength: 1
  name:
    type: string
    minLength: 1
  saved_query_id:
    type: string
    minLength: 1
  saved_query_version_id:
    type: string
    minLength: 1
  evaluator:
    $ref: '#/components/schemas/EvaluatorConfig'
  destination_id:
    type: string
    minLength: 1
  owner:
    $ref: '#/components/schemas/SubscriptionOwner'
required:
  - idempotency_key
  - name
  - saved_query_id
  - saved_query_version_id
  - evaluator
  - destination_id
description: Create enabled from-now Subscription. An optional owner makes it the Subscription of one end user of the client application; without one it is global to the Organization. The owner is fixed for the Subscription's life and part of the idempotent request. One deployment-configured destination per version; destination belongs to this Organization. URL and signing key are provisioned outside this API and not returned. No inline secret or dynamic destination registry in the tracer.
```

</details>

### `SubscriptionVersionCreate`

New immutable configuration of an existing Subscription. The Saved Query and name are unchanged; saved_query_version_id is the current Version of that Saved Query.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `idempotency_key` | string | yes | Minimum length `1`. |
| `saved_query_version_id` | string | yes | Minimum length `1`. |
| `evaluator` | [`EvaluatorConfig`](#evaluatorconfig) | yes |  |
| `destination_id` | string | yes | Minimum length `1`. |

Example `subscription_version_create`:

```json
{
  "idempotency_key": "edit-subscription-1",
  "saved_query_version_id": "query_version_2",
  "evaluator": {
    "plugin_id": "fixture-evaluator",
    "version": "1",
    "configuration": {
      "scenario": "negative"
    }
  },
  "destination_id": "fixture_webhook"
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  idempotency_key:
    type: string
    minLength: 1
  saved_query_version_id:
    type: string
    minLength: 1
  evaluator:
    $ref: '#/components/schemas/EvaluatorConfig'
  destination_id:
    type: string
    minLength: 1
required:
  - idempotency_key
  - saved_query_version_id
  - evaluator
  - destination_id
description: New immutable configuration of an existing Subscription. The Saved Query and name are unchanged; saved_query_version_id is the current Version of that Saved Query.
```

</details>

### `SubscriptionVersion`

Immutable Subscription configuration. Every Version keeps the Subscription's owner, absent for a global Subscription.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `subscription_id` | string | yes | Minimum length `1`. |
| `version_id` | string | yes | Minimum length `1`. |
| `saved_query_id` | string | yes | Minimum length `1`. |
| `saved_query_version_id` | string | yes | Minimum length `1`. |
| `evaluator` | [`EvaluatorConfig`](#evaluatorconfig) | yes |  |
| `destination_id` | string | yes | Minimum length `1`. |
| `owner` | [`SubscriptionOwner`](#subscriptionowner) |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  subscription_id:
    type: string
    minLength: 1
  version_id:
    type: string
    minLength: 1
  saved_query_id:
    type: string
    minLength: 1
  saved_query_version_id:
    type: string
    minLength: 1
  evaluator:
    $ref: '#/components/schemas/EvaluatorConfig'
  destination_id:
    type: string
    minLength: 1
  owner:
    $ref: '#/components/schemas/SubscriptionOwner'
required:
  - subscription_id
  - version_id
  - saved_query_id
  - saved_query_version_id
  - evaluator
  - destination_id
description: Immutable Subscription configuration. Every Version keeps the Subscription's owner, absent for a global Subscription.
```

</details>

### `Subscription`

Absent owner means a global, organization-wide Subscription.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `subscription_id` | string | yes | Minimum length `1`. |
| `name` | string | yes | Minimum length `1`. |
| `enabled` | boolean | yes |  |
| `deleted` | boolean | yes | Logically deleted for good; a deleted Subscription is also disabled and stays readable with its Versions, Matches and Deliveries. |
| `current_version` | [`SubscriptionVersion`](#subscriptionversion) | yes |  |
| `owner` | [`SubscriptionOwner`](#subscriptionowner) |  |  |

Example `subscription`:

```json
{
  "subscription_id": "subscription_1",
  "name": "Example subscription",
  "enabled": true,
  "deleted": false,
  "current_version": {
    "subscription_id": "subscription_1",
    "version_id": "subscription_version_1",
    "saved_query_id": "query_1",
    "saved_query_version_id": "query_version_1",
    "evaluator": {
      "plugin_id": "fixture-evaluator",
      "version": "1",
      "configuration": {
        "scenario": "positive",
        "parameters": {
          "sample": true
        }
      }
    },
    "destination_id": "fixture_webhook"
  }
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  subscription_id:
    type: string
    minLength: 1
  name:
    type: string
    minLength: 1
  enabled:
    type: boolean
  deleted:
    type: boolean
    description: Logically deleted for good; a deleted Subscription is also disabled and stays readable with its Versions, Matches and Deliveries.
  current_version:
    $ref: '#/components/schemas/SubscriptionVersion'
  owner:
    $ref: '#/components/schemas/SubscriptionOwner'
required:
  - subscription_id
  - name
  - enabled
  - deleted
  - current_version
description: Absent owner means a global, organization-wide Subscription.
```

</details>

### `SubscriptionOwner`

Subscription Owner, an opaque end-user reference defined by the client application (for example user-123). Quivr stores, filters and echoes it without interpreting it. At most 128 characters without control characters; none is reserved for the listing filter (422 invalid_owner).

Type: string. Minimum length `1`. Maximum length `128`.

<details>
<summary>Full schema</summary>

```yaml
type: string
minLength: 1
maxLength: 128
description: Subscription Owner, an opaque end-user reference defined by the client application (for example user-123). Quivr stores, filters and echoes it without interpreting it. At most 128 characters without control characters; none is reserved for the listing filter (422 invalid_owner).
```

</details>

### `SubscriptionPage`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `items` | array of [`Subscription`](#subscription) | yes |  |
| `next_page_cursor` | string |  | Minimum length `1`. |

Example `owned_subscription_page`:

```json
{
  "items": [
    {
      "subscription_id": "subscription_2",
      "name": "Alert for one end user",
      "enabled": true,
      "deleted": false,
      "current_version": {
        "subscription_id": "subscription_2",
        "version_id": "subscription_version_2",
        "saved_query_id": "query_1",
        "saved_query_version_id": "query_version_1",
        "evaluator": {
          "plugin_id": "fixture-evaluator",
          "version": "1",
          "configuration": {
            "scenario": "positive",
            "parameters": {
              "sample": true
            }
          }
        },
        "destination_id": "fixture_webhook",
        "owner": "user-123"
      },
      "owner": "user-123"
    }
  ],
  "next_page_cursor": "opaque_page_cursor"
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  items:
    type: array
    items:
      $ref: '#/components/schemas/Subscription'
  next_page_cursor:
    type: string
    minLength: 1
required:
  - items
```

</details>

### `MatchEvidence`

Immutable evidence for a positive result, including evaluator version/configuration. Details are plugin-defined, bounded to 16 KiB and schema-validated by its adapter; core checks referenced Parts. Access is rechecked on reads.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `evaluator` | [`EvaluatorConfig`](#evaluatorconfig) | yes |  |
| `explanation` | string | yes | Maximum length `4096`. |
| `part_keys` | array of string |  | At most `100` items. Each item: Minimum length `1`. |
| `details` | object |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  evaluator:
    $ref: '#/components/schemas/EvaluatorConfig'
  explanation:
    type: string
    maxLength: 4096
  part_keys:
    type: array
    items:
      type: string
      minLength: 1
    maxItems: 100
  details:
    type: object
    additionalProperties: true
required:
  - evaluator
  - explanation
description: Immutable evidence for a positive result, including evaluator version/configuration. Details are plugin-defined, bounded to 16 KiB and schema-validated by its adapter; core checks referenced Parts. Access is rechecked on reads.
```

</details>

### `Match`

Immutable positive historical determination, not a claim of current eligibility. Unique subscription-version/record-version. Correction/withdrawal notifications reference history; no Match is fabricated for negative decisions. owner is the Subscription's owner, absent when global.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `match_id` | string | yes | Minimum length `1`. |
| `subscription_id` | string | yes | Minimum length `1`. |
| `subscription_version_id` | string | yes | Minimum length `1`. |
| `saved_query_id` | string | yes | Minimum length `1`. |
| `saved_query_version_id` | string | yes | Minimum length `1`. |
| `record_id` | string | yes | Minimum length `1`. |
| `record_version_id` | string | yes | Minimum length `1`. |
| `previous_match_id` | string |  | Minimum length `1`. |
| `owner` | [`SubscriptionOwner`](#subscriptionowner) |  |  |
| `evidence` | [`MatchEvidence`](#matchevidence) | yes |  |

Example `positive_match`:

```json
{
  "match_id": "match_1",
  "subscription_id": "subscription_1",
  "subscription_version_id": "subscription_version_1",
  "saved_query_id": "query_1",
  "saved_query_version_id": "query_version_1",
  "record_id": "record_1",
  "record_version_id": "version_1",
  "evidence": {
    "evaluator": {
      "plugin_id": "fixture-evaluator",
      "version": "1",
      "configuration": {
        "scenario": "positive",
        "parameters": {
          "sample": true
        }
      }
    },
    "explanation": "Résultat déterministe de la fixture",
    "part_keys": [
      "body"
    ],
    "details": {
      "fixture": true,
      "values": [
        1,
        false,
        null
      ]
    }
  }
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  match_id:
    type: string
    minLength: 1
  subscription_id:
    type: string
    minLength: 1
  subscription_version_id:
    type: string
    minLength: 1
  saved_query_id:
    type: string
    minLength: 1
  saved_query_version_id:
    type: string
    minLength: 1
  record_id:
    type: string
    minLength: 1
  record_version_id:
    type: string
    minLength: 1
  previous_match_id:
    type: string
    minLength: 1
  owner:
    $ref: '#/components/schemas/SubscriptionOwner'
  evidence:
    $ref: '#/components/schemas/MatchEvidence'
required:
  - match_id
  - subscription_id
  - subscription_version_id
  - saved_query_id
  - saved_query_version_id
  - record_id
  - record_version_id
  - evidence
description: Immutable positive historical determination, not a claim of current eligibility. Unique subscription-version/record-version. Correction/withdrawal notifications reference history; no Match is fabricated for negative decisions. owner is the Subscription's owner, absent when global.
```

</details>

### `MatchPage`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `items` | array of [`Match`](#match) | yes |  |
| `next_page_cursor` | string |  | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  items:
    type: array
    items:
      $ref: '#/components/schemas/Match'
  next_page_cursor:
    type: string
    minLength: 1
required:
  - items
```

</details>

### `MonitoringReferences`

owner is the Subscription Owner, so a client routes the notice to its user; absent for a global Subscription and in notices committed before owners existed. match_id is the new Match for created/corrected, prior positive Match for no_longer_matches/withdrawn. record_version_id is the causal correction version for corrected/no_longer_matches, otherwise the matched version. References alone confer no access.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `match_id` | string | yes | Minimum length `1`. |
| `record_id` | string | yes | Minimum length `1`. |
| `record_version_id` | string | yes | Minimum length `1`. |
| `subscription_id` | string | yes | Minimum length `1`. |
| `subscription_version_id` | string | yes | Minimum length `1`. |
| `delivery_id` | string | yes | Minimum length `1`. |
| `previous_match_id` | string |  | Minimum length `1`. |
| `owner` | [`SubscriptionOwner`](#subscriptionowner) |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  match_id:
    type: string
    minLength: 1
  record_id:
    type: string
    minLength: 1
  record_version_id:
    type: string
    minLength: 1
  subscription_id:
    type: string
    minLength: 1
  subscription_version_id:
    type: string
    minLength: 1
  delivery_id:
    type: string
    minLength: 1
  previous_match_id:
    type: string
    minLength: 1
  owner:
    $ref: '#/components/schemas/SubscriptionOwner'
required:
  - match_id
  - record_id
  - record_version_id
  - subscription_id
  - subscription_version_id
  - delivery_id
description: owner is the Subscription Owner, so a client routes the notice to its user; absent for a global Subscription and in notices committed before owners existed. match_id is the new Match for created/corrected, prior positive Match for no_longer_matches/withdrawn. record_version_id is the causal correction version for corrected/no_longer_matches, otherwise the matched version. References alone confer no access.
```

</details>

### `WebhookEvent`

Immutable reference-only notification. Retries preserve event_id and the exact stored body bytes; signing timestamp changes per attempt. No document content, excerpt, explanation, cursor or secret is embedded.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `event_id` | string | yes | Pattern `^[A-Za-z0-9_-]+$`. |
| `type` | string | yes | One of `match.created`, `match.corrected`, `match.no_longer_matches`, `match.withdrawn`. |
| `schema_version` | string | yes | One of `1`. |
| `occurred_at` | string (date-time) | yes |  |
| `references` | [`MonitoringReferences`](#monitoringreferences) | yes |  |

Example `reference_webhook`:

```json
{
  "event_id": "event_match_1",
  "type": "match.created",
  "schema_version": "1",
  "occurred_at": "2026-09-14T12:00:00Z",
  "references": {
    "match_id": "match_1",
    "record_id": "record_1",
    "record_version_id": "version_1",
    "subscription_id": "subscription_1",
    "subscription_version_id": "subscription_version_1",
    "delivery_id": "delivery_1"
  }
}
```

Example `owned_reference_webhook`:

```json
{
  "event_id": "event_match_2",
  "type": "match.created",
  "schema_version": "1",
  "occurred_at": "2026-09-14T12:00:00Z",
  "references": {
    "match_id": "match_2",
    "record_id": "record_1",
    "record_version_id": "version_1",
    "subscription_id": "subscription_2",
    "subscription_version_id": "subscription_version_2",
    "delivery_id": "delivery_2",
    "owner": "user-123"
  }
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  event_id:
    type: string
    pattern: ^[A-Za-z0-9_-]+$
  type:
    type: string
    enum:
      - match.created
      - match.corrected
      - match.no_longer_matches
      - match.withdrawn
  schema_version:
    type: string
    enum:
      - '1'
  occurred_at:
    type: string
    format: date-time
  references:
    $ref: '#/components/schemas/MonitoringReferences'
required:
  - event_id
  - type
  - schema_version
  - occurred_at
  - references
description: Immutable reference-only notification. Retries preserve event_id and the exact stored body bytes; signing timestamp changes per attempt. No document content, excerpt, explanation, cursor or secret is embedded.
```

</details>

### `DeliveryAdmission`

Current derived admission view, separate from durable Delivery state. A disallowed pending Delivery makes no new network attempt; it does not become a new lifecycle state. destination_unavailable means its destination is no longer configured for the Organization. subscription_disabled lasts until a re-enable; subscription_deleted is permanent. record_withdrawn refuses match.created, match.corrected and match.no_longer_matches; match.withdrawn is admitted for a withdrawn Record. superseded refuses an undelivered match.created or match.corrected once a later match.corrected or match.no_longer_matches exists for the same Subscription and Record, and an undelivered match.no_longer_matches once a later match.corrected exists; match.withdrawn is never superseded.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `allowed` | boolean | yes |  |
| `reason` | string |  | One of `subscription_disabled`, `subscription_deleted`, `record_withdrawn`, `superseded`, `access_denied`, `destination_unavailable`, `terminal`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  allowed:
    type: boolean
  reason:
    type: string
    enum:
      - subscription_disabled
      - subscription_deleted
      - record_withdrawn
      - superseded
      - access_denied
      - destination_unavailable
      - terminal
required:
  - allowed
description: Current derived admission view, separate from durable Delivery state. A disallowed pending Delivery makes no new network attempt; it does not become a new lifecycle state. destination_unavailable means its destination is no longer configured for the Organization. subscription_disabled lasts until a re-enable; subscription_deleted is permanent. record_withdrawn refuses match.created, match.corrected and match.no_longer_matches; match.withdrawn is admitted for a withdrawn Record. superseded refuses an undelivered match.created or match.corrected once a later match.corrected or match.no_longer_matches exists for the same Subscription and Record, and an undelivered match.no_longer_matches once a later match.corrected exists; match.withdrawn is never superseded.
```

</details>

### `Delivery`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `delivery_id` | string | yes | Minimum length `1`. |
| `match_id` | string | yes | Minimum length `1`. |
| `destination_id` | string | yes | Minimum length `1`. |
| `state` | string | yes | One of `pending`, `delivering`, `delivered`, `exhausted`. |
| `event` | [`WebhookEvent`](#webhookevent) | yes |  |
| `attempt_count` | integer | yes | Minimum `0`. |
| `admission` | [`DeliveryAdmission`](#deliveryadmission) | yes |  |
| `last_error` | [`Error`](#error) |  |  |
| `next_attempt_at` | string (date-time) |  | When the next automatic attempt becomes eligible. Present only while the Delivery is pending and admission is allowed; a retry waits with jittered exponential backoff (or a valid Retry-After on 429/503), never beyond the delivery window. |

Example `delivery`:

```json
{
  "delivery_id": "delivery_1",
  "match_id": "match_1",
  "destination_id": "fixture_webhook",
  "state": "pending",
  "event": {
    "event_id": "event_match_1",
    "type": "match.created",
    "schema_version": "1",
    "occurred_at": "2026-09-14T12:00:00Z",
    "references": {
      "match_id": "match_1",
      "record_id": "record_1",
      "record_version_id": "version_1",
      "subscription_id": "subscription_1",
      "subscription_version_id": "subscription_version_1",
      "delivery_id": "delivery_1"
    }
  },
  "attempt_count": 1,
  "admission": {
    "allowed": false,
    "reason": "subscription_disabled"
  }
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  delivery_id:
    type: string
    minLength: 1
  match_id:
    type: string
    minLength: 1
  destination_id:
    type: string
    minLength: 1
  state:
    type: string
    enum:
      - pending
      - delivering
      - delivered
      - exhausted
  event:
    $ref: '#/components/schemas/WebhookEvent'
  attempt_count:
    type: integer
    minimum: 0
  admission:
    $ref: '#/components/schemas/DeliveryAdmission'
  last_error:
    $ref: '#/components/schemas/Error'
  next_attempt_at:
    type: string
    format: date-time
    description: When the next automatic attempt becomes eligible. Present only while the Delivery is pending and admission is allowed; a retry waits with jittered exponential backoff (or a valid Retry-After on 429/503), never beyond the delivery window.
required:
  - delivery_id
  - match_id
  - destination_id
  - state
  - event
  - attempt_count
  - admission
```

</details>

### `DeliveryAttempt`

Read view of append-only admission/outcome facts. Unknown network result can be retried with the same event ID; no response body, signature or signing key is exposed.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `attempt_id` | string | yes | Minimum length `1`. |
| `delivery_id` | string | yes | Minimum length `1`. |
| `number` | integer | yes | Minimum `1`. |
| `outcome` | string | yes | One of `in_flight`, `acknowledged`, `retryable_error`, `permanent_error`, `unknown`. |
| `http_status` | integer |  | Minimum `100`. Maximum `599`. |
| `error` | [`Error`](#error) |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  attempt_id:
    type: string
    minLength: 1
  delivery_id:
    type: string
    minLength: 1
  number:
    type: integer
    minimum: 1
  outcome:
    type: string
    enum:
      - in_flight
      - acknowledged
      - retryable_error
      - permanent_error
      - unknown
  http_status:
    type: integer
    minimum: 100
    maximum: 599
  error:
    $ref: '#/components/schemas/Error'
required:
  - attempt_id
  - delivery_id
  - number
  - outcome
description: Read view of append-only admission/outcome facts. Unknown network result can be retried with the same event ID; no response body, signature or signing key is exposed.
```

</details>

### `DeliveryAttemptPage`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `items` | array of [`DeliveryAttempt`](#deliveryattempt) | yes |  |
| `next_page_cursor` | string |  | Minimum length `1`. |

Example `attempt_page`:

```json
{
  "items": [
    {
      "attempt_id": "attempt_1",
      "delivery_id": "delivery_1",
      "number": 1,
      "outcome": "retryable_error",
      "http_status": 503,
      "error": {
        "code": "receiver_unavailable",
        "message": "HTTP 503",
        "retryable": true
      }
    }
  ]
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  items:
    type: array
    items:
      $ref: '#/components/schemas/DeliveryAttempt'
  next_page_cursor:
    type: string
    minLength: 1
required:
  - items
```

</details>

### `SearchRequest`

Text-only top-k query. Resolve all Corpora in the authenticated Organization and require read/search permission for every requested Corpus before querying. Never silently drop an unauthorized Corpus. Unknown/unsupported profile or mode returns 422; a dependency outage is an error, not an empty successful result. Query token limits are checked against the resolved profile; no silent truncation. Metadata filter syntax and pagination are outside this initial surface.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `query` | string | yes | Minimum length `1`. Maximum length `8192`. |
| `corpus_ids` | array of string | yes | At least `1` items. At most `16` items. Items are unique. Each item: Minimum length `1`. |
| `mode` | string |  | One of `lexical`, `semantic`, `hybrid`. Default `hybrid`. |
| `profile` | string |  | One of `fast`, `balanced`, `deep`. Default `balanced`. |
| `limit` | integer |  | Default `10`. Minimum `1`. Maximum `50`. |

Example `text_search`:

```json
{
  "query": "énergie solaire 🌞",
  "corpus_ids": [
    "corpus_news"
  ],
  "mode": "hybrid",
  "profile": "balanced",
  "limit": 10
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  query:
    type: string
    minLength: 1
    maxLength: 8192
  corpus_ids:
    type: array
    items:
      type: string
      minLength: 1
    minItems: 1
    maxItems: 16
    uniqueItems: true
  mode:
    type: string
    enum:
      - lexical
      - semantic
      - hybrid
    default: hybrid
  profile:
    type: string
    enum:
      - fast
      - balanced
      - deep
    default: balanced
  limit:
    type: integer
    minimum: 1
    maximum: 50
    default: 10
required:
  - query
  - corpus_ids
description: Text-only top-k query. Resolve all Corpora in the authenticated Organization and require read/search permission for every requested Corpus before querying. Never silently drop an unauthorized Corpus. Unknown/unsupported profile or mode returns 422; a dependency outage is an error, not an empty successful result. Query token limits are checked against the resolved profile; no silent truncation. Metadata filter syntax and pagination are outside this initial surface.
```

</details>

### `SearchProfile`

Resolved immutable retrieval profile identity. Version describes query normalization, ranking seed and model/segmentation configuration; no user-facing engine parameters.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | yes | Minimum length `1`. |
| `version` | string | yes | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  name:
    type: string
    minLength: 1
  version:
    type: string
    minLength: 1
required:
  - name
  - version
description: Resolved immutable retrieval profile identity. Version describes query normalization, ranking seed and model/segmentation configuration; no user-facing engine parameters.
```

</details>

### `SearchExcerpt`

Exact canonical normalized Part text slice [start,end), using Unicode code points, not UTF-8 bytes or UTF-16 units. End must be >= start and end-start must equal the excerpt code-point length. Bounds are checked against the referenced immutable Part. No synthetic highlights or rewritten snippets.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `text` | string | yes | Maximum length `4096`. |
| `start` | integer | yes | Minimum `0`. |
| `end` | integer | yes | Minimum `0`. |
| `coordinate_system` | string | yes | One of `unicode_codepoint`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  text:
    type: string
    maxLength: 4096
  start:
    type: integer
    minimum: 0
  end:
    type: integer
    minimum: 0
  coordinate_system:
    type: string
    enum:
      - unicode_codepoint
required:
  - text
  - start
  - end
  - coordinate_system
description: Exact canonical normalized Part text slice [start,end), using Unicode code points, not UTF-8 bytes or UTF-16 units. End must be >= start and end-start must equal the excerpt code-point length. Bounds are checked against the referenced immutable Part. No synthetic highlights or rewritten snippets.
```

</details>

### `SearchHit`

One authorized segment hit. Rehydrate from canonical storage and recheck Organization/Corpus access, currentness, quarantine and Tombstone before returning. Rank is contiguous and one-based after hydration/filtering. Projection Generation, segmentation, segment and optional Embedding Artifact/Vector Space are logical durable IDs, not physical collection names or workflow IDs. Embedding references are omitted when that segment has lexical coverage only. They do not assert that the dense branch contributed to its rank. All hits inherit the response retrieval profile. Raw scores/explainScore stay internal.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `record_id` | string | yes | Minimum length `1`. |
| `version_id` | string | yes | Minimum length `1`. |
| `part_key` | string | yes | Minimum length `1`. |
| `segment_id` | string | yes | Minimum length `1`. |
| `segmentation_id` | string | yes | Minimum length `1`. |
| `projection_generation_id` | string | yes | Minimum length `1`. |
| `embedding_artifact_id` | string |  | Minimum length `1`. |
| `vector_space_id` | string |  | Minimum length `1`. |
| `rank` | integer | yes | Minimum `1`. |
| `excerpt` | [`SearchExcerpt`](#searchexcerpt) | yes |  |
| `availability` | [`Availability`](#availability) | yes |  |

Further rules (conditional requirements or combinations) are in the full schema below.

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  record_id:
    type: string
    minLength: 1
  version_id:
    type: string
    minLength: 1
  part_key:
    type: string
    minLength: 1
  segment_id:
    type: string
    minLength: 1
  segmentation_id:
    type: string
    minLength: 1
  projection_generation_id:
    type: string
    minLength: 1
  embedding_artifact_id:
    type: string
    minLength: 1
  vector_space_id:
    type: string
    minLength: 1
  rank:
    type: integer
    minimum: 1
  excerpt:
    $ref: '#/components/schemas/SearchExcerpt'
  availability:
    $ref: '#/components/schemas/Availability'
required:
  - record_id
  - version_id
  - part_key
  - segment_id
  - segmentation_id
  - projection_generation_id
  - rank
  - excerpt
  - availability
description: One authorized segment hit. Rehydrate from canonical storage and recheck Organization/Corpus access, currentness, quarantine and Tombstone before returning. Rank is contiguous and one-based after hydration/filtering. Projection Generation, segmentation, segment and optional Embedding Artifact/Vector Space are logical durable IDs, not physical collection names or workflow IDs. Embedding references are omitted when that segment has lexical coverage only. They do not assert that the dense branch contributed to its rank. All hits inherit the response retrieval profile. Raw scores/explainScore stay internal.
if:
  required:
    - embedding_artifact_id
then:
  required:
    - vector_space_id
else:
  not:
    required:
      - vector_space_id
```

</details>

### `SearchResponse`

Bounded top-k results after canonical rechecks. May contain fewer hits than requested; no total count, completeness promise or stable pagination snapshot. Empty results still name the resolved profile.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `items` | array of [`SearchHit`](#searchhit) | yes | At most `50` items. |
| `retrieval_profile` | [`SearchProfile`](#searchprofile) | yes |  |

Example `canonical_search_results`:

```json
{
  "items": [
    {
      "record_id": "record_1",
      "version_id": "version_1",
      "part_key": "body",
      "segment_id": "segment_1",
      "segmentation_id": "segmentation_1",
      "projection_generation_id": "generation_1",
      "embedding_artifact_id": "embedding_1",
      "vector_space_id": "vectorspace_1",
      "rank": 1,
      "excerpt": {
        "text": "énergie 🌞",
        "start": 3,
        "end": 12,
        "coordinate_system": "unicode_codepoint"
      },
      "availability": {
        "state": "retrieval_ready",
        "is_current": true,
        "searchable": true
      }
    }
  ],
  "retrieval_profile": {
    "name": "balanced",
    "version": "quivr.text.fixture.v1"
  }
}
```

Example `lexical_search_without_embedding`:

```json
{
  "items": [
    {
      "record_id": "record_2",
      "version_id": "version_2",
      "part_key": "body",
      "segment_id": "segment_2",
      "segmentation_id": "segmentation_2",
      "projection_generation_id": "generation_1",
      "rank": 1,
      "excerpt": {
        "text": "lune",
        "start": 0,
        "end": 4,
        "coordinate_system": "unicode_codepoint"
      },
      "availability": {
        "state": "retrieval_ready",
        "is_current": true,
        "searchable": true
      }
    }
  ],
  "retrieval_profile": {
    "name": "balanced",
    "version": "quivr.text.fixture.v1"
  }
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  items:
    type: array
    items:
      $ref: '#/components/schemas/SearchHit'
    maxItems: 50
  retrieval_profile:
    $ref: '#/components/schemas/SearchProfile'
required:
  - items
  - retrieval_profile
description: Bounded top-k results after canonical rechecks. May contain fewer hits than requested; no total count, completeness promise or stable pagination snapshot. Empty results still name the resolved profile.
```

</details>

### `ProjectionRebuildResult`

Logical generation activated for the requested Corpus. Opaque ID, never a physical search collection name.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `projection_generation_id` | string | yes | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  projection_generation_id:
    type: string
    minLength: 1
required:
  - projection_generation_id
description: Logical generation activated for the requested Corpus. Opaque ID, never a physical search collection name.
```

</details>
