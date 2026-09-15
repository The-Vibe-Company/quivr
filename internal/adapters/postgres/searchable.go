package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/jackc/pgx/v5"
)

func (s ContentStore) BootstrapGeneration(ctx context.Context, collection, spaceID string) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO projection_generations(id,collection,profile_version,active,space_id) VALUES($1,$2,$3,true,$4) ON CONFLICT DO NOTHING`, content.StableID("generation", collection, retrieval.ProfileVersion), collection, retrieval.ProfileVersion, spaceID)
	return err
}
func (s ContentStore) ActiveGeneration(ctx context.Context) (content.Generation, error) {
	var g content.Generation
	err := s.Pool.QueryRow(ctx, `SELECT id,collection,profile_version,space_id FROM projection_generations WHERE active`).Scan(&g.ID, &g.Collection, &g.ProfileVersion, &g.SpaceID)
	return g, err
}
func (s ContentStore) Authorize(ctx context.Context, scope corpus.Scope, ids []string) error {
	var count int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM corpora WHERE organization=$1 AND id=ANY($2)`, scope.Organization, ids).Scan(&count)
	if err != nil {
		return err
	}
	if count != len(ids) {
		return corpus.ErrForbidden
	}
	return nil
}
func (s ContentStore) SaveSegmentation(ctx context.Context, org string, result content.Segmentation) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
	}
	digest := content.SegmentationDigest(result)
	var existing string
	err = tx.QueryRow(ctx, `SELECT digest FROM segmentations WHERE organization=$1 AND id=$2`, org, result.ID).Scan(&existing)
	if err == nil {
		if existing != digest {
			return content.ErrConflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO segmentations VALUES($1,$2,$3,$4,$5,$6)`, org, result.ID, result.VersionID, result.Recipe, digest, result.Provenance); err != nil {
		return err
	}
	for _, p := range result.Segments {
		metadata, err := json.Marshal(p.Derivation)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO segments VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, org, p.ID, result.ID, result.VersionID, p.PartKey, p.Start, p.End, content.Hash([]byte(p.Text)), metadata); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (s ContentStore) BaselineProgress(ctx context.Context, org, id, state, code string, quarantined bool) error {
	if !quarantined {
		_, err := s.Pool.Exec(ctx, `UPDATE record_versions SET processing=$3,error_code=$4 WHERE organization=$1 AND id=$2 AND NOT baseline_ready AND NOT quarantined`, org, id, state, code)
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
	}
	var recordID, corpusID string
	var ready, held bool
	err = tx.QueryRow(ctx, `SELECT v.record_id,r.corpus_id,v.baseline_ready,v.quarantined FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2 FOR UPDATE OF v,r`, org, id).Scan(&recordID, &corpusID, &ready, &held)
	if err != nil {
		return err
	}
	if ready || held {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `UPDATE record_versions SET processing=$3,error_code=$4,quarantined=true WHERE organization=$1 AND id=$2`, org, id, state, code); err != nil {
		return err
	}
	if err = appendEvent(ctx, tx, eventInput{Organization: org, CorpusID: corpusID, Kind: "record.quarantined", Resource: "record", ResourceID: recordID, MutationID: content.StableID("quarantine", id, code)}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s ContentStore) Promote(ctx context.Context, org string, seg content.Segmentation, g content.Generation) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
	}
	var recordID, corpusID, desired string
	var withdrawn, quarantined, ready, active bool
	err = tx.QueryRow(ctx, `SELECT r.id,r.corpus_id,coalesce(r.desired_version_id,''),r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id),v.quarantined,v.baseline_ready FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2 FOR UPDATE OF r,v`, org, seg.VersionID).Scan(&recordID, &corpusID, &desired, &withdrawn, &quarantined, &ready)
	if err != nil {
		return err
	}
	if withdrawn || quarantined {
		return tx.Commit(ctx)
	}
	if err = tx.QueryRow(ctx, `SELECT active FROM projection_generations WHERE id=$1 FOR SHARE`, g.ID).Scan(&active); err != nil {
		return err
	}
	if !active {
		return errors.New("projection generation changed")
	}
	var digest string
	if err = tx.QueryRow(ctx, `SELECT digest FROM segmentations WHERE organization=$1 AND id=$2 AND version_id=$3`, org, seg.ID, seg.VersionID).Scan(&digest); err != nil {
		return err
	}
	if digest != content.SegmentationDigest(seg) {
		return content.ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO projection_coverage VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, org, seg.VersionID, g.ID, seg.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE record_versions SET baseline_ready=true,processing='idle',error_code='' WHERE organization=$1 AND id=$2`, org, seg.VersionID); err != nil {
		return err
	}
	if desired == seg.VersionID {
		if _, err = tx.Exec(ctx, `UPDATE records SET current_version_id=$3 WHERE organization=$1 AND id=$2`, org, recordID, seg.VersionID); err != nil {
			return err
		}
	}
	if !ready {
		if err = appendEvent(ctx, tx, eventInput{Organization: org, CorpusID: corpusID, Kind: "record.retrieval_ready", Resource: "record", ResourceID: recordID, MutationID: content.StableID("baseline", seg.VersionID, g.ID)}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (s ContentStore) Hydrate(ctx context.Context, scope corpus.Scope, c content.Candidate) (content.Hydrated, content.Blob, error) {
	var h content.Hydrated
	var blob content.Blob
	var corpusID string
	err := s.Pool.QueryRow(ctx, `SELECT r.id,v.id,r.corpus_id,sg.segmentation_id,sg.id,sg.part_key,sg.start_offset,sg.end_offset,sg.text_sha256,b.object_key,b.sha256,b.byte_length FROM segments sg JOIN record_versions v ON (v.organization,v.id)=(sg.organization,sg.version_id) JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) JOIN version_parts p ON (p.organization,p.version_id,p.part_key)=(sg.organization,sg.version_id,sg.part_key) JOIN content_blobs b ON (b.organization,b.blob_id)=(p.organization,p.blob_id) JOIN projection_coverage pc ON (pc.organization,pc.version_id,pc.segmentation_id)=(sg.organization,sg.version_id,sg.segmentation_id) JOIN projection_generations g ON g.id=pc.generation_id WHERE sg.organization=$1 AND sg.id=$2 AND g.id=$3 AND g.active AND r.current_version_id=v.id AND NOT r.withdrawn AND NOT v.quarantined AND v.baseline_ready AND NOT EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id)`, scope.Organization, c.SegmentID, c.GenerationID).Scan(&h.RecordID, &h.VersionID, &corpusID, &h.SegmentationID, &h.Segment.ID, &h.Segment.PartKey, &h.Segment.Start, &h.Segment.End, &h.TextSHA256, &blob.Key, &blob.SHA256, &blob.Size)
	if err == nil && !scope.Contains(corpusID) {
		err = corpus.ErrNotFound
	}
	if err == nil {
		var embeddingID, spaceID string
		e := s.Pool.QueryRow(ctx, `SELECT a.id,a.space_id FROM embedding_coverage ec JOIN embedding_artifacts a ON (a.organization,a.id)=(ec.organization,ec.artifact_id) JOIN projection_generations g ON g.id=ec.generation_id AND g.space_id=a.space_id WHERE ec.organization=$1 AND ec.segment_id=$2 AND ec.generation_id=$3`, scope.Organization, c.SegmentID, c.GenerationID).Scan(&embeddingID, &spaceID)
		if e == nil {
			h.EmbeddingID, h.SpaceID = embeddingID, spaceID
		} else if !errors.Is(e, pgx.ErrNoRows) {
			err = e
		}
	}
	h.Availability = content.Availability{State: "retrieval_ready", Current: true, Searchable: true}
	return h, blob, notFound(err)
}
func (s ContentStore) VersionStatus(ctx context.Context, org, id string) (content.Availability, content.Processing, string, error) {
	var a content.Availability
	var p content.Processing
	var code string
	var baseline, quarantine, withdrawn bool
	err := s.Pool.QueryRow(ctx, `SELECT v.baseline_ready,v.quarantined,r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id),coalesce(r.current_version_id=v.id,false),v.processing,v.error_code FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2`, org, id).Scan(&baseline, &quarantine, &withdrawn, &a.Current, &p.State, &code)
	a.State = "materialized"
	if p.State == "running" || p.State == "retrying" {
		a.State = "building_baseline"
	}
	if baseline {
		a.State = "retrieval_ready"
	}
	if quarantine {
		a.State = "quarantined"
	}
	a.Current = a.Current && !withdrawn && !quarantine
	a.Searchable = baseline && a.Current
	if baseline && !quarantine {
		if err = s.Pool.QueryRow(ctx, `SELECT enrichment_state,enrichment_error FROM record_versions WHERE organization=$1 AND id=$2`, org, id).Scan(&p.State, &code); err != nil {
			return a, p, code, err
		}
		if p.State != "idle" {
			p.Phase = "enrichment"
		}
		return a, p, code, err
	}
	if p.State != "idle" {
		p.Phase = "baseline"
	}
	return a, p, code, err
}
