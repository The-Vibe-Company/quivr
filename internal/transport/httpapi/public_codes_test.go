package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
)

// detailed returns err as a domain would report it with an explanation, in
// both supported wrapping styles.
func detailed(err error) map[string]error {
	return map[string]error{
		"bare":        err,
		"fmt wrap":    fmt.Errorf("%w: duplicate Part key %q", err, "a"),
		"with detail": publicerr.WithDetail(err, "attachment exceeds %d bytes", 10),
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
	for sentinel, want := range map[error]string{
		content.ErrInvalid:        "invalid_input",
		content.ErrUnsupported:    "unsupported_content",
		content.ErrConflict:       "idempotency_conflict",
		content.ErrExtensionOwned: "extension_namespace_owned",
		&content.ManifestViolation{Kind: content.ErrInvalid, Detail: `duplicate Part key "a"`}: "invalid_input",
	} {
		for style, err := range detailed(sentinel) {
			if _, code := contentFailure(err); code != want {
				t.Errorf("%v (%s): code %q, want %q", sentinel, style, code, want)
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
	} {
		for style, err := range detailed(sentinel) {
			status, code := written(t, func(w *httptest.ResponseRecorder) { connectorFailure(w, err) })
			if status != want.status || code != want.code {
				t.Errorf("%v (%s): %d %q, want %d %q", sentinel, style, status, code, want.status, want.code)
			}
		}
	}
}

func TestMonitoringFailureCodesIgnoreDetail(t *testing.T) {
	for sentinel, want := range map[error]string{
		monitoring.ErrUnsupportedProfile:   "unsupported_profile",
		monitoring.ErrUnsupportedEvaluator: "unsupported_evaluator",
		monitoring.ErrUnknownDestination:   "unknown_destination",
		monitoring.ErrUnknownSavedQuery:    "unknown_saved_query",
		monitoring.ErrTooLarge:             "definition_too_large",
	} {
		for style, err := range detailed(sentinel) {
			status, code := written(t, func(w *httptest.ResponseRecorder) { monitoringFailure(w, err) })
			if status != 422 || code != want {
				t.Errorf("%v (%s): %d %q, want 422 %q", sentinel, style, status, code, want)
			}
		}
	}
}

// Corpus creation and retrieval reconfiguration share this resolution.
func TestRetrievalFailureCodesIgnoreDetail(t *testing.T) {
	for sentinel, want := range map[error]string{
		corpus.ErrInvalidMapping:     "invalid_mapping",
		corpus.ErrUnsupportedProfile: "unsupported_profile",
	} {
		for style, err := range detailed(sentinel) {
			if code := publicCode(err, "invalid_mapping"); code != want {
				t.Errorf("%v (%s): code %q, want %q", sentinel, style, code, want)
			}
		}
	}
}

func TestUncodedErrorsUseTheExplicitFallback(t *testing.T) {
	if code := publicCode(fmt.Errorf("invalid_input: raw text"), "invalid_mapping"); code != "invalid_mapping" {
		t.Fatalf("code %q, want the fallback", code)
	}
}
