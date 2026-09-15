package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/jackc/pgx/v5"
)

func (s ContentStore) Embedding(ctx context.Context, org, derivation string) (content.Embedding, error) {
	var e content.Embedding
	var b []byte
	err := s.Pool.QueryRow(ctx, `SELECT metadata FROM embedding_artifacts WHERE organization=$1 AND derivation_id=$2`, org, derivation).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &e)
	}
	return e, notFound(err)
}
func (s ContentStore) SaveEmbedding(ctx context.Context, e content.Embedding, space content.VectorSpace) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, e.Organization); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO vector_spaces VALUES($1,$2) ON CONFLICT DO NOTHING`, space.ID, space.Manifest); err != nil {
		return err
	}
	var same bool
	if err = tx.QueryRow(ctx, `SELECT manifest=$2::jsonb FROM vector_spaces WHERE id=$1`, space.ID, space.Manifest).Scan(&same); err != nil {
		return err
	}
	if !same {
		return content.ErrConflict
	}
	var prior string
	err = tx.QueryRow(ctx, `SELECT id FROM embedding_artifacts WHERE organization=$1 AND derivation_id=$2`, e.Organization, e.DerivationID).Scan(&prior)
	if err == nil {
		if prior != e.ID {
			return content.ErrConflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	b, _ := json.Marshal(e)
	_, err = tx.Exec(ctx, `INSERT INTO embedding_artifacts VALUES($1,$2,$3,$4,$5,$6)`, e.Organization, e.ID, e.DerivationID, e.SegmentID, e.SpaceID, b)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s ContentStore) EnrichmentProgress(ctx context.Context, org, id, state, code string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE record_versions SET enrichment_state=$3,enrichment_error=$4 WHERE organization=$1 AND id=$2 AND baseline_ready AND NOT quarantined AND enrichment_state!='idle'`, org, id, state, code)
	return err
}
func (s ContentStore) CommitEnrichment(ctx context.Context, org string, seg content.Segmentation, g content.Generation, artifacts []content.Embedding) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
	}
	var recordID, corpusID string
	var eligible bool
	err = tx.QueryRow(ctx, `SELECT r.id,r.corpus_id,v.baseline_ready AND NOT v.quarantined AND NOT r.withdrawn AND NOT EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id) FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2 FOR UPDATE OF r,v`, org, seg.VersionID).Scan(&recordID, &corpusID, &eligible)
	if err != nil {
		return err
	}
	if !eligible {
		return tx.Commit(ctx)
	}
	var active bool
	if err = tx.QueryRow(ctx, `SELECT active AND space_id=$2 FROM projection_generations WHERE id=$1 FOR SHARE`, g.ID, g.SpaceID).Scan(&active); err != nil {
		return err
	}
	if !active {
		return errors.New("generation changed")
	}
	if len(artifacts) != len(seg.Segments) {
		return content.ErrInvalid
	}
	for i, e := range artifacts {
		if e.Organization != org || e.SegmentID != seg.Segments[i].ID || e.SpaceID != g.SpaceID {
			return content.ErrInvalid
		}
		var stored string
		if err = tx.QueryRow(ctx, `SELECT id FROM embedding_artifacts WHERE organization=$1 AND derivation_id=$2 AND segment_id=$3 AND space_id=$4`, org, e.DerivationID, e.SegmentID, g.SpaceID).Scan(&stored); err != nil {
			return err
		}
		if stored != e.ID {
			return content.ErrConflict
		}
		if _, err = tx.Exec(ctx, `INSERT INTO embedding_coverage VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, org, e.SegmentID, g.ID, e.ID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE record_versions SET enrichment_state='idle',enrichment_error='' WHERE organization=$1 AND id=$2`, org, seg.VersionID); err != nil {
		return err
	}
	mutation := content.StableID("enrichment", seg.ID, g.ID)
	var emitted bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM change_events WHERE organization=$1 AND event_id=$2)`, org, content.StableID("event", org, "record.enrichment_available", "record", mutation)).Scan(&emitted); err != nil {
		return err
	}
	if !emitted {
		if err = appendEvent(ctx, tx, eventInput{Organization: org, CorpusID: corpusID, Kind: "record.enrichment_available", Resource: "record", ResourceID: recordID, MutationID: mutation}); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

var _ content.EmbeddingRepository = ContentStore{}
