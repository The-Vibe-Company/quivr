<!-- Generated from contracts/http/v0/openapi.yaml by `make generate`. Do not edit. -->

# HTTP API reference

> Generated from `contracts/http/v0/openapi.yaml` by `make generate`. Do not edit this page: change the source and regenerate.

Quivr V2 public text foundation contract, version `0.0.0-draft`.

THE-543 and THE-547 evaluation contracts; endpoint implementations are separate work. Matching criterion is plugin-owned and deferred. One configured webhook destination, immutable Matches, independent at-least-once Delivery and reference-only notifications. OpenAPI is authoritative for transport shapes. THE-640 adds text search and asynchronous Corpus projection rebuild initiation.

## Authentication

Every endpoint requires `ApiKey` unless it says otherwise.

| Scheme | Type | Description |
| --- | --- | --- |
| `InstanceToken` | HTTP `bearer` | Source-scoped token accepted only by its instance's declared instance_token routes. Never grants normal API or token management access. |
| `ApiKey` | HTTP `bearer` | API key, not necessarily a JWT. Server derives Organization, permitted actions and Corpus scope; every resource access is authorized. |

## Endpoints

| Endpoint | Operation | Permissions |
| --- | --- | --- |
| [`GET /v0/version`](#get-v0version) | `getBuildVersion` |  |
| [`POST /v0/records`](#post-v0records) | `ingestRecord` | `content:write` |
| [`GET /v0/records`](#get-v0records) | `listRecords` | `content:read` |
| [`GET /v0/records/count`](#get-v0recordscount) | `countRecords` | `content:read` |
| [`POST /v0/records/batch`](#post-v0recordsbatch) | `ingestBatch` | `content:write` |
| [`POST /v0/records/withdrawals`](#post-v0recordswithdrawals) | `withdrawRecord` | `content:write` |
| [`GET /v0/records/{record_id}`](#get-v0recordsrecord_id) | `getRecord` | `content:read` |
| [`GET /v0/records/{record_id}/versions/{version_id}`](#get-v0recordsrecord_idversionsversion_id) | `getVersion` | `content:read` |
| [`POST /v0/facets`](#post-v0facets) | `countFacets` | `content:read` |
| [`GET /v0/ingestion-receipts/{receipt_id}`](#get-v0ingestion-receiptsreceipt_id) | `getReceipt` | `content:read` |
| [`POST /v0/uploads`](#post-v0uploads) | `createUpload` | `blobs:write` |
| [`POST /v0/uploads/{upload_id}/confirm`](#post-v0uploadsupload_idconfirm) | `confirmUpload` | `blobs:write` |
| [`GET /v0/uploads/{upload_id}`](#get-v0uploadsupload_id) | `getUpload` | `blobs:read` |
| [`GET /v0/blobs/{blob_id}`](#get-v0blobsblob_id) | `getBlob` | `blobs:read` |
| [`GET /v0/operations/{operation_id}`](#get-v0operationsoperation_id) | `getOperation` | `operations:read` |
| [`POST /v0/operations/{operation_id}/cancel`](#post-v0operationsoperation_idcancel) | `cancelOperation` | `operations:write` |
| [`POST /v0/operations/{operation_id}/rerun`](#post-v0operationsoperation_idrerun) | `rerunOperation` | `operations:write` |
| [`POST /v0/operations/{operation_id}/pause`](#post-v0operationsoperation_idpause) | `pauseOperation` | `operations:write` |
| [`POST /v0/operations/{operation_id}/resume`](#post-v0operationsoperation_idresume) | `resumeOperation` | `operations:write` |
| [`POST /v0/corpora`](#post-v0corpora) | `createCorpus` | `corpora:write` |
| [`GET /v0/corpora`](#get-v0corpora) | `listCorpora` | `corpora:read` |
| [`GET /v0/corpora/{corpus_id}`](#get-v0corporacorpus_id) | `getCorpus` | `corpora:read` |
| [`PUT /v0/corpora/{corpus_id}/retrieval`](#put-v0corporacorpus_idretrieval) | `configureRetrieval` | `corpora:write`, `operations:write` |
| [`GET /v0/corpora/{corpus_id}/vector-spaces`](#get-v0corporacorpus_idvector-spaces) | `listVectorSpaces` | `corpora:read` |
| [`POST /v0/corpora/{corpus_id}/rebuilds`](#post-v0corporacorpus_idrebuilds) | `rebuildCorpusProjection` | `projections:rebuild` |
| [`GET /v0/changes`](#get-v0changes) | `pollChanges` | `changes:read` |
| [`GET /v0/changes/stream`](#get-v0changesstream) | `streamChanges` | `changes:read` |
| [`POST /v0/saved-queries`](#post-v0saved-queries) | `createSavedQuery` | `monitoring:write` |
| [`GET /v0/saved-queries/{saved_query_id}`](#get-v0saved-queriessaved_query_id) | `getSavedQuery` | `monitoring:read` |
| [`GET /v0/saved-queries/{saved_query_id}/versions/{version_id}`](#get-v0saved-queriessaved_query_idversionsversion_id) | `getSavedQueryVersion` | `monitoring:read` |
| [`POST /v0/saved-queries/{saved_query_id}/versions`](#post-v0saved-queriessaved_query_idversions) | `createSavedQueryVersion` | `monitoring:write` |
| [`POST /v0/saved-queries/{saved_query_id}/delete`](#post-v0saved-queriessaved_query_iddelete) | `deleteSavedQuery` | `monitoring:write` |
| [`POST /v0/saved-queries/{saved_query_id}/rename`](#post-v0saved-queriessaved_query_idrename) | `renameSavedQuery` | `monitoring:write` |
| [`GET /v0/subscriptions`](#get-v0subscriptions) | `listSubscriptions` | `monitoring:read` |
| [`POST /v0/subscriptions`](#post-v0subscriptions) | `createSubscription` | `monitoring:write` |
| [`GET /v0/subscriptions/{subscription_id}`](#get-v0subscriptionssubscription_id) | `getSubscription` | `monitoring:read` |
| [`GET /v0/subscriptions/{subscription_id}/versions/{version_id}`](#get-v0subscriptionssubscription_idversionsversion_id) | `getSubscriptionVersion` | `monitoring:read` |
| [`POST /v0/subscriptions/{subscription_id}/versions`](#post-v0subscriptionssubscription_idversions) | `createSubscriptionVersion` | `monitoring:write` |
| [`POST /v0/subscriptions/{subscription_id}/delete`](#post-v0subscriptionssubscription_iddelete) | `deleteSubscription` | `monitoring:write` |
| [`POST /v0/subscriptions/{subscription_id}/disable`](#post-v0subscriptionssubscription_iddisable) | `disableSubscription` | `monitoring:write` |
| [`POST /v0/subscriptions/{subscription_id}/enable`](#post-v0subscriptionssubscription_idenable) | `enableSubscription` | `monitoring:write` |
| [`POST /v0/subscriptions/{subscription_id}/rename`](#post-v0subscriptionssubscription_idrename) | `renameSubscription` | `monitoring:write` |
| [`POST /v0/subscription-previews`](#post-v0subscription-previews) | `previewSubscription` | `monitoring:write` |
| [`GET /v0/matches`](#get-v0matches) | `listMatches` | `monitoring:read` |
| [`GET /v0/matches/{match_id}`](#get-v0matchesmatch_id) | `getMatch` | `monitoring:read` |
| [`GET /v0/deliveries/{delivery_id}`](#get-v0deliveriesdelivery_id) | `getDelivery` | `monitoring:read` |
| [`GET /v0/deliveries/{delivery_id}/attempts`](#get-v0deliveriesdelivery_idattempts) | `listDeliveryAttempts` | `monitoring:read` |
| [`POST /v0/connectors`](#post-v0connectors) | `createConnector` | `connectors:write` |
| [`GET /v0/connectors`](#get-v0connectors) | `listConnectors` | `connectors:read` |
| [`POST /v0/connectors/{connector_id}/tokens`](#post-v0connectorsconnector_idtokens) | `createConnectorToken` | `connectors:admin` |
| [`GET /v0/connectors/{connector_id}/tokens`](#get-v0connectorsconnector_idtokens) | `listConnectorTokens` | `connectors:admin` |
| [`POST /v0/connectors/{connector_id}/tokens/{token_id}/rotate`](#post-v0connectorsconnector_idtokenstoken_idrotate) | `rotateConnectorToken` | `connectors:admin` |
| [`DELETE /v0/connectors/{connector_id}/tokens/{token_id}`](#delete-v0connectorsconnector_idtokenstoken_id) | `revokeConnectorToken` | `connectors:admin` |
| [`POST /v0/connectors/{connector_id}/api/{path}`](#post-v0connectorsconnector_idapipath) | `pushConnectorAPI` | `connector:push` |
| [`GET /v0/connectors/{connector_id}/api/{path}`](#get-v0connectorsconnector_idapipath) | `challengeConnectorAPI` | `connector:push` |
| [`GET /v0/connectors/{connector_id}`](#get-v0connectorsconnector_id) | `getConnector` | `connectors:read` |
| [`POST /v0/connectors/{connector_id}/disable`](#post-v0connectorsconnector_iddisable) | `disableConnector` | `connectors:write` |
| [`PUT /v0/connectors/{connector_id}/credential`](#put-v0connectorsconnector_idcredential) | `replaceConnectorCredential` | `connectors:write` |
| [`PUT /v0/connectors/{connector_id}/schedule`](#put-v0connectorsconnector_idschedule) | `changeConnectorSchedule` | `connectors:write` |
| [`POST /v0/connectors/{connector_id}/runs`](#post-v0connectorsconnector_idruns) | `requestConnectorRun` | `connectors:write` |
| [`GET /v0/connector-webhooks/{connector_id}`](#get-v0connector-webhooksconnector_id) | `relayConnectorChallenge` |  |
| [`POST /v0/connector-webhooks/{connector_id}`](#post-v0connector-webhooksconnector_id) | `relayConnectorDelivery` |  |
| [`GET /v0/connector-kinds`](#get-v0connector-kinds) | `listConnectorKinds` | `connectors:read` |
| [`GET /v0/admin/audit`](#get-v0adminaudit) | `listAuditEvents` | `audit:read` |
| [`GET /v0/admin/plugins`](#get-v0adminplugins) | `listPluginRegistrations` | `plugins:admin` |
| [`POST /v0/admin/plugins`](#post-v0adminplugins) | `registerPlugin` | `plugins:admin` |
| [`GET /v0/admin/plugins/{registration_id}`](#get-v0adminpluginsregistration_id) | `getPluginRegistration` | `plugins:admin` |
| [`POST /v0/admin/plugins/{registration_id}/activate`](#post-v0adminpluginsregistration_idactivate) | `activatePlugin` | `plugins:admin` |
| [`GET /v0/admin/plugins/plans/{plan_id}`](#get-v0adminpluginsplansplan_id) | `getPipelinePlan` | `plugins:admin` |
| [`GET /v0/admin/plugins/plans`](#get-v0adminpluginsplans) | `listPipelinePlans` | `plugins:admin` |
| [`POST /v0/admin/backfills`](#post-v0adminbackfills) | `requestBackfill` | `plugins:admin` |
| [`POST /v0/admin/spaces/{vector_space_id}/promote`](#post-v0adminspacesvector_space_idpromote) | `promoteVectorSpace` | `plugins:admin` |
| [`GET /v0/admin/quarantine`](#get-v0adminquarantine) | `listQuarantinedVersions` | `plugins:admin` |
| [`POST /v0/admin/quarantine/reprocess`](#post-v0adminquarantinereprocess) | `reprocessQuarantine` | `plugins:admin` |
| [`POST /v0/admin/subscriptions/evaluator-migrations`](#post-v0adminsubscriptionsevaluator-migrations) | `migrateSubscriptionEvaluators` | `plugins:admin` |
| [`GET /v0/admin/subscriptions/evaluation-backlog`](#get-v0adminsubscriptionsevaluation-backlog) | `listEvaluationBacklog` | `plugins:admin` |
| [`POST /v0/admin/subscriptions/evaluation-retirements`](#post-v0adminsubscriptionsevaluation-retirements) | `retireEvaluations` | `plugins:admin` |
| [`GET /v0/admin/subscriptions/evaluation-retirements/{retirement_id}`](#get-v0adminsubscriptionsevaluation-retirementsretirement_id) | `getEvaluationRetirement` | `plugins:admin` |
| [`POST /v0/admin/plugins/plan/rollback`](#post-v0adminpluginsplanrollback) | `rollbackPipelinePlan` | `plugins:admin` |
| [`GET /v0/admin/plugins/plan`](#get-v0adminpluginsplan) | `getActivePipelinePlan` | `plugins:admin` |
| [`GET /v0/admin/active-plugins`](#get-v0adminactive-plugins) | `listActivePlugins` | `observability:read` |
| [`GET /v0/admin/documents`](#get-v0admindocuments) | `listAdminDocuments` | `observability:read` |
| [`GET /v0/admin/documents/{version_id}/timeline`](#get-v0admindocumentsversion_idtimeline) | `getDocumentTimeline` | `observability:read` |
| [`GET /v0/admin/stats/connector-pushes`](#get-v0adminstatsconnector-pushes) | `listConnectorPushStats` | `observability:read` |
| [`GET /v0/admin/stats/plugins`](#get-v0adminstatsplugins) | `getPluginCallStats` | `observability:read` |
| [`GET /v0/admin/stats/searches`](#get-v0adminstatssearches) | `getSearchStats` | `observability:read` |
| [`GET /v0/admin/stats/steps`](#get-v0adminstatssteps) | `getStepStats` | `observability:read` |
| [`GET /v0/admin/stats/received`](#get-v0adminstatsreceived) | `getReceivedStats` | `observability:read` |
| [`GET /v0/admin/stats/matches`](#get-v0adminstatsmatches) | `getMatchStats` | `observability:read` |
| [`GET /v0/admin/stats/top-queries`](#get-v0adminstatstop-queries) | `getTopQueries` | `observability:read` |
| [`POST /v0/search`](#post-v0search) | `searchRecords` | `content:read`, `search:query` |
| [`GET /v0/search/profiles`](#get-v0searchprofiles) | `listSearchProfiles` | `search:query` |

### Version endpoints

#### `GET /v0/version`

Operation `getBuildVersion`.

Read the running Quivr build

Reports the distribution release, source revision and API and plugin engine compatibility versions. Any valid API key can read it; no database access is needed.

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`BuildVersion`](#buildversion) | The running build identity. |
| `default` | `application/json` [`Error`](#error) | Structured error. 401 unauthenticated. |

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

List authorized canonical Records from one or several Corpora. Without metadata predicates the catalog includes withdrawn Records. Metadata predicates require the current Version to be projected in the routed generation; withdrawn Records can disappear from filtered pages after projection purge or rebuild. Use the unfiltered catalog and change feed for complete resynchronization. The default record_id order is ascending byte-wise ID order for resynchronization. accepted_at_desc orders by the current Version's acceptance time, newest first, then by Record ID descending for ties. A correction moves the Record when its replacement Version becomes current. Records without a current Version have no acceptance time; they appear last in unbounded date listings and are excluded by either time bound. Bounds also work with the default ID order. Equal bounds select an empty range; reversed bounds, malformed dates, duplicate or unknown parameters and unknown orders return 422 invalid_query. The opaque page cursor binds Corpus selection, authorization scope, order, time bounds and metadata predicates with their routed generations; changing any returns 409 cursor_scope_changed with resync_url. Repeat the same parameters for subsequent pages. Newer arrivals between pages do not shift the keyset or duplicate previously listed Records. Each page is an independent read, not a historical snapshot: concurrent corrections, withdrawals and readiness changes are reconciled through the change feed. Capture a start-now Change Cursor before scanning and consume changes as invalidations by rereading current resources. This route, relative to the API base, is the resync_url of catalog cursor errors.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `corpus_id` | query | string |  | Minimum length `1`. |
| `corpus_ids` | query | array of string |  | Comma-separated selection of up to 16 distinct Corpora. Supply exactly one of corpus_id or corpus_ids. At least `1` items. At most `16` items. Items are unique. Each item: Minimum length `1`. |
| `metadata` | query | string |  | JSON array of MetadataFilter predicates, at most 16. Applied before ordering and pagination to current Versions projected in the routed generation. Withdrawn Records can be omitted after projection purge or rebuild; use the unfiltered catalog for full resynchronization. Repeat on later pages. Filters require a metadata-capable generation; rebuild older Corpora first. Maximum length `262144`. |
| `order` | query | string |  | One of `record_id`, `accepted_at_desc`. Default `record_id`. |
| `accepted_after` | query | string (date-time) |  | Inclusive lower bound on the current Version's acceptance time. RFC 3339 with an offset and at most 9 fractional-second digits; clients convert local days into bounds. Quivr applies no time-zone rules. |
| `accepted_before` | query | string (date-time) |  | Exclusive upper bound on the current Version's acceptance time. RFC 3339 with an offset and at most 9 fractional-second digits. |
| `page_cursor` | query | string |  | Minimum length `1`. |
| `limit` | query | integer |  | The most items to return. An empty, non-integer or out-of-range value is 422 invalid_limit. Default `100`. Minimum `1`. Maximum `100`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`RecordPage`](#recordpage) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

#### `GET /v0/records/count`

Operation `countRecords`. Requires `content:read`.

Exact count of one Corpus's authorized Records within optional current-Version acceptance-time bounds. Includes withdrawn Records, as the listing does. Records without a current Version are counted only when neither bound is supplied. Uses content:read and the same Corpus scope as listRecords. This is an independent read, not a snapshot shared with listing pages. Equal bounds count zero; reversed bounds, malformed dates, duplicate or unknown parameters return 422 invalid_query.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `corpus_id` | query | string | yes | Minimum length `1`. |
| `accepted_after` | query | string (date-time) |  | Inclusive lower bound on the current Version's acceptance time. RFC 3339 with an offset and at most 9 fractional-second digits; clients convert local days into bounds. Quivr applies no time-zone rules. |
| `accepted_before` | query | string (date-time) |  | Exclusive upper bound on the current Version's acceptance time. RFC 3339 with an offset and at most 9 fractional-second digits. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`RecordCount`](#recordcount) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

#### `POST /v0/records/batch`

Operation `ingestBatch`. Requires `content:write`.

Initial bound: 100 entries (413 batch_too_large), envelope 10 MiB (413 request_too_large). Validate envelope structure/size first, then each entry independently against IngestCommand, including wrong types or missing fields. Each raw entry is also held to the 1 MiB single-request bound (entry error entry_too_large), so the same entry bytes are never refused for size alone. HTTP 200 carries Receipt or Error per entry, with the same error codes as single submission. Same ingestion family/key as single submission. Each entry has a fresh 5-second acceptance budget, bounded by an 8-second deadline for the whole batch, including envelope decoding and validation. Caller cancellation or an earlier caller deadline also applies. An entry that times out, and every remaining entry after the batch deadline, returns retryable content_unavailable. Retry with the same entry keys to recover the original Receipts. If the envelope cannot be read before the deadline, HTTP 503 returns retryable content_unavailable without attempting entries. Reuse entry keys after unknown transport outcomes.

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

### Facets

#### `POST /v0/facets`

Operation `countFacets`. Requires `content:read`.

Exact document counts for projected metadata fields across authorized Corpora. Uses content:read and validates every requested Corpus before resolving mappings, including Corpora excluded for missing facet or predicate fields. Exclusions use the same excluded_corpora shape and rules as search and metadata-filtered listing. All fields are aggregated in one read snapshot from current baseline-ready, non-quarantined Versions; withdrawn Records and Tombstones are excluded immediately. Each distinct array value counts a document once. Missing field values contribute no bucket. Common metadata.* fields and custom fields declared with the filter role are supported. Incompatible types across Corpora return 422 invalid_query. Existing projections without metadata require rebuilding (422 metadata_filter_unavailable). Each field returns at most its limit, selected by descending count then JSON value text in byte order for ties. UTC date buckets are then returned chronologically; empty date buckets are omitted. Histograms require day, month or year on datetime fields; interval on another type is invalid_query. Counts are independent of search and listing reads and can change with ingestion. No text query or ranking is applied. Each API process admits at most 8 active facet requests; excess work returns retryable 503 content_unavailable with Retry-After: 1. Requests have a 25-second execution deadline.

**Request body** (required): `application/json` [`FacetRequest`](#facetrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`FacetResponse`](#facetresponse) | Bounded document counts and excluded Corpus explanations. |
| `default` | `application/json` [`Error`](#error) | Structured error; malformed_json, invalid_schema, request_too_large, unsupported_media_type, invalid_query, forbidden, not_found, metadata_filter_unavailable or content_unavailable. |

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

Only terminal Operations can be intentionally rerun; otherwise 409 operation_not_terminal. A backfill rerun while another backfill of its Corpus has not finished is 409 backfill_in_progress, and one whose ingestion plugin left the active plan is 409 registration_not_active. A quarantine_reprocess rerun takes the Versions still stuck in its source's scope, with the plan active now and its source's dry run as estimate; while another reprocess of its Corpus has not finished it is 409 reprocess_in_progress. A request key replays the same new linked Operation. Revalidate current scope and command eligibility; completed effects remain subject to domain idempotency.

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

#### `POST /v0/operations/{operation_id}/pause`

Operation `pauseOperation`. Requires `operations:write`.

Holds a backfill until it is resumed. A queued or running backfill becomes paused and makes no progress, keeping its checkpoint and the Pipeline Plan it is pinned to. Nothing committed is undone. Any other state is returned unchanged, so repeating the request is safe. Only backfills pause.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `operation_id` | path | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`ActionRequest`](#actionrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `202` | `application/json` [`Operation`](#operation) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 403 without operations:write or the permission of the command that created the Operation (plugins:admin for a backfill), 404 unknown Operation, 422 unsupported_operation_kind for a kind that cannot pause, 503 storage unavailable. |

#### `POST /v0/operations/{operation_id}/resume`

Operation `resumeOperation`. Requires `operations:write`.

Lets a paused backfill continue from its checkpoint, on the Pipeline Plan it is pinned to. Any other state is returned unchanged, so repeating the request is safe. Only backfills resume.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `operation_id` | path | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`ActionRequest`](#actionrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `202` | `application/json` [`Operation`](#operation) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 403 without operations:write or the permission of the command that created the Operation (plugins:admin for a backfill), 404 unknown Operation, 422 unsupported_operation_kind for a kind that cannot pause, 503 storage unavailable. |

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
| `limit` | query | integer |  | The most items to return. An empty, non-integer or out-of-range value is 422 invalid_limit. Default `100`. Minimum `1`. Maximum `100`. |

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

#### `GET /v0/corpora/{corpus_id}/vector-spaces`

Operation `listVectorSpaces`. Requires `corpora:read`.

The vector spaces the Corpus's routed Projection Generation carries, the served one first, each with its owner (the engine, or the ingestion plugin that declares it), model, dimensions, metric, indexed and query modalities, its role in the generation (served answers search, evaluation is indexed and compared but never served) and its coverage, the current segments that hold a vector in it. A Corpus built before a space was enabled lists only the spaces it was built with; rebuild it (rebuildCorpusProjection) to add the others.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `corpus_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`VectorSpaceList`](#vectorspacelist) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 unauthorized scope/action, 404 absent/inaccessible, 503 dependency unavailable. |

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
| `limit` | query | integer |  | The most items to return. An empty, non-integer or out-of-range value is 422 invalid_limit. Default `100`. Minimum `1`. Maximum `100`. |

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

#### `POST /v0/saved-queries/{saved_query_id}/rename`

Operation `renameSavedQuery`. Requires `monitoring:write`.

Change the display name of a Saved Query. The name belongs to the Saved Query, not to its immutable Versions, so no Version is created and no Subscription moves. A new name commits saved_query.renamed in every Corpus of the current Version; the same name commits nothing. Replay of the same key and request returns the Saved Query; a changed request is 409 idempotency_conflict. A deleted Saved Query is 409 saved_query_deleted.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `saved_query_id` | path | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`RenameRequest`](#renamerequest)

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
| `owner` | query | string | yes | A Subscription Owner, or none for global Subscriptions. Without it the request is 422 invalid_query; an empty or refused owner is 422 invalid_owner, as on creation. Minimum length `1`. Maximum length `128`. |
| `page_cursor` | query | string |  | Minimum length `1`. |
| `limit` | query | integer |  | The most items to return. An empty, non-integer or out-of-range value is 422 invalid_limit. Default `100`. Minimum `1`. Maximum `100`. |

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

#### `POST /v0/subscriptions/{subscription_id}/rename`

Operation `renameSubscription`. Requires `monitoring:write`.

Change the display name of a Subscription. The name belongs to the Subscription, not to its immutable Versions, so no Version is created, evaluation and enabled state are unchanged, and its Matches and Deliveries stay attached. A new name commits subscription.renamed in every Corpus of the current Version; the same name commits nothing. Replay of the same key and request returns the Subscription; a changed request is 409 idempotency_conflict. A deleted Subscription is 409 subscription_deleted.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `subscription_id` | path | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`RenameRequest`](#renamerequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`Subscription`](#subscription) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. Existing /v0 authentication, scope, pagination and idempotency semantics apply. |

### Subscription previews

#### `POST /v0/subscription-previews`

Operation `previewSubscription`. Requires `monitoring:write`.

Dry run of a proposed Subscription. Runs the evaluator on the most recently accepted current eligible Record Versions of the Saved Query's Corpora, newest first, and returns what it would have matched. Nothing is written - no Saved Query, Subscription, Match, Delivery or event - and it is not idempotent. The Saved Query is an inline definition or an existing Saved Query Version; the pair is validated as at Subscription creation (422 unsupported_evaluator, invalid_expression, invalid_subscription_configuration). Each Record Version costs one evaluator call, sent with the synthetic Subscription reference preview. At most limit Record Versions (1 to 50, default 20) are judged, within a four-second budget including preparation and storage reads; those still undecided when it runs out are left out and complete is false. An evaluator that cannot be reached or reports a transient failure fails the preview with 503 evaluator_unavailable, and one that refuses or breaks an evaluation with 502 evaluator_error. A deadline during preparation or an earlier caller deadline returns retryable 504 preview_deadline_exceeded. A storage failure remains 503 storage_unavailable. Quivr applies no rate or cost rule; a layer above can limit who previews and how often.

**Request body** (required): `application/json` [`SubscriptionPreviewRequest`](#subscriptionpreviewrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`SubscriptionPreview`](#subscriptionpreview) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error. Existing /v0 authentication and scope semantics apply. |

### Matches

#### `GET /v0/matches`

Operation `listMatches`. Requires `monitoring:read`.

Authorized historical Matches for a Subscription. Stable keyset page cursor, not a Change Cursor. Absence of a Match does not distinguish an evaluator still working from a negative result.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `subscription_id` | query | string | yes | Minimum length `1`. |
| `page_cursor` | query | string |  | Minimum length `1`. |
| `limit` | query | integer |  | The most items to return. An empty, non-integer or out-of-range value is 422 invalid_limit. Default `100`. Minimum `1`. Maximum `100`. |

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
| `limit` | query | integer |  | The most items to return. An empty, non-integer or out-of-range value is 422 invalid_limit. Default `100`. Minimum `1`. Maximum `100`. |

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
| `limit` | query | integer |  | The most items to return. An empty, non-integer or out-of-range value is 422 invalid_limit. Default `100`. Minimum `1`. Maximum `100`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`ConnectorPage`](#connectorpage) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

#### `POST /v0/connectors/{connector_id}/tokens`

Operation `createConnectorToken`. Requires `connectors:admin`.

Issue a source-scoped push token

Requires connectors:admin on the instance's Organization and Corpus. Issue a random bearer token for this instance's instance_token routes only (Plugin API 0.12). The secret is shown once in this response with Cache-Control no-store; only its SHA-256 hash and display metadata are stored. Issuance is not replayable. If the response is lost, list tokens, revoke the lost token and issue another. Disabled instances refuse issuance with 409 connector_disabled. No request body is needed.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `connector_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `201` | `application/json` [`ConnectorTokenCreated`](#connectortokencreated)<br><br>Header `Cache-Control`: string. | New token with its one-time bearer secret. |
| `default` | `application/json` [`Error`](#error) | JSON Error envelope; 401 unauthenticated, 403 without admin permission, 404 out of scope, 409 disabled, 503 unavailable. |

#### `GET /v0/connectors/{connector_id}/tokens`

Operation `listConnectorTokens`. Requires `connectors:admin`.

List push token metadata

Requires connectors:admin on the instance's Organization and Corpus. Returns metadata only, including expired or revoked tokens, never secrets or hashes. Pages are ordered by token_id; next_after is the next page's after value. Disabled instances remain readable.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `connector_id` | path | string | yes | Minimum length `1`. |
| `after` | query | string |  | Maximum length `128`. |
| `limit` | query | integer |  | Default `100`. Minimum `1`. Maximum `100`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`ConnectorTokenList`](#connectortokenlist) | Token metadata page. |
| `default` | `application/json` [`Error`](#error) | JSON Error envelope; 401 unauthenticated, 403 without admin permission, 404 out of scope, 422 invalid pagination, 503 unavailable. |

#### `POST /v0/connectors/{connector_id}/tokens/{token_id}/rotate`

Operation `rotateConnectorToken`. Requires `connectors:admin`.

Replace a push token with five minutes of overlap

Requires connectors:admin on the instance's Organization and Corpus. Atomically issue a new token and mark this token rotated, valid for exactly five more minutes. The replacement has a new token_id and its secret is shown once with Cache-Control no-store. A revoked or already-rotated token is 409 token_inactive; repeating rotation cannot extend the overlap or reveal a secret again. Disabled instances are 409 connector_disabled. Revocation can cut the overlap short. No request body is needed; a lost response requires fresh issuance.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `connector_id` | path | string | yes | Minimum length `1`. |
| `token_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `201` | `application/json` [`ConnectorTokenCreated`](#connectortokencreated)<br><br>Header `Cache-Control`: string. | Replacement token with its one-time bearer secret. |
| `default` | `application/json` [`Error`](#error) | JSON Error envelope; 401 unauthenticated, 403 without admin permission, 404 unknown or foreign token, 409 inactive or disabled, 503 unavailable. |

#### `DELETE /v0/connectors/{connector_id}/tokens/{token_id}`

Operation `revokeConnectorToken`. Requires `connectors:admin`.

Revoke a push token immediately

Requires connectors:admin on the instance's Organization and Corpus. Revoke this token, even during rotation overlap or after the instance is disabled. Every authentication checks durable validity without a cache, so new authentications after the commit fail immediately; already admitted deliveries may finish. Revocation is idempotent and returns metadata only. Revoking an old token does not revoke its replacement; revoke each token_id that should lose access. No request body is needed.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `connector_id` | path | string | yes | Minimum length `1`. |
| `token_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`ConnectorToken`](#connectortoken) | Revoked token metadata, never a secret. |
| `default` | `application/json` [`Error`](#error) | JSON Error envelope; 401 unauthenticated, 403 without admin permission, 404 unknown or foreign token, 503 unavailable. |

#### `POST /v0/connectors/{connector_id}/api/{path}`

Operation `pushConnectorAPI`. API-key calls require `connector:push`. Authentication: `ApiKey` or `InstanceToken` or no API key (see operation description).

Push data to a declared source route

Resolve a named POST route from the instance kind's manifest (Plugin API 0.11; instance tokens and signatures since 0.12). For auth quivr_key, requires a Quivr bearer key with connector:push on the instance's Corpus and Organization, checked before any plugin call. For auth instance_token, requires this instance’s bearer token; Quivr keys and other instances’ tokens are refused with 401 invalid_instance_token. Missing or invalid key is 401; missing action 403; out-of-scope, disabled or unknown instance and undeclared path 404; undeclared method 405 with Allow. JSON body is bounded to 1 MiB and the query to 8192 bytes. Malformed JSON is 400; a request_schema mismatch is 422. Up to 64 bounded headers are relayed without Authorization, Cookie or hop-by-hop headers. The plugin and ingestion stage is bounded to 9 seconds; audit persistence adds at most 2 seconds. Accepted items use normal ingestion idempotency and return 202 with receipts in item order, including withdrawals; the accepted plugin answer is replaced. An empty delivery returns an empty receipts array. A rejected item returns 422 item_rejected; other items may already be accepted, so retrying their stable revisions replays the same receipts. Transient failures return 503 with Retry-After. Engine-generated failures return the JSON Error envelope with the engine's error code and no Quivr-Response-Origin header. A refused plugin verdict returns the plugin's 4xx status, content type and body unchanged, marked with Quivr-Response-Origin: plugin. That header selects the plugin-defined response variant (x-quivr-plugin-response), even when its status overlaps an engine response; the engine response schemas below apply to responses without that header. Per-instance token buckets use the deployment default unless push_policy overrides it. Excess requests return 429 with Retry-After (integer seconds) before plugin invocation; callers outside push_policy.allowed_cidrs return 403 ip_not_allowed. Only explicitly trusted proxy networks may supply X-Forwarded-For addresses. Every attempt for an existing instance commits connector.push.received (2xx) or connector.push.refused and per-instance admin counters before the response is sent. Audit storage failure returns 503. Legacy routes without declared signature ingress keep their existing behavior. For auth signature, the plugin verifies the provider signature; the engine checks one nonempty signature header and optional signed Unix-seconds timestamp within the declared window (401 invalid_signature). PostgreSQL reserves signature and Idempotency-Key fingerprints per instance for window_seconds before the plugin call; duplicate keys return 409 push_replayed. Signature routes bypass the response cache: once replay reservations expire, the plugin verifies every new admitted request, including one reusing an old Idempotency-Key. Signature storage failures return JSON Error 503. GET challenges bypass POST signature guards. X declares receive at api/receive with a 300-second signature cache and no timestamp. Its legacy connector-webhooks address shares the same guard, push policy, audit counters and receipts until engine 1.0.0.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `Idempotency-Key` | header | string |  | Optional opaque key of 1–256 bytes, scoped to the instance. Authorized requests replay the first completed plugin/ingestion answer within the deployment TTL for quivr_key and instance_token routes (default 24h), without calling the plugin or charging another rate token. Authentication, route/body validation, IP and rate refusals do not reserve keys. Reusing a key on another declared route still replays its original answer. Minimum length `1`. Maximum length `256`. |
| `connector_id` | path | string | yes |  |
| `path` | path | string | yes | The declared relative route path, including any nested segments and filled template values. Maximum length `8192`. |

**Request body** (required): `application/json` any

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `202` | `application/json` [`ConnectorPushReceipts`](#connectorpushreceipts) | Items accepted for ingestion; poll each Receipt through the normal API. |
| `429` | `application/json` [`Error`](#error)<br><br>Header `Retry-After`: integer. Seconds until the next rate token. | Instance token bucket exhausted; no plugin call or idempotency reservation. |
| `default` | `application/json` [`Error`](#error)<br><br>Header `Quivr-Response-Origin`: string. Present with value plugin only for a plugin-defined reply, whose status, content type and body are forwarded unchanged. | Structured engine error, or the plugin's refusal answer. |

#### `GET /v0/connectors/{connector_id}/api/{path}`

Operation `challengeConnectorAPI`. API-key calls require `connector:push`. Authentication: `ApiKey` or `InstanceToken` or no API key (see operation description).

Answer a declared source challenge

Resolve a declared GET route with the same request limits and deadline as POST. Routes with auth: quivr_key require connector:push authorization and instance scope. Only synchronous provider challenges are supported; a GET answer carrying ingestion items is rejected before ingestion. Reads and management stay on the normal API. Plugin challenge (2xx) and refusal (4xx) replies carry the plugin's status, content type and body unchanged, marked with Quivr-Response-Origin: plugin. That header selects the plugin-defined response variant (x-quivr-plugin-response), including provider JSON or text challenges and statuses that overlap engine errors. Engine-generated failures have no Quivr-Response-Origin header and follow the JSON Error schema below. A bodyless challenge is relayed with parsed body null. Unknown paths return 404; undeclared methods return 405 with Allow. Authentication follows the declared auth mode: quivr_key or instance_token; credentials for one mode cannot authenticate the other. A 204 challenge must have an empty response body; a plugin answer combining 204 with a nonempty body returns 500 plugin_invalid_response. The same IP allowlist, token bucket, idempotency and audit rules as POST apply. Signature routes bypass the response cache so every admitted call reaches provider verification. Signature GET routes need no bearer key and bypass POST signature and replay checks; the provider challenge is still verified and answered by the plugin.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `Idempotency-Key` | header | string |  | Optional opaque key of 1–256 bytes, scoped to the instance. Authorized requests replay the first completed plugin/ingestion answer within the deployment TTL for quivr_key and instance_token routes (default 24h), without calling the plugin or charging another rate token. Authentication, route/body validation, IP and rate refusals do not reserve keys. Reusing a key on another declared route still replays its original answer. Minimum length `1`. Maximum length `256`. |
| `connector_id` | path | string | yes |  |
| `path` | path | string | yes | The declared relative route path, including any nested segments and filled template values. Maximum length `8192`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `text/plain` string<br><br>Header `Quivr-Response-Origin`: string. Present with value plugin only for a plugin-defined reply, whose status, content type and body are forwarded unchanged. | The plugin's synchronous challenge answer. |
| `429` | `application/json` [`Error`](#error)<br><br>Header `Retry-After`: integer. Seconds until the next rate token. | Instance token bucket exhausted; no plugin call or idempotency reservation. |
| `default` | `application/json` [`Error`](#error)<br><br>Header `Quivr-Response-Origin`: string. Present with value plugin only for a plugin-defined reply, whose status, content type and body are forwarded unchanged. | Structured engine error, or the plugin's refusal answer. |

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

#### `POST /v0/connectors/{connector_id}/runs`

Operation `requestConnectorRun`. Requires `connectors:write`.

Ask for an acquisition run now instead of at the next scheduled time, for example to check again a source that failed. The next run is pulled in, never pushed out, so repeating the request changes nothing and a run already in flight answers it. Rate limits still hold -- the run starts no sooner than the deployment interval floor (30 s by default) after the previous run ended, nor before the Retry-After the source asked for. The run then goes through the usual scheduler lease and records its outcome in health, committing connector.health_changed when the state changes; the request itself commits no event. run_at is when the run is due; the scheduler starts it within seconds after. A disabled instance is 409 connector_disabled.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `connector_id` | path | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`ActionRequest`](#actionrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `202` | `application/json` [`ConnectorRunRequest`](#connectorrunrequest) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

### Connector webhooks

#### `GET /v0/connector-webhooks/{connector_id}`

Operation `relayConnectorChallenge`. No authentication.

Public webhook route of one Connector Instance whose kind declares the push mode; the address is its webhook_url. There is no API key; the connector plugin verifies the request (a signature, a challenge) with the Deposited Credential. A GET is typically a source's verification challenge, relayed to the plugin like a delivery.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `connector_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `2XX` |  | The connector plugin accepted the delivery; its items were ingested before this answer. The body and content type are the plugin's answer to the source (for example a challenge response). |
| `4XX` |  | The connector plugin refused the delivery (for example a bad signature), with its own body; nothing changed. 404 names no enabled instance of a kind that declares push; 413 a body over 1 MiB. |
| `500` |  | The delivery cannot be processed (the plugin reported an access or source error), shown in health.push. |
| `503` |  | Temporarily unavailable (the plugin is unreachable or ingestion is down); retry after Retry-After seconds. Polling catches up meanwhile. |

#### `POST /v0/connector-webhooks/{connector_id}`

Operation `relayConnectorDelivery`. No authentication.

Relay one delivery the source sends to the Connector Instance. The core passes the raw request (a body of at most 1 MiB, lowercase headers) to the connector plugin, which verifies it and returns the items it carries. Items converge with those of pull runs on the same Receipts. Deliveries update health.push.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `connector_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `2XX` |  | The connector plugin accepted the delivery; its items were ingested before this answer. The body and content type are the plugin's answer to the source (for example a challenge response). |
| `4XX` |  | The connector plugin refused the delivery (for example a bad signature), with its own body; nothing changed. 404 names no enabled instance of a kind that declares push; 413 a body over 1 MiB. |
| `500` |  | The delivery cannot be processed (the plugin reported an access or source error), shown in health.push. |
| `503` |  | Temporarily unavailable (the plugin is unreachable or ingestion is down); retry after Retry-After seconds. Polling catches up meanwhile. |

### Connector kinds

#### `GET /v0/connector-kinds`

Operation `listConnectorKinds`. Requires `connectors:read`.

Connector kinds enabled in this deployment, with the JSON Schemas that validate their config and credential secret, so clients can render configuration forms without knowing the kinds. credential_deposits tells whether this deployment accepts Deposited Credentials at all; when unavailable, any create carrying a credential and every rotation is 503 credentials_unavailable.

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`ConnectorKindCatalog`](#connectorkindcatalog) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; see contract HTTP mapping. |

### Admin

#### `GET /v0/admin/audit`

Operation `listAuditEvents`. Requires `audit:read`.

List immutable sensitive-action entries for the API key's organization, newest first. Requires audit:read and organization-wide Corpus scope. Time filters are RFC3339 instants; since is inclusive and until exclusive. Cursors bind organization, permissions and filters including limit; reuse the same filters on later pages. Reads and searches are not audited.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `since` | query | string (date-time) |  | Inclusive RFC3339 timestamp. |
| `until` | query | string (date-time) |  | Exclusive RFC3339 timestamp. |
| `actor` | query | string |  | Exact API key identifier; no substring matching. |
| `action` | query | string |  | Exact audit action name; no substring matching. |
| `target_type` | query | string |  | Exact target type; no substring matching. |
| `target_id` | query | string |  | Exact target identifier; no substring matching. |
| `limit` | query | integer |  | Default `50`. Minimum `1`. Maximum `200`. |
| `page_cursor` | query | string |  |  |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`AuditEventPage`](#auditeventpage) | An organization-scoped page of audit entries. |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 invalid API key, 403 without audit:read or with restricted Corpus scope, 422 invalid_cursor, 422 invalid_schema or invalid_limit, 503 storage_unavailable. |

#### `GET /v0/admin/plugins`

Operation `listPluginRegistrations`. Requires `plugins:admin`.

Every plugin version this deployment has registered, oldest first, with its endpoint, manifest digest, the roles its manifest declares and its state. Quivr never starts a plugin; the operator runs it at its endpoint. Every start records the plugins pinned in the startup configuration. Deployment-wide and not paginated. Requires plugins:admin, an operator action that organization keys do not get.

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`PluginRegistrationList`](#pluginregistrationlist) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 without plugins:admin, 503 storage unavailable. |

#### `POST /v0/admin/plugins`

Operation `registerPlugin`. Requires `plugins:admin`.

Register a plugin version the operator runs at an address. The request carries the exact quivr-plugin.yaml the plugin was built from, its endpoint and the settings it is installed with, with the shape and rules of a pin in the startup configuration (configuration, routes, kinds, spaces); plugin id and version come from the manifest. Quivr then checks it in the background with the Contract Runner against the endpoint, whose discovery must report the manifest's digest, using the normative fixtures and the fixtures the request carries. The registration is registered while its check runs, then validated or rejected with the report. The same idempotency key returns the same registration; a key already used for another registration or other fixtures is 409 idempotency_conflict; a new key for a rejected registration checks it again. 422 invalid_plugin lists what the engine refuses in the manifest, settings or fixture paths. The body is at most 8 MiB (413 request_too_large). Requires plugins:admin.

**Request body** (required): `application/json` [`PluginRegistrationRequest`](#pluginregistrationrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `202` | `application/json` [`PluginRegistration`](#pluginregistration)<br><br>Header `Location`: string (uri-reference). The registration's read URL, which reports the check once it ran. | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 400 malformed, 401 unauthenticated, 403 without plugins:admin, 409 idempotency_conflict, 413 request_too_large, 422 invalid_schema or invalid_plugin, 503 storage unavailable. |

#### `GET /v0/admin/plugins/{registration_id}`

Operation `getPluginRegistration`. Requires `plugins:admin`.

One registration with the Contract Runner's report once its check ran. Requires plugins:admin.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `registration_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`PluginRegistration`](#pluginregistration) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 without plugins:admin, 404 unknown registration, 503 storage unavailable. |

#### `POST /v0/admin/plugins/{registration_id}/activate`

Operation `activatePlugin`. Requires `plugins:admin`.

Make a validated registration serve every role it declares, as a new immutable Pipeline Plan that api and worker follow without restarting; the previous plan stays readable. Every other version of the same plugin leaves the plan, and so does every registration whose roles it takes over entirely. The new plan must keep the rules the engine applies at startup (one normalizer per media type, one provider per connector kind, valid ingestion source routes and retrieval providers per plugin id, extension namespace and vector space ownership); otherwise 409 plugin_conflict lists what breaks. 409 registration_not_validated for a registration that is not validated or inactive. Activating the registration that is already active promotes its evaluation source formats to served, retaining the previous owner for evaluation; it returns the active plan when there is nothing to promote. The target must answer discovery with its exact manifest digest, including for an unchanged activation; otherwise 409 plugin_unreachable names the registration and cause and no plan changes. Work already started keeps its earlier plan. A new version of an alert-rule plugin serves the Subscription Versions created or edited from then on. Every Subscription Version keeps the version it recorded, which keeps judging it and stays draining while Subscriptions pin it, until migrateSubscriptionEvaluators moves them. Requires plugins:admin.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `registration_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`PipelinePlan`](#pipelineplan) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 without plugins:admin, 404 unknown registration, 409 registration_not_validated, plugin_conflict or plugin_unreachable, 503 storage unavailable. |

#### `GET /v0/admin/plugins/plans/{plan_id}`

Operation `getPipelinePlan`. Requires `plugins:admin`.

Any Pipeline Plan this deployment recorded, active or not. Plans are immutable, so an earlier plan stays readable after an activation. Requires plugins:admin.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `plan_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`PipelinePlan`](#pipelineplan) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 without plugins:admin, 404 unknown plan, 503 storage unavailable. |

#### `GET /v0/admin/plugins/plans`

Operation `listPipelinePlans`. Requires `plugins:admin`.

The latest Pipeline Plans this deployment recorded, newest first. Each names the plan it replaced (previous_plan_id) and what recorded it (source), so this list is the history of plan changes; earlier plans stay reachable through previous_plan_id. Requires plugins:admin.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `limit` | query | integer |  | The most items to return. An empty, non-integer or out-of-range value is 422 invalid_limit. Default `20`. Minimum `1`. Maximum `100`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`PipelinePlanList`](#pipelineplanlist) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 without plugins:admin, 422 invalid_limit, 503 storage unavailable. |

#### `POST /v0/admin/backfills`

Operation `requestBackfill`. Requires `plugins:admin`.

Reprocess a Corpus's past Versions with a selected active ingestion plugin, to fill vector spaces of the generation the Corpus is routed to, typically a new evaluation space. The scope is the Corpus and, optionally, a window on when Quivr accepted its Versions. Only Versions segmented by the selected plugin whose segments all hold a vector in the served space and miss one in a target space are processed; live enrichment fills the target spaces for newer Versions once the backfill has started. A dry run is required. dry_run true answers 200 with the estimate and records it under the idempotency key. The same body with dry_run false then accepts the backfill as a queued Operation (202, Location). Without a dry run recorded under that key and scope, the answer is 409 dry_run_required. An estimated cost above the deployment's backfill.max_cost_without_confirmation needs confirm_cost true, otherwise 409 cost_confirmation_required. The same key with another scope is 409 idempotency_conflict, and an accepted key replays its Operation. A Corpus whose previous backfill has not finished is 409 backfill_in_progress. The backfill runs on its own task queue at the deployment's backfill.rate, pinned to the Pipeline Plan active when it starts. It can be paused, resumed and canceled, and resumes from its checkpoint after a restart. It never creates Record Versions or content events. Versions whose projected segments the plugin would cut differently are skipped and counted (segmentation_differs), and a rebuild re-segments them. Requires plugins:admin, on a key of the Corpus's Organization that grants the Corpus.

**Request body** (required): `application/json` [`BackfillRequest`](#backfillrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`BackfillEstimate`](#backfillestimate) | The dry run's estimate, recorded under the key. |
| `202` | `application/json` [`Operation`](#operation)<br><br>Header `Location`: string. | The accepted backfill Operation; read it at the Location. |
| `default` | `application/json` [`Error`](#error) | Structured error; 400 malformed, 401 unauthenticated, 403 without plugins:admin, 404 unknown Corpus, 409 dry_run_required, cost_confirmation_required, idempotency_conflict, backfill_in_progress, registration_not_active or rebuild_required (the Corpus's generation predates named vector spaces), 422 invalid_schema or invalid_backfill (a space the plugin does not declare or the deployment does not enable, or an empty window), 503 storage unavailable. |

#### `POST /v0/admin/spaces/{vector_space_id}/promote`

Operation `promoteVectorSpace`. Requires `plugins:admin`.

Make a registered evaluation space the one search uses, in one call, for the whole deployment. Coverage must be complete, that is every Corpus's routed generation carries the space with a vector for every current segment. Otherwise the answer is 409 coverage_incomplete, with the Corpora and segments it misses in the message, unless force is true. Every generation that carries the space and served the previous one serves it from then on, the default one included, so new Corpora start on it. The previous served space becomes an evaluation space and keeps its vectors, so promoting it again restores it. The choice survives restarts, activations and rollbacks while the ingestion plugin still enables both spaces. Promoting the served space changes nothing. Requires plugins:admin.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `vector_space_id` | path | string | yes | Minimum length `1`. |

**Request body** (required): `application/json` [`VectorSpacePromotionRequest`](#vectorspacepromotionrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`VectorSpacePromotion`](#vectorspacepromotion) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 400 malformed, 401 unauthenticated, 403 without plugins:admin, 404 unknown space, 409 coverage_incomplete, 422 invalid_schema or not_evaluation_space (a retired space), 503 storage unavailable. |

#### `GET /v0/admin/quarantine`

Operation `listQuarantinedVersions`. Requires `plugins:admin`.

The Record Versions stuck in quarantine, in Version id order, with the step each failed at (normalization or ingestion), its reason and when it was quarantined. Stuck means the Version can still become current; a Version its Record no longer desires (a newer revision was accepted) or whose Record was withdrawn is not listed. A reason recorded before quarantine reasons were structured has only its code and names no plugin, and its quarantined_at is when its revision was accepted. Every filter narrows the list; plugin matches the plugin id the reason names. Requires plugins:admin; a key limited to some Corpora lists theirs only. The cursor is bound to the key's scope and filters.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `corpus_id` | query | string |  | Minimum length `1`. |
| `plugin` | query | string |  | Minimum length `1`. |
| `code` | query | string |  | Minimum length `1`. |
| `quarantined_after` | query | string (date-time) |  |  |
| `quarantined_before` | query | string (date-time) |  |  |
| `page_cursor` | query | string |  | Minimum length `1`. |
| `limit` | query | integer |  | The most items to return. An empty, non-integer or out-of-range value is 422 invalid_limit. Default `100`. Minimum `1`. Maximum `100`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`QuarantinePage`](#quarantinepage) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 without plugins:admin, 404 a corpus_id outside the key's Corpora, 409 cursor_scope_changed, 422 invalid query, limit, cursor or window, 503 storage unavailable. |

#### `POST /v0/admin/quarantine/reprocess`

Operation `reprocessQuarantine`. Requires `plugins:admin`.

Rerun, with the Pipeline Plan active now, the step a Corpus's stuck Versions failed at, typically after a plugin was fixed and activated or a plan rolled back. A Version quarantined at normalization is normalized again, published with its new Manifest and processed; one quarantined at ingestion is segmented, embedded and indexed again. A Version that succeeds goes through the normal path, as for a first success. It becomes current if its Record still desires it, searchable, its alerts are evaluated, and the change feed announces it (record.materialized when its content changed, record.retrieval_ready, record.enrichment_available). A Version that fails again stays quarantined with its new reason. The scope is the Corpus's stuck Versions (see listQuarantinedVersions), kept by the optional filters, taken when the reprocess is accepted. Set from_stage to normalization to normalize its stored source Blob again before ingestion; omit it to retry the failed step. A dry run is required. dry_run true answers 200 with the count and records it under the idempotency key. The same body with dry_run false then accepts the reprocess as a queued Operation (202, Location). Without a dry run recorded under that key, scope and restart stage, the answer is 409 dry_run_required. The same key with another scope is 409 idempotency_conflict, and an accepted key replays its Operation. A Corpus whose previous reprocess has not finished is 409 reprocess_in_progress. The reprocess runs on the backfills' task queue at the deployment's backfill.rate, pinned to the plan active when it is accepted. It can be paused, resumed, canceled and rerun. Its counters are versions_in_scope, versions_recovered, versions_quarantined (failed again) and versions_skipped (with skipped_<reason>). Requires plugins:admin, on a key of the Corpus's Organization that grants the Corpus.

**Request body** (required): `application/json` [`QuarantineReprocessRequest`](#quarantinereprocessrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`QuarantineReprocessEstimate`](#quarantinereprocessestimate) | The dry run's count, recorded under the key. |
| `202` | `application/json` [`Operation`](#operation)<br><br>Header `Location`: string. | The accepted reprocess Operation; read it at the Location. |
| `default` | `application/json` [`Error`](#error) | Structured error; 400 malformed, 401 unauthenticated, 403 without plugins:admin, 404 unknown Corpus, 409 dry_run_required, idempotency_conflict or reprocess_in_progress, 422 invalid_schema or invalid_reprocess (an empty window), 503 storage unavailable. |

#### `POST /v0/admin/subscriptions/evaluator-migrations`

Operation `migrateSubscriptionEvaluators`. Requires `plugins:admin`.

Move the key's Organization's Subscriptions from one version of an alert-rule plugin to the version the active Pipeline Plan serves, after that plugin's upgrade or rollback. It looks at the Subscriptions that are not deleted, enabled or disabled, whose current Version pins plugin_id at from_version, in Subscription id order, at most limit of them, after after. Each one's Saved Query expression and evaluator configuration must validate against the served version's schemas first; one that does not is listed in refused with the first issue and keeps its version. A moved Subscription gets a new current Version that keeps its Saved Query Version, configuration and destination and pins the served version. Like an edit, it judges only changes committed after it, nothing is re-evaluated, Matches keep the Version that produced them, and subscription.updated is announced in each of its Corpora. dry_run true validates and lists what would move without moving anything. A call moves each Subscription at most once; calling again with next_after continues, and repeating a call converges. Subscriptions whose Corpora the key does not all grant are left out. 422 unsupported_evaluator when no version of plugin_id serves new Subscriptions, invalid_migration when from_version is that version. Requires plugins:admin.

**Request body** (required): `application/json` [`SubscriptionEvaluatorMigrationRequest`](#subscriptionevaluatormigrationrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`SubscriptionEvaluatorMigration`](#subscriptionevaluatormigration) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 400 malformed, 401 unauthenticated, 403 without plugins:admin, 422 invalid_schema, unsupported_evaluator or invalid_migration, 503 storage unavailable. |

#### `GET /v0/admin/subscriptions/evaluation-backlog`

Operation `listEvaluationBacklog`. Requires `plugins:admin`.

Read evaluation counts grouped by the pinned evaluator plugin id and version, only in the key's Organization and granted Corpora. unavailable counts pending intents whose latest error is evaluator_unavailable, not a live endpoint health check. retired counts terminal evaluator_retired intents. Rows with neither pending nor retired evaluations are omitted. Requires plugins:admin. Pages are ordered by plugin_id@version; next_after is the next after.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `limit` | query | integer |  | Maximum evaluator versions returned; a present invalid value is 422 invalid_limit. Default `100`. Minimum `1`. Maximum `500`. |
| `after` | query | string |  | The previous page's next_after. Counts are live, not a snapshot across pages. Minimum length `1`. Maximum length `256`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`EvaluationBacklogPage`](#evaluationbacklogpage) | Scoped per-version counts |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 without plugins:admin, 404 not configured, 422 invalid_query or invalid_limit, 503 storage unavailable. |

#### `POST /v0/admin/subscriptions/evaluation-retirements`

Operation `retireEvaluations`. Requires `plugins:admin`.

Explicitly abandon a bounded batch of pending evaluations pinned to the named evaluator whose latest error is evaluator_unavailable. Only the key's Organization and granted Corpora are considered. Live leases and withdrawals are left alone. Never migrate an intent, run another evaluator, create a Match or Delivery, or declare no_match. The terminal outcome is evaluator_retired. Each real retirement emits evaluation.retired in the intent's Corpus, with resource.kind=evaluation_retirement and resource.id=retirement_id. The immutable receipt records the reason, original identities and event ids, and is readable at the Location. A real key replay returns the original batch, including its original counts; another request or Corpus scope using that key is 409 idempotency_conflict. An empty real batch also reserves its key. dry_run selects without writes or reserving the key, and remaining reports how many unavailable intents would remain after that selection. Use new real keys for further batches and recheck live backlog counts after dispatch and leases settle. This action does not establish a policy for future intents. It deliberately discards potential alerts; restore the exact old implementation to drain without losing judgments. Requires plugins:admin.

**Request body** (required): `application/json` [`EvaluationRetirementRequest`](#evaluationretirementrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`EvaluationRetirement`](#evaluationretirement)<br><br>Header `Location`: string. The receipt read URL, present only for a real batch. | The dry-run preview or committed audit receipt |
| `default` | `application/json` [`Error`](#error) | Structured error; 400 malformed, 401 unauthenticated, 403 without plugins:admin, 404 not configured, 409 idempotency_conflict, 422 invalid_schema, invalid_input or invalid_limit, 503 storage unavailable. |

#### `GET /v0/admin/subscriptions/evaluation-retirements/{retirement_id}`

Operation `getEvaluationRetirement`. Requires `plugins:admin`.

Read the immutable audit receipt referenced by an evaluation.retired event or the retirement action's Location. Requires plugins:admin and grants covering the original request's entire Corpus scope; a deployment-wide request requires a key granting all Corpora. Unknown, another Organization's and ungranted receipts all answer 404 not_found.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `retirement_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`EvaluationRetirement`](#evaluationretirement) | The committed receipt |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 without plugins:admin, 404 not_found, 503 storage unavailable. |

#### `POST /v0/admin/plugins/plan/rollback`

Operation `rollbackPipelinePlan`. Requires `plugins:admin`.

Make an earlier plan's roles active again, as a new immutable Pipeline Plan with source rollback that api and worker follow without restarting. The default target is the plan the active one replaced; plan_id names another one. Registrations the rollback brings back serve again, even when they were draining or inactive. Registrations it takes out drain, or stop with pinned_work=stop. The target must keep the rules of an activation. Otherwise the answer is 409 plugin_conflict. An alert-rule version it takes out keeps judging the Subscription Versions that pin it, whatever pinned_work says, as after an activation. Every plugin it brings back must answer discovery at its endpoint with its manifest's digest. Otherwise the answer is 409 plugin_unreachable, naming the plugin and the cause, and no plan changes. Restore that registration's exact build at its endpoint, or activate the current registration of the previous ingestion owner to restore its evaluated source formats while retaining the newer build. Historical plans are never rewritten to follow a redeployed build. 409 no_previous_plan when there is no earlier plan to return to. A rollback to the active plan's roles returns the active plan. The same idempotency key with the same request returns the plan it recorded. The same key with another request is 409 idempotency_conflict. Nothing already produced is rewritten. Requires plugins:admin.

**Request body** (required): `application/json` [`PipelinePlanRollbackRequest`](#pipelineplanrollbackrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`PipelinePlan`](#pipelineplan) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 400 malformed, 401 unauthenticated, 403 without plugins:admin, 404 unknown plan, 409 idempotency_conflict, no_previous_plan, plugin_conflict, plugin_unreachable or registration_not_validated, 422 invalid_schema, 503 storage unavailable. |

#### `GET /v0/admin/plugins/plan`

Operation `getActivePipelinePlan`. Requires `plugins:admin`.

The active Pipeline Plan, an immutable mapping of every role of the deployment to the registration serving it, which api and worker follow. 404 not_found when no plan is active, because the startup configuration pins no plugin. Requires plugins:admin.

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`PipelinePlan`](#pipelineplan) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 without plugins:admin, 404 no active plan, 503 storage unavailable. |

#### `GET /v0/admin/active-plugins`

Operation `listActivePlugins`. Requires `observability:read`.

The plugin versions the active Pipeline Plan runs and the roles each serves, for operator views that read the plugin call rollups beside them. It names no address, configuration, manifest or digest, so it needs observability:read on a key that grants every Corpus, not plugins:admin. Empty when no plan is active.

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`ActivePluginList`](#activepluginlist) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 without observability:read on every Corpus, 404 on a deployment without a plugin registry, 503 storage unavailable. |

#### `GET /v0/admin/documents`

Operation `listAdminDocuments`. Requires `observability:read`.

The Organization's most recently accepted Record Versions across all its Corpora, newest first, with their Source, title, current state and step times, read in one query. Requires observability:read on a key for all Corpora; a key limited to some Corpora gets 403. Versions accepted before step times were recorded are not listed. Pages are independent reads, not a snapshot; the cursor is bound to the key's scope.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `page_cursor` | query | string |  | Minimum length `1`. |
| `limit` | query | integer |  | The most items to return. An empty, non-integer or out-of-range value is 422 invalid_limit. Default `100`. Minimum `1`. Maximum `100`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`AdminDocumentPage`](#admindocumentpage) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 without observability:read or all Corpora, 409 cursor_scope_changed, 422 invalid query, limit or cursor, 503 storage unavailable. |

#### `GET /v0/admin/documents/{version_id}/timeline`

Operation `getDocumentTimeline`. Requires `observability:read`.

One Record Version's finished steps in time order, each with how long it took since the step that caused it and, when known, the plugin that ran it. 404 for an unknown Version or one outside the key's Corpora. Requires observability:read.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `version_id` | path | string | yes | Minimum length `1`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`DocumentTimeline`](#documenttimeline) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 without observability:read, 404 not found, 503 storage unavailable. |

#### `GET /v0/admin/stats/connector-pushes`

Operation `listConnectorPushStats`. Requires `observability:read`.

Count accepted and refused pushes per source instance

Per-instance attempts to declared source API routes, split into received (2xx, including cached answers and GET challenges) and refused (all other answers). Includes authentication, scope, body, rate and IP refusals for existing instances. No payload, credentials, client IP or raw idempotency key is stored. Audit events and counters commit together. Requires observability:read on all Corpora; returns only the key's Organization. Uses the Spec 16 rollup windows and retention. By default lists the largest 100 instance/outcome pairs, with a total across every pair. Use connector_id to read both outcomes of one instance; its total then covers that instance only.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `window` | query | string |  | One of `1h`, `24h`, `7d`. Default `1h`. |
| `limit` | query | integer |  | Largest instance/outcome pairs to list; cannot be combined with connector_id. Default `100`. Minimum `1`. Maximum `100`. |
| `connector_id` | query | string |  | Read both outcomes of this instance; cannot be combined with limit. Minimum length `1`. Maximum length `256`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`ConnectorPushStatsList`](#connectorpushstatslist) | Per-instance counters and time buckets. |
| `default` | `application/json` [`Error`](#error) | Structured error. |

#### `GET /v0/admin/stats/plugins`

Operation `getPluginCallStats`. Requires `observability:read`.

Calls, errors and latency of every plugin Contribution invoked for the key's Organization over the window, per plugin version and operation, with the last error code the plugin declared (or plugin_unavailable, invalid_output). Counts are written by each process every few seconds and kept 7 days; a window reads buckets of one resolution (1 minute for 1h, 15 minutes for 24h, 2 hours for 7d) and lists only non-empty ones. Latency percentiles are interpolated from fixed buckets. Requires observability:read on a key that grants every Corpus.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `window` | query | string |  | One of `1h`, `24h`, `7d`. Default `1h`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`PluginCallStatsList`](#plugincallstatslist) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 without observability:read on every Corpus, 422 invalid_window, 503 storage unavailable. |

#### `GET /v0/admin/stats/searches`

Operation `getSearchStats`. Requires `observability:read`.

Searches of the key's Organization over the window per mode and search profile, with errors, latency, the number of results returned and the number of searches over the profile's latency objective, in the buckets of getPluginCallStats. The profile is unknown for a search that named a profile the deployment does not serve. Requires observability:read on a key that grants every Corpus.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `window` | query | string |  | One of `1h`, `24h`, `7d`. Default `1h`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`SearchStatsList`](#searchstatslist) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 without observability:read on every Corpus, 422 invalid_window, 503 storage unavailable. |

#### `GET /v0/admin/stats/steps`

Operation `getStepStats`. Requires `observability:read`.

Processing steps of the key's Organization over the window, in the buckets of getPluginCallStats: baseline (cut into segments and made searchable by keyword), enrichment (vectors added) and accepted_to_searchable (from acceptance to searchable by keyword) time the worker's runs. materialized, segmented, retrieval_ready and enriched are the document timeline's steps, each timed from the step that causes it, so they include the time a Record Version waited for the step. An error is a step that is retried or blocked, with its code. Requires observability:read on a key that grants every Corpus.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `window` | query | string |  | One of `1h`, `24h`, `7d`. Default `1h`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`StepStatsList`](#stepstatslist) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 without observability:read on every Corpus, 422 invalid_window, 503 storage unavailable. |

#### `GET /v0/admin/stats/received`

Operation `getReceivedStats`. Requires `observability:read`.

Documents received by the key's Organization over the window per source namespace, in the buckets of getPluginCallStats. A document is counted when a command reserves a new revision of a Record; a replayed command or a revision the Record already had is not. Lists the limit namespaces with most documents, largest first; total and sources cover every namespace. Requires observability:read on a key that grants every Corpus.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `window` | query | string |  | One of `1h`, `24h`, `7d`. Default `1h`. |
| `limit` | query | integer |  | The most items to return. An empty, non-integer or out-of-range value is 422 invalid_limit. Default `10`. Minimum `1`. Maximum `100`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`ReceivedStatsList`](#receivedstatslist) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 without observability:read on every Corpus, 422 invalid_window or invalid_limit, 503 storage unavailable. |

#### `GET /v0/admin/stats/matches`

Operation `getMatchStats`. Requires `observability:read`.

Matches the Subscriptions of the key's Organization committed over the window, per evaluator plugin, in the buckets of getPluginCallStats. A Match that corrects an earlier one on a new Version of the same Record counts too. Requires observability:read on a key that grants every Corpus.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `window` | query | string |  | One of `1h`, `24h`, `7d`. Default `1h`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`MatchStatsList`](#matchstatslist) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 without observability:read on every Corpus, 422 invalid_window, 503 storage unavailable. |

#### `GET /v0/admin/stats/top-queries`

Operation `getTopQueries`. Requires `observability:read`.

The most frequent search queries of the key's Organization over the window, normalized (lowercased, white space collapsed, at most 200 characters) and counted per hour, each with its hourly counts. Query text is recorded only when the deployment sets observability.record_query_text; otherwise recording is false and the list is empty. Requires observability:read on a key that grants every Corpus.

**Parameters**

| Name | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `window` | query | string |  | One of `1h`, `24h`, `7d`. Default `1h`. |
| `limit` | query | integer |  | The most items to return. An empty, non-integer or out-of-range value is 422 invalid_limit. Default `20`. Minimum `1`. Maximum `100`. |

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`TopQueryList`](#topquerylist) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 without observability:read on every Corpus, 422 invalid_window or invalid_limit, 503 storage unavailable. |

### Search

#### `POST /v0/search`

Operation `searchRecords`. Requires `content:read`, `search:query`.

Resolve the requested profile, compile mandatory Corpus/Organization prefilters and any requested filter, obtain candidates, then canonically hydrate and reauthorize every returned segment. Lexical-first records remain eligible without embeddings; semantic-only queries require vector coverage. Profile selection does not change access/currentness rules. When a retrieval plugin is pinned, it ranks. It asks the engine for candidates in up to three rounds and returns its ranking, which may hold only candidates the engine served in this search, each already authorized and hydrated. Each API process admits at most 64 active searches across callers. At capacity it immediately returns retryable 503 search_unavailable with Retry-After before storage or plugin work.

**Request body** (required): `application/json` [`SearchRequest`](#searchrequest)

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`SearchResponse`](#searchresponse) | Successful response |
| `default` | `application/json` [`Error`](#error)<br><br>Header `Retry-After`: integer. Present when search capacity is full; minimum delay in seconds before retrying. | Structured error; 400 malformed, 401 unauthenticated, 403 unauthorized scope/action, 404 absent/inaccessible, 409 idempotency conflict, 422 unsupported_profile, query_too_long (the query is over the profile's or the vector space owner's length limit; the message names it), unsupported_search or source_filter_unavailable, 502 retrieval_plugin_invalid (the retrieval plugin broke its contract, for example ranked a segment the engine never served it), 503 dependency unavailable, 504 search_deadline_exceeded (the retrieval plugin's rounds outran the profile's hard bound, four times max_latency_ms; a dependency that does not answer in time is 503). At search capacity, 503 search_unavailable includes Retry-After; retry only after that delay. |

#### `GET /v0/search/profiles`

Operation `listSearchProfiles`. Requires `search:query`.

Every installed search profile, the profile aliased as default first, with full names, short names and budgets; the pinned retrieval plugin (core.retrieve unless another is pinned) declares them.

**Responses**

| Status | Body | Description |
| --- | --- | --- |
| `200` | `application/json` [`SearchProfileList`](#searchprofilelist) | Successful response |
| `default` | `application/json` [`Error`](#error) | Structured error; 401 unauthenticated, 403 unauthorized scope/action. |

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

### `BuildVersion`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `version` | string | yes | Distribution release version, or dev for an unversioned build. |
| `revision` | string | yes | Full source commit, or unknown for an unversioned build. |
| `api_version` | string | yes | One of `v0`. |
| `plugin_engine_version` | string | yes | Engine compatibility version checked against plugin manifests, independent of the distribution release. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required: [version, revision, api_version, plugin_engine_version]
properties:
  version:
    type: string
    description: Distribution release version, or dev for an unversioned build.
  revision:
    type: string
    description: Full source commit, or unknown for an unversioned build.
  api_version:
    type: string
    enum: [v0]
  plugin_engine_version:
    type: string
    description: Engine compatibility version checked against plugin manifests, independent of the distribution release.
```

</details>

### `AuditEvent`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string | yes | Decimal audit identifier, represented as text to preserve bigint precision. Pattern `^[1-9][0-9]*$`. |
| `time` | string (date-time) | yes |  |
| `actor` | string | yes | SHA-256-derived API key identifier; empty for unauthenticated attempts. |
| `action` | string | yes |  |
| `target_type` | string | yes |  |
| `target_id` | string | yes | Target identifier; empty if refusal occurred before the target could be resolved. |
| `organization` | string | yes |  |
| `outcome` | string | yes | One of `accepted`, `refused`. |
| `request_id` | string | yes |  |
| `detail` | object | yes |  |
| `detail.status` | integer | yes | Minimum `100`. Maximum `599`. |
| `detail.error_code` | string |  |  |
| `detail.plan_id` | string |  |  |
| `detail.credential_version` | integer |  | Minimum `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required: [id, time, actor, action, target_type, target_id, organization, outcome, request_id, detail]
properties:
  id: {type: string, pattern: '^[1-9][0-9]*$', description: 'Decimal audit identifier, represented as text to preserve bigint precision.'}
  time: {type: string, format: date-time}
  actor: {type: string, description: SHA-256-derived API key identifier; empty for unauthenticated attempts.}
  action: {type: string}
  target_type: {type: string}
  target_id: {type: string, description: Target identifier; empty if refusal occurred before the target could be resolved.}
  organization: {type: string}
  outcome: {type: string, enum: [accepted, refused]}
  request_id: {type: string}
  detail:
    type: object
    additionalProperties: false
    required: [status]
    properties:
      status: {type: integer, minimum: 100, maximum: 599}
      error_code: {type: string}
      plan_id: {type: string}
      credential_version: {type: integer, minimum: 1}
```

</details>

### `AuditEventPage`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `items` | array of [`AuditEvent`](#auditevent) | yes |  |
| `next_page_cursor` | string |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required: [items]
properties:
  items:
    type: array
    items:
      $ref: '#/components/schemas/AuditEvent'
  next_page_cursor: {type: string}
```

</details>

### `ConnectorToken`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `token_id` | string | yes |  |
| `prefix` | string | yes | Display prefix only; cannot authenticate. |
| `created_at` | string (date-time) | yes |  |
| `rotated_at` | string (date-time) |  |  |
| `revoked_at` | string (date-time) |  |  |
| `valid_until` | string (date-time) |  | Exclusive expiry of an old token after rotation; absent for an unrotated token. |

Example `connector_token_revoked`:

```json
{
  "token_id": "token_example_rotated",
  "prefix": "qit_example",
  "created_at": "2026-10-01T10:00:00Z",
  "rotated_at": "2026-10-02T10:00:00Z",
  "valid_until": "2026-10-02T10:05:00Z",
  "revoked_at": "2026-10-02T10:02:00Z"
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required: [token_id, prefix, created_at]
properties:
  token_id: {type: string}
  prefix: {type: string, description: Display prefix only; cannot authenticate.}
  created_at: {type: string, format: date-time}
  rotated_at: {type: string, format: date-time}
  revoked_at: {type: string, format: date-time}
  valid_until: {type: string, format: date-time, description: Exclusive expiry of an old token after rotation; absent for an unrotated token.}
```

</details>

### `ConnectorTokenCreated`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `token` | [`ConnectorToken`](#connectortoken) | yes |  |
| `secret` | string | yes | One-time bearer secret; never persisted or returned by metadata reads. |

Example `connector_token_issued`:

```json
{
  "token": {
    "token_id": "token_example_active",
    "prefix": "qit_example",
    "created_at": "2026-10-01T10:00:00Z"
  },
  "secret": "illustrative-secret-shown-once"
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required: [token, secret]
properties:
  token: {$ref: '#/components/schemas/ConnectorToken'}
  secret: {type: string, description: One-time bearer secret; never persisted or returned by metadata reads.}
```

</details>

### `ConnectorTokenList`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `items` | array of [`ConnectorToken`](#connectortoken) | yes |  |
| `next_after` | string |  |  |

Example `connector_token_page`:

```json
{
  "items": [
    {
      "token_id": "token_example_active",
      "prefix": "qit_example",
      "created_at": "2026-10-01T10:00:00Z"
    },
    {
      "token_id": "token_example_rotated",
      "prefix": "qit_example",
      "created_at": "2026-10-01T10:00:00Z",
      "rotated_at": "2026-10-02T10:00:00Z",
      "valid_until": "2026-10-02T10:05:00Z",
      "revoked_at": "2026-10-02T10:02:00Z"
    }
  ],
  "next_after": "token_example_rotated"
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required: [items]
properties:
  items:
    type: array
    items: {$ref: '#/components/schemas/ConnectorToken'}
  next_after: {type: string}
```

</details>

### `ConnectorPushReceipts`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `receipts` | array of [`Receipt`](#receipt) | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required: [receipts]
properties:
  receipts:
    type: array
    items: {$ref: '#/components/schemas/Receipt'}
```

</details>

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
| `request_id` | string |  | Bounded caller X-Request-ID, or an engine-generated correlation ID. |
| `trace_id` | string |  | W3C trace ID when a trace context is present. |
| `span_id` | string |  | W3C span ID of the API handler when a trace context is present. |
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
  request_id:
    type: string
    description: Bounded caller X-Request-ID, or an engine-generated correlation ID.
  trace_id:
    type: string
    description: W3C trace ID when a trace context is present.
  span_id:
    type: string
    description: W3C span ID of the API handler when a trace context is present.
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
pinned manifest) is retried with backoff and produces no diagnostic while the active Pipeline
Plan names it; the Receipt shows plugin_unavailable while it retries.

- pinned_plugin_unavailable: the processing of this Version started on a Pipeline Plan whose
  normalizer or ingestion plugin an operator has since replaced, and that plugin could not be
  reached, or could no longer serve it, for the deployment's attempt budget. The work is never
  moved to the plugin that
  replaced it: the Version is quarantined, or, when its text was already searchable, its
  enrichment stops. plan, plugin and plugin_version name the plan and the plugin version.
- pinned_plan_stopped: the processing of this Version started on a Pipeline Plan that an operator
  rolled back with pinned_work=stop. At its next call to a plugin that left the active plan, the
  work stopped instead of calling it, with the same outcome and fields as
  pinned_plugin_unavailable.

Quarantined Versions keep their input reference and reason. An operator lists them and reprocesses
them with the active plan (listQuarantinedVersions, reprocessQuarantine); a reprocess that fails
again replaces the reason.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `code` | string | yes | Minimum length `1`. |
| `message` | string | yes | Minimum length `1`. |
| `retryable` | boolean | yes | Whether the same input may succeed if processed again. |
| `plugin` | string |  | Plugin id of the invocation. Minimum length `1`. |
| `plugin_version` | string |  | Plugin version of a pinned_plugin_unavailable or pinned_plan_stopped diagnostic. Minimum length `1`. |
| `plan` | string |  | Pipeline Plan the stopped work was pinned to, in a pinned_plugin_unavailable or pinned_plan_stopped diagnostic. Minimum length `1`. |
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
  pinned manifest) is retried with backoff and produces no diagnostic while the active Pipeline
  Plan names it; the Receipt shows plugin_unavailable while it retries.

  - pinned_plugin_unavailable: the processing of this Version started on a Pipeline Plan whose
    normalizer or ingestion plugin an operator has since replaced, and that plugin could not be
    reached, or could no longer serve it, for the deployment's attempt budget. The work is never
    moved to the plugin that
    replaced it: the Version is quarantined, or, when its text was already searchable, its
    enrichment stops. plan, plugin and plugin_version name the plan and the plugin version.
  - pinned_plan_stopped: the processing of this Version started on a Pipeline Plan that an operator
    rolled back with pinned_work=stop. At its next call to a plugin that left the active plan, the
    work stopped instead of calling it, with the same outcome and fields as
    pinned_plugin_unavailable.

  Quarantined Versions keep their input reference and reason. An operator lists them and reprocesses
  them with the active plan (listQuarantinedVersions, reprocessQuarantine); a reprocess that fails
  again replaces the reason.
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
  plugin_version:
    type: string
    minLength: 1
    description: Plugin version of a pinned_plugin_unavailable or pinned_plan_stopped diagnostic.
  plan:
    type: string
    minLength: 1
    description: Pipeline Plan the stopped work was pinned to, in a pinned_plugin_unavailable or pinned_plan_stopped diagnostic.
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
| `accepted_at` | string (date-time) |  | When Quivr accepted the revision this Version publishes, before any processing. |
| `manifest` | [`ManifestContent`](#manifestcontent) | yes |  |
| `extensions` | [`Extensions`](#extensions) |  |  |
| `provenance` | [`Provenance`](#provenance) |  |  |
| `availability` | [`Availability`](#availability) | yes |  |
| `relations` | array of [`ResolvedRelation`](#resolvedrelation) | yes |  |
| `processing` | [`ProcessingSummary`](#processingsummary) | yes |  |
| `steps` | [`VersionSteps`](#versionsteps) |  |  |
| `diagnostics` | array of [`Diagnostic`](#diagnostic) |  | Why the Version needs attention. A quarantined Version lists its reason first. A Version published through an optional route's fallback lists the normalizer failure it fell back from, and a recorded normalizer_conflict is listed on the Version whose output was kept. Omitted when there is nothing to report. At most `20` items. |

Example `version_relations`:

```json
{
  "record_id": "record_1",
  "version_id": "version_2",
  "accepted_at": "2026-09-29T10:00:00Z",
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
  accepted_at:
    type: string
    format: date-time
    description: When Quivr accepted the revision this Version publishes, before any processing.
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
  steps:
    $ref: '#/components/schemas/VersionSteps'
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

### `VersionSteps`

When each processing step of the Version finished, written once by the transaction that commits the step. A step not finished yet is omitted. Versions materialized before step times were recorded carry only accepted_at, and withdrawn_at for a withdrawal after; history is not reconstructed.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `accepted_at` | string (date-time) |  | Quivr accepted the revision. |
| `materialized_at` | string (date-time) |  | The Version was published from normalized content. |
| `segmented_at` | string (date-time) |  | The Version was first cut into segments. |
| `retrieval_ready_at` | string (date-time) |  | The Version first became searchable. |
| `enriched_at` | string (date-time) |  | Vectors were first attached to the Version. |
| `evaluated_at` | string (date-time) |  | Every Subscription asked to evaluate the Version had decided; one that waits for vectors decides on their round. Omitted when no Subscription evaluated it, or when one waiting for vectors was disabled before they arrived. With more than 100 Subscriptions on one Corpus it can be set once the first 100 have decided. |
| `quarantined_at` | string (date-time) |  | The Version was quarantined. |
| `withdrawn_at` | string (date-time) |  | The Version's Record was withdrawn. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
description: When each processing step of the Version finished, written once by the transaction that commits the step. A step not finished yet is omitted. Versions materialized before step times were recorded carry only accepted_at, and withdrawn_at for a withdrawal after; history is not reconstructed.
properties:
  accepted_at:
    type: string
    format: date-time
    description: Quivr accepted the revision.
  materialized_at:
    type: string
    format: date-time
    description: The Version was published from normalized content.
  segmented_at:
    type: string
    format: date-time
    description: The Version was first cut into segments.
  retrieval_ready_at:
    type: string
    format: date-time
    description: The Version first became searchable.
  enriched_at:
    type: string
    format: date-time
    description: Vectors were first attached to the Version.
  evaluated_at:
    type: string
    format: date-time
    description: Every Subscription asked to evaluate the Version had decided; one that waits for vectors decides on their round. Omitted when no Subscription evaluated it, or when one waiting for vectors was disabled before they arrived. With more than 100 Subscriptions on one Corpus it can be set once the first 100 have decided.
  quarantined_at:
    type: string
    format: date-time
    description: The Version was quarantined.
  withdrawn_at:
    type: string
    format: date-time
    description: The Version's Record was withdrawn.
```

</details>

### `AdminDocument`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `version_id` | string | yes | Minimum length `1`. |
| `record_id` | string | yes | Minimum length `1`. |
| `corpus_id` | string | yes | Minimum length `1`. |
| `source_namespace` | string | yes |  |
| `record_key` | string | yes |  |
| `title` | string |  | The inline title Part of the accepted command, truncated to 200 characters; omitted when there is none. |
| `state` | string | yes | received until the Version is materialized, then its Version Availability state, and withdrawn once its Record is. One of `received`, `materialized`, `building_baseline`, `retrieval_ready`, `quarantined`, `withdrawn`. |
| `is_current` | boolean | yes |  |
| `steps` | [`VersionSteps`](#versionsteps) | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  version_id:
    type: string
    minLength: 1
  record_id:
    type: string
    minLength: 1
  corpus_id:
    type: string
    minLength: 1
  source_namespace:
    type: string
  record_key:
    type: string
  title:
    type: string
    description: The inline title Part of the accepted command, truncated to 200 characters; omitted when there is none.
  state:
    type: string
    enum:
      - received
      - materialized
      - building_baseline
      - retrieval_ready
      - quarantined
      - withdrawn
    description: received until the Version is materialized, then its Version Availability state, and withdrawn once its Record is.
  is_current:
    type: boolean
  steps:
    $ref: '#/components/schemas/VersionSteps'
required:
  - version_id
  - record_id
  - corpus_id
  - source_namespace
  - record_key
  - state
  - is_current
  - steps
```

</details>

### `AdminDocumentPage`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `items` | array of [`AdminDocument`](#admindocument) | yes |  |
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
      $ref: '#/components/schemas/AdminDocument'
  next_page_cursor:
    type: string
    minLength: 1
required:
  - items
```

</details>

### `TimelineStep`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `step` | string | yes | One of `accepted`, `materialized`, `segmented`, `retrieval_ready`, `enriched`, `evaluated`, `quarantined`, `withdrawn`. |
| `at` | string (date-time) | yes |  |
| `since` | string |  | The step the duration is measured from. Each step is timed from the step that causes it; enriched and evaluated both follow retrieval_ready, and quarantined follows the latest step finished before it. Omitted for accepted and withdrawn, and when that step has no time or a later one. |
| `duration_ms` | integer |  | Minimum `0`. |
| `plugin_id` | string |  | The plugin that ran the step, when known. materialized names the normalizer whose output was published; segmented, retrieval_ready and enriched name the ingestion plugin whose segments the Corpus serves. |
| `plugin_version` | string |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  step:
    type: string
    enum:
      - accepted
      - materialized
      - segmented
      - retrieval_ready
      - enriched
      - evaluated
      - quarantined
      - withdrawn
  at:
    type: string
    format: date-time
  since:
    type: string
    description: The step the duration is measured from. Each step is timed from the step that causes it; enriched and evaluated both follow retrieval_ready, and quarantined follows the latest step finished before it. Omitted for accepted and withdrawn, and when that step has no time or a later one.
  duration_ms:
    type: integer
    minimum: 0
  plugin_id:
    type: string
    description: The plugin that ran the step, when known. materialized names the normalizer whose output was published; segmented, retrieval_ready and enriched name the ingestion plugin whose segments the Corpus serves.
  plugin_version:
    type: string
required:
  - step
  - at
```

</details>

### `DocumentTimeline`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `document` | [`AdminDocument`](#admindocument) | yes |  |
| `steps` | array of [`TimelineStep`](#timelinestep) | yes |  |

Example `document_timeline`:

```json
{
  "document": {
    "version_id": "version_7c2e",
    "record_id": "record_41ab",
    "corpus_id": "corpus_9f10",
    "source_namespace": "news",
    "record_key": "article-1042",
    "title": "Harbour reopens after the storm",
    "state": "retrieval_ready",
    "is_current": true,
    "steps": {
      "accepted_at": "2026-09-30T10:00:00Z",
      "materialized_at": "2026-09-30T10:00:01Z",
      "segmented_at": "2026-09-30T10:00:03Z",
      "retrieval_ready_at": "2026-09-30T10:00:04Z",
      "enriched_at": "2026-09-30T10:00:09Z"
    }
  },
  "steps": [
    {
      "step": "accepted",
      "at": "2026-09-30T10:00:00Z"
    },
    {
      "step": "materialized",
      "at": "2026-09-30T10:00:01Z",
      "since": "accepted",
      "duration_ms": 1000
    },
    {
      "step": "segmented",
      "at": "2026-09-30T10:00:03Z",
      "since": "materialized",
      "duration_ms": 2000,
      "plugin_id": "core.ingest",
      "plugin_version": "1.0.0"
    },
    {
      "step": "retrieval_ready",
      "at": "2026-09-30T10:00:04Z",
      "since": "segmented",
      "duration_ms": 1000,
      "plugin_id": "core.ingest",
      "plugin_version": "1.0.0"
    },
    {
      "step": "enriched",
      "at": "2026-09-30T10:00:09Z",
      "since": "retrieval_ready",
      "duration_ms": 5000,
      "plugin_id": "core.ingest",
      "plugin_version": "1.0.0"
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
  document:
    $ref: '#/components/schemas/AdminDocument'
  steps:
    type: array
    items:
      $ref: '#/components/schemas/TimelineStep'
required:
  - document
  - steps
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

Administrative execution only. Retries keep identity. Intentional terminal rerun has a new ID and previous_operation_id. Cancellation does not promise universal rollback; already-terminal state and racing completion may win. projection_rebuild, retrieval_configuration and backfill Operations require corpus_id; when succeeded they require result naming the activated logical generation, or for a backfill the generation it filled. A backfill also carries backfill, and its counters versions_in_scope, versions_done, versions_skipped (with skipped_<reason>) and segments. A quarantine_reprocess carries corpus_id and quarantine_reprocess, and its counters versions_in_scope, versions_recovered, versions_quarantined and versions_skipped (with skipped_<reason>); it has no result. Only a backfill and a quarantine_reprocess can be paused.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `operation_id` | string | yes | Minimum length `1`. |
| `kind` | string | yes | Minimum length `1`. |
| `state` | string | yes | One of `queued`, `running`, `paused`, `succeeded`, `failed`, `cancel_requested`, `canceled`. |
| `progress` | number |  | Approximate fraction, omitted when unknown. Minimum `0`. Maximum `1`. |
| `counters` | map of integer | yes |  |
| `errors` | array of [`Error`](#error) | yes | At most `20` items. |
| `previous_operation_id` | string |  | Minimum length `1`. |
| `corpus_id` | string |  | Minimum length `1`. |
| `result` | [`ProjectionRebuildResult`](#projectionrebuildresult) |  |  |
| `backfill` | [`OperationBackfill`](#operationbackfill) |  |  |
| `quarantine_reprocess` | [`OperationQuarantineReprocess`](#operationquarantinereprocess) |  |  |

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
      - paused
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
  backfill:
    $ref: '#/components/schemas/OperationBackfill'
  quarantine_reprocess:
    $ref: '#/components/schemas/OperationQuarantineReprocess'
required:
  - operation_id
  - kind
  - state
  - counters
  - errors
description: Administrative execution only. Retries keep identity. Intentional terminal rerun has a new ID and previous_operation_id. Cancellation does not promise universal rollback; already-terminal state and racing completion may win. projection_rebuild, retrieval_configuration and backfill Operations require corpus_id; when succeeded they require result naming the activated logical generation, or for a backfill the generation it filled. A backfill also carries backfill, and its counters versions_in_scope, versions_done, versions_skipped (with skipped_<reason>) and segments. A quarantine_reprocess carries corpus_id and quarantine_reprocess, and its counters versions_in_scope, versions_recovered, versions_quarantined and versions_skipped (with skipped_<reason>); it has no result. Only a backfill and a quarantine_reprocess can be paused.
if:
  properties:
    kind:
      enum:
        - projection_rebuild
        - retrieval_configuration
        - backfill
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

Connector kind, provided by a pinned connector plugin; listConnectorKinds lists the kinds this deployment accepts. First-party connector plugins provide rss (RSS 2.0, RSS 1.0, Atom and JSON Feed documents; config url, optional honor_ttl; optional credential username+password or token), x_list (an X list) and m365_mail (Microsoft 365 mailboxes). Another kind is refused with 422 unsupported_connector_kind.

Type: string. Pattern `^[a-z][a-z0-9_]{0,31}$`.

<details>
<summary>Full schema</summary>

```yaml
type: string
pattern: ^[a-z][a-z0-9_]{0,31}$
description: Connector kind, provided by a pinned connector plugin; listConnectorKinds lists the kinds this deployment accepts. First-party connector plugins provide rss (RSS 2.0, RSS 1.0, Atom and JSON Feed documents; config url, optional honor_ttl; optional credential username+password or token), x_list (an X list) and m365_mail (Microsoft 365 mailboxes). Another kind is refused with 422 unsupported_connector_kind.
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
| `interval_seconds` | integer |  | Polling interval. Defaults to the kind's declared interval (rss 300, m365_mail 60, x_list 120); values below the deployment floor (30 s by default) are 422 invalid_interval. Minimum `1`. Maximum `86400`. |

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
    description: Polling interval. Defaults to the kind's declared interval (rss 300, m365_mail 60, x_list 120); values below the deployment floor (30 s by default) are 422 invalid_interval.
```

</details>

### `ConnectorRunRequest`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `connector_id` | string | yes |  |
| `run_at` | string (date-time) | yes | When the requested run is due; now unless the interval floor or the source's Retry-After defers it. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  connector_id:
    type: string
  run_at:
    type: string
    format: date-time
    description: When the requested run is due; now unless the interval floor or the source's Retry-After defers it.
required:
  - connector_id
  - run_at
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

### `PluginRegistration`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `registration_id` | string | yes | Minimum length `1`. |
| `plugin_id` | string | yes | Minimum length `1`. |
| `version` | string | yes | Minimum length `1`. |
| `endpoint` | string | yes | Base URL where the operator runs this plugin version. Minimum length `1`. |
| `manifest_digest` | string | yes | Minimum length `1`. |
| `artifact_digest` | string |  | Artifact digest the plugin reports, recorded as information. Absent when it reports none. Minimum length `1`. |
| `contributions` | array of string | yes |  |
| `roles` | array of string | yes | Roles the manifest declares it can serve, such as normalizer:application/pdf, subscription:<plugin id> or connector:<kind>. The active plan says which it serves. |
| `state` | string | yes | registered while the Contract Runner checks it, then validated or rejected; active while the active plan names it. Once a later plan leaves it out it is draining while pinned_work or subscriptions is above zero, then inactive. One of `registered`, `validated`, `active`, `draining`, `inactive`, `rejected`. |
| `pinned_work` | integer | yes | Unfinished work pinned to a Pipeline Plan that names this registration, such as the processing of a receipt, a connector run or a rebuild. Work finishes on the plan it started on, so a registration a plan change left out keeps being called until this reaches zero. For an alert-rule plugin it also counts the pending evaluations of Subscription Versions that pin its version. Minimum `0`. |
| `subscriptions` | integer | yes | For an alert-rule plugin, the Subscriptions of every Organization that are not deleted, enabled or disabled, whose current Version pins this plugin version. They keep it until migrateSubscriptionEvaluators moves them; 0 for other plugins. Minimum `0`. |
| `check` | [`PluginCheckReport`](#plugincheckreport) |  |  |
| `created_at` | string (date-time) | yes |  |
| `updated_at` | string (date-time) | yes |  |

Example `plugin_registration_checked`:

```json
{
  "registration_id": "plugin_registration_7c2e",
  "plugin_id": "example.embedder",
  "version": "0.2.0",
  "endpoint": "http://127.0.0.1:9960",
  "manifest_digest": "sha256:9d41",
  "contributions": [
    "ingestion"
  ],
  "roles": [
    "ingestion"
  ],
  "state": "rejected",
  "pinned_work": 0,
  "subscriptions": 0,
  "check": {
    "certified": false,
    "checked_at": "2026-09-30T13:00:00Z",
    "passed": 2,
    "failed": 1,
    "skipped": 0,
    "checks": [
      {
        "id": "manifest",
        "title": "quivr-plugin.yaml is valid (quivr plugin inspect)",
        "status": "pass",
        "issues": []
      },
      {
        "id": "health",
        "title": "GET /v0/health answers 200",
        "status": "pass",
        "issues": []
      },
      {
        "id": "discovery",
        "title": "GET /v0/discovery matches quivr-plugin.yaml",
        "status": "fail",
        "issues": [
          {
            "code": "discovery_mismatch",
            "path": "/manifest_digest",
            "message": "discovery serves another manifest digest; the running plugin was built from a different quivr-plugin.yaml"
          }
        ]
      }
    ]
  },
  "created_at": "2026-09-30T13:00:00Z",
  "updated_at": "2026-09-30T13:00:01Z"
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  registration_id:
    type: string
    minLength: 1
  plugin_id:
    type: string
    minLength: 1
  version:
    type: string
    minLength: 1
  endpoint:
    type: string
    minLength: 1
    description: Base URL where the operator runs this plugin version.
  manifest_digest:
    type: string
    minLength: 1
  artifact_digest:
    type: string
    minLength: 1
    description: Artifact digest the plugin reports, recorded as information. Absent when it reports none.
  contributions:
    type: array
    items:
      type: string
  roles:
    type: array
    items:
      type: string
    description: Roles the manifest declares it can serve, such as normalizer:application/pdf, subscription:<plugin id> or connector:<kind>. The active plan says which it serves.
  state:
    type: string
    enum:
      - registered
      - validated
      - active
      - draining
      - inactive
      - rejected
    description: registered while the Contract Runner checks it, then validated or rejected; active while the active plan names it. Once a later plan leaves it out it is draining while pinned_work or subscriptions is above zero, then inactive.
  pinned_work:
    type: integer
    minimum: 0
    description: Unfinished work pinned to a Pipeline Plan that names this registration, such as the processing of a receipt, a connector run or a rebuild. Work finishes on the plan it started on, so a registration a plan change left out keeps being called until this reaches zero. For an alert-rule plugin it also counts the pending evaluations of Subscription Versions that pin its version.
  subscriptions:
    type: integer
    minimum: 0
    description: For an alert-rule plugin, the Subscriptions of every Organization that are not deleted, enabled or disabled, whose current Version pins this plugin version. They keep it until migrateSubscriptionEvaluators moves them; 0 for other plugins.
  check:
    $ref: '#/components/schemas/PluginCheckReport'
  created_at:
    type: string
    format: date-time
  updated_at:
    type: string
    format: date-time
required:
  - registration_id
  - plugin_id
  - version
  - endpoint
  - manifest_digest
  - contributions
  - roles
  - state
  - pinned_work
  - subscriptions
  - created_at
  - updated_at
```

</details>

### `PluginRegistrationRequest`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `idempotency_key` | string | yes | Minimum length `1`. Maximum length `200`. |
| `endpoint` | string | yes | Base URL where the operator runs the plugin, such as http://127.0.0.1:9900. A connector plugin needs https unless it is on loopback. Minimum length `1`. Maximum length `2048`. |
| `manifest` | string | yes | The exact text of the quivr-plugin.yaml the plugin was built from; its sha256 must be the manifest digest the plugin's discovery reports. Minimum length `1`. Maximum length `262144`. |
| `configuration` | object |  | Plugin configuration, validated against the manifest's configuration schema. |
| `routes` | array of object |  | Media types routed to the plugin's normalizer. At most `100` items. |
| `routes[].media_type` | string | yes | Minimum length `1`. |
| `routes[].mode` | string |  | One of `required`, `optional`. |
| `kinds` | array of string |  | Alert kinds offered, a subset of those the manifest declares; absent offers them all. At most `100` items. Each item: Minimum length `1`. |
| `spaces` | map of string |  | Vector spaces of an ingestion plugin by space id, served or evaluation; absent serves the only declared space. |
| `fixtures` | map of string |  | The plugin's own test files, by path inside its fixtures folder (such as events.json or inputs/events.csv), each base64-encoded; at most 4 MiB decoded in total. The check runs them as quivr plugin test runs that folder, so a connector, or a normalizer for a media type without a normative fixture, can be certified. They are stored with the registration; a new idempotency key for a rejected registration checks it again with that request's fixtures. At most `100` properties. |

Example `plugin_registration_request`:

```json
{
  "idempotency_key": "register-embedder-2",
  "endpoint": "http://127.0.0.1:9960",
  "manifest": "id: example.embedder\nversion: 0.2.0\n...",
  "spaces": {
    "example.embedder.small": "served"
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
    maxLength: 200
  endpoint:
    type: string
    minLength: 1
    maxLength: 2048
    description: Base URL where the operator runs the plugin, such as http://127.0.0.1:9900. A connector plugin needs https unless it is on loopback.
  manifest:
    type: string
    minLength: 1
    maxLength: 262144
    description: The exact text of the quivr-plugin.yaml the plugin was built from; its sha256 must be the manifest digest the plugin's discovery reports.
  configuration:
    type: object
    description: Plugin configuration, validated against the manifest's configuration schema.
  routes:
    type: array
    maxItems: 100
    description: Media types routed to the plugin's normalizer.
    items:
      type: object
      additionalProperties: false
      properties:
        media_type:
          type: string
          minLength: 1
        mode:
          type: string
          enum:
            - required
            - optional
      required:
        - media_type
  kinds:
    type: array
    maxItems: 100
    description: Alert kinds offered, a subset of those the manifest declares; absent offers them all.
    items:
      type: string
      minLength: 1
  spaces:
    type: object
    description: Vector spaces of an ingestion plugin by space id, served or evaluation; absent serves the only declared space.
    additionalProperties:
      type: string
      enum:
        - served
        - evaluation
  fixtures:
    type: object
    maxProperties: 100
    description: The plugin's own test files, by path inside its fixtures folder (such as events.json or inputs/events.csv), each base64-encoded; at most 4 MiB decoded in total. The check runs them as quivr plugin test runs that folder, so a connector, or a normalizer for a media type without a normative fixture, can be certified. They are stored with the registration; a new idempotency key for a rejected registration checks it again with that request's fixtures.
    propertyNames:
      minLength: 1
      maxLength: 255
    additionalProperties:
      type: string
      contentEncoding: base64
      maxLength: 5592408
required:
  - idempotency_key
  - endpoint
  - manifest
```

</details>

### `PluginCheckReport`

What the Contract Runner reported on a registration; certified plugins are validated.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `certified` | boolean | yes |  |
| `checked_at` | string (date-time) | yes |  |
| `passed` | integer | yes | Minimum `0`. |
| `failed` | integer | yes | Minimum `0`. |
| `skipped` | integer | yes | Minimum `0`. |
| `checks` | array of [`PluginCheck`](#plugincheck) | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
description: What the Contract Runner reported on a registration; certified plugins are validated.
properties:
  certified:
    type: boolean
  checked_at:
    type: string
    format: date-time
  passed:
    type: integer
    minimum: 0
  failed:
    type: integer
    minimum: 0
  skipped:
    type: integer
    minimum: 0
  checks:
    type: array
    items:
      $ref: '#/components/schemas/PluginCheck'
required:
  - certified
  - checked_at
  - passed
  - failed
  - skipped
  - checks
```

</details>

### `PluginCheck`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string | yes | Minimum length `1`. |
| `title` | string | yes | Minimum length `1`. |
| `contribution` | string |  | Minimum length `1`. |
| `status` | string | yes | One of `pass`, `fail`, `skip`. |
| `fixture` | string |  | The fixture the check ran, such as fixtures/events.json, or a normative fixture prefixed with contracts:. Minimum length `1`. |
| `issues` | array of [`PluginIssue`](#pluginissue) | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  id:
    type: string
    minLength: 1
  title:
    type: string
    minLength: 1
  contribution:
    type: string
    minLength: 1
  status:
    type: string
    enum:
      - pass
      - fail
      - skip
  fixture:
    type: string
    minLength: 1
    description: The fixture the check ran, such as fixtures/events.json, or a normative fixture prefixed with contracts:.
  issues:
    type: array
    items:
      $ref: '#/components/schemas/PluginIssue'
required:
  - id
  - title
  - status
  - issues
```

</details>

### `PluginIssue`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `code` | string | yes | Minimum length `1`. |
| `path` | string |  |  |
| `message` | string | yes | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  code:
    type: string
    minLength: 1
  path:
    type: string
  message:
    type: string
    minLength: 1
required:
  - code
  - message
```

</details>

### `PluginRegistrationList`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `items` | array of [`PluginRegistration`](#pluginregistration) | yes |  |

Example `plugin_registrations`:

```json
{
  "items": [
    {
      "registration_id": "plugin_registration_3f1c",
      "plugin_id": "pdf-text",
      "version": "0.1.0",
      "endpoint": "http://127.0.0.1:9900",
      "manifest_digest": "sha256:5b2e",
      "contributions": [
        "normalizer"
      ],
      "roles": [
        "normalizer:application/pdf"
      ],
      "state": "active",
      "pinned_work": 4,
      "subscriptions": 0,
      "created_at": "2026-09-30T10:00:00Z",
      "updated_at": "2026-09-30T10:00:00Z"
    },
    {
      "registration_id": "plugin_registration_9a7d",
      "plugin_id": "connector.rss",
      "version": "1.0.0",
      "endpoint": "http://127.0.0.1:9920",
      "manifest_digest": "sha256:c41a",
      "artifact_digest": "sha256:0d9e",
      "contributions": [
        "connector"
      ],
      "roles": [
        "connector:rss"
      ],
      "state": "draining",
      "pinned_work": 1,
      "subscriptions": 0,
      "created_at": "2026-09-30T10:00:00Z",
      "updated_at": "2026-09-30T10:00:00Z"
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
      $ref: '#/components/schemas/PluginRegistration'
required:
  - items
```

</details>

### `PipelinePlanRole`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `role` | string | yes | Minimum length `1`. |
| `registration_id` | string | yes | Minimum length `1`. |
| `plugin_id` | string | yes | Minimum length `1`. |
| `version` | string | yes | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  role:
    type: string
    minLength: 1
  registration_id:
    type: string
    minLength: 1
  plugin_id:
    type: string
    minLength: 1
  version:
    type: string
    minLength: 1
required:
  - role
  - registration_id
  - plugin_id
  - version
```

</details>

### `PipelinePlanList`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `items` | array of [`PipelinePlan`](#pipelineplan) | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  items:
    type: array
    items:
      $ref: '#/components/schemas/PipelinePlan'
required:
  - items
```

</details>

### `BackfillRequest`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `idempotency_key` | string | yes | Minimum length `1`. Maximum length `200`. |
| `corpus_id` | string | yes | Minimum length `1`. |
| `accepted_after` | string (date-time) |  | Only Versions Quivr accepted at or after this time; absent, from the first. |
| `accepted_before` | string (date-time) |  | Only Versions Quivr accepted before this time; absent, up to now. |
| `registration_id` | string |  | The ingestion plugin registration to run; absent, the active plan's default. Any ingestion member of the active plan is accepted; a registration outside it is 409 registration_not_active. Minimum length `1`. |
| `spaces` | array of string |  | The vector spaces to fill, which the plugin declares and the deployment serves or evaluates; absent, the deployment's evaluation spaces the plugin owns. At least `1` items. At most `8` items. Items are unique. Each item: Minimum length `1`. |
| `dry_run` | boolean | yes | true reports the estimate and records it; false starts the backfill a dry run with the same key and scope preceded. |
| `confirm_cost` | boolean |  | Accept an estimated cost above the deployment's backfill.max_cost_without_confirmation. Default `false`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  idempotency_key:
    type: string
    minLength: 1
    maxLength: 200
  corpus_id:
    type: string
    minLength: 1
  accepted_after:
    type: string
    format: date-time
    description: Only Versions Quivr accepted at or after this time; absent, from the first.
  accepted_before:
    type: string
    format: date-time
    description: Only Versions Quivr accepted before this time; absent, up to now.
  registration_id:
    type: string
    minLength: 1
    description: The ingestion plugin registration to run; absent, the active plan's default. Any ingestion member of the active plan is accepted; a registration outside it is 409 registration_not_active.
  spaces:
    type: array
    minItems: 1
    maxItems: 8
    uniqueItems: true
    items:
      type: string
      minLength: 1
    description: The vector spaces to fill, which the plugin declares and the deployment serves or evaluates; absent, the deployment's evaluation spaces the plugin owns.
  dry_run:
    type: boolean
    description: true reports the estimate and records it; false starts the backfill a dry run with the same key and scope preceded.
  confirm_cost:
    type: boolean
    default: false
    description: Accept an estimated cost above the deployment's backfill.max_cost_without_confirmation.
required:
  - idempotency_key
  - corpus_id
  - dry_run
```

</details>

### `BackfillEstimate`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `registration_id` | string | yes | Minimum length `1`. |
| `spaces` | array of string | yes | Each item: Minimum length `1`. |
| `versions` | integer | yes | Versions in scope that miss a vector in a target space. Minimum `0`. |
| `segments` | integer | yes | Their segments, which the plugin embeds. Minimum `0`. |
| `input_tokens` | integer | yes | Estimated tokens the plugin embeds, one per four code points of segment text. Minimum `0`. |
| `estimated_seconds` | number (double) | yes | How long the backfill should take, at the deployment's backfill.rate or the recent throughput, whichever is slower. Minimum `0`. |
| `duration_basis` | string | yes | What the duration comes from. One of `rate`, `recent_backfills`, `recent_plugin_calls`. |
| `estimated_cost_usd` | number (double) |  | Estimated cost in US dollars, rounded up to the cent, of the target spaces that declare an input_price in the plugin manifest; a space without one adds nothing. Absent when none declares one, so the cost is unknown. Minimum `0`. |
| `confirmation_required` | boolean | yes | The cost exceeds backfill.max_cost_without_confirmation, so starting the backfill needs confirm_cost. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  registration_id:
    type: string
    minLength: 1
  spaces:
    type: array
    items:
      type: string
      minLength: 1
  versions:
    type: integer
    minimum: 0
    description: Versions in scope that miss a vector in a target space.
  segments:
    type: integer
    minimum: 0
    description: Their segments, which the plugin embeds.
  input_tokens:
    type: integer
    minimum: 0
    description: Estimated tokens the plugin embeds, one per four code points of segment text.
  estimated_seconds:
    type: number
    format: double
    minimum: 0
    description: How long the backfill should take, at the deployment's backfill.rate or the recent throughput, whichever is slower.
  duration_basis:
    type: string
    enum:
      - rate
      - recent_backfills
      - recent_plugin_calls
    description: What the duration comes from.
  estimated_cost_usd:
    type: number
    format: double
    minimum: 0
    description: Estimated cost in US dollars, rounded up to the cent, of the target spaces that declare an input_price in the plugin manifest; a space without one adds nothing. Absent when none declares one, so the cost is unknown.
  confirmation_required:
    type: boolean
    description: The cost exceeds backfill.max_cost_without_confirmation, so starting the backfill needs confirm_cost.
required:
  - registration_id
  - spaces
  - versions
  - segments
  - input_tokens
  - estimated_seconds
  - duration_basis
  - confirmation_required
```

</details>

### `OperationBackfill`

What a backfill fills and how far it got.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `registration_id` | string | yes | Minimum length `1`. |
| `spaces` | array of string | yes | Each item: Minimum length `1`. |
| `accepted_after` | string (date-time) |  |  |
| `accepted_before` | string (date-time) |  |  |
| `plan_id` | string |  | The Pipeline Plan the backfill is pinned to, once it started. Minimum length `1`. |
| `checkpoint` | string |  | The last Version id it finished; it resumes after it, in Version id order. Minimum length `1`. |
| `estimate` | [`BackfillEstimate`](#backfillestimate) | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
description: What a backfill fills and how far it got.
properties:
  registration_id:
    type: string
    minLength: 1
  spaces:
    type: array
    items:
      type: string
      minLength: 1
  accepted_after:
    type: string
    format: date-time
  accepted_before:
    type: string
    format: date-time
  plan_id:
    type: string
    minLength: 1
    description: The Pipeline Plan the backfill is pinned to, once it started.
  checkpoint:
    type: string
    minLength: 1
    description: The last Version id it finished; it resumes after it, in Version id order.
  estimate:
    $ref: '#/components/schemas/BackfillEstimate'
required:
  - registration_id
  - spaces
  - estimate
```

</details>

### `QuarantinedVersion`

A Record Version stuck in quarantine.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `version_id` | string | yes | Minimum length `1`. |
| `record_id` | string | yes | Minimum length `1`. |
| `corpus_id` | string | yes | Minimum length `1`. |
| `receipt_id` | string | yes | Minimum length `1`. |
| `stage` | string | yes | The step it failed at, which a reprocess reruns. normalization - its normalizer failed or its route was removed, and it was published with its submitted input; ingestion - its segmentation through the ingestion plugin was refused or stopped. One of `normalization`, `ingestion`. |
| `reason` | [`Diagnostic`](#diagnostic) | yes |  |
| `quarantined_at` | string (date-time) | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
description: A Record Version stuck in quarantine.
properties:
  version_id:
    type: string
    minLength: 1
  record_id:
    type: string
    minLength: 1
  corpus_id:
    type: string
    minLength: 1
  receipt_id:
    type: string
    minLength: 1
  stage:
    type: string
    enum:
      - normalization
      - ingestion
    description: The step it failed at, which a reprocess reruns. normalization - its normalizer failed or its route was removed, and it was published with its submitted input; ingestion - its segmentation through the ingestion plugin was refused or stopped.
  reason:
    $ref: '#/components/schemas/Diagnostic'
  quarantined_at:
    type: string
    format: date-time
required:
  - version_id
  - record_id
  - corpus_id
  - receipt_id
  - stage
  - reason
  - quarantined_at
```

</details>

### `QuarantinePage`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `items` | array of [`QuarantinedVersion`](#quarantinedversion) | yes |  |
| `next_page_cursor` | string |  | Present while more Versions may follow. Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  items:
    type: array
    items:
      $ref: '#/components/schemas/QuarantinedVersion'
  next_page_cursor:
    type: string
    minLength: 1
    description: Present while more Versions may follow.
required:
  - items
```

</details>

### `QuarantineReprocessRequest`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `idempotency_key` | string | yes | Minimum length `1`. Maximum length `200`. |
| `corpus_id` | string | yes | Minimum length `1`. |
| `plugin` | string |  | Only Versions whose quarantine reason names this plugin id. Minimum length `1`. |
| `code` | string |  | Only Versions quarantined with this reason code. Minimum length `1`. |
| `quarantined_after` | string (date-time) |  | Only Versions quarantined at or after this time. |
| `quarantined_before` | string (date-time) |  | Only Versions quarantined before this time. |
| `from_stage` | string |  | Restart from the stored source Blob through normalization, then ingestion. Omit to retry the step that failed. Inputs that cannot be republished stay quarantined and are skipped. One of `normalization`. |
| `dry_run` | boolean | yes | true reports the count and records it; false starts the reprocess a dry run with the same key and scope preceded. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  idempotency_key:
    type: string
    minLength: 1
    maxLength: 200
  corpus_id:
    type: string
    minLength: 1
  plugin:
    type: string
    minLength: 1
    description: Only Versions whose quarantine reason names this plugin id.
  code:
    type: string
    minLength: 1
    description: Only Versions quarantined with this reason code.
  quarantined_after:
    type: string
    format: date-time
    description: Only Versions quarantined at or after this time.
  quarantined_before:
    type: string
    format: date-time
    description: Only Versions quarantined before this time.
  from_stage:
    type: string
    enum: [normalization]
    x-enum-varnames: [QuarantineReprocessRequestFromStageNormalization]
    description: Restart from the stored source Blob through normalization, then ingestion. Omit to retry the step that failed. Inputs that cannot be republished stay quarantined and are skipped.
  dry_run:
    type: boolean
    description: true reports the count and records it; false starts the reprocess a dry run with the same key and scope preceded.
required:
  - idempotency_key
  - corpus_id
  - dry_run
```

</details>

### `QuarantineReprocessEstimate`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `versions` | integer | yes | Stuck Versions in scope. Minimum `0`. |
| `stages` | object | yes | Of them, how many failed at each step. |
| `stages.normalization` | integer | yes | Minimum `0`. |
| `stages.ingestion` | integer | yes | Minimum `0`. |
| `codes` | map of integer | yes | Of them, how many by reason code. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  versions:
    type: integer
    minimum: 0
    description: Stuck Versions in scope.
  stages:
    type: object
    description: Of them, how many failed at each step.
    properties:
      normalization:
        type: integer
        minimum: 0
      ingestion:
        type: integer
        minimum: 0
    required:
      - normalization
      - ingestion
    additionalProperties: false
  codes:
    type: object
    description: Of them, how many by reason code.
    additionalProperties:
      type: integer
      minimum: 0
required:
  - versions
  - stages
  - codes
```

</details>

### `OperationQuarantineReprocess`

What a quarantine reprocess covers, the plan it runs with and the dry run it followed.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `plugin` | string |  | Minimum length `1`. |
| `code` | string |  | Minimum length `1`. |
| `quarantined_after` | string (date-time) |  |  |
| `quarantined_before` | string (date-time) |  |  |
| `from_stage` | string |  | The requested restart stage; omitted when retrying the failed step. One of `normalization`. |
| `plan_id` | string | yes | The Pipeline Plan it runs with, the one active when it was accepted. Minimum length `1`. |
| `estimate` | [`QuarantineReprocessEstimate`](#quarantinereprocessestimate) | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
description: What a quarantine reprocess covers, the plan it runs with and the dry run it followed.
properties:
  plugin:
    type: string
    minLength: 1
  code:
    type: string
    minLength: 1
  quarantined_after:
    type: string
    format: date-time
  quarantined_before:
    type: string
    format: date-time
  from_stage:
    type: string
    enum: [normalization]
    x-enum-varnames: [OperationQuarantineReprocessFromStageNormalization]
    description: The requested restart stage; omitted when retrying the failed step.
  plan_id:
    type: string
    minLength: 1
    description: The Pipeline Plan it runs with, the one active when it was accepted.
  estimate:
    $ref: '#/components/schemas/QuarantineReprocessEstimate'
required:
  - plan_id
  - estimate
```

</details>

### `VectorSpacePromotionRequest`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `force` | boolean |  | Promote even though some current segments have no vector in the space; those lose their semantic hits until a backfill fills them. Default `false`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  force:
    type: boolean
    default: false
    description: Promote even though some current segments have no vector in the space; those lose their semantic hits until a backfill fills them.
```

</details>

### `VectorSpacePromotion`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `served_space_id` | string | yes | Minimum length `1`. |
| `previous_space_id` | string | yes | The space it replaced, now for evaluation; empty when none was served. |
| `generations_switched` | integer | yes | Generations that now serve the space. Minimum `0`. |
| `corpora_incomplete` | integer | yes | Corpora whose routed generation lacks the space or a vector in it. Minimum `0`. |
| `segments_missing` | integer | yes | Current segments without a vector in the space. Minimum `0`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  served_space_id:
    type: string
    minLength: 1
  previous_space_id:
    type: string
    description: The space it replaced, now for evaluation; empty when none was served.
  generations_switched:
    type: integer
    minimum: 0
    description: Generations that now serve the space.
  corpora_incomplete:
    type: integer
    minimum: 0
    description: Corpora whose routed generation lacks the space or a vector in it.
  segments_missing:
    type: integer
    minimum: 0
    description: Current segments without a vector in the space.
required:
  - served_space_id
  - previous_space_id
  - generations_switched
  - corpora_incomplete
  - segments_missing
```

</details>

### `SubscriptionEvaluatorMigrationRequest`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `plugin_id` | string | yes | The alert-rule plugin, such as alerts. Minimum length `1`. Maximum length `256`. |
| `from_version` | string | yes | The version the Subscriptions to move pin now. Minimum length `1`. Maximum length `256`. |
| `dry_run` | boolean | yes | true validates and lists what would move, without moving anything. |
| `limit` | integer |  | The most Subscriptions this call looks at. Default `100`. Minimum `1`. Maximum `500`. |
| `after` | string |  | Continue after this Subscription id, the next_after of the previous call. Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  plugin_id:
    type: string
    minLength: 1
    maxLength: 256
    description: The alert-rule plugin, such as alerts.
  from_version:
    type: string
    minLength: 1
    maxLength: 256
    description: The version the Subscriptions to move pin now.
  dry_run:
    type: boolean
    description: true validates and lists what would move, without moving anything.
  limit:
    type: integer
    minimum: 1
    maximum: 500
    default: 100
    description: The most Subscriptions this call looks at.
  after:
    type: string
    minLength: 1
    description: Continue after this Subscription id, the next_after of the previous call.
required:
  - plugin_id
  - from_version
  - dry_run
```

</details>

### `SubscriptionEvaluatorMigration`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `plugin_id` | string | yes |  |
| `from_version` | string | yes |  |
| `to_version` | string | yes | The version the active plan serves, which moved Subscriptions pin. |
| `dry_run` | boolean | yes |  |
| `moved` | array of [`SubscriptionEvaluatorMove`](#subscriptionevaluatormove) | yes | The Subscriptions moved, or that would move in a dry run. |
| `refused` | array of [`SubscriptionEvaluatorRefusal`](#subscriptionevaluatorrefusal) | yes | The Subscriptions left on from_version. code is invalid_expression or invalid_subscription_configuration when the served version's schemas refuse them (field and message name the first issue), or subscription_changed when the Subscription got another Version during the call. |
| `next_after` | string |  | Present when more Subscriptions may pin from_version; pass it as after. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  plugin_id:
    type: string
  from_version:
    type: string
  to_version:
    type: string
    description: The version the active plan serves, which moved Subscriptions pin.
  dry_run:
    type: boolean
  moved:
    type: array
    description: The Subscriptions moved, or that would move in a dry run.
    items:
      $ref: '#/components/schemas/SubscriptionEvaluatorMove'
  refused:
    type: array
    description: The Subscriptions left on from_version. code is invalid_expression or invalid_subscription_configuration when the served version's schemas refuse them (field and message name the first issue), or subscription_changed when the Subscription got another Version during the call.
    items:
      $ref: '#/components/schemas/SubscriptionEvaluatorRefusal'
  next_after:
    type: string
    description: Present when more Subscriptions may pin from_version; pass it as after.
required:
  - plugin_id
  - from_version
  - to_version
  - dry_run
  - moved
  - refused
```

</details>

### `SubscriptionEvaluatorMove`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `subscription_id` | string | yes |  |
| `from_version_id` | string | yes | The Subscription Version that pinned from_version. |
| `version_id` | string |  | The new current Subscription Version; absent in a dry run. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  subscription_id:
    type: string
  from_version_id:
    type: string
    description: The Subscription Version that pinned from_version.
  version_id:
    type: string
    description: The new current Subscription Version; absent in a dry run.
required:
  - subscription_id
  - from_version_id
```

</details>

### `SubscriptionEvaluatorRefusal`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `subscription_id` | string | yes |  |
| `version_id` | string | yes | Its current Subscription Version, which keeps from_version. |
| `code` | string | yes |  |
| `field` | string |  |  |
| `message` | string |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  subscription_id:
    type: string
  version_id:
    type: string
    description: Its current Subscription Version, which keeps from_version.
  code:
    type: string
  field:
    type: string
  message:
    type: string
required:
  - subscription_id
  - version_id
  - code
```

</details>

### `PipelinePlanRollbackRequest`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `idempotency_key` | string | yes | Minimum length `1`. Maximum length `200`. |
| `plan_id` | string |  | The plan whose roles become active again; absent returns to the plan the active one replaced (its previous_plan_id). Minimum length `1`. |
| `pinned_work` | string |  | What happens to work pinned to a plan naming a plugin version the rollback takes out. drain lets it finish on that version, which stays draining meanwhile. stop keeps it from calling that version again once each process follows the new plan (within plugin_plan_poll). Its next call fails instead. The processing of a Version then stops with the diagnostic pinned_plan_stopped, with the outcome of pinned_plugin_unavailable, and a rebuild fails with that code. A connector run fails as when its plugin is unavailable, and its next run uses the active plan. One of `drain`, `stop`. Default `drain`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  idempotency_key:
    type: string
    minLength: 1
    maxLength: 200
  plan_id:
    type: string
    minLength: 1
    description: The plan whose roles become active again; absent returns to the plan the active one replaced (its previous_plan_id).
  pinned_work:
    type: string
    enum:
      - drain
      - stop
    default: drain
    description: What happens to work pinned to a plan naming a plugin version the rollback takes out. drain lets it finish on that version, which stays draining meanwhile. stop keeps it from calling that version again once each process follows the new plan (within plugin_plan_poll). Its next call fails instead. The processing of a Version then stops with the diagnostic pinned_plan_stopped, with the outcome of pinned_plugin_unavailable, and a rebuild fails with that code. A connector run fails as when its plugin is unavailable, and its next run uses the active plan.
required:
  - idempotency_key
```

</details>

### `PipelinePlan`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `plan_id` | string | yes | Minimum length `1`. |
| `created_at` | string (date-time) | yes |  |
| `activated_at` | string (date-time) | yes |  |
| `source` | string | yes | What recorded the plan, and so why the plan changed. configuration is the startup configuration, which applies the roles its pins changed since it last applied, even over an earlier activation of the same role. activation is an operator activation. rollback is an operator rollback to an earlier plan's roles. One of `configuration`, `activation`, `rollback`. |
| `previous_plan_id` | string |  | The plan this one replaced; absent for the first plan of the deployment. Minimum length `1`. |
| `roles` | array of [`PipelinePlanRole`](#pipelineplanrole) | yes | One entry per role, sorted by role. |

Example `pipeline_plan`:

```json
{
  "plan_id": "plan_6e0b",
  "created_at": "2026-09-30T10:00:00Z",
  "activated_at": "2026-09-30T10:00:00Z",
  "roles": [
    {
      "role": "connector:rss",
      "registration_id": "plugin_registration_9a7d",
      "plugin_id": "connector.rss",
      "version": "1.0.0"
    },
    {
      "role": "normalizer:application/pdf",
      "registration_id": "plugin_registration_3f1c",
      "plugin_id": "pdf-text",
      "version": "0.1.0"
    }
  ],
  "source": "configuration"
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  plan_id:
    type: string
    minLength: 1
  created_at:
    type: string
    format: date-time
  activated_at:
    type: string
    format: date-time
  source:
    type: string
    enum:
      - configuration
      - activation
      - rollback
    description: What recorded the plan, and so why the plan changed. configuration is the startup configuration, which applies the roles its pins changed since it last applied, even over an earlier activation of the same role. activation is an operator activation. rollback is an operator rollback to an earlier plan's roles.
  previous_plan_id:
    type: string
    minLength: 1
    description: The plan this one replaced; absent for the first plan of the deployment.
  roles:
    type: array
    description: One entry per role, sorted by role.
    items:
      $ref: '#/components/schemas/PipelinePlanRole'
required:
  - plan_id
  - created_at
  - activated_at
  - source
  - roles
```

</details>

### `ActivePluginList`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `plan_activated_at` | string (date-time) |  | When the active plan was activated; absent when no plan is active. |
| `items` | array of [`ActivePlugin`](#activeplugin) | yes | One entry per plugin version in the plan, sorted by plugin id then version. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  plan_activated_at:
    type: string
    format: date-time
    description: When the active plan was activated; absent when no plan is active.
  items:
    type: array
    description: One entry per plugin version in the plan, sorted by plugin id then version.
    items:
      $ref: '#/components/schemas/ActivePlugin'
required:
  - items
```

</details>

### `ActivePlugin`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `plugin_id` | string | yes | Minimum length `1`. |
| `version` | string | yes | Minimum length `1`. |
| `roles` | array of string | yes | The plan's roles this version serves, sorted, for example ingestion, retrieval, normalizer:<media type>, subscription:<plugin id> or connector:<kind>. Each item: Minimum length `1`. |

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
  roles:
    type: array
    description: The plan's roles this version serves, sorted, for example ingestion, retrieval, normalizer:<media type>, subscription:<plugin id> or connector:<kind>.
    items:
      type: string
      minLength: 1
required:
  - plugin_id
  - version
  - roles
```

</details>

### `StatsSummary`

A series over the whole window. Latencies are in milliseconds and present only when count is positive.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `count` | integer | yes | Minimum `0`. |
| `errors` | integer | yes | Minimum `0`. |
| `p50_ms` | number |  | Minimum `0`. |
| `p95_ms` | number |  | Minimum `0`. |
| `mean_ms` | number |  | Minimum `0`. |
| `last_error_code` | string |  | Minimum length `1`. |
| `last_error_at` | string (date-time) |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
description: A series over the whole window. Latencies are in milliseconds and present only when count is positive.
properties:
  count:
    type: integer
    minimum: 0
  errors:
    type: integer
    minimum: 0
  p50_ms:
    type: number
    minimum: 0
  p95_ms:
    type: number
    minimum: 0
  mean_ms:
    type: number
    minimum: 0
  last_error_code:
    type: string
    minLength: 1
  last_error_at:
    type: string
    format: date-time
required:
  - count
  - errors
```

</details>

### `StatsPoint`

One non-empty bucket, starting at start and lasting the list's resolution_seconds.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `start` | string (date-time) | yes |  |
| `count` | integer | yes | Minimum `1`. |
| `errors` | integer | yes | Minimum `0`. |
| `p50_ms` | number | yes | Minimum `0`. |
| `p95_ms` | number | yes | Minimum `0`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
description: One non-empty bucket, starting at start and lasting the list's resolution_seconds.
properties:
  start:
    type: string
    format: date-time
  count:
    type: integer
    minimum: 1
  errors:
    type: integer
    minimum: 0
  p50_ms:
    type: number
    minimum: 0
  p95_ms:
    type: number
    minimum: 0
required:
  - start
  - count
  - errors
  - p50_ms
  - p95_ms
```

</details>

### `PluginCallStats`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `plugin_id` | string | yes | Minimum length `1`. |
| `plugin_version` | string | yes | Minimum length `1`. |
| `operation` | string | yes | One of `normalize`, `segment_and_embed`, `embed_query`, `search_round`, `connector_fetch`, `connector_receive`, `check_credential`, `describe_attachment`, `upload_attachment`, `evaluate_subscription`. |
| `summary` | [`StatsSummary`](#statssummary) | yes |  |
| `points` | array of [`StatsPoint`](#statspoint) | yes | Non-empty buckets, oldest first. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  plugin_id:
    type: string
    minLength: 1
  plugin_version:
    type: string
    minLength: 1
  operation:
    type: string
    enum:
      - normalize
      - segment_and_embed
      - embed_query
      - search_round
      - connector_fetch
      - connector_receive
      - check_credential
      - describe_attachment
      - upload_attachment
      - evaluate_subscription
  summary:
    $ref: '#/components/schemas/StatsSummary'
  points:
    type: array
    description: Non-empty buckets, oldest first.
    items:
      $ref: '#/components/schemas/StatsPoint'
required:
  - plugin_id
  - plugin_version
  - operation
  - summary
  - points
```

</details>

### `SearchStats`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `mode` | string | yes | One of `lexical`, `semantic`, `hybrid`. |
| `profile` | string | yes | Minimum length `1`. |
| `results` | integer | yes | Results returned by these searches in total. Minimum `0`. |
| `over_objective` | integer | yes | Searches that took longer than the profile's latency objective, its max_latency_ms. They still answered. Minimum `0`. |
| `summary` | [`StatsSummary`](#statssummary) | yes |  |
| `points` | array of [`StatsPoint`](#statspoint) | yes | Non-empty buckets, oldest first. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  mode:
    type: string
    enum:
      - lexical
      - semantic
      - hybrid
  profile:
    type: string
    minLength: 1
  results:
    type: integer
    minimum: 0
    description: Results returned by these searches in total.
  over_objective:
    type: integer
    minimum: 0
    description: Searches that took longer than the profile's latency objective, its max_latency_ms. They still answered.
  summary:
    $ref: '#/components/schemas/StatsSummary'
  points:
    type: array
    description: Non-empty buckets, oldest first.
    items:
      $ref: '#/components/schemas/StatsPoint'
required:
  - mode
  - profile
  - results
  - over_objective
  - summary
  - points
```

</details>

### `StepStats`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `step` | string | yes | Minimum length `1`. |
| `summary` | [`StatsSummary`](#statssummary) | yes |  |
| `points` | array of [`StatsPoint`](#statspoint) | yes | Non-empty buckets, oldest first. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  step:
    type: string
    minLength: 1
  summary:
    $ref: '#/components/schemas/StatsSummary'
  points:
    type: array
    description: Non-empty buckets, oldest first.
    items:
      $ref: '#/components/schemas/StatsPoint'
required:
  - step
  - summary
  - points
```

</details>

### `PluginCallStatsList`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `window` | [`StatsWindowName`](#statswindowname) | yes |  |
| `resolution_seconds` | integer | yes | Minimum `1`. |
| `from` | string (date-time) | yes |  |
| `to` | string (date-time) | yes |  |
| `items` | array of [`PluginCallStats`](#plugincallstats) | yes |  |

Example `plugin_call_stats`:

```json
{
  "window": "1h",
  "resolution_seconds": 60,
  "from": "2026-09-30T09:00:00Z",
  "to": "2026-09-30T10:00:12Z",
  "items": [
    {
      "plugin_id": "core.ingest",
      "plugin_version": "1.0.0",
      "operation": "embed_query",
      "summary": {
        "count": 42,
        "errors": 1,
        "p50_ms": 18.5,
        "p95_ms": 61,
        "mean_ms": 24.2,
        "last_error_code": "plugin_unavailable",
        "last_error_at": "2026-09-30T09:41:07Z"
      },
      "points": [
        {
          "start": "2026-09-30T09:41:00Z",
          "count": 12,
          "errors": 1,
          "p50_ms": 17,
          "p95_ms": 48.75
        },
        {
          "start": "2026-09-30T09:59:00Z",
          "count": 30,
          "errors": 0,
          "p50_ms": 19.2,
          "p95_ms": 62.5
        }
      ]
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
  window:
    $ref: '#/components/schemas/StatsWindowName'
  resolution_seconds:
    type: integer
    minimum: 1
  from:
    type: string
    format: date-time
  to:
    type: string
    format: date-time
  items:
    type: array
    items:
      $ref: '#/components/schemas/PluginCallStats'
required:
  - window
  - resolution_seconds
  - from
  - to
  - items
```

</details>

### `SearchStatsList`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `window` | [`StatsWindowName`](#statswindowname) | yes |  |
| `resolution_seconds` | integer | yes | Minimum `1`. |
| `from` | string (date-time) | yes |  |
| `to` | string (date-time) | yes |  |
| `items` | array of [`SearchStats`](#searchstats) | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  window:
    $ref: '#/components/schemas/StatsWindowName'
  resolution_seconds:
    type: integer
    minimum: 1
  from:
    type: string
    format: date-time
  to:
    type: string
    format: date-time
  items:
    type: array
    items:
      $ref: '#/components/schemas/SearchStats'
required:
  - window
  - resolution_seconds
  - from
  - to
  - items
```

</details>

### `StepStatsList`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `window` | [`StatsWindowName`](#statswindowname) | yes |  |
| `resolution_seconds` | integer | yes | Minimum `1`. |
| `from` | string (date-time) | yes |  |
| `to` | string (date-time) | yes |  |
| `items` | array of [`StepStats`](#stepstats) | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  window:
    $ref: '#/components/schemas/StatsWindowName'
  resolution_seconds:
    type: integer
    minimum: 1
  from:
    type: string
    format: date-time
  to:
    type: string
    format: date-time
  items:
    type: array
    items:
      $ref: '#/components/schemas/StepStats'
required:
  - window
  - resolution_seconds
  - from
  - to
  - items
```

</details>

### `CountPoint`

One non-empty bucket, starting at start and lasting the list's resolution_seconds.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `start` | string (date-time) | yes |  |
| `count` | integer | yes | Minimum `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
description: One non-empty bucket, starting at start and lasting the list's resolution_seconds.
properties:
  start:
    type: string
    format: date-time
  count:
    type: integer
    minimum: 1
required:
  - start
  - count
```

</details>

### `ReceivedStats`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `source_namespace` | string | yes | Minimum length `1`. |
| `count` | integer | yes | Minimum `1`. |
| `points` | array of [`CountPoint`](#countpoint) | yes | Non-empty buckets, oldest first. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  source_namespace:
    type: string
    minLength: 1
  count:
    type: integer
    minimum: 1
  points:
    type: array
    description: Non-empty buckets, oldest first.
    items:
      $ref: '#/components/schemas/CountPoint'
required:
  - source_namespace
  - count
  - points
```

</details>

### `ReceivedStatsList`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `window` | [`StatsWindowName`](#statswindowname) | yes |  |
| `resolution_seconds` | integer | yes | Minimum `1`. |
| `from` | string (date-time) | yes |  |
| `to` | string (date-time) | yes |  |
| `total` | integer | yes | Documents received from every source namespace, listed or not. Minimum `0`. |
| `sources` | integer | yes | Source namespaces that received at least one document, listed or not. Minimum `0`. |
| `items` | array of [`ReceivedStats`](#receivedstats) | yes | Most documents first. |

Example `received_stats`:

```json
{
  "window": "24h",
  "resolution_seconds": 900,
  "from": "2026-09-29T10:00:00Z",
  "to": "2026-09-30T10:00:12Z",
  "total": 57,
  "sources": 3,
  "items": [
    {
      "source_namespace": "news-feed",
      "count": 41,
      "points": [
        {
          "start": "2026-09-30T08:15:00Z",
          "count": 17
        },
        {
          "start": "2026-09-30T09:45:00Z",
          "count": 24
        }
      ]
    },
    {
      "source_namespace": "api-uploads",
      "count": 12,
      "points": [
        {
          "start": "2026-09-30T09:00:00Z",
          "count": 12
        }
      ]
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
  window:
    $ref: '#/components/schemas/StatsWindowName'
  resolution_seconds:
    type: integer
    minimum: 1
  from:
    type: string
    format: date-time
  to:
    type: string
    format: date-time
  total:
    type: integer
    minimum: 0
    description: Documents received from every source namespace, listed or not.
  sources:
    type: integer
    minimum: 0
    description: Source namespaces that received at least one document, listed or not.
  items:
    type: array
    description: Most documents first.
    items:
      $ref: '#/components/schemas/ReceivedStats'
required:
  - window
  - resolution_seconds
  - from
  - to
  - total
  - sources
  - items
```

</details>

### `MatchStats`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `evaluator` | string | yes | Plugin id of the evaluator of the Subscriptions that matched. Minimum length `1`. |
| `count` | integer | yes | Minimum `1`. |
| `points` | array of [`CountPoint`](#countpoint) | yes | Non-empty buckets, oldest first. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  evaluator:
    type: string
    minLength: 1
    description: Plugin id of the evaluator of the Subscriptions that matched.
  count:
    type: integer
    minimum: 1
  points:
    type: array
    description: Non-empty buckets, oldest first.
    items:
      $ref: '#/components/schemas/CountPoint'
required:
  - evaluator
  - count
  - points
```

</details>

### `MatchStatsList`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `window` | [`StatsWindowName`](#statswindowname) | yes |  |
| `resolution_seconds` | integer | yes | Minimum `1`. |
| `from` | string (date-time) | yes |  |
| `to` | string (date-time) | yes |  |
| `total` | integer | yes | Matches of every evaluator. Minimum `0`. |
| `items` | array of [`MatchStats`](#matchstats) | yes | Most Matches first. |

Example `match_stats`:

```json
{
  "window": "7d",
  "resolution_seconds": 7200,
  "from": "2026-09-23T10:00:00Z",
  "to": "2026-09-30T10:00:12Z",
  "total": 9,
  "items": [
    {
      "evaluator": "keywords",
      "count": 9,
      "points": [
        {
          "start": "2026-09-29T14:00:00Z",
          "count": 4
        },
        {
          "start": "2026-09-30T08:00:00Z",
          "count": 5
        }
      ]
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
  window:
    $ref: '#/components/schemas/StatsWindowName'
  resolution_seconds:
    type: integer
    minimum: 1
  from:
    type: string
    format: date-time
  to:
    type: string
    format: date-time
  total:
    type: integer
    minimum: 0
    description: Matches of every evaluator.
  items:
    type: array
    description: Most Matches first.
    items:
      $ref: '#/components/schemas/MatchStats'
required:
  - window
  - resolution_seconds
  - from
  - to
  - total
  - items
```

</details>

### `StatsWindowName`

Type: string. One of `1h`, `24h`, `7d`.

<details>
<summary>Full schema</summary>

```yaml
type: string
enum:
  - 1h
  - 24h
  - 7d
```

</details>

### `TopQuery`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `query` | string | yes | Minimum length `1`. Maximum length `200`. |
| `count` | integer | yes | Minimum `1`. |
| `points` | array of [`CountPoint`](#countpoint) | yes | Non-empty hourly buckets, oldest first. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  query:
    type: string
    minLength: 1
    maxLength: 200
  count:
    type: integer
    minimum: 1
  points:
    type: array
    description: Non-empty hourly buckets, oldest first.
    items:
      $ref: '#/components/schemas/CountPoint'
required:
  - query
  - count
  - points
```

</details>

### `TopQueryList`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `window` | [`StatsWindowName`](#statswindowname) | yes |  |
| `resolution_seconds` | integer | yes | Length of a bucket of points; query text is counted per hour. Minimum `1`. |
| `recording` | boolean | yes | Whether this deployment records query text (observability.record_query_text). |
| `items` | array of [`TopQuery`](#topquery) | yes | Most frequent first. |

Example `top_queries`:

```json
{
  "window": "24h",
  "resolution_seconds": 3600,
  "recording": true,
  "items": [
    {
      "query": "solar energy",
      "count": 14,
      "points": [
        {
          "start": "2026-09-30T08:00:00Z",
          "count": 5
        },
        {
          "start": "2026-09-30T09:00:00Z",
          "count": 9
        }
      ]
    },
    {
      "query": "storm warning",
      "count": 6,
      "points": [
        {
          "start": "2026-09-30T09:00:00Z",
          "count": 6
        }
      ]
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
  window:
    $ref: '#/components/schemas/StatsWindowName'
  resolution_seconds:
    type: integer
    minimum: 1
    description: Length of a bucket of points; query text is counted per hour.
  recording:
    type: boolean
    description: Whether this deployment records query text (observability.record_query_text).
  items:
    type: array
    description: Most frequent first.
    items:
      $ref: '#/components/schemas/TopQuery'
required:
  - window
  - resolution_seconds
  - recording
  - items
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

### `ConnectorPushStats`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `connector_id` | string | yes |  |
| `outcome` | string | yes | One of `received`, `refused`. |
| `count` | integer | yes | Minimum `0`. |
| `points` | array of [`CountPoint`](#countpoint) | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  connector_id: {type: string}
  outcome: {type: string, enum: [received, refused]}
  count: {type: integer, minimum: 0}
  points:
    type: array
    items: {$ref: '#/components/schemas/CountPoint'}
required: [connector_id, outcome, count, points]
```

</details>

### `ConnectorPushStatsList`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `window` | [`StatsWindowName`](#statswindowname) | yes |  |
| `resolution_seconds` | integer | yes | Minimum `1`. |
| `from` | string (date-time) | yes |  |
| `to` | string (date-time) | yes |  |
| `total` | integer | yes | Minimum `0`. |
| `items` | array of [`ConnectorPushStats`](#connectorpushstats) | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  window: {$ref: '#/components/schemas/StatsWindowName'}
  resolution_seconds: {type: integer, minimum: 1}
  from: {type: string, format: date-time}
  to: {type: string, format: date-time}
  total: {type: integer, minimum: 0}
  items:
    type: array
    items: {$ref: '#/components/schemas/ConnectorPushStats'}
required: [window, resolution_seconds, from, to, total, items]
```

</details>

### `ConnectorPushPolicy`

Engine-owned protection of declared source API routes, separate from plugin config. Missing fields inherit deployment defaults; configure at instance creation.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `rate_per_second` | number (double) |  | Token refill rate per second (deployment default 10). Minimum `0.001`. Maximum `100000`. |
| `burst` | integer |  | Token bucket capacity (deployment default 100). Minimum `1`. Maximum `100000`. |
| `allowed_cidrs` | array of string |  | Allowed IPv4/IPv6 CIDRs; absent or empty permits every address. At most `256` items. Each item: Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
description: >-
  Engine-owned protection of declared source API routes, separate from plugin config. Missing fields inherit deployment defaults; configure at instance creation.
properties:
  rate_per_second:
    type: number
    format: double
    minimum: 0.001
    maximum: 100000
    description: Token refill rate per second (deployment default 10).
  burst:
    type: integer
    minimum: 1
    maximum: 100000
    description: Token bucket capacity (deployment default 100).
  allowed_cidrs:
    type: array
    maxItems: 256
    items: {type: string, minLength: 1}
    description: Allowed IPv4/IPv6 CIDRs; absent or empty permits every address.
```

</details>

### `ConnectorCreate`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `idempotency_key` | string | yes | Minimum length `1`. |
| `corpus_id` | string | yes | Minimum length `1`. |
| `source_namespace` | string | yes | Minimum length `1`. Maximum length `200`. |
| `kind` | [`ConnectorKind`](#connectorkind) | yes |  |
| `push_policy` | [`ConnectorPushPolicy`](#connectorpushpolicy) |  |  |
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
  "push_policy": {
    "rate_per_second": 2.5,
    "burst": 10,
    "allowed_cidrs": [
      "192.0.2.0/24"
    ]
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
  push_policy:
    $ref: '#/components/schemas/ConnectorPushPolicy'
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

Last committed Connector Health, evaluated at each acquisition run, credential replacement and disable; evaluated_at shows its age. Precedence disabled, access_error, credential_expiring, silent, active. access_error means the source refused access (distinct from silent, which means no new item within the threshold), including a push channel refused access while polling carries the collection. Other failures appear only as last_error.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `state` | string | yes | One of `active`, `silent`, `access_error`, `credential_expiring`, `disabled`. |
| `evaluated_at` | string (date-time) | yes |  |
| `last_success_at` | string (date-time) |  |  |
| `last_item_at` | string (date-time) |  |  |
| `last_error` | [`ConnectorError`](#connectorerror) |  |  |
| `usage` | [`ConnectorUsage`](#connectorusage) |  |  |
| `diagnostics` | object |  | Kind-defined diagnostics from the latest acquisition page, documented on the kind's operator guide page (for x_list, the deletion recheck coverage). Informational; never holds a secret or source content. |
| `push` | [`ConnectorPush`](#connectorpush) |  |  |

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
  push:
    $ref: '#/components/schemas/ConnectorPush'
required:
  - state
  - evaluated_at
description: Last committed Connector Health, evaluated at each acquisition run, credential replacement and disable; evaluated_at shows its age. Precedence disabled, access_error, credential_expiring, silent, active. access_error means the source refused access (distinct from silent, which means no new item within the threshold), including a push channel refused access while polling carries the collection. Other failures appear only as last_error.
```

</details>

### `ConnectorPush`

Push delivery health, present once a kind that declares the push mode reports its push channel.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `state` | string | yes | active, deliveries are expected; pending, the kind has not set its push channel up yet; degraded, the setup failed or deliveries fail or miss items, and polling at the instance's interval carries the collection. One of `active`, `pending`, `degraded`. |
| `error` | [`ConnectorPushError`](#connectorpusherror) |  |  |
| `last_delivery_at` | string (date-time) |  | Last delivery the connector plugin accepted. |
| `poll_interval_seconds` | integer |  | While push is active, polling runs at most this often, as a safety net. Minimum `1`. |

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
      - pending
      - degraded
    description: active, deliveries are expected; pending, the kind has not set its push channel up yet; degraded, the setup failed or deliveries fail or miss items, and polling at the instance's interval carries the collection.
  error:
    $ref: '#/components/schemas/ConnectorPushError'
  last_delivery_at:
    type: string
    format: date-time
    description: Last delivery the connector plugin accepted.
  poll_interval_seconds:
    type: integer
    minimum: 1
    description: While push is active, polling runs at most this often, as a safety net.
required:
  - state
description: Push delivery health, present once a kind that declares the push mode reports its push channel.
```

</details>

### `ConnectorPushError`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `class` | string | yes | One of `access`, `transient`, `source`. |
| `code` | string | yes | For example webhook_invalid (the source invalidated the webhook), plugin_unavailable (a delivery found the plugin down) or missed_deliveries (polling found items no delivery brought). Minimum length `1`. |
| `at` | string (date-time) | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  class:
    type: string
    enum:
      - access
      - transient
      - source
  code:
    type: string
    minLength: 1
    description: For example webhook_invalid (the source invalidated the webhook), plugin_unavailable (a delivery found the plugin down) or missed_deliveries (polling found items no delivery brought).
  at:
    type: string
    format: date-time
required:
  - class
  - code
  - at
```

</details>

### `Connector`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `connector_id` | string | yes | Minimum length `1`. |
| `corpus_id` | string | yes | Minimum length `1`. |
| `source_namespace` | string | yes | Minimum length `1`. |
| `kind` | [`ConnectorKind`](#connectorkind) | yes |  |
| `push_policy` | [`ConnectorPushPolicy`](#connectorpushpolicy) |  |  |
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
| `webhook_url` | string (uri) |  | Public address of the instance's webhook route, present when its kind declares the push mode and the deployment sets public_url. The kind's plugin registers it with the source. |

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
  "push_policy": {
    "rate_per_second": 2.5,
    "burst": 10,
    "allowed_cidrs": [
      "192.0.2.0/24"
    ]
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
  push_policy:
    $ref: '#/components/schemas/ConnectorPushPolicy'
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
  webhook_url:
    type: string
    format: uri
    description: Public address of the instance's webhook route, present when its kind declares the push mode and the deployment sets public_url. The kind's plugin registers it with the source.
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

### `VectorSpaceList`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `projection_generation_id` | string | yes | Minimum length `1`. |
| `segments` | integer | yes | Current segments the generation projects; a space whose coverage equals it holds a vector for every one. Minimum `0`. |
| `items` | array of [`VectorSpace`](#vectorspace) | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  projection_generation_id:
    type: string
    minLength: 1
  segments:
    type: integer
    minimum: 0
    description: Current segments the generation projects; a space whose coverage equals it holds a vector for every one.
  items:
    type: array
    items:
      $ref: '#/components/schemas/VectorSpace'
required:
  - projection_generation_id
  - segments
  - items
```

</details>

### `VectorSpace`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `vector_space_id` | string | yes | The space's identity, as search hits report it. A plugin space is <name>@<version>. Minimum length `1`. |
| `name` | string | yes | Minimum length `1`. |
| `version` | string | yes |  |
| `owner` | object | yes |  |
| `owner.kind` | string | yes | One of `engine`, `plugin`. |
| `owner.plugin_id` | string |  | Minimum length `1`. |
| `owner.plugin_version` | string |  | Minimum length `1`. |
| `model` | string | yes |  |
| `dimensions` | integer | yes | Minimum `0`. |
| `metric` | string | yes | One of `cosine`, `dot`, `l2`. |
| `indexes` | array of string | yes |  |
| `query_modalities` | array of string | yes |  |
| `role` | string | yes | One of `served`, `evaluation`. |
| `coverage` | object | yes |  |
| `coverage.segments` | integer | yes | Minimum `0`. |
| `coverage.total_segments` | integer |  | Current segments in this plugin's own projection; different plugins may cut the same Version differently. Minimum `0`. |
| `coverage.versions_covered` | integer |  | Current eligible Versions whose entire projection has vectors in this space. Minimum `0`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  vector_space_id:
    type: string
    minLength: 1
    description: The space's identity, as search hits report it. A plugin space is <name>@<version>.
  name:
    type: string
    minLength: 1
  version:
    type: string
  owner:
    type: object
    additionalProperties: false
    properties:
      kind:
        type: string
        enum:
          - engine
          - plugin
      plugin_id:
        type: string
        minLength: 1
      plugin_version:
        type: string
        minLength: 1
    required:
      - kind
  model:
    type: string
  dimensions:
    type: integer
    minimum: 0
  metric:
    type: string
    enum:
      - cosine
      - dot
      - l2
  indexes:
    type: array
    items:
      type: string
  query_modalities:
    type: array
    items:
      type: string
  role:
    type: string
    enum:
      - served
      - evaluation
  coverage:
    type: object
    additionalProperties: false
    properties:
      segments:
        type: integer
        minimum: 0
      total_segments:
        type: integer
        minimum: 0
        description: Current segments in this plugin's own projection; different plugins may cut the same Version differently.
      versions_covered:
        type: integer
        minimum: 0
        description: Current eligible Versions whose entire projection has vectors in this space.
    required:
      - segments
required:
  - vector_space_id
  - name
  - version
  - owner
  - model
  - dimensions
  - metric
  - indexes
  - query_modalities
  - role
  - coverage
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

### `RecordCount`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `count` | integer (int64) | yes | Minimum `0`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  count:
    type: integer
    format: int64
    minimum: 0
required:
  - count
```

</details>

### `RecordPage`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `excluded_corpora` | array of [`CorpusExclusion`](#corpusexclusion) |  |  |
| `items` | array of [`Record`](#record) | yes |  |
| `next_page_cursor` | string |  | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  excluded_corpora:
    type: array
    items:
      $ref: '#/components/schemas/CorpusExclusion'
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

### `RenameRequest`

New display name of a Saved Query or Subscription.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `idempotency_key` | string | yes | Minimum length `1`. |
| `name` | string | yes | Minimum length `1`. |

Example `rename`:

```json
{
  "idempotency_key": "rename-1",
  "name": "Storms and hail"
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
required:
  - idempotency_key
  - name
description: New display name of a Saved Query or Subscription.
```

</details>

### `EvaluationBacklogPage`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `items` | array of [`EvaluationCounts`](#evaluationcounts) | yes | At most `500` items. |
| `next_after` | string |  | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required:
  - items
properties:
  items:
    type: array
    maxItems: 500
    items:
      $ref: '#/components/schemas/EvaluationCounts'
  next_after:
    type: string
    minLength: 1
```

</details>

### `EvaluationCounts`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `plugin_id` | string | yes |  |
| `version` | string | yes |  |
| `pending` | integer | yes | Minimum `0`. |
| `erroring` | integer | yes | Minimum `0`. |
| `unavailable` | integer | yes | Minimum `0`. |
| `retired` | integer | yes | Minimum `0`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required:
  - plugin_id
  - version
  - pending
  - erroring
  - unavailable
  - retired
properties:
  plugin_id:
    type: string
  version:
    type: string
  pending:
    type: integer
    minimum: 0
  erroring:
    type: integer
    minimum: 0
  unavailable:
    type: integer
    minimum: 0
  retired:
    type: integer
    minimum: 0
```

</details>

### `EvaluationRetirementRequest`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `key` | string | yes | Minimum length `1`. Maximum length `128`. |
| `plugin_id` | string | yes | Minimum length `1`. Maximum length `128`. |
| `version` | string | yes | Minimum length `1`. Maximum length `128`. |
| `reason` | string | yes | The operator's explanation for deliberately abandoning these evaluations. Minimum length `1`. Maximum length `1024`. |
| `dry_run` | boolean | yes |  |
| `limit` | integer |  | Default `100`. Minimum `1`. Maximum `500`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required:
  - key
  - plugin_id
  - version
  - reason
  - dry_run
properties:
  key:
    type: string
    minLength: 1
    maxLength: 128
  plugin_id:
    type: string
    minLength: 1
    maxLength: 128
  version:
    type: string
    minLength: 1
    maxLength: 128
  reason:
    type: string
    minLength: 1
    maxLength: 1024
    description: The operator's explanation for deliberately abandoning these evaluations.
  dry_run:
    type: boolean
  limit:
    type: integer
    minimum: 1
    maximum: 500
    default: 100
```

</details>

### `EvaluationRetirement`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `key` | string | yes |  |
| `plugin_id` | string | yes |  |
| `version` | string | yes |  |
| `reason` | string | yes |  |
| `dry_run` | boolean | yes |  |
| `limit` | integer | yes | Minimum `1`. Maximum `500`. |
| `retirement_id` | string | yes | Minimum length `1`. |
| `created_at` | string (date-time) |  |  |
| `outcome` | string | yes | One of `evaluator_retired`. |
| `items` | array of [`RetiredEvaluation`](#retiredevaluation) | yes | At most `500` items. |
| `remaining` | integer | yes | Unavailable pending evaluations remaining in this request's scope, including leased ones. Minimum `0`. |
| `leased` | integer | yes | Unavailable pending evaluations skipped because their leases have not expired. Minimum `0`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required:
  - key
  - plugin_id
  - version
  - reason
  - dry_run
  - limit
  - retirement_id
  - outcome
  - items
  - remaining
  - leased
properties:
  key:
    type: string
  plugin_id:
    type: string
  version:
    type: string
  reason:
    type: string
  dry_run:
    type: boolean
  limit:
    type: integer
    minimum: 1
    maximum: 500
  retirement_id:
    type: string
    minLength: 1
  created_at:
    type: string
    format: date-time
  outcome:
    type: string
    enum:
      - evaluator_retired
  items:
    type: array
    maxItems: 500
    items:
      $ref: '#/components/schemas/RetiredEvaluation'
  remaining:
    type: integer
    minimum: 0
    description: Unavailable pending evaluations remaining in this request's scope, including leased ones.
  leased:
    type: integer
    minimum: 0
    description: Unavailable pending evaluations skipped because their leases have not expired.
```

</details>

### `RetiredEvaluation`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `subscription_id` | string | yes |  |
| `subscription_version_id` | string | yes |  |
| `sequence` | integer (int64) | yes | The original trigger's Organization journal position. |
| `corpus_id` | string | yes |  |
| `record_id` | string | yes |  |
| `record_version_id` | string | yes |  |
| `event_id` | string |  | The evaluation.retired event; absent for a dry run. Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required:
  - subscription_id
  - subscription_version_id
  - sequence
  - corpus_id
  - record_id
  - record_version_id
properties:
  subscription_id:
    type: string
  subscription_version_id:
    type: string
  sequence:
    type: integer
    format: int64
    description: The original trigger's Organization journal position.
  corpus_id:
    type: string
  record_id:
    type: string
  record_version_id:
    type: string
  event_id:
    type: string
    minLength: 1
    description: The evaluation.retired event; absent for a dry run.
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
| `retrieval_profile` | string | yes | A search profile the deployment answers (listSearchProfiles), recorded as sent; an unserved profile is 422 unsupported_profile. The profile is checked only when a definition is written, so a Version keeps its profile and keeps evaluating after the profile stops being served. Minimum length `1`. |
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
    description: A search profile the deployment answers (listSearchProfiles), recorded as sent; an unserved profile is 422 unsupported_profile. The profile is checked only when a definition is written, so a Version keeps its profile and keeps evaluating after the profile stops being served.
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
    "retrieval_profile": "default",
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

New immutable definition of an existing Saved Query. The name is unchanged (rename changes it).

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
    "retrieval_profile": "default",
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
description: New immutable definition of an existing Saved Query. The name is unchanged (rename changes it).
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
      "retrieval_profile": "default",
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

Pins an installed evaluator by plugin id and version, and its configuration. Evaluators are the subscription Contributions of the plugins pinned at startup (Plugin Protocol v0). The configuration must satisfy the evaluator's declared configuration schema.

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
description: Pins an installed evaluator by plugin id and version, and its configuration. Evaluators are the subscription Contributions of the plugins pinned at startup (Plugin Protocol v0). The configuration must satisfy the evaluator's declared configuration schema.
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

New immutable configuration of an existing Subscription. The Saved Query and name are unchanged (rename changes the name); saved_query_version_id is the current Version of that Saved Query.

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
description: New immutable configuration of an existing Subscription. The Saved Query and name are unchanged (rename changes the name); saved_query_version_id is the current Version of that Saved Query.
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

Subscription Owner, an opaque end-user reference defined by the client application (for example user-123). Quivr stores, filters and echoes it without interpreting it. At most 128 characters without control characters; none is reserved for the listing filter. A refused owner is 422 invalid_owner, in a creation body as in the listing filter.

Type: string. Minimum length `1`. Maximum length `128`.

<details>
<summary>Full schema</summary>

```yaml
type: string
minLength: 1
maxLength: 128
description: Subscription Owner, an opaque end-user reference defined by the client application (for example user-123). Quivr stores, filters and echoes it without interpreting it. At most 128 characters without control characters; none is reserved for the listing filter. A refused owner is 422 invalid_owner, in a creation body as in the listing filter.
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

### `SubscriptionPreviewRequest`

A proposed Subscription to preview. Give either definition, an inline Saved Query definition, or saved_query_id with saved_query_version_id, an existing Saved Query Version; not both.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `definition` | [`SavedQueryDefinition`](#savedquerydefinition) |  |  |
| `saved_query_id` | string |  | Minimum length `1`. |
| `saved_query_version_id` | string |  | Minimum length `1`. |
| `evaluator` | [`EvaluatorConfig`](#evaluatorconfig) | yes |  |
| `limit` | integer |  | The most Record Versions to judge, newest first. Each one costs an evaluator call. Default `20`. Minimum `1`. Maximum `50`. |
| `accepted_after` | string (date-time) |  | Judge only revisions accepted after this instant. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  definition:
    $ref: '#/components/schemas/SavedQueryDefinition'
  saved_query_id:
    type: string
    minLength: 1
  saved_query_version_id:
    type: string
    minLength: 1
  evaluator:
    $ref: '#/components/schemas/EvaluatorConfig'
  limit:
    type: integer
    minimum: 1
    maximum: 50
    default: 20
    description: The most Record Versions to judge, newest first. Each one costs an evaluator call.
  accepted_after:
    type: string
    format: date-time
    description: Judge only revisions accepted after this instant.
required:
  - evaluator
description: A proposed Subscription to preview. Give either definition, an inline Saved Query definition, or saved_query_id with saved_query_version_id, an existing Saved Query Version; not both.
```

</details>

### `SubscriptionPreviewMatch`

A Record Version the proposed Subscription would have matched, with the evidence a Match would carry. It is not a Match and is not stored.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `corpus_id` | string | yes | Minimum length `1`. |
| `record_id` | string | yes | Minimum length `1`. |
| `record_version_id` | string | yes | Minimum length `1`. |
| `accepted_at` | string (date-time) | yes |  |
| `evidence` | [`MatchEvidence`](#matchevidence) | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  corpus_id:
    type: string
    minLength: 1
  record_id:
    type: string
    minLength: 1
  record_version_id:
    type: string
    minLength: 1
  accepted_at:
    type: string
    format: date-time
  evidence:
    $ref: '#/components/schemas/MatchEvidence'
required:
  - corpus_id
  - record_id
  - record_version_id
  - accepted_at
  - evidence
description: A Record Version the proposed Subscription would have matched, with the evidence a Match would carry. It is not a Match and is not stored.
```

</details>

### `SubscriptionPreview`

What a proposed Subscription would have matched among recent Record Versions.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `evaluated` | integer | yes | Record Versions the evaluator decided. Minimum `0`. |
| `matched` | integer | yes | Minimum `0`. |
| `not_ready` | integer | yes | Record Versions the evaluator could not decide yet (for example before enrichment). Minimum `0`. |
| `complete` | boolean | yes | False when the time budget ran out before every listed Record Version was decided. |
| `oldest_accepted_at` | string (date-time) |  | When the oldest decided Record Version was accepted; absent when none was decided. |
| `matches` | array of [`SubscriptionPreviewMatch`](#subscriptionpreviewmatch) | yes | Most recently accepted first. At most `50` items. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  evaluated:
    type: integer
    minimum: 0
    description: Record Versions the evaluator decided.
  matched:
    type: integer
    minimum: 0
  not_ready:
    type: integer
    minimum: 0
    description: Record Versions the evaluator could not decide yet (for example before enrichment).
  complete:
    type: boolean
    description: False when the time budget ran out before every listed Record Version was decided.
  oldest_accepted_at:
    type: string
    format: date-time
    description: When the oldest decided Record Version was accepted; absent when none was decided.
  matches:
    type: array
    maxItems: 50
    items:
      $ref: '#/components/schemas/SubscriptionPreviewMatch'
    description: Most recently accepted first.
required:
  - evaluated
  - matched
  - not_ready
  - complete
  - matches
description: What a proposed Subscription would have matched among recent Record Versions.
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

### `FacetRequest`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `corpus_ids` | array of string | yes | At least `1` items. At most `16` items. Items are unique. Each item: Minimum length `1`. |
| `fields` | array of [`FacetField`](#facetfield) | yes | Distinct logical metadata field names, in response order. At least `1` items. At most `16` items. |
| `filter` | [`SearchFilter`](#searchfilter) |  |  |
| `accepted_after` | string (date-time) |  | Inclusive current-Version acceptance-time lower bound, as in listing. |
| `accepted_before` | string (date-time) |  | Exclusive current-Version acceptance-time upper bound, as in listing. |

Example `metadata_facets_request`:

```json
{
  "corpus_ids": [
    "corpus_a",
    "corpus_b"
  ],
  "fields": [
    {
      "field": "metadata.language",
      "limit": 10
    },
    {
      "field": "metadata.published_at",
      "interval": "month",
      "limit": 20
    }
  ],
  "filter": {
    "metadata": [
      {
        "field": "metadata.tags",
        "any_of": [
          "harbour"
        ]
      }
    ],
    "source_namespaces": [
      "source"
    ]
  }
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required: [corpus_ids, fields]
properties:
  corpus_ids:
    type: array
    minItems: 1
    maxItems: 16
    uniqueItems: true
    items:
      type: string
      minLength: 1
  fields:
    type: array
    minItems: 1
    maxItems: 16
    items:
      $ref: '#/components/schemas/FacetField'
    description: Distinct logical metadata field names, in response order.
  filter:
    $ref: '#/components/schemas/SearchFilter'
  accepted_after:
    type: string
    format: date-time
    description: Inclusive current-Version acceptance-time lower bound, as in listing.
  accepted_before:
    type: string
    format: date-time
    description: Exclusive current-Version acceptance-time upper bound, as in listing.
```

</details>

### `FacetField`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `field` | string | yes | Pattern `^(metadata\.)?[a-z][a-z0-9_]{0,63}$`. |
| `limit` | integer |  | Default `20`. Minimum `1`. Maximum `100`. |
| `interval` | string |  | Required only for datetime fields; bucket start in UTC. One of `day`, `month`, `year`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required: [field]
properties:
  field:
    type: string
    pattern: '^(metadata\.)?[a-z][a-z0-9_]{0,63}$'
  limit:
    type: integer
    minimum: 1
    maximum: 100
    default: 20
  interval:
    type: string
    enum: [day, month, year]
    description: Required only for datetime fields; bucket start in UTC.
```

</details>

### `FacetResponse`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `items` | array of [`Facet`](#facet) | yes | At most `16` items. |
| `excluded_corpora` | array of [`CorpusExclusion`](#corpusexclusion) |  | At most `16` items. |

Example `typed_metadata_facets`:

```json
{
  "items": [
    {
      "field": "metadata.language",
      "buckets": [
        {
          "value": "en",
          "count": 2
        }
      ]
    },
    {
      "field": "rating",
      "buckets": [
        {
          "value": 16777217,
          "count": 1
        },
        {
          "value": 2.5,
          "count": 1
        }
      ]
    },
    {
      "field": "flag",
      "buckets": [
        {
          "value": false,
          "count": 2
        }
      ]
    },
    {
      "field": "metadata.published_at",
      "buckets": [
        {
          "value": "2026-10-01T00:00:00Z",
          "count": 2
        }
      ]
    },
    {
      "field": "metadata.tags",
      "buckets": []
    }
  ],
  "excluded_corpora": [
    {
      "corpus_id": "corpus_b",
      "fields": [
        "rating"
      ]
    }
  ]
}
```

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required: [items]
properties:
  items:
    type: array
    maxItems: 16
    items:
      $ref: '#/components/schemas/Facet'
  excluded_corpora:
    type: array
    maxItems: 16
    items:
      $ref: '#/components/schemas/CorpusExclusion'
```

</details>

### `Facet`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `field` | string | yes |  |
| `buckets` | array of [`FacetBucket`](#facetbucket) | yes | At most `100` items. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required: [field, buckets]
properties:
  field:
    type: string
  buckets:
    type: array
    maxItems: 100
    items:
      $ref: '#/components/schemas/FacetBucket'
```

</details>

### `FacetBucket`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `value` | one of string, number (double), boolean | yes | Typed scalar value, array member, or RFC 3339 UTC date bucket start. |
| `count` | integer (int64) | yes | Minimum `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required: [value, count]
properties:
  value:
    oneOf:
      - type: string
      - type: number
        format: double
        x-go-type: float64
      - type: boolean
    description: Typed scalar value, array member, or RFC 3339 UTC date bucket start.
  count:
    type: integer
    format: int64
    minimum: 1
```

</details>

### `SearchRequest`

Text-only top-k query. Resolve all Corpora in the authenticated Organization and require read/search permission for every requested Corpus before querying. Never silently drop an unauthorized Corpus. Unknown/unsupported profile or mode returns 422; a dependency outage is an error, not an empty successful result. Query token limits are checked against the resolved profile; no silent truncation. An optional filter narrows candidates inside the engine query, before ranking and the limit, in every mode. Other metadata filters and pagination are outside this surface.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `query` | string | yes | At most 8192 code points on the wire. A semantic or hybrid query is also limited by the owner of the searched vector space (the first-party core.ingest plugin accepts at most 256 tokens of its model's tokenizer); a longer query is refused with 422 query_too_long, whose message names the limit, never truncated. Minimum length `1`. Maximum length `8192`. |
| `corpus_ids` | array of string | yes | At least `1` items. At most `16` items. Items are unique. Each item: Minimum length `1`. |
| `mode` | string |  | One of `lexical`, `semantic`, `hybrid`. Default `hybrid`. |
| `profile` | string |  | A configured short name or a full plugin/profile name this deployment answers (listSearchProfiles). retrieval.profiles maps short names, including default, to full names. An unknown profile returns 422 unsupported_profile. Default `default`. Minimum length `1`. |
| `limit` | integer |  | Default `10`. Minimum `1`. Maximum `50`. |
| `evaluation_plugin` | string |  | Ingestion plugin whose independent projection to measure. Requires evaluation_space; omitted searches follow served routing. Minimum length `1`. |
| `evaluation_space` | string |  | Vector space owned by evaluation_plugin. Both fields are required together; invalid selections return 422 unsupported_search. Minimum length `1`. |
| `filter` | [`SearchFilter`](#searchfilter) |  |  |

Example `text_search`:

```json
{
  "query": "énergie solaire 🌞",
  "corpus_ids": [
    "corpus_news"
  ],
  "mode": "hybrid",
  "profile": "default",
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
    description: At most 8192 code points on the wire. A semantic or hybrid query is also limited by the owner of the searched vector space (the first-party core.ingest plugin accepts at most 256 tokens of its model's tokenizer); a longer query is refused with 422 query_too_long, whose message names the limit, never truncated.
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
    minLength: 1
    default: default
    description: A configured short name or a full plugin/profile name this deployment answers (listSearchProfiles). retrieval.profiles maps short names, including default, to full names. An unknown profile returns 422 unsupported_profile.
  limit:
    type: integer
    minimum: 1
    maximum: 50
    default: 10
  evaluation_plugin:
    type: string
    minLength: 1
    description: Ingestion plugin whose independent projection to measure. Requires evaluation_space; omitted searches follow served routing.
  evaluation_space:
    type: string
    minLength: 1
    description: Vector space owned by evaluation_plugin. Both fields are required together; invalid selections return 422 unsupported_search.
  filter:
    $ref: '#/components/schemas/SearchFilter'
required:
  - query
  - corpus_ids
description: Text-only top-k query. Resolve all Corpora in the authenticated Organization and require read/search permission for every requested Corpus before querying. Never silently drop an unauthorized Corpus. Unknown/unsupported profile or mode returns 422; a dependency outage is an error, not an empty successful result. Query token limits are checked against the resolved profile; no silent truncation. An optional filter narrows candidates inside the engine query, before ranking and the limit, in every mode. Other metadata filters and pagination are outside this surface.
```

</details>

### `SearchFilter`

Candidate filter applied before ranking. Every present condition must hold. A requested Corpus served by a Projection Generation built before source filtering existed returns 422 source_filter_unavailable; rebuild that Corpus once (rebuildCorpusProjection) to enable it. Unfiltered search is unaffected.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `source_namespaces` | array of string |  | Keep only Records whose Source Namespace is one of these values. Ranking and the limit apply within the filtered set, so a source's best matches are returned even when other sources outrank them. At least `1` items. At most `50` items. Items are unique. Each item: Minimum length `1`. Maximum length `200`. |
| `metadata` | array of [`MetadataFilter`](#metadatafilter) |  | ANDed typed filters. Common fields use metadata.language, metadata.published_at, metadata.source_type, metadata.source, metadata.author, metadata.subjects, metadata.tags, metadata.country and metadata.place. Other names require the filter role in the Corpus's effective retrieval mapping. Corpora missing a requested filter field are excluded and reported. A metadata-capable generation is required; rebuild older Corpora first (422 metadata_filter_unavailable). At least `1` items. At most `16` items. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
minProperties: 1
properties:
  source_namespaces:
    type: array
    items:
      type: string
      minLength: 1
      maxLength: 200
    minItems: 1
    maxItems: 50
    uniqueItems: true
    description: Keep only Records whose Source Namespace is one of these values. Ranking and the limit apply within the filtered set, so a source's best matches are returned even when other sources outrank them.
  metadata:
    type: array
    minItems: 1
    maxItems: 16
    items:
      $ref: '#/components/schemas/MetadataFilter'
    description: ANDed typed filters. Common fields use metadata.language, metadata.published_at, metadata.source_type, metadata.source, metadata.author, metadata.subjects, metadata.tags, metadata.country and metadata.place. Other names require the filter role in the Corpus's effective retrieval mapping. Corpora missing a requested filter field are excluded and reported. A metadata-capable generation is required; rebuild older Corpora first (422 metadata_filter_unavailable).
description: Candidate filter applied before ranking. Every present condition must hold. A requested Corpus served by a Projection Generation built before source filtering existed returns 422 source_filter_unavailable; rebuild that Corpus once (rebuildCorpusProjection) to enable it. Unfiltered search is unaffected.
```

</details>

### `MetadataFilter`

Every present condition must hold. Field names are unique within a request; at most 16 predicates. Wrong types and reversed bounds return 422 invalid_query. Missing/mistyped source values do not match. Datetimes are normalized to UTC milliseconds (submillisecond digits are truncated).

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `field` | string | yes | Pattern `^(metadata\.)?[a-z][a-z0-9_]{0,63}$`. |
| `any_of` | array of one of string, number, boolean |  | Exact equality (one value) or any-of equality. String-array fields match any element; values retain their declared type. Datetime equality uses RFC3339 strings. At least `1` items. At most `50` items. |
| `gte` | string (date-time) |  | Inclusive lower datetime bound. Requires a datetime field. |
| `lte` | string (date-time) |  | Inclusive upper datetime bound. Requires a datetime field. |

Further rules (conditional requirements or combinations) are in the full schema below.

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  field:
    type: string
    pattern: '^(metadata\.)?[a-z][a-z0-9_]{0,63}$'
  any_of:
    type: array
    minItems: 1
    maxItems: 50
    items:
      anyOf:
        - type: string
          minLength: 1
          maxLength: 200
        - type: number
          x-go-type: float64
        - type: boolean
    description: Exact equality (one value) or any-of equality. String-array fields match any element; values retain their declared type. Datetime equality uses RFC3339 strings.
  gte:
    type: string
    format: date-time
    description: Inclusive lower datetime bound. Requires a datetime field.
  lte:
    type: string
    format: date-time
    description: Inclusive upper datetime bound. Requires a datetime field.
required: [field]
anyOf:
  - required: [any_of]
  - required: [gte]
  - required: [lte]
description: Every present condition must hold. Field names are unique within a request; at most 16 predicates. Wrong types and reversed bounds return 422 invalid_query. Missing/mistyped source values do not match. Datetimes are normalized to UTC milliseconds (submillisecond digits are truncated).
```

</details>

### `CorpusExclusion`

Authorized Corpus excluded because these logical fields are not declared with the filter role. Common metadata fields are always declared.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `corpus_id` | string | yes |  |
| `fields` | array of string | yes |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  corpus_id:
    type: string
  fields:
    type: array
    items:
      type: string
required: [corpus_id, fields]
description: Authorized Corpus excluded because these logical fields are not declared with the filter role. Common metadata fields are always declared.
```

</details>

### `SearchProfile`

Resolved retrieval profile identity. Name is the requested short or full name (default when the request named none). Version identifies what ranked as plugin:<plugin id>@<version>/<profile>, naming the retrieval plugin, its version and the profile.

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
description: Resolved retrieval profile identity. Name is the requested short or full name (default when the request named none). Version identifies what ranked as plugin:<plugin id>@<version>/<profile>, naming the retrieval plugin, its version and the profile.
```

</details>

### `SearchProfileList`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `items` | array of [`SearchProfileDescription`](#searchprofiledescription) | yes | At least `1` items. |

Example `search_profiles`:

```json
{
  "items": [
    {
      "name": "default",
      "full_name": "example.fusion/default",
      "aliases": [
        "default"
      ],
      "description": "Keywords and vectors fused by rank.",
      "max_latency_ms": 500,
      "max_cost_cents": 0,
      "provider": {
        "kind": "plugin",
        "plugin_id": "example.fusion",
        "plugin_version": "0.1.0"
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
      $ref: '#/components/schemas/SearchProfileDescription'
    minItems: 1
required:
  - items
```

</details>

### `SearchProfileDescription`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | yes | Minimum length `1`. |
| `full_name` | string | yes | Full plugin/profile name, accepted directly by search. Minimum length `1`. |
| `aliases` | array of string | yes | Configured short names that select this profile; empty for an unaliased profile. Items are unique. Each item: Minimum length `1`. |
| `description` | string |  |  |
| `max_latency_ms` | integer |  | Latency objective (p95 target) of one search under this profile. A slower search still answers; the hard bound is four times this value, at least 2 s and at most 9 s. Minimum `1`. |
| `max_cost_cents` | number |  | Most a search may spend on paid calls. Minimum `0`. |
| `provider` | object | yes |  |
| `provider.kind` | string | yes | plugin, the retrieval plugin that answers the profile. engine is no longer returned since the engine's own search moved into core.retrieve. One of `engine`, `plugin`. |
| `provider.plugin_id` | string |  | Minimum length `1`. |
| `provider.plugin_version` | string |  | Minimum length `1`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  name:
    type: string
    minLength: 1
  full_name:
    type: string
    minLength: 1
    description: Full plugin/profile name, accepted directly by search.
  aliases:
    type: array
    items:
      type: string
      minLength: 1
    uniqueItems: true
    description: Configured short names that select this profile; empty for an unaliased profile.
  description:
    type: string
  max_latency_ms:
    type: integer
    minimum: 1
    description: Latency objective (p95 target) of one search under this profile. A slower search still answers; the hard bound is four times this value, at least 2 s and at most 9 s.
  max_cost_cents:
    type: number
    minimum: 0
    description: Most a search may spend on paid calls.
  provider:
    type: object
    additionalProperties: false
    properties:
      kind:
        type: string
        enum:
          - engine
          - plugin
        description: plugin, the retrieval plugin that answers the profile. engine is no longer returned since the engine's own search moved into core.retrieve.
      plugin_id:
        type: string
        minLength: 1
      plugin_version:
        type: string
        minLength: 1
    required:
      - kind
required:
  - name
  - full_name
  - aliases
  - provider
```

</details>

### `SearchUsage`

What a search answered by a retrieval plugin spent; rounds of the plugin, elapsed time, the paid calls and total cost across all profiles, and the time spent in each phase.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `rounds` | integer | yes | Minimum `1`. Maximum `3`. |
| `elapsed_ms` | integer | yes | Minimum `0`. |
| `paid_calls` | integer | yes | Minimum `0`. |
| `cost_cents` | number | yes | Minimum `0`. |
| `profiles` | array of [`SearchProfileUsage`](#searchprofileusage) |  | Each profile invocation, outer first; paid calls and cost belong to that plugin only. At most `65` items. |
| `phases` | [`SearchPhases`](#searchphases) |  |  |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  rounds:
    type: integer
    minimum: 1
    maximum: 3
  elapsed_ms:
    type: integer
    minimum: 0
  paid_calls:
    type: integer
    minimum: 0
  cost_cents:
    type: number
    minimum: 0
  profiles:
    type: array
    maxItems: 65
    items:
      $ref: '#/components/schemas/SearchProfileUsage'
    description: Each profile invocation, outer first; paid calls and cost belong to that plugin only.
  phases:
    $ref: '#/components/schemas/SearchPhases'
required:
  - rounds
  - elapsed_ms
  - paid_calls
  - cost_cents
description: What a search answered by a retrieval plugin spent; rounds of the plugin, elapsed time, the paid calls and total cost across all profiles, and the time spent in each phase.
```

</details>

### `SearchProfileUsage`

Own usage of one profile invocation; aggregate SearchUsage totals include every level.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `profile` | string | yes | Full plugin/profile name. |
| `plugin_version` | string | yes |  |
| `rounds` | integer | yes | Minimum `1`. Maximum `3`. |
| `paid_calls` | integer | yes | Minimum `0`. |
| `cost_cents` | number | yes | Minimum `0`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
required: [profile, plugin_version, rounds, paid_calls, cost_cents]
properties:
  profile:
    type: string
    description: Full plugin/profile name.
  plugin_version:
    type: string
  rounds:
    type: integer
    minimum: 1
    maximum: 3
  paid_calls:
    type: integer
    minimum: 0
  cost_cents:
    type: number
    minimum: 0
description: Own usage of one profile invocation; aggregate SearchUsage totals include every level.
```

</details>

### `SearchPhases`

Milliseconds a search spent in each phase. routing_ms authorizes the request and routes it to projection generations; coverage_ms reads the vector spaces and their coverage; plugin_rounds_ms waits for the retrieval plugin's rounds; query_encoding_ms encodes the query with the plugin that owns each vector space, 0 when no candidate request needs a vector; index_query_ms queries the index; hydration_ms rechecks and reads candidates from canonical storage. The phases do not add up to elapsed_ms, which also counts the engine's own work between them.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `routing_ms` | integer | yes | Minimum `0`. |
| `coverage_ms` | integer | yes | Minimum `0`. |
| `plugin_rounds_ms` | integer | yes | Minimum `0`. |
| `query_encoding_ms` | integer | yes | Minimum `0`. |
| `index_query_ms` | integer | yes | Minimum `0`. |
| `hydration_ms` | integer | yes | Minimum `0`. |

<details>
<summary>Full schema</summary>

```yaml
type: object
additionalProperties: false
properties:
  routing_ms:
    type: integer
    minimum: 0
  coverage_ms:
    type: integer
    minimum: 0
  plugin_rounds_ms:
    type: integer
    minimum: 0
  query_encoding_ms:
    type: integer
    minimum: 0
  index_query_ms:
    type: integer
    minimum: 0
  hydration_ms:
    type: integer
    minimum: 0
required:
  - routing_ms
  - coverage_ms
  - plugin_rounds_ms
  - query_encoding_ms
  - index_query_ms
  - hydration_ms
description: Milliseconds a search spent in each phase. routing_ms authorizes the request and routes it to projection generations; coverage_ms reads the vector spaces and their coverage; plugin_rounds_ms waits for the retrieval plugin's rounds; query_encoding_ms encodes the query with the plugin that owns each vector space, 0 when no candidate request needs a vector; index_query_ms queries the index; hydration_ms rechecks and reads candidates from canonical storage. The phases do not add up to elapsed_ms, which also counts the engine's own work between them.
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
| `explanation` | string |  | Why the retrieval plugin ranked this hit here, when it says so. Minimum length `1`. Maximum length `1024`. |

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
  explanation:
    type: string
    minLength: 1
    maxLength: 1024
    description: Why the retrieval plugin ranked this hit here, when it says so.
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
| `excluded_corpora` | array of [`CorpusExclusion`](#corpusexclusion) |  |  |
| `items` | array of [`SearchHit`](#searchhit) | yes | At most `50` items. |
| `retrieval_profile` | [`SearchProfile`](#searchprofile) | yes |  |
| `usage` | [`SearchUsage`](#searchusage) |  |  |

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
    "name": "default",
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
    "name": "default",
    "version": "quivr.text.fixture.v1"
  }
}
```

Example `plugin_ranked_search_results`:

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
      },
      "explanation": "rank 1 by keywords, rank 2 by the served space"
    }
  ],
  "retrieval_profile": {
    "name": "deep",
    "version": "plugin:example.fusion@0.1.0/deep"
  },
  "usage": {
    "rounds": 2,
    "elapsed_ms": 84,
    "paid_calls": 0,
    "cost_cents": 0,
    "phases": {
      "routing_ms": 2,
      "coverage_ms": 3,
      "plugin_rounds_ms": 9,
      "query_encoding_ms": 21,
      "index_query_ms": 18,
      "hydration_ms": 27
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
  excluded_corpora:
    type: array
    items:
      $ref: '#/components/schemas/CorpusExclusion'
  items:
    type: array
    items:
      $ref: '#/components/schemas/SearchHit'
    maxItems: 50
  retrieval_profile:
    $ref: '#/components/schemas/SearchProfile'
  usage:
    $ref: '#/components/schemas/SearchUsage'
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
