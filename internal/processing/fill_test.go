package processing_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/processing"
)

// fillPlugin is a later version of the plugin that segmented the Version:
// it answers the given segments with a vector in each requested space.
type fillPlugin struct {
	segments []content.SegmentInput
	calls    *int
}

func (fillPlugin) Descriptor() processing.IngestionDescriptor {
	return processing.IngestionDescriptor{Recipe: "plugin:p@2", Producer: "plugin:p@2", Spaces: []string{"p.large@1"}, SegmentsOnly: true,
		VectorSpaces: map[string]content.VectorSpace{"p.large@1": {ID: "p.large@1", Manifest: json.RawMessage(`{"space":"p.large"}`), Dimensions: 2}}}
}
func (p fillPlugin) SegmentAndEmbed(_ context.Context, _, _ string, _ content.Version, spaces []string) ([]processing.PluginSegment, error) {
	*p.calls++
	out := make([]processing.PluginSegment, len(p.segments))
	for i, s := range p.segments {
		out[i] = processing.PluginSegment{SegmentInput: s, Vectors: map[string][]float32{spaces[0]: {1, float32(i)}}}
	}
	return out, nil
}

// memoryArtifacts keeps Embedding Artifacts and their bytes in memory.
type memoryArtifacts struct {
	content.EmbeddingRepository
	byDerivation map[string]content.Embedding
	blobs        map[string][]byte
}

func (m *memoryArtifacts) Embedding(_ context.Context, _, derivation string) (content.Embedding, error) {
	if e, ok := m.byDerivation[derivation]; ok {
		return e, nil
	}
	return content.Embedding{}, corpus.ErrNotFound
}
func (m *memoryArtifacts) SaveEmbedding(_ context.Context, e content.Embedding, _ content.VectorSpace) error {
	m.byDerivation[e.DerivationID] = e
	return nil
}
func (m *memoryArtifacts) Put(_ context.Context, _ string, b []byte) (content.Blob, error) {
	sha := content.Hash(b)
	m.blobs[sha] = b
	return content.Blob{Key: sha, SHA256: sha, Size: int64(len(b))}, nil
}
func (m *memoryArtifacts) Read(_ context.Context, b content.Blob) ([]byte, error) {
	return m.blobs[b.SHA256], nil
}

// A backfill fills the segments a generation projects, even when an earlier
// version of the plugin made them: the plugin's segments must have their
// Parts and offsets, and a second fill reuses the stored vectors.
func TestFillKeepsTheProjectedSegments(t *testing.T) {
	v := content.Version{RecordID: "r", ID: "v", Manifest: content.Manifest{Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "first paragraph\n\nsecond paragraph"}}}}}
	projected := []content.SegmentInput{{PartKey: "body", Start: 0, End: 15}, {PartKey: "body", Start: 17, End: 33}}
	seg, err := content.PluginSegmentation("org", v, "plugin:p@1", nil, projected)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		segments []content.SegmentInput
		want     error
	}{
		{"other offsets", []content.SegmentInput{{PartKey: "body", Start: 0, End: 33}}, processing.ErrSegmentsDiffer},
		{"one segment moved", []content.SegmentInput{projected[0], {PartKey: "body", Start: 16, End: 33}}, processing.ErrSegmentsDiffer},
		{"the same segments", projected, nil},
	} {
		calls := 0
		store := &memoryArtifacts{byDerivation: map[string]content.Embedding{}, blobs: map[string][]byte{}}
		d := processing.PluginDeriver{Content: content.Service{Embeddings: store, Blobs: store}, Plugin: fillPlugin{segments: tc.segments, calls: &calls}}
		data, err := d.Fill(context.Background(), "org", "corpus", v, seg, []string{"p.large@1"})
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v, want %v", tc.name, err, tc.want)
		}
		if tc.want != nil {
			continue
		}
		if len(data) != 2 || data[0].Artifact.SegmentID != seg.Segments[0].ID || data[1].Artifact.SegmentID != seg.Segments[1].ID || data[1].Artifact.Producer != "plugin:p@2" {
			t.Fatalf("%s: vectors %+v", tc.name, data)
		}
		if _, err = d.Fill(context.Background(), "org", "corpus", v, seg, []string{"p.large@1"}); err != nil || calls != 1 {
			t.Fatalf("%s: second fill called the plugin %d times: %v", tc.name, calls, err)
		}
	}
}
