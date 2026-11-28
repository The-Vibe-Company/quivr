package content

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
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
	// Rows are independent to callers even when decoded together.
	if grown := append(vectors[0], 9); grown[2] != 9 || vectors[2][0] != 3 {
		t.Fatalf("appending to a row changed its neighbour: %v", vectors)
	}
	if _, err := EncodeEmbeddingFile(f, [][]float32{{1, float32(math.NaN())}, nil, {3, 4}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-finite vector encoded: %v", err)
	}
	matrix := len(raw) - 3*2*4
	corrupt := func(offset int, value uint32) []byte {
		b := bytes.Clone(raw)
		binary.LittleEndian.PutUint32(b[matrix+offset:], value)
		return b
	}
	for _, tc := range []struct {
		name  string
		file  EmbeddingFile
		bytes []byte
	}{
		{"truncated", f, raw[:len(raw)-1]},
		{"NaN in a present row", f, corrupt(4, math.Float32bits(float32(math.NaN())))},
		{"infinity in the last row", f, corrupt(20, math.Float32bits(float32(math.Inf(-1))))},
		{"bytes in an absent row", f, corrupt(8, 1)},
		{"wrong owner", EmbeddingFile{Organization: "other", VersionID: "version", SegmentationID: "cuts", SpaceID: "space", Producer: "plugin:p@1", Dimensions: 2, RowCount: 3, Presence: []byte{5}}, raw},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeEmbeddingFile(tc.file, tc.bytes); !errors.Is(err, ErrArtifactCorrupt) {
				t.Fatalf("invalid file accepted: %v", err)
			}
		})
	}
}
