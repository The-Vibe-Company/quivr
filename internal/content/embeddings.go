package content

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
)

type VectorSpace struct {
	ID       string
	Manifest json.RawMessage
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
	CommitEnrichment(context.Context, string, Segmentation, Generation, []Embedding) error
}

func VectorBytes(vector []float32) ([]byte, error) {
	if len(vector) != 384 {
		return nil, ErrInvalid
	}
	norm := 0.0
	raw := make([]byte, 4*len(vector))
	for i, x := range vector {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return nil, ErrInvalid
		}
		norm += float64(x) * float64(x)
		binary.LittleEndian.PutUint32(raw[4*i:], math.Float32bits(x))
	}
	if math.Abs(math.Sqrt(norm)-1) > .001 {
		return nil, ErrInvalid
	}
	return raw, nil
}
func ReadVector(raw []byte) ([]float32, error) {
	if len(raw) != 1536 {
		return nil, ErrInvalid
	}
	v := make([]float32, 384)
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
	if e.SpaceID != space.ID || e.DerivationID == "" || e.Producer == "" {
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

// EmbeddingData keeps a verified float32 vector associated with its durable artifact.
type EmbeddingData struct {
	Artifact Embedding
	Vector   []float32
}

func (s Service) EnrichmentProgress(ctx context.Context, org, versionID, state, code string) error {
	if org == "" || versionID == "" {
		return ErrInvalid
	}
	if (state != "running" || code != "") && (state != "retrying" || code != "enrichment_unavailable") && (state != "blocked" || code != "derivation_conflict") {
		return ErrInvalid
	}
	return s.Embeddings.EnrichmentProgress(ctx, org, versionID, state, code)
}
func (s Service) CommitEnrichment(ctx context.Context, org string, seg Segmentation, g Generation, data []EmbeddingData) error {
	if len(data) == 0 || len(data) != len(seg.Segments) || g.SpaceID == "" {
		return ErrInvalid
	}
	artifacts := make([]Embedding, len(data))
	for i, p := range data {
		e := p.Artifact
		if e.Organization != org || e.VersionID != seg.VersionID || e.SegmentationID != seg.ID || e.SegmentID != seg.Segments[i].ID || e.SpaceID != g.SpaceID {
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
