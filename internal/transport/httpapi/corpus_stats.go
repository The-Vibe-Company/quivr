package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

type statsPage struct {
	Scope  string                  `json:"scope"`
	Corpus string                  `json:"corpus"`
	Query  string                  `json:"query"`
	Hour   *time.Time              `json:"hour,omitempty"`
	Source *content.SourceStatsKey `json:"source,omitempty"`
}

const statsHistoryDomain = "quivr.corpus-stats.history.v1"
const statsSourcesDomain = "quivr.corpus-stats.sources.v1"

func (a *API) GetCorpusStats(ctx context.Context, in transport.GetCorpusStatsRequestObject) (transport.GetCorpusStatsResponseObject, error) {
	return transport.GetCorpusStatsResponseFunc(func(w http.ResponseWriter) { a.corpusStats(w, in.HTTPRequest, requestScope(ctx), in.CorpusId) }), nil
}

func (a *API) corpusStats(w http.ResponseWriter, r *http.Request, scope corpus.Scope, id string) {
	if err := scope.Require(corpus.ActionContentRecords); err != nil {
		writeError(w, err, nil)
		return
	}
	if !scope.Contains(id) {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	values := r.URL.Query()
	q := content.CorpusStatsQuery{SourceLimit: 100}
	for key, v := range values {
		switch key {
		case "include", "resolution", "accepted_after", "accepted_before", "histogram_cursor", "sources_cursor", "source_limit":
		default:
			writeError(w, publicerr.InvalidQuery, nil)
			return
		}
		if len(v) != 1 || v[0] == "" {
			writeError(w, publicerr.InvalidQuery, nil)
			return
		}
	}
	seen := map[string]bool{}
	if values.Has("include") {
		for _, v := range strings.Split(values.Get("include"), ",") {
			if seen[v] {
				writeError(w, publicerr.InvalidQuery, nil)
				return
			}
			seen[v] = true
			switch v {
			case "histogram":
				q.Histogram = true
			case "sources":
				q.Sources = true
			default:
				writeError(w, publicerr.InvalidQuery, nil)
				return
			}
		}
	}
	if !q.Histogram && (values.Has("resolution") || values.Has("accepted_after") || values.Has("accepted_before") || values.Has("histogram_cursor")) {
		writeError(w, publicerr.InvalidQuery, nil)
		return
	}
	if !q.Sources && (values.Has("source_limit") || values.Has("sources_cursor")) {
		writeError(w, publicerr.InvalidQuery, nil)
		return
	}
	if values.Has("resolution") {
		switch values.Get("resolution") {
		case "hour":
			q.Hourly = true
		case "day":
		default:
			writeError(w, publicerr.InvalidQuery, nil)
			return
		}
	}
	var ok bool
	q.From, ok = recordTime(w, values, "accepted_after")
	if !ok {
		return
	}
	q.To, ok = recordTime(w, values, "accepted_before")
	if !ok {
		return
	}
	for _, bound := range []*time.Time{q.From, q.To} {
		if bound != nil {
			*bound = bound.UTC()
			if !bound.Equal(bound.Truncate(time.Hour)) {
				writeError(w, publicerr.InvalidQuery, nil)
				return
			}
		}
	}
	if q.From != nil && q.To != nil && q.To.Before(*q.From) {
		writeError(w, publicerr.InvalidQuery, nil)
		return
	}
	if values.Has("source_limit") {
		var err error
		q.SourceLimit, err = strconv.Atoi(values.Get("source_limit"))
		if err != nil || q.SourceLimit < 1 || q.SourceLimit > 1000 {
			writeError(w, publicerr.InvalidQuery, nil)
			return
		}
	}
	raw, _ := json.Marshal(q)
	queryDigest := content.Hash(raw)
	for _, cursor := range []struct{ key, domain string }{{"histogram_cursor", statsHistoryDomain}, {"sources_cursor", statsSourcesDomain}} {
		if !values.Has(cursor.key) {
			continue
		}
		var p statsPage
		if a.decodePage(cursor.domain, values.Get(cursor.key), &p) != nil || (cursor.key == "histogram_cursor" && p.Hour == nil) || (cursor.key == "sources_cursor" && p.Source == nil) {
			writeError(w, publicerr.InvalidCursor, nil)
			return
		}
		if p.Scope != scopeDigest(scope) || p.Corpus != id || p.Query != queryDigest {
			writeError(w, publicerr.CursorScopeChanged, nil)
			return
		}
		if cursor.key == "histogram_cursor" {
			q.HistoryAfter = p.Hour
		} else {
			q.SourceAfter = p.Source
		}
	}
	out, err := a.Content.CorpusStats(r.Context(), scope, id, q)
	if err != nil {
		writeError(w, err, publicerr.ContentUnavailable)
		return
	}
	page := statsPage{Scope: scopeDigest(scope), Corpus: id, Query: queryDigest}
	if out.Histogram != nil && out.Histogram.Next != nil {
		page.Hour = out.Histogram.Next
		out.Histogram.NextPageCursor = a.encodePage(statsHistoryDomain, page)
		page.Hour = nil
	}
	if out.Sources != nil && out.Sources.Next != nil {
		page.Source = out.Sources.Next
		out.Sources.NextPageCursor = a.encodePage(statsSourcesDomain, page)
	}
	send(w, 200, out)
}
