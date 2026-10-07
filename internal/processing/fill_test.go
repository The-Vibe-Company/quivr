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

// firstWriterArtifacts keeps the first artifact stored for a derivation and
// the first manifest registered for a space, as the repository does, and
// reports a later different one as a conflict.
type firstWriterArtifacts struct {
	*memoryArtifacts
	spaces map[string]string
}

func (m firstWriterArtifacts) SaveEmbedding(ctx context.Context, e content.Embedding, space content.VectorSpace) error {
	if old, ok := m.spaces[space.ID]; ok && old != string(space.Manifest) {
		return content.ErrConflict
	}
	m.spaces[space.ID] = string(space.Manifest)
	if old, ok := m.byDerivation[e.DerivationID]; ok && old.ID != e.ID {
		return content.ErrConflict
	}
	return m.memoryArtifacts.SaveEmbedding(ctx, e, space)
}

// An ingestion running beside a rebuild can store a segment's vector first.
// The provider is not bitwise deterministic, so the plugin's answer may
// differ: the stored artifact is adopted instead of refusing the Version.
// A space whose registered manifest changed is still refused.
func TestFillAdoptsAVectorAnotherDerivationStoredFirst(t *testing.T) {
	v := content.Version{RecordID: "r", ID: "v", Manifest: content.Manifest{Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "first paragraph\n\nsecond paragraph"}}}}}
	projected := []content.SegmentInput{{PartKey: "body", Start: 0, End: 15}, {PartKey: "body", Start: 17, End: 33}}
	seg, err := content.PluginSegmentation("org", v, "plugin:p@1", nil, projected)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		stored     []float32
		registered string
		refused    bool
	}{
		{"nearly equal vector", []float32{1, 0.001}, "", false},
		{"space manifest changed", []float32{1, 0.001}, `{"space":"p.large","metric":"dot"}`, true},
	} {
		calls := 0
		plugin := fillPlugin{segments: projected, calls: &calls}
		store := firstWriterArtifacts{&memoryArtifacts{byDerivation: map[string]content.Embedding{}, blobs: map[string][]byte{}}, map[string]string{}}
		service := content.Service{Embeddings: store, Blobs: store}
		space := plugin.Descriptor().VectorSpaces["p.large@1"]
		// The concurrent ingestion stored only the first segment.
		if _, err = service.SaveEmbedding(context.Background(), content.EmbeddingInput("org", "corpus", v, seg, seg.Segments[0], space, "plugin:p@2"), space, tc.stored); err != nil {
			t.Fatal(err)
		}
		if tc.registered != "" {
			store.spaces[space.ID] = tc.registered
		}
		d := processing.PluginDeriver{Content: service, Plugin: plugin}
		data, err := d.Fill(context.Background(), "org", "corpus", v, seg, []string{"p.large@1"})
		if tc.refused {
			if !errors.Is(err, content.ErrIngestionRefused) {
				t.Fatalf("%s: %v, want a refusal", tc.name, err)
			}
			continue
		}
		if err != nil || calls != 1 {
			t.Fatalf("%s: %v after %d plugin calls", tc.name, err, calls)
		}
		if len(data) != 2 || data[0].Vector[1] != 0.001 || data[1].Vector[1] != 1 {
			t.Fatalf("%s: vectors %+v, want the stored first vector and the plugin's second", tc.name, data)
		}
	}
}

func TestFillRefusesChangedPackedSources(t *testing.T) {
	v := content.Version{ID: "v", Manifest: content.Manifest{Parts: []content.Part{
		{Key: "a", Role: "body", Content: content.Text{Kind: "text", Text: "first"}},
		{Key: "b", Role: "body", Content: content.Text{Kind: "text", Text: "second"}},
	}}}
	projected := content.SegmentInput{PartKey: "a", End: 5, SourceRanges: []content.SourceRange{{PartKey: "a", End: 5}, {PartKey: "b", End: 6}}, SourceSeparator: "\n\n"}
	seg, err := content.PluginSegmentation("org", v, "plugin:p@1", nil, []content.SegmentInput{projected})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"later range", "separator"} {
		t.Run(change, func(t *testing.T) {
			answer := projected
			answer.SourceRanges = append([]content.SourceRange(nil), projected.SourceRanges...)
			if change == "later range" {
				answer.SourceRanges[1].End = 5
			} else {
				answer.SourceSeparator = " "
			}
			calls := 0
			store := &memoryArtifacts{byDerivation: map[string]content.Embedding{}, blobs: map[string][]byte{}}
			d := processing.PluginDeriver{Content: content.Service{Embeddings: store, Blobs: store}, Plugin: fillPlugin{segments: []content.SegmentInput{answer}, calls: &calls}}
			if _, err = d.Fill(t.Context(), "org", "corpus", v, seg, []string{"p.large@1"}); !errors.Is(err, processing.ErrSegmentsDiffer) {
				t.Fatalf("changed %s accepted: %v", change, err)
			}
			if len(store.byDerivation) != 0 {
				t.Fatal("stored vector for different packed source")
			}
		})
	}
}
