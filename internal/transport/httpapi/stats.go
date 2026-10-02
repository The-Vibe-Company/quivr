package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/observability"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// WithObservability counts searches on recorder and serves the admin stats
// reads from reader (THE-795).
func WithObservability(recorder *observability.Recorder, reader observability.Reader) Option {
	return func(a *API) { a.Recorder, a.Stats = recorder, reader }
}

const statsPrefix = "/v0/admin/stats/"

// statsRoutes serves GET /v0/admin/stats/{plugins,searches,steps,received,
// matches,top-queries} behind observability:read on all Corpora, always for
// the key's own Organization.
func (a *API) statsRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
	if !strings.HasPrefix(r.URL.Path, statsPrefix) {
		return false
	}
	name := strings.TrimPrefix(r.URL.Path, statsPrefix)
	series := map[string]string{"plugins": observability.SeriesPluginCall, "searches": observability.SeriesSearch, "steps": observability.SeriesStep,
		"received": observability.SeriesReceived, "matches": observability.SeriesMatch, "top-queries": observability.SeriesSearchQuery, "connector-pushes": observability.SeriesConnectorPush}[name]
	switch {
	case series == "":
		writeError(w, publicerr.NotFound, nil)
	case r.Method != "GET":
		writeError(w, publicerr.MethodNotAllowed, nil)
	default:
		err := a.Stats.Read(scope, func(reader observability.ScopedReader) error {
			a.stats(w, r, reader, series)
			return nil
		})
		if err != nil {
			writeError(w, err, publicerr.NotFound)
		}
	}
	return true
}

// countedLimits are the default and largest limit of the counted reads: the
// keys of received and top-queries come from clients, so a read lists the
// largest only. Matches are keyed by evaluator plugin, a deployment set.
var countedLimits = map[string][2]int{observability.SeriesReceived: {10, 100}, observability.SeriesSearchQuery: {20, 100}, observability.SeriesMatch: {100, 100}, observability.SeriesConnectorPush: {100, 100}}

func (a *API) stats(w http.ResponseWriter, r *http.Request, reader observability.ScopedReader, series string) {
	q := r.URL.Query()
	limits, counted := countedLimits[series]
	for k, v := range q {
		if (k != "window" && (k != "limit" || series == observability.SeriesMatch || !counted) && (k != "connector_id" || series != observability.SeriesConnectorPush)) || len(v) != 1 {
			writeError(w, publicerr.InvalidQuery, nil)
			return
		}
	}
	window, ok := observability.ParseWindow(q.Get("window"))
	if !ok {
		writeError(w, publicerr.InvalidWindow, nil)
		return
	}
	name := transport.StatsWindowName(window.Name)
	if series == observability.SeriesConnectorPush && q.Has("connector_id") {
		id := q.Get("connector_id")
		if id == "" || len(id) > 256 || q.Has("limit") {
			writeError(w, publicerr.InvalidQuery, nil)
			return
		}
		report, err := reader.Report(r.Context(), series, window, observability.Key(id, "received"), observability.Key(id, "refused"))
		if err != nil {
			writeError(w, publicerr.StorageUnavailable, nil)
			return
		}
		out := transport.ConnectorPushStatsList{Window: name, ResolutionSeconds: int(window.Tier.Resolution / time.Second), From: report.From, To: report.To, Items: []transport.ConnectorPushStats{}}
		for _, s := range report.Series {
			key := observability.KeyParts(s.Key, 2)
			points := make([]transport.CountPoint, 0, len(s.Points))
			for _, p := range s.Points {
				points = append(points, transport.CountPoint{Start: p.Start, Count: int(p.Count)})
			}
			out.Total += int(s.Summary.Count)
			out.Items = append(out.Items, transport.ConnectorPushStats{ConnectorId: key[0], Outcome: transport.ConnectorPushStatsOutcome(key[1]), Count: int(s.Summary.Count), Points: points})
		}
		send(w, 200, out)
		return
	}
	if counted {
		limit, ok := pageLimit(w, q, limits[0], limits[1])
		if !ok {
			return
		}
		a.counts(w, r, reader, series, window, limit)
		return
	}
	report, err := reader.Report(r.Context(), series, window)
	if err != nil {
		writeError(w, publicerr.StorageUnavailable, nil)
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
			out.Items = append(out.Items, transport.SearchStats{Mode: transport.SearchStatsMode(key[0]), Profile: key[1], Results: int(s.Summary.Items), OverObjective: int(s.Summary.OverObjective), Summary: statsSummary(s.Summary), Points: statsPoints(s.Points)})
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

// counts serves the counted reads: documents received per source namespace,
// Matches per evaluator and the most frequent queries.
func (a *API) counts(w http.ResponseWriter, r *http.Request, reader observability.ScopedReader, series string, window observability.Window, limit int) {
	var counts observability.Counts
	var err error
	if series == observability.SeriesSearchQuery {
		counts, err = reader.TopQueries(r.Context(), window, limit)
	} else {
		counts, err = reader.Counts(r.Context(), series, window, limit)
	}
	if err != nil {
		writeError(w, publicerr.StorageUnavailable, nil)
		return
	}
	name := transport.StatsWindowName(window.Name)
	resolution := int(counts.Resolution / time.Second)
	switch series {
	case observability.SeriesConnectorPush:
		out := transport.ConnectorPushStatsList{Window: name, ResolutionSeconds: resolution, From: counts.From, To: counts.To, Total: int(counts.Total), Items: []transport.ConnectorPushStats{}}
		for _, s := range counts.Series {
			key := observability.KeyParts(s.Key, 2)
			out.Items = append(out.Items, transport.ConnectorPushStats{ConnectorId: key[0], Outcome: transport.ConnectorPushStatsOutcome(key[1]), Count: int(s.Count), Points: countPoints(s.Points)})
		}
		send(w, 200, out)
	case observability.SeriesSearchQuery:
		out := transport.TopQueryList{Window: name, ResolutionSeconds: resolution, Recording: a.Stats.RecordQueryText, Items: make([]transport.TopQuery, 0, len(counts.Series))}
		for _, s := range counts.Series {
			out.Items = append(out.Items, transport.TopQuery{Query: s.Key, Count: int(s.Count), Points: countPoints(s.Points)})
		}
		send(w, 200, out)
	case observability.SeriesReceived:
		out := transport.ReceivedStatsList{Window: name, ResolutionSeconds: resolution, From: counts.From, To: counts.To, Total: int(counts.Total), Sources: counts.Distinct,
			Items: make([]transport.ReceivedStats, 0, len(counts.Series))}
		for _, s := range counts.Series {
			out.Items = append(out.Items, transport.ReceivedStats{SourceNamespace: s.Key, Count: int(s.Count), Points: countPoints(s.Points)})
		}
		send(w, 200, out)
	default:
		out := transport.MatchStatsList{Window: name, ResolutionSeconds: resolution, From: counts.From, To: counts.To, Total: int(counts.Total), Items: make([]transport.MatchStats, 0, len(counts.Series))}
		for _, s := range counts.Series {
			out.Items = append(out.Items, transport.MatchStats{Evaluator: s.Key, Count: int(s.Count), Points: countPoints(s.Points)})
		}
		send(w, 200, out)
	}
}

func countPoints(points []observability.CountPoint) []transport.CountPoint {
	out := make([]transport.CountPoint, 0, len(points))
	for _, p := range points {
		out = append(out, transport.CountPoint{Start: p.Start, Count: int(p.Count)})
	}
	return out
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
		_, body := errorResponse(err, publicerr.SearchUnavailable)
		code = body.Code
	}
	a.Recorder.Search(observability.Search{Organization: org, Mode: q.Mode, Profile: profile, Query: q.Query, Results: len(result.Hits), Duration: time.Since(started), ErrorCode: code,
		OverObjective: result.Usage != nil && result.Usage.OverObjective})
}
