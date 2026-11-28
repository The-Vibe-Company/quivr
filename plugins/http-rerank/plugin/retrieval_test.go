package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

var candidates = []quivrplugin.Candidate{
	{SegmentID: "a", PartKey: "title", Text: "Headline", PassageText: strings.Repeat("é", 210), Score: 20},
	{SegmentID: "b", Text: "A different passage", Score: 14},
	{SegmentID: "c", Text: "Another document", Score: 10},
}

func searchRequest(endpoint, options string) *quivrplugin.SearchRequest {
	req := &quivrplugin.SearchRequest{
		Configuration: json.RawMessage(fmt.Sprintf(`{"endpoint":%q%s}`, endpoint, options)),
		Round:         2, Limit: 3,
		Served: []quivrplugin.ServedRequest{{
			Round: 1,
			Request: quivrplugin.CandidateRequest{
				Primitive: quivrplugin.PrimitiveProfile,
				Profile:   &quivrplugin.ProfileCandidates{Name: "core.retrieve/default", Limit: 50},
			},
			Candidates: candidates,
		}},
	}
	req.Query.Text = "search phrase"
	return req
}

func TestProfileRequest(t *testing.T) {
	for _, test := range []struct {
		options string
		profile string
		depth   int
	}{
		{"", "core.retrieve/default", 50},
		{`,"base_profile":"core.retrieve/deep","candidate_count":12`, "core.retrieve/deep", 12},
		{`,"candidate_count":100`, "core.retrieve/default", 50},
	} {
		req := searchRequest("https://rerank.example/rerank", test.options)
		req.Round = 1
		answer, err := New().Search(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		if len(answer.Requests) != 1 || answer.Requests[0].Primitive != quivrplugin.PrimitiveProfile ||
			answer.Requests[0].Profile == nil || answer.Requests[0].Profile.Name != test.profile ||
			answer.Requests[0].Profile.Limit != test.depth {
			t.Fatalf("expected profile %s with depth %d, got %+v", test.profile, test.depth, answer.Requests)
		}
	}
}

func TestProtocolsAndRanking(t *testing.T) {
	for _, test := range []struct {
		name, options, response, firstDoc string
		order                             []string
		scores                            []float64
	}{
		{
			"tei", `,"max_doc_chars":200`,
			`[{"index":2,"score":0.9},{"index":0,"score":0.1},{"index":1,"score":0.6}]`,
			"Headline\n\n" + strings.Repeat("é", 190),
			[]string{"c", "b", "a"}, []float64{0.9, 0.6, 0.1},
		},
		{
			"rrf", `,"ranking":"rrf"`,
			`[{"index":2,"score":0.9},{"index":0,"score":0.1},{"index":1,"score":0.6}]`,
			"Headline\n\n" + strings.Repeat("é", 210),
			[]string{"a", "c", "b"}, []float64{1.0/61 + 1.0/63, 1.0/61 + 1.0/63, 2.0 / 62},
		},
		{
			"cohere", `,"protocol":"cohere","model":"example-model","ranking":"linear","rerank_weight":0.25,"include_title":false`,
			`{"results":[{"index":2,"relevance_score":0.9},{"index":0,"relevance_score":0.1},{"index":1,"relevance_score":0.6}]}`,
			strings.Repeat("é", 210),
			[]string{"a", "b", "c"}, []float64{0.75, 0.45625, 0.25},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("RERANKER_API_KEY", "")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/rerank" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "" {
					t.Error("expected an unauthenticated JSON POST to /rerank")
				}
				var got any
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
					return
				}
				docs := []any{test.firstDoc, "A different passage", "Another document"}
				want := map[string]any{"query": "search phrase", "texts": docs, "truncate": true}
				if test.name == "cohere" {
					want = map[string]any{"query": "search phrase", "documents": docs, "model": "example-model", "top_n": float64(3)}
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("request payload = %v, want %v", got, want)
				}
				io.WriteString(w, test.response)
			}))
			defer server.Close()

			answer, err := New().Search(t.Context(), searchRequest(server.URL+"/rerank", test.options))
			if err != nil {
				t.Fatal(err)
			}
			if len(answer.Ranking) != len(test.order) {
				t.Fatalf("ranking = %+v, want %v", answer.Ranking, test.order)
			}
			for i, hit := range answer.Ranking {
				if hit.SegmentID != test.order[i] || math.Abs(hit.Score-test.scores[i]) > 1e-12 {
					t.Errorf("hit %d = %+v, want %s with score %g", i, hit, test.order[i], test.scores[i])
				}
			}
		})
	}
}

func TestServiceFailure(t *testing.T) {
	const key = "test-api-key"
	t.Setenv("RERANKER_API_KEY", key)
	for _, test := range []struct {
		name, cause, body string
		timeout, fallback bool
	}{
		{name: "timeout fallback", cause: "timed out", timeout: true, fallback: true},
		{name: "timeout retryable", cause: "timed out", timeout: true},
		{name: "HTTP error", cause: "HTTP 502", body: key},
		{name: "duplicate index", cause: "one valid score", body: `[{"index":0,"score":0},{"index":0,"score":1},{"index":2,"score":2}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+key {
					t.Error("missing expected Bearer authorization")
				}
				_, _ = io.Copy(io.Discard, r.Body)
				if test.timeout {
					<-r.Context().Done()
					return
				}
				if test.name == "HTTP error" {
					w.WriteHeader(http.StatusBadGateway)
				}
				io.WriteString(w, test.body)
			}))
			defer server.Close()
			req := searchRequest(server.URL+"/rerank", fmt.Sprintf(`,"timeout_ms":100,"fallback_on_error":%t`, test.fallback))
			req.Limit = 1
			r := New().(retriever)
			r.client.Transport = server.Client().Transport
			answer, err := r.Search(context.Background(), req)
			if test.fallback {
				if err != nil || len(answer.Ranking) != 1 {
					t.Fatalf("expected one fallback hit, got %+v, %v", answer, err)
				}
				hit := answer.Ranking[0]
				if hit.SegmentID != "a" || hit.Score != 20 || !strings.Contains(hit.Explanation, test.cause) || strings.Contains(hit.Explanation, key) {
					t.Fatal("fallback should preserve the top base hit and safely explain the timeout")
				}
			} else {
				searchErr, ok := err.(*quivrplugin.SearchError)
				if !ok || !searchErr.Retryable || !strings.Contains(searchErr.Message, test.cause) || strings.Contains(searchErr.Message, key) {
					t.Fatal("expected a retryable search error with a redacted service failure")
				}
			}
		})
	}
}

func TestEmptyShortlist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the reranker was called with an empty shortlist")
	}))
	defer server.Close()
	req := searchRequest(server.URL+"/rerank", "")
	req.Served[0].Candidates = nil
	answer, err := New().Search(t.Context(), req)
	if err != nil || answer.Ranking == nil || len(answer.Ranking) != 0 {
		t.Fatalf("expected empty ranking, got %+v, %v", answer, err)
	}
}

func TestInvalidConfiguration(t *testing.T) {
	for _, configuration := range []string{
		`{}`,
		`{"endpoint":"file:///rerank"}`,
		`{"endpoint":"http://:8080/rerank"}`,
		`{"endpoint":"http://host:invalid/rerank"}`,
		`{"endpoint":"http://host:65536/rerank"}`,
		`{"endpoint":"https://rerank.example/rerank","protocol":"cohere"}`,
	} {
		req := &quivrplugin.SearchRequest{Round: 1, Configuration: json.RawMessage(configuration)}
		_, err := New().Search(t.Context(), req)
		searchErr, ok := err.(*quivrplugin.SearchError)
		if !ok || searchErr.Retryable || searchErr.Code != "invalid_configuration" {
			t.Errorf("configuration %s: expected terminal configuration error, got %v", configuration, err)
		}
	}
}

func TestShortSearchBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the reranker was called with insufficient time to return a fallback")
	}))
	defer server.Close()
	req := searchRequest(server.URL+"/rerank", "")
	req.Budget = &quivrplugin.SearchBudget{RemainingTimeMS: 1}
	answer, err := New().Search(t.Context(), req)
	if err != nil || len(answer.Ranking) != 3 || answer.Ranking[0].SegmentID != "a" ||
		!strings.Contains(answer.Ranking[0].Explanation, "insufficient remaining search time") {
		t.Fatalf("expected base ranking with a budget explanation, got %+v, %v", answer, err)
	}
}

func TestPreserveCanonicalPassage(t *testing.T) {
	c := quivrplugin.Candidate{
		PartKey: "title", Text: "Headline", PassageText: "Headline\n\nPassage",
		SourceSeparator: "\n\n",
		SourceRanges:    []quivrplugin.SourceRange{{PartKey: "title"}, {PartKey: "body"}},
	}
	cfg := configuration{MaxDocChars: 200}
	if got := document(c, cfg); got != "Headline\n\nPassage" {
		t.Errorf("without extra title, document = %q, want the full canonical passage", got)
	}
	c.SourceRanges = c.SourceRanges[:1]
	c.PassageText = c.Text
	if got := document(c, cfg); got != "Headline" {
		t.Errorf("title-only document = %q, want its only passage text preserved", got)
	}
}

func TestBearerRequiresHTTPS(t *testing.T) {
	t.Setenv("RERANKER_API_KEY", "test-api-key")
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("sent a request to a plain HTTP endpoint with authentication configured")
	}))
	defer server.Close()
	req := searchRequest(server.URL+"/rerank", `,"fallback_on_error":false`)
	_, err := New().Search(t.Context(), req)
	searchErr, ok := err.(*quivrplugin.SearchError)
	if !ok || !searchErr.Retryable || searchErr.Message != "reranker authentication requires HTTPS" {
		t.Fatal("expected a safe error explaining the HTTPS requirement")
	}
}

func TestDiagnosticsCountersAndTimeoutPhases(t *testing.T) {
	t.Setenv("RERANKER_API_KEY", "")
	r := New().(retriever)

	// 1. Successful rerank counts request, success, and title-anchor formatted candidates.
	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		_, _ = io.WriteString(w, `[{"index":0,"score":0.9},{"index":1,"score":0.5},{"index":2,"score":0.1}]`)
	}))
	defer okServer.Close()
	if _, err := r.Search(t.Context(), searchRequest(okServer.URL+"/rerank", "")); err != nil {
		t.Fatal(err)
	}

	// 2. Budget timeout increments budget timeout + fallback counters.
	budgetReq := searchRequest(okServer.URL+"/rerank", "")
	budgetReq.Budget = &quivrplugin.SearchBudget{RemainingTimeMS: 1}
	if _, err := r.Search(t.Context(), budgetReq); err != nil {
		t.Fatal(err)
	}

	// 3. HTTP round-trip timeout increments request timeout + fallback counters.
	releaseHeader := make(chan struct{})
	slowHeaderServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		select {
		case <-req.Context().Done():
		case <-releaseHeader:
		}
	}))
	defer func() {
		close(releaseHeader)
		slowHeaderServer.Close()
	}()
	ans, err := r.Search(t.Context(), searchRequest(slowHeaderServer.URL+"/rerank", `,"timeout_ms":100,"fallback_on_error":true`))
	if err != nil || !strings.Contains(ans.Ranking[0].Explanation, timeoutPhaseHTTPRoundTrip) {
		t.Fatalf("http roundtrip timeout = %+v, %v; want phase %s", ans, err, timeoutPhaseHTTPRoundTrip)
	}

	// 4. Response body read timeout increments body read timeout + error counters when fallback is off.
	releaseBody := make(chan struct{})
	slowBodyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `[{"index":0`)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-req.Context().Done():
		case <-releaseBody:
		}
	}))
	defer func() {
		close(releaseBody)
		slowBodyServer.Close()
	}()
	_, err = r.Search(t.Context(), searchRequest(slowBodyServer.URL+"/rerank", `,"timeout_ms":100,"fallback_on_error":false`))
	if err == nil || !strings.Contains(err.Error(), timeoutPhaseResponseRead) {
		t.Fatalf("response read timeout err=%v; want phase %s", err, timeoutPhaseResponseRead)
	}

	got := r.Diagnostics()
	want := DiagnosticsSnapshot{
		Requests:              4,
		Succeeded:             1,
		TitleAnchorsFormatted: 3, // candidate "a" formatted in calls 1, 3, and 4 (skipped in budget call 2)
		Fallbacks:             2,
		Errors:                1,
		BudgetTimeouts:        1,
		RequestTimeouts:       1,
		BodyReadTimeouts:      1,
	}
	if got != want {
		t.Fatalf("diagnostics = %+v, want %+v", got, want)
	}
}
