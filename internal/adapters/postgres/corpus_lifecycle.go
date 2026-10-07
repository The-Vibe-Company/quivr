package postgres

import (
	"context"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

func (s Store) Archive(ctx context.Context, org, id string, archived bool) (corpus.Corpus, error) {
	if _, err := database(ctx, s.Pool).Exec(ctx, `UPDATE corpora SET archived=$3 WHERE organization=$1 AND id=$2`, org, id, archived); err != nil {
		return corpus.Corpus{}, err
	}
	return s.Read(ctx, org, id)
}
func (s Store) Rename(ctx context.Context, org, id, name string) (corpus.Corpus, error) {
	if _, err := database(ctx, s.Pool).Exec(ctx, `UPDATE corpora SET name=$3 WHERE organization=$1 AND id=$2`, org, id, name); err != nil {
		return corpus.Corpus{}, err
	}
	return s.Read(ctx, org, id)
}
