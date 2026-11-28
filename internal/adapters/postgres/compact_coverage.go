package postgres

import (
	"context"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5"
	"slices"
)

// validateCompactCoverageSQL checks every artifact's stored identity in one
// unnest statement while preserving caller ordinality so missing rows report
// pgx.ErrNoRows and mismatches report content.ErrConflict in deterministic
// order.
const validateCompactCoverageSQL = `SELECT req.ord,
 encode(e.artifact_sha256,'hex'),
 f.producer,
 coalesce(sg.derivation->>'model_input_sha256','')
FROM unnest($1::bigint[],$2::integer[],$3::text[],$4::text[])
 WITH ORDINALITY AS req(file_id,ordinal,segment_id,space_id,ord)
JOIN storage_organizations o ON o.organization=$5
JOIN storage_segments k ON k.organization_id=o.id AND k.segment_id=req.segment_id
JOIN storage_spaces sp ON sp.space_id=req.space_id
JOIN compact_embeddings e ON e.organization_id=o.id AND e.file_id=req.file_id AND e.ordinal=req.ordinal AND e.segment_id=k.id AND e.space_id=sp.id
JOIN embedding_files f ON f.id=e.file_id AND f.version_id=$6 AND f.segmentation_id=$7
JOIN segments sg ON (sg.organization,sg.id)=(o.organization,k.segment_id)
ORDER BY req.ord`

const lockCompactCoverageFilesSQL = `SELECT id,organization_id,row_count
FROM embedding_files
WHERE id=ANY($1::bigint[])
ORDER BY id
FOR UPDATE`

const loadCompactCoverageBatchSQL = `SELECT c.file_id,c.covered
FROM unnest($1::bigint[],$2::bigint[]) AS req(organization_id,file_id)
JOIN compact_embedding_coverage c ON c.organization_id=req.organization_id AND c.file_id=req.file_id AND c.generation_id=$3`

const upsertCompactCoverageBatchSQL = `INSERT INTO compact_embedding_coverage(organization_id,file_id,generation_id,covered)
SELECT x.organization_id,x.file_id,$3,x.covered
FROM unnest($1::bigint[],$2::bigint[],$4::bytea[]) AS x(organization_id,file_id,covered)
ORDER BY x.organization_id,x.file_id
ON CONFLICT(organization_id,file_id,generation_id) DO UPDATE SET covered=EXCLUDED.covered`

type compactCoverageBatch struct {
	FileIDs       []int64
	Ordinals      []int
	SegmentIDs    []string
	SpaceIDs      []string
	ByFile        map[int64][]byte
	SortedFileIDs []int64
}

func prepareCompactCoverageBatch(org string, seg content.Segmentation, g content.Generation, artifacts []content.Embedding) (compactCoverageBatch, error) {
	n := len(artifacts)
	batch := compactCoverageBatch{
		FileIDs:    make([]int64, n),
		Ordinals:   make([]int, n),
		SegmentIDs: make([]string, n),
		SpaceIDs:   make([]string, n),
		ByFile:     make(map[int64][]byte),
	}
	rowCounts := make(map[int64]int)
	for i, e := range artifacts {
		if e.File == nil || e.File.ID == 0 || e.Organization != org || e.VersionID != seg.VersionID || e.SegmentationID != seg.ID || !g.Carries(e.SpaceID) || e.Ordinal < 0 || e.Ordinal >= e.File.RowCount {
			return compactCoverageBatch{}, content.ErrInvalid
		}
		if prev, ok := rowCounts[e.File.ID]; ok && prev != e.File.RowCount {
			return compactCoverageBatch{}, content.ErrInvalid
		}
		rowCounts[e.File.ID] = e.File.RowCount
		mask := batch.ByFile[e.File.ID]
		if mask == nil {
			mask = make([]byte, (e.File.RowCount+7)/8)
			batch.ByFile[e.File.ID] = mask
		}
		content.MarkPresent(mask, e.Ordinal)
		batch.FileIDs[i] = e.File.ID
		batch.Ordinals[i] = e.Ordinal
		batch.SegmentIDs[i] = e.SegmentID
		batch.SpaceIDs[i] = e.SpaceID
	}
	batch.SortedFileIDs = sortedFileIDs(batch.ByFile)
	return batch, nil
}

// insertCompactCoverage validates the fixed-width tuple at the file/ordinal
// index and merges generation coverage once per file. Its caller owns the
// pre-journal savepoint and final routing/currentness fence.
func insertCompactCoverage(ctx context.Context, tx pgx.Tx, org string, seg content.Segmentation, g content.Generation, artifacts []content.Embedding) (int64, error) {
	if len(artifacts) == 0 {
		return 0, nil
	}
	batch, err := prepareCompactCoverageBatch(org, seg, g, artifacts)
	if err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, validateCompactCoverageSQL, batch.FileIDs, batch.Ordinals, batch.SegmentIDs, batch.SpaceIDs, org, seg.VersionID, seg.ID)
	if err != nil {
		return 0, err
	}
	expectedOrd := 1
	for rows.Next() {
		var ord int
		var id, producer, inputSHA string
		if err = rows.Scan(&ord, &id, &producer, &inputSHA); err != nil {
			rows.Close()
			return 0, err
		}
		if ord != expectedOrd {
			rows.Close()
			return 0, pgx.ErrNoRows
		}
		e := artifacts[ord-1]
		if id != e.ID || producer != e.Producer || inputSHA != e.InputSHA || e.DerivationID != content.StableID("embedding-derivation", org, e.SegmentID, e.SpaceID, inputSHA, producer) {
			rows.Close()
			return 0, content.ErrConflict
		}
		expectedOrd++
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if expectedOrd != len(artifacts)+1 {
		return 0, pgx.ErrNoRows
	}
	if len(batch.SortedFileIDs) == 1 {
		return mergeSingleFileCompactCoverage(ctx, tx, batch.SortedFileIDs[0], g.ID, batch.ByFile[batch.SortedFileIDs[0]])
	}
	return mergeMultiFileCompactCoverage(ctx, tx, batch.SortedFileIDs, g.ID, batch.ByFile)
}

func mergeSingleFileCompactCoverage(ctx context.Context, tx pgx.Tx, fileID int64, generationID string, mask []byte) (int64, error) {
	// Serialize per-file bitmap updates before reading the old mask, so
	// counter deltas also remain correct under concurrent backfills.
	var rows int
	var organizationID int64
	if err := tx.QueryRow(ctx, `SELECT organization_id,row_count FROM embedding_files WHERE id=$1 FOR UPDATE`, fileID).Scan(&organizationID, &rows); err != nil {
		return 0, err
	}
	if len(mask) != (rows+7)/8 {
		return 0, content.ErrInvalid
	}
	var previous []byte
	err := tx.QueryRow(ctx, `SELECT covered FROM compact_embedding_coverage WHERE organization_id=$1 AND file_id=$2 AND generation_id=$3`, organizationID, fileID, generationID).Scan(&previous)
	if err != nil && err != pgx.ErrNoRows {
		return 0, err
	}
	merged, inserted, err := mergeCoverageBitmap(mask, previous, rows)
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO compact_embedding_coverage(organization_id,file_id,generation_id,covered)
 SELECT organization_id,id,$2,$3 FROM embedding_files WHERE id=$1
 ON CONFLICT(organization_id,file_id,generation_id) DO UPDATE SET covered=EXCLUDED.covered`, fileID, generationID, merged)
	if err != nil {
		return 0, err
	}
	return inserted, nil
}

func mergeMultiFileCompactCoverage(ctx context.Context, tx pgx.Tx, fileIDs []int64, generationID string, byFile map[int64][]byte) (int64, error) {
	// Lock every touched embedding_files row in ascending ID order before
	// reading compact_embedding_coverage in a fresh READ COMMITTED snapshot.
	lockRows, err := tx.Query(ctx, lockCompactCoverageFilesSQL, fileIDs)
	if err != nil {
		return 0, err
	}
	orgIDs := make([]int64, 0, len(fileIDs))
	rowCounts := make([]int, 0, len(fileIDs))
	idx := 0
	for lockRows.Next() {
		var id, organizationID int64
		var rows int
		if err = lockRows.Scan(&id, &organizationID, &rows); err != nil {
			lockRows.Close()
			return 0, err
		}
		if idx >= len(fileIDs) || id != fileIDs[idx] {
			lockRows.Close()
			return 0, pgx.ErrNoRows
		}
		if len(byFile[id]) != (rows+7)/8 {
			lockRows.Close()
			return 0, content.ErrInvalid
		}
		orgIDs = append(orgIDs, organizationID)
		rowCounts = append(rowCounts, rows)
		idx++
	}
	if err = lockRows.Err(); err != nil {
		lockRows.Close()
		return 0, err
	}
	lockRows.Close()
	if idx != len(fileIDs) {
		return 0, pgx.ErrNoRows
	}

	covRows, err := tx.Query(ctx, loadCompactCoverageBatchSQL, orgIDs, fileIDs, generationID)
	if err != nil {
		return 0, err
	}
	prevByFile := make(map[int64][]byte, len(fileIDs))
	for covRows.Next() {
		var fileID int64
		var covered []byte
		if err = covRows.Scan(&fileID, &covered); err != nil {
			covRows.Close()
			return 0, err
		}
		prevByFile[fileID] = covered
	}
	if err = covRows.Err(); err != nil {
		covRows.Close()
		return 0, err
	}
	covRows.Close()

	var inserted int64
	mergedMasks := make([][]byte, len(fileIDs))
	for i, fileID := range fileIDs {
		merged, delta, err := mergeCoverageBitmap(byFile[fileID], prevByFile[fileID], rowCounts[i])
		if err != nil {
			return 0, err
		}
		mergedMasks[i] = merged
		inserted += delta
	}
	if _, err = tx.Exec(ctx, upsertCompactCoverageBatchSQL, orgIDs, fileIDs, generationID, mergedMasks); err != nil {
		return 0, err
	}
	return inserted, nil
}

func mergeCoverageBitmap(mask, previous []byte, rows int) ([]byte, int64, error) {
	if len(previous) != 0 && len(previous) != len(mask) {
		return nil, 0, content.ErrConflict
	}
	merged := make([]byte, len(mask))
	for i := range merged {
		merged[i] = mask[i]
		if len(previous) > 0 {
			merged[i] |= previous[i]
		}
	}
	var inserted int64
	for i := 0; i < rows; i++ {
		if content.Present(mask, i) && !content.Present(previous, i) {
			inserted++
		}
	}
	return merged, inserted, nil
}

func sortedFileIDs(m map[int64][]byte) []int64 {
	ids := make([]int64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
