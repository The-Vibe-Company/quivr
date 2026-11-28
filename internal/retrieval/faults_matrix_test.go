package retrieval_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/The-Vibe-Company/quivr/contracts"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/testutil/apicontract"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
	"gopkg.in/yaml.v3"
)

type faultKind string

const (
	faultNone         faultKind = "none"
	faultDown         faultKind = "down"
	faultSlowDeadline faultKind = "slow_deadline"
	faultIntermittent faultKind = "intermittent"
)

type faultController struct {
	kind  faultKind
	calls atomic.Int32
}

func (f *faultController) inject(ctx context.Context, name string) error {
	n := f.calls.Add(1)
	switch f.kind {
	case faultNone:
		return nil
	case faultDown:
		return errors.New(name + " unavailable")
	case faultSlowDeadline:
		<-ctx.Done()
		return ctx.Err()
	case faultIntermittent:
		if n == 1 {
			return errors.New(name + " transient outage")
		}
		return nil
	default:
		return nil
	}
}

type matrixRouting struct {
	fault *faultController
}

func (r *matrixRouting) Authorize(ctx context.Context, _ corpus.Scope, _ []string) error {
	return r.fault.inject(ctx, "routing")
}

func (r *matrixRouting) Generation(ctx context.Context, _, _ string) (content.Generation, error) {
	return content.Generation{
		ID:                       "gen_a",
		Collection:               "Search",
		ProfileVersion:           retrieval.ProfileVersion,
		SpaceID:                  "space",
		SourceNamespaceProjected: true,
		ItemKeywordsProjected:    true,
		MetadataProjected:        true,
		SpacesProjected:          true,
		Spaces:                   []content.GenerationSpace{{ID: "space", Role: content.SpaceServed, OwnerPluginID: "core.ingest"}},
	}, nil
}

type matrixRegistry struct {
	fault          *faultController
	vectorsMissing bool
}

func (r *matrixRegistry) VectorSpaces(ctx context.Context, _, _ string) (content.Generation, []content.SpaceCoverage, int64, error) {
	if err := r.fault.inject(ctx, "registry"); err != nil {
		return content.Generation{}, nil, 0, err
	}
	space := content.SpaceCoverage{
		GenerationRole: content.SpaceServed,
		OwnerPluginID:  "core.ingest",
		Segments:       3,
	}
	space.ID, space.Model, space.Metric, space.QueryModalities = "space", "example/e5", "cosine", []string{"text"}
	space.VectorSpace.Dimensions = 1
	if r.vectorsMissing {
		zero := int64(0)
		space.CoverageUnknown = true
		space.ServingSegments = &zero
	}
	return content.Generation{ID: "gen_a", SpaceID: "space"}, []content.SpaceCoverage{space}, 3, nil
}

type matrixEncoder struct {
	fault *faultController
}

func (*matrixEncoder) Owns(space string) bool { return space == "space" }

func (e *matrixEncoder) EncodeQuery(ctx context.Context, _, _, _ string) ([]float32, error) {
	if err := e.fault.inject(ctx, "encoder"); err != nil {
		return nil, err
	}
	return []float32{1}, nil
}

type matrixProjection struct {
	fault *faultController
}

func (*matrixProjection) Publish(context.Context, content.Generation, string, string, string, content.Version, content.Segmentation) error {
	return nil
}
func (*matrixProjection) PublishEmbeddings(context.Context, content.Generation, string, []content.EmbeddingData) error {
	return nil
}
func (p *matrixProjection) Search(ctx context.Context, _ []retrieval.Route, _ corpus.Scope, _ retrieval.Request) ([]content.Candidate, error) {
	if err := p.fault.inject(ctx, "projection"); err != nil {
		return nil, err
	}
	return []content.Candidate{
		{SegmentID: "vec-a", GenerationID: "gen_a", Score: 3},
		{SegmentID: "vec-b", GenerationID: "gen_a", Score: 2},
	}, nil
}

type matrixBaseline struct {
	content.BaselineRepository
	fault *faultController
}

func (b *matrixBaseline) Hydrate(ctx context.Context, _ corpus.Scope, cs []content.Candidate) (map[int]content.Located, error) {
	if err := b.fault.inject(ctx, "hydration"); err != nil {
		return nil, err
	}
	out := make(map[int]content.Located, len(cs))
	for i, c := range cs {
		h := content.Hydrated{
			GenerationID:   c.GenerationID,
			RecordID:       "rec-" + c.SegmentID,
			VersionID:      "ver-" + c.SegmentID,
			SegmentationID: "segm-" + c.SegmentID,
			TextSHA256:     content.Hash([]byte(segmentText)),
			Availability:   content.Availability{State: "retrieval_ready", Current: true, Searchable: true},
			Segment:        content.Segment{ID: c.SegmentID, PartKey: "body", End: len([]rune(segmentText))},
		}
		if strings.HasPrefix(c.SegmentID, "vec-") {
			h.EmbeddingID, h.SpaceID = "emb-"+c.SegmentID, "space"
		}
		out[i] = content.Located{Hydrated: h, Blob: content.Blob{Key: "blob-" + c.SegmentID}}
	}
	return out, nil
}

type matrixRouter struct {
	rerankerFault *faultController
}

func (matrixRouter) Profiles() []retrieval.Profile { return nil }

func (r matrixRouter) Resolve(name string) (retrieval.Ranker, string, bool) {
	baseManifest := plugins.Manifest{
		ID:            "core.retrieve",
		Version:       "1.0.0",
		Compatibility: plugins.Compatibility{PluginAPI: ">=0.13.0 <0.14.0"},
		Contributions: plugins.Contributions{Retrieval: &plugins.Retrieval{
			Profiles: map[string]plugins.RetrievalProfile{"default": {MaxLatencyMS: 500}},
		}},
	}
	deepManifest := plugins.Manifest{
		ID:            "example.rerank",
		Version:       "1.0.0",
		Compatibility: plugins.Compatibility{PluginAPI: ">=0.13.0 <0.14.0"},
		Requires:      []plugins.Requirement{{Plugin: "core.retrieve", Version: ">=1.0.0", Profiles: []string{"default"}}},
		Contributions: plugins.Contributions{Retrieval: &plugins.Retrieval{
			Profiles: map[string]plugins.RetrievalProfile{"deep": {MaxLatencyMS: 3000, MaxCostCents: 1}},
		}},
	}
	base := &profileRanker{
		m: baseManifest,
		round: func(ctx context.Context, q plugins.SearchRequest) ([]byte, error) {
			return passthrough(ctx, q)
		},
	}
	deep := &profileRanker{
		m: deepManifest,
		round: func(ctx context.Context, q plugins.SearchRequest) ([]byte, error) {
			if q.Round == 1 {
				return answer(map[string]any{"requests": []any{
					map[string]any{"primitive": "profile", "profile": map[string]any{
						"name":  "core.retrieve/default",
						"mode":  q.Query.Mode,
						"limit": q.Limit,
					}},
				}})
			}
			if err := r.rerankerFault.inject(ctx, "reranker"); err != nil {
				return nil, err
			}
			cands := q.Served[0].Candidates
			hits := make([]any, 0, len(cands))
			for i := len(cands) - 1; i >= 0; i-- {
				hits = append(hits, map[string]any{
					"segment_id":  cands[i].SegmentID,
					"score":       float64(len(cands) - i),
					"explanation": "reranked",
				})
			}
			return answer(map[string]any{"ranking": map[string]any{"hits": hits}})
		},
	}
	switch name {
	case "default", "core.retrieve/default":
		return base, "default", true
	case "deep", "example.rerank/deep":
		return deep, "deep", true
	default:
		return nil, "", false
	}
}

func openAPIDegradationReasons(t *testing.T) []string {
	t.Helper()
	var doc struct {
		Components struct {
			Schemas struct {
				SearchDegradation struct {
					Properties struct {
						Reason struct {
							Enum []string `yaml:"enum"`
						} `yaml:"reason"`
					} `yaml:"properties"`
				} `yaml:"SearchDegradation"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(contracts.OpenAPI(), &doc); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	enum := doc.Components.Schemas.SearchDegradation.Properties.Reason.Enum
	if len(enum) == 0 {
		t.Fatal("OpenAPI SearchDegradation.reason enum is empty")
	}
	return enum
}

type matrixSearchResponse struct {
	Code             string `json:"code"`
	RetrievalProfile struct {
		Name     string `json:"name"`
		Version  string `json:"version"`
		Degraded []struct {
			Reason    string   `json:"reason"`
			CorpusIDs []string `json:"corpus_ids"`
		} `json:"degraded"`
	} `json:"retrieval_profile"`
}

// assertDegradedContract enforces the biconditional HTTP degradation invariant:
// X-Quivr-Degraded=="true" iff retrieval_profile.degraded is non-empty and
// every reported reason is a member of the authoritative OpenAPI enum.
func assertDegradedContract(t *testing.T, w *httptest.ResponseRecorder, validReasons []string, wantStatus int, wantCode string, wantReasons []string) {
	t.Helper()
	if w.Code != wantStatus {
		t.Fatalf("HTTP status=%d body=%s; want %d", w.Code, w.Body.String(), wantStatus)
	}
	var body matrixSearchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response %q: %v", w.Body.String(), err)
	}
	hdrDegraded := w.Header().Get("X-Quivr-Degraded")
	hdrReason := w.Header().Get("X-Quivr-Degraded-Reason")

	if wantStatus != http.StatusOK {
		if wantCode != "" && body.Code != wantCode {
			t.Fatalf("error code=%q body=%s; want %q", body.Code, w.Body.String(), wantCode)
		}
		if hdrDegraded != "" || hdrReason != "" {
			t.Fatalf("non-200 response must not set degradation headers: X-Quivr-Degraded=%q X-Quivr-Degraded-Reason=%q", hdrDegraded, hdrReason)
		}
		return
	}

	degradedList := body.RetrievalProfile.Degraded
	isDegradedBody := len(degradedList) > 0
	isDegradedHeader := hdrDegraded == "true"
	if isDegradedHeader != isDegradedBody {
		t.Fatalf("degradation header/body mismatch: X-Quivr-Degraded=%q vs retrieval_profile.degraded=%+v", hdrDegraded, degradedList)
	}
	if !isDegradedBody {
		if hdrDegraded != "" || hdrReason != "" {
			t.Fatalf("undegraded 200 must omit degradation headers: X-Quivr-Degraded=%q X-Quivr-Degraded-Reason=%q", hdrDegraded, hdrReason)
		}
		if len(wantReasons) > 0 {
			t.Fatalf("expected degraded reasons %v, got undegraded response", wantReasons)
		}
		return
	}

	gotReasons := make([]string, 0, len(degradedList))
	for _, d := range degradedList {
		if !slices.Contains(validReasons, d.Reason) {
			t.Fatalf("degradation reason %q not in OpenAPI enum %v", d.Reason, validReasons)
		}
		if len(d.CorpusIDs) == 0 {
			t.Fatalf("degradation %+v missing corpus_ids", d)
		}
		gotReasons = append(gotReasons, d.Reason)
	}
	if wantHdr := strings.Join(gotReasons, ","); hdrReason != wantHdr {
		t.Fatalf("X-Quivr-Degraded-Reason=%q; want %q", hdrReason, wantHdr)
	}
	if !slices.Equal(gotReasons, wantReasons) {
		t.Fatalf("degraded reasons=%v; want %v", gotReasons, wantReasons)
	}
}

// Owns the 6-dependency x {down, slow>deadline, intermittent} x {lexical, semantic, hybrid}
// fault-injection matrix through the real HTTP API + OpenAPI contract validator,
// verifying exact status codes, recovery on intermittent faults, and the
// X-Quivr-Degraded == "true" <-> retrieval_profile.degraded biconditional.
func TestSearchFaultInjectionMatrixAndDegradedHeaderAccuracy(t *testing.T) {
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	validReasons := openAPIDegradationReasons(t)
	dependencies := []string{"routing", "registry", "encoder", "projection", "hydration", "reranker"}
	faultModes := []faultKind{faultDown, faultSlowDeadline, faultIntermittent}
	searchModes := []string{"lexical", "semantic", "hybrid"}

	const apiKey = "matrix-key"
	keys := map[string]corpus.Scope{
		apiKey: {Organization: "org_a", Actions: []string{"search:query", "content:read"}, Corpora: []string{"*"}},
	}

	for _, dep := range dependencies {
		for _, fault := range faultModes {
			for _, mode := range searchModes {
				name := dep + "/" + string(fault) + "/" + mode
				t.Run(name, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						routingFault := &faultController{kind: faultNone}
						registryFault := &faultController{kind: faultNone}
						encoderFault := &faultController{kind: faultNone}
						projectionFault := &faultController{kind: faultNone}
						hydrationFault := &faultController{kind: faultNone}
						rerankerFault := &faultController{kind: faultNone}

						switch dep {
						case "routing":
							routingFault.kind = fault
						case "registry":
							registryFault.kind = fault
						case "encoder":
							encoderFault.kind = fault
						case "projection":
							projectionFault.kind = fault
						case "hydration":
							hydrationFault.kind = fault
						case "reranker":
							rerankerFault.kind = fault
						}

						profile := "default"
						if dep == "reranker" {
							profile = "deep"
						}

						svc := retrieval.Service{
							Routing:        &matrixRouting{fault: routingFault},
							Registry:       &matrixRegistry{fault: registryFault},
							Spaces:         &matrixEncoder{fault: encoderFault},
							Projection:     &matrixProjection{fault: projectionFault},
							ProfilesRouter: matrixRouter{rerankerFault: rerankerFault},
							Content: content.Service{
								RecordStore: fakeRecords{},
								Baseline:    &matrixBaseline{fault: hydrationFault},
								Blobs:       fakeBlobs{},
							},
						}
						handler, err := httpapi.New(nil, content.Service{}, svc, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"))
						if err != nil {
							t.Fatal(err)
						}
						api := apicontract.Handler(t, handler)

						doSearch := func() *httptest.ResponseRecorder {
							req := httptest.NewRequest(http.MethodPost, "/v0/search", strings.NewReader(
								`{"query":"harbour","corpus_ids":["corpus_a"],"mode":"`+mode+`","profile":"`+profile+`","limit":5}`,
							))
							req.Header.Set("Authorization", "Bearer "+apiKey)
							req.Header.Set("Content-Type", "application/json")
							w := httptest.NewRecorder()
							api.ServeHTTP(w, req)
							return w
						}

						// Call 1: exercises the injected fault.
						w1 := doSearch()
						switch dep {
						case "encoder":
							if mode == "lexical" {
								// Lexical search never invokes the query vector encoder.
								assertDegradedContract(t, w1, validReasons, http.StatusOK, "", nil)
							} else {
								assertDegradedContract(t, w1, validReasons, http.StatusServiceUnavailable, "model_unavailable", nil)
							}
						case "reranker":
							wantReason := retrieval.DegradationRerankerUnavailable
							if fault == faultSlowDeadline {
								wantReason = retrieval.DegradationRerankerBudgetExceeded
							}
							assertDegradedContract(t, w1, validReasons, http.StatusOK, "", []string{wantReason})
						default:
							assertDegradedContract(t, w1, validReasons, http.StatusServiceUnavailable, "search_unavailable", nil)
						}

						// Call 2 under intermittent fault: proves full recovery without sticky degradation.
						if fault == faultIntermittent {
							w2 := doSearch()
							assertDegradedContract(t, w2, validReasons, http.StatusOK, "", nil)
						}
					})
				})
			}
		}
	}

	// Verify vectors_unavailable degradation across all 3 search modes and
	// compound degradation (vectors_unavailable + reranker_unavailable) on deep hybrid.
	t.Run("vectors_unavailable_and_compound_degradation", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			reg := &matrixRegistry{fault: &faultController{kind: faultNone}, vectorsMissing: true}
			rerankerFault := &faultController{kind: faultNone}
			svc := retrieval.Service{
				Routing:        &matrixRouting{fault: &faultController{kind: faultNone}},
				Registry:       reg,
				Spaces:         &matrixEncoder{fault: &faultController{kind: faultNone}},
				Projection:     &matrixProjection{fault: &faultController{kind: faultNone}},
				ProfilesRouter: matrixRouter{rerankerFault: rerankerFault},
				Content: content.Service{
					RecordStore: fakeRecords{},
					Baseline:    &matrixBaseline{fault: &faultController{kind: faultNone}},
					Blobs:       fakeBlobs{},
				},
			}
			handler, err := httpapi.New(nil, content.Service{}, svc, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"))
			if err != nil {
				t.Fatal(err)
			}
			api := apicontract.Handler(t, handler)

			call := func(mode, profile string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, "/v0/search", strings.NewReader(
					`{"query":"harbour","corpus_ids":["corpus_a"],"mode":"`+mode+`","profile":"`+profile+`","limit":5}`,
				))
				req.Header.Set("Authorization", "Bearer "+apiKey)
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				api.ServeHTTP(w, req)
				return w
			}

			// Lexical succeeds undegraded; semantic refuses 422; hybrid degrades with vectors_unavailable.
			assertDegradedContract(t, call("lexical", "default"), validReasons, http.StatusOK, "", nil)
			assertDegradedContract(t, call("semantic", "default"), validReasons, http.StatusUnprocessableEntity, "unsupported_search", nil)
			assertDegradedContract(t, call("hybrid", "default"), validReasons, http.StatusOK, "", []string{retrieval.DegradationVectorsUnavailable})

			// Compound degradation: vectors_unavailable + reranker_unavailable on deep hybrid.
			rerankerFault.kind = faultDown
			assertDegradedContract(t, call("hybrid", "deep"), validReasons, http.StatusOK, "", []string{
				retrieval.DegradationVectorsUnavailable,
				retrieval.DegradationRerankerUnavailable,
			})

			// After repair: undegraded 200 OK.
			reg.vectorsMissing = false
			rerankerFault.kind = faultNone
			assertDegradedContract(t, call("hybrid", "deep"), validReasons, http.StatusOK, "", nil)
		})
	})
}
