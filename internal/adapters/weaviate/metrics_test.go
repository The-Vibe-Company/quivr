package weaviate_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

// Owns Weaviate HTTP call classification through Store's real transport stack.
func TestStoreCountsClassifiedHTTPCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/.well-known/ready":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/graphql":
			_, _ = w.Write([]byte(`{"data":{"Get":{"QuivrTextV4":[]}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/batch/objects":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/schema/"):
			_, _ = w.Write([]byte(`{"class":"QuivrTextV4"}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/objects/"):
			_, _ = w.Write([]byte(`{"id":"obj-1"}`))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/objects/"):
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	store := weaviate.New(server.URL)
	metrics := telemetry.NewDependencyMetrics()
	store.SetMetrics(metrics)

	ctx := context.Background()
	if err := store.Ready(ctx); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/graphql"},
		{http.MethodPost, "/v1/graphql"},
		{http.MethodPost, "/v1/batch/objects"},
		{http.MethodGet, "/v1/schema/QuivrTextV4"},
		{http.MethodGet, "/v1/objects/QuivrTextV4/obj-1"},
		{http.MethodDelete, "/v1/objects/QuivrTextV4/obj-1"},
	} {
		req, err := http.NewRequestWithContext(ctx, tc.method, server.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := store.Client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		res.Body.Close()
	}

	var b strings.Builder
	metrics.Write(&b)
	text := b.String()
	for _, want := range []string{
		`quivr_dependency_calls_total{dependency="weaviate",protocol="http",operation="ready"} 1`,
		`quivr_dependency_calls_total{dependency="weaviate",protocol="http",operation="query"} 2`,
		`quivr_dependency_calls_total{dependency="weaviate",protocol="http",operation="write"} 1`,
		`quivr_dependency_calls_total{dependency="weaviate",protocol="http",operation="schema"} 1`,
		`quivr_dependency_calls_total{dependency="weaviate",protocol="http",operation="read"} 1`,
		`quivr_dependency_calls_total{dependency="weaviate",protocol="http",operation="delete"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
}
