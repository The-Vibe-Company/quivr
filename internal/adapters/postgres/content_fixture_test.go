package postgres_test

import (
	"context"
	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5/pgxpool"
)

// contentStores wires real domain adapters for existing cross-area scenarios.
func contentStores(pool *pgxpool.Pool) fixtureContentStores {
	return fixtureContentStores{Pool: pool, objects: &objectMemory{objects: map[string][]byte{}},
		SubmissionStore:          postgres.SubmissionStore{Pool: pool},
		ReceiptStore:             postgres.ReceiptStore{Pool: pool},
		RecordStore:              postgres.RecordStore{Pool: pool},
		VersionStore:             postgres.VersionStore{Pool: pool},
		ActivityStore:            postgres.ActivityStore{Pool: pool},
		BackfillStore:            postgres.BackfillStore{Pool: pool},
		ChangeStore:              postgres.ChangeStore{Pool: pool},
		EmbeddingStore:           postgres.EmbeddingStore{Pool: pool},
		IngestionEvaluationStore: postgres.IngestionEvaluationStore{Pool: pool},
		MatchStore:               postgres.MatchStore{Pool: pool},
		MaterializationStore:     postgres.MaterializationStore{Pool: pool},
		MonitoringStore:          postgres.MonitoringStore{Pool: pool},
		NormalizationStore:       postgres.NormalizationStore{Pool: pool},
		OperationStore:           postgres.OperationStore{Pool: pool},
		ProjectionStore:          postgres.ProjectionStore{Pool: pool},
		PurgeStore:               postgres.PurgeStore{Pool: pool},
		QuarantineStore:          postgres.QuarantineStore{Pool: pool},
		RebuildStore:             postgres.RebuildStore{Pool: pool},
		ServingProjectionStore:   postgres.ServingProjectionStore{Pool: pool},
		SpaceStore:               postgres.SpaceStore{Pool: pool},
		UploadStore:              postgres.UploadStore{Pool: pool},
	}
}

type fixtureContentStores struct {
	Pool    *pgxpool.Pool
	objects *objectMemory
	postgres.SubmissionStore
	postgres.ReceiptStore
	postgres.RecordStore
	postgres.VersionStore
	postgres.ActivityStore
	postgres.BackfillStore
	postgres.ChangeStore
	postgres.EmbeddingStore
	postgres.IngestionEvaluationStore
	postgres.MatchStore
	postgres.MaterializationStore
	postgres.MonitoringStore
	postgres.NormalizationStore
	postgres.OperationStore
	postgres.ProjectionStore
	postgres.PurgeStore
	postgres.QuarantineStore
	postgres.RebuildStore
	postgres.ServingProjectionStore
	postgres.SpaceStore
	postgres.UploadStore
}

// SQL lifecycle fixtures store real packed artifacts through the same grouped
// boundary as production. Keep their objects across partial group additions.
func saveFixtureEmbedding(ctx context.Context, store fixtureContentStores, e *content.Embedding, space content.VectorSpace) error {
	var segmentation, version, recipe, corpus, sliceSHA string
	err := store.Pool.QueryRow(ctx, `SELECT sg.segmentation_id,sg.version_id,st.recipe,r.corpus_id,sg.text_sha256
 FROM segments sg JOIN segmentations st ON (st.organization,st.id)=(sg.organization,sg.segmentation_id)
 JOIN record_versions v ON (v.organization,v.id)=(sg.organization,sg.version_id)
 JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 WHERE sg.organization=$1 AND sg.id=$2`, e.Organization, e.SegmentID).Scan(&segmentation, &version, &recipe, &corpus, &sliceSHA)
	if err != nil {
		return err
	}
	stored, err := store.StoredSegmentation(ctx, e.Organization, version, recipe)
	if err != nil {
		return err
	}
	seg := content.Segmentation{ID: segmentation, VersionID: version, Recipe: recipe}
	var passage content.Segment
	for _, p := range stored.Segments {
		segment := content.Segment{ID: p.ID, PartKey: p.PartKey, Derivation: p.Derivation}
		seg.Segments = append(seg.Segments, segment)
		if p.ID == e.SegmentID {
			passage = segment
		}
	}
	producer := e.Producer
	if producer == "" {
		producer = recipe
	}
	input := content.EmbeddingInput(e.Organization, corpus, content.Version{ID: version}, seg, passage, space, producer)
	input.SliceSHA = sliceSHA
	dimensions := space.Dimensions
	if dimensions == 0 {
		dimensions = 1
	}
	vector := make([]float32, dimensions)
	vector[0] = 1
	service := content.Service{Embeddings: store, Blobs: store.objects}
	data, err := service.SaveEmbeddingGroup(ctx, seg, space, []content.EmbeddingData{{Artifact: input, Vector: vector}})
	if err != nil {
		return err
	}
	for _, d := range data {
		if d.Artifact.SegmentID == e.SegmentID {
			*e = d.Artifact
			return nil
		}
	}
	return content.ErrInvalid
}

func coverFixtureEmbedding(ctx context.Context, store fixtureContentStores, e content.Embedding, g content.Generation) error {
	mask := make([]byte, (e.File.RowCount+7)/8)
	content.MarkPresent(mask, e.Ordinal)
	_, err := store.Pool.Exec(ctx, `INSERT INTO compact_embedding_coverage(organization_id,file_id,generation_id,covered)
 SELECT organization_id,id,$2,$3 FROM embedding_files WHERE id=$1
 ON CONFLICT(organization_id,file_id,generation_id) DO UPDATE SET covered=set_bit(compact_embedding_coverage.covered,$4,1)`, e.File.ID, g.ID, mask, e.Ordinal)
	return err
}
