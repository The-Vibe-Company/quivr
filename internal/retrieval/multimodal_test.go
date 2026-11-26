package retrieval_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

type multimodalBaseline struct {
	content.BaselineRepository
}

func (multimodalBaseline) Hydrate(_ context.Context, _ corpus.Scope, cs []content.Candidate) (map[int]content.Located, error) {
	out := map[int]content.Located{}
	for i, c := range cs {
		if c.SegmentID == "img-seg-1" {
			out[i] = content.Located{
				Hydrated: content.Hydrated{
					GenerationID:   c.GenerationID,
					RecordID:       "rec-img-1",
					VersionID:      "ver-img-1",
					SegmentationID: "seg-img-1",
					EmbeddingID:    "emb-img-1",
					SpaceID:        "crossmodal.image.vision@1",
					TextSHA256:     content.Hash(nil),
					Segment: content.Segment{
						ID:      "img-seg-1",
						PartKey: "image",
						Start:   0,
						End:     0,
						Derivation: content.SegmentDerivation{
							Region: &content.ImageRegion{
								CoordinateSystem: "normalized_bbox",
								X:                0,
								Y:                0,
								Width:            0.5,
								Height:           0.5,
								ImageWidth:       640,
								ImageHeight:      480,
								Label:            "grid:0,0",
							},
						},
					},
				},
			}
			continue
		}
		out[i] = content.Located{
			Hydrated: content.Hydrated{
				GenerationID:   c.GenerationID,
				RecordID:       "rec-txt-1",
				VersionID:      "ver-txt-1",
				SegmentationID: "seg-txt-1",
				EmbeddingID:    "emb-txt-1",
				SpaceID:        "core.ingest.e5-small@1",
				TextSHA256:     content.Hash([]byte(segmentText)),
				Segment: content.Segment{
					ID:      c.SegmentID,
					PartKey: "body",
					Start:   0,
					End:     len([]rune(segmentText)),
				},
			},
			Blob: content.Blob{Key: "blob-txt-1"},
		}
	}
	return out, nil
}

type multimodalSpaces struct {
	imageServing int64
}

func (m multimodalSpaces) VectorSpaces(context.Context, string, string) (content.Generation, []content.SpaceCoverage, int64, error) {
	textServing := int64(2)
	textSpace := content.SpaceCoverage{
		GenerationRole:  content.SpaceServed,
		OwnerPluginID:   "core.ingest",
		Segments:        2,
		ServingSegments: &textServing,
	}
	textSpace.ID = "core.ingest.e5-small@1"
	textSpace.Model = "e5-small"
	textSpace.Metric = "cosine"
	textSpace.Indexes = []string{"text"}
	textSpace.QueryModalities = []string{"text"}
	textSpace.VectorSpace.Dimensions = 2

	imgServing := m.imageServing
	imgSpace := content.SpaceCoverage{
		GenerationRole:  content.SpaceServed,
		OwnerPluginID:   "crossmodal.image",
		Segments:        m.imageServing,
		ServingSegments: &imgServing,
	}
	imgSpace.ID = "crossmodal.image.vision@1"
	imgSpace.Model = "crossmodal/vision-text-512"
	imgSpace.Metric = "cosine"
	imgSpace.Indexes = []string{"image"}
	imgSpace.QueryModalities = []string{"text"}
	imgSpace.VectorSpace.Dimensions = 2

	return content.Generation{}, []content.SpaceCoverage{textSpace, imgSpace}, 2 + m.imageServing, nil
}

type recordingEncoder struct {
	called []string
}

func (e *recordingEncoder) Owns(space string) bool {
	return space == "core.ingest.e5-small@1" || space == "crossmodal.image.vision@1"
}

func (e *recordingEncoder) EncodeQuery(_ context.Context, _, space, _ string) ([]float32, error) {
	e.called = append(e.called, space)
	return []float32{0.6, 0.8}, nil
}

type multimodalRanker struct {
	apiRange string
	served   []plugins.ServedRequest
}

func (r *multimodalRanker) Manifest() *plugins.Manifest {
	return &plugins.Manifest{
		ID:            "example.multimodal.retrieve",
		Version:       "1.0.0",
		Compatibility: plugins.Compatibility{Engine: ">=0.1.0 <0.3.0", PluginAPI: r.apiRange},
		Contributions: plugins.Contributions{
			Retrieval: &plugins.Retrieval{
				Profiles: map[string]plugins.RetrievalProfile{"default": {MaxLatencyMS: 2000}},
				Limits:   plugins.RetrievalLimits{MaxRounds: 3, MaxRequests: 4, MaxCandidates: 50},
			},
		},
	}
}
func (r *multimodalRanker) Configuration() json.RawMessage { return json.RawMessage(`{}`) }
func (r *multimodalRanker) Round(_ context.Context, q plugins.SearchRequest) ([]byte, error) {
	if q.Round == 1 {
		var reqs []map[string]any
		for _, sp := range q.Spaces {
			if sp.Role != content.SpaceServed {
				continue
			}
			if len(sp.Indexes) > 0 && !slices.Contains(sp.Indexes, "text") {
				reqs = append(reqs, map[string]any{"primitive": "near_vector", "space": sp.ID, "query_text": q.Query.Text, "k": q.Limit})
			} else {
				reqs = append(reqs, map[string]any{"primitive": "hybrid", "space": sp.ID, "query_text": q.Query.Text, "alpha": 0.5, "fusion": "relative_score", "k": q.Limit})
			}
		}
		return answer(map[string]any{"requests": reqs})
	}
	r.served = q.Served
	var hits []map[string]any
	for _, s := range q.Served {
		for _, c := range s.Candidates {
			hits = append(hits, map[string]any{"segment_id": c.SegmentID, "score": c.Score})
		}
	}
	return answer(map[string]any{"ranking": map[string]any{"hits": hits}})
}

type spaceProjection struct {
	bySpace map[string][]content.Candidate
}

func (p *spaceProjection) Publish(context.Context, content.Generation, string, string, string, content.Version, content.Segmentation) error {
	return nil
}
func (p *spaceProjection) PublishEmbeddings(context.Context, content.Generation, string, []content.EmbeddingData) error {
	return nil
}
func (p *spaceProjection) Search(_ context.Context, _ []retrieval.Route, _ corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	return p.bySpace[q.Space], nil
}

// Natural-language search across a Corpus with both text and image spaces
// hydrates exact ImageRegion provenance for image hits without reading S3 for
// region-only segments, while a text-only Corpus never invokes the cross-modal
// image query encoder.
func TestCrossModalImageSearchAndTextOnlyIsolation(t *testing.T) {
	gen := content.Generation{
		ID:                       "gen-mm",
		Collection:               "Shared",
		ProfileVersion:           retrieval.ProfileVersion,
		SpaceID:                  "core.ingest.e5-small@1",
		SourceNamespaceProjected: true,
		SpacesProjected:          true,
		Spaces: []content.GenerationSpace{
			{ID: "core.ingest.e5-small@1", Role: content.SpaceServed, OwnerPluginID: "core.ingest"},
			{ID: "crossmodal.image.vision@1", Role: content.SpaceServed, OwnerPluginID: "crossmodal.image"},
		},
	}

	t.Run("multimodal corpus returns hydrated image region", func(t *testing.T) {
		for _, tc := range []struct {
			name             string
			apiRange         string
			wantCandidateReg bool
		}{
			{name: "0.20 ranker receives region on candidate", apiRange: ">=0.20.0 <0.21.0", wantCandidateReg: true},
			{name: "older ranker omits region on candidate", apiRange: ">=0.7.0 <0.20.0", wantCandidateReg: false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				enc := &recordingEncoder{}
				ranker := &multimodalRanker{apiRange: tc.apiRange}
				proj := &spaceProjection{bySpace: map[string][]content.Candidate{
					"core.ingest.e5-small@1":    {{SegmentID: "txt-seg-1", GenerationID: gen.ID, Score: 0.7}},
					"crossmodal.image.vision@1": {{SegmentID: "img-seg-1", GenerationID: gen.ID, Score: 0.95}},
				}}
				svc := retrieval.Service{
					Routing:    spaceRouting{"corpus": gen},
					Registry:   multimodalSpaces{imageServing: 1},
					Spaces:     enc,
					Projection: proj,
					Ranker:     ranker,
					Content:    content.Service{Baseline: multimodalBaseline{}, Blobs: fakeBlobs{}},
				}

				res, err := svc.Search(context.Background(), searchScope, retrieval.Request{Query: "red beacon top left", CorpusIDs: []string{"corpus"}, Profile: "default", Mode: "hybrid", Limit: 5})
				if err != nil {
					t.Fatalf("Search: %v", err)
				}
				if len(res.Hits) != 2 {
					t.Fatalf("got %d hits, want 2", len(res.Hits))
				}
				var imgHit *retrieval.Hit
				var txtHit *retrieval.Hit
				for i := range res.Hits {
					if res.Hits[i].Segment.ID == "img-seg-1" {
						imgHit = &res.Hits[i]
					} else {
						txtHit = &res.Hits[i]
					}
				}
				if imgHit == nil || imgHit.Segment.Derivation.Region == nil || imgHit.Segment.Derivation.Region.Label != "grid:0,0" || imgHit.Segment.Text != "" {
					t.Fatalf("unexpected image hit: %+v", imgHit)
				}
				if txtHit == nil || txtHit.Segment.Derivation.Region != nil || txtHit.Segment.Text != segmentText {
					t.Fatalf("unexpected text hit: %+v", txtHit)
				}
				var sawImgCandidate bool
				for _, s := range ranker.served {
					for _, c := range s.Candidates {
						if c.SegmentID == "img-seg-1" {
							sawImgCandidate = true
							if (c.Region != nil) != tc.wantCandidateReg {
								t.Fatalf("candidate region present = %v, want %v", c.Region != nil, tc.wantCandidateReg)
							}
						}
					}
				}
				if !sawImgCandidate {
					t.Fatal("image candidate was not served to ranker")
				}
			})
		}
	})

	t.Run("text-only corpus never calls cross-modal image encoder", func(t *testing.T) {
		enc := &recordingEncoder{}
		ranker := &multimodalRanker{apiRange: ">=0.20.0 <0.21.0"}
		proj := &spaceProjection{bySpace: map[string][]content.Candidate{
			"core.ingest.e5-small@1": {{SegmentID: "txt-seg-1", GenerationID: gen.ID, Score: 0.9}},
		}}
		svc := retrieval.Service{
			Routing:    spaceRouting{"corpus": gen},
			Registry:   multimodalSpaces{imageServing: 0},
			Spaces:     enc,
			Projection: proj,
			Ranker:     ranker,
			Content:    content.Service{Baseline: multimodalBaseline{}, Blobs: fakeBlobs{}},
		}
		res, err := svc.Search(context.Background(), searchScope, retrieval.Request{Query: "lanterne", CorpusIDs: []string{"corpus"}, Profile: "default", Mode: "hybrid", Limit: 5})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(res.Hits) != 1 || res.Hits[0].Segment.ID != "txt-seg-1" || res.Hits[0].Segment.Derivation.Region != nil {
			t.Fatalf("unexpected text-only hits: %+v", res.Hits)
		}
		if slices.Contains(enc.called, "crossmodal.image.vision@1") {
			t.Fatalf("text-only search unexpectedly called cross-modal encoder: %v", enc.called)
		}
	})

	// Sanity guard: text segment with missing blob key still attempts a blob read
	// and fails closed rather than taking the region-only shortcut.
	t.Run("text segment without region fails closed on missing blob key", func(t *testing.T) {
		missingBlob := missingBlobBaseline{}
		blobs := &missingKeyBlobs{}
		svc := content.Service{Baseline: missingBlob, Blobs: blobs}
		_, err := svc.Hydrate(context.Background(), searchScope, []content.Candidate{{SegmentID: "txt-1", GenerationID: "gen"}})
		if err == nil {
			t.Fatal("expected error reading missing text blob")
		}
		if !blobs.sawEmptyKey {
			t.Fatal("expected Blobs.Read to be called with empty key")
		}
	})
}

type missingBlobBaseline struct{ content.BaselineRepository }

func (missingBlobBaseline) Hydrate(_ context.Context, _ corpus.Scope, _ []content.Candidate) (map[int]content.Located, error) {
	return map[int]content.Located{
		0: {Hydrated: content.Hydrated{Segment: content.Segment{ID: "txt-1", PartKey: "body", End: 4}, TextSHA256: content.Hash([]byte("text"))}},
	}, nil
}

type missingKeyBlobs struct {
	content.Blobs
	sawEmptyKey bool
}

func (b *missingKeyBlobs) Read(_ context.Context, blob content.Blob) ([]byte, error) {
	if blob.Key == "" {
		b.sawEmptyKey = true
		return nil, errors.New("missing blob key")
	}
	return []byte("text"), nil
}
