package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/quarantine"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// WithQuarantine enables GET /v0/admin/quarantine and
// POST /v0/admin/quarantine/reprocess (plugins:admin).
func WithQuarantine(service quarantine.Service) Option {
	return func(a *API) { a.Quarantine = &service }
}

const (
	quarantinePath = "/v0/admin/quarantine"
	reprocessPath  = "/v0/admin/quarantine/reprocess"
)

// quarantineRoutes serves the quarantine listing and the reprocess command.
func (a *API) quarantineRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
	method := "GET"
	switch r.URL.Path {
	case quarantinePath:
	case reprocessPath:
		method = "POST"
	default:
		return false
	}
	switch {
	case r.Method != method:
		failure(w, 405, "method_not_allowed")
	case a.Quarantine == nil:
		failure(w, 404, "not_found")
	case !scope.Allows(operations.BackfillPermission):
		failure(w, 403, "forbidden")
	case method == "GET":
		a.listQuarantine(w, r, scope)
	default:
		a.requestReprocess(w, r, scope)
	}
	return true
}

// quarantinePage is the signed payload of a quarantine page cursor: the
// last Version id, bound to the scope and the filters.
type quarantinePage struct {
	Version int    `json:"v"`
	Scope   string `json:"s"`
	After   string `json:"a"`
}

// quarantineBinding digests the key's scope and the listing's filters.
func quarantineBinding(scope corpus.Scope, f quarantine.Filter) string {
	b, _ := json.Marshal([]any{scopeDigest(scope), f.CorpusID, f.Plugin, f.Code, f.After, f.Before})
	h := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func (a *API) encodeQuarantinePage(p quarantinePage) string {
	b, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(a.signCursor(quarantinePageDomain, b))
}

func (a *API) decodeQuarantinePage(token, binding string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", errors.New("invalid_cursor")
	}
	b, e1 := base64.RawURLEncoding.DecodeString(parts[0])
	sig, e2 := base64.RawURLEncoding.DecodeString(parts[1])
	var p quarantinePage
	if e1 != nil || e2 != nil || !hmac.Equal(sig, a.signCursor(quarantinePageDomain, b)) || json.Unmarshal(b, &p) != nil || p.Version != 1 {
		return "", errors.New("invalid_cursor")
	}
	if p.Scope != binding {
		return "", errPageScope
	}
	return p.After, nil
}

func (a *API) listQuarantine(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	q := r.URL.Query()
	var f quarantine.Filter
	limit := quarantine.MaxPage
	for k, v := range q {
		if len(v) != 1 || v[0] == "" {
			failure(w, 422, "invalid_query")
			return
		}
		switch k {
		case "corpus_id":
			f.CorpusID = v[0]
		case "plugin":
			f.Plugin = v[0]
		case "code":
			f.Code = v[0]
		case "quarantined_after", "quarantined_before":
			t, err := time.Parse(time.RFC3339Nano, v[0])
			if err != nil {
				failure(w, 422, "invalid_query")
				return
			}
			t = t.UTC()
			if k == "quarantined_after" {
				f.After = &t
			} else {
				f.Before = &t
			}
		case "limit":
			n, err := strconv.Atoi(v[0])
			if err != nil || n < 1 || n > quarantine.MaxPage {
				failure(w, 422, "invalid_limit")
				return
			}
			limit = n
		case "page_cursor":
		default:
			failure(w, 422, "invalid_query")
			return
		}
	}
	binding := quarantineBinding(scope, f)
	after := ""
	if q.Has("page_cursor") {
		var err error
		if after, err = a.decodeQuarantinePage(q.Get("page_cursor"), binding); errors.Is(err, errPageScope) {
			failure(w, 409, "cursor_scope_changed")
			return
		} else if err != nil {
			failure(w, 422, "invalid_cursor")
			return
		}
	}
	// One more than the page tells whether another follows.
	entries, err := a.Quarantine.List(r.Context(), scope, f, after, limit+1)
	switch {
	case errors.Is(err, corpus.ErrForbidden):
		failure(w, 403, "forbidden")
		return
	case errors.Is(err, corpus.ErrNotFound):
		failure(w, 404, "not_found")
		return
	case errors.Is(err, quarantine.ErrInvalid):
		e := apiError(422, "invalid_query")
		e.Message = err.Error()
		send(w, 422, e)
		return
	case err != nil:
		failure(w, 503, "storage_unavailable")
		return
	}
	page := transport.QuarantinePage{Items: make([]transport.QuarantinedVersion, 0, min(len(entries), limit))}
	for i, e := range entries {
		if i == limit {
			next := a.encodeQuarantinePage(quarantinePage{Version: 1, Scope: binding, After: entries[limit-1].VersionID})
			page.NextPageCursor = &next
			break
		}
		page.Items = append(page.Items, transport.QuarantinedVersion{VersionId: e.VersionID, RecordId: e.RecordID, CorpusId: e.CorpusID, ReceiptId: e.ReceiptID,
			Stage: transport.QuarantinedVersionStage(e.Stage), Reason: diagnosticsToTransport([]content.Diagnostic{e.Reason})[0], QuarantinedAt: e.QuarantinedAt})
	}
	send(w, 200, page)
}

// reprocessRequest is the reprocess command.
type reprocessRequest struct {
	IdempotencyKey    string     `json:"idempotency_key"`
	CorpusID          string     `json:"corpus_id"`
	Plugin            string     `json:"plugin"`
	Code              string     `json:"code"`
	QuarantinedAfter  *time.Time `json:"quarantined_after"`
	QuarantinedBefore *time.Time `json:"quarantined_before"`
	DryRun            bool       `json:"dry_run"`
}

func (a *API) requestReprocess(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	var in reprocessRequest
	if !decodeInto(w, r, a.reprocessSchema, &in) {
		return
	}
	f := quarantine.Filter{CorpusID: in.CorpusID, Plugin: in.Plugin, Code: in.Code, After: in.QuarantinedAfter, Before: in.QuarantinedBefore}
	estimate, op, err := a.Quarantine.Request(r.Context(), scope, quarantine.Request{Key: in.IdempotencyKey, Filter: f, DryRun: in.DryRun})
	switch {
	case errors.Is(err, corpus.ErrForbidden):
		failure(w, 403, "forbidden")
	case errors.Is(err, corpus.ErrNotFound):
		failure(w, 404, "not_found")
	case errors.Is(err, operations.ErrConflict):
		failure(w, 409, "idempotency_conflict")
	case errors.Is(err, quarantine.ErrDryRunRequired):
		failure(w, 409, quarantine.ErrDryRunRequired.Error())
	case errors.Is(err, quarantine.ErrInProgress):
		failure(w, 409, quarantine.ErrInProgress.Error())
	case errors.Is(err, quarantine.ErrInvalid):
		e := apiError(422, quarantine.ErrInvalid.Error())
		e.Message = err.Error()
		send(w, 422, e)
	case err != nil:
		failure(w, 503, "storage_unavailable")
	case in.DryRun:
		send(w, 200, reprocessEstimateToTransport(estimate))
	default:
		// The Operation and its dispatch intent are committed before this response.
		w.Header().Set("Location", "/v0/operations/"+op.ID)
		send(w, 202, operationToTransport(op))
	}
}

func reprocessEstimateToTransport(e operations.ReprocessEstimate) transport.QuarantineReprocessEstimate {
	out := transport.QuarantineReprocessEstimate{Versions: int(e.Versions), Codes: map[string]int{}}
	out.Stages.Normalization, out.Stages.Ingestion = int(e.Stages["normalization"]), int(e.Stages["ingestion"])
	for code, n := range e.Codes {
		out.Codes[code] = int(n)
	}
	return out
}

func reprocessToTransport(r *operations.Reprocess) *transport.OperationQuarantineReprocess {
	if r == nil {
		return nil
	}
	return &transport.OperationQuarantineReprocess{Plugin: optionalString(r.Plugin), Code: optionalString(r.Code), QuarantinedAfter: r.QuarantinedAfter, QuarantinedBefore: r.QuarantinedBefore,
		PlanId: r.PlanID, Estimate: reprocessEstimateToTransport(r.Estimate)}
}
