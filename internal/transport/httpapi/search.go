package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

// maxConcurrentSearches bounds storage fan-out per API process across callers.
// Excess work is rejected immediately; there is no unbounded search queue.
const maxConcurrentSearches = 64

func (a *API) search(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	raw, ok := decodeRequest(w, r, a.schemas["SearchRequest"])
	if !ok {
		return
	}
	b, err := json.Marshal(raw)
	if err != nil {
		writeError(w, publicerr.InvalidSchema, nil)
		return
	}
	var wire transport.SearchRequest
	if err = json.Unmarshal(b, &wire); err != nil {
		writeError(w, publicerr.InvalidSchema, nil)
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
	if wire.Filter != nil {
		if wire.Filter.RecordIds != nil {
			q.RecordIDs = *wire.Filter.RecordIds
		}
		if wire.Filter.VersionIds != nil {
			q.VersionIDs = *wire.Filter.VersionIds
		}
	}
	if wire.EvaluationPlugin != nil {
		q.EvaluationPlugin = *wire.EvaluationPlugin
	}
	if wire.EvaluationSpace != nil {
		q.Space = *wire.EvaluationSpace
	}
	if wire.Filter != nil && wire.Filter.Metadata != nil {
		encoded, _ := json.Marshal(wire.Filter.Metadata)
		if json.Unmarshal(encoded, &q.Metadata) != nil {
			writeError(w, publicerr.InvalidQuery, nil)
			return
		}
	}
	started := time.Now()
	select {
	case a.searches <- struct{}{}:
		defer func() { <-a.searches }()
	default:
		a.loadMetrics.SearchRefused()
		a.recordSearch(scope.Organization, q, retrieval.Result{}, started, publicerr.SearchUnavailable)
		w.Header().Set("Retry-After", "1")
		writeError(w, publicerr.SearchUnavailable, nil)
		return
	}
	result, err := a.Retrieval.Search(r.Context(), scope, q)
	a.recordSearch(scope.Organization, q, result, started, err)
	if err != nil {
		writeError(w, err, publicerr.SearchUnavailable)
		return
	}
	response := transport.SearchResponse{ExcludedCorpora: exclusionsToTransport(result.ExcludedCorpora), Items: []transport.SearchHit{}, RetrievalProfile: transport.SearchProfile{Name: result.Profile, Version: result.ProfileVersion}}
	if len(result.Degraded) > 0 {
		degraded := make([]transport.SearchDegradation, 0, len(result.Degraded))
		for _, d := range result.Degraded {
			degraded = append(degraded, transport.SearchDegradation{Reason: transport.SearchDegradationReason(d.Reason), CorpusIds: d.CorpusIDs})
		}
		response.RetrievalProfile.Degraded = &degraded
	}
	for i, h := range result.Hits {
		hit := transport.SearchHit{EmbeddingArtifactId: optionalString(h.EmbeddingID), VectorSpaceId: optionalString(h.SpaceID), RecordId: h.RecordID, VersionId: h.VersionID, PartKey: h.Segment.PartKey, SegmentId: h.Segment.ID, SegmentationId: h.SegmentationID, ProjectionGenerationId: h.GenerationID, Rank: i + 1, Excerpt: transport.SearchExcerpt{Text: h.Segment.AnchorText(), Start: h.Segment.Start, End: h.Segment.End, CoordinateSystem: "unicode_codepoint"}, Availability: availabilityToTransport(h.Availability), Explanation: optionalString(h.Explanation)}
		if len(h.Segment.SourceExcerpts) > 0 {
			hit.PassageText = optionalString(h.Segment.Text)
			excerpts := []struct {
				CoordinateSystem transport.SearchHitSourceExcerptsCoordinateSystem `json:"coordinate_system"`
				End              int                                               `json:"end"`
				PartKey          string                                            `json:"part_key"`
				Start            int                                               `json:"start"`
				Text             string                                            `json:"text"`
			}{}
			for _, source := range h.Segment.SourceExcerpts {
				excerpts = append(excerpts, struct {
					CoordinateSystem transport.SearchHitSourceExcerptsCoordinateSystem `json:"coordinate_system"`
					End              int                                               `json:"end"`
					PartKey          string                                            `json:"part_key"`
					Start            int                                               `json:"start"`
					Text             string                                            `json:"text"`
				}{"unicode_codepoint", source.End, source.PartKey, source.Start, source.Text})
			}
			hit.SourceExcerpts = &excerpts
		}
		response.Items = append(response.Items, hit)
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
	profiles := make([]transport.SearchProfileUsage, 0, len(u.Profiles))
	for _, p := range u.Profiles {
		profiles = append(profiles, transport.SearchProfileUsage{Profile: p.Profile, PluginVersion: p.PluginVersion, Rounds: p.Rounds, PaidCalls: p.PaidCalls, CostCents: float32(p.CostCents)})
	}
	out := &transport.SearchUsage{Rounds: u.Rounds, ElapsedMs: int(u.Elapsed.Milliseconds()), PaidCalls: u.PaidCalls, CostCents: float32(u.CostCents),
		Phases: &transport.SearchPhases{RoutingMs: int(ph.Routing.Milliseconds()), CoverageMs: int(ph.Coverage.Milliseconds()), PluginRoundsMs: int(ph.PluginRounds.Milliseconds()),
			QueryEncodingMs: int(ph.QueryEncoding.Milliseconds()), IndexQueryMs: int(ph.IndexQuery.Milliseconds()), HydrationMs: int(ph.Hydration.Milliseconds())}}
	if len(profiles) > 0 {
		out.Profiles = &profiles
	}
	return out
}

// searchProfiles lists the search profiles this deployment answers.
func (a *API) searchProfiles(w http.ResponseWriter, scope corpus.Scope) {
	profiles, err := a.Retrieval.ProfilesScoped(scope)
	if err != nil {
		writeError(w, publicerr.Forbidden, nil)
		return
	}
	out := transport.SearchProfileList{Items: []transport.SearchProfileDescription{}}
	for _, p := range profiles {
		latency, cost := p.MaxLatencyMS, float32(p.MaxCostCents)
		item := transport.SearchProfileDescription{Name: p.Name, FullName: p.FullName, Aliases: p.Aliases, Description: optionalString(p.Description), MaxLatencyMs: &latency, MaxCostCents: &cost}
		item.Provider.Kind = transport.SearchProfileDescriptionProviderKindPlugin
		item.Provider.PluginId, item.Provider.PluginVersion = optionalString(p.PluginID), optionalString(p.PluginVersion)
		out.Items = append(out.Items, item)
	}
	send(w, 200, out)
}
