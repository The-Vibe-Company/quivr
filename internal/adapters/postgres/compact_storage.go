package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InitializeStorage selects compact defaults only for an installation without
// accepted work. Expanded installations retain the previous writer's format
// until the operator activates compact writes after draining older binaries.
func InitializeStorage(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `INSERT INTO storage_state(singleton,compact) SELECT true,NOT EXISTS(SELECT 1 FROM ingestion_receipts) ON CONFLICT DO NOTHING`)
	return err
}

// ActivateCompactStorage is an irreversible write-format boundary. The operator
// must retire older API/workers first. Routing precedes this fence, just as it
// precedes the organization journal in regular writers.
func ActivateCompactStorage(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, projectionRoutingLock); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO storage_state(singleton,compact,activated_at) VALUES(true,true,now()) ON CONFLICT(singleton) DO UPDATE SET compact=true,activated_at=coalesce(storage_state.activated_at,now())`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func compactWrites(ctx context.Context, tx pgx.Tx) (bool, error) {
	var compact bool
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT compact FROM storage_state WHERE singleton FOR SHARE),false)`).Scan(&compact)
	return compact, err
}

func requestMatches(previous, digest, canonical []byte) bool {
	sum := sha256.Sum256(canonical)
	if len(digest) > 0 {
		return bytes.Equal(digest, sum[:])
	}
	return bytes.Equal(previous, canonical)
}

// Full request identity is hashed before removing transport-only fields from
// the one durable work input. Required source/content/provenance remain intact.
func importRequestStorage(canonical []byte, compact, retainDetail bool) (copy, receipt, execution []byte) {
	if !compact || retainDetail {
		return canonical, canonical, canonical
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(canonical, &fields)
	delete(fields, "idempotency_key")
	delete(fields, "source_revision")
	delete(fields, "source_position")
	execution, _ = json.Marshal(fields)
	return []byte{}, []byte("{}"), execution
}
