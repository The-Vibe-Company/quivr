package s3_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	store "github.com/The-Vibe-Company/quivr/internal/adapters/s3"
	"github.com/The-Vibe-Company/quivr/internal/content"
)

// The S3 transport accepts bounded vector files larger than canonical objects,
// including immutable PUT verification, without widening canonical hydration.
func TestVectorFileObjectBound(t *testing.T) {
	data := bytes.Repeat([]byte{1, 2, 3, 4}, (2<<20)/4+1)
	var stored []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			var err error
			stored, err = io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(200)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(stored)))
		w.Write(stored)
	}))
	defer server.Close()
	blobs := store.New(store.Config{Endpoint: server.URL, Bucket: "vectors", AccessKey: "local-test", SecretKey: "local-test"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	blob, err := blobs.PutVectorFile(ctx, "example", data)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := blobs.ReadVectorFile(ctx, blob)
	if err != nil || !bytes.Equal(raw, data) {
		t.Fatalf("vector file read: %v", err)
	}
	if _, err = blobs.Read(ctx, blob); err == nil {
		t.Fatal("canonical object bound was widened")
	}
	blob.Size = content.MaxVectorFileBytes + 1
	if _, err = blobs.ReadVectorFile(ctx, blob); err == nil {
		t.Fatal("unbounded vector object accepted")
	}
}
