package postgres_test

import (
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// contentStores wires real domain adapters for existing cross-area scenarios.
func contentStores(pool *pgxpool.Pool) fixtureContentStores {
	return fixtureContentStores{Pool: pool,
		ContentStore:    postgres.ContentStore{Pool: pool},
		SubmissionStore: postgres.SubmissionStore{Pool: pool},
		ReceiptStore:    postgres.ReceiptStore{Pool: pool},
		RecordStore:     postgres.RecordStore{Pool: pool},
		VersionStore:    postgres.VersionStore{Pool: pool},
	}
}

type fixtureContentStores struct {
	Pool *pgxpool.Pool
	postgres.ContentStore
	postgres.SubmissionStore
	postgres.ReceiptStore
	postgres.RecordStore
	postgres.VersionStore
}
