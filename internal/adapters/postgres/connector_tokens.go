package postgres

import (
	"context"
	"errors"

	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5"
)

const tokenColumns = `id,prefix,created_at,rotated_at,revoked_at,valid_until`

func scanInstanceToken(row pgx.Row) (connectors.TokenInfo, error) {
	var token connectors.TokenInfo
	err := row.Scan(&token.ID, &token.Prefix, &token.CreatedAt, &token.RotatedAt, &token.RevokedAt, &token.ValidUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		err = corpus.ErrNotFound
	}
	return token, err
}

// Serialize issuance/rotation with disable and with competing rotations.
func lockTokenInstance(ctx context.Context, tx pgx.Tx, org, id string) error {
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT enabled FROM connector_instances WHERE organization=$1 AND id=$2 FOR UPDATE`, org, id).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return corpus.ErrNotFound
	}
	if err != nil {
		return err
	}
	if !enabled {
		return connectors.ErrDisabled
	}
	return nil
}

func insertInstanceToken(ctx context.Context, tx pgx.Tx, org, id string, n connectors.TokenDeposit) (connectors.TokenInfo, error) {
	return scanInstanceToken(tx.QueryRow(ctx, `INSERT INTO connector_instance_tokens(organization,connector_id,id,hash,prefix) VALUES($1,$2,$3,$4,$5) RETURNING `+tokenColumns, org, id, n.ID, n.Hash, n.Prefix))
}

func (s ConnectorStore) CreateInstanceToken(ctx context.Context, org, id string, n connectors.TokenDeposit) (connectors.TokenInfo, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return connectors.TokenInfo{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockTokenInstance(ctx, tx, org, id); err != nil {
		return connectors.TokenInfo{}, err
	}
	token, err := insertInstanceToken(ctx, tx, org, id, n)
	if err != nil {
		return connectors.TokenInfo{}, err
	}
	return token, tx.Commit(ctx)
}

func (s ConnectorStore) ListInstanceTokens(ctx context.Context, org, id, after string, limit int) ([]connectors.TokenInfo, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+tokenColumns+` FROM connector_instance_tokens WHERE organization=$1 AND connector_id=$2 AND id>$3 ORDER BY id LIMIT $4`, org, id, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := []connectors.TokenInfo{}
	for rows.Next() {
		token, err := scanInstanceToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

func (s ConnectorStore) RotateInstanceToken(ctx context.Context, org, id, tokenID string, n connectors.TokenDeposit) (connectors.TokenInfo, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return connectors.TokenInfo{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockTokenInstance(ctx, tx, org, id); err != nil {
		return connectors.TokenInfo{}, err
	}
	old, err := scanInstanceToken(tx.QueryRow(ctx, `SELECT `+tokenColumns+` FROM connector_instance_tokens WHERE organization=$1 AND connector_id=$2 AND id=$3 FOR UPDATE`, org, id, tokenID))
	if err != nil {
		return connectors.TokenInfo{}, err
	}
	if old.RevokedAt != nil || old.RotatedAt != nil {
		return connectors.TokenInfo{}, connectors.ErrTokenInactive
	}
	_, err = tx.Exec(ctx, `UPDATE connector_instance_tokens SET rotated_at=statement_timestamp(),valid_until=statement_timestamp()+($4 * interval '1 second') WHERE organization=$1 AND connector_id=$2 AND id=$3`, org, id, tokenID, int64(connectors.TokenRotationOverlap.Seconds()))
	if err != nil {
		return connectors.TokenInfo{}, err
	}
	token, err := insertInstanceToken(ctx, tx, org, id, n)
	if err != nil {
		return connectors.TokenInfo{}, err
	}
	return token, tx.Commit(ctx)
}

func (s ConnectorStore) RevokeInstanceToken(ctx context.Context, org, id, tokenID string) (connectors.TokenInfo, error) {
	return scanInstanceToken(s.Pool.QueryRow(ctx, `UPDATE connector_instance_tokens SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE organization=$1 AND connector_id=$2 AND id=$3 RETURNING `+tokenColumns, org, id, tokenID))
}

func (s ConnectorStore) LoadInstanceTokenHash(ctx context.Context, org, id, tokenID string) ([]byte, error) {
	var hash []byte
	err := s.Pool.QueryRow(ctx, `SELECT t.hash FROM connector_instance_tokens t JOIN connector_instances c ON c.organization=t.organization AND c.id=t.connector_id WHERE t.organization=$1 AND t.connector_id=$2 AND t.id=$3 AND c.enabled AND t.revoked_at IS NULL AND (t.valid_until IS NULL OR t.valid_until>clock_timestamp())`, org, id, tokenID).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		err = corpus.ErrNotFound
	}
	return hash, err
}
