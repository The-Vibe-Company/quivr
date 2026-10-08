package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StorageCompaction advances in bounded units. Object IO finishes before the
// version/routing fences; retirement and its checkpoint commit together.
type StorageCompaction struct {
	Pool  *pgxpool.Pool
	Blobs content.Blobs
}
type CompactionStatus struct {
	ID                 string `json:"id"`
	Phase              string `json:"phase"`
	Checkpoint         string `json:"checkpoint"`
	RetireAuditDetail  bool   `json:"retire_audit_detail"`
	GroupsDone         int64  `json:"groups_done"`
	ReceiptsDone       int64  `json:"receipts_done"`
	NormalizationsDone int64  `json:"normalizations_done"`
}

func (s StorageCompaction) Start(ctx context.Context, id string, retireAudit bool) (CompactionStatus, error) {
	if id == "" || len(id) > 128 {
		return CompactionStatus{}, content.ErrInvalid
	}
	if retireAudit {
		active, err := (EmbeddingStore{Pool: s.Pool}).CompactStorage(ctx)
		if err != nil {
			return CompactionStatus{}, err
		}
		if !active {
			return CompactionStatus{}, errors.New("audit retirement requires compact storage activation")
		}
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO storage_compactions(id,retire_audit_detail) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, retireAudit)
	if err != nil {
		return CompactionStatus{}, err
	}
	status, err := s.Status(ctx, id)
	if err == nil && status.RetireAuditDetail != retireAudit {
		err = content.ErrConflict
	}
	return status, err
}
func (s StorageCompaction) Status(ctx context.Context, id string) (CompactionStatus, error) {
	var c CompactionStatus
	err := s.Pool.QueryRow(ctx, `SELECT id,phase,checkpoint,retire_audit_detail,groups_done,receipts_done,normalizations_done FROM storage_compactions WHERE id=$1`, id).Scan(&c.ID, &c.Phase, &c.Checkpoint, &c.RetireAuditDetail, &c.GroupsDone, &c.ReceiptsDone, &c.NormalizationsDone)
	return c, notFound(err)
}
func checkpointPair(value string) (org, id string, errorValue error) {
	if value == "" {
		return "", "", nil
	}
	var pair [2]string
	errorValue = json.Unmarshal([]byte(value), &pair)
	return pair[0], pair[1], errorValue
}
func pairCheckpoint(org, id string) string {
	raw, _ := json.Marshal([2]string{org, id})
	return string(raw)
}
func (s StorageCompaction) advance(ctx context.Context, tx pgx.Tx, c CompactionStatus, phase, checkpoint string, groups, receipts, normalizations int64) error {
	tag, err := tx.Exec(ctx, `UPDATE storage_compactions SET phase=$4,checkpoint=$5,groups_done=groups_done+$6,receipts_done=receipts_done+$7,normalizations_done=normalizations_done+$8 WHERE id=$1 AND phase=$2 AND checkpoint=$3`, c.ID, c.Phase, c.Checkpoint, phase, checkpoint, groups, receipts, normalizations)
	if err == nil && tag.RowsAffected() != 1 {
		err = content.ErrConflict
	}
	return err
}
func (s StorageCompaction) nextPhase(ctx context.Context, c CompactionStatus, phase string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = s.advance(ctx, tx, c, phase, "", 0, 0, 0); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s StorageCompaction) Step(ctx context.Context, id string) (CompactionStatus, error) {
	c, err := s.Status(ctx, id)
	if err != nil {
		return c, err
	}
	switch c.Phase {
	case "vectors":
		err = s.vectorStep(ctx, c)
	case "receipts":
		err = s.receiptStep(ctx, c)
	case "normalizations":
		err = s.normalizationStep(ctx, c)
	case "done":
		return c, nil
	default:
		err = content.ErrInvalid
	}
	if err != nil {
		return c, err
	}
	return s.Status(ctx, id)
}

func (s StorageCompaction) vectorStep(ctx context.Context, c CompactionStatus) error {
	org, id, err := checkpointPair(c.Checkpoint)
	if err != nil {
		return err
	}
	var metadata []byte
	err = s.Pool.QueryRow(ctx, `SELECT a.organization,a.id,a.metadata FROM embedding_artifacts a WHERE (a.organization,a.id)>($1,$2)
 AND (coalesce((SELECT compact FROM storage_state WHERE singleton),false) OR NOT EXISTS(
 SELECT 1 FROM storage_organizations o JOIN storage_segments k ON k.organization_id=o.id AND k.segment_id=a.segment_id
 JOIN storage_spaces sp ON sp.space_id=a.space_id JOIN compact_embeddings e ON (e.organization_id,e.segment_id,e.space_id)=(o.id,k.id,sp.id)
 WHERE o.organization=a.organization AND encode(e.artifact_sha256,'hex')=a.id))
 ORDER BY a.organization,a.id LIMIT 1`, org, id).Scan(&org, &id, &metadata)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.nextPhase(ctx, c, "receipts")
	}
	if err != nil {
		return err
	}
	var first content.Embedding
	if err = json.Unmarshal(metadata, &first); err != nil {
		return err
	}
	stored, err := (ProjectionStore{Pool: s.Pool}).StoredSegmentation(ctx, org, first.VersionID, first.Recipe)
	if err != nil {
		return err
	}
	seg := content.Segmentation{ID: stored.ID, VersionID: first.VersionID, Recipe: first.Recipe}
	derivations := make([]string, 0, len(stored.Segments))
	for _, p := range stored.Segments {
		seg.Segments = append(seg.Segments, content.Segment{ID: p.ID})
		derivations = append(derivations, content.StableID("embedding-derivation", org, p.ID, first.SpaceID, p.Derivation.ModelInputSHA256, first.Producer))
	}
	rows, err := s.Pool.Query(ctx, `SELECT metadata FROM embedding_artifacts WHERE organization=$1 AND derivation_id=ANY($2::text[])`, org, derivations)
	if err != nil {
		return err
	}
	artifacts, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (content.Embedding, error) {
		var e content.Embedding
		var raw []byte
		err := r.Scan(&raw)
		if err == nil {
			err = json.Unmarshal(raw, &e)
		}
		return e, err
	})
	if err != nil {
		return err
	}
	service := content.Service{Blobs: s.Blobs, Embeddings: EmbeddingStore{Pool: s.Pool}}
	data, err := service.LoadEmbeddingData(ctx, artifacts)
	if err != nil {
		return err
	}
	space := content.VectorSpace{ID: first.SpaceID}
	if err = s.Pool.QueryRow(ctx, `SELECT manifest FROM vector_spaces WHERE id=$1`, space.ID).Scan(&space.Manifest); err != nil {
		return err
	}
	packed, err := service.PackEmbeddingGroup(ctx, seg, space, data)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockProcessingVersion(ctx, tx, org, first.VersionID); err != nil {
		return err
	}
	active, err := compactWrites(ctx, tx)
	if err != nil {
		return err
	}
	if active {
		if err = retireVectorGroup(ctx, tx, org, seg, space, packed); err != nil {
			return err
		}
	}
	if err = s.advance(ctx, tx, c, c.Phase, pairCheckpoint(org, id), 1, 0, 0); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func retireVectorGroup(ctx context.Context, tx pgx.Tx, org string, seg content.Segmentation, space content.VectorSpace, data []content.EmbeddingData) error {
	// Canonical bytes have been verified outside this transaction. A concurrent
	// fill may add rows but cannot replace an existing ordinal or artifact ID.
	byID := map[string]content.Embedding{}
	ids := make([]string, 0, len(data))
	segments := make([]string, 0, len(data))
	for _, d := range data {
		byID[d.Artifact.ID] = d.Artifact
		ids = append(ids, d.Artifact.ID)
		segments = append(segments, d.Artifact.SegmentID)
	}
	rows, err := tx.Query(ctx, `SELECT generation_id,artifact_id FROM embedding_coverage WHERE organization=$1 AND segment_id=ANY($2::text[]) AND space_id=$3 ORDER BY generation_id,segment_id`, org, segments, space.ID)
	if err != nil {
		return err
	}
	type covered struct{ generation, id string }
	coverage, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (covered, error) {
		var x covered
		err := r.Scan(&x.generation, &x.id)
		return x, err
	})
	if err != nil {
		return err
	}
	groups := map[string][]content.Embedding{}
	var generations []string
	for _, x := range coverage {
		e, ok := byID[x.id]
		if !ok {
			return content.ErrConflict
		}
		if _, ok := groups[x.generation]; !ok {
			generations = append(generations, x.generation)
		}
		groups[x.generation] = append(groups[x.generation], e)
	}
	for _, generation := range generations {
		g := content.Generation{ID: generation, SpaceID: space.ID}
		if _, err = insertCompactCoverage(ctx, tx, org, seg, g, groups[generation]); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM embedding_coverage WHERE organization=$1 AND segment_id=ANY($2::text[]) AND space_id=$3 AND artifact_id=ANY($4::text[])`, org, segments, space.ID, ids); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM embedding_artifacts WHERE organization=$1 AND id=ANY($2::text[])`, org, ids)
	return err
}

func (s StorageCompaction) receiptStep(ctx context.Context, c CompactionStatus) error {
	org, id, err := checkpointPair(c.Checkpoint)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockProjectionRouting(ctx, tx); err != nil {
		return err
	}
	active, err := compactWrites(ctx, tx)
	if err != nil {
		return err
	}
	if c.RetireAuditDetail && !active {
		return content.ErrConflict
	}
	var recordID, slot string
	err = tx.QueryRow(ctx, `SELECT organization,id,record_id,coalesce(slot,'') FROM ingestion_receipts WHERE (organization,id)>($1,$2) ORDER BY organization,id LIMIT 1 FOR UPDATE`, org, id).Scan(&org, &id, &recordID, &slot)
	if errors.Is(err, pgx.ErrNoRows) {
		tx.Rollback(ctx)
		return s.nextPhase(ctx, c, "normalizations")
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE ingestion_receipts SET request_digest=coalesce(request_digest,sha256(canonical_request)),canonical_request=CASE WHEN $3 THEN ''::bytea ELSE canonical_request END,command=CASE WHEN $3 THEN '{}'::jsonb ELSE command END WHERE organization=$1 AND id=$2`, org, id, c.RetireAuditDetail)
	if err != nil {
		return err
	}
	if c.RetireAuditDetail {
		if _, err = tx.Exec(ctx, `UPDATE accepted_revisions SET command=command-'idempotency_key'-'source_revision'-'source_position' WHERE organization=$1 AND record_id=$2 AND slot=$3`, org, recordID, slot); err != nil {
			return err
		}
	}
	if err = s.advance(ctx, tx, c, c.Phase, pairCheckpoint(org, id), 0, 1, 0); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s StorageCompaction) normalizationStep(ctx context.Context, c CompactionStatus) error {
	org, id, err := checkpointPair(c.Checkpoint)
	if err != nil {
		return err
	}
	err = s.Pool.QueryRow(ctx, `SELECT organization,version_id FROM normalizations WHERE (organization,version_id)>($1,$2) ORDER BY organization,version_id LIMIT 1`, org, id).Scan(&org, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.nextPhase(ctx, c, "done")
	}
	if err != nil {
		return err
	}
	var snapshot []byte
	if err = s.Pool.QueryRow(ctx, `SELECT sha256(convert_to(to_jsonb(n)::text,'UTF8')) FROM normalizations n WHERE organization=$1 AND version_id=$2`, org, id).Scan(&snapshot); err != nil {
		return err
	}
	store := NormalizationStore{Pool: s.Pool, Blobs: s.Blobs}
	n, found, err := store.Normalized(ctx, org, id)
	if err != nil {
		return err
	}
	if !found {
		return content.ErrConflict
	}
	if c.RetireAuditDetail {
		n = compactNormalization(n)
	}
	raw, err := json.Marshal(n)
	if err != nil {
		return err
	}
	blob, err := s.Blobs.Put(ctx, org, raw)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockProcessingVersion(ctx, tx, org, id); err != nil {
		return err
	}
	active, err := compactWrites(ctx, tx)
	if err != nil {
		return err
	}
	if c.RetireAuditDetail && !active {
		return content.ErrConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE normalizations SET outcome_key=$3,outcome_sha256=$4,outcome_size=$5,
 extensions=CASE WHEN $6 THEN '{}'::jsonb ELSE extensions END,
 invocation_id=CASE WHEN $7 AND failure_code='' THEN '' ELSE invocation_id END
 WHERE organization=$1 AND version_id=$2 AND sha256(convert_to(to_jsonb(normalizations)::text,'UTF8'))=$8`, org, id, blob.Key, blob.SHA256, blob.Size, active, c.RetireAuditDetail, snapshot)
	if err == nil && tag.RowsAffected() != 1 {
		return content.ErrConflict
	}
	if err != nil {
		return err
	}
	if err = s.advance(ctx, tx, c, c.Phase, pairCheckpoint(org, id), 0, 0, 1); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
