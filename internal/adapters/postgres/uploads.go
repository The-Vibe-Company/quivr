package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
	"github.com/jackc/pgx/v5"
)

// Create stores the session or converges on the same request identity.
func (s ContentStore) Create(ctx context.Context, org, id string, req uploads.Request, objectKey string, expires time.Time) (uploads.Meta, bool, error) {
	canonical, err := json.Marshal(req)
	if err != nil {
		return uploads.Meta{}, false, err
	}
	tag, err := s.Pool.Exec(ctx, `INSERT INTO uploads(organization,id,request_key,canonical_request,sha256,byte_length,media_type,object_key,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`, org, id, req.Key, canonical, req.SHA256, req.SizeBytes, req.MediaType, objectKey, expires)
	if err != nil {
		return uploads.Meta{}, false, err
	}
	existing, err := s.Get(ctx, org, id)
	if err != nil {
		return uploads.Meta{}, false, err
	}
	if tag.RowsAffected() == 0 {
		var previous []byte
		if err = s.Pool.QueryRow(ctx, `SELECT canonical_request FROM uploads WHERE organization=$1 AND request_key=$2`, org, req.Key).Scan(&previous); err != nil {
			return uploads.Meta{}, false, err
		}
		if !bytes.Equal(previous, canonical) {
			return uploads.Meta{}, false, uploads.ErrConflict
		}
		existing, err = s.getByRequestKey(ctx, org, req.Key)
		if err != nil {
			return uploads.Meta{}, false, err
		}
		return existing, false, nil
	}
	return existing, true, nil
}

func (s ContentStore) getByRequestKey(ctx context.Context, org, key string) (uploads.Meta, error) {
	return s.scanUpload(s.Pool.QueryRow(ctx, `SELECT id,state,sha256,byte_length,media_type,object_key,coalesce(blob_id,''),error_code,expires_at FROM uploads WHERE organization=$1 AND request_key=$2`, org, key))
}

func (s ContentStore) Get(ctx context.Context, org, id string) (uploads.Meta, error) {
	return s.scanUpload(s.Pool.QueryRow(ctx, `SELECT id,state,sha256,byte_length,media_type,object_key,coalesce(blob_id,''),error_code,expires_at FROM uploads WHERE organization=$1 AND id=$2`, org, id))
}

func (s ContentStore) scanUpload(row pgx.Row) (uploads.Meta, error) {
	var m uploads.Meta
	err := row.Scan(&m.ID, &m.State, &m.SHA256, &m.SizeBytes, &m.MediaType, &m.ObjectKey, &m.BlobID, &m.ErrorCode, &m.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, uploads.ErrNotFound
	}
	return m, err
}

func (s ContentStore) SetState(ctx context.Context, org, id, state, blobID, code string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE uploads SET state=$3,blob_id=nullif($4,''),error_code=$5 WHERE organization=$1 AND id=$2`, org, id, state, blobID, code)
	return err
}

func (s ContentStore) SaveBlob(ctx context.Context, org, id, objectKey, sha256 string, size int64, mediaType string) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO verified_blobs(organization,blob_id,object_key,sha256,byte_length,media_type) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, org, id, objectKey, sha256, size, mediaType)
	return err
}

func (s ContentStore) Blob(ctx context.Context, org, id string) (uploads.Meta, error) {
	var m uploads.Meta
	err := s.Pool.QueryRow(ctx, `SELECT blob_id,object_key,sha256,byte_length,media_type FROM verified_blobs WHERE organization=$1 AND blob_id=$2`, org, id).Scan(&m.BlobID, &m.ObjectKey, &m.SHA256, &m.SizeBytes, &m.MediaType)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, uploads.ErrNotFound
	}
	m.ID = id
	m.State = "verified"
	return m, err
}

// VerifiedBlob resolves an Organization-scoped Blob reference for ingestion.
func (s ContentStore) VerifiedBlob(ctx context.Context, org, id string) (content.VerifiedBlob, error) {
	var b content.VerifiedBlob
	err := s.Pool.QueryRow(ctx, `SELECT blob_id,object_key,sha256,byte_length,media_type FROM verified_blobs WHERE organization=$1 AND blob_id=$2`, org, id).Scan(&b.ID, &b.Blob.Key, &b.Blob.SHA256, &b.Blob.Size, &b.MediaType)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, content.ErrUnverifiedBlob
	}
	return b, err
}
