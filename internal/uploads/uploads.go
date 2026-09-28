// Package uploads owns presigned transfer sessions and verified Blob identity.
package uploads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

// MaxUploadBytes bounds a single presigned transfer expectation.
const MaxUploadBytes = 1 << 30

var (
	ErrConflict = errors.New("idempotency_conflict")
	ErrNotFound = errors.New("not_found")
	ErrInvalid  = errors.New("invalid_input")
	// ErrVerificationMismatch distinguishes altered bytes from a temporary
	// transfer-verification failure.
	ErrVerificationMismatch = errors.New("verified_bytes_mismatch")
)

// Request is the client's expectation for the bytes about to be transferred.
type Request struct {
	Key       string
	SizeBytes int64
	SHA256    string
	MediaType string
}

// Meta is the durable upload session state.
type Meta struct {
	ID        string
	State     string
	SHA256    string
	SizeBytes int64
	MediaType string
	ObjectKey string
	BlobID    string
	ErrorCode string
	ExpiresAt time.Time
}

// Session is the public view of an upload session.
type Session struct {
	ID            string
	State         string
	SHA256        string
	SizeBytes     int64
	MediaType     string
	UploadURL     string
	UploadHeaders map[string]string
	ExpiresAt     time.Time
	BlobID        string
	ErrorCode     string
}

// BlobInfo is the public view of a verified Blob.
type BlobInfo struct {
	ID        string
	SizeBytes int64
	SHA256    string
	MediaType string
}

// Store persists upload sessions and verified Blob identities.
type Store interface {
	Create(context.Context, string, string, Request, string, time.Time) (Meta, bool, error)
	Get(context.Context, string, string) (Meta, error)
	SetState(context.Context, string, string, string, string, string) error
	SaveBlob(context.Context, string, string, string, string, int64, string) error
	Blob(context.Context, string, string) (Meta, error)
}

// Transfer issues presigned capabilities and verifies transferred bytes.
type Transfer interface {
	PresignPut(context.Context, string, int64, string, string, time.Duration) (string, map[string]string, error)
	Verify(context.Context, string, int64, string) error
}

// Writer streams server-side bytes into storage. The body is seekable so the
// store can retry without buffering it; its length and digest are declared.
type Writer interface {
	PutStream(ctx context.Context, objectKey string, body io.ReadSeeker, size int64, sha256hex, mediaType string) error
}

// Service applies validation, verification and the upload state machine.
type Service struct {
	Store    Store
	Transfer Transfer
	// Writer enables server-side deposits (connector attachments).
	Writer Writer
	TTL    time.Duration
	Now    func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s Service) ttl() time.Duration {
	if s.TTL > 0 {
		return s.TTL
	}
	return 15 * time.Minute
}

func valid(req Request) bool {
	if req.Key == "" || req.SizeBytes < 1 || req.SizeBytes > MaxUploadBytes || req.MediaType == "" || !utf8.ValidString(req.MediaType) {
		return false
	}
	if len(req.SHA256) != 64 {
		return false
	}
	for _, c := range req.SHA256 {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Create issues or replays an upload session for the request identity.
func (s Service) Create(ctx context.Context, org string, req Request) (Session, error) {
	if !valid(req) {
		return Session{}, ErrInvalid
	}
	id := content.StableID("upload", org, req.Key)
	objectKey := content.Hash([]byte(org)) + "/uploads/" + id
	meta, _, err := s.Store.Create(ctx, org, id, req, objectKey, s.now().Add(s.ttl()))
	if err != nil {
		return Session{}, err
	}
	return s.session(ctx, meta)
}

// Confirm starts or observes checksum and size verification.
func (s Service) Confirm(ctx context.Context, org, id string) (Session, error) {
	meta, err := s.Store.Get(ctx, org, id)
	if err != nil {
		return Session{}, err
	}
	if meta.State == "verified" || meta.State == "rejected" {
		return s.session(ctx, meta)
	}
	if meta.State == "expired" || s.now().After(meta.ExpiresAt) {
		_ = s.Store.SetState(ctx, org, id, "expired", "", "upload_expired")
		meta.State, meta.ErrorCode = "expired", "upload_expired"
		return s.session(ctx, meta)
	}
	if err = s.Store.SetState(ctx, org, id, "verifying", "", ""); err != nil {
		return Session{}, err
	}
	meta.State = "verifying"
	if err = s.Transfer.Verify(ctx, meta.ObjectKey, meta.SizeBytes, meta.SHA256); err != nil {
		if !errors.Is(err, ErrVerificationMismatch) {
			// A temporary transfer-verification failure must stay confirmable, so
			// the client can retry once storage recovers.
			_ = s.Store.SetState(ctx, org, id, "awaiting_upload", "", "verification_unavailable")
			meta.State, meta.ErrorCode = "awaiting_upload", "verification_unavailable"
			return s.session(ctx, meta)
		}
		_ = s.Store.SetState(ctx, org, id, "rejected", "", "verification_failed")
		meta.State, meta.ErrorCode = "rejected", "verification_failed"
		return s.session(ctx, meta)
	}
	blobID := content.StableID("blob", org, meta.SHA256, meta.MediaType)
	if err = s.Store.SaveBlob(ctx, org, blobID, meta.ObjectKey, meta.SHA256, meta.SizeBytes, meta.MediaType); err != nil {
		return Session{}, err
	}
	if err = s.Store.SetState(ctx, org, id, "verified", blobID, ""); err != nil {
		return Session{}, err
	}
	meta.State, meta.BlobID = "verified", blobID
	return s.session(ctx, meta)
}

// Get observes a session independently of verification.
func (s Service) Get(ctx context.Context, org, id string) (Session, error) {
	meta, err := s.Store.Get(ctx, org, id)
	if err != nil {
		return Session{}, err
	}
	if (meta.State == "awaiting_upload" || meta.State == "verifying") && s.now().After(meta.ExpiresAt) {
		_ = s.Store.SetState(ctx, org, id, "expired", "", "upload_expired")
		meta.State, meta.ErrorCode = "expired", "upload_expired"
	}
	return s.session(ctx, meta)
}

// Blob reads verified Blob metadata within the Organization scope.
func (s Service) Blob(ctx context.Context, org, id string) (BlobInfo, error) {
	meta, err := s.Store.Blob(ctx, org, id)
	if err != nil {
		return BlobInfo{}, err
	}
	return BlobInfo{ID: id, SizeBytes: meta.SizeBytes, SHA256: meta.SHA256, MediaType: meta.MediaType}, nil
}

func (s Service) session(ctx context.Context, meta Meta) (Session, error) {
	out := Session{ID: meta.ID, State: meta.State, SHA256: meta.SHA256, SizeBytes: meta.SizeBytes, MediaType: meta.MediaType, ExpiresAt: meta.ExpiresAt, BlobID: meta.BlobID, ErrorCode: meta.ErrorCode}
	if meta.State != "awaiting_upload" {
		return out, nil
	}
	url, headers, err := s.Transfer.PresignPut(ctx, meta.ObjectKey, meta.SizeBytes, meta.SHA256, meta.MediaType, time.Until(meta.ExpiresAt))
	if err != nil {
		return Session{}, err
	}
	out.UploadURL, out.UploadHeaders = url, headers
	return out, nil
}

// Deposit stores bytes collected server-side (e.g. a connector attachment) as
// a verified Blob with the same identity as a client upload of the same bytes.
// The stream is hashed while it is spooled to a temporary file through a fixed
// buffer, so memory stays bounded whatever the attachment size. More than max
// bytes, an empty stream or a missing media type wrap content.ErrInvalid.
func (s Service) Deposit(ctx context.Context, org string, r io.Reader, mediaType string, max int64) (string, int64, error) {
	if s.Writer == nil {
		return "", 0, errors.New("blob deposits are not configured")
	}
	if mediaType == "" || !utf8.ValidString(mediaType) || max < 1 || max > MaxUploadBytes {
		return "", 0, fmt.Errorf("%w: invalid deposit expectation", content.ErrInvalid)
	}
	spool, err := os.CreateTemp("", "quivr-deposit-*")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(spool.Name())
	defer spool.Close()
	h := sha256.New()
	size, err := io.CopyBuffer(io.MultiWriter(spool, h), io.LimitReader(r, max+1), make([]byte, 64<<10))
	if err != nil {
		return "", 0, err
	}
	if size < 1 || size > max {
		return "", 0, fmt.Errorf("%w: deposit size outside 1..%d bytes", content.ErrInvalid, max)
	}
	if _, err = spool.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	objectKey := content.Hash([]byte(org)) + "/blobs/sha256/" + digest
	if err = s.Writer.PutStream(ctx, objectKey, spool, size, digest, mediaType); err != nil {
		return "", 0, err
	}
	if err = s.Transfer.Verify(ctx, objectKey, size, digest); err != nil {
		return "", 0, err
	}
	blobID := content.StableID("blob", org, digest, mediaType)
	if err = s.Store.SaveBlob(ctx, org, blobID, objectKey, digest, size, mediaType); err != nil {
		return "", 0, err
	}
	return blobID, size, nil
}
