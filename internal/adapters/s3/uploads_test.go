package s3_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	store "github.com/The-Vibe-Company/quivr/internal/adapters/s3"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

func transferConfig(t *testing.T) store.Config {
	t.Helper()
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
	return cfg.S3
}

func TestPresignedTransferVerifyAndRange(t *testing.T) {
	cfg := transferConfig(t)
	blobs := store.New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	objectKey := "adapter-transfers/upload-object"
	data := []byte("Ligne un\nLigne deux\n")
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])

	url, headers, err := blobs.PresignPut(ctx, objectKey, int64(len(data)), digest, "text/plain", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode/100 != 2 {
		t.Fatalf("presigned transfer refused: %d", response.StatusCode)
	}
	if err = blobs.Verify(ctx, objectKey, int64(len(data)), digest); err != nil {
		t.Fatalf("transferred bytes did not verify: %v", err)
	}
	other := sha256.Sum256([]byte("different expectation"))
	if err = blobs.Verify(ctx, objectKey, int64(len(data)), hex.EncodeToString(other[:])); !errors.Is(err, uploads.ErrVerificationMismatch) {
		t.Fatal("altered expectation was not reported as a mismatch")
	}
	if err = blobs.Verify(ctx, "adapter-transfers/absent", int64(len(data)), digest); err == nil {
		t.Fatal("absent object verified")
	}
	window, err := blobs.ReadRange(ctx, objectKey, 0, 4)
	if err != nil || string(window) != "Ligne" {
		t.Fatalf("range read failed: %q %v", window, err)
	}
	// A signed GET reference reads exactly the stored bytes without credentials.
	signed, expires, err := blobs.PresignGet(ctx, objectKey, time.Minute)
	if err != nil || !expires.After(time.Now()) {
		t.Fatalf("signed reference: %v", err)
	}
	got, err := http.Get(signed)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(got.Body)
	got.Body.Close()
	if got.StatusCode != 200 || !bytes.Equal(body, data) {
		t.Fatalf("signed GET returned %d %q", got.StatusCode, body)
	}
}

// A connector attachment reaches storage only through a grant: a presigned PUT
// pinned to its length, checksum and media type. Bytes other than the
// granted ones never verify, whether storage refuses them or not, and a
// granted object larger than the 2 MiB canonical-object read bound verifies
// by streaming.
func TestGrantedUploadsVerifyOnlyTheGrantedBytes(t *testing.T) {
	cfg := transferConfig(t)
	blobs := store.New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	data := bytes.Repeat([]byte("pièce jointe "), 300_000)
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	put := func(objectKey string, body []byte) {
		t.Helper()
		url, headers, err := blobs.PresignPut(ctx, objectKey, int64(len(data)), digest, "application/pdf", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		request, _ := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		request.ContentLength = int64(len(body))
		if response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request); err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
	}
	granted := "adapter-transfers/granted-" + digest
	put(granted, data)
	if err := blobs.Verify(ctx, granted, int64(len(data)), digest); err != nil {
		t.Fatalf("granted bytes did not verify: %v", err)
	}
	altered := bytes.Clone(data)
	altered[0] = 'P'
	other := "adapter-transfers/altered-" + digest
	put(other, altered)
	if err := blobs.Verify(ctx, other, int64(len(data)), digest); err == nil {
		t.Fatal("bytes other than the granted ones verified")
	}
}
