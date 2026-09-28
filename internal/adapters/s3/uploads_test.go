package s3_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"sync/atomic"
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

func TestStreamedDepositIsStoredOnceAndVerified(t *testing.T) {
	cfg := transferConfig(t)
	blobs := store.New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// Larger than the 2 MiB canonical-object read bound: verification must stream.
	data := bytes.Repeat([]byte("pièce jointe "), 300_000)
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	objectKey := "adapter-transfers/deposit-" + digest
	for i := 0; i < 2; i++ { // A repeated deposit of the same bytes converges.
		if err := blobs.PutStream(ctx, objectKey, bytes.NewReader(data), int64(len(data)), digest, "application/pdf"); err != nil {
			t.Fatalf("deposit %d: %v", i, err)
		}
	}
	if err := blobs.Verify(ctx, objectKey, int64(len(data)), digest); err != nil {
		t.Fatalf("deposited bytes did not verify: %v", err)
	}
	if err := blobs.Verify(ctx, objectKey, int64(len(data)), hex.EncodeToString(make([]byte, 32))); !errors.Is(err, uploads.ErrVerificationMismatch) {
		t.Fatal("streamed verification did not detect a digest mismatch")
	}
}

// Storage may refuse a conditional PUT before reading the body and close the
// connection, so the client sees a reset instead of 412; a stored PUT can also
// lose its response. The proxy injects both as TCP resets in front of real
// storage. A deposit resolves them by observing the object, never by trusting
// an ambiguous transport outcome, and a present object of the wrong length is
// not accepted.
func TestStreamedDepositResolvesAmbiguousStorageOutcomes(t *testing.T) {
	cfg := transferConfig(t)
	target, err := url.Parse(cfg.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	var puts atomic.Int64
	var loseNext, refuseAll atomic.Bool
	errReset := errors.New("synthetic connection reset")
	reset := func(w http.ResponseWriter) {
		conn, _, hijackErr := w.(http.Hijacker).Hijack()
		if hijackErr != nil {
			panic(hijackErr)
		}
		_ = conn.(*net.TCPConn).SetLinger(0) // Close with RST, as the storage peer does.
		_ = conn.Close()
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ModifyResponse = func(r *http.Response) error {
		if r.Request.Method == http.MethodPut && (r.StatusCode == http.StatusPreconditionFailed || loseNext.CompareAndSwap(true, false)) {
			r.Body.Close()
			return errReset
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) { reset(w) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts.Add(1)
			if refuseAll.Load() {
				reset(w)
				return
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	defer server.Close()
	cfg.Endpoint = server.URL
	blobs := store.New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	data := bytes.Repeat([]byte("pièce jointe ambiguë "), 200_000)
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	objectKey := fmt.Sprintf("adapter-transfers/ambiguous-%d-%s", time.Now().UnixNano(), digest)

	// A stored PUT whose response is lost is proven by the stored object.
	loseNext.Store(true)
	if err = blobs.PutStream(ctx, objectKey, bytes.NewReader(data), int64(len(data)), digest, "application/pdf"); err != nil {
		t.Fatalf("lost acknowledgement of a stored deposit: %v", err)
	}
	if loseNext.Load() {
		t.Fatal("failure injection never followed a stored PUT")
	}
	if err = blobs.Verify(ctx, objectKey, int64(len(data)), digest); err != nil {
		t.Fatalf("deposited bytes did not verify: %v", err)
	}
	// A repeated deposit of present bytes converges without resending them.
	sent := puts.Load()
	if err = blobs.PutStream(ctx, objectKey, bytes.NewReader(data), int64(len(data)), digest, "application/pdf"); err != nil {
		t.Fatalf("repeated deposit: %v", err)
	}
	if puts.Load() != sent {
		t.Fatalf("repeated deposit resent the body (%d PUTs)", puts.Load()-sent)
	}
	// A present object of another length is not mistaken for this deposit:
	// the conditional PUT is refused (reset) and the deposit fails.
	shorter := data[:len(data)-1]
	shortSum := sha256.Sum256(shorter)
	if err = blobs.PutStream(ctx, objectKey, bytes.NewReader(shorter), int64(len(shorter)), hex.EncodeToString(shortSum[:]), "application/pdf"); err == nil {
		t.Fatal("a present object of another length was accepted")
	}
	// A deposit that storage never receives stays an error.
	refuseAll.Store(true)
	absentKey := objectKey + "-absent"
	if err = blobs.PutStream(ctx, absentKey, bytes.NewReader(data), int64(len(data)), digest, "application/pdf"); err == nil {
		t.Fatal("a deposit that never reached storage was accepted")
	}
	if err = blobs.Verify(ctx, absentKey, int64(len(data)), digest); err == nil {
		t.Fatal("refused deposit is present")
	}
}
