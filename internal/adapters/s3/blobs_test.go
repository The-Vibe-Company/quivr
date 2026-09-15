package s3_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	store "github.com/The-Vibe-Company/quivr-v2/internal/adapters/s3"
)

func TestImmutableObjectSurvivesLostPutAcknowledgment(t *testing.T) {
	path := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if path == "" {
		t.Skip("real S3 adapter suite runs inside make verify")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		S3 store.Config `json:"s3"`
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(cfg.S3.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	var lost atomic.Bool
	proxy.ModifyResponse = func(r *http.Response) error {
		if r.Request.Method == "PUT" && r.StatusCode >= 200 && r.StatusCode < 300 && lost.CompareAndSwap(false, true) {
			r.Body.Close()
			return errors.New("synthetic lost acknowledgment after successful storage")
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) { w.WriteHeader(503) }
	server := httptest.NewServer(proxy)
	defer server.Close()
	cfg.S3.Endpoint = server.URL
	blobs := store.New(cfg.S3)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	first, err := blobs.Put(ctx, "adapter-lost-response", []byte("Canonical bytes 🌞"))
	if err != nil {
		t.Fatal(err)
	}
	if !lost.Load() {
		t.Fatal("failure injection never followed a successful PUT")
	}
	second, err := blobs.Put(ctx, "adapter-lost-response", []byte("Canonical bytes 🌞"))
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatal("ambiguous replay changed identity")
	}
	bytes, err := blobs.Read(ctx, first)
	if err != nil || string(bytes) != "Canonical bytes 🌞" {
		t.Fatal("immutable bytes unavailable", err)
	}
	corrupt := first
	corrupt.SHA256 = "wrong-digest"
	if _, err = blobs.Read(ctx, corrupt); err == nil {
		t.Fatal("wrong checksum was accepted")
	}
	other, err := blobs.Put(ctx, "another-organization", []byte("Canonical bytes 🌞"))
	if err != nil {
		t.Fatal(err)
	}
	if other.Key == first.Key {
		t.Fatal("object identity crossed Organization")
	}
}
