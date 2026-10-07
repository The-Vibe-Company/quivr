package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const normalizationMetadataColumns = `outcome,idempotency_key,invocation_id,plugin_id,plugin_version,plugin_api,contribution,input_sha256,input_blob_id,coalesce(manifest_key,''),coalesce(manifest_sha256,''),coalesce(manifest_size,0),failure_code,failure_message,failure_retryable,coalesce(failure_plan,''),coalesce(conflict_invocation_id,''),coalesce(conflict_manifest_sha256,'')`
const normalizationColumns = normalizationMetadataColumns + `,extensions,coalesce(outcome_key,''),coalesce(outcome_sha256,''),coalesce(outcome_size,0)`

// scanNormalization reads one row of normalizationColumns.
func (s NormalizationStore) scanNormalization(ctx context.Context, row pgx.Row) (content.Normalized, error) {
	var blob content.Blob
	var n content.Normalized
	p := &n.Provenance
	var failure content.NormalizationFailure
	var conflict content.NormalizationConflict
	var extensions []byte
	err := row.Scan(&n.Outcome, &p.IdempotencyKey, &p.InvocationID, &p.PluginID, &p.PluginVersion, &p.PluginAPI, &p.Contribution, &p.InputSHA256, &n.InputBlobID,
		&n.Manifest.Key, &n.Manifest.SHA256, &n.Manifest.Size, &failure.Code, &failure.Message, &failure.Retryable, &failure.Plan, &conflict.InvocationID, &conflict.ManifestSHA256, &extensions, &blob.Key, &blob.SHA256, &blob.Size)
	if err != nil {
		return n, err
	}
	if blob.Key != "" {
		if s.Blobs == nil {
			return n, errors.New("normalization outcome storage unavailable")
		}
		raw, err := s.Blobs.Read(ctx, blob)
		if err != nil {
			return n, err
		}
		if int64(len(raw)) != blob.Size || content.Hash(raw) != blob.SHA256 {
			return n, content.ErrArtifactCorrupt
		}
		if err = json.Unmarshal(raw, &n); err != nil {
			return n, content.ErrArtifactCorrupt
		}
		p = &n.Provenance
	} else if err = json.Unmarshal(extensions, &n.Extensions); err != nil {
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
	n, err := s.scanNormalization(ctx, s.Pool.QueryRow(ctx, `SELECT `+normalizationColumns+` FROM normalizations WHERE organization=$1 AND version_id=$2`, org, versionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return content.Normalized{}, false, nil
	}
	return n, err == nil, err
}

// Public diagnostics need only operational SQL fields, not the bulky produced
// extensions. Keep this read independent of object-storage availability.
func (s NormalizationStore) diagnosticNormalization(ctx context.Context, org, versionID string) (content.Normalized, bool, error) {
	n, err := s.scanNormalization(ctx, s.Pool.QueryRow(ctx, `SELECT `+normalizationMetadataColumns+`, '{}'::jsonb, '', '', 0 FROM normalizations WHERE organization=$1 AND version_id=$2`, org, versionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return content.Normalized{}, false, nil
	}
	return n, err == nil, err
}

// SaveNormalized records the first normalization outcome of a Record Version
// and returns the recorded one: a concurrent or repeated attempt never
// overwrites it.
func (s NormalizationStore) SaveNormalized(ctx context.Context, org, versionID string, n content.Normalized) (content.Normalized, error) {
	if n.Outcome == "" {
		n.Outcome = content.OutcomeNormalized
	}
	// A one-way activation may land during candidate preparation. Recheck under
	// the shared routing fence and retry preparation before taking any DB locks.
	for attempt := 0; attempt < 2; attempt++ {
		candidate := n
		var blob content.Blob
		compact := false
		if s.Blobs != nil && !s.RetainImportAuditDetail {
			var err error
			compact, err = (EmbeddingStore{Pool: s.Pool}).CompactStorage(ctx)
			if err != nil {
				return content.Normalized{}, err
			}
			if compact {
				candidate = compactNormalization(n)
				raw, err := json.Marshal(candidate)
				if err != nil {
					return content.Normalized{}, err
				}
				blob, err = s.Blobs.Put(ctx, org, raw)
				if err != nil {
					return content.Normalized{}, err
				}
			}
		}
		written, err := s.saveNormalized(ctx, org, versionID, candidate, blob, compact)
		if err != nil {
			return content.Normalized{}, err
		}
		if !written {
			continue
		}
		stored, found, err := s.Normalized(ctx, org, versionID)
		if err == nil && !found {
			err = errors.New("normalization record missing after insert")
		}
		return stored, err
	}
	return content.Normalized{}, content.ErrConflict
}

func (s NormalizationStore) saveNormalized(ctx context.Context, org, versionID string, n content.Normalized, blob content.Blob, compact bool) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if err = lockProcessingVersion(ctx, tx, org, versionID); err != nil {
		return false, err
	}
	active, err := compactWrites(ctx, tx)
	if err != nil {
		return false, err
	}
	if compact != (active && s.Blobs != nil && !s.RetainImportAuditDetail) {
		return false, nil
	}

	p := n.Provenance
	outcome := n.Outcome
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
	if compact {
		extensions = content.Extensions{}
	}
	if extensions == nil {
		extensions = content.Extensions{}
	}
	extensionsJSON, err := json.Marshal(extensions)
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO normalizations(organization,version_id,outcome,idempotency_key,invocation_id,plugin_id,plugin_version,plugin_api,contribution,input_sha256,input_blob_id,manifest_key,manifest_sha256,manifest_size,failure_code,failure_message,failure_retryable,extensions,failure_plan,outcome_key,outcome_sha256,outcome_size) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,NULLIF($19,''),NULLIF($20,''),NULLIF($21,''),NULLIF($22,0)) ON CONFLICT DO NOTHING`,
		org, versionID, outcome, p.IdempotencyKey, p.InvocationID, p.PluginID, p.PluginVersion, p.PluginAPI, p.Contribution, p.InputSHA256, n.InputBlobID, key, sha, size, failure.Code, failure.Message, failure.Retryable, extensionsJSON, failure.Plan, blob.Key, blob.SHA256, blob.Size)
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
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
type NormalizationStore struct {
	Pool                    *pgxpool.Pool
	Blobs                   content.Blobs
	RetainImportAuditDetail bool
}

// Operational outcomes and produced extensions remain durable. Successful call
// invocation IDs are optional audit detail; failures retain their diagnostic ID.
// The deterministic invocation digest remains necessary for conflict detection.
func compactNormalization(n content.Normalized) content.Normalized {
	if n.Failure == nil {
		n.Provenance.InvocationID = ""
	}
	return n
}
