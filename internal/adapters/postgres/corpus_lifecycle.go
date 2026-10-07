package postgres

import (
	"context"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

func (s Store) Archive(ctx context.Context, org, id string, archived bool) (corpus.Corpus, error) {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return corpus.Corpus{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	tag, err := tx.Exec(ctx, `UPDATE corpora SET archived=$3 WHERE organization=$1 AND id=$2 AND archived IS DISTINCT FROM $3`, org, id, archived)
	if err != nil {
		return corpus.Corpus{}, err
	}
	if archived && tag.RowsAffected() != 0 {
		// A suspended acquisition may already have completed in Temporal. Give
		// restoration a fresh workflow identity, without changing its checkpoint,
		// enabled state or next scheduled time. Repeated archive is a no-op.
		if _, err = tx.Exec(ctx, `UPDATE connector_instances SET run_sequence=run_sequence+1,lease_until=NULL WHERE organization=$1 AND corpus_id=$2`, org, id); err != nil {
			return corpus.Corpus{}, err
		}
	}
	c, err := readCorpus(ctx, tx, org, id)
	if err != nil {
		return corpus.Corpus{}, err
	}
	return c, tx.Commit(ctx)
}
func (s Store) Rename(ctx context.Context, org, id, name string) (corpus.Corpus, error) {
	if _, err := database(ctx, s.Pool).Exec(ctx, `UPDATE corpora SET name=$3 WHERE organization=$1 AND id=$2`, org, id, name); err != nil {
		return corpus.Corpus{}, err
	}
	return s.Read(ctx, org, id)
}
