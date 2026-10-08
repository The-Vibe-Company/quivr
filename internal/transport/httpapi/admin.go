package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
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
	return a.encodePage(documentPageDomain, p)
}

func (a *API) decodeDocumentPage(token string, s corpus.Scope) (*content.ActivityCursor, error) {
	var p documentPage
	if a.decodePage(documentPageDomain, token, &p) != nil || p.Version != 1 {
		return nil, errors.New("invalid_cursor")
	}
	if p.Scope != scopeDigest(s) {
		return nil, errPageScope
	}
	return &content.ActivityCursor{AcceptedAt: p.AcceptedAt, VersionID: p.After}, nil
}

func (a *API) handleGetDocumentTimeline(w http.ResponseWriter, r *http.Request, scope corpus.Scope, versionID string) {
	if versionID == "" {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	activity, err := a.Activity.Version(r.Context(), scope, versionID)
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
		return
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
}

func (a *API) listDocuments(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	var limit int
	var ok bool
	var after *content.ActivityCursor
	items, err := a.Activity.Latest(r.Context(), scope, nil, 0, func() (*content.ActivityCursor, int, error) {
		q := r.URL.Query()
		for k, v := range q {
			if (k != "page_cursor" && k != "limit") || len(v) != 1 {
				writeError(w, publicerr.InvalidQuery, nil)
				return nil, 0, errResponseWritten
			}
		}
		limit, ok = pageLimit(w, q, 100, 100)
		if !ok {
			return nil, 0, errResponseWritten
		}
		if q.Has("page_cursor") {
			var err error
			if after, err = a.decodeDocumentPage(q.Get("page_cursor"), scope); errors.Is(err, errPageScope) {
				writeError(w, publicerr.CursorScopeChanged, nil)
				return nil, 0, errResponseWritten
			} else if err != nil {
				writeError(w, publicerr.InvalidCursor, nil)
				return nil, 0, errResponseWritten
			}
		}

		return after, limit + 1, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
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

func documentToTransport(a content.Activity) transport.AdminDocument {
	return transport.AdminDocument{VersionId: a.VersionID, RecordId: a.RecordID, CorpusId: a.Source.CorpusID, SourceNamespace: a.Source.Namespace, RecordKey: a.Source.RecordKey,
		Title: optionalString(a.Title), State: transport.AdminDocumentState(a.State), IsCurrent: a.Current, Steps: stepsToTransport(a.Steps),
		Evaluation: transport.AdminDocumentEvaluation(a.Evaluation)}
}

func stepsToTransport(s content.Steps) transport.VersionSteps {
	return transport.VersionSteps{AcceptedAt: s.Accepted, MaterializedAt: s.Materialized, SegmentedAt: s.Segmented, RetrievalReadyAt: s.RetrievalReady,
		EnrichedAt: s.Enriched, EvaluatedAt: s.Evaluated, QuarantinedAt: s.Quarantined, WithdrawnAt: s.Withdrawn}
}
