package weaviate_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
)

func TestDefaultClientRetainsConnectionsForConcurrentIndexRequests(t *testing.T) {
	store := weaviate.New("http://index.test")
	pool, ok := store.Client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("index transport = %T, want an HTTP pool", store.Client.Transport)
	}
	if pool.MaxIdleConns != 512 || pool.MaxIdleConnsPerHost != 128 || pool.IdleConnTimeout != 90*time.Second {
		t.Fatalf("idle pool total=%d per host=%d timeout=%s, want 512, 128, 90s", pool.MaxIdleConns, pool.MaxIdleConnsPerHost, pool.IdleConnTimeout)
	}
}
