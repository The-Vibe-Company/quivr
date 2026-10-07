package content

import (
	"errors"
	"testing"
)

func TestVectorFileIntegrityAndSparseRows(t *testing.T) {
	f := EmbeddingFile{Organization: "org", VersionID: "version", SegmentationID: "cuts", SpaceID: "space", Producer: "plugin:p@1", Dimensions: 2, RowCount: 3, Presence: []byte{5}}
	raw, err := EncodeEmbeddingFile(f, [][]float32{{1, -2}, nil, {3, 4}})
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := DecodeEmbeddingFile(f, raw)
	if err != nil || len(vectors) != 3 || vectors[0][1] != -2 || vectors[1] != nil || vectors[2][0] != 3 {
		t.Fatalf("sparse vector rows: %v %v", vectors, err)
	}
	for _, tc := range []struct {
		name  string
		file  EmbeddingFile
		bytes []byte
	}{
		{"truncated", f, raw[:len(raw)-1]},
		{"wrong owner", EmbeddingFile{Organization: "other", VersionID: "version", SegmentationID: "cuts", SpaceID: "space", Producer: "plugin:p@1", Dimensions: 2, RowCount: 3, Presence: []byte{5}}, raw},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeEmbeddingFile(tc.file, tc.bytes); !errors.Is(err, ErrArtifactCorrupt) {
				t.Fatalf("invalid file accepted: %v", err)
			}
		})
	}
}
