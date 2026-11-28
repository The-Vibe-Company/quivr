package content

import (
	"context"
	"strconv"
	"testing"
)

type preparedVectorFile struct {
	Blobs
	raw []byte
}

func (p preparedVectorFile) ReadVectorFile(context.Context, Blob) ([]byte, error) { return p.raw, nil }

func benchVectorFile(dimensions, rows int) (EmbeddingFile, [][]float32) {
	f := EmbeddingFile{Organization: "org", VersionID: "version", SegmentationID: "cuts", SpaceID: "space", Producer: "plugin:p@1", Dimensions: dimensions, RowCount: rows, Presence: make([]byte, (rows+7)/8)}
	vectors := make([][]float32, rows)
	backing := make([]float32, rows*dimensions)
	for i := range vectors {
		if i%7 == 3 {
			continue // sparse rows stay absent
		}
		MarkPresent(f.Presence, i)
		vectors[i] = backing[i*dimensions : (i+1)*dimensions : (i+1)*dimensions]
		for j := range vectors[i] {
			vectors[i][j] = float32(i%97)*0.25 - float32(j%13)
		}
	}
	return f, vectors
}

func BenchmarkVectorFileRoundTrip(b *testing.B) {
	for _, dimensions := range []int{1024, 4096} {
		f, vectors := benchVectorFile(dimensions, 10_000)
		b.Run("dim="+strconv.Itoa(dimensions), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				raw, err := EncodeEmbeddingFile(f, vectors)
				if err != nil {
					b.Fatal(err)
				}
				decoded, err := DecodeEmbeddingFile(f, raw)
				if err != nil || len(decoded) != len(vectors) {
					b.Fatal(err)
				}
			}
		})
	}
	// The largest admissible file: the read path's allocations relative to
	// the file size it already holds in memory.
	const dimensions = 4096
	rows := int((MaxVectorFileBytes - 1024) / (4 * dimensions))
	f, vectors := benchVectorFile(dimensions, rows)
	raw, err := EncodeEmbeddingFile(f, vectors)
	if err != nil {
		b.Fatal(err)
	}
	f.Blob = virtualBlob(f.Organization, raw)
	svc := Service{Blobs: preparedVectorFile{raw: raw}}
	b.Run("read=256MiB", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(raw)))
		for range b.N {
			decoded, err := svc.readVectorFile(context.Background(), f)
			if err != nil || len(decoded) != rows {
				b.Fatal(err)
			}
		}
	})
}
