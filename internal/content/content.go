// Package content owns durable acceptance and immutable Version publication.
package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

var (
	ErrConflict    = errors.New("idempotency_conflict")
	ErrUnsupported = errors.New("unsupported_content")
	ErrInvalid     = errors.New("invalid_input")
)

type Source struct {
	CorpusID  string `json:"corpus_id"`
	Namespace string `json:"namespace"`
	RecordKey string `json:"record_key"`
}
type Text struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}
type Command struct {
	Key        string         `json:"idempotency_key"`
	Source     Source         `json:"source"`
	Revision   string         `json:"source_revision,omitempty"`
	Position   string         `json:"source_position,omitempty"`
	Content    Text           `json:"content"`
	Extensions map[string]any `json:"extensions,omitempty"`
	Provenance map[string]any `json:"provenance,omitempty"`
}
type Processing struct {
	State string `json:"state"`
	Phase string `json:"phase,omitempty"`
}
type Availability struct {
	State      string `json:"state"`
	Current    bool   `json:"is_current"`
	Searchable bool   `json:"searchable"`
}
type Diagnostic struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}
type Receipt struct {
	ID           string        `json:"receipt_id"`
	State        string        `json:"state"`
	Outcome      string        `json:"outcome,omitempty"`
	RecordID     string        `json:"record_id,omitempty"`
	VersionID    string        `json:"version_id,omitempty"`
	Source       Source        `json:"source"`
	Processing   Processing    `json:"processing"`
	Availability *Availability `json:"availability,omitempty"`
	Diagnostics  []Diagnostic  `json:"diagnostics"`
}
type Record struct {
	ID               string `json:"record_id"`
	Source           Source `json:"source"`
	Withdrawn        bool   `json:"withdrawn"`
	CurrentVersionID string `json:"current_version_id,omitempty"`
}
type Part struct {
	Key     string `json:"key"`
	Role    string `json:"role"`
	Content Text   `json:"content"`
}
type Manifest struct {
	Kind  string `json:"kind"`
	Parts []Part `json:"parts"`
}
type Version struct {
	RecordID     string         `json:"record_id"`
	ID           string         `json:"version_id"`
	Manifest     Manifest       `json:"manifest"`
	Provenance   map[string]any `json:"provenance,omitempty"`
	Availability Availability   `json:"availability"`
	Relations    []any          `json:"relations"`
	Processing   Processing     `json:"processing"`
}
type Blob struct {
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type StoredVersion struct {
	RecordID, ID, CorpusID string
	ManifestBlob, TextBlob Blob
	Provenance             map[string]any
	Availability           Availability
	Processing             Processing
}
type Work struct {
	Organization, ReceiptID, RecordID, VersionID, Digest, Slot string
	Order                                                      int64
	Position, PredecessorID                                    string
	Command                                                    Command
}
type Repository interface {
	Accept(context.Context, corpus.Scope, Command) (Receipt, error)
	Receipt(context.Context, string, string) (Receipt, error)
	Record(context.Context, string, string) (Record, error)
	Version(context.Context, string, string, string) (StoredVersion, error)
	Work(context.Context, string, string) (Work, bool, error)
	Progress(context.Context, string, string, string, string) error
	Publish(context.Context, Work, Blob, Blob) error
}
type Blobs interface {
	Put(context.Context, string, []byte) (Blob, error)
	Read(context.Context, Blob) ([]byte, error)
}
type Service struct {
	Repository Repository
	Blobs      Blobs
	Baseline   BaselineRepository
}

func (s Service) Accept(ctx context.Context, scope corpus.Scope, c Command) (Receipt, error) {
	if !scope.Allows("content:write") {
		return Receipt{}, corpus.ErrForbidden
	}
	if !scope.Contains(c.Source.CorpusID) {
		return Receipt{}, corpus.ErrNotFound
	}
	if c.Content.Kind != "text" || len(c.Extensions) > 0 {
		return Receipt{}, ErrUnsupported
	}
	if ids, ok := c.Provenance["source_blob_ids"].([]any); ok && len(ids) > 0 {
		return Receipt{}, ErrUnsupported
	}
	if c.Key == "" || c.Source.CorpusID == "" || c.Source.Namespace == "" || c.Source.RecordKey == "" || c.Content.Text == "" || !utf8.ValidString(c.Content.Text) {
		return Receipt{}, ErrInvalid
	}
	if c.Position != "" {
		n, ok := new(big.Int).SetString(c.Position, 10)
		if !ok || n.Sign() < 0 || len(c.Position) > 1000 {
			return Receipt{}, ErrInvalid
		}
		c.Position = n.String()
	}
	result, err := s.Repository.Accept(ctx, scope, c)
	if !scope.Allows("content:read") {
		result.RecordID = ""
		result.VersionID = ""
		result.Availability = nil
	}
	return result, err
}
func (s Service) Receipt(ctx context.Context, scope corpus.Scope, id string) (Receipt, error) {
	if !scope.Allows("content:read") {
		return Receipt{}, corpus.ErrForbidden
	}
	r, err := s.Repository.Receipt(ctx, scope.Organization, id)
	if err == nil && !scope.Contains(r.Source.CorpusID) {
		return Receipt{}, corpus.ErrNotFound
	}
	return r, err
}
func (s Service) Record(ctx context.Context, scope corpus.Scope, id string) (Record, error) {
	if !scope.Allows("content:read") {
		return Record{}, corpus.ErrForbidden
	}
	r, err := s.Repository.Record(ctx, scope.Organization, id)
	if err == nil && !scope.Contains(r.Source.CorpusID) {
		return Record{}, corpus.ErrNotFound
	}
	return r, err
}
func (s Service) Version(ctx context.Context, scope corpus.Scope, recordID, id string) (Version, error) {
	if !scope.Allows("content:read") {
		return Version{}, corpus.ErrForbidden
	}
	stored, err := s.Repository.Version(ctx, scope.Organization, recordID, id)
	if err != nil {
		return Version{}, err
	}
	if !scope.Contains(stored.CorpusID) {
		return Version{}, corpus.ErrNotFound
	}
	data, err := s.Blobs.Read(ctx, stored.ManifestBlob)
	if err != nil {
		return Version{}, err
	}
	var manifest Manifest
	if err = json.Unmarshal(data, &manifest); err != nil {
		return Version{}, err
	}
	text, err := s.Blobs.Read(ctx, stored.TextBlob)
	if err != nil {
		return Version{}, err
	}
	if len(manifest.Parts) != 1 || manifest.Parts[0].Content.Text != string(text) {
		return Version{}, errors.New("canonical artifact mismatch")
	}
	return Version{RecordID: recordID, ID: id, Manifest: manifest, Provenance: stored.Provenance, Availability: stored.Availability, Relations: []any{}, Processing: stored.Processing}, nil
}
func ManifestFor(c Command) Manifest {
	return Manifest{Kind: "manifest", Parts: []Part{{Key: "body", Role: "body", Content: c.Content}}}
}
func Digest(c Command) string {
	b, _ := json.Marshal(ManifestFor(c))
	return Hash(b)
}
func Hash(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func StableID(prefix string, values ...string) string {
	b, _ := json.Marshal(values)
	return prefix + "_" + Hash(b)
}
func NewerPosition(incoming, existing string) bool {
	if incoming == "" || existing == "" {
		return true
	}
	a, _ := new(big.Int).SetString(incoming, 10)
	b, _ := new(big.Int).SetString(existing, 10)
	return a.Cmp(b) >= 0
}

// Materialize performs I/O outside publication's canonical transaction.
func (s Service) Materialize(ctx context.Context, org, receiptID string) error {
	work, done, err := s.Repository.Work(ctx, org, receiptID)
	if err != nil || done {
		return err
	}
	if err = s.Repository.Progress(ctx, org, receiptID, "running", ""); err != nil {
		return err
	}
	manifest, err := json.Marshal(ManifestFor(work.Command))
	if err != nil {
		return err
	}
	textBlob, err := s.Blobs.Put(ctx, org, []byte(work.Command.Content.Text))
	if err != nil {
		_ = s.Repository.Progress(ctx, org, receiptID, "retrying", "blob_verification_unavailable")
		return errors.New("canonical text publication unavailable")
	}
	manifestBlob, err := s.Blobs.Put(ctx, org, manifest)
	if err != nil {
		_ = s.Repository.Progress(ctx, org, receiptID, "retrying", "blob_verification_unavailable")
		return errors.New("canonical manifest publication unavailable")
	}
	if err = s.Repository.Publish(ctx, work, textBlob, manifestBlob); err != nil {
		_ = s.Repository.Progress(ctx, org, receiptID, "retrying", "publication_unavailable")
		return errors.New("canonical transaction unavailable")
	}
	return nil
}

// Dispatch names one accepted command that still needs a durable workflow start.
type Dispatch struct{ Organization, ReceiptID string }

var ErrNoDispatch = errors.New("no_pending_dispatch")
