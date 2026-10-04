package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
	"github.com/The-Vibe-Company/quivr/tests/fakes/embedding"
)

// This test owns provider mapping; Contract Runner owns plugin protocol checks.
func TestProviderModesAndUsage(t *testing.T) {
	for _, format := range []string{"openai", "cohere"} {
		t.Run(format, func(t *testing.T) {
			fake := embedding.New()
			server := httptest.NewServer(fake)
			defer server.Close()
			c := testConfig(format, server.URL+"/")
			if format == "cohere" {
				c.Auth = "api-key"
			}
			c.QueryPrefix = "query: "
			c.DocumentPrefix = "passage: "
			var logs bytes.Buffer
			i := newIngester(c, "fake-key", slog.New(slog.NewJSONHandler(&logs, nil)))
			got, err := i.SegmentAndEmbed(context.Background(), ingestRequest(c, "A library opens downtown.", true))
			if err != nil || len(got) != 1 || len(got[0].Vectors[c.spaceID()]) != 8 {
				t.Fatalf("segments %v, error %v", got, err)
			}
			vector, err := i.EmbedQuery(context.Background(), queryRequest(c, "A library opens downtown."))
			if err != nil || len(vector) != 8 {
				t.Fatalf("query %v, error %v", vector, err)
			}
			calls := fake.Calls()
			if len(calls) != 2 || calls[0].Texts[0] != "passage: A library opens downtown." || calls[1].Texts[0] != "query: A library opens downtown." {
				t.Fatalf("wrong model inputs: %+v", calls)
			}
			if format == "cohere" && (calls[0].InputType != "search_document" || calls[1].InputType != "search_query") {
				t.Fatalf("wrong modes: %+v", calls)
			}
			if calls[0].Model != "test-model" || calls[0].Dimensions != 8 || calls[0].Auth != c.Auth || calls[1].Auth != c.Auth {
				t.Fatalf("wrong model/dimension: %+v", calls)
			}
			var usage map[string]any
			if err := json.NewDecoder(&logs).Decode(&usage); err != nil || usage["input_tokens"] != float64(len(calls[0].Texts[0])) || usage["estimated"] != false {
				t.Fatalf("usage: %v %v", usage, err)
			}
			if strings.Contains(logs.String(), "fake-key") || strings.Contains(logs.String(), server.URL) {
				t.Fatal("usage leaked credentials or endpoint")
			}
		})
	}
}

func TestWindowsBoundInputsAndPreserveUnicodeOffsets(t *testing.T) {
	c := testConfig("openai", "http://127.0.0.1:9")
	c.MaxTokens = 60
	c.Overlap = 5
	c.DocumentPrefix = "passage: "
	text := "Bonjour 🌌. Une bibliothèque ouvre.\n\nDeuxième paragraphe et encore une phrase."
	i := newIngester(c, "", slog.Default())
	got, err := i.SegmentAndEmbed(context.Background(), ingestRequest(c, text, false))
	if err != nil {
		t.Fatal(err)
	}
	runes := []rune(text)
	covered := make([]bool, len(runes))
	for n, s := range got {
		input := c.DocumentPrefix + string(runes[s.Start:s.End])
		if len(input) > c.MaxTokens || !utf8.ValidString(input) || len(s.Vectors) != 0 {
			t.Fatalf("unbounded or invalid input: %+v", s)
		}
		for k := s.Start; k < s.End; k++ {
			covered[k] = true
		}
		if n > 0 && s.Start >= got[n-1].End {
			t.Fatalf("missing overlap: %+v", got)
		}
	}
	for k, ok := range covered {
		if !ok {
			t.Fatalf("code point %d was lost", k)
		}
	}
	if string(runes[:got[0].End]) != "Bonjour 🌌. Une bibliothèque ouvre.\n\n" {
		t.Fatalf("first window did not snap to paragraph end: %+v", got[0])
	}
}

func TestSpaceIdentityAndConfigurationBinding(t *testing.T) {
	c := testConfig("openai", "http://127.0.0.1:9")
	original := c.spaceID()
	for _, change := range []func(*configuration){func(c *configuration) { c.Model = "other" }, func(c *configuration) { c.Dimensions = 16 }, func(c *configuration) { c.QueryPrefix = "query: " }, func(c *configuration) { c.Revision = "2" }, func(c *configuration) { c.Metric = "dot" }} {
		other := c
		change(&other)
		if other.spaceID() == original {
			t.Fatal("changed semantics reused a space")
		}
	}
	manifest, err := c.manifest([]string{"hosted-embed"})
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/quivr-plugin.yaml"
	if err := os.WriteFile(path, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := quivrplugin.New(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = p.Ingestion(newIngester(c, "", slog.Default()))
	handler, err := p.Handler()
	if err != nil {
		t.Fatal(err)
	}
	req := ingestRequest(c, "hello", false)
	changed := c
	changed.Model = "other"
	req.Configuration, _ = json.Marshal(changed)
	body, _ := json.Marshal(req)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, httptest.NewRequest(http.MethodPost, "/v0/contributions/ingestion/segment_and_embed", bytes.NewReader(body)))
	if result.Code != 400 || !strings.Contains(result.Body.String(), "invalid_configuration") {
		t.Fatalf("drift accepted: %d %s", result.Code, result.Body.String())
	}
}

// Independently configured owners must coexist in one ingestion routing plan.
func TestConfiguredOwnersHaveIndependentManifestAndSpaceNamespaces(t *testing.T) {
	for _, id := range []string{"hosted.embed.pro", "hosted.embed.fast"} {
		raw := fmt.Sprintf(`{"plugin_id":%q,"format":"openai","base_url":"http://127.0.0.1:9","auth":"none","model":"example","dimensions":8}`, id)
		c, err := parseConfiguration([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := c.manifest([]string{"hosted-embed"})
		if err != nil {
			t.Fatal(err)
		}
		path := t.TempDir() + "/quivr-plugin.yaml"
		if err = os.WriteFile(path, manifest, 0600); err != nil {
			t.Fatal(err)
		}
		p, err := quivrplugin.New(path)
		if err != nil {
			t.Fatal(err)
		}
		if p.Manifest().ID != id || !strings.HasPrefix(c.spaceID(), id+".") {
			t.Fatalf("owner namespace: %s %s", p.Manifest().ID, c.spaceID())
		}
	}
	for _, id := range []string{"", "Core.Owner", "has spaces", strings.Repeat("a", 41)} {
		raw := fmt.Sprintf(`{"plugin_id":%q,"format":"openai","base_url":"http://127.0.0.1:9","auth":"none","model":"example","dimensions":8}`, id)
		if _, err := parseConfiguration([]byte(raw)); err == nil {
			t.Fatalf("invalid owner accepted: %q", id)
		}
	}
}

func TestRetryResumeAndRefusal(t *testing.T) {
	fake := embedding.New()
	server := httptest.NewServer(fake)
	defer server.Close()
	c := testConfig("openai", server.URL)
	c.BatchSize = 1
	c.MaxTokens = 18
	c.Overlap = 0
	c.MaxRetries = 1
	i := newIngester(c, "fake-key", slog.Default())
	req := ingestRequest(c, "aaaaaaaaaabbbbbbbbbbcccccccccc", true)
	fake.Enqueue(200, 429, 503)
	_, err := i.SegmentAndEmbed(context.Background(), req)
	if e, ok := err.(*quivrplugin.IngestError); !ok || !e.Retryable || e.Code != "embedding_incomplete" {
		t.Fatalf("want resumable error, got %v", err)
	}
	got, err := i.SegmentAndEmbed(context.Background(), req)
	if err != nil || len(got) != 3 {
		t.Fatalf("resume: %v %v", got, err)
	}
	calls := fake.Calls()
	if len(calls) != 5 || calls[3].Texts[0] != "bbbbbbbbbb" {
		t.Fatalf("re-embedded completed batch: %+v", calls)
	}
	fake.Enqueue(401)
	_, err = i.EmbedQuery(context.Background(), queryRequest(c, "denied"))
	if e, ok := err.(*quivrplugin.IngestError); !ok || e.Retryable {
		t.Fatalf("auth refusal must be terminal: %v", err)
	}
	fake.Enqueue(429, 200)
	if _, err = i.EmbedQuery(context.Background(), queryRequest(c, "recover")); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedProviderAnswers(t *testing.T) {
	for _, fault := range []string{"count", "dimensions", "duplicate_index", "zero", "overflow"} {
		t.Run(fault, func(t *testing.T) {
			fake := embedding.New()
			fake.Fault = fault
			server := httptest.NewServer(fake)
			defer server.Close()
			c := testConfig("openai", server.URL)
			i := newIngester(c, "fake-key", slog.Default())
			_, err := i.EmbedQuery(context.Background(), queryRequest(c, "hello"))
			if e, ok := err.(*quivrplugin.IngestError); !ok || !e.Retryable {
				t.Fatalf("bad vector accepted: %v", err)
			}
		})
	}
}

func testConfig(format, url string) configuration {
	c, err := parseConfiguration([]byte(`{"format":"` + format + `","base_url":"` + url + `","auth":"bearer","model":"test-model","dimensions":8}`))
	if err != nil {
		panic(err)
	}
	return c
}
func ingestRequest(c configuration, text string, embed bool) *quivrplugin.IngestRequest {
	raw, _ := json.Marshal(c)
	req := &quivrplugin.IngestRequest{InvocationID: "test", IdempotencyKey: "test", Contribution: "ingestion", OrganizationID: "org", Configuration: raw, Parts: []quivrplugin.IngestPart{{Key: "body", Role: "body", Text: text}}, Spaces: []string{}}
	req.Version.CorpusID = "corpus"
	req.Version.RecordID = "record"
	req.Version.RecordVersionID = "version"
	if embed {
		req.Spaces = []string{c.spaceID()}
	}
	return req
}
func queryRequest(c configuration, text string) *quivrplugin.QueryRequest {
	raw, _ := json.Marshal(c)
	r := &quivrplugin.QueryRequest{InvocationID: "query", Contribution: "ingestion", OrganizationID: "org", Configuration: raw, Space: c.spaceID()}
	r.Query.Modality = "text"
	r.Query.Text = text
	return r
}

// Pure parsing owns retry timing; no test waits for the wall clock.
func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		delay time.Duration
		valid bool
	}{{"0", 0, true}, {"120", 120 * time.Second, true}, {"Sat, 03 Oct 2026 10:00:03 GMT", 3 * time.Second, true}, {"Sat, 03 Oct 2026 09:59:00 GMT", 0, true}, {"-1", 0, false}, {"nonsense", 0, false}} {
		delay, valid := retryAfter(tc.value, now)
		if delay != tc.delay || valid != tc.valid {
			t.Fatalf("Retry-After %q: %v %v", tc.value, delay, valid)
		}
	}
}

func TestResumeCacheIsolatesOrganizationsAndQueryMode(t *testing.T) {
	fake := embedding.New()
	server := httptest.NewServer(fake)
	defer server.Close()
	c := testConfig("cohere", server.URL)
	i := newIngester(c, "fake-key", slog.Default())
	req := ingestRequest(c, "library", true)
	for _, org := range []string{"one", "one", "two"} {
		req.OrganizationID = org
		if _, err := i.SegmentAndEmbed(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := i.EmbedQuery(context.Background(), queryRequest(c, "library")); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 3 || calls[2].InputType != "search_query" {
		t.Fatalf("cache crossed organization or query mode: %+v", calls)
	}
}

func TestConfigAndContentRefusals(t *testing.T) {
	for _, raw := range []string{
		`{"format":"openai","base_url":"https://key@example.org","auth":"bearer","model":"m","dimensions":8}`,
		`{"format":"openai","base_url":"http://example.org","auth":"bearer","model":"m","dimensions":8,"api_key":"secret"}`,
		`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"overlap":512}`,
		`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"model_revision":"has spaces"}`,
	} {
		if _, err := parseConfiguration([]byte(raw)); err == nil {
			t.Fatal("accepted invalid configuration")
		}
	}
	c := testConfig("openai", "http://127.0.0.1:9")
	i := newIngester(c, "", slog.Default())
	for _, text := range []string{"", "invalid\x00text", strings.Repeat("a", (256<<10)+1)} {
		if _, err := i.SegmentAndEmbed(context.Background(), ingestRequest(c, text, false)); err == nil {
			t.Fatal("accepted invalid content")
		}
	}
	if _, err := i.EmbedQuery(context.Background(), queryRequest(c, strings.Repeat("q", c.MaxTokens))); err == nil {
		t.Fatal("oversize query accepted")
	}
}

// The configured package must be admissible for every accepted model/base URL,
// and must accept the same configuration the operator originally supplied.
func TestConfiguredManifestAcceptsOriginalConfiguration(t *testing.T) {
	for _, tc := range []struct{ name, model, url string }{{"numeric deployment", "3-model", "http://127.0.0.1:9/v1"}, {"trailing slash", "test-model", "http://127.0.0.1:9/v1/"}} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"format": "openai", "auth": "none", "model": tc.model, "dimensions": 8, "base_url": tc.url})
			c, err := parseConfiguration(raw)
			if err != nil {
				t.Fatal(err)
			}
			body, err := c.manifest([]string{"hosted-embed"})
			if err != nil {
				t.Fatal(err)
			}
			path := t.TempDir() + "/quivr-plugin.yaml"
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			p, err := quivrplugin.New(path)
			if err != nil {
				t.Fatalf("accepted configuration generated inadmissible manifest: %v", err)
			}
			_ = p.Ingestion(newIngester(c, "", slog.Default()))
			handler, err := p.Handler()
			if err != nil {
				t.Fatal(err)
			}
			req := ingestRequest(c, "hello", false)
			req.Configuration = raw
			request, _ := json.Marshal(req)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("POST", "/v0/contributions/ingestion/segment_and_embed", bytes.NewReader(request)))
			if response.Code != 200 {
				t.Fatalf("original accepted config rejected: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestManifestRequiresCredentialOnlyForAuthenticatedProviders(t *testing.T) {
	for _, auth := range []string{"bearer", "api-key", "none"} {
		c := testConfig("openai", "http://127.0.0.1:9")
		c.Auth = auth
		body, err := c.manifest([]string{"hosted-embed"})
		if err != nil {
			t.Fatal(err)
		}
		var manifest struct {
			Secrets []struct {
				Required bool `json:"required"`
			} `json:"secrets"`
		}
		if err := json.Unmarshal(body, &manifest); err != nil {
			t.Fatal(err)
		}
		if len(manifest.Secrets) != 1 || manifest.Secrets[0].Required != (auth != "none") {
			t.Fatalf("auth %s has wrong required credential: %s", auth, body)
		}
	}
}

// Document and query calls share one provider cap, including outstanding
// retries. Cancellation must release admission without leaking a request.
func TestProviderCapsConcurrentCalls(t *testing.T) {
	fake := embedding.New()
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-release; fake.ServeHTTP(w, r) }))
	defer func() { releaseAll(); server.Close() }()
	c := testConfig("openai", server.URL)
	c.MaxConcurrentRequests = 2
	i := newIngester(c, "fake-key", slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var group sync.WaitGroup
	errs := make(chan error, 6)
	for n := 0; n < 6; n++ {
		group.Add(1)
		go func(n int) {
			defer group.Done()
			_, err := i.EmbedQuery(ctx, queryRequest(c, fmt.Sprint("query ", n)))
			errs <- err
		}(n)
	}
	for n := 0; n < 2; n++ {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("configured provider slots did not overlap")
		}
	}
	waiter, stop := context.WithTimeout(ctx, time.Millisecond)
	_, waitErr := i.EmbedQuery(waiter, queryRequest(c, "cancelled waiter"))
	stop()
	if waitErr == nil {
		t.Fatal("waiter unexpectedly succeeded")
	}
	select {
	case <-entered:
		t.Fatal("provider cap exceeded")
	default:
	}
	releaseAll()
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.Calls()) != 6 {
		t.Fatalf("completed calls %d, want 6", len(fake.Calls()))
	}
}

// A Retry-After discovered by one call also fences subsequent calls. A
// cancelled waiter makes no provider attempt; no wall-clock sleep is needed.
func TestProviderSharesThrottleAcrossCalls(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
	}))
	defer server.Close()
	c := testConfig("openai", server.URL)
	c.MaxRetries = 0
	i := newIngester(c, "fake-key", slog.New(slog.DiscardHandler))
	_, err := i.EmbedQuery(t.Context(), queryRequest(c, "first"))
	if err == nil {
		t.Fatal("429 succeeded")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	_, err = i.EmbedQuery(ctx, queryRequest(c, "second"))
	if err == nil || calls.Load() != 1 {
		t.Fatalf("throttled waiter made a provider call: calls=%d error=%v", calls.Load(), err)
	}
}
