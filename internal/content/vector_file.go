package content

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"reflect"

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
	CompactStorage(context.Context) (bool, error)
	// False means compact mode activated during immutable-object preparation.
	SaveLegacyEmbeddingGroup(context.Context, []Embedding, VectorSpace) (bool, error)
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
			row, err := VectorBytes(v)
			if err != nil {
				return nil, err
			}
			copy(raw[offset:], row)
		} else if len(v) != 0 {
			return nil, ErrInvalid
		}
		offset += 4 * f.Dimensions
	}
	return raw, nil
}

func DecodeEmbeddingFile(f EmbeddingFile, raw []byte) ([][]float32, error) {
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
	vectors := make([][]float32, f.RowCount)
	zero := make([]byte, 4*f.Dimensions)
	offset := 12 + n
	for i := range vectors {
		row := raw[offset : offset+4*f.Dimensions]
		offset += 4 * f.Dimensions
		if !Present(f.Presence, i) {
			if !bytes.Equal(row, zero) {
				return nil, ErrArtifactCorrupt
			}
			continue
		}
		var err error
		vectors[i], err = ReadVector(row)
		if err != nil {
			return nil, ErrArtifactCorrupt
		}
	}
	return vectors, nil
}

func (s Service) readVectorFile(ctx context.Context, f EmbeddingFile) ([][]float32, error) {
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
	if int64(len(raw)) != f.Blob.Size || Hash(raw) != f.Blob.SHA256 {
		return nil, ErrArtifactCorrupt
	}
	return DecodeEmbeddingFile(f, raw)
}

// LoadEmbeddingData reads each referenced file once and verifies each vector's
// content and original public artifact identity. Legacy artifacts remain readable.
func (s Service) LoadEmbeddingData(ctx context.Context, artifacts []Embedding) ([]EmbeddingData, error) {
	type loadedFile struct {
		descriptor EmbeddingFile
		vectors    [][]float32
	}
	files := map[string]loadedFile{}
	result := make([]EmbeddingData, 0, len(artifacts))
	for _, e := range artifacts {
		if e.File == nil {
			a, v, err := s.loadEmbeddingArtifact(ctx, e)
			if err != nil {
				return nil, err
			}
			result = append(result, EmbeddingData{a, v})
			continue
		}
		f := *e.File
		cached, ok := files[f.Blob.Key]
		vectors := cached.vectors
		if !ok {
			var err error
			vectors, err = s.readVectorFile(ctx, f)
			if err != nil {
				return nil, err
			}
			files[f.Blob.Key] = loadedFile{f, vectors}
		} else if cached.descriptor.Blob != f.Blob || !reflect.DeepEqual(fileHeader(cached.descriptor), fileHeader(f)) {
			return nil, ErrArtifactCorrupt
		}
		if e.Ordinal < 0 || e.Ordinal >= len(vectors) || !Present(f.Presence, e.Ordinal) || e.Organization != f.Organization || e.VersionID != f.VersionID || e.SegmentationID != f.SegmentationID || e.SpaceID != f.SpaceID || e.Producer != f.Producer {
			return nil, ErrArtifactCorrupt
		}
		vector := vectors[e.Ordinal]
		raw, err := VectorBytes(vector)
		expected := e
		expected.ID = ""
		expected.Manifest = Blob{}
		manifest := embeddingManifest(expected)
		if err != nil || Hash(raw) != e.Payload.SHA256 || e.ID != Hash(append(append([]byte("quivr/embedding-artifact/v1\x00"), manifest...), raw...)) {
			return nil, ErrArtifactCorrupt
		}
		result = append(result, EmbeddingData{e, vector})
	}
	return result, nil
}

// LoadEmbeddingGroup recovers the requested space in one file read. Inputs
// describe the pinned producer and complete segmentation, not arbitrary hashes.
func (s Service) LoadEmbeddingGroup(ctx context.Context, inputs []Embedding) ([]EmbeddingData, bool, error) {
	if len(inputs) == 0 {
		return nil, false, ErrInvalid
	}
	if repo, ok := s.Embeddings.(EmbeddingFileRepository); ok {
		_, artifacts, err := repo.EmbeddingGroup(ctx, inputs[0].Organization, inputs[0].SegmentationID, inputs[0].SpaceID)
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
	}
	data := make([]EmbeddingData, 0, len(inputs))
	for _, input := range inputs {
		e, v, err := s.loadLegacyEmbedding(ctx, input.Organization, input.DerivationID)
		if errors.Is(err, corpus.ErrNotFound) || errors.Is(err, ErrArtifactMissing) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		data = append(data, EmbeddingData{e, v})
	}
	return data, true, nil
}

func virtualBlob(org string, raw []byte) Blob {
	sha := Hash(raw)
	return Blob{Key: Hash([]byte(org)) + "/sha256/" + sha, SHA256: sha, Size: int64(len(raw))}
}

// SaveEmbeddingGroup stores/adopts one matrix for a space. Existing canonical
// rows always win, including incomplete output left by a legacy writer.
func (s Service) SaveEmbeddingGroup(ctx context.Context, seg Segmentation, space VectorSpace, data []EmbeddingData) ([]EmbeddingData, error) {
	repo, ok := s.Embeddings.(EmbeddingFileRepository)
	compact := false
	if ok {
		var err error
		compact, err = repo.CompactStorage(ctx)
		if err != nil {
			return nil, err
		}
	}
	if !compact && ok {
		return s.saveLegacyEmbeddingGroup(ctx, repo, seg, space, data)
	}
	if !compact {
		out := make([]EmbeddingData, 0, len(data))
		for _, d := range data {
			e, err := s.SaveEmbedding(ctx, d.Artifact, space, d.Vector)
			vector := d.Vector
			if errors.Is(err, ErrConflict) {
				_, stored, loadErr := s.LoadEmbedding(ctx, d.Artifact.Organization, d.Artifact.DerivationID)
				if loadErr == nil && len(stored) == len(vector) {
					vector = stored
					e, err = s.SaveEmbedding(ctx, d.Artifact, space, stored)
				}
			}
			if err != nil {
				return nil, err
			}
			out = append(out, EmbeddingData{e, vector})
		}
		return out, nil
	}
	return s.packEmbeddingGroup(ctx, repo, seg, space, data)
}

// PackEmbeddingGroup converts legacy artifacts without activating compact-only
// writes. The caller supplies verified canonical vectors; it never embeds.
func (s Service) PackEmbeddingGroup(ctx context.Context, seg Segmentation, space VectorSpace, data []EmbeddingData) ([]EmbeddingData, error) {
	repo, ok := s.Embeddings.(EmbeddingFileRepository)
	if !ok {
		return nil, ErrInvalid
	}
	return s.packEmbeddingGroup(ctx, repo, seg, space, data)
}

func (s Service) packEmbeddingGroup(ctx context.Context, repo EmbeddingFileRepository, seg Segmentation, space VectorSpace, data []EmbeddingData) ([]EmbeddingData, error) {
	if len(data) == 0 || len(seg.Segments) == 0 {
		return nil, ErrInvalid
	}
	first := data[0].Artifact
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
			// The legacy repository's unique derivation key preserves partial winners.
			a, v, err := s.loadLegacyEmbedding(ctx, e.Organization, e.DerivationID)
			if err == nil {
				if len(v) != len(d.Vector) {
					return nil, ErrConflict
				}
				bySegment[e.SegmentID] = EmbeddingData{a, v}
				continue
			}
			if !errors.Is(err, corpus.ErrNotFound) {
				return nil, err
			}
			raw, err := VectorBytes(d.Vector)
			if err != nil || space.Dimensions > 0 && len(d.Vector) != space.Dimensions {
				return nil, ErrInvalid
			}
			e.Payload = virtualBlob(e.Organization, raw)
			e.ID = ""
			e.Manifest = Blob{}
			manifest := embeddingManifest(e)
			e.ID = Hash(append(append([]byte("quivr/embedding-artifact/v1\x00"), manifest...), raw...))
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

// Compatibility writes arbitrate the entire group under one routing fence.
// Immutable objects are prepared first; activation retries with the matrix path.
func (s Service) saveLegacyEmbeddingGroup(ctx context.Context, repo EmbeddingFileRepository, seg Segmentation, space VectorSpace, data []EmbeddingData) ([]EmbeddingData, error) {
	for attempt := 0; attempt < 8; attempt++ {
		prepared := make([]EmbeddingData, 0, len(data))
		artifacts := make([]Embedding, 0, len(data))
		for _, d := range data {
			e, v, err := s.loadLegacyEmbedding(ctx, d.Artifact.Organization, d.Artifact.DerivationID)
			if err == nil {
				if len(v) != len(d.Vector) || (space.Dimensions > 0 && len(v) != space.Dimensions) {
					return nil, ErrConflict
				}
				d = EmbeddingData{e, v}
			} else if !errors.Is(err, corpus.ErrNotFound) {
				return nil, err
			}
			if err != nil {
				e, err = s.prepareEmbedding(ctx, d.Artifact, space, d.Vector)
				if err != nil {
					return nil, err
				}
				d.Artifact = e
			}
			prepared = append(prepared, d)
			artifacts = append(artifacts, d.Artifact)
		}
		saved, err := repo.SaveLegacyEmbeddingGroup(ctx, artifacts, space)
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !saved {
			return s.packEmbeddingGroup(ctx, repo, seg, space, data)
		}
		return prepared, nil
	}
	return nil, ErrConflict
}
