package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/The-Vibe-Company/quivr/internal/testutil/apicontract"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

// referenceDecodeSearchRequest mirrors the legacy decodeRequest + JSON
// re-marshal + transport.SearchRequest unmarshal + metadata filter re-marshal
// pipeline so contract equivalence tests prove byte-identical verdicts without
// duplicating OpenAPI schema rules.
func referenceDecodeSearchRequest(t *testing.T, raw []byte) (retrieval.Request, error) {
	t.Helper()
	contract, err := loadContract()
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var parsed any
	if err := dec.Decode(&parsed); err != nil {
		return retrieval.Request{}, publicerr.MalformedJson
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return retrieval.Request{}, publicerr.MalformedJson
	}
	if err := contract.schemas["SearchRequest"].Validate(parsed); err != nil {
		return retrieval.Request{}, publicerr.InvalidSchema
	}
	b, err := json.Marshal(parsed)
	if err != nil {
		return retrieval.Request{}, publicerr.InvalidSchema
	}
	var wire transport.SearchRequest
	if err := json.Unmarshal(b, &wire); err != nil {
		return retrieval.Request{}, publicerr.InvalidSchema
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
		encoded, err := json.Marshal(wire.Filter.Metadata)
		if err != nil {
			return retrieval.Request{}, publicerr.InvalidQuery
		}
		if err := json.Unmarshal(encoded, &q.Metadata); err != nil {
			return retrieval.Request{}, publicerr.InvalidQuery
		}
	}
	return q, nil
}

// Owns 1-to-1 contract equivalence between single-pass decodeSearchRequest and
// the authoritative OpenAPI SearchRequest JSON Schema + wire mapping.
func TestDecodeSearchRequestMatchesJSONSchemaAndWireMapping(t *testing.T) {
	corpora16 := make([]string, 16)
	for i := range corpora16 {
		corpora16[i] = "corpus_" + strconv.Itoa(i)
	}
	corpora17 := append(append([]string{}, corpora16...), "corpus_16")
	corpora16JSON, _ := json.Marshal(corpora16)
	corpora17JSON, _ := json.Marshal(corpora17)

	ids50 := make([]string, 50)
	for i := range ids50 {
		ids50[i] = "id_" + strconv.Itoa(i)
	}
	ids51 := append(append([]string{}, ids50...), "id_50")
	ids50JSON, _ := json.Marshal(ids50)
	ids51JSON, _ := json.Marshal(ids51)

	md16 := make([]map[string]any, 16)
	for i := range md16 {
		md16[i] = map[string]any{"field": "field_" + strconv.Itoa(i), "any_of": []any{"v"}}
	}
	md17 := append(append([]map[string]any{}, md16...), map[string]any{"field": "field_16", "any_of": []any{"v"}})
	md16JSON, _ := json.Marshal(md16)
	md17JSON, _ := json.Marshal(md17)

	any50 := make([]any, 50)
	for i := range any50 {
		any50[i] = float64(i)
	}
	any51 := append(append([]any{}, any50...), float64(50))
	any50JSON, _ := json.Marshal(any50)
	any51JSON, _ := json.Marshal(any51)

	cases := []struct {
		name string
		raw  string
	}{
		// Valid requests covering defaults, boundaries, escapes, and filters.
		{
			name: "minimal defaults",
			raw:  `{"query":"harbour","corpus_ids":["c1"]}`,
		},
		{
			name: "whitespace query passes schema",
			raw:  `{"query":"   ","corpus_ids":["c1"]}`,
		},
		{
			name: "all fields with 16 corpora and typed metadata",
			raw:  `{"query":"forecast \n\t \"q4\" \u00e9 \uD83D\uDE00","corpus_ids":` + string(corpora16JSON) + `,"mode":"semantic","profile":"deep","limit":50,"evaluation_plugin":"core.ingest","evaluation_space":"space.v1","filter":{"source_namespaces":["docs","wiki"],"record_ids":["r1","r2"],"version_ids":["v1"],"metadata":[{"field":"metadata.language","any_of":["en","fr"]},{"field":"score_band","any_of":[1,2.5,-3e1]},{"field":"verified","any_of":[true,false]},{"field":"metadata.published_at","gte":"2025-01-01T00:00:00Z","lte":"2026-06-15T12:30:45.123000+02:00"}]}}`,
		},
		{
			name: "escaped object keys and escaped Z in datetime",
			raw:  `{"\u0071uery":"ok","\u0063orpus_ids":["c1"],"\u006dode":"lexical","\u006cimit":1,"\u0066ilter":{"\u006detadata":[{"\u0066ield":"metadata.published_at","\u0067te":"2025-01-01T00:00:00\u005a"}]}}`,
		},
		{
			name: "datetime trailing zeros and +00:00 normalization",
			raw:  `{"query":"ok","corpus_ids":["c1"],"filter":{"metadata":[{"field":"metadata.published_at","gte":"2025-01-01T00:00:00.000Z","lte":"2025-12-31T23:59:59+00:00"}]}}`,
		},
		{
			name: "query exact 8192 multibyte codepoints",
			raw:  `{"query":"` + strings.Repeat("é", 8192) + `","corpus_ids":["c1"]}`,
		},
		{
			name: "filter string exact 200 multibyte codepoints",
			raw:  `{"query":"q","corpus_ids":["c1"],"filter":{"record_ids":["` + strings.Repeat("é", 200) + `"],"metadata":[{"field":"metadata.tags","any_of":["` + strings.Repeat("é", 200) + `"]}]}}`,
		},
		{
			name: "max 50 unique filter ids and 16 metadata predicates and 50 any_of items",
			raw:  `{"query":"q","corpus_ids":["c1"],"filter":{"source_namespaces":` + string(ids50JSON) + `,"metadata":` + string(md16JSON) + `}}`,
		},
		{
			name: "50 any_of items with duplicates allowed by schema",
			raw:  `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"score","any_of":` + string(any50JSON) + `},{"field":"tag","any_of":["a","a"]}]}}`,
		},
		{
			name: "duplicate metadata field names across predicates allowed by schema",
			raw:  `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"metadata.language","any_of":["en"]},{"field":"metadata.language","any_of":["fr"]}]}}`,
		},
		{
			name: "single evaluation_plugin without evaluation_space allowed by schema",
			raw:  `{"query":"q","corpus_ids":["c1"],"evaluation_plugin":"core.ingest"}`,
		},
		{
			name: "metadata field max length 64 chars after metadata prefix",
			raw:  `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"metadata.a` + strings.Repeat("b1_", 21) + `","any_of":["x"]}]}}`,
		},
		{
			name: "large payload exceeding alias threshold",
			raw:  strings.Repeat(" ", searchDecodeAliasMaxBytes+128) + `{"query":"large","corpus_ids":["c1","c2"],"mode":"hybrid","limit":12,"filter":{"metadata":[{"field":"score","any_of":[1.5,true,"ok"]},{"field":"metadata.published_at","gte":"2026-01-01T00:00:00Z"}]}}`,
		},

		// Schema violations (must return InvalidSchema).
		{name: "empty object", raw: `{}`},
		{name: "bare array", raw: `[]`},
		{name: "bare string", raw: `"hello"`},
		{name: "bare null", raw: `null`},
		{name: "missing query", raw: `{"corpus_ids":["c1"]}`},
		{name: "missing corpus_ids", raw: `{"query":"q"}`},
		{name: "empty query", raw: `{"query":"","corpus_ids":["c1"]}`},
		{name: "query 8193 codepoints", raw: `{"query":"` + strings.Repeat("é", 8193) + `","corpus_ids":["c1"]}`},
		{name: "null query", raw: `{"query":null,"corpus_ids":["c1"]}`},
		{name: "unknown top-level key", raw: `{"query":"q","corpus_ids":["c1"],"extra":1}`},
		{name: "corpus_ids empty", raw: `{"query":"q","corpus_ids":[]}`},
		{name: "corpus_ids 17 items", raw: `{"query":"q","corpus_ids":` + string(corpora17JSON) + `}`},
		{name: "corpus_ids duplicate", raw: `{"query":"q","corpus_ids":["c1","c1"]}`},
		{name: "corpus_ids empty element", raw: `{"query":"q","corpus_ids":[""]}`},
		{name: "corpus_ids wrong element type", raw: `{"query":"q","corpus_ids":[123]}`},
		{name: "mode invalid enum", raw: `{"query":"q","corpus_ids":["c1"],"mode":"dense"}`},
		{name: "mode null", raw: `{"query":"q","corpus_ids":["c1"],"mode":null}`},
		{name: "profile empty", raw: `{"query":"q","corpus_ids":["c1"],"profile":""}`},
		{name: "limit zero", raw: `{"query":"q","corpus_ids":["c1"],"limit":0}`},
		{name: "limit negative", raw: `{"query":"q","corpus_ids":["c1"],"limit":-5}`},
		{name: "limit 51", raw: `{"query":"q","corpus_ids":["c1"],"limit":51}`},
		{name: "limit float 10.0", raw: `{"query":"q","corpus_ids":["c1"],"limit":10.0}`},
		{name: "limit exponent 1e1", raw: `{"query":"q","corpus_ids":["c1"],"limit":1e1}`},
		{name: "limit string", raw: `{"query":"q","corpus_ids":["c1"],"limit":"10"}`},
		{name: "limit null", raw: `{"query":"q","corpus_ids":["c1"],"limit":null}`},
		{name: "evaluation_plugin empty", raw: `{"query":"q","corpus_ids":["c1"],"evaluation_plugin":""}`},
		{name: "evaluation_space empty", raw: `{"query":"q","corpus_ids":["c1"],"evaluation_space":""}`},
		{name: "filter empty object violates minProperties", raw: `{"query":"q","corpus_ids":["c1"],"filter":{}}`},
		{name: "filter null", raw: `{"query":"q","corpus_ids":["c1"],"filter":null}`},
		{name: "filter unknown key", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"extra":["x"]}}`},
		{name: "source_namespaces 51 items", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"source_namespaces":` + string(ids51JSON) + `}}`},
		{name: "source_namespaces duplicate", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"source_namespaces":["a","a"]}}`},
		{name: "record_ids 201 runes", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"record_ids":["` + strings.Repeat("é", 201) + `"]}}`},
		{name: "version_ids empty", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"version_ids":[]}}`},
		{name: "metadata empty array", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[]}}`},
		{name: "metadata 17 items", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":` + string(md17JSON) + `}}`},
		{name: "metadata missing field", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"any_of":["a"]}]}}`},
		{name: "metadata missing condition", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"metadata.language"}]}}`},
		{name: "metadata bad field uppercase", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"Metadata.language","any_of":["en"]}]}}`},
		{name: "metadata bad field leading digit", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"1field","any_of":["en"]}]}}`},
		{name: "metadata bad field too long", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"metadata.a` + strings.Repeat("b", 64) + `","any_of":["en"]}]}}`},
		{name: "metadata unknown key", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"tag","any_of":["x"],"extra":1}]}}`},
		{name: "metadata any_of empty", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"tag","any_of":[]}]}}`},
		{name: "metadata any_of 51 items", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"score","any_of":` + string(any51JSON) + `}]}}`},
		{name: "metadata any_of null element", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"tag","any_of":[null]}]}}`},
		{name: "metadata any_of object element", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"tag","any_of":[{}]}]}}`},
		{name: "metadata any_of array element", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"tag","any_of":[[]]}]}}`},
		{name: "metadata any_of empty string", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"tag","any_of":[""]}]}}`},
		{name: "metadata any_of 201 rune string", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"tag","any_of":["` + strings.Repeat("é", 201) + `"]}]}}`},
		{name: "metadata any_of overflow float", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"score","any_of":[1e999]}]}}`},
		{name: "metadata gte invalid date", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"metadata.published_at","gte":"2025-02-30T00:00:00Z"}]}}`},
		{name: "metadata lte lowercase z", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"metadata.published_at","lte":"2025-01-01T00:00:00z"}]}}`},
		{name: "metadata gte non-string", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"metadata.published_at","gte":12345}]}}`},

		// Duplicate keys: last-value-wins parity.
		{
			name: "duplicate query invalid before valid",
			raw:  `{"query":"","query":"ok","corpus_ids":["c1"]}`,
		},
		{
			name: "duplicate query valid before invalid",
			raw:  `{"query":"ok","query":123,"corpus_ids":["c1"]}`,
		},
		{
			name: "duplicate mode invalid before valid",
			raw:  `{"query":"q","corpus_ids":["c1"],"mode":"bad","mode":"lexical"}`,
		},
		{
			name: "duplicate limit invalid before valid",
			raw:  `{"query":"q","corpus_ids":["c1"],"limit":99,"limit":5}`,
		},
		{
			name: "duplicate filter replaces prior filter cleanly",
			raw:  `{"query":"q","corpus_ids":["c1"],"filter":{"record_ids":["r1"]},"filter":{"version_ids":["v2"]}}`,
		},
		{
			name: "duplicate filter invalid before valid",
			raw:  `{"query":"q","corpus_ids":["c1"],"filter":{},"filter":{"source_namespaces":["ns1"]}}`,
		},
		{
			name: "duplicate metadata filter field invalid before valid",
			raw:  `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"BAD","field":"good","any_of":[],"any_of":["ok"]}]}}`,
		},

		// Malformed JSON syntax (must return MalformedJson even after schema errors).
		{name: "empty body", raw: ``},
		{name: "truncated object", raw: `{"query":"q","corpus_ids":["c1"`},
		{name: "trailing document", raw: `{"query":"q","corpus_ids":["c1"]} {}`},
		{name: "schema error before syntax error", raw: `{"unknown":1, broken`},
		{name: "invalid corpus_ids element before syntax error", raw: `{"query":"q","corpus_ids":[123, broken]}`},
		{name: "invalid any_of element before syntax error", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"tag","any_of":[null, broken]}]}}`},
		{name: "leading zero number", raw: `{"query":"q","corpus_ids":["c1"],"limit":01}`},
		{name: "unescaped control char in query", raw: "{\"query\":\"a\nb\",\"corpus_ids\":[\"c1\"]}"},
		{name: "deeply nested value rejected cleanly", raw: `{"query":"q","corpus_ids":["c1"],"filter":{"metadata":[{"field":"tag","any_of":[` + strings.Repeat("[", maxJSONNestingDepth+5) + strings.Repeat("]", maxJSONNestingDepth+5) + `]}]}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantReq, wantErr := referenceDecodeSearchRequest(t, []byte(tc.raw))
			gotReq, gotErr := decodeSearchRequest([]byte(tc.raw))
			if !errors.Is(gotErr, wantErr) {
				t.Fatalf("err=%v, want %v (raw=%s)", gotErr, wantErr, tc.raw)
			}
			if wantErr == nil && !reflect.DeepEqual(gotReq, wantReq) {
				t.Fatalf("request mismatch:\ngot:  %#v\nwant: %#v", gotReq, wantReq)
			}
		})
	}
}

type panicSearchRouting struct {
	mu      sync.Mutex
	onAuth  func(ctx context.Context) error
	entered chan struct{}
	release chan struct{}
}

func (p *panicSearchRouting) Authorize(ctx context.Context, _ corpus.Scope, _ []string) error {
	p.mu.Lock()
	fn := p.onAuth
	p.mu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	return nil
}

func (*panicSearchRouting) Generation(context.Context, string, string) (content.Generation, error) {
	return content.Generation{ID: "gen", ProfileVersion: retrieval.ProfileVersion, SpaceID: "space"}, nil
}

type noopSearchDep struct{}

func (noopSearchDep) Publish(context.Context, content.Generation, string, string, string, content.Version, content.Segmentation) error {
	return nil
}
func (noopSearchDep) PublishEmbeddings(context.Context, content.Generation, string, []content.EmbeddingData) error {
	return nil
}
func (noopSearchDep) Search(context.Context, []retrieval.Route, corpus.Scope, retrieval.Request) ([]content.Candidate, error) {
	return nil, nil
}
func (noopSearchDep) Manifest() *plugins.Manifest {
	return &plugins.Manifest{
		ID:      "core.retrieve",
		Version: "1.0.0",
		Contributions: plugins.Contributions{Retrieval: &plugins.Retrieval{
			Profiles: map[string]plugins.RetrievalProfile{"default": {MaxLatencyMS: 500}},
		}},
	}
}
func (noopSearchDep) Configuration() json.RawMessage { return json.RawMessage(`{}`) }
func (noopSearchDep) Round(_ context.Context, q plugins.SearchRequest) ([]byte, error) {
	if q.Round == 1 {
		return json.Marshal(map[string]any{"requests": []any{map[string]any{"primitive": "bm25", "query_text": q.Query.Text, "k": q.Limit}}})
	}
	return json.Marshal(map[string]any{"ranking": map[string]any{"hits": []any{}}})
}

// Owns HTTP search panic -> 503 recovery, Retry-After header, admission lease
// release back to 0 (unblocking queued waiters), process survival, and
// redaction of query text, panic values, and API credentials from logs/bodies.
func TestSearchPanicRecoveryReturns503ReleasesAdmissionAndRedactsLogs(t *testing.T) {
	var logBuf bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	const (
		secretQuery  = "CONFIDENTIAL_QUERY_MARKER_2591"
		secretAPIKey = "SECRET_BEARER_KEY_2591"
		secretPanic  = "PANIC_PAYLOAD_SECRET_2591"
	)

	routing := &panicSearchRouting{}
	limiter := NewSearchAdmission(SearchAdmissionConfig{
		InitialLimit: 1,
		MinLimit:     1,
		MaxLimit:     2,
		MaxQueue:     2,
	})
	metrics := telemetry.NewLoadMetrics()
	keys := map[string]corpus.Scope{
		secretAPIKey: {Organization: "org_a", Actions: []string{"search:query", "content:read"}, Corpora: []string{"*"}},
	}
	handler, err := New(
		nil,
		content.Service{},
		retrieval.Service{Routing: routing, Projection: noopSearchDep{}, Ranker: noopSearchDep{}},
		uploads.Service{},
		keys,
		[]byte("cursor-key-0123456789abcdef0123456789"),
		WithSearchAdmission(limiter),
		WithLoadMetrics(metrics),
	)
	if err != nil {
		t.Fatal(err)
	}
	api := apicontract.Handler(t, handler)

	newReq := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v0/search", strings.NewReader(`{"query":"`+secretQuery+`","corpus_ids":["corpus_a"],"mode":"lexical"}`))
		r.Header.Set("Authorization", "Bearer "+secretAPIKey)
		r.Header.Set("Content-Type", "application/json")
		return r
	}

	panicCases := []struct {
		name      string
		wantType  string
		triggerFn func(context.Context) error
	}{
		{
			name:     "string panic carrying secret payload",
			wantType: "string",
			triggerFn: func(context.Context) error {
				panic(secretPanic + " " + secretQuery + " " + secretAPIKey)
			},
		},
		{
			name:     "nil pointer dereference runtime panic",
			wantType: "runtime.errorString",
			triggerFn: func(context.Context) error {
				var ptr *int
				_ = *ptr
				return nil
			},
		},
	}

	for _, tc := range panicCases {
		t.Run(tc.name, func(t *testing.T) {
			logBuf.Reset()
			routing.mu.Lock()
			routing.onAuth = tc.triggerFn
			routing.mu.Unlock()

			w := httptest.NewRecorder()
			api.ServeHTTP(w, newReq())

			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d body=%s; want 503", w.Code, w.Body.String())
			}
			retryAfter, convErr := strconv.Atoi(w.Header().Get("Retry-After"))
			if convErr != nil || retryAfter < 1 {
				t.Fatalf("Retry-After=%q; want positive integer seconds", w.Header().Get("Retry-After"))
			}
			var body struct {
				Code      string `json:"code"`
				Message   string `json:"message"`
				Retryable bool   `json:"retryable"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("invalid error JSON %q: %v", w.Body.String(), err)
			}
			if body.Code != "search_unavailable" || body.Message != "search unavailable" || !body.Retryable {
				t.Fatalf("unexpected error body: %+v", body)
			}
			if stats := limiter.Stats(); stats.InUse != 0 || stats.Queued != 0 || stats.Available != stats.Limit {
				t.Fatalf("admission stats after panic = %+v; want InUse=0 Queued=0 Available=%d", stats, stats.Limit)
			}

			// Verify logs record panic_type + stack and never leak query, panic text, or credentials.
			logged := logBuf.String()
			if !strings.Contains(logged, `"panic_type":"`+tc.wantType+`"`) {
				t.Fatalf("logs missing panic_type %q:\n%s", tc.wantType, logged)
			}
			if !strings.Contains(logged, `"stack":"goroutine `) {
				t.Fatalf("logs missing stack trace:\n%s", logged)
			}
			for _, forbidden := range []string{secretQuery, secretAPIKey, secretPanic} {
				if strings.Contains(logged, forbidden) {
					t.Fatalf("logs leaked %q:\n%s", forbidden, logged)
				}
				if strings.Contains(w.Body.String(), forbidden) {
					t.Fatalf("response body leaked %q: %s", forbidden, w.Body.String())
				}
			}
		})
	}

	// Verify a queued healthy search is cleanly admitted once a panicking
	// in-flight search unwinds and releases its admission slot.
	entered := make(chan struct{})
	release := make(chan struct{})
	routing.mu.Lock()
	routing.onAuth = func(context.Context) error {
		close(entered)
		<-release
		routing.mu.Lock()
		routing.onAuth = nil
		routing.mu.Unlock()
		panic("in-flight panic while waiter queued")
	}
	routing.mu.Unlock()

	panicDone := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		api.ServeHTTP(w, newReq())
		panicDone <- w.Code
	}()
	<-entered

	queuedDone := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		api.ServeHTTP(w, newReq())
		queuedDone <- w.Code
	}()

	// Wait until the second request is observed queued on the limiter (no sleep).
	for limiter.Stats().Queued == 0 {
		if stats := limiter.Stats(); stats.InUse == 0 {
			break
		}
	}
	close(release)

	if code := <-panicDone; code != http.StatusServiceUnavailable {
		t.Fatalf("panicking request status=%d; want 503", code)
	}
	if code := <-queuedDone; code != http.StatusOK {
		t.Fatalf("queued request after panic release status=%d; want 200", code)
	}
	if stats := limiter.Stats(); stats.InUse != 0 || stats.Queued != 0 {
		t.Fatalf("post-recovery admission stats=%+v; want InUse=0 Queued=0", stats)
	}
}

// panickedRanker is a ranker whose round failed with a panic retrieval
// recovered.
type panickedRanker struct{ noopSearchDep }

func (panickedRanker) Round(context.Context, plugins.SearchRequest) ([]byte, error) {
	return nil, fmt.Errorf("round: %w", retrieval.ErrPanicked)
}

// A dependency panic that retrieval recovers is answered as a handler panic:
// 503 search_unavailable with Retry-After, no phase timings, and the
// admission slot returned.
func TestSearchRecoveredDependencyPanicReturns503WithRetryAfter(t *testing.T) {
	limiter := NewSearchAdmission(SearchAdmissionConfig{InitialLimit: 1, MinLimit: 1, MaxLimit: 2, MaxQueue: 2})
	const key = "search-key"
	keys := map[string]corpus.Scope{key: {Organization: "org_a", Actions: []string{"search:query", "content:read"}, Corpora: []string{"*"}}}
	handler, err := New(nil, content.Service{}, retrieval.Service{Routing: &panicSearchRouting{}, Projection: noopSearchDep{}, Ranker: panickedRanker{}}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"), WithSearchAdmission(limiter))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v0/search", strings.NewReader(`{"query":"lanterne","corpus_ids":["corpus_a"],"mode":"lexical"}`))
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	apicontract.Handler(t, handler).ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"search_unavailable"`) {
		t.Fatalf("status=%d body=%s; want 503 search_unavailable", w.Code, w.Body.String())
	}
	if retryAfter, err := strconv.Atoi(w.Header().Get("Retry-After")); err != nil || retryAfter < 1 {
		t.Fatalf("Retry-After=%q; want positive integer seconds", w.Header().Get("Retry-After"))
	}
	for _, header := range []string{"Server-Timing", "X-Quivr-Degraded", "X-Quivr-Degraded-Reason"} {
		if got := w.Header().Get(header); got != "" {
			t.Fatalf("%s=%q on a recovered panic; want none", header, got)
		}
	}
	if stats := limiter.Stats(); stats.InUse != 0 || stats.Available != stats.Limit {
		t.Fatalf("admission stats after a recovered panic = %+v; want the slot returned", stats)
	}
}
