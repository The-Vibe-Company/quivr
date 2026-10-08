package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ObserveQueueRecords runs the observation hook every record writer runs in
// its transaction, for fixtures that write canonical state directly.
func ObserveQueueRecords(ctx context.Context, pool *pgxpool.Pool, organization string, records ...string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	organizations := make([]string, len(records))
	for i := range records {
		organizations[i] = organization
	}
	if err = observeQueueRecords(ctx, tx, organizations, records); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
