package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"unicode"
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
	request := httptest.NewRequest(http.MethodPost, "/v0/contributions/ingestion/segment_and_embed", bytes.NewReader(body))
	signTestRequest(t, c.PluginID, request, body)
	handler.ServeHTTP(result, request)
	if result.Code != 400 || !strings.Contains(result.Body.String(), "invalid_configuration") {
		t.Fatalf("drift accepted: %d %s", result.Code, result.Body.String())
	}
}

func signTestRequest(t *testing.T, pluginID string, request *http.Request, body []byte) {
	t.Helper()
	secret := []byte("fixture-signing-secret-for-hosted-contract")
	ring, _ := json.Marshal(map[string]any{"active": "test", "keys": []any{map[string]string{"id": "test", "secret": base64.RawURLEncoding.EncodeToString(secret)}}})
	t.Setenv(quivrplugin.EnvSigningKeys, string(ring))
	digest := sha256.Sum256(body)
	now := time.Now().Unix()
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "quivr-engine+jwt", "kid": "test"})
	claims, _ := json.Marshal(map[string]any{"aud": pluginID, "plugin_id": pluginID, "contribution": "ingestion", "method": request.Method, "target": request.URL.RequestURI(), "iat": now, "exp": now + 60, "body_sha256": hex.EncodeToString(digest[:])})
	encoded := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(encoded))
	request.Header.Set("Authorization", "Bearer "+encoded+"."+base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
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
	c, err := parseConfiguration([]byte(`{"packing":"none","plugin_version":"1.0.0","format":"` + format + `","base_url":"` + url + `","auth":"bearer","model":"test-model","dimensions":8}`))
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
	// Literal external keys and omission protect the configuration contract.
	for _, tc := range []struct {
		raw  string
		wait int
	}{
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8}`, 25},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"batch_wait_ms":0}`, 0},
	} {
		c, err := parseConfiguration([]byte(tc.raw))
		if err != nil || c.BatchWaitMS != tc.wait {
			t.Fatalf("batch collection default: %d %v", c.BatchWaitMS, err)
		}
	}
	for _, tc := range []struct{ raw, reason string }{
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"tokenizer":{"python":"bad\u0000path","model":"local.json","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`, "tokenizer requires"},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"tokenizer":{"python":"python3","model":"bad\u0000path","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`, "tokenizer requires"},

		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"batch_wait_ms":-1}`, "batch_wait_ms must be between 0 and 100"},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"batch_wait_ms":101}`, "batch_wait_ms must be between 0 and 100"},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"batch_wait_ms":100,"call_budget_ms":100}`, "batch_wait_ms must be less than call_budget_ms"},
		{`{"format":"openai","base_url":"https://key@example.org","auth":"bearer","model":"m","dimensions":8}`, "base_url must be an HTTP(S) base without credentials"},
		{`{"format":"openai","base_url":"http://example.org","auth":"bearer","model":"m","dimensions":8,"api_key":"secret"}`, "configuration must contain only declared fields"},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"overlap":512}`, "invalid segment, overlap or batch limits"},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"model_revision":"has spaces"}`, "model and model_revision must be nonempty and bounded"},
	} {
		if _, err := parseConfiguration([]byte(tc.raw)); err == nil || !strings.Contains(err.Error(), tc.reason) {
			t.Errorf("configuration %s: got %v, want reason %q", tc.raw, err, tc.reason)
		}
	}
	c := testConfig("openai", "http://127.0.0.1:9")
	i := newIngester(c, "", slog.Default())
	for _, tc := range []struct{ text, code string }{
		{"", "no_indexable_text"}, {"invalid\x00text", "invalid_text"}, {strings.Repeat("a", (256<<10)+1), "segmentation_limit"},
	} {
		_, err := i.SegmentAndEmbed(context.Background(), ingestRequest(c, tc.text, false))
		var refusal *quivrplugin.IngestError
		if !errors.As(err, &refusal) || refusal.Code != tc.code || refusal.Retryable {
			t.Errorf("content: got %v, want terminal %s", err, tc.code)
		}
	}
	_, err := i.EmbedQuery(context.Background(), queryRequest(c, strings.Repeat("q", c.MaxTokens)))
	var refusal *quivrplugin.IngestError
	if !errors.As(err, &refusal) || refusal.Code != "query_limit" || refusal.Retryable {
		t.Errorf("oversize query: got %v, want terminal query_limit", err)
	}
}

// The configured package must be admissible for every accepted model/base URL,
// and must accept the same configuration the operator originally supplied.
func TestConfiguredManifestAcceptsOriginalConfiguration(t *testing.T) {
	for _, tc := range []struct{ name, model, url string }{{"numeric deployment", "3-model", "http://127.0.0.1:9/v1"}, {"trailing slash", "test-model", "http://127.0.0.1:9/v1/"}, {"empty title context", "test-model", "http://127.0.0.1:9/v1"}} {
		t.Run(tc.name, func(t *testing.T) {
			settings := map[string]any{"format": "openai", "auth": "none", "model": tc.model, "dimensions": 8, "base_url": tc.url}
			if tc.name == "empty title context" {
				settings["title_context_parts"] = []string{}
			}
			raw, _ := json.Marshal(settings)
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
			signed := httptest.NewRequest("POST", "/v0/contributions/ingestion/segment_and_embed", bytes.NewReader(request))
			signTestRequest(t, c.PluginID, signed, request)
			handler.ServeHTTP(response, signed)
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

// Owns cross-Version coalescing and result routing at the ingestion boundary.
// Existing provider tests own wire modes, and retry tests own resume caching.
func TestConcurrentVersionsShareBoundedProviderBatches(t *testing.T) {
	var mu sync.Mutex
	var batches [][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		batches = append(batches, req.Input)
		mu.Unlock()
		data := []map[string]any{}
		for n, text := range req.Input {
			var id int
			fmt.Sscanf(text, "bulletin %d", &id)
			data = append(data, map[string]any{"index": n, "embedding": []float32{float32(id + 1), 1, 0, 0, 0, 0, 0, 0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	c := testConfig("openai", server.URL)
	c.BatchSize = 16
	c.MaxConcurrentRequests = 1
	i := newIngester(c, "fake-key", slog.Default())
	start := make(chan struct{})
	var workers sync.WaitGroup
	for n := range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req := ingestRequest(c, fmt.Sprintf("bulletin %d", n), true)
			req.InvocationID = fmt.Sprintf("inv-%d", n)
			req.Version.RecordID = fmt.Sprintf("record-%d", n)
			req.Version.RecordVersionID = fmt.Sprintf("version-%d", n)
			got, err := i.SegmentAndEmbed(ctx, req)
			if err != nil || len(got) != 1 {
				t.Errorf("Version %d: %v %v", n, got, err)
				return
			}
			if v := got[0].Vectors[c.spaceID()]; len(v) != 8 || v[0] != float32(n+1) {
				t.Errorf("Version %d received another vector: %v", n, v)
			}
		}()
	}
	close(start)
	workers.Wait()
	mu.Lock()
	defer mu.Unlock()
	shared := false
	for _, batch := range batches {
		if len(batch) > c.BatchSize {
			t.Fatalf("provider input limit exceeded: %d", len(batch))
		}
		if len(batch) > 1 {
			shared = true
		}
	}
	if !shared {
		t.Fatalf("all %d provider calls contain one Version; archive latency is paid per Version", len(batches))
	}
}

// The queue boundary owns cancellation, tenant isolation and poisoning of a
// shared call. Gate admission is held by a channel, never a wall-clock wait.
func TestDocumentBatchQueueIsolatesCancellationAndRefusal(t *testing.T) {
	var mu sync.Mutex
	var batches [][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		batches = append(batches, req.Input)
		mu.Unlock()
		for _, input := range req.Input {
			if input == "deny" {
				w.WriteHeader(400)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"param": "input", "code": "invalid_input"}})
				return
			}
		}
		data := []map[string]any{}
		for n, input := range req.Input {
			data = append(data, map[string]any{"index": n, "embedding": []float32{float32(input[0]), 1, 0, 0, 0, 0, 0, 0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	c := testConfig("openai", server.URL)
	c.BatchSize = 3
	c.BatchTokens = 25
	c.MaxConcurrentRequests = 4
	c.BatchWaitMS = 10
	i := newIngester(c, "fake-key", slog.Default())
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	for range c.MaxConcurrentRequests {
		if err := i.provider.gate.acquire(ctx); err != nil {
			t.Fatal(err)
		}
	}
	admitted := true
	defer func() {
		if admitted {
			for range c.MaxConcurrentRequests {
				i.provider.gate.release()
			}
		}
	}()
	type queued struct {
		text   string
		result <-chan documentResult
	}
	var requests []queued
	canceled, cancel := context.WithCancel(ctx)
	for _, job := range []struct {
		org, text string
		ctx       context.Context
	}{
		{"one", "A", ctx}, {"one", "deny", ctx}, {"one", "E", ctx},
		{"two", "B", ctx}, {"two", "C", ctx},
		{"three", "cancel", canceled}, {"three", "D", ctx},
	} {
		result, err := i.documents.submit(job.ctx, job.org, []string{job.text}, job.text)
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, queued{job.text, result})
	}
	cancel()
	for range c.MaxConcurrentRequests {
		i.provider.gate.release()
	}
	admitted = false
	for _, job := range requests {
		select {
		case result := <-job.result:
			if job.text == "deny" {
				var refusal *quivrplugin.IngestError
				if !errors.As(result.err, &refusal) || refusal.Retryable {
					t.Fatalf("refusal: %v", result.err)
				}
			} else if job.text == "cancel" {
				if !errors.Is(result.err, context.Canceled) {
					t.Fatalf("cancellation: %v", result.err)
				}
			} else if result.err != nil || len(result.vectors) != 1 || result.vectors[0][0] != float32(job.text[0]) {
				t.Fatalf("healthy %s: %+v", job.text, result)
			}
		case <-ctx.Done():
			t.Fatal("queue did not settle")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, batch := range batches {
		org := ""
		tokens := 0
		for _, text := range batch {
			if text == "cancel" {
				t.Fatal("cancelled input reached provider")
			}
			group := map[string]string{"A": "one", "deny": "one", "E": "one", "B": "two", "C": "two", "D": "three"}[text]
			if org != "" && group != org {
				t.Fatalf("mixed Organizations: %v", batch)
			}
			org = group
			tokens += len(text) + specialTokens
		}
		if len(batch) > 3 || tokens > 25 {
			t.Fatalf("provider limits exceeded: %v", batch)
		}
	}
}

// Shared credential/endpoint failures belong to the provider boundary: one
// collected batch must fail once, without replaying its inputs through splits.
func TestDocumentBatchPropagatesGlobalProviderRefusalOnce(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 422} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				if status == 400 || status == 422 {
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"param": "model", "code": "invalid_model"}})
				}
			}))
			defer server.Close()
			c := testConfig("openai", server.URL)
			c.MaxConcurrentRequests = 1
			i := newIngester(c, "fake-key", slog.Default())
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := i.provider.gate.acquire(ctx); err != nil {
				t.Fatal(err)
			}
			results := make([]<-chan documentResult, 0, c.BatchSize)
			for range c.BatchSize {
				result, err := i.documents.submit(ctx, "org_a", []string{"document"}, "opaque")
				if err != nil {
					i.provider.gate.release()
					t.Fatal(err)
				}
				results = append(results, result)
			}
			i.provider.gate.release()
			for _, result := range results {
				select {
				case out := <-result:
					var refusal *quivrplugin.IngestError
					if !errors.As(out.err, &refusal) || refusal.Retryable || refusal.Code != "inference_refused" {
						t.Fatalf("global refusal: %v", out.err)
					}
				case <-ctx.Done():
					t.Fatal("global refusal did not settle")
				}
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("global HTTP %d refusal issued %d provider calls, want 1", status, got)
			}
		})
	}
}

func TestDocumentBatchDropsCancellationBeforeRefusalProbe(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	var mu sync.Mutex
	var calls [][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		calls = append(calls, req.Input)
		first := len(calls) == 1
		mu.Unlock()
		if first {
			close(started)
			<-release
		}
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"param": "input"}})
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(release) })
	c := testConfig("openai", server.URL)
	c.BatchSize, c.MaxConcurrentRequests = 2, 1
	i := newIngester(c, "fake-key", slog.Default())
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if err := i.provider.gate.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	left, err := i.documents.submit(canceled, "org_a", []string{"cancel"}, "cancel")
	if err != nil {
		i.provider.gate.release()
		t.Fatal(err)
	}
	right, err := i.documents.submit(ctx, "org_a", []string{"deny"}, "deny")
	i.provider.gate.release()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("combined request did not start")
	}
	cancel()
	releaseOnce.Do(func() { close(release) })
	select {
	case out := <-left:
		if !errors.Is(out.err, context.Canceled) {
			t.Fatalf("canceled: %v", out.err)
		}
	case <-ctx.Done():
		t.Fatal("canceled request did not settle")
	}
	select {
	case out := <-right:
		var refusal *quivrplugin.IngestError
		if !errors.As(out.err, &refusal) || refusal.Retryable {
			t.Fatalf("refusal: %v", out.err)
		}
	case <-ctx.Done():
		t.Fatal("refused request did not settle")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || len(calls[1]) != 1 || calls[1][0] != "deny" {
		t.Fatalf("canceled input retried in provider calls: %v", calls)
	}
}

// This dependency counts words independently of the production packing rules.
type wordCounter struct{}

func (wordCounter) Encode(_ context.Context, inputs []tokenInput) ([]tokenEncoding, error) {
	out := make([]tokenEncoding, len(inputs))
	for i, in := range inputs {
		r := []rune(in.Text)
		start := -1
		for j := 0; j <= len(r); j++ {
			if j < len(r) && !unicode.IsSpace(r[j]) {
				if start < 0 {
					start = j
				}
				continue
			}
			if start >= 0 {
				out[i].Offsets = append(out[i].Offsets, [2]int{start, j})
				start = -1
			}
		}
		out[i].Tokens = len(out[i].Offsets)
		if in.Special {
			out[i].Tokens += 2
		}
	}
	return out, nil
}
func TestPackedPassagesKeepParagraphsAndRebalance(t *testing.T) {
	c := testConfig("openai", "http://127.0.0.1:9")
	c.Packing = "paragraphs"
	c.BodyTokens = 10
	c.MaxChunks = 4
	c.RebalanceTail = true
	c.TailMinFraction = 0.25
	i := newIngester(c, "", slog.Default())
	i.tokenizer = wordCounter{}
	req := ingestRequest(c, "", false)
	req.Parts = []quivrplugin.IngestPart{
		{Key: "a", Role: "body", Text: "one two three four five six"},
		{Key: "b", Role: "body", Text: "seven eight nine"},
		{Key: "c", Role: "body", Text: "ten eleven"},
	}
	got, err := i.SegmentAndEmbed(t.Context(), req)
	if err != nil || len(got) != 2 {
		t.Fatalf("packed passages=%+v err=%v", got, err)
	}
	if len(got[0].SourceRanges) != 1 || len(got[1].SourceRanges) != 2 || got[1].SourceRanges[0].PartKey != "b" {
		t.Fatalf("tiny tail was not rebalanced with whole paragraphs: %+v", got)
	}
	c.BodyTokens = 8
	c.RebalanceTail = false
	i = newIngester(c, "", slog.Default())
	i.tokenizer = wordCounter{}
	req = ingestRequest(c, "one two\r\n\r\nthree four five six seven eight nine", false)
	got, err = i.SegmentAndEmbed(t.Context(), req)
	boundary := utf8.RuneCountInString("one two\r\n\r\n")
	if err != nil || len(got) != 2 || got[0].End != boundary || got[1].Start != boundary {
		t.Fatalf("CRLF paragraphs split: %+v %v", got, err)
	}

}

func TestPackedPassagesSplitOnlyOversizedParagraphAndEnforceCap(t *testing.T) {
	c := testConfig("openai", "http://127.0.0.1:9")
	c.Packing = "paragraphs"
	c.BodyTokens = 8
	c.MaxChunks = 4
	c.RebalanceTail = false
	i := newIngester(c, "", slog.Default())
	i.tokenizer = wordCounter{}
	req := ingestRequest(c, "one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen seventeen eighteen nineteen twenty", false)
	got, err := i.SegmentAndEmbed(t.Context(), req)
	if err != nil || len(got) != 3 {
		t.Fatalf("oversized paragraph: got %d chunks, err=%v", len(got), err)
	}
	previous := 0
	for _, s := range got {
		if s.Start != previous {
			t.Fatalf("gap or overlap at %d, want %d", s.Start, previous)
		}
		previous = s.End
	}
	if previous != utf8.RuneCountInString(req.Parts[0].Text) {
		t.Fatal("lost paragraph tail")
	}
	c.MaxChunks = 2
	i = newIngester(c, "", slog.Default())
	i.tokenizer = wordCounter{}
	if _, err = i.SegmentAndEmbed(t.Context(), req); err == nil {
		t.Fatal("accepted item above hard chunk cap")
	}
}

func TestPackedGemmaUsesHeadlineTitleAndKeepsMetadataOut(t *testing.T) {
	fake := embedding.New()
	server := httptest.NewServer(fake)
	defer server.Close()
	c := testConfig("openai", server.URL)
	c.Packing = "paragraphs"
	c.DocumentTemplate = "gemma"
	c.TitleSource = "title"
	c.BodyTokens = 812
	c.MaxTokens = 2048
	c.MaxChunks = 4
	c.DocumentPrefix = ""
	req := ingestRequest(c, "The library opens downtown.", true)
	req.Parts = append([]quivrplugin.IngestPart{{Key: "headline", Role: "title", Text: "Library opens"}, {Key: "date", Role: "date", Text: "2026-10-01"}, {Key: "code", Role: "category", Text: "12345"}}, req.Parts...)
	i := newIngester(c, "fake-key", slog.Default())
	got, err := i.SegmentAndEmbed(t.Context(), req)
	if err != nil || len(got) != 1 {
		t.Fatalf("headline passage: %+v %v", got, err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Texts[0] != "title: Library opens | text: The library opens downtown." {
		t.Fatalf("unexpected Gemma prompt: %+v", calls)
	}
	c.TitleSource = "none"
	i = newIngester(c, "fake-key", slog.Default())
	req.Spaces = []string{c.spaceID()}
	if _, err = i.SegmentAndEmbed(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if calls = fake.Calls(); len(calls) != 2 || calls[1].Texts[0] != "title: none | text: The library opens downtown." {
		t.Fatalf("title omission variant: %+v", calls)
	}

	c.TitleSource = "title"
	i = newIngester(c, "fake-key", slog.Default())
	req = ingestRequest(c, "", true)
	req.Parts = []quivrplugin.IngestPart{{Key: "headline", Role: "title", Text: "Library opens"}}
	if _, err = i.SegmentAndEmbed(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if calls = fake.Calls(); len(calls) != 3 || calls[2].Texts[0] != "title: Library opens | text: " {
		t.Fatalf("title-only duplicated: %+v", calls)
	}
	c.Packing = "none"
	for index, variant := range []struct {
		template, source, prefix, want string
	}{
		{"gemma", "title", "", "title: none | text: Library opens"},
		{"gemma", "inline", "", "title: none | text: Library opens"},
		{"prefix", "title", "passage: ", "passage: Library opens"},
	} {
		c.DocumentTemplate, c.TitleSource, c.DocumentPrefix = variant.template, variant.source, variant.prefix
		i = newIngester(c, "fake-key", slog.Default())
		req.Spaces = []string{c.spaceID()}
		if _, err = i.SegmentAndEmbed(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		if calls = fake.Calls(); len(calls) != index+4 || calls[index+3].Texts[0] != variant.want {
			t.Fatalf("legacy title-only duplicated: %+v", calls)
		}
	}

}
