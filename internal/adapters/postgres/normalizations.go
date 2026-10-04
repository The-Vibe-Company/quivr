package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const normalizationColumns = `outcome,idempotency_key,invocation_id,plugin_id,plugin_version,plugin_api,contribution,input_sha256,input_blob_id,coalesce(manifest_key,''),coalesce(manifest_sha256,''),coalesce(manifest_size,0),failure_code,failure_message,failure_retryable,coalesce(failure_plan,''),coalesce(conflict_invocation_id,''),coalesce(conflict_manifest_sha256,''),extensions`

// scanNormalization reads one row of normalizationColumns.
func scanNormalization(row pgx.Row) (content.Normalized, error) {
	var n content.Normalized
	p := &n.Provenance
	var failure content.NormalizationFailure
	var conflict content.NormalizationConflict
	var extensions []byte
	err := row.Scan(&n.Outcome, &p.IdempotencyKey, &p.InvocationID, &p.PluginID, &p.PluginVersion, &p.PluginAPI, &p.Contribution, &p.InputSHA256, &n.InputBlobID,
		&n.Manifest.Key, &n.Manifest.SHA256, &n.Manifest.Size, &failure.Code, &failure.Message, &failure.Retryable, &failure.Plan, &conflict.InvocationID, &conflict.ManifestSHA256, &extensions)
	if err != nil {
		return n, err
	}
	if err = json.Unmarshal(extensions, &n.Extensions); err != nil {
		return n, err
	}
	if failure.Code != "" {
		n.Failure = &failure
	}
	if n.Outcome == content.OutcomeFallback && n.Failure != nil {
		p.Fallback = &content.NormalizationFallback{Code: failure.Code, Message: failure.Message}
	}
	if conflict.InvocationID != "" {
		n.Conflict = &conflict
	}
	return n, nil
}

// Normalized reads the recorded normalization outcome of a Record Version.
func (s NormalizationStore) Normalized(ctx context.Context, org, versionID string) (content.Normalized, bool, error) {
	n, err := scanNormalization(s.Pool.QueryRow(ctx, `SELECT `+normalizationColumns+` FROM normalizations WHERE organization=$1 AND version_id=$2`, org, versionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return content.Normalized{}, false, nil
	}
	return n, err == nil, err
}

// SaveNormalized records the first normalization outcome of a Record Version
// and returns the recorded one: a concurrent or repeated attempt never
// overwrites it.
func (s NormalizationStore) SaveNormalized(ctx context.Context, org, versionID string, n content.Normalized) (content.Normalized, error) {
	p := n.Provenance
	outcome := n.Outcome
	if outcome == "" {
		outcome = content.OutcomeNormalized
	}
	var failure content.NormalizationFailure
	if n.Failure != nil {
		failure = *n.Failure
	}
	var key, sha *string
	var size *int64
	if outcome != content.OutcomeFailed {
		key, sha, size = &n.Manifest.Key, &n.Manifest.SHA256, &n.Manifest.Size
	}
	extensions := n.Extensions
	if extensions == nil {
		extensions = content.Extensions{}
	}
	extensionsJSON, err := json.Marshal(extensions)
	if err != nil {
		return content.Normalized{}, err
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO normalizations(organization,version_id,outcome,idempotency_key,invocation_id,plugin_id,plugin_version,plugin_api,contribution,input_sha256,input_blob_id,manifest_key,manifest_sha256,manifest_size,failure_code,failure_message,failure_retryable,extensions,failure_plan) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,NULLIF($19,'')) ON CONFLICT DO NOTHING`,
		org, versionID, outcome, p.IdempotencyKey, p.InvocationID, p.PluginID, p.PluginVersion, p.PluginAPI, p.Contribution, p.InputSHA256, n.InputBlobID, key, sha, size, failure.Code, failure.Message, failure.Retryable, extensionsJSON, failure.Plan)
	if err != nil {
		return content.Normalized{}, err
	}
	stored, found, err := s.Normalized(ctx, org, versionID)
	if err == nil && !found {
		err = errors.New("normalization record missing after insert")
	}
	return stored, err
}

// RecordConflict records the first divergent output of a recorded
// normalization. The recorded outcome and Manifest never change.
func (s NormalizationStore) RecordConflict(ctx context.Context, org, versionID string, c content.NormalizationConflict) error {
	_, err := s.Pool.Exec(ctx, `UPDATE normalizations SET conflict_invocation_id=$3,conflict_manifest_sha256=$4 WHERE organization=$1 AND version_id=$2 AND conflict_invocation_id IS NULL`, org, versionID, c.InvocationID, c.ManifestSHA256)
	return err
}

// CountAttempt counts one budgeted normalization failure of a Version.
func (s NormalizationStore) CountAttempt(ctx context.Context, org, versionID, code, invocationID string) (int, error) {
	var attempts int
	err := s.Pool.QueryRow(ctx, `INSERT INTO normalization_attempts(organization,version_id,attempts,last_code,last_invocation_id) VALUES($1,$2,1,$3,$4)
ON CONFLICT(organization,version_id) DO UPDATE SET attempts=normalization_attempts.attempts+1,last_code=excluded.last_code,last_invocation_id=excluded.last_invocation_id,updated_at=now() RETURNING attempts`, org, versionID, code, invocationID).Scan(&attempts)
	return attempts, err
}

// Superseded reports whether a Record is withdrawn, or whether it desires
// another accepted revision than versionID: such a Version never becomes
// current, so it is not worth an external normalization.
func (s NormalizationStore) Superseded(ctx context.Context, org, recordID, versionID string) (bool, bool, error) {
	var withdrawn bool
	var desired string
	err := s.Pool.QueryRow(ctx, `SELECT `+recordGoneSQL+`,coalesce(r.desired_version_id,'') FROM records r WHERE r.organization=$1 AND r.id=$2`, org, recordID).Scan(&withdrawn, &desired)
	if err != nil {
		return false, false, err
	}
	return withdrawn, desired != "" && desired != versionID, nil
}

// NormalizationStore persists normalizations state.
type NormalizationStore struct{ Pool *pgxpool.Pool }
