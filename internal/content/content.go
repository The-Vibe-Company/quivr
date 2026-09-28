// Package content owns durable acceptance and immutable Version publication.
package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

var (
	ErrConflict       = errors.New("idempotency_conflict")
	ErrUnsupported    = errors.New("unsupported_content")
	ErrInvalid        = errors.New("invalid_input")
	ErrUnverifiedBlob = errors.New("unverified_blob")
	// ErrArtifactMissing and ErrArtifactCorrupt report durable object-storage
	// integrity failures, distinct from transient unavailability.
	ErrArtifactMissing = errors.New("durable_artifact_missing")
	ErrArtifactCorrupt = errors.New("durable_artifact_corrupt")
)

// maxCanonicalBlobBytes bounds a single canonical object the content service reads.
const maxCanonicalBlobBytes = 2 << 20

// maxGenericJSONBytes bounds source-declared generic JSON (extensions and the
// non-text Manifest structure) before it is durably accepted.
const maxGenericJSONBytes = 64 << 10

// maxManifestParts bounds the number of Parts in one explicit Manifest.
const maxManifestParts = 256

type Source struct {
	CorpusID  string `json:"corpus_id"`
	Namespace string `json:"namespace"`
	RecordKey string `json:"record_key"`
}

// Text carries either inline text or a verified Blob reference. A Blob reference
// is resolved to its immutable bytes before acceptance; the stored Command is
// then always inline text so downstream canonical publication is unchanged.
type Text struct {
	Kind      string `json:"kind"`
	Text      string `json:"text,omitempty"`
	BlobID    string `json:"blob_id,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	// BlobSHA256 records the verified checksum of a preserved Blob Part. It is
	// canonical internal provenance and is not part of the public transport shape.
	BlobSHA256 string `json:"blob_sha256,omitempty"`
}

// Extension is one namespaced, schema-versioned source object. Source data is
// distinct from computed Derivations and Annotations.
type Extension struct {
	SchemaVersion string         `json:"schema_version"`
	Data          map[string]any `json:"data"`
}

// Extensions maps plugin namespaces to their declared schema-versioned payload.
type Extensions map[string]Extension

// Part is a typed, hierarchical component of an explicit Manifest. A Blob Part
// preserves a verified same-Organization Blob reference without extracting or
// reinterpreting its bytes.
type Part struct {
	Key        string     `json:"key"`
	ParentKey  string     `json:"parent_key,omitempty"`
	Role       string     `json:"role"`
	Content    Text       `json:"content"`
	Extensions Extensions `json:"extensions,omitempty"`
}

// Relation is a source-provided link to an independently identified Record in
// the same Organization. Expansion resolves the target's current eligible
// Version without mutating this reference.
type Relation struct {
	Type                 string `json:"type"`
	Target               Source `json:"target"`
	SourceTargetRevision string `json:"source_target_revision,omitempty"`
}

// Manifest is the atomically published, immutable description of a Record
// Version's Parts and Relations.
type Manifest struct {
	Kind      string     `json:"kind"`
	Parts     []Part     `json:"parts"`
	Relations []Relation `json:"relations,omitempty"`
}

// VerifiedBlob is an Organization-scoped verified Blob identity.
type VerifiedBlob struct {
	ID        string
	Blob      Blob
	MediaType string
}

// BlobSource resolves a verified Blob reference within an Organization.
type BlobSource interface {
	VerifiedBlob(context.Context, string, string) (VerifiedBlob, error)
}

// RelationResolver expands immutable Record-target Relations against canonical
// currentness, availability and authorization. It never mutates the Manifest.
type RelationResolver interface {
	Resolve(context.Context, corpus.Scope, []Relation) ([]ResolvedRelation, error)
}

type Command struct {
	Key        string         `json:"idempotency_key"`
	Source     Source         `json:"source"`
	Revision   string         `json:"source_revision,omitempty"`
	Position   string         `json:"source_position,omitempty"`
	Content    Text           `json:"content"`
	Manifest   *Manifest      `json:"manifest,omitempty"`
	Extensions Extensions     `json:"extensions,omitempty"`
	Provenance map[string]any `json:"provenance,omitempty"`
}

// Withdrawal is an absorbing command that fences a Record identity. It shares
// the Source identity vocabulary with ingestion but uses its own receipt route
// family and never publishes a Version.
type Withdrawal struct {
	Key    string `json:"idempotency_key"`
	Source Source `json:"source"`
	Reason string `json:"reason,omitempty"`
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
	// NewRevision reports, on the call that accepted the command, that it
	// reserved a revision the Record did not have yet: it will publish a new
	// Version (unless it conflicts). Replays and repeated revisions report
	// false. Internal only; not part of the public Receipt shape.
	NewRevision bool `json:"-"`
}
type Record struct {
	ID               string `json:"record_id"`
	Source           Source `json:"source"`
	Withdrawn        bool   `json:"withdrawn"`
	CurrentVersionID string `json:"current_version_id,omitempty"`
}
type Version struct {
	RecordID     string             `json:"record_id"`
	ID           string             `json:"version_id"`
	Manifest     Manifest           `json:"manifest"`
	Extensions   Extensions         `json:"extensions,omitempty"`
	Provenance   map[string]any     `json:"provenance,omitempty"`
	Availability Availability       `json:"availability"`
	Relations    []ResolvedRelation `json:"relations"`
	Processing   Processing         `json:"processing"`
}

// ResolvedRelation is a separate live view of one source Relation. Unavailable
// covers missing, unready, withdrawn and inaccessible without distinguishing
// existence or revealing a resolved target ID.
type ResolvedRelation struct {
	Source          Relation `json:"source_reference"`
	Status          string   `json:"status"`
	TargetRecordID  string   `json:"target_record_id,omitempty"`
	TargetVersionID string   `json:"target_version_id,omitempty"`
}

type Blob struct {
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// PartBlob names the canonical immutable bytes of one text Part.
type PartBlob struct {
	Key  string
	Role string
	Blob Blob
}

// Publication carries the external immutable objects that one atomic canonical
// publication references.
type Publication struct {
	Normalized Blob
	Manifest   Blob
	Parts      []PartBlob
}

type StoredVersion struct {
	RecordID, ID, CorpusID string
	ManifestBlob, TextBlob Blob
	Provenance             map[string]any
	Extensions             Extensions
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
	Withdraw(context.Context, corpus.Scope, Withdrawal) (Receipt, error)
	Receipt(context.Context, string, string) (Receipt, error)
	Record(context.Context, string, string) (Record, error)
	Version(context.Context, string, string, string) (StoredVersion, error)
	Work(context.Context, string, string) (Work, bool, error)
	Progress(context.Context, string, string, string, string) error
	Publish(context.Context, Work, Publication) error
}

// RecordCatalog lists one Corpus's Records, withdrawn ones included, in stable
// key order after an exclusive key. Each call is an independent short read:
// traversal is not a snapshot, and the change journal covers concurrent writes.
type RecordCatalog interface {
	Records(ctx context.Context, org, corpusID, after string, limit int) ([]Record, error)
}
type Blobs interface {
	Put(context.Context, string, []byte) (Blob, error)
	Read(context.Context, Blob) ([]byte, error)
}
type Service struct {
	Repository Repository
	Catalog    RecordCatalog
	Blobs      Blobs
	Baseline   BaselineRepository
	Embeddings EmbeddingRepository
	BlobSource BlobSource
	Relations  RelationResolver
	Extensions ExtensionValidator
}

func (s Service) Accept(ctx context.Context, scope corpus.Scope, c Command) (Receipt, error) {
	if !scope.Allows("content:write") {
		return Receipt{}, corpus.ErrForbidden
	}
	if !scope.Contains(c.Source.CorpusID) {
		return Receipt{}, corpus.ErrNotFound
	}
	switch c.Content.Kind {
	case "text":
		// Inline text cannot assert Blob provenance; only verified references may.
		if ids, ok := c.Provenance["source_blob_ids"].([]any); ok && len(ids) > 0 {
			return Receipt{}, ErrUnsupported
		}
	case "blob":
		verified, err := s.resolveBlob(ctx, scope.Organization, c.Content)
		if err != nil {
			return Receipt{}, err
		}
		if c.Provenance == nil {
			c.Provenance = map[string]any{}
		}
		c.Provenance["source_blob_ids"] = []any{c.Content.BlobID}
		c.Content = Text{Kind: "text", Text: string(verified)}
	case "manifest":
		if c.Manifest == nil {
			return Receipt{}, ErrInvalid
		}
		if err := s.validateManifest(ctx, scope.Organization, c.Manifest); err != nil {
			return Receipt{}, err
		}
		if err := s.validateSourceBlobIDs(ctx, scope.Organization, c.Provenance); err != nil {
			return Receipt{}, err
		}
	default:
		return Receipt{}, ErrUnsupported
	}
	if c.Key == "" || c.Source.CorpusID == "" || c.Source.Namespace == "" || c.Source.RecordKey == "" {
		return Receipt{}, ErrInvalid
	}
	if c.Content.Kind != "manifest" && (c.Content.Text == "" || !ValidText(c.Content.Text)) {
		return Receipt{}, ErrInvalid
	}
	if err := s.validateExtensions(ctx, c.Extensions); err != nil {
		return Receipt{}, err
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

// Withdraw durably fences a Record identity. The record may exist already or be
// fenced before any materialization; a later ordinary ingestion of that identity
// is a terminal conflict.
func (s Service) Withdraw(ctx context.Context, scope corpus.Scope, w Withdrawal) (Receipt, error) {
	if !scope.Allows("content:write") {
		return Receipt{}, corpus.ErrForbidden
	}
	if !scope.Contains(w.Source.CorpusID) {
		return Receipt{}, corpus.ErrNotFound
	}
	if w.Key == "" || w.Source.CorpusID == "" || w.Source.Namespace == "" || w.Source.RecordKey == "" || !ValidText(w.Reason) {
		return Receipt{}, ErrInvalid
	}
	result, err := s.Repository.Withdraw(ctx, scope, w)
	if !scope.Allows("content:read") {
		result.RecordID = ""
		result.VersionID = ""
		result.Availability = nil
	}
	return result, err
}

// validateManifest enforces the semantic Manifest constraints the JSON Schema
// cannot express: unique Part keys, an acyclic same-Manifest parent hierarchy,
// valid text content, verified same-Organization Blob Parts and schema-validated
// Part extensions. Role semantics beyond preservation belong to the processing
// profile, which blocks rather than rejects an unsupported combination.
func (s Service) validateManifest(ctx context.Context, org string, m *Manifest) error {
	if len(m.Parts) == 0 {
		return ErrInvalid
	}
	if len(m.Parts) > maxManifestParts {
		return ErrUnsupported
	}
	byKey := make(map[string]Part, len(m.Parts))
	for i := range m.Parts {
		p := m.Parts[i]
		if p.Key == "" || p.Role == "" {
			return ErrInvalid
		}
		if _, exists := byKey[p.Key]; exists {
			return ErrInvalid
		}
		byKey[p.Key] = p
		switch p.Content.Kind {
		case "text":
			if p.Content.Text == "" || !ValidText(p.Content.Text) {
				return ErrInvalid
			}
		case "blob":
			checksum, err := s.verifyPartBlob(ctx, org, p.Content)
			if err != nil {
				return err
			}
			// Preserve the verified checksum in the immutable canonical Manifest.
			m.Parts[i].Content.BlobSHA256 = checksum
		default:
			return ErrUnsupported
		}
		if err := s.validateExtensions(ctx, p.Extensions); err != nil {
			return err
		}
	}
	for _, p := range m.Parts {
		if p.ParentKey == "" {
			continue
		}
		if p.ParentKey == p.Key {
			return ErrInvalid
		}
		parent, ok := byKey[p.ParentKey]
		if !ok {
			return ErrInvalid
		}
		seen := map[string]bool{}
		for {
			if seen[parent.Key] {
				return ErrInvalid
			}
			seen[parent.Key] = true
			if parent.ParentKey == "" {
				break
			}
			parent, ok = byKey[parent.ParentKey]
			if !ok {
				return ErrInvalid
			}
		}
	}
	for _, r := range m.Relations {
		if r.Type == "" || r.Target.CorpusID == "" || r.Target.Namespace == "" || r.Target.RecordKey == "" {
			return ErrInvalid
		}
	}
	if !boundedManifestStructure(m) {
		return ErrUnsupported
	}
	return nil
}

// boundedManifestStructure bounds the non-text shape of a Manifest (Part keys,
// roles, parents, Blob references and extensions, plus Relations) independently
// of text size, which acceptance and processing bound separately.
func boundedManifestStructure(m *Manifest) bool {
	parts := make([]Part, len(m.Parts))
	for i, p := range m.Parts {
		p.Content.Text = ""
		parts[i] = p
	}
	return boundedJSON(struct {
		Parts     []Part     `json:"parts"`
		Relations []Relation `json:"relations,omitempty"`
	}{Parts: parts, Relations: m.Relations})
}

// validateSourceBlobIDs requires every client-declared provenance Blob reference
// to be a verified Blob in the same Organization, so a Manifest cannot forge an
// original source reference that the inline-text path would reject.
func (s Service) validateSourceBlobIDs(ctx context.Context, org string, provenance map[string]any) error {
	raw, ok := provenance["source_blob_ids"]
	if !ok {
		return nil
	}
	ids, ok := raw.([]any)
	if !ok {
		return ErrInvalid
	}
	for _, value := range ids {
		id, ok := value.(string)
		if !ok || id == "" {
			return ErrInvalid
		}
		if s.BlobSource == nil {
			return ErrUnverifiedBlob
		}
		verified, err := s.BlobSource.VerifiedBlob(ctx, org, id)
		if err != nil && !errors.Is(err, ErrUnverifiedBlob) {
			return err
		}
		if err != nil || verified.ID != id {
			return ErrUnverifiedBlob
		}
	}
	return nil
}

// verifyPartBlob preserves a verified same-Organization Blob reference without
// reading or reinterpreting its bytes. It returns the verified checksum.
func (s Service) verifyPartBlob(ctx context.Context, org string, ref Text) (string, error) {
	if s.BlobSource == nil || ref.BlobID == "" || ref.MediaType == "" {
		return "", ErrUnverifiedBlob
	}
	verified, err := s.BlobSource.VerifiedBlob(ctx, org, ref.BlobID)
	if err != nil && !errors.Is(err, ErrUnverifiedBlob) {
		return "", err
	}
	if err != nil || verified.ID != ref.BlobID || verified.MediaType != ref.MediaType {
		return "", ErrUnverifiedBlob
	}
	return verified.Blob.SHA256, nil
}

func (s Service) validateExtensions(ctx context.Context, exts Extensions) error {
	if len(exts) == 0 {
		return nil
	}
	if !boundedJSON(exts) {
		return ErrUnsupported
	}
	validator := s.Extensions
	if validator == nil {
		validator = BuiltinExtensions{}
	}
	return validator.Validate(ctx, exts)
}

// boundedJSON keeps source-declared generic JSON within an explicit budget.
func boundedJSON(v any) bool {
	b, err := json.Marshal(v)
	return err == nil && len(b) <= maxGenericJSONBytes
}

// ValidText reports whether s is canonical UTF-8 text that may be stored: valid
// encoding and no NUL. It is the single text predicate shared by acceptance,
// validation and processing.
func ValidText(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }

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

// Records returns up to limit authorized Records of a Corpus after key after.
func (s Service) Records(ctx context.Context, scope corpus.Scope, corpusID, after string, limit int) ([]Record, error) {
	if !scope.Allows("content:read") {
		return nil, corpus.ErrForbidden
	}
	if !scope.Contains(corpusID) {
		return nil, corpus.ErrNotFound
	}
	return s.Catalog.Records(ctx, scope.Organization, corpusID, after, limit)
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
	if NormalizedText(manifest) != string(text) {
		return Version{}, errors.New("canonical artifact mismatch")
	}
	relations, err := s.resolveRelations(ctx, scope, manifest.Relations)
	if err != nil {
		return Version{}, err
	}
	return Version{RecordID: recordID, ID: id, Manifest: manifest, Extensions: stored.Extensions, Provenance: stored.Provenance, Availability: stored.Availability, Relations: relations, Processing: stored.Processing}, nil
}

// resolveRelations expands source Relations independently of the immutable
// Manifest. A resolver outage is an infrastructure failure, never a silent
// "unavailable" that could be mistaken for a negative trust decision.
func (s Service) resolveRelations(ctx context.Context, scope corpus.Scope, relations []Relation) ([]ResolvedRelation, error) {
	out := make([]ResolvedRelation, len(relations))
	for i, r := range relations {
		out[i] = ResolvedRelation{Source: r, Status: "unavailable"}
	}
	if len(relations) == 0 {
		return out, nil
	}
	if s.Relations == nil {
		return nil, errors.New("relation resolution unavailable")
	}
	resolved, err := s.Relations.Resolve(ctx, scope, relations)
	if err != nil {
		return nil, err
	}
	if len(resolved) != len(relations) {
		return nil, errors.New("relation resolution incomplete")
	}
	return resolved, nil
}

// ManifestFor returns the canonical Manifest an accepted Command publishes. An
// inline text or Blob leaf normalizes to the single body Part contract.
func ManifestFor(c Command) Manifest {
	if c.Manifest != nil {
		m := *c.Manifest
		if m.Kind == "" {
			m.Kind = "manifest"
		}
		return m
	}
	return Manifest{Kind: "manifest", Parts: []Part{{Key: "body", Role: "body", Content: Text{Kind: "text", Text: c.Content.Text}}}}
}

// NormalizedText is the source-faithful concatenation of text Parts used as the
// canonical Version text pointer. It does not replace per-Part coordinates.
func NormalizedText(m Manifest) string {
	var b strings.Builder
	written := false
	for _, p := range m.Parts {
		if p.Content.Kind != "text" {
			continue
		}
		if written {
			b.WriteByte('\n')
		}
		b.WriteString(p.Content.Text)
		written = true
	}
	return b.String()
}

// resolveBlob verifies and reads a same-Organization text Blob's immutable bytes.
func (s Service) resolveBlob(ctx context.Context, org string, ref Text) ([]byte, error) {
	if s.BlobSource == nil || s.Blobs == nil || ref.BlobID == "" || ref.MediaType == "" {
		return nil, ErrUnverifiedBlob
	}
	if !strings.HasPrefix(strings.ToLower(ref.MediaType), "text/") {
		return nil, ErrUnverifiedBlob
	}
	verified, err := s.BlobSource.VerifiedBlob(ctx, org, ref.BlobID)
	if err != nil && !errors.Is(err, ErrUnverifiedBlob) {
		return nil, err
	}
	if err != nil || verified.MediaType != ref.MediaType {
		return nil, ErrUnverifiedBlob
	}
	if verified.Blob.Size < 1 || verified.Blob.Size > maxCanonicalBlobBytes {
		return nil, ErrUnsupported
	}
	data, err := s.Blobs.Read(ctx, verified.Blob)
	if err != nil {
		return nil, err
	}
	// The referenced object must still be the verified bytes; possession of a
	// Blob ID never authorizes altered content.
	if int64(len(data)) != verified.Blob.Size || Hash(data) != verified.Blob.SHA256 {
		return nil, ErrUnverifiedBlob
	}
	if !ValidText(string(data)) {
		return nil, ErrInvalid
	}
	return data, nil
}
func Digest(c Command) string {
	b, _ := json.Marshal(struct {
		Manifest   Manifest   `json:"manifest"`
		Extensions Extensions `json:"extensions,omitempty"`
	}{Manifest: ManifestFor(c), Extensions: c.Extensions})
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
	manifest := ManifestFor(work.Command)
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	normalized, err := s.Blobs.Put(ctx, org, []byte(NormalizedText(manifest)))
	if err != nil {
		_ = s.Repository.Progress(ctx, org, receiptID, "retrying", "blob_verification_unavailable")
		return errors.New("canonical text publication unavailable")
	}
	manifestBlob, err := s.Blobs.Put(ctx, org, manifestBytes)
	if err != nil {
		_ = s.Repository.Progress(ctx, org, receiptID, "retrying", "blob_verification_unavailable")
		return errors.New("canonical manifest publication unavailable")
	}
	publication := Publication{Normalized: normalized, Manifest: manifestBlob}
	for _, part := range manifest.Parts {
		if part.Content.Kind != "text" {
			continue
		}
		blob, err := s.Blobs.Put(ctx, org, []byte(part.Content.Text))
		if err != nil {
			_ = s.Repository.Progress(ctx, org, receiptID, "retrying", "blob_verification_unavailable")
			return errors.New("canonical part publication unavailable")
		}
		publication.Parts = append(publication.Parts, PartBlob{Key: part.Key, Role: part.Role, Blob: blob})
	}
	if err = s.Repository.Publish(ctx, work, publication); err != nil {
		_ = s.Repository.Progress(ctx, org, receiptID, "retrying", "publication_unavailable")
		return errors.New("canonical transaction unavailable")
	}
	return nil
}

// Dispatch names one accepted command that still needs a durable workflow start.
type Dispatch struct{ Organization, ReceiptID string }

var ErrNoDispatch = errors.New("no_pending_dispatch")
