package httpapi

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// WithActivity enables the operator reads of document activity.
func WithActivity(activities content.Activities) Option {
	return func(a *API) { a.Activity = activities }
}

// documentPage is the signed payload of an admin documents page cursor: the
// keyset position, bound to the authorization scope.
type documentPage struct {
	Version    int       `json:"v"`
	Scope      string    `json:"s"`
	AcceptedAt time.Time `json:"t"`
	After      string    `json:"a"`
}

func (a *API) encodeDocumentPage(p documentPage) string {
	b, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(a.signCursor(documentPageDomain, b))
}

func (a *API) decodeDocumentPage(token string, s corpus.Scope) (*content.ActivityCursor, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, errors.New("invalid_cursor")
	}
	b, e1 := base64.RawURLEncoding.DecodeString(parts[0])
	sig, e2 := base64.RawURLEncoding.DecodeString(parts[1])
	var p documentPage
	if e1 != nil || e2 != nil || !hmac.Equal(sig, a.signCursor(documentPageDomain, b)) || json.Unmarshal(b, &p) != nil || p.Version != 1 {
		return nil, errors.New("invalid_cursor")
	}
	if p.Scope != scopeDigest(s) {
		return nil, errPageScope
	}
	return &content.ActivityCursor{AcceptedAt: p.AcceptedAt, VersionID: p.After}, nil
}

// adminDocumentRoutes serves GET /v0/admin/documents and
// GET /v0/admin/documents/{version_id}/timeline, behind observability:read.
func (a *API) adminDocumentRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
	versionID, timeline := strings.CutPrefix(r.URL.Path, "/v0/admin/documents/")
	if timeline {
		versionID, timeline = strings.CutSuffix(versionID, "/timeline")
		timeline = timeline && versionID != "" && !strings.Contains(versionID, "/")
	}
	if r.URL.Path != "/v0/admin/documents" && !timeline {
		return false
	}
	switch {
	case r.Method != "GET":
		failure(w, 405, "method_not_allowed")
	case !scope.Allows(content.ObservabilityRead):
		failure(w, 403, "forbidden")
	case a.Activity.Store == nil:
		failure(w, 404, "not_found")
	case timeline:
		activity, err := a.Activity.Version(r.Context(), scope, versionID)
		if err != nil {
			activityFailure(w, err)
			return true
		}
		out := transport.DocumentTimeline{Document: documentToTransport(activity), Steps: []transport.TimelineStep{}}
		for _, step := range content.Timeline(activity) {
			item := transport.TimelineStep{Step: transport.TimelineStepStep(step.Step), At: step.At}
			if step.Since != "" {
				since, ms := step.Since, int(step.Duration.Milliseconds())
				item.Since, item.DurationMs = &since, &ms
			}
			if step.Plugin != nil {
				id, version := step.Plugin.ID, step.Plugin.Version
				item.PluginId, item.PluginVersion = &id, &version
			}
			out.Steps = append(out.Steps, item)
		}
		send(w, 200, out)
	default:
		a.listDocuments(w, r, scope)
	}
	return true
}

func (a *API) listDocuments(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	q := r.URL.Query()
	for k, v := range q {
		if (k != "page_cursor" && k != "limit") || len(v) != 1 {
			failure(w, 422, "invalid_query")
			return
		}
	}
	limit := 100
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > 100 {
			failure(w, 422, "invalid_limit")
			return
		}
		limit = n
	}
	var after *content.ActivityCursor
	if q.Has("page_cursor") {
		var err error
		if after, err = a.decodeDocumentPage(q.Get("page_cursor"), scope); errors.Is(err, errPageScope) {
			failure(w, 409, "cursor_scope_changed")
			return
		} else if err != nil {
			failure(w, 422, "invalid_cursor")
			return
		}
	}
	items, err := a.Activity.Latest(r.Context(), scope, after, limit+1)
	if err != nil {
		activityFailure(w, err)
		return
	}
	page := transport.AdminDocumentPage{Items: make([]transport.AdminDocument, 0, min(len(items), limit))}
	for i, item := range items {
		if i == limit {
			last := items[limit-1]
			next := a.encodeDocumentPage(documentPage{Version: 1, Scope: scopeDigest(scope), AcceptedAt: *last.Steps.Accepted, After: last.VersionID})
			page.NextPageCursor = &next
			break
		}
		page.Items = append(page.Items, documentToTransport(item))
	}
	send(w, 200, page)
}

func activityFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, corpus.ErrForbidden):
		failure(w, 403, "forbidden")
	case errors.Is(err, corpus.ErrNotFound):
		failure(w, 404, "not_found")
	default:
		failure(w, 503, "storage_unavailable")
	}
}

func documentToTransport(a content.Activity) transport.AdminDocument {
	return transport.AdminDocument{VersionId: a.VersionID, RecordId: a.RecordID, CorpusId: a.Source.CorpusID, SourceNamespace: a.Source.Namespace, RecordKey: a.Source.RecordKey,
		Title: optionalString(a.Title), State: transport.AdminDocumentState(a.State), IsCurrent: a.Current, Steps: stepsToTransport(a.Steps)}
}

func stepsToTransport(s content.Steps) transport.VersionSteps {
	return transport.VersionSteps{AcceptedAt: s.Accepted, MaterializedAt: s.Materialized, SegmentedAt: s.Segmented, RetrievalReadyAt: s.RetrievalReady,
		EnrichedAt: s.Enriched, EvaluatedAt: s.Evaluated, QuarantinedAt: s.Quarantined, WithdrawnAt: s.Withdrawn}
}
