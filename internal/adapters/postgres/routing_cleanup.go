package postgres

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
)

// Historical snapshots and dirty sets are work data, not immutable Operation
// results. Deleting a bounded page lets retries resume without a large sweep.
func (s RoutingStore) cleanupRouting(ctx context.Context, tx pgx.Tx, w *routingWork) (bool, error) {
	var active string
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT epoch FROM routing_switch_state WHERE singleton),'')`).Scan(&active); err != nil {
		return false, err
	}
	for _, epoch := range []string{w.id, w.previousEpoch} {
		if epoch == "" {
			continue
		}
		for _, table := range []string{"routing_dirty_records", "routing_dirty_corpora", "routing_dirty_generations", "routing_generation_settings", "routing_coverage_gaps"} {
			if epoch == active && (table == "routing_generation_settings" || table == "routing_coverage_gaps") {
				continue
			}
			q := fmt.Sprintf(`DELETE FROM %s WHERE ctid IN (SELECT ctid FROM %s WHERE epoch=$1 LIMIT $2)`, table, table)
			tag, err := tx.Exec(ctx, q, epoch, routingBatch)
			if err != nil {
				return false, err
			}
			if tag.RowsAffected() > 0 {
				return false, nil
			}
		}
	}
	_, err := tx.Exec(ctx, `UPDATE routing_operations SET phase='done' WHERE organization=$1 AND id=$2`, w.org, w.id)
	return err == nil, err
}
