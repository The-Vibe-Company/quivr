// Package content owns durable acceptance and immutable Version publication.
package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/internal/audit"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
)

var (
	ErrConflict       = publicerr.IdempotencyConflict
	ErrUnsupported    = publicerr.UnsupportedContent
	ErrInvalid        = publicerr.InvalidInput
	ErrUnverifiedBlob = publicerr.UnverifiedBlob
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
	// SourceMediaType is engine-owned acceptance metadata, stored apart from
	// the canonical command so existing receipt replays keep their identity.
	SourceMediaType string         `json:"-"`
	Key             string         `json:"idempotency_key"`
	Source          Source         `json:"source"`
	Revision        string         `json:"source_revision,omitempty"`
	Position        string         `json:"source_position,omitempty"`
	Content         Text           `json:"content"`
	Manifest        *Manifest      `json:"manifest,omitempty"`
	Extensions      Extensions     `json:"extensions,omitempty"`
	Provenance      map[string]any `json:"provenance,omitempty"`
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

// Diagnostic explains why processing needs attention. Plugin, Contribution
// and InvocationID name the external invocation a normalization diagnostic
// concerns.
type Diagnostic struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Plugin    string `json:"plugin,omitempty"`
	// PluginVersion and Plan name the plugin version and the Pipeline Plan
	// that work pinned to a plan was stopped on.
	PluginVersion string `json:"plugin_version,omitempty"`
	Plan          string `json:"plan,omitempty"`
	Contribution  string `json:"contribution,omitempty"`
	InvocationID  string `json:"invocation_id,omitempty"`
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
	ID                string     `json:"record_id"`
	Source            Source     `json:"source"`
	Withdrawn         bool       `json:"withdrawn"`
	CurrentVersionID  string     `json:"current_version_id,omitempty"`
	CurrentAcceptedAt *time.Time `json:"-"`
}
type Version struct {
	// SourceMediaType routes ingestion after normalization.
	SourceMediaType string             `json:"-"`
	RecordID        string             `json:"record_id"`
	ID              string             `json:"version_id"`
	Manifest        Manifest           `json:"manifest"`
	Extensions      Extensions         `json:"extensions,omitempty"`
	Provenance      map[string]any     `json:"provenance,omitempty"`
	Availability    Availability       `json:"availability"`
	Relations       []ResolvedRelation `json:"relations"`
	Processing      Processing         `json:"processing"`
	// Diagnostics explain a quarantine, a normalizer fallback or a recorded
	// normalizer conflict.
	Diagnostics []Diagnostic `json:"diagnostics"`
	// AcceptedAt is when the revision this Version publishes was accepted;
	// nil when no receipt records it.
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
	// Steps are when each processing step finished; public reads only.
	Steps Steps `json:"-"`
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
	// Quarantine, when set, publishes the Version quarantined with this
	// reason in the same transaction, announced by record.quarantined.
	Quarantine *Diagnostic
}

type StoredVersion struct {
	SourceMediaType        string
	RecordID, ID, CorpusID string
	AcceptedAt             *time.Time
	Steps                  Steps
	ManifestBlob, TextBlob Blob
	Provenance             map[string]any
	Extensions             Extensions
	Availability           Availability
	Processing             Processing
	Diagnostics            []Diagnostic
}
type Work struct {
	Organization, ReceiptID, RecordID, VersionID, Digest, Slot string
	Order                                                      int64
	Position, PredecessorID                                    string
	Command                                                    Command
}

// SubmissionStore commits validated ingestion and withdrawal commands.
type SubmissionStore interface {
	Accept(context.Context, corpus.Scope, Command) (Receipt, error)
	Withdraw(context.Context, corpus.Scope, Withdrawal) (Receipt, error)
}

// ReceiptReader reads the durable outcome of one accepted command.
type ReceiptReader interface {
	Receipt(context.Context, string, string) (Receipt, error)
}

// RecordReader reads a stable Record identity and its current Version pointer.
type RecordReader interface {
	Record(context.Context, string, string) (Record, error)
}

// VersionReader reads immutable canonical content and current availability.
type VersionReader interface {
	Version(context.Context, string, string, string) (StoredVersion, error)
}

// MaterializationStore runs the durable publication step for accepted work.
type MaterializationStore interface {
	Work(context.Context, string, string) (Work, bool, error)
	Progress(context.Context, string, string, string, string) error
	Publish(context.Context, Work, Publication) error
}

// RecordCatalog reads one Corpus's catalog, including withdrawn Records.
// Each page is an independent read; the change journal covers concurrent writes.
type RecordCatalog interface {
	Records(context.Context, string, string, RecordQuery) ([]Record, error)
	CountRecords(context.Context, string, string, RecordQuery) (int64, error)
}
type Blobs interface {
	Put(context.Context, string, []byte) (Blob, error)
	Read(context.Context, Blob) ([]byte, error)
}
type Service struct {
	Corpora         corpus.Store
	Submissions     SubmissionStore
	Receipts        ReceiptReader
	RecordStore     RecordReader
	Versions        VersionReader
	Materialization MaterializationStore
	Catalog         RecordCatalog
	Facets          FacetReader
	Blobs           Blobs
	Baseline        BaselineRepository
	Embeddings      EmbeddingRepository
	BlobSource      BlobSource
	Relations       RelationResolver
	Extensions      ExtensionValidator
	// Routes names the Blob media types an external normalizer handles; nil
	// routes none. Normalizations holds their durable validated output.
	Routes         NormalizerRoutes
	Normalizations NormalizationStore
	// Supersession lets a routed Version skip normalization once its Record
	// was withdrawn or desires another revision; nil never skips.
	Supersession Supersession
	// Received observes each accepted command that reserved a new revision,
	// by Organization and source namespace (THE-798); nil observes nothing.
	// It must not block: it runs on the request path.
	Received func(organization, sourceNamespace string)
}

func (s Service) Accept(ctx context.Context, scope corpus.Scope, c Command) (Receipt, error) {
	if err := scope.Require(corpus.ActionContentAccept); err != nil {
		return Receipt{}, err
	}
	return s.accept(ctx, scope, c, scope.Allows("content:read"))
}

func (s Service) accept(ctx context.Context, scope corpus.Scope, c Command, reveal bool) (Receipt, error) {
	if !scope.Contains(c.Source.CorpusID) {
		return Receipt{}, corpus.ErrNotFound
	}
	if _, forged := c.Provenance["normalization"]; forged {
		// Normalization provenance is engine-owned; only publication writes it.
		return Receipt{}, ErrInvalid
	}
	c.SourceMediaType = "text/plain"
	if c.Content.Kind == "blob" {
		c.SourceMediaType = c.Content.MediaType
	}
	switch c.Content.Kind {
	case "text":
		// Inline text cannot assert Blob provenance; only verified references may.
		if ids, ok := c.Provenance["source_blob_ids"].([]any); ok && len(ids) > 0 {
			return Receipt{}, ErrUnsupported
		}
	case "blob":
		if s.Routes != nil && s.Routes.Routed(ctx, c.Content.MediaType) {
			// A routed Blob is normalized after acceptance, never read here: the
			// Version identity derives from the verified submitted input.
			checksum, err := s.verifyPartBlob(ctx, scope.Organization, c.Content)
			if err != nil {
				return Receipt{}, err
			}
			if c.Provenance == nil {
				c.Provenance = map[string]any{}
			}
			c.Provenance["source_blob_ids"] = []any{c.Content.BlobID}
			c.Content = Text{Kind: "blob", BlobID: c.Content.BlobID, MediaType: c.Content.MediaType, BlobSHA256: checksum}
			break
		}
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
	if c.Content.Kind == "text" && (c.Content.Text == "" || !ValidText(c.Content.Text)) {
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
	result, err := s.Submissions.Accept(ctx, scope, c)
	if err == nil && result.NewRevision && s.Received != nil {
		s.Received(scope.Organization, c.Source.Namespace)
	}
	if !reveal {
		result.RecordID = ""
		result.VersionID = ""
		result.Availability = nil
	}
	return result, err
}

// Withdraw durably fences a Record identity. The record may exist already or be
// fenced before any materialization; a later ordinary ingestion of that identity
// is a terminal conflict.
func (s Service) Withdraw(ctx context.Context, scope corpus.Scope, w Withdrawal, prepare ...func() (Withdrawal, error)) (Receipt, error) {
	if err := scope.Require(corpus.ActionContentWithdraw); err != nil {
		return Receipt{}, err
	}
	for _, load := range prepare {
		var err error
		w, err = load()
		if err != nil {
			return Receipt{}, err
		}
	}
	return s.withdraw(ctx, scope, w, scope.Allows("content:read"))
}

func (s Service) withdraw(ctx context.Context, scope corpus.Scope, w Withdrawal, reveal bool) (Receipt, error) {
	if !scope.Contains(w.Source.CorpusID) {
		return Receipt{}, corpus.ErrNotFound
	}
	if w.Key == "" || w.Source.CorpusID == "" || w.Source.Namespace == "" || w.Source.RecordKey == "" || !ValidText(w.Reason) {
		return Receipt{}, ErrInvalid
	}
	result, err := s.Submissions.Withdraw(ctx, scope, w)
	if err == nil {
		audit.RecordTarget(ctx, "record", result.RecordID)
	}
	if !reveal {
		result.RecordID = ""
		result.VersionID = ""
		result.Availability = nil
	}
	return result, err
}

// validateManifest enforces the semantic Manifest constraints the JSON Schema
// cannot express: the structural rules of CheckManifest, plus verified
// same-Organization Blob Parts and schema-validated Part extensions. Role
// semantics beyond preservation belong to the processing profile, which blocks
// rather than rejects an unsupported combination.
func (s Service) validateManifest(ctx context.Context, org string, m *Manifest) error {
	return CheckManifest(m, func(i int) error {
		p := m.Parts[i]
		if p.Content.Kind == "blob" {
			checksum, err := s.verifyPartBlob(ctx, org, p.Content)
			if err != nil {
				return err
			}
			// Preserve the verified checksum in the immutable canonical Manifest.
			m.Parts[i].Content.BlobSHA256 = checksum
		}
		return s.validateExtensions(ctx, p.Extensions)
	})
}

// ManifestViolation is a structural Manifest rejection. Its public code
// resolves through Kind (ErrInvalid or ErrUnsupported) with publicerr.Code, and
// Error reports only that code so logs never echo submitted keys; Detail
// carries the actionable explanation for tooling such as the Plugin Contract
// Runner.
type ManifestViolation struct {
	Kind   error
	Detail string
}

func (v *ManifestViolation) Error() string { return v.Kind.Error() }
func (v *ManifestViolation) Unwrap() error { return v.Kind }

func violation(kind error, format string, args ...any) error {
	return &ManifestViolation{Kind: kind, Detail: fmt.Sprintf(format, args...)}
}

// CheckManifest applies the engine's structural Manifest rules that JSON Schema
// cannot express: a bounded non-empty Part list, unique Part keys, valid text,
// known content kinds, an acyclic same-Manifest parent hierarchy, complete
// Relation targets and a bounded non-text structure. part, when non-nil, runs
// once per Part in order, after that Part's own structural checks; acceptance
// uses it for Blob verification and extension validation. Errors are
// *ManifestViolation wrapping ErrInvalid or ErrUnsupported (or part's error). The Plugin Contract Runner
// reuses this function so "passes the runner" means "the engine accepts it".
func CheckManifest(m *Manifest, part func(i int) error) error {
	if len(m.Parts) == 0 {
		return violation(ErrInvalid, "a Manifest needs at least one Part")
	}
	if len(m.Parts) > maxManifestParts {
		return violation(ErrUnsupported, "%d Parts exceed the limit of %d", len(m.Parts), maxManifestParts)
	}
	byKey := make(map[string]Part, len(m.Parts))
	for i := range m.Parts {
		p := m.Parts[i]
		if p.Key == "" || p.Role == "" {
			return violation(ErrInvalid, "Part %d needs a key and a role", i)
		}
		if _, exists := byKey[p.Key]; exists {
			return violation(ErrInvalid, "duplicate Part key %q", p.Key)
		}
		byKey[p.Key] = p
		switch p.Content.Kind {
		case "text":
			if p.Content.Text == "" || !ValidText(p.Content.Text) {
				return violation(ErrInvalid, "Part %q text must be non-empty valid UTF-8 without NUL", p.Key)
			}
		case "blob":
		default:
			return violation(ErrUnsupported, "Part %q has unsupported content kind %q", p.Key, p.Content.Kind)
		}
		if part != nil {
			if err := part(i); err != nil {
				return err
			}
		}
	}
	for _, p := range m.Parts {
		if p.ParentKey == "" {
			continue
		}
		if p.ParentKey == p.Key {
			return violation(ErrInvalid, "Part %q is its own parent", p.Key)
		}
		parent, ok := byKey[p.ParentKey]
		if !ok {
			return violation(ErrInvalid, "Part %q names unknown parent %q", p.Key, p.ParentKey)
		}
		seen := map[string]bool{}
		for {
			if seen[parent.Key] {
				return violation(ErrInvalid, "parent cycle through Part %q", parent.Key)
			}
			seen[parent.Key] = true
			if parent.ParentKey == "" {
				break
			}
			parent, ok = byKey[parent.ParentKey]
			if !ok {
				return violation(ErrInvalid, "Part %q names unknown parent %q", p.Key, p.ParentKey)
			}
		}
	}
	for i, r := range m.Relations {
		if r.Type == "" || r.Target.CorpusID == "" || r.Target.Namespace == "" || r.Target.RecordKey == "" {
			return violation(ErrInvalid, "Relation %d needs a type and a complete target", i)
		}
	}
	if !boundedManifestStructure(m) {
		return violation(ErrUnsupported, "Manifest structure exceeds %d bytes", maxGenericJSONBytes)
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
	return CheckExtensions(ctx, s.Extensions, exts)
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
	if err := scope.Require(corpus.ActionContentReceipt); err != nil {
		return Receipt{}, err
	}
	r, err := s.Receipts.Receipt(ctx, scope.Organization, id)
	if err == nil && !scope.Contains(r.Source.CorpusID) {
		return Receipt{}, corpus.ErrNotFound
	}
	return r, err
}
func (s Service) Record(ctx context.Context, scope corpus.Scope, id string) (Record, error) {
	if err := scope.Require(corpus.ActionContentRecord); err != nil {
		return Record{}, err
	}
	r, err := s.record(ctx, scope, id)
	if err == nil {
		err = s.visibleCorpus(ctx, scope.Organization, r.Source.CorpusID)
	}
	if err != nil {
		return Record{}, err
	}
	return r, nil
}

func (s Service) record(ctx context.Context, scope corpus.Scope, id string) (Record, error) {
	r, err := s.RecordStore.Record(ctx, scope.Organization, id)
	if err == nil && !scope.Contains(r.Source.CorpusID) {
		return Record{}, corpus.ErrNotFound
	}
	return r, err
}

func (s Service) Version(ctx context.Context, scope corpus.Scope, recordID, id string) (Version, error) {
	if err := scope.Require(corpus.ActionContentVersion); err != nil {
		return Version{}, err
	}
	stored, err := s.storedVersion(ctx, scope, recordID, id)
	if err == nil {
		err = s.visibleCorpus(ctx, scope.Organization, stored.CorpusID)
	}
	if err != nil {
		return Version{}, err
	}
	return s.versionFromStored(ctx, scope, recordID, id, stored)
}

// Public content reads respect archive visibility. Trusted workers still read
// retained content so archiving cannot consume or strand their pending work.
func (s Service) visibleCorpus(ctx context.Context, org, id string) error {
	if s.Corpora == nil {
		return nil
	}
	c, err := s.Corpora.Read(ctx, org, id)
	if err == nil && c.Archived {
		return corpus.ErrNotFound
	}
	return err
}

func (s Service) version(ctx context.Context, scope corpus.Scope, recordID, id string) (Version, error) {
	stored, err := s.storedVersion(ctx, scope, recordID, id)
	if err != nil {
		return Version{}, err
	}
	return s.versionFromStored(ctx, scope, recordID, id, stored)
}
func (s Service) storedVersion(ctx context.Context, scope corpus.Scope, recordID, id string) (StoredVersion, error) {
	stored, err := s.Versions.Version(ctx, scope.Organization, recordID, id)
	if err != nil {
		return StoredVersion{}, err
	}
	if !scope.Contains(stored.CorpusID) {
		return StoredVersion{}, corpus.ErrNotFound
	}
	return stored, nil
}
func (s Service) versionFromStored(ctx context.Context, scope corpus.Scope, recordID, id string, stored StoredVersion) (Version, error) {
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
	diagnostics := stored.Diagnostics
	if diagnostics == nil {
		diagnostics = []Diagnostic{}
	}
	return Version{SourceMediaType: stored.SourceMediaType, RecordID: recordID, ID: id, AcceptedAt: stored.AcceptedAt, Steps: stored.Steps, Manifest: manifest, Extensions: stored.Extensions, Provenance: stored.Provenance, Availability: stored.Availability, Relations: relations, Processing: stored.Processing, Diagnostics: diagnostics}, nil
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
// inline text or Blob leaf normalizes to the single body Part contract. A
// routed Blob, still a verified reference after acceptance, is described by
// its single source Blob Part: that is the Version identity digest input,
// while publication uses the durable normalizer output instead.
func ManifestFor(c Command) Manifest {
	if c.Content.Kind == "blob" {
		return Manifest{Kind: "manifest", Parts: []Part{{Key: "source", Role: "source", Content: c.Content}}}
	}
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
	work, done, err := s.Materialization.Work(ctx, org, receiptID)
	if err != nil || done {
		return err
	}
	if err = s.Materialization.Progress(ctx, org, receiptID, "running", ""); err != nil {
		return err
	}
	manifest := ManifestFor(work.Command)
	var quarantine *Diagnostic
	if work.Command.Content.Kind == "blob" {
		manifest, quarantine, err = s.routedManifest(ctx, &work)
		if errors.Is(err, errWithdrawn) {
			// A withdrawn Record never publishes: publication resolves the
			// receipt as a conflict without reading any object.
			if err = s.Materialization.Publish(ctx, work, Publication{}); err != nil {
				_ = s.Materialization.Progress(ctx, org, receiptID, "retrying", "publication_unavailable")
				return errors.New("canonical transaction unavailable")
			}
			return nil
		}
		if err != nil {
			code := "blob_verification_unavailable"
			if errors.Is(err, ErrNormalizationPending) {
				code = "normalization_pending"
			}
			_ = s.Materialization.Progress(ctx, org, receiptID, "retrying", code)
			return err
		}
	}
	publication, err := s.objects(ctx, org, manifest)
	if err != nil {
		_ = s.Materialization.Progress(ctx, org, receiptID, "retrying", "blob_verification_unavailable")
		return err
	}
	publication.Quarantine = quarantine
	if err = s.Materialization.Publish(ctx, work, publication); err != nil {
		_ = s.Materialization.Progress(ctx, org, receiptID, "retrying", "publication_unavailable")
		return errors.New("canonical transaction unavailable")
	}
	return nil
}

// objects stores the immutable objects a publication of manifest references:
// its normalized text, the Manifest and each text Part.
func (s Service) objects(ctx context.Context, org string, manifest Manifest) (Publication, error) {
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return Publication{}, err
	}
	normalized, err := s.Blobs.Put(ctx, org, []byte(NormalizedText(manifest)))
	if err != nil {
		return Publication{}, errors.New("canonical text publication unavailable")
	}
	manifestBlob, err := s.Blobs.Put(ctx, org, manifestBytes)
	if err != nil {
		return Publication{}, errors.New("canonical manifest publication unavailable")
	}
	publication := Publication{Normalized: normalized, Manifest: manifestBlob}
	for _, part := range manifest.Parts {
		if part.Content.Kind != "text" {
			continue
		}
		blob, err := s.Blobs.Put(ctx, org, []byte(part.Content.Text))
		if err != nil {
			return Publication{}, errors.New("canonical part publication unavailable")
		}
		publication.Parts = append(publication.Parts, PartBlob{Key: part.Key, Role: part.Role, Blob: blob})
	}
	return publication, nil
}

// ErrRepublicationWithdrawn reports a Record withdrawn before a quarantined
// Version could be published again.
var ErrRepublicationWithdrawn = errors.New("record withdrawn")

// Republication decides again what a Version published quarantined at
// either stage publishes, from its normalization outcome recorded since:
// the objects of the normalized (or fallback) Manifest with the Work carrying
// its provenance and extensions, or, when normalization failed again or the
// Version can no longer be normalized, the reason it stays quarantined. It
// never writes the Version; it is ErrNormalizationPending while no outcome is
// recorded and ErrRepublicationWithdrawn for a withdrawn Record.
func (s Service) Republication(ctx context.Context, org, receiptID string) (Work, Publication, *Diagnostic, error) {
	work, _, err := s.Materialization.Work(ctx, org, receiptID)
	if err != nil {
		return work, Publication{}, nil, err
	}
	if work.Command.Content.Kind != "blob" {
		return work, Publication{}, nil, fmt.Errorf("%w: the Version was not published from a Blob", ErrInvalid)
	}
	manifest, quarantine, err := s.routedManifest(ctx, &work)
	switch {
	case errors.Is(err, errWithdrawn):
		return work, Publication{}, nil, ErrRepublicationWithdrawn
	case err != nil:
		return work, Publication{}, nil, err
	case quarantine != nil:
		return work, Publication{}, quarantine, nil
	}
	publication, err := s.objects(ctx, org, manifest)
	return work, publication, nil, err
}

// Dispatch names one accepted command that still needs a durable workflow start.
type Dispatch struct{ Organization, ReceiptID, TraceContext string }

// DispatchBatch is an immutable, durable group of receipt intents. Its ID
// survives a lost workflow-start acknowledgement and worker restarts.
type DispatchBatch struct {
	ID        string
	WorkQueue string `json:",omitempty"`
	Receipts  []Dispatch
	// Legacy preserves an old receipt's workflow identity after a lost start
	// acknowledgement, including receipts accepted by an older API process.
	Legacy bool
}

var ErrNoDispatch = errors.New("no_pending_dispatch")

// NormalizerRoutes reports whether a Blob media type is routed to an external
// normalizer.
type NormalizerRoutes interface {
	Routed(ctx context.Context, mediaType string) bool
}

// Supersession reports, before any external normalization, whether a Record
// was withdrawn or whether a Version can no longer become current because the
// Record desires another accepted revision.
type Supersession interface {
	Superseded(ctx context.Context, org, recordID, versionID string) (withdrawn, superseded bool, err error)
}

// Normalization is the bounded provenance of one external normalization,
// published as the Version's provenance.normalization. producer and
// producer_version keep naming the acquirer.
type Normalization struct {
	PluginID       string `json:"plugin_id"`
	PluginVersion  string `json:"plugin_version"`
	PluginAPI      string `json:"plugin_api"`
	Contribution   string `json:"contribution"`
	InvocationID   string `json:"invocation_id"`
	IdempotencyKey string `json:"idempotency_key"`
	InputSHA256    string `json:"input_sha256"`
	// Fallback is set when an optional route's plugin failed and the built-in
	// text path produced the published Manifest; InvocationID then names the
	// failed invocation.
	Fallback *NormalizationFallback `json:"fallback,omitempty"`
}

// NormalizationFallback is the failure an optional route fell back from.
type NormalizationFallback struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Normalization outcomes.
const (
	// OutcomeNormalized publishes the plugin's validated Manifest.
	OutcomeNormalized = "normalized"
	// OutcomeFallback publishes the built-in text path's Manifest after
	// an optional route failed.
	OutcomeFallback = "fallback"
	// OutcomeFailed publishes nothing from the plugin: the Version is
	// published quarantined with its submitted input Manifest.
	OutcomeFailed = "failed"
)

// NormalizationFailure is the structured reason of a failed or fallen-back
// normalization.
type NormalizationFailure struct {
	Code      string
	Message   string
	Retryable bool
	// Plan is the Pipeline Plan of work stopped because its normalizer left
	// the active plan and stayed unreachable.
	Plan string
}

// NormalizationConflict records a divergent output for the recorded
// idempotency key. It never replaces the recorded Manifest.
type NormalizationConflict struct {
	InvocationID   string
	ManifestSHA256 string
}

// Normalized is the durable outcome of one Record Version's external
// normalization: its Manifest object (none when failed), its provenance, the
// extensions it produced, and the failure or conflict it records.
type Normalized struct {
	// Outcome is one of the Outcome* values; empty means normalized.
	Outcome     string
	Manifest    Blob
	Provenance  Normalization
	InputBlobID string
	// Extensions are the top-level extensions the plugin produced in its own
	// declared namespaces, published beside the submitted ones.
	Extensions Extensions
	Failure    *NormalizationFailure
	Conflict   *NormalizationConflict
}

// Failed reports whether nothing from the plugin may be published.
func (n Normalized) Failed() bool { return n.Outcome == OutcomeFailed }

// Diagnostics are the public diagnostics a normalization outcome contributes
// to its Version: the failure it quarantined or fell back from, and a recorded
// conflict.
func (n Normalized) Diagnostics() []Diagnostic {
	var out []Diagnostic
	p := n.Provenance
	if n.Failure != nil {
		d := Diagnostic{Code: n.Failure.Code, Message: n.Failure.Message, Retryable: n.Failure.Retryable, Plugin: p.PluginID, Contribution: p.Contribution, InvocationID: p.InvocationID}
		if n.Failure.Plan != "" {
			d.Plan, d.PluginVersion = n.Failure.Plan, p.PluginVersion
		}
		out = append(out, d)
	}
	if n.Conflict != nil {
		out = append(out, Diagnostic{Code: CodeNormalizerConflict, Message: "A later invocation with the same idempotency key returned a different output; the recorded output was kept.", Plugin: p.PluginID, Contribution: p.Contribution, InvocationID: n.Conflict.InvocationID})
	}
	return out
}

// Codes of normalization outcomes that do not come from a plugin answer.
const (
	CodeNormalizerConflict      = "normalizer_conflict"
	CodeNormalizerUnrouted      = "normalizer_unrouted"
	CodeNormalizationSuperseded = "normalization_superseded"
)

// NormalizationStore reads the durable normalization outcome of a Record Version.
type NormalizationStore interface {
	Normalized(ctx context.Context, org, versionID string) (Normalized, bool, error)
}

// ErrNormalizationPending means a routed Version has no durable normalization
// outcome yet; publication retries after normalization.
var ErrNormalizationPending = errors.New("normalization_pending")

var errWithdrawn = errors.New("record withdrawn")

// BuiltinManifest is the built-in text path's Manifest for a text Blob: one
// body Part holding the verified bytes, exactly as an unrouted text Blob is
// accepted. It fails with ErrUnverifiedBlob, ErrUnsupported or ErrInvalid when
// the built-in path cannot take the Blob.
func (s Service) BuiltinManifest(ctx context.Context, org string, ref Text) (Manifest, error) {
	data, err := s.resolveBlob(ctx, org, ref)
	if err != nil {
		return Manifest{}, err
	}
	return ManifestFor(Command{Content: Text{Kind: "text", Text: string(data)}}), nil
}

// BuiltinRefusal reports whether err is the built-in text path refusing a
// Blob's bytes, as opposed to an outage.
func BuiltinRefusal(err error) bool {
	return errors.Is(err, ErrUnverifiedBlob) || errors.Is(err, ErrUnsupported) || errors.Is(err, ErrInvalid)
}

// routedManifest decides what a routed Blob Version publishes, from its
// durable normalization outcome, and records normalization provenance on the
// Work (never on the stored Command). Without an outcome, a withdrawn Record
// resolves as a conflict, a superseded Version and a Blob whose route was
// removed (and that the built-in text path cannot take) are quarantined, and a
// removed route over a text Blob takes the built-in text path; otherwise the
// Version waits for normalization.
func (s Service) routedManifest(ctx context.Context, work *Work) (Manifest, *Diagnostic, error) {
	c := work.Command
	if s.Normalizations == nil {
		return Manifest{}, nil, ErrNormalizationPending
	}
	n, found, err := s.Normalizations.Normalized(ctx, work.Organization, work.VersionID)
	if err != nil {
		return Manifest{}, nil, err
	}
	if found {
		if n.Failed() {
			diagnostics := n.Diagnostics()
			return ManifestFor(c), &diagnostics[0], nil
		}
		return s.normalizedManifest(ctx, work, n)
	}
	if s.Supersession != nil {
		withdrawn, superseded, err := s.Supersession.Superseded(ctx, work.Organization, work.RecordID, work.VersionID)
		if err != nil {
			return Manifest{}, nil, err
		}
		if withdrawn {
			return Manifest{}, nil, errWithdrawn
		}
		if superseded {
			return ManifestFor(c), &Diagnostic{Code: CodeNormalizationSuperseded, Message: "A newer revision of the Record was accepted before this Version was normalized; the normalizer was not invoked."}, nil
		}
	}
	if s.Routes != nil && s.Routes.Routed(ctx, c.Content.MediaType) {
		return Manifest{}, nil, ErrNormalizationPending
	}
	// The route was removed after acceptance.
	m, err := s.BuiltinManifest(ctx, work.Organization, c.Content)
	if BuiltinRefusal(err) {
		return ManifestFor(c), &Diagnostic{Code: CodeNormalizerUnrouted, Message: fmt.Sprintf("No normalizer route handles %q any more and the built-in text path cannot read this Blob.", c.Content.MediaType)}, nil
	}
	return m, nil, err
}

// normalizedManifest loads the recorded normalized or fallback Manifest and
// records its provenance on the Work.
func (s Service) normalizedManifest(ctx context.Context, work *Work, n Normalized) (Manifest, *Diagnostic, error) {
	data, err := s.Blobs.Read(ctx, n.Manifest)
	if err != nil {
		return Manifest{}, nil, err
	}
	var m Manifest
	if err = json.Unmarshal(data, &m); err != nil {
		return Manifest{}, nil, fmt.Errorf("normalized manifest: %w", ErrArtifactCorrupt)
	}
	raw, err := json.Marshal(n.Provenance)
	if err != nil {
		return Manifest{}, nil, err
	}
	var normalization map[string]any
	if err = json.Unmarshal(raw, &normalization); err != nil {
		return Manifest{}, nil, err
	}
	provenance := make(map[string]any, len(work.Command.Provenance)+1)
	for k, v := range work.Command.Provenance {
		provenance[k] = v
	}
	provenance["normalization"] = normalization
	work.Command.Provenance = provenance
	if len(n.Extensions) > 0 {
		// Common metadata is shared by acquirers and normalizers. Overlay only
		// the fields supplied by normalization, preserving the source's other
		// values without mutating the accepted command.
		extensions := make(Extensions, len(work.Command.Extensions)+len(n.Extensions))
		for ns, ext := range work.Command.Extensions {
			extensions[ns] = ext
		}
		for ns, ext := range n.Extensions {
			if ns == CommonMetadataNamespace {
				data := make(map[string]any)
				for key, value := range extensions[ns].Data {
					data[key] = value
				}
				for key, value := range ext.Data {
					data[key] = value
				}
				ext.Data = data
			}
			extensions[ns] = ext
		}
		work.Command.Extensions = extensions
	}
	return m, nil, nil
}
