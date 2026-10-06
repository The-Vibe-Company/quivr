package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

type heldSearchRouting struct {
	arrived chan struct{}
	calls   atomic.Int32
}

func (h *heldSearchRouting) Authorize(ctx context.Context, _ corpus.Scope, _ []string) error {
	if h.calls.Add(1) <= 64 {
		h.arrived <- struct{}{}
		<-ctx.Done()
	}
	return retrieval.ErrUnavailable
}
func (*heldSearchRouting) Generation(context.Context, string, string) (content.Generation, error) {
	return content.Generation{}, retrieval.ErrUnavailable
}

// Admission protects the whole API process across callers before any storage
// or plugin work, and cancellation releases capacity without a queue or sleep.
func TestSearchCapacityRejectsExcessAndReleasesCanceledRequests(t *testing.T) {
	set, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: "../../../sdks/go/examples/fusion-retriever/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:1"}})
	if err != nil {
		t.Fatal(err)
	}
	live, err := plugins.NewLive("capacity", set)
	if err != nil {
		t.Fatal(err)
	}
	route := &heldSearchRouting{arrived: make(chan struct{}, 64)}
	service := retrieval.Service{Routing: route, ProfilesRouter: pluginhttp.LiveRetriever{Live: live}}
	keys := map[string]corpus.Scope{
		"caller-a": {Organization: "org_a", Actions: []string{"search:query", "content:read"}, Corpora: []string{"*"}},
		"caller-b": {Organization: "org_b", Actions: []string{"search:query", "content:read"}, Corpora: []string{"*"}},
	}
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, service, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	request := func(ctx context.Context, key string) *http.Request {
		r := httptest.NewRequest("POST", "/v0/search", strings.NewReader(`{"query":"library","corpus_ids":["corpus_a"],"profile":"deep"}`)).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	var cancels []context.CancelFunc
	done := make(chan int, 64)
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
		for len(cancels) > 0 {
			<-done
			cancels = cancels[1:]
		}
	}()
	for i := 0; i < 64; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		key := "caller-a"
		if i%2 == 1 {
			key = "caller-b"
		}
		go func() { w := httptest.NewRecorder(); handler.ServeHTTP(w, request(ctx, key)); done <- w.Code }()
	}
	for i := 0; i < 64; i++ {
		<-route.arrived
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request(context.Background(), "caller-a"))
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 503 || body["code"] != "search_unavailable" || body["retryable"] != true || w.Header().Get("Retry-After") != "1" || route.calls.Load() != 64 {
		t.Fatalf("capacity: status=%d body=%v Retry-After=%q dependency calls=%d; want retryable 503, Retry-After 1, 64 calls", w.Code, body, w.Header().Get("Retry-After"), route.calls.Load())
	}
	cancels[0]()
	<-done
	cancels = cancels[1:]
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, request(context.Background(), "caller-b"))
	if route.calls.Load() != 65 || w.Header().Get("Retry-After") != "" {
		t.Fatalf("canceled search did not release capacity: calls=%d Retry-After=%q", route.calls.Load(), w.Header().Get("Retry-After"))
	}
}
