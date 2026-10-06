package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/internal/quarantine"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
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
	return a.encodePage(quarantinePageDomain, p)
}

func (a *API) decodeQuarantinePage(token, binding string) (string, error) {
	var p quarantinePage
	if a.decodePage(quarantinePageDomain, token, &p) != nil || p.Version != 1 {
		return "", errors.New("invalid_cursor")
	}
	if p.Scope != binding {
		return "", errPageScope
	}
	return p.After, nil
}

func (a *API) listQuarantine(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	if a.Quarantine == nil {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	var limit int
	var ok bool
	var binding string
	var f quarantine.Filter
	entries, err := a.Quarantine.List(r.Context(), scope, quarantine.Filter{}, "", 0, func() (quarantine.Filter, string, int, error) {
		q := r.URL.Query()
		for k, v := range q {
			// An empty limit or page cursor gets its own code below, as on every list.
			if len(v) != 1 || (v[0] == "" && k != "limit" && k != "page_cursor") {
				writeError(w, publicerr.InvalidQuery, nil)
				return quarantine.Filter{}, "", 0, errResponseWritten
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
					writeError(w, publicerr.InvalidQuery, nil)
					return quarantine.Filter{}, "", 0, errResponseWritten
				}
				t = t.UTC()
				if k == "quarantined_after" {
					f.After = &t
				} else {
					f.Before = &t
				}
			case "limit", "page_cursor":
			default:
				writeError(w, publicerr.InvalidQuery, nil)
				return quarantine.Filter{}, "", 0, errResponseWritten
			}
		}
		limit, ok = pageLimit(w, q, quarantine.MaxPage, quarantine.MaxPage)
		if !ok {
			return quarantine.Filter{}, "", 0, errResponseWritten
		}
		binding = quarantineBinding(scope, f)
		after := ""
		if q.Has("page_cursor") {
			var err error
			if after, err = a.decodeQuarantinePage(q.Get("page_cursor"), binding); errors.Is(err, errPageScope) {
				writeError(w, publicerr.CursorScopeChanged, nil)
				return quarantine.Filter{}, "", 0, errResponseWritten
			} else if err != nil {
				writeError(w, publicerr.InvalidCursor, nil)
				return quarantine.Filter{}, "", 0, errResponseWritten
			}
		}
		// One more than the page tells whether another follows.

		return f, after, limit + 1, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	switch {
	case err != nil:
		writeError(w, err, publicerr.StorageUnavailable)
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
	FromStage         string     `json:"from_stage"`
	DryRun            bool       `json:"dry_run"`
}

func (a *API) requestReprocess(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	if a.Quarantine == nil {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	var in reprocessRequest
	estimate, op, err := a.Quarantine.Request(r.Context(), scope, quarantine.Request{}, func() (quarantine.Request, error) {
		if !decodeInto(w, r, a.schemas["QuarantineReprocessRequest"], &in) {
			return quarantine.Request{}, errResponseWritten
		}
		f := quarantine.Filter{CorpusID: in.CorpusID, Plugin: in.Plugin, Code: in.Code, After: in.QuarantinedAfter, Before: in.QuarantinedBefore}

		return quarantine.Request{Key: in.IdempotencyKey, Filter: f, DryRun: in.DryRun, FromStage: in.FromStage}, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	switch {
	case err != nil:
		writeError(w, err, publicerr.StorageUnavailable)
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
		FromStage: reprocessFromStage(r.FromStage), PlanId: r.PlanID, Estimate: reprocessEstimateToTransport(r.Estimate)}
}

func reprocessFromStage(stage string) *transport.OperationQuarantineReprocessFromStage {
	if stage == "" {
		return nil
	}
	v := transport.OperationQuarantineReprocessFromStage(stage)
	return &v
}
