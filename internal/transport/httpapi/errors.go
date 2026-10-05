package httpapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/backfill"
	"github.com/The-Vibe-Company/quivr/internal/changes"
	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/The-Vibe-Company/quivr/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

// errorResponse is shared by JSON responses, batch entries and SSE events.
// Only explicitly public, structured diagnostics may change the message.
func errorResponse(err error, fallback *publicerr.Error, corpusID ...string) (int, transport.Error) {
	e := publicerr.Resolve(err, fallback)
	// Internal lifecycle identities remain distinct from public refusals.
	switch {
	case errors.Is(err, connectors.ErrNoWebhook):
		e = publicerr.NotFound
	case errors.Is(err, content.ErrSpaceOwner), errors.Is(err, content.ErrSpaceChanged):
		e = publicerr.PluginConflict
	}
	code := publicerr.ResponseCode(err, e)
	body := transport.Error{Code: code, Message: strings.ReplaceAll(code, "_", " "), Retryable: e.Retryable()}
	if field := publicerr.Field(err); field != "" {
		body.Field = &field
	}
	switch e {
	case publicerr.QueryTooLong:
		if detail := publicerr.Detail(err); detail != "" {
			body.Message = detail
		}
	case publicerr.InvalidExpression, publicerr.InvalidSubscriptionConfiguration:
		if field, message := monitoring.Field(err); field != "" {
			body.Field = &field
			if message != "" {
				body.Message = message
			}
		}
	case publicerr.InvalidPlugin, publicerr.PluginConflict:
		body.Message = pluginErrorMessage(err, body.Message)
	case publicerr.PluginUnreachable:
		body.Message = pluginErrorMessage(err, body.Message)
		var issues *registry.IssueError
		if errors.As(err, &issues) && len(issues.Issues) > 0 {
			const recovery = "; restore the exact build at its endpoint, or activate the current registration of the previous owner listed by GET /v0/admin/plugins"
			body.Message = boundedPublicText(body.Message, maxPublicErrorMessage-len(recovery)) + recovery
		}
	case publicerr.CostConfirmationRequired:
		body.Message = "the estimated cost exceeds backfill.max_cost_without_confirmation; repeat the request with confirm_cost"
	case publicerr.CoverageIncomplete:
		var incomplete *backfill.IncompleteError
		if errors.As(err, &incomplete) {
			p := incomplete.Promotion
			body.Message = fmt.Sprintf("vector space %s lacks a vector for %d current segments in %d Corpora; backfill them, or force the promotion", boundedPublicText(p.Served, 128), p.SegmentsMissing, p.CorporaIncomplete)
		}
	case publicerr.CursorExpired, publicerr.CursorScopeChanged:
		if len(corpusID) > 0 {
			resync := changes.ResyncURL(corpusID[0])
			body.ResyncUrl = &resync
			body.Message += "; resynchronize"
		}
	}
	return int(e.Class()), body
}

// writeError is the single writer for errors owned by the engine HTTP API.
// A route supplies only its unavailable fallback and optional resync Corpus.
func writeError(w http.ResponseWriter, err error, fallback *publicerr.Error, corpusID ...string) {
	status, body := errorResponse(err, fallback, corpusID...)
	if coded, ok := w.(interface{ SetErrorCode(string) }); ok {
		coded.SetErrorCode(body.Code)
	}
	var plain *plainError
	if errors.As(err, &plain) {
		w.Header().Set("Content-Type", plain.contentType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, plain.body)
		return
	}
	send(w, status, body)
}

// plainError retains the established legacy webhook delivery representation.
// Declared connector APIs use the JSON envelope for the same condition.
type plainError struct {
	err               error
	body, contentType string
}

func (e *plainError) Error() string { return e.err.Error() }
func (e *plainError) Unwrap() error { return e.err }
