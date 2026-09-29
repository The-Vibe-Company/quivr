package postgres

import (
	"context"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/jackc/pgx/v5"
)

// Normalized reads the recorded normalizer output of a Record Version.
func (s ContentStore) Normalized(ctx context.Context, org, versionID string) (content.Normalized, bool, error) {
	var n content.Normalized
	p := &n.Provenance
	err := s.Pool.QueryRow(ctx, `SELECT idempotency_key,invocation_id,plugin_id,plugin_version,plugin_api,contribution,input_sha256,manifest_key,manifest_sha256,manifest_size FROM normalizations WHERE organization=$1 AND version_id=$2`, org, versionID).
		Scan(&p.IdempotencyKey, &p.InvocationID, &p.PluginID, &p.PluginVersion, &p.PluginAPI, &p.Contribution, &p.InputSHA256, &n.Manifest.Key, &n.Manifest.SHA256, &n.Manifest.Size)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.Normalized{}, false, nil
	}
	return n, err == nil, err
}

// SaveNormalized records the first normalizer output of a Record Version and
// returns the recorded one: a concurrent or repeated attempt never overwrites it.
func (s ContentStore) SaveNormalized(ctx context.Context, org, versionID string, n content.Normalized) (content.Normalized, error) {
	p := n.Provenance
	_, err := s.Pool.Exec(ctx, `INSERT INTO normalizations(organization,version_id,idempotency_key,invocation_id,plugin_id,plugin_version,plugin_api,contribution,input_sha256,manifest_key,manifest_sha256,manifest_size) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT DO NOTHING`,
		org, versionID, p.IdempotencyKey, p.InvocationID, p.PluginID, p.PluginVersion, p.PluginAPI, p.Contribution, p.InputSHA256, n.Manifest.Key, n.Manifest.SHA256, n.Manifest.Size)
	if err != nil {
		return content.Normalized{}, err
	}
	stored, found, err := s.Normalized(ctx, org, versionID)
	if err == nil && !found {
		err = errors.New("normalization record missing after insert")
	}
	return stored, err
}
