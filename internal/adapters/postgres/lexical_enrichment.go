package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5"
)

// UpdateSegmentLexicalText persists Phase-2 lexical enrichment updates to
// canonical segments.derivation and updates segmentations.digest atomically
// without altering segment IDs, offsets, text checksums, or dense embeddings.
func (s ProjectionStore) UpdateSegmentLexicalText(ctx context.Context, org string, seg content.Segmentation, updates []content.SegmentLexicalUpdate) (content.Segmentation, error) {
	updated, changed, err := content.ApplySegmentLexicalUpdates(seg, updates)
	if err != nil || !changed {
		return updated, err
	}
	beforeDigest := content.SegmentationDigest(seg)
	afterDigest := content.SegmentationDigest(updated)

	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return seg, err
	}
	defer tx.Rollback(ctx)

	if err = lockProcessingVersion(ctx, tx, org, seg.VersionID); err != nil {
		return seg, err
	}
	var storedDigest string
	err = tx.QueryRow(ctx, `SELECT digest FROM segmentations WHERE organization=$1 AND id=$2 AND version_id=$3 FOR UPDATE`, org, seg.ID, seg.VersionID).Scan(&storedDigest)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return seg, corpus.ErrNotFound
		}
		return seg, err
	}
	if storedDigest == afterDigest {
		// Already persisted by a prior attempt; idempotent retry.
		if err = tx.Commit(ctx); err != nil {
			return seg, err
		}
		return updated, nil
	}
	if storedDigest != beforeDigest {
		return seg, content.ErrConflict
	}

	ids := make([]string, len(updates))
	for i, u := range updates {
		ids[i] = u.SegmentID
	}
	rows, err := tx.Query(ctx, `SELECT id, derivation FROM segments WHERE organization=$1 AND segmentation_id=$2 AND id=ANY($3)`, org, seg.ID, ids)
	if err != nil {
		return seg, err
	}
	derivations := make(map[string]content.SegmentDerivation, len(updates))
	for rows.Next() {
		var id string
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return seg, err
		}
		var d content.SegmentDerivation
		if err = json.Unmarshal(raw, &d); err != nil {
			rows.Close()
			return seg, err
		}
		derivations[id] = d
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return seg, err
	}
	if len(derivations) != len(updates) {
		return seg, corpus.ErrNotFound
	}

	encoded := make([][]byte, len(updates))
	for i, u := range updates {
		d := derivations[u.SegmentID]
		d.LexicalText = u.LexicalText
		metadata, err := compactSegmentDerivation(d)
		if err != nil {
			return seg, err
		}
		encoded[i] = metadata
	}
	if _, err = tx.Exec(ctx, `UPDATE segments AS s SET derivation=u.derivation FROM unnest($3::text[],$4::jsonb[]) AS u(id,derivation) WHERE s.organization=$1 AND s.segmentation_id=$2 AND s.id=u.id`, org, seg.ID, ids, encoded); err != nil {
		return seg, err
	}
	if _, err = tx.Exec(ctx, `UPDATE segmentations SET digest=$4 WHERE organization=$1 AND id=$2 AND version_id=$3`, org, seg.ID, seg.VersionID, afterDigest); err != nil {
		return seg, err
	}
	if err = tx.Commit(ctx); err != nil {
		return seg, err
	}
	return updated, nil
}

var _ content.LexicalTextStore = ProjectionStore{}
