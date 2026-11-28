package s3_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	s3store "github.com/The-Vibe-Company/quivr/internal/adapters/s3"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

// Owns S3 operation counting across Put (write + verification read), Read,
// Verify (HeadObject + fallback GetObject), and Ready (HeadBucket).
func TestStoreCountsS3Operations(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/bench-bucket/")
		if r.URL.Path == "/bench-bucket" || r.URL.Path == "/bench-bucket/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			objects[key] = body
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case http.MethodHead:
			// Return 501 so Verify falls back to GetObject and exercises both counters.
			w.WriteHeader(http.StatusNotImplemented)
		case http.MethodGet:
			mu.Lock()
			data, ok := objects[key]
			mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	store := s3store.New(s3store.Config{
		Endpoint:  server.URL,
		AccessKey: "test-access",
		SecretKey: "test-secret",
		Bucket:    "bench-bucket",
	})
	metrics := telemetry.NewDependencyMetrics()
	store.SetMetrics(metrics)

	ctx := context.Background()
	if err := store.Ready(ctx); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	payload := []byte("benchmark neutral text")
	blob, err := store.Put(ctx, "org-bench", payload)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := store.Read(ctx, blob); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if err := store.Verify(ctx, blob.Key, blob.Size, blob.SHA256); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	var b strings.Builder
	metrics.Write(&b)
	text := b.String()
	// This store does not echo the PUT checksum, so Put issues 1 PutObject + 1
	// verification GetObject; Read issues 1 GetObject;
	// Verify issues 1 HeadObject + 1 fallback GetObject; Ready issues 1 HeadBucket.
	for _, want := range []string{
		`quivr_dependency_calls_total{dependency="s3",protocol="http",operation="bucket"} 1`,
		`quivr_dependency_calls_total{dependency="s3",protocol="http",operation="put_object"} 1`,
		`quivr_dependency_calls_total{dependency="s3",protocol="http",operation="head_object"} 1`,
		`quivr_dependency_calls_total{dependency="s3",protocol="http",operation="get_object"} 3`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
}
