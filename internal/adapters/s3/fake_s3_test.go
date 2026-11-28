package s3_test

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	store "github.com/The-Vibe-Company/quivr/internal/adapters/s3"
)

// fakeS3 is an in-memory, path-style S3 endpoint with the PutObject semantics
// the store relies on: If-None-Match: * refuses an existing key with 412, a
// supplied x-amz-checksum-sha256 is validated against the body (400 BadDigest)
// and echoed on success. Hooks inject faults per request.
type fakeS3 struct {
	t        testing.TB
	server   *httptest.Server
	mu       sync.Mutex
	objects  map[string][]byte
	requests atomic.Int64
	puts     atomic.Int64
	gets     atomic.Int64
	// onPut runs after the body is read and validated, before the fake replies.
	// It returns true when it wrote (or deliberately dropped) the response.
	onPut func(w http.ResponseWriter, key string, body []byte, f *fakeS3) bool
	// onGet replaces the GET reply when it returns true.
	onGet func(w http.ResponseWriter, key string, stored []byte, ok bool) bool
}

const fakeBucket = "fake-bucket"

func newFakeS3(t testing.TB) *fakeS3 {
	f := &fakeS3{t: t, objects: map[string][]byte{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeS3) store() *store.Store {
	return store.New(store.Config{Endpoint: f.server.URL, AccessKey: "local-test", SecretKey: "local-test", Bucket: fakeBucket})
}

func (f *fakeS3) set(key string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = data
}

func (f *fakeS3) delete(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
}

func (f *fakeS3) lookup(key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[key]
	return data, ok
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	key := strings.TrimPrefix(r.URL.Path, "/"+fakeBucket+"/")
	switch r.Method {
	case http.MethodPut:
		f.puts.Add(1)
		body := make([]byte, max(r.ContentLength, 0))
		if _, err := io.ReadFull(r.Body, body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		checksum := r.Header.Get("X-Amz-Checksum-Sha256")
		sum := sha256.Sum256(body)
		if checksum != "" && checksum != base64.StdEncoding.EncodeToString(sum[:]) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "<Error><Code>BadDigest</Code></Error>")
			return
		}
		if f.onPut != nil && f.onPut(w, key, body, f) {
			return
		}
		f.mu.Lock()
		_, exists := f.objects[key]
		if !exists {
			f.objects[key] = body
		}
		f.mu.Unlock()
		if exists && r.Header.Get("If-None-Match") == "*" {
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = io.WriteString(w, "<Error><Code>PreconditionFailed</Code></Error>")
			return
		}
		if checksum != "" {
			w.Header().Set("X-Amz-Checksum-Sha256", checksum)
			w.Header().Set("X-Amz-Checksum-Type", "FULL_OBJECT")
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		f.gets.Add(1)
		stored, ok := f.lookup(key)
		if f.onGet != nil && f.onGet(w, key, stored, ok) {
			return
		}
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code></Error>")
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(stored)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(stored)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
