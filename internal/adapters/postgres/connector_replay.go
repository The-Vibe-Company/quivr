package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/jackc/pgx/v5"
)

var _ connectors.ReplayStore = ConnectorStore{}

// ReserveReplay serializes short reservations on the instance row so a
// multi-key replay reserves nothing and replicas agree on one winner.
func (s ConnectorStore) ReserveReplay(ctx context.Context, org, id, token string, keys []string, now, expires time.Time) (bool, error) {
	if len(keys) == 0 || len(keys) > 2 || token == "" || !expires.After(now) {
		return false, errors.New("invalid replay reservation")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var enabled bool
	err = tx.QueryRow(ctx, `SELECT enabled FROM connector_instances WHERE organization=$1 AND id=$2 FOR UPDATE`, org, id).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !enabled {
		return false, corpus.ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM connector_signature_replays WHERE organization=$1 AND connector_id=$2 AND expires_at<=$3`, org, id, now); err != nil {
		return false, err
	}
	var duplicate bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM connector_signature_replays WHERE organization=$1 AND connector_id=$2 AND fingerprint=ANY($3))`, org, id, keys).Scan(&duplicate); err != nil {
		return false, err
	}
	if duplicate {
		return false, tx.Commit(ctx)
	}
	for _, key := range keys {
		if _, err = tx.Exec(ctx, `INSERT INTO connector_signature_replays(organization,connector_id,fingerprint,reservation,expires_at) VALUES($1,$2,$3,$4,$5)`, org, id, key, token, expires); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}

// ReleaseReplay cannot erase a replacement reservation after expiry.
func (s ConnectorStore) ReleaseReplay(ctx context.Context, org, id, token string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM connector_signature_replays WHERE organization=$1 AND connector_id=$2 AND reservation=$3`, org, id, token)
	return err
}
