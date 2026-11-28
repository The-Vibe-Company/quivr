package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"math"
	"math/bits"
	"reflect"

	"golang.org/x/sync/semaphore"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

// MaxVectorFileBytes bounds a vector matrix independently from canonical text
// objects. The reader checks the descriptor before allocating its contents.
const MaxVectorFileBytes int64 = 256 << 20

// EmbeddingFile describes the current immutable file of one derivation group.
// Presence distinguishes missing rows from valid zero-valued vectors. Ordinals
// follow the complete durable segmentation, including partially derived groups.
type EmbeddingFile struct {
	ID             int64  `json:"id,omitempty"`
	Organization   string `json:"organization"`
	CorpusID       string `json:"corpus"`
	VersionID      string `json:"version"`
	SegmentationID string `json:"segmentation"`
	Recipe         string `json:"recipe"`
	SpaceID        string `json:"space"`
	Producer       string `json:"producer"`
	Dimensions     int    `json:"dimensions"`
	RowCount       int    `json:"rows"`
	Presence       []byte `json:"presence"`
	Blob           Blob   `json:"blob"`
}

type EmbeddingFileRepository interface {
	EmbeddingGroup(context.Context, string, string, string) (EmbeddingFile, []Embedding, error)
	// expected is the prior descriptor digest; an empty digest means absent.
	// False means another writer won. No external IO occurs in this transaction.
	SaveEmbeddingGroup(context.Context, EmbeddingFile, VectorSpace, []Embedding, string) (bool, error)
}

type vectorFileWriter interface {
	PutVectorFile(context.Context, string, []byte) (Blob, error)
}

type vectorFileReader interface {
	ReadVectorFile(context.Context, Blob) ([]byte, error)
}

func Present(bitmap []byte, ordinal int) bool {
	return ordinal >= 0 && ordinal/8 < len(bitmap) && bitmap[ordinal/8]&(1<<uint(ordinal%8)) != 0
}
func MarkPresent(bitmap []byte, ordinal int) { bitmap[ordinal/8] |= 1 << uint(ordinal%8) }

func fileHeader(f EmbeddingFile) EmbeddingFile { f.ID = 0; f.Blob = Blob{}; return f }
func validFileShape(f EmbeddingFile) bool {
	return f.Organization != "" && f.VersionID != "" && f.SegmentationID != "" && f.SpaceID != "" && f.Producer != "" && f.Dimensions > 0 && f.Dimensions <= MaxDimensions && f.RowCount > 0 && int64(f.RowCount) <= MaxVectorFileBytes/(int64(f.Dimensions)*4) && len(f.Presence) == (f.RowCount+7)/8 && (f.RowCount%8 == 0 || f.Presence[len(f.Presence)-1]>>uint(f.RowCount%8) == 0)
}

// EncodeEmbeddingFile writes a v1 header and contiguous little-endian float32
// rows. Metadata appears once per file, not once per vector.
func EncodeEmbeddingFile(f EmbeddingFile, vectors [][]float32) ([]byte, error) {
	if !validFileShape(f) || len(vectors) != f.RowCount {
		return nil, ErrInvalid
	}
	header, err := json.Marshal(fileHeader(f))
	if err != nil || len(header) > 64<<10 {
		return nil, ErrInvalid
	}
	size := int64(12+len(header)) + int64(f.RowCount)*int64(f.Dimensions)*4
	if size > MaxVectorFileBytes {
		return nil, ErrInvalid
	}
	raw := make([]byte, int(size))
	copy(raw, []byte("QVEC\x00\x00\x00\x01"))
	binary.LittleEndian.PutUint32(raw[8:], uint32(len(header)))
	copy(raw[12:], header)
	offset := 12 + len(header)
	for i, v := range vectors {
		if Present(f.Presence, i) {
			if len(v) != f.Dimensions {
				return nil, ErrInvalid
			}
			// Validate and write in place: no per-row buffer.
			if !putVector(raw[offset:offset+4*f.Dimensions], v) {
				return nil, ErrInvalid
			}
		} else if len(v) != 0 {
			return nil, ErrInvalid
		}
		offset += 4 * f.Dimensions
	}
	return raw, nil
}

func embeddingFileMatrix(f EmbeddingFile, raw []byte) ([]byte, error) {
	matrix, err := embeddingFileHeader(f, raw)
	if err != nil {
		return nil, err
	}
	if !validMatrixRows(f, matrix, nil, nil) {
		return nil, ErrArtifactCorrupt
	}
	return matrix, nil
}

// embeddingFileHeader verifies the file's framing and header and returns its
// unverified row matrix.
func embeddingFileHeader(f EmbeddingFile, raw []byte) ([]byte, error) {
	if !validFileShape(f) || len(raw) < 12 || int64(len(raw)) > MaxVectorFileBytes || !bytes.Equal(raw[:8], []byte("QVEC\x00\x00\x00\x01")) {
		return nil, ErrArtifactCorrupt
	}
	n := int(binary.LittleEndian.Uint32(raw[8:]))
	if n > 64<<10 || n > len(raw)-12 || int64(len(raw)-12-n) != int64(f.RowCount)*int64(f.Dimensions)*4 {
		return nil, ErrArtifactCorrupt
	}
	var header EmbeddingFile
	if json.Unmarshal(raw[12:12+n], &header) != nil || !reflect.DeepEqual(header, fileHeader(f)) {
		return nil, ErrArtifactCorrupt
	}
	return raw[12+n:], nil
}

// validMatrixRows checks that absent rows are zero and present rows finite.
// When decoded is non-nil it receives each present row, sliced in order from
// backing, so verification and decoding share one pass over the matrix.
func validMatrixRows(f EmbeddingFile, matrix []byte, decoded [][]float32, backing []float32) bool {
	width := 4 * f.Dimensions
	zero := make([]byte, width)
	next := 0
	for i := range f.RowCount {
		row := matrix[i*width : (i+1)*width]
		if !Present(f.Presence, i) {
			if !bytes.Equal(row, zero) {
				return false
			}
			continue
		}
		if decoded != nil {
			v := backing[next : next+f.Dimensions : next+f.Dimensions]
			next += f.Dimensions
			if !getVector(v, row) {
				return false
			}
			decoded[i] = v
			continue
		}
		for offset := 0; offset < len(row); offset += 4 {
			v := float64(math.Float32frombits(binary.LittleEndian.Uint32(row[offset:])))
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return false
			}
		}
	}
	return true
}

// DecodeEmbeddingFile verifies a file and returns its rows, nil where absent.
// Present rows share one backing array, each capped at its own row.
func DecodeEmbeddingFile(f EmbeddingFile, raw []byte) ([][]float32, error) {
	matrix, err := embeddingFileHeader(f, raw)
	if err != nil {
		return nil, err
	}
	// validFileShape guarantees no presence bits past RowCount.
	present := 0
	for _, b := range f.Presence {
		present += bits.OnesCount8(b)
	}
	vectors := make([][]float32, f.RowCount)
	if !validMatrixRows(f, matrix, vectors, make([]float32, present*f.Dimensions)) {
		return nil, ErrArtifactCorrupt
	}
	return vectors, nil
}

// Share a byte budget across concurrent reads. Reserve twice the descriptor's
// size for the object buffer and decoding; returned requested vectors belong
// to the caller's already bounded projection batch.
var vectorReadBudget = semaphore.NewWeighted(2 * MaxVectorFileBytes)

func reserveVectorRead(ctx context.Context, f EmbeddingFile) (func(), error) {
	if f.Blob.Size < 0 || f.Blob.Size > MaxVectorFileBytes {
		return nil, ErrArtifactCorrupt
	}
	weight := max(int64(1), 2*f.Blob.Size)
	if err := vectorReadBudget.Acquire(ctx, weight); err != nil {
		return nil, err
	}
	return func() { vectorReadBudget.Release(weight) }, nil
}

func (s Service) readVectorFile(ctx context.Context, f EmbeddingFile) ([][]float32, error) {
	release, err := reserveVectorRead(ctx, f)
	if err != nil {
		return nil, err
	}
	defer release()
	raw, err := s.readVectorFileBytes(ctx, f)
	if err != nil {
		return nil, err
	}
	return DecodeEmbeddingFile(f, raw)
}

func (s Service) readVectorFileBytes(ctx context.Context, f EmbeddingFile) ([]byte, error) {
	if f.Blob.Size < 0 || f.Blob.Size > MaxVectorFileBytes {
		return nil, ErrArtifactCorrupt
	}
	var raw []byte
	var err error
	if reader, ok := s.Blobs.(vectorFileReader); ok {
		raw, err = reader.ReadVectorFile(ctx, f.Blob)
	} else {
		raw, err = s.Blobs.Read(ctx, f.Blob)
	}
	if err != nil {
		return nil, err
	}
	// Both readers return bytes already verified against the Blob's SHA-256
	// (see Blobs.Read); hashing a 256 MiB matrix again is pure overhead.
	if int64(len(raw)) != f.Blob.Size {
		return nil, ErrArtifactCorrupt
	}
	return raw, nil
}

// LoadEmbeddingData reads each referenced file once and verifies each vector's
// content and original public artifact identity.
func (s Service) LoadEmbeddingData(ctx context.Context, artifacts []Embedding) ([]EmbeddingData, error) {
	type fileGroup struct {
		descriptor EmbeddingFile
		indices    []int
	}
	files := map[string]*fileGroup{}
	result := make([]EmbeddingData, len(artifacts))
	for i, e := range artifacts {
		if e.File == nil {
			return nil, ErrArtifactCorrupt
		}
		f := *e.File
		group, ok := files[f.Blob.Key]
		if !ok {
			group = &fileGroup{descriptor: f}
			files[f.Blob.Key] = group
		} else if group.descriptor.Blob != f.Blob || !reflect.DeepEqual(fileHeader(group.descriptor), fileHeader(f)) {
			return nil, ErrArtifactCorrupt
		}
		group.indices = append(group.indices, i)
	}
	// Decode only requested rows; do not retain whole decoded matrices across
	// all documents of a rebuild or subscription batch.
	var ids artifactHasher
	for _, group := range files {
		if err := func() error {
			f := group.descriptor
			release, err := reserveVectorRead(ctx, f)
			if err != nil {
				return err
			}
			defer release()
			raw, err := s.readVectorFileBytes(ctx, f)
			if err != nil {
				return err
			}
			matrix, err := embeddingFileMatrix(f, raw)
			if err != nil {
				return err
			}
			for _, i := range group.indices {
				e := artifacts[i]
				if e.Ordinal < 0 || e.Ordinal >= f.RowCount || !Present(f.Presence, e.Ordinal) || e.Organization != f.Organization || e.VersionID != f.VersionID || e.SegmentationID != f.SegmentationID || e.SpaceID != f.SpaceID || e.Producer != f.Producer {
					return ErrArtifactCorrupt
				}
				row := matrix[e.Ordinal*4*f.Dimensions : (e.Ordinal+1)*4*f.Dimensions]
				vector, err := ReadVector(row)
				expected := e
				expected.ID = ""
				expected.Manifest = Blob{}
				manifest := embeddingManifest(expected)
				if err != nil || Hash(row) != e.Payload.SHA256 || e.ID != ids.id(manifest, row) {
					return ErrArtifactCorrupt
				}
				result[i] = EmbeddingData{e, vector}
			}
			return nil
		}(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// LoadEmbeddingGroup recovers the requested space in one file read. Inputs
// describe the pinned producer and complete segmentation, not arbitrary hashes.
func (s Service) LoadEmbeddingGroup(ctx context.Context, inputs []Embedding) ([]EmbeddingData, bool, error) {
	if len(inputs) == 0 {
		return nil, false, ErrInvalid
	}
	_, artifacts, err := s.Embeddings.EmbeddingGroup(ctx, inputs[0].Organization, inputs[0].SegmentationID, inputs[0].SpaceID)
	if err == nil {
		bySegment := map[string]Embedding{}
		for _, e := range artifacts {
			bySegment[e.SegmentID] = e
		}
		ordered := make([]Embedding, 0, len(inputs))
		for _, input := range inputs {
			e, ok := bySegment[input.SegmentID]
			if !ok {
				return nil, false, nil
			}
			if e.DerivationID != input.DerivationID {
				return nil, false, ErrConflict
			}
			ordered = append(ordered, e)
		}
		data, err := s.LoadEmbeddingData(ctx, ordered)
		return data, err == nil, err
	}
	if !errors.Is(err, corpus.ErrNotFound) {
		return nil, false, err
	}
	return nil, false, nil
}

// artifactHasher computes embedding artifact IDs, reusing one SHA-256 state
// and digest buffer instead of concatenating prefix, manifest and row.
type artifactHasher struct {
	h   hash.Hash
	sum [sha256.Size]byte
}

const artifactIDPrefix = "quivr/embedding-artifact/v1\x00"

// id is Hash(prefix + manifest + row).
func (a *artifactHasher) id(manifest, row []byte) string {
	if a.h == nil {
		a.h = sha256.New()
	}
	a.h.Reset()
	_, _ = io.WriteString(a.h, artifactIDPrefix)
	_, _ = a.h.Write(manifest)
	_, _ = a.h.Write(row)
	return hex.EncodeToString(a.h.Sum(a.sum[:0]))
}

func virtualBlob(org string, raw []byte) Blob {
	sha := Hash(raw)
	return Blob{Key: Hash([]byte(org)) + "/sha256/" + sha, SHA256: sha, Size: int64(len(raw))}
}

// SaveEmbeddingGroup stores one matrix for a space, adopting canonical partial
// output when another writer has already stored it.
func (s Service) SaveEmbeddingGroup(ctx context.Context, seg Segmentation, space VectorSpace, data []EmbeddingData) ([]EmbeddingData, error) {
	repo := s.Embeddings
	if len(data) == 0 || len(seg.Segments) == 0 {
		return nil, ErrInvalid
	}
	first := data[0].Artifact
	var ids artifactHasher
	for attempt := 0; attempt < 8; attempt++ {
		old, stored, err := repo.EmbeddingGroup(ctx, first.Organization, seg.ID, space.ID)
		if err != nil && !errors.Is(err, corpus.ErrNotFound) {
			return nil, err
		}
		prior := ""
		bySegment := map[string]EmbeddingData{}
		if err == nil {
			if old.Producer != first.Producer {
				return nil, ErrConflict
			}
			prior = old.Blob.SHA256
			loaded, err := s.LoadEmbeddingData(ctx, stored)
			if err != nil {
				return nil, err
			}
			for _, d := range loaded {
				bySegment[d.Artifact.SegmentID] = d
			}
		}
		for _, d := range data {
			e := d.Artifact
			if e.Organization != first.Organization || e.VersionID != seg.VersionID || e.SegmentationID != seg.ID || e.SpaceID != space.ID || e.Producer != first.Producer || e.CorpusID != first.CorpusID || e.Recipe != seg.Recipe {
				return nil, ErrInvalid
			}
			if _, ok := bySegment[e.SegmentID]; ok {
				continue
			}
			raw, err := VectorBytes(d.Vector)
			if err != nil || space.Dimensions > 0 && len(d.Vector) != space.Dimensions {
				return nil, ErrInvalid
			}
			e.Payload = virtualBlob(e.Organization, raw)
			e.ID = ""
			e.Manifest = Blob{}
			manifest := embeddingManifest(e)
			e.ID = ids.id(manifest, raw)
			e.Manifest = virtualBlob(e.Organization, manifest)
			bySegment[e.SegmentID] = EmbeddingData{e, d.Vector}
		}
		dimensions := len(data[0].Vector)
		f := EmbeddingFile{Organization: first.Organization, CorpusID: first.CorpusID, VersionID: seg.VersionID, SegmentationID: seg.ID, Recipe: seg.Recipe, SpaceID: space.ID, Producer: first.Producer, Dimensions: dimensions, RowCount: len(seg.Segments), Presence: make([]byte, (len(seg.Segments)+7)/8)}
		vectors := make([][]float32, len(seg.Segments))
		artifacts := make([]Embedding, 0, len(bySegment))
		for i, p := range seg.Segments {
			if d, ok := bySegment[p.ID]; ok {
				if len(d.Vector) != dimensions {
					return nil, ErrConflict
				}
				MarkPresent(f.Presence, i)
				vectors[i] = d.Vector
				d.Artifact.Ordinal = i
				artifacts = append(artifacts, d.Artifact)
			}
		}
		if len(artifacts) != len(bySegment) {
			return nil, ErrInvalid
		}
		raw, err := EncodeEmbeddingFile(f, vectors)
		if err != nil {
			return nil, err
		}
		if writer, ok := s.Blobs.(vectorFileWriter); ok {
			f.Blob, err = writer.PutVectorFile(ctx, f.Organization, raw)
		} else {
			f.Blob, err = s.Blobs.Put(ctx, f.Organization, raw)
		}
		if err != nil {
			return nil, err
		}
		saved, err := repo.SaveEmbeddingGroup(ctx, f, space, artifacts, prior)
		if err != nil {
			return nil, err
		}
		if !saved {
			continue
		}
		// Read back to include the durable logical file ID used for coverage.
		_, artifacts, err = repo.EmbeddingGroup(ctx, f.Organization, seg.ID, space.ID)
		if err != nil {
			return nil, err
		}
		return s.LoadEmbeddingData(ctx, artifacts)
	}
	return nil, ErrConflict
}
