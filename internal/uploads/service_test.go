package uploads_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

type memoryStore struct {
	sessions map[string]uploads.Meta
	byKey    map[string]string
	blobs    map[string]uploads.Meta
}

func newMemoryStore() *memoryStore {
	return &memoryStore{sessions: map[string]uploads.Meta{}, byKey: map[string]string{}, blobs: map[string]uploads.Meta{}}
}

func (m *memoryStore) Create(_ context.Context, org, id string, req uploads.Request, objectKey string, expires time.Time) (uploads.Meta, bool, error) {
	if existing, ok := m.byKey[org+"/"+req.Key]; ok {
		return m.sessions[org+"/"+existing], false, nil
	}
	meta := uploads.Meta{ID: id, State: "awaiting_upload", SHA256: req.SHA256, SizeBytes: req.SizeBytes, MediaType: req.MediaType, ObjectKey: objectKey, ExpiresAt: expires}
	m.sessions[org+"/"+id] = meta
	m.byKey[org+"/"+req.Key] = id
	return meta, true, nil
}

func (m *memoryStore) Get(_ context.Context, org, id string) (uploads.Meta, error) {
	if meta, ok := m.sessions[org+"/"+id]; ok {
		return meta, nil
	}
	return uploads.Meta{}, uploads.ErrNotFound
}

// Writes refuse a cancelled context, as PostgreSQL writes do.
func (m *memoryStore) SetState(ctx context.Context, org, id, state, blobID, code string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := m.sessions[org+"/"+id]; !ok {
		return uploads.ErrNotFound
	}
	meta := m.sessions[org+"/"+id]
	meta.State, meta.BlobID, meta.ErrorCode = state, blobID, code
	m.sessions[org+"/"+id] = meta
	return nil
}

func (m *memoryStore) SaveBlob(ctx context.Context, org, id, objectKey, sha256 string, size int64, mediaType string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.blobs[org+"/"+id] = uploads.Meta{ID: id, State: "verified", ObjectKey: objectKey, SHA256: sha256, SizeBytes: size, MediaType: mediaType, BlobID: id}
	return nil
}

func (m *memoryStore) Blob(_ context.Context, org, id string) (uploads.Meta, error) {
	if meta, ok := m.blobs[org+"/"+id]; ok {
		return meta, nil
	}
	return uploads.Meta{}, uploads.ErrNotFound
}

type fakeTransfer struct {
	err    error
	signed int
	// onVerify runs while storage is read; afterVerify once it answered.
	onVerify, afterVerify func()
}

func (f *fakeTransfer) PresignPut(context.Context, string, int64, string, string, time.Duration) (string, map[string]string, error) {
	f.signed++
	return "http://storage.invalid/upload", map[string]string{"content-type": "text/plain"}, nil
}

func (f *fakeTransfer) Verify(ctx context.Context, _ string, _ int64, _ string) error {
	if f.onVerify != nil {
		f.onVerify()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.afterVerify != nil {
		defer f.afterVerify()
	}
	return f.err
}

const digest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func TestCreateReplaysAndPresigns(t *testing.T) {
	store := newMemoryStore()
	transfer := &fakeTransfer{}
	service := uploads.Service{Store: store, Transfer: transfer}
	req := uploads.Request{Key: digest + ":5:text/plain", SizeBytes: 5, SHA256: digest, MediaType: "text/plain"}
	first, err := service.Create(context.Background(), "org_a", req)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != "awaiting_upload" || first.UploadURL == "" || first.UploadHeaders["content-type"] != "text/plain" {
		t.Fatal(first)
	}
	second, err := service.Create(context.Background(), "org_a", req)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || transfer.signed != 2 {
		t.Fatalf("replay did not converge: %v %v", second, transfer.signed)
	}
}

// A grant reuses a verified Blob of the same identity instead of issuing an
// upload; otherwise it is an upload session that only Confirm verifies.
func TestGrantReusesAVerifiedBlobOrIssuesAnUpload(t *testing.T) {
	store := newMemoryStore()
	transfer := &fakeTransfer{}
	service := uploads.Service{Store: store, Transfer: transfer}
	req := uploads.Request{Key: "connector:attachment-1", SizeBytes: 5, SHA256: digest, MediaType: "text/plain"}
	granted, err := service.Grant(context.Background(), "org_a", req)
	if err != nil || granted.State != "awaiting_upload" || granted.UploadURL == "" || granted.BlobID != "" {
		t.Fatalf("first grant %+v %v", granted, err)
	}
	confirmed, err := service.Confirm(context.Background(), "org_a", granted.ID)
	if err != nil || confirmed.State != "verified" {
		t.Fatalf("confirm %+v %v", confirmed, err)
	}
	req.Key = "connector:attachment-2"
	reused, err := service.Grant(context.Background(), "org_a", req)
	if err != nil || reused.State != "verified" || reused.BlobID != confirmed.BlobID || reused.UploadURL != "" || transfer.signed != 1 {
		t.Fatalf("same bytes must reuse the verified Blob: %+v %v (signed %d)", reused, err, transfer.signed)
	}
	if other, err := service.Grant(context.Background(), "org_b", req); err != nil || other.State != "awaiting_upload" {
		t.Fatalf("another Organization's Blob is never reused: %+v %v", other, err)
	}
	if _, err := service.Grant(context.Background(), "org_a", uploads.Request{Key: "x", SizeBytes: 5, SHA256: "nope", MediaType: "text/plain"}); !errors.Is(err, uploads.ErrInvalid) {
		t.Fatalf("invalid expectation: %v", err)
	}
}

func TestConfirmVerifiesAndIsRepeatable(t *testing.T) {
	store := newMemoryStore()
	service := uploads.Service{Store: store, Transfer: &fakeTransfer{}}
	session, err := service.Create(context.Background(), "org_a", uploads.Request{Key: digest + ":5:text/plain", SizeBytes: 5, SHA256: digest, MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := service.Confirm(context.Background(), "org_a", session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.State != "verified" || confirmed.BlobID == "" {
		t.Fatal(confirmed)
	}
	replayed, err := service.Confirm(context.Background(), "org_a", session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.State != "verified" || replayed.BlobID != confirmed.BlobID {
		t.Fatal(replayed)
	}
	blob, err := service.Blob(context.Background(), "org_a", confirmed.BlobID)
	if err != nil || blob.SHA256 != digest || blob.SizeBytes != 5 {
		t.Fatal(blob, err)
	}
}

func TestConfirmRejectsAlteredBytesButRetriesTransientFailure(t *testing.T) {
	store := newMemoryStore()
	service := uploads.Service{Store: store, Transfer: &fakeTransfer{err: uploads.ErrVerificationMismatch}}
	session, err := service.Create(context.Background(), "org_a", uploads.Request{Key: digest + ":5:text/plain", SizeBytes: 5, SHA256: digest, MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	altered, err := service.Confirm(context.Background(), "org_a", session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if altered.State != "rejected" || altered.BlobID != "" || altered.ErrorCode != "verification_failed" {
		t.Fatal(altered)
	}

	transientStore := newMemoryStore()
	transient := uploads.Service{Store: transientStore, Transfer: &fakeTransfer{err: errors.New("storage down")}}
	pending, err := transient.Create(context.Background(), "org_a", uploads.Request{Key: digest + ":5:text/plain", SizeBytes: 5, SHA256: digest, MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	retryable, err := transient.Confirm(context.Background(), "org_a", pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retryable.State != "awaiting_upload" || retryable.ErrorCode != "verification_unavailable" || retryable.UploadURL == "" {
		t.Fatalf("transient failure was latched: %v", retryable)
	}
	transient.Transfer = &fakeTransfer{}
	recovered, err := transient.Confirm(context.Background(), "org_a", pending.ID)
	if err != nil || recovered.State != "verified" || recovered.BlobID == "" {
		t.Fatalf("retry did not reconcile: %v %v", recovered, err)
	}
}

// A worker stopping during or right after verification cancels Confirm. The
// session must still leave verifying: its replay would carry no upload URL
// (THE-1314).
func TestACancelledConfirmStillSettlesTheSession(t *testing.T) {
	for name, tc := range map[string]struct {
		during bool
		want   string
	}{
		"during verification": {during: true, want: "awaiting_upload"},
		"after verification":  {during: false, want: "verified"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			transfer := &fakeTransfer{afterVerify: cancel}
			if tc.during {
				transfer = &fakeTransfer{onVerify: cancel}
			}
			service := uploads.Service{Store: newMemoryStore(), Transfer: transfer}
			req := uploads.Request{Key: digest + ":5:text/plain", SizeBytes: 5, SHA256: digest, MediaType: "text/plain"}
			session, err := service.Create(ctx, "org_a", req)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Confirm(ctx, "org_a", session.ID); err != nil {
				t.Fatalf("cancelled Confirm: %v", err)
			}
			replayed, err := service.Create(context.Background(), "org_a", req)
			if err != nil || replayed.State != tc.want || (tc.want == "awaiting_upload") != (replayed.UploadURL != "") {
				t.Fatalf("replay: state %q url set %v err %v; want %s", replayed.State, replayed.UploadURL != "", err, tc.want)
			}
		})
	}
}

func TestCrossOrganizationAndExpiry(t *testing.T) {
	store := newMemoryStore()
	now := time.Now()
	service := uploads.Service{Store: store, Transfer: &fakeTransfer{}, Now: func() time.Time { return now }, TTL: time.Minute}
	session, err := service.Create(context.Background(), "org_a", uploads.Request{Key: digest + ":5:text/plain", SizeBytes: 5, SHA256: digest, MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Get(context.Background(), "org_b", session.ID); !errors.Is(err, uploads.ErrNotFound) {
		t.Fatal("foreign Organization observed the session")
	}
	now = now.Add(2 * time.Minute)
	expired, err := service.Get(context.Background(), "org_a", session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if expired.State != "expired" || expired.UploadURL != "" {
		t.Fatal(expired)
	}
}

func TestCreateValidatesExpectation(t *testing.T) {
	service := uploads.Service{Store: newMemoryStore(), Transfer: &fakeTransfer{}}
	for _, req := range []uploads.Request{
		{Key: "x", SizeBytes: 0, SHA256: digest, MediaType: "text/plain"},
		{Key: "x", SizeBytes: 5, SHA256: "not-a-digest", MediaType: "text/plain"},
		{Key: "x", SizeBytes: 5, SHA256: digest, MediaType: ""},
	} {
		if _, err := service.Create(context.Background(), "org_a", req); !errors.Is(err, uploads.ErrInvalid) {
			t.Fatalf("accepted invalid request: %v", req)
		}
	}
}
