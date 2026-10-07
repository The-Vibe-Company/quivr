package content

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
)

// VectorSpace is a registered vector space: ID is its identity (for a
// plugin space, "<space id>@<space version>"), Manifest its immutable
// description, Dimensions the vector size (0 leaves the size unchecked).
type VectorSpace struct {
	ID         string
	Manifest   json.RawMessage
	Dimensions int
}

// MaxDimensions bounds any vector the engine stores.
const MaxDimensions = 4096

// Space roles of the registry: served spaces answer search, evaluation spaces
// are indexed and compared but never served, retired spaces are no longer
// written to new generations.
const (
	SpaceServed     = "served"
	SpaceEvaluation = "evaluation"
	SpaceRetired    = "retired"
)

// RegisteredSpace is one entry of the vector space registry: the space, its
// owner (an ingestion plugin, or the engine itself when OwnerPluginID is
// empty), its description and its role in this deployment.
type RegisteredSpace struct {
	VectorSpace
	// Name and Version are the declared space id and version.
	Name, Version                     string
	OwnerPluginID, OwnerPluginVersion string
	Model, Metric                     string
	Indexes, QueryModalities          []string
	Role                              string
}
type Embedding struct {
	ID             string `json:"embedding_artifact_id"`
	DerivationID   string `json:"derivation_id"`
	Organization   string `json:"organization_id"`
	CorpusID       string `json:"corpus_id"`
	VersionID      string `json:"version_id"`
	SegmentID      string `json:"segment_id"`
	PartKey        string `json:"part_key"`
	SegmentationID string `json:"segmentation_id"`
	Recipe         string `json:"segmentation_recipe"`
	SourceSHA      string `json:"normalized_content_sha256"`
	SliceSHA       string `json:"slice_sha256"`
	InputSHA       string `json:"input_sha256"`
	InputBytes     int    `json:"input_bytes"`
	SpaceID        string `json:"vector_space_id"`
	Producer       string `json:"producer"`
	Payload        Blob   `json:"payload"`
	Manifest       Blob   `json:"manifest"`
}
type EmbeddingRepository interface {
	Embedding(context.Context, string, string) (Embedding, error)
	SaveEmbedding(context.Context, Embedding, VectorSpace) error
	EnrichmentProgress(context.Context, string, string, string, string) error
	// CountEnrichmentTimeout records that an enrichment call for a Version
	// ended at the plugin's deadline and returns how many have.
	CountEnrichmentTimeout(ctx context.Context, org, versionID string) (int, error)
	// BlockEnrichment stops a Version's enrichment with a structured reason,
	// listed in the Version's diagnostics.
	BlockEnrichment(ctx context.Context, org, versionID string, reason Diagnostic) error
	CommitEnrichment(context.Context, string, Segmentation, Generation, []Embedding) error
	// EnrichmentEligible reports whether a Version is its Record's current,
	// eligible Version, the only kind search can serve.
	EnrichmentEligible(ctx context.Context, org, versionID string) (bool, error)
}

// EnrichmentEligible reports whether enrichment of a Version can still serve search.
func (s Service) EnrichmentEligible(ctx context.Context, org, versionID string) (bool, error) {
	if org == "" || versionID == "" {
		return false, ErrInvalid
	}
	return s.Embeddings.EnrichmentEligible(ctx, org, versionID)
}

// VectorBytes encodes a vector as little-endian float32s. Every value must be
// finite; a space's own model checks, such as unit norm, stay with its owner.
func VectorBytes(vector []float32) ([]byte, error) {
	if len(vector) == 0 || len(vector) > MaxDimensions {
		return nil, ErrInvalid
	}
	raw := make([]byte, 4*len(vector))
	for i, x := range vector {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return nil, ErrInvalid
		}
		binary.LittleEndian.PutUint32(raw[4*i:], math.Float32bits(x))
	}
	return raw, nil
}
func ReadVector(raw []byte) ([]float32, error) {
	if len(raw) == 0 || len(raw)%4 != 0 || len(raw) > 4*MaxDimensions {
		return nil, ErrInvalid
	}
	v := make([]float32, len(raw)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
	}
	_, err := VectorBytes(v)
	return v, err
}
func EmbeddingInput(org, corpusID string, v Version, seg Segmentation, p Segment, space VectorSpace, producer string) Embedding {
	return Embedding{DerivationID: StableID("embedding-derivation", org, p.ID, space.ID, p.Derivation.ModelInputSHA256, producer), Organization: org, CorpusID: corpusID, VersionID: v.ID, SegmentID: p.ID, PartKey: p.PartKey, SegmentationID: seg.ID, Recipe: seg.Recipe, SourceSHA: p.Derivation.NormalizedSHA256, SliceSHA: Hash([]byte(p.Text)), InputSHA: p.Derivation.ModelInputSHA256, InputBytes: len(p.Derivation.ModelInput), SpaceID: space.ID, Producer: producer}
}
func (s Service) LoadEmbedding(ctx context.Context, org, derivation string) (Embedding, []float32, error) {
	e, err := s.Embeddings.Embedding(ctx, org, derivation)
	if err != nil {
		return e, nil, err
	}
	manifest, err := s.Blobs.Read(ctx, e.Manifest)
	if err != nil {
		return e, nil, err
	}
	expected := e
	expected.Manifest = Blob{}
	expected.ID = ""
	b := embeddingManifest(expected)
	if string(b) != string(manifest) {
		return e, nil, ErrConflict
	}
	raw, err := s.Blobs.Read(ctx, e.Payload)
	if err != nil {
		return e, nil, err
	}
	if e.ID != Hash(append(append([]byte("quivr/embedding-artifact/v1\x00"), manifest...), raw...)) {
		return e, nil, ErrConflict
	}
	v, err := ReadVector(raw)
	return e, v, err
}
func (s Service) SaveEmbedding(ctx context.Context, e Embedding, space VectorSpace, vector []float32) (Embedding, error) {
	raw, err := VectorBytes(vector)
	if err != nil {
		return e, err
	}
	if e.SpaceID != space.ID || e.DerivationID == "" || e.Producer == "" || (space.Dimensions > 0 && len(vector) != space.Dimensions) {
		return e, ErrInvalid
	}
	e.Payload, err = s.Blobs.Put(ctx, e.Organization, raw)
	if err != nil {
		return e, err
	}
	manifest := embeddingManifest(e)
	e.ID = Hash(append(append([]byte("quivr/embedding-artifact/v1\x00"), manifest...), raw...))
	e.Manifest, err = s.Blobs.Put(ctx, e.Organization, manifest)
	if err != nil {
		return e, err
	}
	return e, s.Embeddings.SaveEmbedding(ctx, e, space)
}

// All manifest values are bounded integers or ASCII identifiers. Sorting object
// keys gives the RFC 8785 representation for this restricted manifest vocabulary.
func embeddingManifest(e Embedding) []byte {
	b, _ := json.Marshal(e)
	var fields map[string]any
	_ = json.Unmarshal(b, &fields)
	b, _ = json.Marshal(fields)
	return b
}

// CheckCoverage checks that vectors cover a segmentation in a generation:
// each segment has exactly one vector in the served space, and at most one in
// each other space the generation carries, and nothing else.
func CheckCoverage(seg Segmentation, g Generation, data []EmbeddingData) error {
	spaces := map[string]bool{}
	for _, id := range g.VectorSpaces() {
		spaces[id] = true
	}
	segments := map[string]bool{}
	for _, p := range seg.Segments {
		segments[p.ID] = true
	}
	seen := map[[2]string]bool{}
	served := map[string]bool{}
	for _, p := range data {
		key := [2]string{p.Artifact.SegmentID, p.Artifact.SpaceID}
		if !segments[key[0]] || !spaces[key[1]] || seen[key] {
			return ErrInvalid
		}
		seen[key] = true
		if key[1] == g.ServedFor(PluginOfRecipe(seg.Recipe)) {
			served[key[0]] = true
		}
	}
	if len(served) != len(segments) {
		return ErrInvalid
	}
	return nil
}

// EmbeddingData keeps a verified float32 vector associated with its durable artifact.
type EmbeddingData struct {
	Artifact Embedding
	Vector   []float32
}

// CodeEnrichmentTimeout is the diagnostic of a Version whose enrichment
// stopped because the ingestion plugin reached its deadline too many times.
const CodeEnrichmentTimeout = "enrichment_timeout"

// CodeRebuildRequired settles enrichment whose pinned recipe cannot serve the
// routed generation. Lexical readiness is retained until compatible coverage
// becomes serving through a rebuild or an independent serving projection.
const CodeRebuildRequired = "rebuild_required"

// ReconcileServingEnrichment lets canonical storage settle an old recipe and
// durably hand off its serving projection before vectors are published. Stores
// without independent serving work retain ordinary publication.
func (s Service) ReconcileServingEnrichment(ctx context.Context, org string, seg Segmentation, g Generation) (bool, error) {
	if store, ok := s.Embeddings.(interface {
		ReconcileServingEnrichment(context.Context, string, Segmentation, Generation) (bool, error)
	}); ok {
		return store.ReconcileServingEnrichment(ctx, org, seg, g)
	}
	return true, nil
}

func (s Service) EnrichmentProgress(ctx context.Context, org, versionID, state, code string) error {
	if org == "" || versionID == "" {
		return ErrInvalid
	}
	if (state != "idle" || code != "") && (state != "running" || code != "") && (state != "retrying" || code != "enrichment_unavailable") && (state != "blocked" || (code != "derivation_conflict" && code != CodeEnrichmentTimeout)) {
		return ErrInvalid
	}
	return s.Embeddings.EnrichmentProgress(ctx, org, versionID, state, code)
}

// CountEnrichmentTimeout records an enrichment call for a Version that ended
// at the plugin's deadline and returns how many have.
func (s Service) CountEnrichmentTimeout(ctx context.Context, org, versionID string) (int, error) {
	if org == "" || versionID == "" {
		return 0, ErrInvalid
	}
	return s.Embeddings.CountEnrichmentTimeout(ctx, org, versionID)
}

// BlockEnrichment stops the enrichment of a searchable Version with a reason
// that names why, such as work pinned to a plugin that left the active plan.
// The Version stays searchable by keyword.
func (s Service) BlockEnrichment(ctx context.Context, org, versionID string, reason Diagnostic) error {
	if org == "" || versionID == "" || reason.Code == "" || reason.Message == "" {
		return ErrInvalid
	}
	return s.Embeddings.BlockEnrichment(ctx, org, versionID, reason)
}

// CommitEnrichment records the vectors attached to a generation: every
// segment has one in the served space, and each other artifact is in one of
// the generation's spaces, at most one per segment and space.
func (s Service) CommitEnrichment(ctx context.Context, org string, seg Segmentation, g Generation, data []EmbeddingData) error {
	if len(data) == 0 || g.SpaceID == "" {
		return ErrInvalid
	}
	if err := CheckCoverage(seg, g, data); err != nil {
		return err
	}
	artifacts := make([]Embedding, len(data))
	for i, p := range data {
		e := p.Artifact
		if e.Organization != org || e.VersionID != seg.VersionID || e.SegmentationID != seg.ID {
			return ErrInvalid
		}
		raw, err := VectorBytes(p.Vector)
		if err != nil || Hash(raw) != e.Payload.SHA256 {
			return ErrInvalid
		}
		artifacts[i] = e
	}
	return s.Embeddings.CommitEnrichment(ctx, org, seg, g, artifacts)
}
