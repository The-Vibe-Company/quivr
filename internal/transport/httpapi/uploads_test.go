package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

type refusedUploads struct {
	uploads.Store
	err error
}

func (store refusedUploads) Create(context.Context, string, string, uploads.Request, string, time.Time) (uploads.Meta, bool, error) {
	return uploads.Meta{}, false, store.err
}

func TestUploadRouteErrors(t *testing.T) {
	for _, row := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"conflict", uploads.ErrConflict, 409, "idempotency_conflict"},
		{"invalid upload", uploads.ErrInvalid, 422, "invalid_input"},
		{"invalid content", content.ErrInvalid, 422, "invalid_input"},
		{"storage outage", errors.New("private storage detail"), 503, "storage_unavailable"},
	} {
		t.Run(row.name, func(t *testing.T) {
			handler, err := httpapi.New(nil, content.Service{}, retrieval.Service{}, uploads.Service{Store: refusedUploads{err: fmt.Errorf("private detail: %w", row.err)}},
				map[string]corpus.Scope{adminKey: {Organization: "org_a", Actions: []string{"blobs:write"}, Corpora: []string{"*"}}}, catalogCursorKey)
			if err != nil {
				t.Fatal(err)
			}
			status, got := postJSON(t, checkedAPI(t, handler), "/v0/uploads", adminKey, map[string]any{"size_bytes": 1, "sha256": strings.Repeat("a", 64), "media_type": "text/plain"})
			if status != row.status || got["code"] != row.code || got["retryable"] != (row.status == 503) || strings.Contains(fmt.Sprint(got), "private") {
				t.Fatalf("want %d %s without private details, got %d %v", row.status, row.code, status, got)
			}
		})
	}
}
