package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Capture a rebuild's scope once in the background, including operations
// accepted by preceding binaries. Progress already lives on the operation.
// Counting is outside canonical writer transactions; the conditional update
// commits scope assignments before snapshot aggregation. No gap/coverage
// anti-join or ingestion write maintenance is needed for this approximate signal.
func initializeQueueOperationScopes(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT organization,id,corpus_id FROM operations
 WHERE kind IN ('projection_rebuild','retrieval_configuration') AND state IN ('queued','running')
 AND NOT counters ? 'versions_in_scope'`)
	if err != nil {
		return err
	}
	type scope struct{ organization, id, corpus string }
	operations, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (scope, error) {
		var op scope
		err := row.Scan(&op.organization, &op.id, &op.corpus)
		return op, err
	})
	if err != nil {
		return err
	}
	// Count each corpus before taking any operation row locks. Several pending
	// operations can share a scope; their initial estimate needs only one scan.
	type corpusKey struct{ organization, corpus string }
	sizes := make(map[corpusKey]int64)
	for _, op := range operations {
		key := corpusKey{op.organization, op.corpus}
		if _, counted := sizes[key]; counted {
			continue
		}
		var size int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM records r
 JOIN record_versions v ON (v.organization,v.id)=(r.organization,r.current_version_id)
 WHERE r.organization=$1 AND r.corpus_id=$2 AND `+eligibleVersionSQL, op.organization, op.corpus).Scan(&size); err != nil {
			return err
		}
		sizes[key] = size
	}
	for _, op := range operations {
		if _, err := tx.Exec(ctx, `UPDATE operations SET counters=counters || jsonb_build_object('versions_in_scope',$3::bigint)
 WHERE organization=$1 AND id=$2 AND state IN ('queued','running') AND NOT counters ? 'versions_in_scope'`, op.organization, op.id, sizes[corpusKey{op.organization, op.corpus}]); err != nil {
			return err
		}
	}
	return nil
}
