package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

func (a *API) search(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	raw, ok := decodeRequest(w, r, a.searchSchema)
	if !ok {
		return
	}
	b, err := json.Marshal(raw)
	if err != nil {
		failure(w, 422, "invalid_schema")
		return
	}
	var wire transport.SearchRequest
	if err = json.Unmarshal(b, &wire); err != nil {
		failure(w, 422, "invalid_schema")
		return
	}
	q := retrieval.Request{Query: wire.Query, CorpusIDs: wire.CorpusIds, Mode: "hybrid", Limit: 10}
	if wire.Mode != nil {
		q.Mode = string(*wire.Mode)
	}
	if wire.Profile != nil {
		q.Profile = *wire.Profile
	}
	if wire.Limit != nil {
		q.Limit = *wire.Limit
	}
	if wire.Filter != nil && wire.Filter.SourceNamespaces != nil {
		q.SourceNamespaces = *wire.Filter.SourceNamespaces
	}
	started := time.Now()
	result, err := a.Retrieval.Search(r.Context(), scope, q)
	a.recordSearch(scope.Organization, q, result, started, err)
	if err != nil {
		status, body := searchError(err)
		send(w, status, body)
		return
	}
	response := transport.SearchResponse{Items: []transport.SearchHit{}, RetrievalProfile: transport.SearchProfile{Name: result.Profile, Version: result.ProfileVersion}}
	for i, h := range result.Hits {
		response.Items = append(response.Items, transport.SearchHit{EmbeddingArtifactId: optionalString(h.EmbeddingID), VectorSpaceId: optionalString(h.SpaceID), RecordId: h.RecordID, VersionId: h.VersionID, PartKey: h.Segment.PartKey, SegmentId: h.Segment.ID, SegmentationId: h.SegmentationID, ProjectionGenerationId: h.GenerationID, Rank: i + 1, Excerpt: transport.SearchExcerpt{Text: h.Segment.Text, Start: h.Segment.Start, End: h.Segment.End, CoordinateSystem: "unicode_codepoint"}, Availability: availabilityToTransport(h.Availability), Explanation: optionalString(h.Explanation)})
	}
	if u := result.Usage; u != nil {
		response.Usage = usageToTransport(*u)
	}
	send(w, 200, response)
}

// usageToTransport is what a search spent, with each phase in whole
// milliseconds, rounded down.
func usageToTransport(u retrieval.Usage) *transport.SearchUsage {
	ph := u.Phases
	return &transport.SearchUsage{Rounds: u.Rounds, ElapsedMs: int(u.Elapsed.Milliseconds()), PaidCalls: u.PaidCalls, CostCents: float32(u.CostCents),
		Phases: &transport.SearchPhases{RoutingMs: int(ph.Routing.Milliseconds()), CoverageMs: int(ph.Coverage.Milliseconds()), PluginRoundsMs: int(ph.PluginRounds.Milliseconds()),
			QueryEncodingMs: int(ph.QueryEncoding.Milliseconds()), IndexQueryMs: int(ph.IndexQuery.Milliseconds()), HydrationMs: int(ph.Hydration.Milliseconds())}}
}

// searchProfiles lists the search profiles this deployment answers.
func (a *API) searchProfiles(w http.ResponseWriter, scope corpus.Scope) {
	if !scope.Allows("search:query") {
		failure(w, 403, "forbidden")
		return
	}
	out := transport.SearchProfileList{Items: []transport.SearchProfileDescription{}}
	for _, p := range a.Retrieval.Profiles() {
		latency, cost := p.MaxLatencyMS, float32(p.MaxCostCents)
		item := transport.SearchProfileDescription{Name: p.Name, FullName: p.FullName, Aliases: p.Aliases, Description: optionalString(p.Description), MaxLatencyMs: &latency, MaxCostCents: &cost}
		item.Provider.Kind = transport.SearchProfileDescriptionProviderKindPlugin
		item.Provider.PluginId, item.Provider.PluginVersion = optionalString(p.PluginID), optionalString(p.PluginVersion)
		out.Items = append(out.Items, item)
	}
	send(w, 200, out)
}

// searchError is the status and error body of a failed search. A query over
// its length limit carries a message naming the limit.
func searchError(err error) (int, transport.Error) {
	status, code := searchFailure(err)
	body := apiError(status, code)
	if detail := publicerr.Detail(err); code == "query_too_long" && detail != "" {
		body.Message = detail
	}
	return status, body
}

// searchFailure maps a search error to its status and public code.
func searchFailure(err error) (int, string) {
	switch {
	case errors.Is(err, corpus.ErrForbidden):
		return 403, "forbidden"
	case errors.Is(err, retrieval.ErrUnsupportedProfile):
		return 422, "unsupported_profile"
	case errors.Is(err, retrieval.ErrQueryTooLong):
		return 422, "query_too_long"
	case errors.Is(err, retrieval.ErrUnsupported):
		return 422, "unsupported_search"
	case errors.Is(err, retrieval.ErrSourceFilterUnavailable):
		return 422, "source_filter_unavailable"
	case errors.Is(err, retrieval.ErrPluginInvalid):
		return 502, "retrieval_plugin_invalid"
	case errors.Is(err, retrieval.ErrDeadline):
		return 504, "search_deadline_exceeded"
	}
	return 503, "search_unavailable"
}
