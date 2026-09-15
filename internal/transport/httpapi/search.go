package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
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
	q := retrieval.Request{Query: wire.Query, CorpusIDs: wire.CorpusIds, Mode: "hybrid", Profile: "balanced", Limit: 10}
	if wire.Mode != nil {
		q.Mode = string(*wire.Mode)
	}
	if wire.Profile != nil {
		q.Profile = string(*wire.Profile)
	}
	if wire.Limit != nil {
		q.Limit = *wire.Limit
	}
	result, err := a.Retrieval.Search(r.Context(), scope, q)
	if err != nil {
		switch {
		case errors.Is(err, corpus.ErrForbidden):
			failure(w, 403, "forbidden")
		case errors.Is(err, retrieval.ErrUnsupported):
			failure(w, 422, "unsupported_search")
		default:
			failure(w, 503, "search_unavailable")
		}
		return
	}
	response := transport.SearchResponse{Items: []transport.SearchHit{}, RetrievalProfile: transport.SearchProfile{Name: "balanced", Version: result.Generation.ProfileVersion}}
	for i, h := range result.Hits {
		response.Items = append(response.Items, transport.SearchHit{RecordId: h.RecordID, VersionId: h.VersionID, PartKey: h.Segment.PartKey, SegmentId: h.Segment.ID, SegmentationId: h.SegmentationID, ProjectionGenerationId: result.Generation.ID, Rank: i + 1, Excerpt: transport.SearchExcerpt{Text: h.Segment.Text, Start: h.Segment.Start, End: h.Segment.End, CoordinateSystem: "unicode_codepoint"}, Availability: availabilityToTransport(h.Availability)})
	}
	send(w, 200, response)
}
