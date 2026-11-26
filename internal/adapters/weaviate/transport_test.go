package weaviate_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
)

func TestDefaultClientRetainsConnectionsForConcurrentIndexRequests(t *testing.T) {
	store := weaviate.New("http://index.test")
	transport := store.Client.Transport
	for {
		unwrapper, ok := transport.(interface{ Unwrap() http.RoundTripper })
		if !ok {
			break
		}
		transport = unwrapper.Unwrap()
	}
	pool, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("index transport = %T, want an HTTP pool", transport)
	}
	if pool.MaxIdleConns != 512 || pool.MaxIdleConnsPerHost != 128 || pool.IdleConnTimeout != 90*time.Second {
		t.Fatalf("idle pool total=%d per host=%d timeout=%s, want 512, 128, 90s", pool.MaxIdleConns, pool.MaxIdleConnsPerHost, pool.IdleConnTimeout)
	}

	// Unwrap must also reach *http.Transport when authTransport wraps the pool.
	authed := weaviate.New("http://index.test", "fixture-api-key")
	authedTransport := authed.Client.Transport
	for {
		unwrapper, ok := authedTransport.(interface{ Unwrap() http.RoundTripper })
		if !ok {
			break
		}
		authedTransport = unwrapper.Unwrap()
	}
	if _, ok := authedTransport.(*http.Transport); !ok {
		t.Fatalf("authenticated index transport = %T, want *http.Transport", authedTransport)
	}
}

func TestDefaultClientUsesCustomRoundTripper(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	requests := 0
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.Method != http.MethodGet || req.URL.Path != "/v1/.well-known/ready" {
			t.Errorf("readiness request = %s %s", req.Method, req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	store := weaviate.New("http://index.test")
	if err := store.Ready(t.Context()); err != nil || requests != 1 {
		t.Fatalf("ready error=%v custom transport requests=%d, want one successful request", err, requests)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
