package postgres

import (
	"cmp"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5"
)

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

// internEmbeddingKeysSQL interns shared lookup keys across independent tables
// before lookupEmbeddingGroupStateSQL reads them in a fresh READ COMMITTED
// snapshot.
const internEmbeddingKeysSQL = `WITH vs AS (
 INSERT INTO vector_spaces(id,manifest) VALUES($1,$2) ON CONFLICT DO NOTHING
), org AS (
 INSERT INTO storage_organizations(organization) VALUES($3) ON CONFLICT DO NOTHING
)
INSERT INTO storage_spaces(space_id) VALUES($4) ON CONFLICT DO NOTHING`

const lookupEmbeddingGroupStateSQL = `SELECT vs.manifest=$2::jsonb,
 o.id,
 s.id,
 COALESCE((SELECT encode(ef.sha256,'hex') FROM embedding_files ef WHERE ef.organization_id=o.id AND ef.segmentation_id=$5 AND ef.space_id=s.id), '')
FROM vector_spaces vs
CROSS JOIN storage_organizations o
CROSS JOIN storage_spaces s
WHERE vs.id=$1 AND o.organization=$3 AND s.space_id=$4`

// upsertEmbeddingFileAndSegmentsSQL upserts the embedding_files descriptor and
// interns all storage_segments in sorted segment_id order so concurrent writers
// on overlapping segment sets acquire PK locks in the same direction.
// insertCompactEmbeddingsSQL runs in a subsequent statement so READ COMMITTED
// sees storage_segments rows committed concurrently.
const upsertEmbeddingFileAndSegmentsSQL = `WITH ef AS (
 INSERT INTO embedding_files(organization_id,version_id,segmentation_id,space_id,corpus_id,recipe,producer,object_key,sha256,byte_length,dimensions,row_count,presence)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
 ON CONFLICT(organization_id,segmentation_id,space_id) DO UPDATE SET object_key=EXCLUDED.object_key,sha256=EXCLUDED.sha256,byte_length=EXCLUDED.byte_length,presence=EXCLUDED.presence
 WHERE embedding_files.producer=EXCLUDED.producer AND embedding_files.dimensions=EXCLUDED.dimensions AND embedding_files.row_count=EXCLUDED.row_count
 RETURNING id
), ss AS (
 INSERT INTO storage_segments(organization_id,segment_id)
 SELECT $1,x.segment_id
 FROM ef
 CROSS JOIN unnest($14::text[]) AS x(segment_id)
 ORDER BY x.segment_id
 ON CONFLICT(organization_id,segment_id) DO NOTHING
)
SELECT id FROM ef`

// insertCompactEmbeddingsSQL maps external segment IDs to interned numeric IDs
// and inserts compact_embeddings in ascending k.id primary-key order with
// ON CONFLICT DO NOTHING for deadlock-free idempotent replay.
const insertCompactEmbeddingsSQL = `INSERT INTO compact_embeddings(organization_id,segment_id,space_id,file_id,ordinal,vector_sha256,artifact_sha256)
SELECT $1,k.id,$3,$4,x.ordinal,x.vector_sha256,x.artifact_sha256
FROM unnest($2::text[],$5::integer[],$6::bytea[],$7::bytea[])
 AS x(segment_id,ordinal,vector_sha256,artifact_sha256)
JOIN storage_segments k ON k.organization_id=$1 AND k.segment_id=x.segment_id
ORDER BY k.id
ON CONFLICT(organization_id,segment_id,space_id) DO NOTHING`

type compactEmbeddingBatch struct {
	UniqueSegmentIDs []string
	SegmentIDs       []string
	Ordinals         []int
	VectorSHAs       [][]byte
	ArtifactSHAs     [][]byte
}

func prepareCompactEmbeddingBatch(artifacts []content.Embedding) (compactEmbeddingBatch, error) {
	n := len(artifacts)
	if n == 0 {
		return compactEmbeddingBatch{
			UniqueSegmentIDs: []string{},
			SegmentIDs:       []string{},
			Ordinals:         []int{},
			VectorSHAs:       [][]byte{},
			ArtifactSHAs:     [][]byte{},
		}, nil
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		return cmp.Compare(artifacts[a].SegmentID, artifacts[b].SegmentID)
	})
	batch := compactEmbeddingBatch{
		UniqueSegmentIDs: make([]string, 0, n),
		SegmentIDs:       make([]string, n),
		Ordinals:         make([]int, n),
		VectorSHAs:       make([][]byte, n),
		ArtifactSHAs:     make([][]byte, n),
	}
	for dst, src := range order {
		e := artifacts[src]
		vsha, err := hex.DecodeString(e.Payload.SHA256)
		if err != nil || len(vsha) != 32 {
			return compactEmbeddingBatch{}, content.ErrInvalid
		}
		asha, err := hex.DecodeString(e.ID)
		if err != nil || len(asha) != 32 {
			return compactEmbeddingBatch{}, content.ErrInvalid
		}
		if dst == 0 || batch.SegmentIDs[dst-1] != e.SegmentID {
			batch.UniqueSegmentIDs = append(batch.UniqueSegmentIDs, e.SegmentID)
		}
		batch.SegmentIDs[dst] = e.SegmentID
		batch.Ordinals[dst] = e.Ordinal
		batch.VectorSHAs[dst] = vsha
		batch.ArtifactSHAs[dst] = asha
	}
	return batch, nil
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
	// Intern shared keys once, then read them in a second statement of the same
	// batch so READ COMMITTED takes a fresh snapshot after any concurrent insert.
	setup := &pgx.Batch{}
	setup.Queue(internEmbeddingKeysSQL, space.ID, space.Manifest, f.Organization, f.SpaceID)
	setup.Queue(lookupEmbeddingGroupStateSQL, space.ID, space.Manifest, f.Organization, f.SpaceID, f.SegmentationID)
	setupRes := tx.SendBatch(ctx, setup)
	if _, err = setupRes.Exec(); err != nil {
		setupRes.Close()
		return false, err
	}
	var same bool
	var organizationID int64
	var spaceID int32
	var previous string
	if err = setupRes.QueryRow().Scan(&same, &organizationID, &spaceID, &previous); err != nil {
		setupRes.Close()
		return false, err
	}
	if err = setupRes.Close(); err != nil {
		return false, err
	}
	if !same {
		return false, content.ErrConflict
	}
	if previous != expected {
		return false, nil
	}
	sha, err := hex.DecodeString(f.Blob.SHA256)
	if err != nil || len(sha) != 32 {
		return false, content.ErrInvalid
	}
	batch, err := prepareCompactEmbeddingBatch(artifacts)
	if err != nil {
		return false, err
	}
	var fileID int64
	err = tx.QueryRow(ctx, upsertEmbeddingFileAndSegmentsSQL,
		organizationID, f.VersionID, f.SegmentationID, spaceID, f.CorpusID, f.Recipe, f.Producer, f.Blob.Key, sha, f.Blob.Size, f.Dimensions, f.RowCount, f.Presence,
		batch.UniqueSegmentIDs).Scan(&fileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, content.ErrConflict
	}
	if err != nil {
		return false, err
	}
	if len(batch.SegmentIDs) > 0 {
		if _, err = tx.Exec(ctx, insertCompactEmbeddingsSQL,
			organizationID, batch.SegmentIDs, spaceID, fileID, batch.Ordinals, batch.VectorSHAs, batch.ArtifactSHAs); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}

var _ content.EmbeddingFileRepository = EmbeddingStore{}
