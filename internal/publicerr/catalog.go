// Package publicerr owns the public error catalog. Add a code here once;
// domains reference its sentinel and transports use its response class.
package publicerr

// Class is a standard HTTP response class, independent of net/http.
type Class int

const (
	BadRequestClass           Class = 400
	UnauthenticatedClass      Class = 401
	ForbiddenClass            Class = 403
	NotFoundClass             Class = 404
	MethodNotAllowedClass     Class = 405
	ConflictClass             Class = 409
	InternalClass             Class = 500
	GoneClass                 Class = 410
	TooLargeClass             Class = 413
	UnsupportedMediaTypeClass Class = 415
	InvalidClass              Class = 422
	ThrottledClass            Class = 429
	BadGatewayClass           Class = 502
	UnavailableClass          Class = 503
	DeadlineClass             Class = 504
)

var catalog = map[string]*Error{}

func declare(code string, class Class, retryable bool) *Error {
	if _, exists := catalog[code]; exists {
		panic("duplicate public error code: " + code)
	}
	e := &Error{code: code, class: class, retryable: retryable}
	catalog[code] = e
	return e
}

func declareResponse(code string, response *Error) *Error {
	e := declare(code, response.Class(), response.Retryable())
	e.responseCode = response.ResponseCode()
	return e
}

var (
	CorpusArchived                   = declare("corpus_archived", ConflictClass, false)
	InvalidPlugin                    = declare("invalid_plugin", InvalidClass, false)
	SubscriptionChanged              = declare("subscription_changed", ConflictClass, false)
	BackfillInProgress               = declare("backfill_in_progress", ConflictClass, false)
	BatchTooLarge                    = declare("batch_too_large", TooLargeClass, false)
	ChangesUnavailable               = declare("changes_unavailable", UnavailableClass, true)
	DeliveryFailed                   = declare("delivery_failed", InternalClass, false)
	DeliveryUnavailable              = declare("delivery_unavailable", UnavailableClass, true)
	InvalidIdempotencyKey            = declare("invalid_idempotency_key", BadRequestClass, false)
	IPNotAllowed                     = declare("ip_not_allowed", ForbiddenClass, false)
	RateLimited                      = declare("rate_limited", ThrottledClass, true)
	ConnectorDisabled                = declare("connector_disabled", ConflictClass, false)
	ConnectorPaused                  = declare("connector_paused", ConflictClass, false)
	ConnectorsUnavailable            = declare("connectors_unavailable", UnavailableClass, true)
	ContentUnavailable               = declare("content_unavailable", UnavailableClass, true)
	CostConfirmationRequired         = declare("cost_confirmation_required", ConflictClass, false)
	CoverageIncomplete               = declare("coverage_incomplete", ConflictClass, false)
	CredentialsUnavailable           = declare("credentials_unavailable", UnavailableClass, false)
	CursorExpired                    = declare("cursor_expired", GoneClass, false)
	CursorScopeChanged               = declare("cursor_scope_changed", ConflictClass, false)
	DefinitionTooLarge               = declare("definition_too_large", InvalidClass, false)
	DependencyUnavailable            = declare("dependency_unavailable", UnavailableClass, true)
	DryRunRequired                   = declare("dry_run_required", ConflictClass, false)
	EntryTooLarge                    = declare("entry_too_large", TooLargeClass, false)
	EvaluatorError                   = declare("evaluator_error", BadGatewayClass, false)
	EvaluatorUnavailable             = declare("evaluator_unavailable", UnavailableClass, true)
	ExtensionNamespaceOwned          = declare("extension_namespace_owned", InvalidClass, false)
	Forbidden                        = declare("forbidden", ForbiddenClass, false)
	IdempotencyConflict              = declare("idempotency_conflict", ConflictClass, false)
	InvalidApiKey                    = declare("invalid_api_key", UnauthenticatedClass, false)
	InvalidBackfill                  = declare("invalid_backfill", InvalidClass, false)
	InvalidConfig                    = declare("invalid_config", InvalidClass, false)
	InvalidCredential                = declare("invalid_credential", InvalidClass, false)
	InvalidCursor                    = declare("invalid_cursor", InvalidClass, false)
	InvalidExpression                = declare("invalid_expression", InvalidClass, false)
	InvalidInput                     = declare("invalid_input", InvalidClass, false)
	InvalidInterval                  = declare("invalid_interval", InvalidClass, false)
	InvalidInstanceToken             = declare("invalid_instance_token", UnauthenticatedClass, false)
	InvalidJson                      = declare("invalid_json", BadRequestClass, false)
	InvalidLimit                     = declare("invalid_limit", InvalidClass, false)
	InvalidMapping                   = declare("invalid_mapping", InvalidClass, false)
	InvalidMigration                 = declare("invalid_migration", InvalidClass, false)
	InvalidOwner                     = declare("invalid_owner", InvalidClass, false)
	InvalidQuery                     = declare("invalid_query", InvalidClass, false)
	InvalidReprocess                 = declare("invalid_reprocess", InvalidClass, false)
	InvalidSchema                    = declare("invalid_schema", InvalidClass, false)
	InvalidSignature                 = declare("invalid_signature", UnauthenticatedClass, false)
	InvalidSubscriptionConfiguration = declare("invalid_subscription_configuration", InvalidClass, false)
	InvalidWindow                    = declare("invalid_window", InvalidClass, false)
	ItemRejected                     = declare("item_rejected", InvalidClass, false)
	MalformedJson                    = declare("malformed_json", BadRequestClass, false)
	MethodNotAllowed                 = declare("method_not_allowed", MethodNotAllowedClass, false)
	NoPreviousPlan                   = declare("no_previous_plan", ConflictClass, false)
	NotEvaluationSpace               = declare("not_evaluation_space", InvalidClass, false)
	NotFound                         = declare("not_found", NotFoundClass, false)
	OperationNotTerminal             = declare("operation_not_terminal", ConflictClass, false)
	PluginConflict                   = declare("plugin_conflict", ConflictClass, false)
	PluginUnreachable                = declare("plugin_unreachable", ConflictClass, false)
	PushReplayed                     = declare("push_replayed", ConflictClass, false)
	QueryTooLong                     = declare("query_too_long", InvalidClass, false)
	RebuildRequired                  = declare("rebuild_required", ConflictClass, false)
	RegistrationNotActive            = declare("registration_not_active", ConflictClass, false)
	RegistrationNotValidated         = declare("registration_not_validated", ConflictClass, false)
	ReprocessInProgress              = declare("reprocess_in_progress", ConflictClass, false)
	RequestTooLarge                  = declare("request_too_large", TooLargeClass, false)
	ReservedIdempotencyKey           = declare("reserved_idempotency_key", InvalidClass, false)
	RetrievalPluginInvalid           = declare("retrieval_plugin_invalid", BadGatewayClass, false)
	SavedQueryDeleted                = declare("saved_query_deleted", ConflictClass, false)
	SavedQueryInUse                  = declare("saved_query_in_use", ConflictClass, false)
	SearchDeadlineExceeded           = declare("search_deadline_exceeded", DeadlineClass, false)
	PreviewDeadlineExceeded          = declare("preview_deadline_exceeded", DeadlineClass, true)
	SearchUnavailable                = declare("search_unavailable", UnavailableClass, true)
	ModelUnavailable                 = declare("model_unavailable", UnavailableClass, true)
	MetadataFilterUnavailable        = declare("metadata_filter_unavailable", InvalidClass, false)
	FilterTooBroad                   = declare("filter_too_broad", InvalidClass, false)
	SourceFilterUnavailable          = declare("source_filter_unavailable", InvalidClass, false)
	SourceNamespaceInUse             = declare("source_namespace_in_use", ConflictClass, false)
	StorageUnavailable               = declare("storage_unavailable", UnavailableClass, true)
	SubscriptionDeleted              = declare("subscription_deleted", ConflictClass, false)
	TokenInactive                    = declare("token_inactive", ConflictClass, false)
	TokensUnavailable                = declareResponse("tokens_unavailable", ConnectorsUnavailable)
	UnknownDestination               = declare("unknown_destination", InvalidClass, false)
	UnknownSavedQuery                = declare("unknown_saved_query", InvalidClass, false)
	UnsupportedConnectorKind         = declare("unsupported_connector_kind", InvalidClass, false)
	UnsupportedContent               = declare("unsupported_content", InvalidClass, false)
	UnsupportedEvaluator             = declare("unsupported_evaluator", InvalidClass, false)
	UnsupportedMediaType             = declare("unsupported_media_type", UnsupportedMediaTypeClass, false)
	UnsupportedOperationKind         = declare("unsupported_operation_kind", InvalidClass, false)
	UnsupportedProfile               = declare("unsupported_profile", InvalidClass, false)
	UnsupportedSearch                = declare("unsupported_search", InvalidClass, false)
	UnverifiedBlob                   = declare("unverified_blob", InvalidClass, false)
)
