package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// After publication, admit replacements in bounded batches. Both the already
// readable version and a different desired successor can need the new owner.
// Failed recovery never removes the readable outgoing projection.
func (s RoutingStore) recoverRouting(ctx context.Context, tx pgx.Tx, w *routingWork) (bool, error) {
	if err := lockProjectionRouting(ctx, tx); err != nil {
		return false, err
	}
	rows, err := tx.Query(ctx, `SELECT organization,id,current_version_id,desired_version_id FROM records WHERE (organization,id)>($1,$2) ORDER BY organization,id LIMIT $3`, w.cursorOrg, w.cursorRecord, routingBatch)
	if err != nil {
		return false, err
	}
	type record struct {
		org, id          string
		current, desired *string
	}
	batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (record, error) {
		var v record
		err := r.Scan(&v.org, &v.id, &v.current, &v.desired)
		return v, err
	})
	if err != nil {
		return false, err
	}
	for _, r := range batch {
		if r.current != nil {
			if err = queueServingProjection(ctx, tx, r.org, *r.current); err != nil {
				return false, err
			}
		}
		if r.desired != nil && (r.current == nil || *r.desired != *r.current) {
			if err = queueServingProjection(ctx, tx, r.org, *r.desired); err != nil {
				return false, err
			}
		}
		w.cursorOrg, w.cursorRecord = r.org, r.id
	}
	done := len(batch) < routingBatch
	phase := "recovery"
	if done {
		phase = "cleanup"
	}
	_, err = tx.Exec(ctx, `UPDATE routing_operations SET phase=$3,cursor_organization=$4,cursor_record=$5 WHERE organization=$1 AND id=$2`, w.org, w.id, phase, w.cursorOrg, w.cursorRecord)
	return false, err
}

// The new owner's complete vectors and preferred coverage become visible in
// the same publication transaction that drops the exact outgoing fallback.
func resolveRoutingGap(ctx context.Context, tx pgx.Tx, org, version, generation, owner string) error {
	_, err := tx.Exec(ctx, `DELETE FROM routing_coverage_gaps gap USING routing_switch_state st,records r
 WHERE st.singleton AND gap.epoch=st.epoch AND gap.organization=$1 AND gap.version_id=$2 AND gap.owner_plugin_id=$4
 AND (r.organization,r.id)=(gap.organization,gap.record_id)
 AND $3=`+routedGenerationSQL("r.organization", "r.corpus_id"), org, version, generation, owner)
	return err
}
