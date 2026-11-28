package retrieval_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

type richBaseline struct {
	content.BaselineRepository
	leadCalls int
}

const (
	richLeadText = "Dockers stopped work at the northern harbour at dawn."
	richDeepText = "Port negotiators scheduled talks for Tuesday afternoon."
)

func (b *richBaseline) Hydrate(_ context.Context, _ corpus.Scope, cs []content.Candidate) (map[int]content.Located, error) {
	out := map[int]content.Located{}
	meta := json.RawMessage(`{"language":"en","published_at":"2026-10-01T12:30:00Z","source":"Harbour Wire","tags":["harbour"]}`)
	for i, c := range cs {
		switch c.SegmentID {
		case "seg-lead":
			out[i] = content.Located{
				Hydrated: content.Hydrated{
					GenerationID:   c.GenerationID,
					RecordID:       "rec-1",
					VersionID:      "ver-1",
					SegmentationID: "segmentation-1",
					Title:          "Harbour ferry crews begin stoppage",
					CommonMetadata: meta,
					TotalPassages:  4,
					TextSHA256:     content.Hash([]byte(richLeadText)),
					Segment: content.Segment{
						ID:         "seg-lead",
						PartKey:    "body",
						Start:      0,
						End:        len([]rune(richLeadText)),
						Derivation: content.SegmentDerivation{Ordinal: 0},
					},
				},
				Blob: content.Blob{Key: "blob-lead"},
			}
		case "seg-deep":
			out[i] = content.Located{
				Hydrated: content.Hydrated{
					GenerationID:   c.GenerationID,
					RecordID:       "rec-1",
					VersionID:      "ver-1",
					SegmentationID: "segmentation-1",
					Title:          "Harbour ferry crews begin stoppage",
					CommonMetadata: meta,
					TotalPassages:  4,
					TextSHA256:     content.Hash([]byte(richDeepText)),
					Segment: content.Segment{
						ID:         "seg-deep",
						PartKey:    "body",
						Start:      200,
						End:        200 + len([]rune(richDeepText)),
						Derivation: content.SegmentDerivation{Ordinal: 2},
					},
				},
				Blob: content.Blob{Key: "blob-deep"},
			}
		case "seg-legacy":
			out[i] = content.Located{
				Hydrated: content.Hydrated{
					GenerationID:   c.GenerationID,
					RecordID:       "rec-2",
					VersionID:      "ver-2",
					SegmentationID: "segmentation-2",
					TotalPassages:  0,
					TextSHA256:     content.Hash([]byte(richLeadText)),
					Segment: content.Segment{
						ID:         "seg-legacy",
						PartKey:    "body",
						Start:      0,
						End:        len([]rune(richLeadText)),
						Derivation: content.SegmentDerivation{Ordinal: 0},
					},
				},
				Blob: content.Blob{Key: "blob-lead"},
			}
		}
	}
	return out, nil
}

func (b *richBaseline) LeadSegments(_ context.Context, _ string, refs []content.LeadSegmentRef) (map[string]string, error) {
	b.leadCalls++
	out := map[string]string{}
	for _, ref := range refs {
		if ref.SegmentationID == "segmentation-1" {
			out[ref.SegmentationID] = "seg-lead"
		}
	}
	return out, nil
}

type richBlobs struct{ content.Blobs }

func (richBlobs) Read(_ context.Context, blob content.Blob) ([]byte, error) {
	if blob.Key == "blob-deep" {
		return []byte(strings.Repeat("x", 200) + richDeepText), nil
	}
	return []byte(richLeadText), nil
}

type richRanker struct {
	id          string
	apiRange    string
	requires    []plugins.Requirement
	primitive   string
	includeLead bool
	maxCand     int
	reqK        int
	rawServed   []byte
	served      []plugins.ServedRequest
}

func (r *richRanker) Manifest() *plugins.Manifest {
	maxC := r.maxCand
	if maxC == 0 {
		maxC = 50
	}
	return &plugins.Manifest{
		ID:            r.id,
		Version:       "1.0.0",
		Compatibility: plugins.Compatibility{Engine: ">=0.1.0 <0.3.0", PluginAPI: r.apiRange},
		Requires:      r.requires,
		Contributions: plugins.Contributions{
			Retrieval: &plugins.Retrieval{
				Profiles: map[string]plugins.RetrievalProfile{"default": {MaxLatencyMS: 2000}},
				Limits:   plugins.RetrievalLimits{MaxRounds: 3, MaxRequests: 4, MaxCandidates: maxC},
			},
		},
	}
}
func (r *richRanker) Configuration() json.RawMessage { return json.RawMessage(`{}`) }
func (r *richRanker) Round(_ context.Context, q plugins.SearchRequest) ([]byte, error) {
	if q.Round == 1 {
		if r.primitive == plugins.PrimitiveProfile {
			req := map[string]any{
				"primitive": "profile",
				"profile":   map[string]any{"name": "inner.retrieve/default", "limit": 10},
			}
			if r.includeLead {
				req["include_lead"] = true
			}
			return answer(map[string]any{"requests": []any{req}})
		}
		k := r.reqK
		if k == 0 {
			k = 10
		}
		req := map[string]any{"primitive": "bm25", "query_text": q.Query.Text, "k": k}
		if r.includeLead {
			req["include_lead"] = true
		}
		return answer(map[string]any{"requests": []any{req}})
	}
	r.served = q.Served
	r.rawServed, _ = json.Marshal(q)
	var hits []map[string]any
	for _, s := range q.Served {
		for _, c := range s.Candidates {
			hits = append(hits, map[string]any{"segment_id": c.SegmentID, "score": c.Score})
		}
	}
	return answer(map[string]any{"ranking": map[string]any{"hits": hits}})
}

type staticRouter struct {
	rankers map[string]retrieval.Ranker
}

func (r staticRouter) Profiles() []retrieval.Profile { return nil }
func (r staticRouter) Resolve(profile string) (retrieval.Ranker, string, bool) {
	if rk, ok := r.rankers[profile]; ok {
		return rk, "default", true
	}
	return nil, "", false
}

// Plugin API 0.21 rich candidate metadata and opt-in lead passages are strictly
// gated behind 0.21 ranges so older plugins never receive unknown JSON keys,
// and lead hydration stays zero-cost unless include_lead is requested.
func TestRichRetrievalCandidatesAndOptInLeadPassage(t *testing.T) {
	gen := content.Generation{
		ID:             "gen-rich",
		Collection:     "Shared",
		ProfileVersion: retrieval.ProfileVersion,
		SpaceID:        "core.ingest.e5-small@1",
	}
	proj := &spaceProjection{bySpace: map[string][]content.Candidate{
		"": {
			{SegmentID: "seg-deep", GenerationID: gen.ID, Score: 0.9},
			{SegmentID: "seg-legacy", GenerationID: gen.ID, Score: 0.5},
		},
	}}

	t.Run("0.21 ranker with include_lead receives rich fields and lead passage only for ordinal > 0", func(t *testing.T) {
		base := &richBaseline{}
		ranker := &richRanker{id: "example.rich", apiRange: ">=0.21.0 <0.22.0", includeLead: true, maxCand: 250, reqK: 200}
		svc := retrieval.Service{
			Routing:    spaceRouting{"corpus": gen},
			Projection: proj,
			Ranker:     ranker,
			Content:    content.Service{Baseline: base, Blobs: richBlobs{}},
		}
		if _, err := svc.Search(context.Background(), searchScope, retrieval.Request{Query: "harbour", CorpusIDs: []string{"corpus"}, Profile: "default", Mode: "lexical", Limit: 5}); err != nil {
			t.Fatalf("Search: %v", err)
		}
		if base.leadCalls != 1 {
			t.Fatalf("LeadSegments called %d times, want 1", base.leadCalls)
		}
		if issues := plugins.ValidateDocument("retrieval-search-request.schema.json", ranker.rawServed); len(issues) > 0 {
			t.Fatalf("served round 2 request violates schema: %+v", issues)
		}
		cands := ranker.served[0].Candidates
		if len(cands) != 2 {
			t.Fatalf("got %d candidates, want 2", len(cands))
		}
		deep, legacy := cands[0], cands[1]
		if deep.Title != "Harbour ferry crews begin stoppage" || deep.TotalPassages != 4 || deep.PassageOrdinal == nil || *deep.PassageOrdinal != 2 || deep.LeadPassage != richLeadText || len(deep.CommonMetadata) == 0 {
			t.Fatalf("unexpected deep candidate: %+v", deep)
		}
		if legacy.PassageOrdinal == nil || *legacy.PassageOrdinal != 0 || legacy.TotalPassages != 0 || legacy.Title != "" || len(legacy.CommonMetadata) != 0 || legacy.LeadPassage != "" {
			t.Fatalf("unexpected legacy candidate: %+v", legacy)
		}
	})

	t.Run("0.21 ranker without include_lead skips LeadSegments entirely", func(t *testing.T) {
		base := &richBaseline{}
		ranker := &richRanker{id: "example.rich", apiRange: ">=0.21.0 <0.22.0", includeLead: false}
		svc := retrieval.Service{
			Routing:    spaceRouting{"corpus": gen},
			Projection: proj,
			Ranker:     ranker,
			Content:    content.Service{Baseline: base, Blobs: richBlobs{}},
		}
		if _, err := svc.Search(context.Background(), searchScope, retrieval.Request{Query: "harbour", CorpusIDs: []string{"corpus"}, Profile: "default", Mode: "lexical", Limit: 5}); err != nil {
			t.Fatalf("Search: %v", err)
		}
		if base.leadCalls != 0 {
			t.Fatalf("LeadSegments called %d times without include_lead, want 0", base.leadCalls)
		}
		if ranker.served[0].Candidates[0].LeadPassage != "" {
			t.Fatalf("lead_passage populated without include_lead: %+v", ranker.served[0].Candidates[0])
		}
	})

	t.Run("older and wide ranges never see 0.21 candidate JSON keys", func(t *testing.T) {
		for _, apiRange := range []string{">=0.20.0 <0.21.0", ">=0.7.0 <0.22.0"} {
			t.Run(apiRange, func(t *testing.T) {
				base := &richBaseline{}
				ranker := &richRanker{id: "example.old", apiRange: apiRange}
				svc := retrieval.Service{
					Routing:    spaceRouting{"corpus": gen},
					Projection: proj,
					Ranker:     ranker,
					Content:    content.Service{Baseline: base, Blobs: richBlobs{}},
				}
				if _, err := svc.Search(context.Background(), searchScope, retrieval.Request{Query: "harbour", CorpusIDs: []string{"corpus"}, Profile: "default", Mode: "lexical", Limit: 5}); err != nil {
					t.Fatalf("Search: %v", err)
				}
				var decoded struct {
					Served []struct {
						Candidates []map[string]any `json:"candidates"`
					} `json:"served"`
				}
				if err := json.Unmarshal(ranker.rawServed, &decoded); err != nil {
					t.Fatal(err)
				}
				for _, cand := range decoded.Served[0].Candidates {
					for _, key := range []string{"title", "common_metadata", "passage_ordinal", "total_passages", "lead_passage"} {
						if _, present := cand[key]; present {
							t.Fatalf("range %q received 0.21 field %q on candidate: %v", apiRange, key, cand)
						}
					}
				}
			})
		}
	})

	t.Run("composed profile reranker with include_lead receives rich fields and lead passage", func(t *testing.T) {
		base := &richBaseline{}
		inner := &richRanker{id: "inner.retrieve", apiRange: ">=0.20.0 <0.21.0"}
		outer := &richRanker{
			id:          "outer.rerank",
			apiRange:    ">=0.21.0 <0.22.0",
			requires:    []plugins.Requirement{{Plugin: "inner.retrieve", Version: ">=0.1.0 <2.0.0", Profiles: []string{"default"}}},
			primitive:   plugins.PrimitiveProfile,
			includeLead: true,
		}
		svc := retrieval.Service{
			Routing:    spaceRouting{"corpus": gen},
			Projection: proj,
			ProfilesRouter: staticRouter{rankers: map[string]retrieval.Ranker{
				"default":                outer,
				"inner.retrieve/default": inner,
			}},
			Content: content.Service{Baseline: base, Blobs: richBlobs{}},
		}
		if _, err := svc.Search(context.Background(), searchScope, retrieval.Request{Query: "harbour", CorpusIDs: []string{"corpus"}, Profile: "default", Mode: "lexical", Limit: 5}); err != nil {
			t.Fatalf("Search: %v", err)
		}
		if base.leadCalls != 1 {
			t.Fatalf("LeadSegments called %d times, want 1", base.leadCalls)
		}
		if got := outer.served[0].Candidates[0]; got.LeadPassage != richLeadText || got.Title != "Harbour ferry crews begin stoppage" || got.TotalPassages != 4 || got.PassageOrdinal == nil || *got.PassageOrdinal != 2 {
			t.Fatalf("composed candidate missing 0.21 rich fields or lead: %+v", got)
		}
	})
}
