package s3_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

type outcome int

const (
	succeeds outcome = iota
	corrupt          // errors.Is(err, content.ErrArtifactCorrupt)
	missing          // errors.Is(err, content.ErrArtifactMissing)
	fails            // an error with neither durable-artifact classification
)

func classify(err error) outcome {
	switch {
	case err == nil:
		return succeeds
	case errors.Is(err, content.ErrArtifactCorrupt):
		return corrupt
	case errors.Is(err, content.ErrArtifactMissing):
		return missing
	default:
		return fails
	}
}

func dropConnection(t *testing.T, w http.ResponseWriter) {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		t.Errorf("hijack: %v", err)
		return
	}
	conn.Close()
}

// Owns the write contract: Put reports success only when S3 acknowledged the
// validated checksum of a new object or a read-back proved the stored bytes.
// Every ambiguous outcome must issue that read-back.
func TestPutProvesStoredBytes(t *testing.T) {
	data := []byte("Canonical bytes 🌞")
	other := []byte("Different bytes 🌧")
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f *fakeS3, key string)
		want  outcome
		gets  int64
	}{
		{name: "checksum acknowledged", want: succeeds, gets: 0},
		{name: "store ignores checksum", want: succeeds, gets: 1, setup: func(t *testing.T, f *fakeS3, key string) {
			f.onPut = func(w http.ResponseWriter, key string, body []byte, f *fakeS3) bool {
				f.set(key, body)
				w.WriteHeader(http.StatusOK)
				return true
			}
		}},
		{name: "acknowledged checksum differs", want: corrupt, gets: 1, setup: func(t *testing.T, f *fakeS3, key string) {
			f.onPut = func(w http.ResponseWriter, key string, body []byte, f *fakeS3) bool {
				f.set(key, other)
				w.Header().Set("X-Amz-Checksum-Sha256", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
				w.WriteHeader(http.StatusOK)
				return true
			}
		}},
		{name: "composite checksum", want: corrupt, gets: 1, setup: func(t *testing.T, f *fakeS3, key string) {
			f.onPut = func(w http.ResponseWriter, key string, body []byte, f *fakeS3) bool {
				f.set(key, other)
				sum := sha256.Sum256(body)
				w.Header().Set("X-Amz-Checksum-Sha256", base64.StdEncoding.EncodeToString(sum[:]))
				w.Header().Set("X-Amz-Checksum-Type", "COMPOSITE")
				w.WriteHeader(http.StatusOK)
				return true
			}
		}},
		// The SDK retries the reset attempt, which then meets 412 on the
		// committed key; the bytes are proven by GET either way.
		{name: "committed then connection reset", want: succeeds, gets: 1, setup: func(t *testing.T, f *fakeS3, key string) {
			f.onPut = func(w http.ResponseWriter, key string, body []byte, f *fakeS3) bool {
				if f.puts.Load() > 1 {
					return false
				}
				f.set(key, body)
				dropConnection(t, w)
				return true
			}
		}},
		{name: "existing key with identical bytes", want: succeeds, gets: 1, setup: func(t *testing.T, f *fakeS3, key string) {
			f.set(key, data)
		}},
		{name: "existing key with different bytes", want: corrupt, gets: 1, setup: func(t *testing.T, f *fakeS3, key string) {
			f.set(key, other)
		}},
		// 501 is not retried by the SDK, keeping the case free of backoff waits;
		// retried 5xx statuses reach the same read-back after the last attempt.
		{name: "server error before commit", want: fails, gets: 1, setup: func(t *testing.T, f *fakeS3, key string) {
			f.onPut = func(w http.ResponseWriter, key string, body []byte, f *fakeS3) bool {
				w.WriteHeader(http.StatusNotImplemented)
				return true
			}
		}},
		{name: "server error after commit", want: succeeds, gets: 1, setup: func(t *testing.T, f *fakeS3, key string) {
			f.onPut = func(w http.ResponseWriter, key string, body []byte, f *fakeS3) bool {
				f.set(key, body)
				w.WriteHeader(http.StatusNotImplemented)
				return true
			}
		}},
		{name: "read-back unavailable", want: fails, gets: 1, setup: func(t *testing.T, f *fakeS3, key string) {
			f.set(key, data)
			f.onGet = func(w http.ResponseWriter, key string, stored []byte, ok bool) bool {
				w.WriteHeader(http.StatusForbidden)
				return true
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeS3(t)
			key := content.Hash([]byte("example-org")) + "/sha256/" + content.Hash(data)
			if tc.setup != nil {
				tc.setup(t, f, key)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			blob, err := f.store().Put(ctx, "example-org", data)
			if got := classify(err); got != tc.want {
				t.Fatalf("Put outcome %d (%v); want %d", got, err, tc.want)
			}
			if got := f.gets.Load(); got != tc.gets {
				t.Fatalf("Put issued %d read-backs; want %d", got, tc.gets)
			}
			if err == nil {
				if stored, _ := f.lookup(key); blob.Key != key || !bytes.Equal(stored, data) {
					t.Fatalf("Put succeeded with blob %+v but stored %q", blob, stored)
				}
			}
		})
	}
}

// Owns the read contract: a read returns bytes only when they are exactly the
// blob's size and digest, and classifies storage faults.
func TestReadVerifiesStoredBytes(t *testing.T) {
	data := []byte("Canonical bytes 🌞")
	blob := content.Blob{Key: "org/sha256/" + content.Hash(data), SHA256: content.Hash(data), Size: int64(len(data))}
	reply := func(body []byte, declared int) func(w http.ResponseWriter, _ string, _ []byte, _ bool) bool {
		return func(w http.ResponseWriter, _ string, _ []byte, _ bool) bool {
			if declared >= 0 {
				w.Header().Set("Content-Length", strconv.Itoa(declared))
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			if declared < 0 {
				// Without a length the server streams the body chunked.
				http.NewResponseController(w).Flush()
			}
			return true
		}
	}
	flipped := bytes.Clone(data)
	flipped[0] ^= 1
	for _, tc := range []struct {
		name  string
		onGet func(w http.ResponseWriter, key string, stored []byte, ok bool) bool
		want  outcome
	}{
		{name: "exact body", onGet: reply(data, len(data)), want: succeeds},
		{name: "exact body of unknown length", onGet: reply(data, -1), want: succeeds},
		{name: "absent object", want: missing},
		{name: "unavailable object", want: fails, onGet: func(w http.ResponseWriter, _ string, _ []byte, _ bool) bool {
			w.WriteHeader(http.StatusForbidden)
			return true
		}},
		{name: "truncated transfer", onGet: reply(data[:5], len(data)), want: fails},
		{name: "shorter object", onGet: reply(data[:5], 5), want: corrupt},
		{name: "shorter object of unknown length", onGet: reply(data[:5], -1), want: corrupt},
		{name: "wrong bytes", onGet: reply(flipped, len(flipped)), want: corrupt},
		{name: "oversized object", onGet: reply(append(bytes.Clone(data), 'x'), len(data)+1), want: corrupt},
		{name: "oversized object of unknown length", onGet: reply(append(bytes.Clone(data), 'x'), -1), want: corrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeS3(t)
			f.onGet = tc.onGet
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			got, err := f.store().Read(ctx, blob)
			if outcome := classify(err); outcome != tc.want {
				t.Fatalf("Read outcome %d (%v); want %d", outcome, err, tc.want)
			}
			if err == nil && !bytes.Equal(got, data) {
				t.Fatalf("Read returned %q; want %q", got, data)
			}
		})
	}
}

// A blob record claiming the largest vector size must not make a short stored
// object allocate that size before its bytes disprove it.
func TestShortVectorObjectDoesNotAllocateDeclaredSize(t *testing.T) {
	f := newFakeS3(t)
	blob := content.Blob{Key: "org/sha256/declared", SHA256: content.Hash(nil), Size: content.MaxVectorFileBytes}
	f.set(blob.Key, []byte("short stored object"))
	blobs := f.store()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := blobs.ReadVectorFile(ctx, blob)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, content.ErrArtifactCorrupt) {
		t.Fatalf("short object read: %v; want ErrArtifactCorrupt", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 16<<20 {
		t.Fatalf("short object read allocated %d bytes for a %d-byte declared size", allocated, blob.Size)
	}
}
