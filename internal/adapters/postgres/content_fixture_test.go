package postgres_test

import (
	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// contentStores wires real domain adapters for existing cross-area scenarios.
func contentStores(pool *pgxpool.Pool) fixtureContentStores {
	return fixtureContentStores{Pool: pool,
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
	Pool *pgxpool.Pool
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
