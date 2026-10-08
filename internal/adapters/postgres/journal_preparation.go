package postgres

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5"
)

// Preparation is part of the caller's transaction. A savepoint lets a fresh
// journal guard discard speculative hash writes without publishing them on a
// no-op. Rolling it back also releases the journal lock, so callers must fence
// again before changing mutable state.
func prepareJournal(ctx context.Context, tx pgx.Tx, prepare func(pgx.Tx) error) (pgx.Tx, error) {
	if group := journalGroupOf(ctx); group != nil {
		if group.locked {
			return nil, content.ErrInvalid
		}
		return tx, prepare(tx)
	}
	if err := lockProjectionRouting(ctx, tx); err != nil {
		return nil, err
	}
	stage, err := tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if err = prepare(stage); err != nil {
		return nil, err
	}
	return stage, nil
}

func insertPublicationBlobs(ctx context.Context, tx pgx.Tx, org string, p content.Publication) error {
	blobs := []content.Blob{p.Normalized, p.Manifest}
	for _, part := range p.Parts {
		blobs = append(blobs, part.Blob)
	}
	// Preserve the first supplied key for a hash, while acquiring shared hash
	// identities in the same order across publications to avoid avoidable cycles.
	seen := map[string]bool{}
	unique := blobs[:0]
	for _, b := range blobs {
		if !seen[b.SHA256] {
			unique = append(unique, b)
			seen[b.SHA256] = true
		}
	}
	slices.SortFunc(unique, func(a, b content.Blob) int { return cmp.Compare(a.SHA256, b.SHA256) })
	writes := &pgx.Batch{}
	for _, b := range unique {
		writes.Queue("INSERT INTO content_blobs VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING", org, content.StableID("blob", org, b.SHA256), b.Key, b.SHA256, b.Size)
	}
	return tx.SendBatch(ctx, writes).Close()
}

// A Version and its Parts are immutable canonical facts. Prepare them with
// their blobs, then let the fresh receipt/source guard accept the preparation
// or roll it back before any events or mutable receipt state can commit.
func insertPublicationVersion(ctx context.Context, tx pgx.Tx, w content.Work, p content.Publication) (bool, error) {
	if err := insertPublicationBlobs(ctx, tx, w.Organization, p); err != nil {
		return false, err
	}
	provenance, err := json.Marshal(w.Command.Provenance)
	if err != nil {
		return false, err
	}
	extensions := w.Command.Extensions
	if extensions == nil {
		extensions = content.Extensions{}
	}
	extensionsJSON, err := json.Marshal(extensions)
	if err != nil {
		return false, err
	}
	var quarantine []byte
	processing, code := "queued", ""
	if q := p.Quarantine; q != nil {
		if quarantine, err = json.Marshal(q); err != nil {
			return false, err
		}
		processing, code = "blocked", q.Code
	}
	keys, roles, blobs := make([]string, len(p.Parts)), make([]string, len(p.Parts)), make([]string, len(p.Parts))
	for i, part := range p.Parts {
		keys[i], roles[i], blobs[i] = part.Key, part.Role, content.StableID("blob", w.Organization, part.Blob.SHA256)
	}
	var created bool
	err = tx.QueryRow(ctx, `WITH version AS (
 INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,source_position,predecessor_id,text_blob_id,manifest_blob_id,provenance,extensions,quarantined,processing,error_code,quarantine,materialized_at,quarantined_at,quarantine_stage)
 VALUES($1,$2,$3,$4,$5,$6,$7,nullif($8,''),$9,$10,$11,$12,$13,$14,$15,$16,clock_timestamp(),CASE WHEN $13 THEN clock_timestamp() END,CASE WHEN $13 THEN 'normalization' END)
 ON CONFLICT(organization,id) DO NOTHING RETURNING id
), parts AS (
 INSERT INTO version_parts(organization,version_id,part_key,role,blob_id)
 SELECT $1,v.id,p.part_key,p.role,p.blob_id FROM version v
 CROSS JOIN unnest($17::text[],$18::text[],$19::text[]) p(part_key,role,blob_id)
) SELECT EXISTS(SELECT 1 FROM version)`, w.Organization, w.VersionID, w.RecordID, w.Slot, w.Digest, w.Order, w.Position, w.PredecessorID, content.StableID("blob", w.Organization, p.Normalized.SHA256), content.StableID("blob", w.Organization, p.Manifest.SHA256), provenance, extensionsJSON, p.Quarantine != nil, processing, code, quarantine, keys, roles, blobs).Scan(&created)
	return created, err
}

// Artifacts and generation space identities are immutable. Check those hash
// identities and insert coverage before the journal fence; eligibility,
// routing, currentness and public events still belong behind that fence.
func insertEmbeddingCoverage(ctx context.Context, tx pgx.Tx, org string, seg content.Segmentation, g content.Generation, artifacts []content.Embedding) (int64, error) {
	var compact, legacy []content.Embedding
	for _, e := range artifacts {
		if e.File != nil {
			compact = append(compact, e)
		} else {
			legacy = append(legacy, e)
		}
	}
	var packedCount int64
	if len(compact) > 0 {
		var err error
		packedCount, err = insertCompactCoverage(ctx, tx, org, seg, g, compact)
		if err != nil {
			return 0, err
		}
	}
	artifacts = legacy
	artifacts = slices.Clone(artifacts)
	slices.SortFunc(artifacts, func(a, b content.Embedding) int {
		if order := cmp.Compare(a.SegmentID, b.SegmentID); order != 0 {
			return order
		}
		return cmp.Compare(a.SpaceID, b.SpaceID)
	})
	segments := map[string]bool{}
	for _, p := range seg.Segments {
		segments[p.ID] = true
	}
	batch := &pgx.Batch{}
	for _, e := range artifacts {
		if e.Organization != org || !segments[e.SegmentID] || !g.Carries(e.SpaceID) {
			return 0, content.ErrInvalid
		}
		batch.Queue(`SELECT id FROM `+embeddingArtifactsRelation+` WHERE organization=$1 AND derivation_id=$2 AND segment_id=$3 AND space_id=$4`, org, e.DerivationID, e.SegmentID, e.SpaceID)
		batch.Queue(`INSERT INTO embedding_coverage(organization,segment_id,generation_id,artifact_id,space_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, org, e.SegmentID, g.ID, e.ID, e.SpaceID)
	}
	results := tx.SendBatch(ctx, batch)
	defer results.Close()
	var inserted int64
	for _, e := range artifacts {
		var stored string
		if err := results.QueryRow().Scan(&stored); err != nil {
			return 0, err
		}
		if stored != e.ID {
			return 0, content.ErrConflict
		}
		tag, err := results.Exec()
		if err != nil {
			return 0, err
		}
		inserted += tag.RowsAffected()
	}
	return inserted + packedCount, results.Close()
}
