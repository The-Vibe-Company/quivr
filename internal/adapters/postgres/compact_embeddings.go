package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5"
)

func (s EmbeddingStore) CompactStorage(ctx context.Context) (bool, error) {
	var compact bool
	err := s.Pool.QueryRow(ctx, `SELECT coalesce((SELECT compact FROM storage_state WHERE singleton),false)`).Scan(&compact)
	return compact, err
}

func (s EmbeddingStore) EmbeddingGroup(ctx context.Context, org, segmentation, space string) (content.EmbeddingFile, []content.Embedding, error) {
	var f content.EmbeddingFile
	var sha []byte
	err := s.Pool.QueryRow(ctx, `SELECT f.id,o.organization,f.corpus_id,f.version_id,f.segmentation_id,f.recipe,sp.space_id,f.producer,f.object_key,f.sha256,f.byte_length,f.dimensions,f.row_count,f.presence
 FROM storage_organizations o JOIN embedding_files f ON f.organization_id=o.id JOIN storage_spaces sp ON sp.id=f.space_id
 WHERE o.organization=$1 AND f.segmentation_id=$2 AND sp.space_id=$3`, org, segmentation, space).Scan(&f.ID, &f.Organization, &f.CorpusID, &f.VersionID, &f.SegmentationID, &f.Recipe, &f.SpaceID, &f.Producer, &f.Blob.Key, &sha, &f.Blob.Size, &f.Dimensions, &f.RowCount, &f.Presence)
	if err != nil {
		return f, nil, notFound(err)
	}
	f.Blob.SHA256 = hex.EncodeToString(sha)
	rows, err := s.Pool.Query(ctx, `SELECT k.segment_id,e.ordinal,e.vector_sha256,e.artifact_sha256,sg.part_key,sg.text_sha256,sg.derivation
 FROM compact_embeddings e JOIN storage_segments k ON (k.organization_id,k.id)=(e.organization_id,e.segment_id)
 JOIN segments sg ON sg.organization=$2 AND sg.id=k.segment_id WHERE e.file_id=$1 ORDER BY e.ordinal`, f.ID, org)
	if err != nil {
		return f, nil, err
	}
	defer rows.Close()
	var artifacts []content.Embedding
	for rows.Next() {
		var e content.Embedding
		var vectorSHA, artifactSHA, derivation []byte
		if err = rows.Scan(&e.SegmentID, &e.Ordinal, &vectorSHA, &artifactSHA, &e.PartKey, &e.SliceSHA, &derivation); err != nil {
			return f, nil, err
		}
		var d content.SegmentDerivation
		if err = json.Unmarshal(derivation, &d); err != nil {
			return f, nil, err
		}
		e.ID = hex.EncodeToString(artifactSHA)
		e.Organization = org
		e.CorpusID = f.CorpusID
		e.VersionID = f.VersionID
		e.SegmentationID = f.SegmentationID
		e.Recipe = f.Recipe
		e.SpaceID = f.SpaceID
		e.Producer = f.Producer
		e.SourceSHA = d.NormalizedSHA256
		e.InputSHA = d.ModelInputSHA256
		e.InputBytes = len(d.ModelInput)
		e.DerivationID = content.StableID("embedding-derivation", org, e.SegmentID, space, e.InputSHA, e.Producer)
		e.Payload = content.Blob{SHA256: hex.EncodeToString(vectorSHA), Size: int64(4 * f.Dimensions)}
		e.Payload.Key = content.Hash([]byte(org)) + "/sha256/" + e.Payload.SHA256
		e.File = &f
		artifacts = append(artifacts, e)
	}
	return f, artifacts, rows.Err()
}

func (s EmbeddingStore) SaveEmbeddingGroup(ctx context.Context, f content.EmbeddingFile, space content.VectorSpace, artifacts []content.Embedding, expected string) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if err = lockProcessingVersion(ctx, tx, f.Organization, f.VersionID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO vector_spaces(id,manifest) VALUES($1,$2) ON CONFLICT DO NOTHING`, space.ID, space.Manifest); err != nil {
		return false, err
	}
	var same bool
	if err = tx.QueryRow(ctx, `SELECT manifest=$2::jsonb FROM vector_spaces WHERE id=$1`, space.ID, space.Manifest).Scan(&same); err != nil {
		return false, err
	}
	if !same {
		return false, content.ErrConflict
	}
	// Intern shared keys once. These sequential indexes replace hash indexes on
	// each artifact; the hot tuple itself has only numeric keys and two digests.
	if _, err = tx.Exec(ctx, `INSERT INTO storage_organizations(organization) VALUES($1) ON CONFLICT DO NOTHING`, f.Organization); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO storage_spaces(space_id) VALUES($1) ON CONFLICT DO NOTHING`, f.SpaceID); err != nil {
		return false, err
	}
	var organizationID int64
	var spaceID int32
	if err = tx.QueryRow(ctx, `SELECT o.id,s.id FROM storage_organizations o CROSS JOIN storage_spaces s WHERE o.organization=$1 AND s.space_id=$2`, f.Organization, f.SpaceID).Scan(&organizationID, &spaceID); err != nil {
		return false, err
	}
	var previous string
	err = tx.QueryRow(ctx, `SELECT encode(sha256,'hex') FROM embedding_files WHERE organization_id=$1 AND segmentation_id=$2 AND space_id=$3`, organizationID, f.SegmentationID, spaceID).Scan(&previous)
	if errors.Is(err, pgx.ErrNoRows) {
		previous = ""
	} else if err != nil {
		return false, err
	}
	if previous != expected {
		return false, nil
	}
	// Recheck partial legacy winners under the same version fence used by their
	// writer, after the candidate object's upload has completed.
	for _, e := range artifacts {
		var id string
		err = tx.QueryRow(ctx, `SELECT id FROM embedding_artifacts WHERE organization=$1 AND derivation_id=$2`, f.Organization, e.DerivationID).Scan(&id)
		if err == nil && id != e.ID {
			return false, nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
	}
	sha, err := hex.DecodeString(f.Blob.SHA256)
	if err != nil || len(sha) != 32 {
		return false, content.ErrInvalid
	}
	var fileID int64
	err = tx.QueryRow(ctx, `INSERT INTO embedding_files(organization_id,version_id,segmentation_id,space_id,corpus_id,recipe,producer,object_key,sha256,byte_length,dimensions,row_count,presence)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
 ON CONFLICT(organization_id,segmentation_id,space_id) DO UPDATE SET object_key=EXCLUDED.object_key,sha256=EXCLUDED.sha256,byte_length=EXCLUDED.byte_length,presence=EXCLUDED.presence
 WHERE embedding_files.producer=EXCLUDED.producer AND embedding_files.dimensions=EXCLUDED.dimensions AND embedding_files.row_count=EXCLUDED.row_count
 RETURNING id`, organizationID, f.VersionID, f.SegmentationID, spaceID, f.CorpusID, f.Recipe, f.Producer, f.Blob.Key, sha, f.Blob.Size, f.Dimensions, f.RowCount, f.Presence).Scan(&fileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, content.ErrConflict
	}
	if err != nil {
		return false, err
	}
	writes := &pgx.Batch{}
	for _, e := range artifacts {
		vsha, err := hex.DecodeString(e.Payload.SHA256)
		if err != nil || len(vsha) != 32 {
			return false, content.ErrInvalid
		}
		asha, err := hex.DecodeString(e.ID)
		if err != nil || len(asha) != 32 {
			return false, content.ErrInvalid
		}
		writes.Queue(`INSERT INTO storage_segments(organization_id,segment_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, organizationID, e.SegmentID)
		writes.Queue(`INSERT INTO compact_embeddings(organization_id,segment_id,space_id,file_id,ordinal,vector_sha256,artifact_sha256)
 SELECT $1,id,$3,$4,$5,$6,$7 FROM storage_segments WHERE organization_id=$1 AND segment_id=$2
 ON CONFLICT(organization_id,segment_id,space_id) DO NOTHING`, organizationID, e.SegmentID, spaceID, fileID, e.Ordinal, vsha, asha)
	}
	if err = tx.SendBatch(ctx, writes).Close(); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

var _ content.EmbeddingFileRepository = EmbeddingStore{}

// SaveLegacyEmbeddingGroup keeps compatibility writes on the legacy side of
// activation. It holds no organization journal lock and performs no object IO.
func (s EmbeddingStore) SaveLegacyEmbeddingGroup(ctx context.Context, artifacts []content.Embedding, space content.VectorSpace) (bool, error) {
	if len(artifacts) == 0 {
		return false, content.ErrInvalid
	}
	first := artifacts[0]
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if err = lockProcessingVersion(ctx, tx, first.Organization, first.VersionID); err != nil {
		return false, err
	}
	active, err := compactWrites(ctx, tx)
	if err != nil {
		return false, err
	}
	if active {
		return false, nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO vector_spaces(id,manifest) VALUES($1,$2) ON CONFLICT DO NOTHING`, space.ID, space.Manifest); err != nil {
		return false, err
	}
	var same bool
	if err = tx.QueryRow(ctx, `SELECT manifest=$2::jsonb FROM vector_spaces WHERE id=$1`, space.ID, space.Manifest).Scan(&same); err != nil {
		return false, err
	}
	if !same {
		return false, content.ErrConflict
	}
	for _, e := range artifacts {
		if e.Organization != first.Organization || e.VersionID != first.VersionID || e.SegmentationID != first.SegmentationID || e.SpaceID != space.ID || e.Producer != first.Producer {
			return false, content.ErrInvalid
		}
		var prior string
		err = tx.QueryRow(ctx, `SELECT id FROM embedding_artifacts WHERE organization=$1 AND derivation_id=$2`, e.Organization, e.DerivationID).Scan(&prior)
		if err == nil {
			if prior != e.ID {
				return false, content.ErrConflict
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
		raw, err := json.Marshal(e)
		if err != nil {
			return false, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO embedding_artifacts VALUES($1,$2,$3,$4,$5,$6)`, e.Organization, e.ID, e.DerivationID, e.SegmentID, e.SpaceID, raw); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}
