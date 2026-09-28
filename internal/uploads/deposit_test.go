package uploads_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

// streamingStorage hashes what it receives without retaining it, like S3.
type streamingStorage struct {
	objects map[string]string // key -> sha256
	putErr  error
}

func (s *streamingStorage) PutStream(_ context.Context, key string, r io.ReadSeeker, size int64, sha256hex, _ string) error {
	if s.putErr != nil {
		return s.putErr
	}
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil || n != size || hex.EncodeToString(h.Sum(nil)) != sha256hex {
		return errors.New("stream does not match its declaration")
	}
	s.objects[key] = sha256hex
	return nil
}
func (s *streamingStorage) PresignPut(context.Context, string, int64, string, string, time.Duration) (string, map[string]string, error) {
	return "", nil, nil
}
func (s *streamingStorage) Verify(_ context.Context, key string, _ int64, sha256hex string) error {
	if s.objects[key] != sha256hex {
		return uploads.ErrVerificationMismatch
	}
	return nil
}

type repeatReader struct{ b byte }

func (r repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.b
	}
	return len(p), nil
}

func depositService() (uploads.Service, *memoryStore, *streamingStorage) {
	store, storage := newMemoryStore(), &streamingStorage{objects: map[string]string{}}
	return uploads.Service{Store: store, Transfer: storage, Writer: storage}, store, storage
}

func TestDepositStoresAVerifiedBlobWithTheUploadIdentity(t *testing.T) {
	svc, store, _ := depositService()
	id, size, err := svc.Deposit(context.Background(), "org_a", strings.NewReader("%PDF-1.7 attachment"), "application/pdf", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	sum := content.Hash([]byte("%PDF-1.7 attachment"))
	if size != 19 || id != content.StableID("blob", "org_a", sum, "application/pdf") {
		t.Fatalf("id %s size %d", id, size)
	}
	blob, err := svc.Blob(context.Background(), "org_a", id)
	if err != nil || blob.SHA256 != sum || blob.MediaType != "application/pdf" {
		t.Fatalf("blob %+v err %v", blob, err)
	}
	if _, err = store.Blob(context.Background(), "org_b", id); err == nil {
		t.Fatal("a deposited Blob must stay in its Organization")
	}
}

func TestDepositRefusesOversizedEmptyAndInvalidStreams(t *testing.T) {
	svc, _, _ := depositService()
	for name, tc := range map[string]struct {
		r         io.Reader
		mediaType string
	}{
		"oversized":  {io.LimitReader(repeatReader{'a'}, 11), "text/plain"},
		"empty":      {strings.NewReader(""), "text/plain"},
		"media type": {strings.NewReader("x"), ""},
	} {
		if _, _, err := svc.Deposit(context.Background(), "org_a", tc.r, tc.mediaType, 10); !errors.Is(err, content.ErrInvalid) {
			t.Errorf("%s: err %v, want content.ErrInvalid", name, err)
		}
	}
}

func TestDepositFailsWhenStorageCannotProveTheBytes(t *testing.T) {
	svc, store, storage := depositService()
	storage.putErr = errors.New("storage unavailable")
	if _, _, err := svc.Deposit(context.Background(), "org_a", strings.NewReader("bytes"), "text/plain", 10); err == nil || errors.Is(err, content.ErrInvalid) {
		t.Fatalf("err %v: a storage failure is retryable, not an invalid item", err)
	}
	if len(store.blobs) != 0 {
		t.Fatal("no Blob may be recorded without verified bytes")
	}
}

// A 24 MiB attachment is streamed through a bounded buffer: allocations stay a
// small fraction of the payload, so memory does not grow with attachment size.
func TestDepositStreamsWithBoundedMemory(t *testing.T) {
	svc, _, _ := depositService()
	const size = 24 << 20
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if _, n, err := svc.Deposit(context.Background(), "org_a", io.LimitReader(repeatReader{'z'}, size), "application/octet-stream", 25<<20); err != nil || n != size {
		t.Fatalf("n %d err %v", n, err)
	}
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 4<<20 {
		t.Fatalf("deposit allocated %d bytes for a %d-byte stream; it must stream, not buffer", allocated, size)
	}
}
