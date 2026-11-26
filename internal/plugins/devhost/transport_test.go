package devhost

import (
	"net/http"
	"testing"
	"time"
)

func TestDefaultClientRetainsConnectionsForConcurrentInvocations(t *testing.T) {
	pool, ok := httpClient().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("default plugin transport = %T, want a dedicated HTTP pool", httpClient().Transport)
	}
	if pool.MaxIdleConns != 512 || pool.MaxIdleConnsPerHost != 128 || pool.IdleConnTimeout != 90*time.Second {
		t.Fatalf("idle pool total=%d per host=%d timeout=%s, want 512, 128, 90s", pool.MaxIdleConns, pool.MaxIdleConnsPerHost, pool.IdleConnTimeout)
	}
}
