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

	store "github.com/The-Vibe-Company/quivr-v2/internal/adapters/s3"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
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
}
