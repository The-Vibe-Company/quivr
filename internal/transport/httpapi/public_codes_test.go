package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

// detailed returns err as a domain would report it with an explanation, in
// both supported wrapping styles.
func detailed(err error) map[string]error {
	return map[string]error{
		"bare":        err,
		"fmt wrap":    fmt.Errorf("%w: %s", err, internalErrorMarker),
		"with detail": publicerr.WithDetail(err, internalErrorMarker),
	}
}

func written(t *testing.T, write func(*httptest.ResponseRecorder)) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	write(rec)
	var body struct{ Code string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode failure body %q: %v", rec.Body.String(), err)
	}
	return rec.Code, body.Code
}

// Adding detail to a domain error must never change its public code.
func TestContentFailureCodesIgnoreDetail(t *testing.T) {
	type public struct {
		status int
		code   string
	}
	for sentinel, want := range map[error]public{
		content.ErrInvalid:        {422, "invalid_input"},
		content.ErrUnsupported:    {422, "unsupported_content"},
		content.ErrConflict:       {409, "idempotency_conflict"},
		content.ErrExtensionOwned: {422, "extension_namespace_owned"},
		&content.ManifestViolation{Kind: content.ErrInvalid, Detail: `duplicate Part key "a"`}:    {422, "invalid_input"},
		&content.ManifestViolation{Kind: content.ErrUnsupported, Detail: `Part "a" kind "video"`}: {422, "unsupported_content"},
	} {
		for style, err := range detailed(sentinel) {
			if status, code := written(t, func(w *httptest.ResponseRecorder) { writeError(w, err, publicerr.ContentUnavailable) }); status != want.status || code != want.code {
				t.Errorf("%v (%s): %d %q, want %d %q", sentinel, style, status, code, want.status, want.code)
			}
		}
	}
}

func TestConnectorFailureCodesIgnoreDetail(t *testing.T) {
	for sentinel, want := range map[error]struct {
		status int
		code   string
	}{
		connectors.ErrConflict:          {409, "idempotency_conflict"},
		connectors.ErrNamespaceInUse:    {409, "source_namespace_in_use"},
		connectors.ErrDisabled:          {409, "connector_disabled"},
		connectors.ErrUnsupportedKind:   {422, "unsupported_connector_kind"},
		connectors.ErrInvalidConfig:     {422, "invalid_config"},
		connectors.ErrInvalidCredential: {422, "invalid_credential"},
		connectors.ErrInvalidInterval:   {422, "invalid_interval"},
		connectors.ErrInvalid:           {422, "invalid_input"},
		// Provider codes can match engine codes; delivery owns their class.
		(connectors.RelayAnswer{Status: 500, ErrorCode: "invalid_input"}).PublicError(): {500, "invalid_input"},
		(connectors.RelayAnswer{Status: 503, ErrorCode: "invalid_input"}).PublicError(): {503, "invalid_input"},
		// A deployment without credential_key; retrying cannot help.
		connectors.ErrCredentialsUnavailable: {503, "credentials_unavailable"},
	} {
		for style, err := range detailed(sentinel) {
			status, code := written(t, func(w *httptest.ResponseRecorder) { writeError(w, err, publicerr.ConnectorsUnavailable) })
			if status != want.status || code != want.code {
				t.Errorf("%v (%s): %d %q, want %d %q", sentinel, style, status, code, want.status, want.code)
			}
		}
	}
	rec := httptest.NewRecorder()
	writeError(rec, connectors.ErrCredentialsUnavailable, publicerr.ConnectorsUnavailable)
	var body struct{ Retryable *bool }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Retryable == nil || *body.Retryable {
		t.Fatalf("credentials_unavailable must not be retryable: %s", rec.Body.String())
	}
}

// monitoringFailure owns every monitoring refusal's status, code and
// retryability. A preview's evaluator failure always carries the plugin's
// detail; only an unreachable or transient evaluator is worth retrying.
func TestMonitoringFailureCodesIgnoreDetail(t *testing.T) {
	type public struct {
		status    int
		code      string
		retryable bool
	}
	for sentinel, want := range map[error]public{
		monitoring.ErrForbidden:                     {403, "forbidden", false},
		monitoring.ErrNotFound:                      {404, "not_found", false},
		monitoring.ErrConflict:                      {409, "idempotency_conflict", false},
		monitoring.ErrSubscriptionDeleted:           {409, "subscription_deleted", false},
		monitoring.ErrSavedQueryDeleted:             {409, "saved_query_deleted", false},
		monitoring.ErrSavedQueryInUse:               {409, "saved_query_in_use", false},
		monitoring.ErrUnsupportedProfile:            {422, "unsupported_profile", false},
		monitoring.ErrUnsupportedEvaluator:          {422, "unsupported_evaluator", false},
		monitoring.ErrUnknownDestination:            {422, "unknown_destination", false},
		monitoring.ErrUnknownSavedQuery:             {422, "unknown_saved_query", false},
		monitoring.ErrTooLarge:                      {422, "definition_too_large", false},
		monitoring.ErrInvalidOwner:                  {422, "invalid_owner", false},
		monitoring.ErrInvalidMigration:              {422, "invalid_migration", false},
		monitoring.ErrInvalidExpression:             {422, "invalid_expression", false},
		monitoring.ErrInvalidEvaluatorConfiguration: {422, "invalid_subscription_configuration", false},
		monitoring.ErrPreviewUnavailable:            {503, "evaluator_unavailable", true},
		monitoring.ErrPreviewFailed:                 {502, "evaluator_error", false},
		errors.New("connection refused"):            {503, "storage_unavailable", true},
	} {
		for style, err := range detailed(sentinel) {
			rec := httptest.NewRecorder()
			writeError(rec, err, publicerr.StorageUnavailable)
			var body struct {
				Code, Message string
				Retryable     *bool
			}
			if json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Retryable == nil {
				t.Fatalf("%v (%s): undecodable failure %q", sentinel, style, rec.Body.String())
			}
			if rec.Code != want.status || body.Code != want.code || *body.Retryable != want.retryable {
				t.Errorf("%v (%s): %d %q retryable=%v, want %+v", sentinel, style, rec.Code, body.Code, *body.Retryable, want)
			}
			assertPublicMessage(t, rec, want.code)
		}
	}
}

// A schema refusal of a pinned expression or configuration names the request
// member and the first schema issue.
func TestMonitoringSchemaRefusalNamesTheField(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, monitoring.Invalid(monitoring.ErrInvalidExpression, "/saved_query_version_id", "/expression/text: minLength: got 0, want 1"), publicerr.StorageUnavailable)
	var body struct {
		Code, Message, Field string
		Retryable            bool
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 422 || body.Code != "invalid_expression" || body.Field != "/saved_query_version_id" || body.Message != "/expression/text: minLength: got 0, want 1" || body.Retryable {
		t.Fatalf("%d %+v", rec.Code, body)
	}
}

// A refused upload answers the public code of its sentinel; the request schema
// alone answers invalid_schema.
func TestUploadFailureCodesIgnoreDetail(t *testing.T) {
	for sentinel, want := range map[error]struct {
		status int
		code   string
	}{
		uploads.ErrInvalid:  {422, "invalid_input"},
		content.ErrInvalid:  {422, "invalid_input"},
		uploads.ErrNotFound: {404, "not_found"},
		uploads.ErrConflict: {409, "idempotency_conflict"},
	} {
		for style, err := range detailed(sentinel) {
			status, code := written(t, func(w *httptest.ResponseRecorder) { writeError(w, err, publicerr.StorageUnavailable) })
			if status != want.status || code != want.code {
				t.Errorf("%v (%s): %d %q, want %d %q", sentinel, style, status, code, want.status, want.code)
			}
		}
	}
}

func TestUncodedErrorsUseTheExplicitFallback(t *testing.T) {
	_, body := errorResponse(fmt.Errorf("invalid_input: raw text"), publicerr.InvalidMapping)
	if code := body.Code; code != "invalid_mapping" {
		t.Fatalf("code %q, want the fallback", code)
	}
}

// A retrieval plugin's broken answer and a search over its profile's deadline
// keep their public codes, whatever detail the engine adds.
func TestSearchFailureCodesIgnoreDetail(t *testing.T) {
	for sentinel, want := range map[error]struct {
		status    int
		code      string
		message   string
		retryable bool
	}{
		retrieval.ErrUnsupportedProfile: {422, "unsupported_profile", "unsupported profile", false},
		retrieval.ErrUnsupported:        {422, "unsupported_search", "unsupported search", false},
		retrieval.ErrQueryTooLong:       {422, "query_too_long", "query too long", false},
		retrieval.ErrPluginInvalid:      {502, "retrieval_plugin_invalid", "retrieval plugin invalid", false},
		retrieval.ErrDeadline:           {504, "search_deadline_exceeded", "search deadline exceeded", false},
		retrieval.ErrUnavailable:        {503, "search_unavailable", "search unavailable", true},
	} {
		const detail = "query exceeds 256 tokens, the limit of profile default"
		for style, err := range map[string]error{
			"bare":        sentinel,
			"fmt wrap":    fmt.Errorf("%w: %s", sentinel, detail),
			"with detail": publicerr.WithDetail(sentinel, detail),
		} {
			t.Run(want.code+"/"+style, func(t *testing.T) {
				message := want.message
				if sentinel == retrieval.ErrQueryTooLong && style == "with detail" {
					message = detail
				}
				status, body := errorResponse(err, publicerr.SearchUnavailable)
				if status != want.status || body.Code != want.code || body.Message != message || body.Retryable != want.retryable {
					t.Fatalf("%d %+v, want %d %s message=%q retryable=%v", status, body, want.status, want.code, message, want.retryable)
				}
			})
		}
	}
}

// The shared writer owns every public plugin registry refusal response,
// including wrapped errors and structured issue reports.
func TestPluginFailureCodesIgnoreDetail(t *testing.T) {
	for sentinel, want := range map[error]struct {
		status int
		code   string
	}{
		corpus.ErrForbidden:              {403, "forbidden"},
		registry.ErrNotFound:             {404, "not_found"},
		registry.ErrIdempotencyConflict:  {409, "idempotency_conflict"},
		registry.ErrNotValidated:         {409, "registration_not_validated"},
		registry.ErrConflict:             {409, "plugin_conflict"},
		registry.ErrInvalid:              {422, "invalid_plugin"},
		registry.ErrUnreachable:          {409, "plugin_unreachable"},
		registry.ErrNoPreviousPlan:       {409, "no_previous_plan"},
		content.ErrSpaceChanged:          {409, "plugin_conflict"},
		content.ErrSpaceOwner:            {409, "plugin_conflict"},
		errors.New("connection refused"): {503, "storage_unavailable"},
	} {
		for style, err := range detailed(sentinel) {
			rec := httptest.NewRecorder()
			writeError(rec, err, publicerr.StorageUnavailable)
			if rec.Code != want.status {
				t.Errorf("%v (%s): status %d, want %d", sentinel, style, rec.Code, want.status)
			}
			assertPublicMessage(t, rec, want.code)
		}
	}
	rec := httptest.NewRecorder()
	writeError(rec, &registry.IssueError{Kind: registry.ErrConflict, Issues: []plugins.Issue{{Code: plugins.CodeKindConflict, Path: "/plugins/1/manifest", Message: internalErrorMarker}}}, publicerr.StorageUnavailable)
	if rec.Code != 409 {
		t.Fatalf("conflict status %d, want 409", rec.Code)
	}
	assertPublicMessage(t, rec, "plugin_conflict", "plugin conflict; kind_conflict /plugins/1/manifest")
}
