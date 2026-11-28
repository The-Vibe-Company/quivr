package pluginhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost"
)

func benchSearchManifest() *plugins.Manifest {
	return &plugins.Manifest{
		ID:      "bench.retriever",
		Version: "1.0.0",
		Compatibility: plugins.Compatibility{
			Engine:    ">=0.1.0 <0.3.0",
			PluginAPI: ">=0.21.0 <0.22.0",
		},
		Contributions: plugins.Contributions{
			Retrieval: &plugins.Retrieval{
				Profiles: map[string]plugins.RetrievalProfile{
					"default": {MaxLatencyMS: 2000, MaxCostCents: 10},
				},
				Limits: plugins.RetrievalLimits{
					MaxRounds:        3,
					MaxRequests:      4,
					MaxCandidates:    400,
					MaxResponseBytes: plugins.DefaultRetrievalMaxResponseBytes,
				},
			},
		},
	}
}

func benchCandidates(round, count int) []plugins.Candidate {
	const textPayload = "The core retrieval transport delivers candidate passages across rounds to a pinned plugin ranker, preserving canonical metadata, score provenance, and byte-exact JSON serialization for signed HTTP requests. "
	bodyText := strings.Repeat(textPayload, 4) // ~800 bytes
	meta := json.RawMessage(`{"author":"engineering","department":"platform","language":"en"}`)
	out := make([]plugins.Candidate, count)
	for i := range count {
		ord := i
		out[i] = plugins.Candidate{
			SegmentID:      fmt.Sprintf("seg-r%d-%04d", round, i),
			RecordID:       fmt.Sprintf("rec-%04d", i),
			VersionID:      fmt.Sprintf("ver-%04d", i),
			PartKey:        "body",
			Text:           bodyText,
			Start:          0,
			End:            len(bodyText),
			Score:          1.0 / float64(i+1),
			Title:          fmt.Sprintf("Architecture Document Section %04d", i),
			CommonMetadata: meta,
			PassageOrdinal: &ord,
			TotalPassages:  count,
		}
	}
	return out
}

func benchFirstRequest() plugins.SearchRequest {
	return plugins.SearchRequest{
		InvocationID:   "inv-bench-round-1",
		OrganizationID: "org-bench",
		Configuration:  json.RawMessage(`{"mode":"balanced"}`),
		Profile:        "default",
		Query:          plugins.SearchQuery{Text: "incremental search transcript serialization", Mode: "hybrid"},
		Limit:          20,
		Scope:          plugins.SearchScope{CorpusIDs: []string{"corpus-1", "corpus-2"}},
		Spaces: []plugins.SearchSpace{
			{
				ID:              "engine.text@1",
				Owner:           plugins.SpaceOwner{Kind: "engine"},
				Model:           "default",
				Dimensions:      768,
				Metric:          "cosine",
				Indexes:         []string{"text"},
				QueryModalities: []string{"text"},
				Role:            "served",
				Coverage:        plugins.SpaceCoverage{Segments: 10000, Total: 10000},
			},
		},
		Budget: &plugins.SearchBudget{RemainingTimeMS: 1800, RemainingCostCents: 9.5},
	}
}

func benchMarshalRequest(req plugins.SearchRequest) ([]byte, func(), error) {
	return marshalSearchRequest(req)
}

func BenchmarkMarshalSearchRequest(b *testing.B) {
	cases := []struct {
		name     string
		rounds   int
		perRound int
	}{
		{name: "1x150", rounds: 1, perRound: 150},
		{name: "2x200", rounds: 2, perRound: 200},
		{name: "3x150", rounds: 3, perRound: 150},
	}
	m := benchSearchManifest()
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			reqSpec := []plugins.CandidateRequest{{Primitive: plugins.PrimitiveBM25, QueryText: "incremental search transcript", K: tc.perRound}}
			allCands := make([][][]plugins.Candidate, tc.rounds)
			for r := range tc.rounds {
				allCands[r] = [][]plugins.Candidate{benchCandidates(r+1, tc.perRound)}
			}

			// Build prior rounds as they occurred before the current round,
			// warming each prior round's serialization once.
			baseSession := plugins.NewRetrievalSession(m, benchFirstRequest())
			for r := range tc.rounds - 1 {
				baseSession.Serve(fmt.Sprintf("inv-bench-round-%d", r+2), reqSpec, allCands[r])
				warmed, release, err := benchMarshalRequest(baseSession.Request())
				if err != nil {
					b.Fatal(err)
				}
				release()
				_ = warmed
			}

			lastIdx := tc.rounds - 1
			lastInv := fmt.Sprintf("inv-bench-round-%d", tc.rounds+1)

			// Verify wire size once outside the timed loop.
			probe := *baseSession
			probe.Serve(lastInv, reqSpec, allCands[lastIdx])
			sample, release, err := benchMarshalRequest(probe.Request())
			if err != nil {
				b.Fatal(err)
			}
			bodyBytes := len(sample)
			release()

			b.ReportAllocs()
			b.ResetTimer()
			var sink int
			for range b.N {
				s := *baseSession
				s.Serve(lastInv, reqSpec, allCands[lastIdx])
				body, release, err := benchMarshalRequest(s.Request())
				if err != nil {
					b.Fatal(err)
				}
				sink += len(body)
				release()
			}
			b.ReportMetric(float64(bodyBytes), "wire_B/op")
			if sink == 0 {
				b.Fatal("empty body")
			}
		})
	}
}

func makeBigSearchResponse(targetBytes int) []byte {
	type hit struct {
		SegmentID   string  `json:"segment_id"`
		Score       float64 `json:"score"`
		Explanation string  `json:"explanation"`
	}
	explanation := strings.Repeat("ranked candidate explanation with provenance tokens and lexical match summary ", 10)
	var hits []hit
	for i := 0; ; i++ {
		hits = append(hits, hit{
			SegmentID:   fmt.Sprintf("seg-r1-%04d", i),
			Score:       1.0 / float64(i+1),
			Explanation: explanation,
		})
		if i%20 == 19 {
			raw, _ := json.Marshal(map[string]any{"ranking": map[string]any{"hits": hits}})
			if len(raw) >= targetBytes {
				return raw
			}
		}
	}
}

func BenchmarkInvokeSearchBigResponse(b *testing.B) {
	payload := makeBigSearchResponse(270 * 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	reqBody := []byte(`{"invocation_id":"inv-1","round":2}`)
	noopCheck := func([]byte) []plugins.Issue { return nil }
	ctx := context.Background()

	// Warm connection pool.
	res, err := devhost.InvokeSearch(ctx, srv.URL, reqBody, plugins.DefaultRetrievalMaxResponseBytes, noopCheck)
	if err != nil || len(res.Body) != len(payload) {
		b.Fatalf("warmup failed: res=%v err=%v", res, err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	var sink int
	for range b.N {
		res, err := devhost.InvokeSearch(ctx, srv.URL, reqBody, plugins.DefaultRetrievalMaxResponseBytes, noopCheck)
		if err != nil {
			b.Fatal(err)
		}
		sink += len(res.Body)
	}
	b.ReportMetric(float64(len(payload)), "resp_B/op")
	if sink == 0 {
		b.Fatal("empty response")
	}
}

func BenchmarkInvokeSearchSmallResponse(b *testing.B) {
	// ~463 KB request, tiny response (matches scout baseline probe).
	m := benchSearchManifest()
	s := plugins.NewRetrievalSession(m, benchFirstRequest())
	reqSpec := []plugins.CandidateRequest{{Primitive: plugins.PrimitiveBM25, QueryText: "harbour", K: 200}}
	s.Serve("inv-2", reqSpec, [][]plugins.Candidate{benchCandidates(1, 200)})
	s.Serve("inv-3", reqSpec, [][]plugins.Candidate{benchCandidates(2, 200)})
	reqBody, err := json.Marshal(s.Request())
	if err != nil {
		b.Fatal(err)
	}

	smallResp := []byte(`{"requests":[{"primitive":"bm25","query_text":"harbour","k":10}]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(smallResp)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(smallResp)
	}))
	defer srv.Close()

	noopCheck := func([]byte) []plugins.Issue { return nil }
	ctx := context.Background()
	if _, err := devhost.InvokeSearch(ctx, srv.URL, reqBody, plugins.DefaultRetrievalMaxResponseBytes, noopCheck); err != nil {
		b.Fatalf("warmup: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	var sink int
	for range b.N {
		res, err := devhost.InvokeSearch(ctx, srv.URL, reqBody, plugins.DefaultRetrievalMaxResponseBytes, noopCheck)
		if err != nil {
			b.Fatal(err)
		}
		sink += len(res.Body)
	}
	b.ReportMetric(float64(len(reqBody)), "req_B/op")
	if sink == 0 {
		b.Fatal("empty response")
	}
}
