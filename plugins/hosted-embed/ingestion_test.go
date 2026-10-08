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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
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
	t.Run("explicit templates ignore model names", func(t *testing.T) {
		for _, model := range []string{"google/embeddinggemma-2", "another-model"} {
			fake := embedding.New()
			server := httptest.NewServer(fake)
			defer server.Close()
			raw := fmt.Sprintf(`{"format":"openai","base_url":%q,"auth":"bearer","model":%q,"dimensions":8,"document_template":"title: {title} | text: {text}","query_template":"task: search result | query: {query}"}`, server.URL, model)
			c, err := parseConfiguration([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			i := newIngester(c, "fake-key", slog.New(slog.DiscardHandler))
			req := ingestRequest(c, "Body with {query} and {title}.", true)
			req.Parts = append([]quivrplugin.IngestPart{{Key: "headline", Role: "title", Text: "A {text} title"}}, req.Parts...)
			if _, err := i.SegmentAndEmbed(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			if _, err := i.EmbedQuery(t.Context(), queryRequest(c, "Find {prefix}")); err != nil {
				t.Fatal(err)
			}
			calls := fake.Calls()
			if len(calls) != 2 || calls[0].Texts[0] != "title: A {text} title | text: Body with {query} and {title}." || calls[1].Texts[0] != "task: search result | query: Find {prefix}" {
				t.Fatalf("model %s prompt bytes: %+v", model, calls)
			}
		}
	})
}

// Owns local query routing through the plugin HTTP boundary. The remote mapping
// keeper above owns provider formats; these dependencies only supply HTTP vectors.
// Tokenizer cancellation barriers exercise admission without wall-clock waits.
type queryCostCounter struct{ calls *atomic.Int32 }

func (c *queryCostCounter) Encode(_ context.Context, inputs []tokenInput) ([]tokenEncoding, error) {
	c.calls.Add(1)
	return []tokenEncoding{{Tokens: len(inputs[0].Text) + specialTokens}}, nil
}

// Observe a running query's budget setup while all tokenizer slots stay held.
type queryWaitContext struct {
	context.Context
	started chan struct{}
	once    sync.Once
}

func (c *queryWaitContext) Deadline() (time.Time, bool) {
	c.once.Do(func() { close(c.started) })
	return c.Context.Deadline()
}

type queryBudgetCounter struct {
	t       *testing.T
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (c *queryBudgetCounter) Encode(ctx context.Context, _ []tokenInput) ([]tokenEncoding, error) {
	c.calls.Add(1)
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > time.Second {
		c.t.Error("local tokenization lacks the one-second query budget")
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil, ctx.Err()
	}
	c.started <- struct{}{}
	<-c.release
	return nil, ctx.Err()
}
func TestLocalQueryTokenizationSharesDeadlineAndAdmission(t *testing.T) {
	c := testConfig("openai", "http://127.0.0.1:9")
	i := newIngester(c, "", slog.New(slog.DiscardHandler))
	counter := &queryBudgetCounter{t: t, started: make(chan struct{}, 5), release: make(chan struct{})}
	var released sync.Once
	release := func() { released.Do(func() { close(counter.release) }) }
	defer release()
	i.provider.counter = counter
	local := c
	local.RequestTimeoutMS, local.MaxRetries, local.Auth = 1000, 0, "none"
	i.queries = &provider{config: local, counter: counter, log: i.provider.log, local: true, gate: &providerGate{slots: make(chan struct{}, 4)}}
	completed := make(chan error, 4)
	cancels := []context.CancelFunc{}
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()
	for range 4 {
		ctx, cancel := context.WithCancel(t.Context())
		cancels = append(cancels, cancel)
		go func() { _, err := i.EmbedQuery(ctx, queryRequest(c, "A library opens.")); completed <- err }()
		select {
		case <-counter.started:
		case <-t.Context().Done():
			t.Fatal("tokenizer did not begin")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	waiting := &queryWaitContext{Context: ctx, started: make(chan struct{})}
	queued := make(chan error, 1)
	go func() { _, err := i.EmbedQuery(waiting, queryRequest(c, "A library opens.")); queued <- err }()
	select {
	case <-waiting.started:
	case <-t.Context().Done():
		t.Fatal("queued query did not start")
	}
	cancel()
	select {
	case err := <-queued:
		if err == nil {
			t.Fatal("cancelled waiting query accepted")
		}
	case <-t.Context().Done():
		t.Fatal("queued query did not cancel before slots released")
	}
	if counter.calls.Load() != 4 {
		t.Fatalf("tokenization exceeded four admitted queries: %d", counter.calls.Load())
	}
	for _, cancel := range cancels {
		cancel()
	}
	release()
	for range 4 {
		select {
		case err := <-completed:
			if err == nil {
				t.Fatal("cancelled tokenization succeeded")
			}
		case <-t.Context().Done():
			t.Fatal("cancelled query did not finish")
		}
	}
}

func TestLocalQueryRouting(t *testing.T) {
	const revision = "914f7f89142e33e7"
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			remote := embedding.New()
			remoteServer := httptest.NewServer(remote)
			defer remoteServer.Close()
			var localCalls atomic.Int32
			var unavailable atomic.Bool
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					_, _ = w.Write([]byte(`{"status":"ok","model":"test-model","model_revision":"914f7f89142e33e7","source_revision":"914f7f89142e33e77833254d9c9b90c3cef7303b","dimensions":8}`))
					return
				}
				localCalls.Add(1)
				if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "" || r.Header.Get("api-key") != "" || r.Header.Get("X-Quivr-Timeout-Ms") == "" {
					t.Errorf("wrong local path, credential or deadline: %s", r.URL.Path)
				}
				var body struct {
					Model      string
					Input      []string
					Dimensions int
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "test-model" || body.Dimensions != 8 || len(body.Input) != 1 || body.Input[0] != "query: A library opens downtown." {
					t.Errorf("wrong local model input: %+v, %v", body, err)
				}
				if unavailable.Load() {
					w.WriteHeader(503)
					return
				}
				_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,0,0,0,0,0,0,0]}]}`))
			}))
			defer local.Close()
			c := testConfig("openai", remoteServer.URL)
			c.Revision, c.QueryPrefix, c.DocumentPrefix = revision, "query: ", "passage: "
			manifest, _ := c.manifest([]string{"hosted-embed"})
			path := t.TempDir() + "/quivr-plugin.yaml"
			if err := os.WriteFile(path, manifest, 0600); err != nil {
				t.Fatal(err)
			}
			p, err := quivrplugin.New(path)
			if err != nil {
				t.Fatal(err)
			}
			i := newIngester(c, "fake-key", slog.New(slog.DiscardHandler))
			url := ""
			if enabled {
				url = local.URL + "/v1"
			}
			if err := i.localQueries(t.Context(), url); err != nil {
				t.Fatal(err)
			}
			if err := p.Ingestion(i); err != nil {
				t.Fatal(err)
			}
			handler, err := p.Handler()
			if err != nil {
				t.Fatal(err)
			}
			invoke := func(method string, request any) *httptest.ResponseRecorder {
				t.Helper()
				body, _ := json.Marshal(request)
				response := httptest.NewRecorder()
				httpRequest := httptest.NewRequest("POST", "/v0/contributions/ingestion/"+method, bytes.NewReader(body))
				signTestRequest(t, c.PluginID, httpRequest, body)
				handler.ServeHTTP(response, httpRequest)
				return response
			}
			if got := invoke("segment_and_embed", ingestRequest(c, "A library opens downtown.", true)); got.Code != 200 {
				t.Fatalf("document: %d %s", got.Code, got.Body.String())
			}
			var counts atomic.Int32
			if enabled {
				counter := &queryCostCounter{calls: &counts}
				i.provider.counter, i.queries.counter = counter, counter
			}
			if got := invoke("embed_query", queryRequest(c, "A library opens downtown.")); got.Code != 200 {
				t.Fatalf("query: %d %s", got.Code, got.Body.String())
			}
			if enabled && counts.Load() != 1 {
				t.Fatalf("query tokenization repeated: %d", counts.Load())
			}
			wantRemote, wantLocal := 2, int32(0)
			if enabled {
				wantRemote, wantLocal = 1, 1
			}
			calls := remote.Calls()
			if len(calls) != wantRemote || localCalls.Load() != wantLocal || calls[0].Auth != "bearer" || calls[0].Texts[0] != "passage: A library opens downtown." {
				t.Fatalf("routing: remote %+v, local %d", calls, localCalls.Load())
			}
			if enabled {
				unavailable.Store(true)
				if got := invoke("embed_query", queryRequest(c, "A library opens downtown.")); got.Code != 503 {
					t.Fatalf("local outage: %d %s", got.Code, got.Body.String())
				}
				if len(remote.Calls()) != 1 || localCalls.Load() != 2 {
					t.Fatal("local failure retried or fell back remotely")
				}
			}
			if after, _ := i.config.manifest([]string{"hosted-embed"}); !bytes.Equal(manifest, after) {
				t.Fatal("runtime routing changed the pinned manifest")
			}
		})
	}
}

func TestLocalQueryRefusesIdentityAndEndpointDrift(t *testing.T) {
	for _, field := range []string{"model", "model_revision", "source_revision", "dimensions", "status"} {
		t.Run(field, func(t *testing.T) {
			metadata := map[string]any{"status": "ok", "model": "test-model", "model_revision": "914f7f89142e33e7", "source_revision": "914f7f89142e33e77833254d9c9b90c3cef7303b", "dimensions": 8}
			metadata[field] = "other"
			if field == "dimensions" {
				metadata[field] = 9
			}
			if field == "source_revision" {
				metadata[field] = "014f7f89142e33e77833254d9c9b90c3cef7303b"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(metadata) }))
			defer server.Close()
			c := testConfig("openai", "http://127.0.0.1:9")
			c.Revision = "914f7f89142e33e7"
			if err := newIngester(c, "fake-key", slog.New(slog.DiscardHandler)).localQueries(t.Context(), server.URL+"/v1"); err == nil {
				t.Fatalf("accepted %s drift", field)
			}
		})
	}
	c := testConfig("openai", "http://127.0.0.1:9")
	for _, url := range []string{"https://127.0.0.1/v1", "http://example.org/v1", "http://localhost/v1", "http://key@127.0.0.1/v1", "http://127.0.0.1/v1?token=value", "http://127.0.0.1/v1#fragment", "http://127.0.0.1/other", "http://127.0.0.1/v1%2f", "http://127.0.0.1/v%31"} {
		if err := newIngester(c, "", slog.New(slog.DiscardHandler)).localQueries(t.Context(), url); err == nil || !strings.Contains(err.Error(), "QUIVR_HOSTED_QUERY_URL") {
			t.Fatalf("invalid local endpoint: %v", err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid revision reached readiness") }))
	defer server.Close()
	for _, revision := range []string{"1", "release-2026-0016", "914f7f89142e33eX"} {
		c.Revision = revision
		if err := newIngester(c, "", slog.New(slog.DiscardHandler)).localQueries(t.Context(), server.URL+"/v1"); err == nil || !strings.Contains(err.Error(), "model_revision") {
			t.Fatalf("invalid local revision: %v", err)
		}
	}
	c.Format = "cohere"
	if err := newIngester(c, "", slog.New(slog.DiscardHandler)).localQueries(t.Context(), "http://127.0.0.1:9/v1"); err == nil {
		t.Fatal("accepted unsupported provider format")
	}
}

func TestPackedInputsPreserveUnicodeOffsets(t *testing.T) {
	c := testConfig("openai", "http://127.0.0.1:9")
	c.MaxTokens = 60
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
		if n > 0 && s.Start != got[n-1].End {
			t.Fatalf("gap or overlap: %+v", got)
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
	rawExample, err := os.ReadFile("examples/tei.json")
	if err != nil {
		t.Fatal(err)
	}
	example, err := parseConfiguration(rawExample)
	if err != nil {
		t.Fatal(err)
	}
	// This released identity included title text in each passage. Full-text
	// pages embed titles separately and must never share its vector namespace.
	if example.spaceID() == "hosted.embed.intfloat-multilingual-e5-sma-384-96f19d0e4243e08b" {
		t.Fatal("new passage semantics reused the released space")
	}
	c := testConfig("openai", "http://127.0.0.1:9")
	original := c.spaceID()
	tuned := c
	tuned.TokenizerProcesses = 2
	if tuned.spaceID() != original {
		t.Fatal("tokenizer process tuning changed the vector space")
	}
	for _, change := range []func(*configuration){func(c *configuration) { c.Model = "other" }, func(c *configuration) { c.Dimensions = 16 }, func(c *configuration) { c.QueryPrefix = "query: " }, func(c *configuration) { c.Revision = "2" }, func(c *configuration) { c.Metric = "dot" }, func(c *configuration) { c.DocumentTemplate = "document: {text}" }, func(c *configuration) { c.QueryTemplate = "question: {query}" }} {
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
	var declared struct {
		Configuration struct {
			ExecutionKeys []string `json:"execution_keys"`
		} `json:"configuration"`
		Secrets []struct {
			Name     string `json:"name"`
			Required bool   `json:"required"`
		} `json:"secrets"`
	}
	if err := json.Unmarshal(manifest, &declared); err != nil {
		t.Fatal(err)
	}
	if len(declared.Secrets) != 1 || declared.Secrets[0].Name != "EMBED_API_KEY" || !declared.Secrets[0].Required {
		t.Fatalf("provider-neutral credential declaration: %+v", declared.Secrets)
	}
	want := []string{"tokenizer_processes", "max_concurrent_requests", "batch_size", "max_batch_tokens", "request_timeout_ms", "call_budget_ms", "batch_wait_ms", "max_retries"}
	slices.Sort(want)
	slices.Sort(declared.Configuration.ExecutionKeys)
	if !slices.Equal(declared.Configuration.ExecutionKeys, want) {
		t.Fatalf("execution-only configuration contract: got %v, want %v", declared.Configuration.ExecutionKeys, want)
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
	// Literal external keys and omission protect the configuration contract.
	for _, tc := range []struct {
		raw       string
		wait      int
		processes int
	}{
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8}`, 25, 0},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"batch_wait_ms":0}`, 0, 0},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"tokenizer_processes":0}`, 25, 0},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"tokenizer_processes":2}`, 25, 2},
	} {
		c, err := parseConfiguration([]byte(tc.raw))
		if err != nil || c.BatchWaitMS != tc.wait || c.TokenizerProcesses != tc.processes || c.DocumentTemplate != "{prefix}{text}" || c.QueryTemplate != "{prefix}{query}" {
			t.Fatalf("configuration defaults: %+v %v", c, err)
		}
	}
	for _, tc := range []struct{ raw, reason string }{
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"packing":"paragraphs"}`, "configuration must contain only declared fields"},
		{`{"format":"cohere","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"query_input_type":"search_query"}`, "configuration must contain only declared fields"},
		{`{"format":"cohere","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"document_input_type":"search_document"}`, "configuration must contain only declared fields"},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"document_template":"gemma"}`, "document_template: must contain {text}"},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"query_template":"{text}"}`, "query_template: unsupported"},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"query_template":"{query","document_template":"{text}"}`, "query_template: unsupported"},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"document_template":"{text}}"}`, "document_template: unmatched"},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"document_template":"{text}","document_prefix":"passage: "}`, "document_template: nonempty prefix requires {prefix}"},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"query_template":"{query}","query_prefix":"query: "}`, "query_template: nonempty prefix requires {prefix}"},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"query_template":"{query}\u0000"}`, "query_template: must not contain NUL"},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"tokenizer":{"python":"bad\u0000path","model":"local.json","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`, "tokenizer requires"},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"tokenizer":{"python":"python3","model":"bad\u0000path","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`, "tokenizer requires"},

		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"tokenizer_processes":-1}`, "tokenizer_processes must be between 0 and 32"},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"tokenizer_processes":33}`, "tokenizer_processes must be between 0 and 32"},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"batch_wait_ms":-1}`, "batch_wait_ms must be between 0 and 100"},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"batch_wait_ms":101}`, "batch_wait_ms must be between 0 and 100"},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"batch_wait_ms":100,"call_budget_ms":100}`, "batch_wait_ms must be less than call_budget_ms"},
		{`{"format":"openai","base_url":"https://key@example.org","auth":"bearer","model":"m","dimensions":8}`, "base_url must be an HTTP(S) base without credentials"},
		{`{"format":"openai","base_url":"http://example.org","auth":"bearer","model":"m","dimensions":8,"api_key":"secret"}`, "configuration must contain only declared fields"},
		{`{"format":"openai","base_url":"http://example.org","auth":"none","model":"m","dimensions":8,"overlap":512}`, "configuration must contain only declared fields"},
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
	c.QueryTemplate = strings.Repeat("q", c.MaxTokens-specialTokens-4) + "{query}"
	i = newIngester(c, "", slog.Default())
	_, err := i.EmbedQuery(context.Background(), queryRequest(c, "short"))
	var refusal *quivrplugin.IngestError
	if !errors.As(err, &refusal) || refusal.Code != "query_limit" || refusal.Retryable {
		t.Errorf("oversize query: got %v, want terminal query_limit", err)
	}
}

// The configured package must be admissible for every accepted model/base URL,
// and must accept the same configuration the operator originally supplied.
func TestConfiguredManifestAcceptsOriginalConfiguration(t *testing.T) {
	examples, err := os.ReadDir("examples")
	if err != nil {
		t.Fatal(err)
	}
	for _, example := range examples {
		t.Run(example.Name(), func(t *testing.T) {
			raw, err := os.ReadFile("examples/" + example.Name())
			if err != nil {
				t.Fatal(err)
			}
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
			if _, err := quivrplugin.New(path); err != nil {
				t.Fatalf("example manifest admission: %v", err)
			}
		})
	}
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
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "3600")
				w.Header().Set("Connection", "close")
				w.WriteHeader(status)
			}))
			defer server.Close()
			c := testConfig("openai", server.URL)
			c.MaxRetries = 0
			i := newIngester(c, "fake-key", slog.New(slog.DiscardHandler))
			_, err := i.EmbedQuery(t.Context(), queryRequest(c, "first"))
			if err == nil {
				t.Fatalf("HTTP %d succeeded", status)
			}
			// Keep real socket I/O outside the virtual-time bubble. Only the
			// subsequent admission wait needs the virtual deadline.
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				_, err := i.EmbedQuery(ctx, queryRequest(c, "second"))
				if err == nil || ctx.Err() != context.DeadlineExceeded || calls.Load() != 1 {
					t.Fatalf("throttled waiter made a provider call: calls=%d error=%v", calls.Load(), err)
				}
			})
		})
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
func TestDocumentBatchRetriesSharedProviderFailureWithoutSplitting(t *testing.T) {
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
					if !errors.As(out.err, &refusal) || !refusal.Retryable || refusal.Code != "provider_unavailable" {
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

func TestPackedPassagesSplitOnlyOversizedParagraphAndKeepEveryPassage(t *testing.T) {
	c := testConfig("openai", "http://127.0.0.1:9")
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
	got, err = i.SegmentAndEmbed(t.Context(), req)
	if err != nil || len(got) != 3 {
		t.Fatalf("work group size dropped passages: got %d, err=%v", len(got), err)
	}
	if got[2].End != 131 {
		t.Fatalf("last passage ends at %d; want full text end 131", got[2].End)
	}
}

// Provider negotiation must create real independently embedded child passages,
// and continuation must retain the tail even when one page is already full.
func TestPagedIngestionSplitsProviderInputWithoutLosingText(t *testing.T) {
	for _, variant := range []struct {
		name, text, refusal, prefix, template string
		maxTokens                             int
	}{
		{"unicode", "αβγδεζηθ", `{"error":{"code":"context_length_exceeded"}}`, "", "", 512},
		{"top-level message", "αβγδεζηθ", `{"message":"input is too long"}`, "", "", 512},
		{"string error", "αβγδεζηθ", `{"error":"input is too long"}`, "", "", 512},
		{"structured detail", "αβγδεζηθ", `{"error":{"code":"context_length_exceeded"},"detail":{"message":"input is too long"}}`, "", "", 512},
		{"validation detail", "αβγδεζηθ", `{"detail":[{"msg":"input is too long"}]}`, "", "", 512},
		{"Gemma window template", "αβγδεζηθ", `{"error":{"code":"context_length_exceeded"}}`, "title: none | text: ", "title: {title} | text: {text}", 512},
		{"whitespace window", "        ", `{"error":{"code":"context_length_exceeded"}}`, "", "", 512},
		{"conservative budget", "αβγδεζηθ", `{"error":{"code":"context_length_exceeded"}}`, "", "", 2},
	} {
		t.Run(variant.name, func(t *testing.T) {
			var accepted []string
			var acceptedMu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Input []string `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				for _, input := range request.Input {
					if utf8.RuneCountInString(strings.TrimPrefix(input, variant.prefix)) > 4 {
						w.WriteHeader(400)
						_, _ = w.Write([]byte(variant.refusal))
						return
					}
				}
				data := []any{}
				for n, input := range request.Input {
					acceptedMu.Lock()
					accepted = append(accepted, input)
					acceptedMu.Unlock()
					data = append(data, map[string]any{"index": n, "embedding": []float32{1, 2}})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			}))
			defer server.Close()
			c := testConfig("openai", server.URL)
			c.Auth = "none"
			c.Dimensions = 2
			c.MaxChunks = 1
			c.BatchWaitMS = 0
			c.TitleSource = "none"
			c.MaxTokens = variant.maxTokens
			if variant.template != "" {
				c.DocumentTemplate = variant.template
			}
			i := newIngester(c, "", slog.Default())
			i.tokenizer = wordCounter{}
			req := ingestRequest(c, variant.text, true)
			req.Page = &quivrplugin.IngestPageRequest{Start: 0, MaxSegments: 1}
			first, err := i.SegmentAndEmbedPage(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			if len(first.Segments) != 1 || first.NextStart == nil || *first.NextStart != 4 || first.Segments[0].End != 4 || len(first.Segments[0].Vectors[c.spaceID()]) != 2 {
				t.Fatalf("first page=%+v", first)
			}
			if first.Segments[0].Provenance["provider_split"] != true {
				t.Fatalf("missing split diagnostic: %+v", first.Segments[0].Provenance)
			}
			req.Page.Start = *first.NextStart
			last, err := i.SegmentAndEmbedPage(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			if len(last.Segments) != 1 || last.NextStart != nil || last.Segments[0].Start != 4 || last.Segments[0].End != 8 {
				t.Fatalf("tail page=%+v", last)
			}
			want := []string{variant.prefix + string([]rune(variant.text)[:4]), variant.prefix + string([]rune(variant.text)[4:])}
			if want[0] == want[1] {
				want = want[:1]
			} // Identical inputs reuse the vector cache.
			acceptedMu.Lock()
			acceptedInputs := slices.Clone(accepted)
			acceptedMu.Unlock()
			if !slices.Equal(acceptedInputs, want) || len(last.Segments[0].Vectors[c.spaceID()]) != 2 {
				t.Fatalf("provider accepted %v; want both complete halves", acceptedInputs)
			}

		})
	}
}

func TestPackedGemmaUsesHeadlineTitleAndKeepsMetadataOut(t *testing.T) {
	t.Run("twenty paragraph Parts", func(t *testing.T) {
		fake := embedding.New()
		server := httptest.NewServer(fake)
		defer server.Close()
		c := testConfig("openai", server.URL)
		c.DocumentTemplate, c.TitleSource = "title: {title} | text: {text}", "title"
		c.BodyTokens, c.MaxTokens, c.MaxChunks = 512, 2048, 4
		i := newIngester(c, "fake-key", slog.Default())
		i.tokenizer = wordCounter{}
		req := ingestRequest(c, "", true)
		req.Parts = []quivrplugin.IngestPart{{Key: "slug", Role: "context", Text: "update"}, {Key: "headline", Role: "title", Text: "Library opens"}}
		for n := range 20 {
			req.Parts = append(req.Parts, quivrplugin.IngestPart{Key: fmt.Sprintf("paragraph-%02d", n), Role: "body", Text: fmt.Sprint(n) + " " + strings.Repeat("word ", 79)})
		}
		got, err := i.SegmentAndEmbed(t.Context(), req)
		if err != nil || len(got) < 3 || len(got) > 5 {
			t.Fatalf("twenty paragraphs: passages=%d err=%v", len(got), err)
		}
		covered := 0
		for _, s := range got {
			if len(s.SourceRanges) < 2 || len(s.Vectors[c.spaceID()]) != c.Dimensions {
				t.Fatalf("unpacked or unembedded passage: %+v", s)
			}
			words := 0
			for _, r := range s.SourceRanges {
				part := req.Parts[covered+2]
				if r.PartKey != part.Key || r.Start != 0 || r.End != utf8.RuneCountInString(part.Text) {
					t.Fatalf("missing or repeated paragraph %d: %+v", covered, r)
				}
				words += len(strings.Fields(part.Text))
				covered++
			}
			if words > 512 {
				t.Fatalf("body budget exceeded: %d words", words)
			}
		}
		if covered != 20 {
			t.Fatalf("covered %d paragraphs; want twenty", covered)
		}
		inputs := 0
		for _, call := range fake.Calls() {
			for _, input := range call.Texts {
				body, ok := strings.CutPrefix(input, "title: Library opens | text: ")
				if !ok || strings.Contains(body, "Library opens") || strings.Contains(body, "update") {
					t.Fatalf("unexpected title/context in model input: %q", input)
				}
				inputs++
			}
		}
		if inputs != len(got) {
			t.Fatalf("provider inputs=%d passages=%d", inputs, len(got))
		}
	})
	fake := embedding.New()
	server := httptest.NewServer(fake)
	defer server.Close()
	c := testConfig("openai", server.URL)
	c.DocumentTemplate = "title: {title} | text: {text}"
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
	// Plain templates must keep title-only text in the body once.
	c.DocumentTemplate, c.DocumentPrefix = "{prefix}{text}", "passage: "
	i = newIngester(c, "fake-key", slog.Default())
	req.Spaces = []string{c.spaceID()}
	if _, err = i.SegmentAndEmbed(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if calls = fake.Calls(); len(calls) != 4 || calls[3].Texts[0] != "passage: Library opens" {
		t.Fatalf("plain title-only lost or duplicated: %+v", calls)
	}
	c.TitleContextParts = []string{"place"}
	req.Parts = append(req.Parts, quivrplugin.IngestPart{Key: "place", Role: "place", Text: "Riverside"})
	for index, variant := range []struct {
		template, source, prefix, want string
	}{
		{"title: {title} | text: {text}", "title", "", "title: Library opens — Riverside | text: "},
		{"{prefix}{text}", "inline", "passage: ", "passage: Riverside\n\nLibrary opens"},
		{"{prefix}{text}", "title", "passage: ", "passage: Riverside\n\nLibrary opens"},
	} {
		c.DocumentTemplate, c.TitleSource, c.DocumentPrefix = variant.template, variant.source, variant.prefix
		i = newIngester(c, "fake-key", slog.Default())
		req.Spaces = []string{c.spaceID()}
		if _, err = i.SegmentAndEmbed(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		if calls = fake.Calls(); len(calls) != index+5 || calls[index+4].Texts[0] != variant.want {
			t.Fatalf("title-only context lost or headline duplicated: %+v", calls)
		}
	}
}
