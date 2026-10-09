package postgres

import (
	"context"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5"
	"slices"
)

// insertCompactCoverage validates the fixed-width tuple at the file/ordinal
// index and merges generation coverage once per file. Its caller owns the
// pre-journal savepoint and final routing/currentness fence.
func insertCompactCoverage(ctx context.Context, tx pgx.Tx, org string, seg content.Segmentation, g content.Generation, artifacts []content.Embedding) (int64, error) {
	byFile := map[int64][]byte{}
	batch := &pgx.Batch{}
	for _, e := range artifacts {
		if e.File == nil || e.File.ID == 0 || e.Organization != org || e.VersionID != seg.VersionID || e.SegmentationID != seg.ID || !g.Carries(e.SpaceID) || e.Ordinal < 0 || e.Ordinal >= e.File.RowCount {
			return 0, content.ErrInvalid
		}
		mask := byFile[e.File.ID]
		if mask == nil {
			mask = make([]byte, (e.File.RowCount+7)/8)
		}
		content.MarkPresent(mask, e.Ordinal)
		byFile[e.File.ID] = mask
		batch.Queue(`SELECT encode(e.artifact_sha256,'hex'),f.producer,coalesce(sg.derivation->>'model_input_sha256','') FROM compact_embeddings e
 JOIN storage_organizations o ON o.id=e.organization_id JOIN storage_segments k ON (k.organization_id,k.id)=(e.organization_id,e.segment_id)
 JOIN storage_spaces sp ON sp.id=e.space_id
 JOIN embedding_files f ON f.id=e.file_id
 JOIN segments sg ON (sg.organization,sg.id)=(o.organization,k.segment_id) WHERE e.file_id=$1 AND e.ordinal=$2 AND o.organization=$3 AND k.segment_id=$4 AND sp.space_id=$5 AND f.version_id=$6 AND f.segmentation_id=$7`, e.File.ID, e.Ordinal, org, e.SegmentID, e.SpaceID, seg.VersionID, seg.ID)
	}
	result := tx.SendBatch(ctx, batch)
	for _, e := range artifacts {
		var id, producer, inputSHA string
		if err := result.QueryRow().Scan(&id, &producer, &inputSHA); err != nil {
			result.Close()
			return 0, err
		}
		if id != e.ID || producer != e.Producer || inputSHA != e.InputSHA || e.DerivationID != content.StableID("embedding-derivation", org, e.SegmentID, e.SpaceID, inputSHA, producer) {
			result.Close()
			return 0, content.ErrConflict
		}
	}
	if err := result.Close(); err != nil {
		return 0, err
	}
	var inserted int64
	// Stable file order prevents coverage-row arbitration cycles across writers.
	for _, fileID := range sortedFileIDs(byFile) {
		mask := byFile[fileID]
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
		err := tx.QueryRow(ctx, `SELECT covered FROM compact_embedding_coverage WHERE organization_id=$1 AND file_id=$2 AND generation_id=$3`, organizationID, fileID, g.ID).Scan(&previous)
		if err != nil && err != pgx.ErrNoRows {
			return 0, err
		}
		merged := make([]byte, len(mask))
		if len(previous) != 0 && len(previous) != len(mask) {
			return 0, content.ErrConflict
		}
		for i := range merged {
			merged[i] = mask[i]
			if len(previous) > 0 {
				merged[i] |= previous[i]
			}
		}
		for i := 0; i < rows; i++ {
			if content.Present(mask, i) && !content.Present(previous, i) {
				inserted++
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO compact_embedding_coverage(organization_id,file_id,generation_id,covered)
 SELECT organization_id,id,$2,$3 FROM embedding_files WHERE id=$1
 ON CONFLICT(organization_id,file_id,generation_id) DO UPDATE SET covered=EXCLUDED.covered`, fileID, g.ID, merged)
		if err != nil {
			return 0, err
		}
	}
	return inserted, nil
}

func sortedFileIDs(m map[int64][]byte) []int64 {
	ids := make([]int64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
