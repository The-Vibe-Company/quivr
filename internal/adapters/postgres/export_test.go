package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

// RefreshFacetSnapshot refreshes now, as a fast read does in the background.
func (s RecordStore) RefreshFacetSnapshot(ctx context.Context, org, corpusID, generation string, declared []content.FacetField) error {
	return s.refreshFacetSnapshot(ctx, org, corpusID, generation, declared)
}

// SampleFacets counts a uniform sample, as a fast count does once an exact
// count overruns its attempt: a fixture cannot make a small Corpus slow.
func (s RecordStore) SampleFacets(ctx context.Context, org string, q content.FacetQuery) (content.FacetCounts, error) {
	return s.sampleFacets(ctx, org, q)
}

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
