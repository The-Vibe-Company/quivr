package s3_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	store "github.com/The-Vibe-Company/quivr/internal/adapters/s3"
	"github.com/The-Vibe-Company/quivr/internal/content"
)

// Concurrent hydration must reuse its connections between batches instead of
// exhausting ephemeral ports during sustained searches. The server holds each
// cohort until every read arrives; no timing assumption forces concurrency.
func TestConcurrentCanonicalReadsReuseConnections(t *testing.T) {
	const readers = 32
	type wave struct {
		arrived chan struct{}
		release chan struct{}
	}
	var current atomic.Pointer[wave]
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		batch := current.Load()
		batch.arrived <- struct{}{}
		select {
		case <-batch.release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Length", "15")
		fmt.Fprint(w, "canonical bytes")
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); server.Close() }()
	blobs := store.New(store.Config{Endpoint: server.URL, AccessKey: "local-test", SecretKey: "local-test", Bucket: "canonical"})
	blob := content.Blob{Key: "org/sha256/object", SHA256: content.Hash([]byte("canonical bytes")), Size: 15}
	for cohort := 0; cohort < 2; cohort++ {
		batch := &wave{arrived: make(chan struct{}, readers), release: make(chan struct{})}
		current.Store(batch)
		results := make(chan error, readers)
		for i := 0; i < readers; i++ {
			go func() {
				data, err := blobs.Read(ctx, blob)
				if err == nil && string(data) != "canonical bytes" {
					err = fmt.Errorf("read %q", data)
				}
				results <- err
			}()
		}
		for i := 0; i < readers; i++ {
			select {
			case <-batch.arrived:
			case <-ctx.Done():
				close(batch.release)
				t.Fatal("concurrent reads did not reach storage", ctx.Err())
			}
		}
		close(batch.release)
		for i := 0; i < readers; i++ {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := connections.Load(); got != readers {
		t.Fatalf("two %d-reader cohorts opened %d connections; want %d reused connections", readers, got, readers)
	}
}
