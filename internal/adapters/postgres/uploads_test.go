package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUploadSessionsReconcileAndScope(t *testing.T) {
	path := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if path == "" {
		t.Skip("real PostgreSQL suite runs inside make verify")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		DatabaseURL string `json:"database_url"`
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := contentStores(pool)
	org := "adapter-uploads"
	digest := strings.Repeat("a", 64)
	req := uploads.Request{Key: "request-key", SizeBytes: 12, SHA256: digest, MediaType: "text/plain"}

	meta, fresh, err := store.Create(ctx, org, "upload_1", req, "adapter-uploads/object", time.Now().Add(time.Minute))
	if err != nil || !fresh {
		t.Fatal(err, fresh)
	}
	replay, fresh, err := store.Create(ctx, org, "upload_1", req, "adapter-uploads/object", time.Now().Add(time.Minute))
	if err != nil || fresh || replay.ID != meta.ID {
		t.Fatal("replay did not converge", replay, fresh, err)
	}
	changed := req
	changed.SHA256 = strings.Repeat("b", 64)
	if _, _, err = store.Create(ctx, org, "upload_1", changed, "adapter-uploads/object", time.Now().Add(time.Minute)); !errors.Is(err, uploads.ErrConflict) {
		t.Fatal("conflicting expectation was accepted", err)
	}
	if err = store.SetState(ctx, org, meta.ID, "verified", "blob_1", ""); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveBlob(ctx, org, "blob_1", "adapter-uploads/object", digest, 12, "text/plain"); err != nil {
		t.Fatal(err)
	}
	blob, err := store.Blob(ctx, org, "blob_1")
	if err != nil || blob.SHA256 != digest || blob.SizeBytes != 12 {
		t.Fatal(blob, err)
	}
	if _, err = store.Blob(ctx, "other-org", "blob_1"); !errors.Is(err, uploads.ErrNotFound) {
		t.Fatal("Blob crossed Organization")
	}
	verified, err := store.VerifiedBlob(ctx, org, "blob_1")
	if err != nil || verified.Blob.SHA256 != digest || verified.MediaType != "text/plain" {
		t.Fatal(verified, err)
	}
	if _, err = store.VerifiedBlob(ctx, org, "absent"); !errors.Is(err, content.ErrUnverifiedBlob) {
		t.Fatal("absent Blob resolved", err)
	}
}
