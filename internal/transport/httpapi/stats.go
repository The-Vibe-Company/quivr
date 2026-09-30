package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/observability"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// WithObservability counts searches on recorder and serves the admin stats
// reads from reader (THE-795).
func WithObservability(recorder *observability.Recorder, reader observability.Reader) Option {
	return func(a *API) { a.Recorder, a.Stats = recorder, reader }
}

const statsPrefix = "/v0/admin/stats/"

// statsRoutes serves GET /v0/admin/stats/{plugins,searches,steps,top-queries}
// behind observability:read on all Corpora, always for the key's own
// Organization.
func (a *API) statsRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
	if !strings.HasPrefix(r.URL.Path, statsPrefix) {
		return false
	}
	name := strings.TrimPrefix(r.URL.Path, statsPrefix)
	series := map[string]string{"plugins": observability.SeriesPluginCall, "searches": observability.SeriesSearch, "steps": observability.SeriesStep}[name]
	switch {
	case series == "" && name != "top-queries":
		failure(w, 404, "not_found")
	case r.Method != "GET":
		failure(w, 405, "method_not_allowed")
	// The rollups cover every Corpus of the Organization, query text included.
	case !scope.Allows(content.ObservabilityRead) || !scope.AllCorpora():
		failure(w, 403, "forbidden")
	case a.Stats.Store == nil:
		failure(w, 404, "not_found")
	default:
		a.stats(w, r, scope.Organization, series)
	}
	return true
}

func (a *API) stats(w http.ResponseWriter, r *http.Request, org, series string) {
	q := r.URL.Query()
	for k, v := range q {
		if (k != "window" && (k != "limit" || series != "")) || len(v) != 1 {
			failure(w, 422, "invalid_query")
			return
		}
	}
	window, ok := observability.ParseWindow(q.Get("window"))
	if !ok {
		failure(w, 422, "invalid_window")
		return
	}
	name := transport.StatsWindowName(window.Name)
	if series == "" {
		limit := 20
		if q.Has("limit") {
			n, err := strconv.Atoi(q.Get("limit"))
			if err != nil || n < 1 || n > 100 {
				failure(w, 422, "invalid_limit")
				return
			}
			limit = n
		}
		top, err := a.Stats.TopQueries(r.Context(), org, window, limit)
		if err != nil {
			failure(w, 503, "storage_unavailable")
			return
		}
		out := transport.TopQueryList{Window: name, Recording: a.Stats.RecordQueryText, Items: make([]transport.TopQuery, 0, len(top))}
		for _, k := range top {
			out.Items = append(out.Items, transport.TopQuery{Query: k.Key, Count: int(k.Count)})
		}
		send(w, 200, out)
		return
	}
	report, err := a.Stats.Report(r.Context(), org, series, window)
	if err != nil {
		failure(w, 503, "storage_unavailable")
		return
	}
	resolution := int(window.Tier.Resolution / time.Second)
	switch series {
	case observability.SeriesPluginCall:
		out := transport.PluginCallStatsList{Window: name, ResolutionSeconds: resolution, From: report.From, To: report.To, Items: []transport.PluginCallStats{}}
		for _, s := range report.Series {
			key := observability.KeyParts(s.Key, 3)
			out.Items = append(out.Items, transport.PluginCallStats{PluginId: key[0], PluginVersion: key[1], Operation: transport.PluginCallStatsOperation(key[2]), Summary: statsSummary(s.Summary), Points: statsPoints(s.Points)})
		}
		send(w, 200, out)
	case observability.SeriesSearch:
		out := transport.SearchStatsList{Window: name, ResolutionSeconds: resolution, From: report.From, To: report.To, Items: []transport.SearchStats{}}
		for _, s := range report.Series {
			key := observability.KeyParts(s.Key, 2)
			out.Items = append(out.Items, transport.SearchStats{Mode: transport.SearchStatsMode(key[0]), Profile: key[1], Results: int(s.Summary.Items), Summary: statsSummary(s.Summary), Points: statsPoints(s.Points)})
		}
		send(w, 200, out)
	default:
		out := transport.StepStatsList{Window: name, ResolutionSeconds: resolution, From: report.From, To: report.To, Items: []transport.StepStats{}}
		for _, s := range report.Series {
			out.Items = append(out.Items, transport.StepStats{Step: s.Key, Summary: statsSummary(s.Summary), Points: statsPoints(s.Points)})
		}
		send(w, 200, out)
	}
}

func statsSummary(s observability.Summary) transport.StatsSummary {
	out := transport.StatsSummary{Count: int(s.Count), Errors: int(s.Errors), LastErrorCode: optionalString(s.LastErrorCode)}
	if s.Count > 0 {
		p50, p95, mean := float32(s.P50MS), float32(s.P95MS), float32(s.MeanMS)
		out.P50Ms, out.P95Ms, out.MeanMs = &p50, &p95, &mean
	}
	if !s.LastErrorAt.IsZero() {
		at := s.LastErrorAt
		out.LastErrorAt = &at
	}
	return out
}

func statsPoints(points []observability.Point) []transport.StatsPoint {
	out := make([]transport.StatsPoint, 0, len(points))
	for _, p := range points {
		out = append(out, transport.StatsPoint{Start: p.Start, Count: int(p.Count), Errors: int(p.Errors), P50Ms: float32(p.P50MS), P95Ms: float32(p.P95MS)})
	}
	return out
}

// recordSearch counts one public search. The profile label is the one that
// answered, or the requested one only when the deployment serves it, so a
// client cannot create series with arbitrary names.
func (a *API) recordSearch(org string, q retrieval.Request, result retrieval.Result, started time.Time, err error) {
	if a.Recorder == nil {
		return
	}
	profile := result.Profile
	if profile == "" {
		profile = q.Profile
		if profile == "" || profile == retrieval.LegacyProfile {
			profile = retrieval.DefaultProfile
		}
		if !a.Retrieval.Serves(profile) {
			profile = "unknown"
		}
	}
	code := ""
	if err != nil {
		_, code = searchFailure(err)
	}
	a.Recorder.Search(observability.Search{Organization: org, Mode: q.Mode, Profile: profile, Query: q.Query, Results: len(result.Hits), Duration: time.Since(started), ErrorCode: code})
}
