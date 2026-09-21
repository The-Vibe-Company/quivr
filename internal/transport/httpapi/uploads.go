package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

func uploadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, corpus.ErrForbidden):
		failure(w, 403, "forbidden")
	case errors.Is(err, uploads.ErrNotFound):
		failure(w, 404, "not_found")
	case errors.Is(err, uploads.ErrConflict):
		failure(w, 409, "idempotency_conflict")
	case errors.Is(err, uploads.ErrInvalid), errors.Is(err, content.ErrInvalid):
		failure(w, 422, "invalid_schema")
	default:
		failure(w, 503, "storage_unavailable")
	}
}

func sessionToTransport(s uploads.Session) transport.Upload {
	out := transport.Upload{UploadId: s.ID, State: transport.UploadState(s.State)}
	if s.UploadURL != "" {
		url := s.UploadURL
		method := transport.UploadUploadMethod("PUT")
		headers := s.UploadHeaders
		expires := s.ExpiresAt
		out.UploadUrl = &url
		out.UploadMethod = &method
		out.UploadHeaders = &headers
		out.ExpiresAt = &expires
	}
	if s.BlobID != "" {
		id := s.BlobID
		out.BlobId = &id
	}
	if s.ErrorCode != "" {
		out.Error = &transport.Error{Code: s.ErrorCode, Message: strings.ReplaceAll(s.ErrorCode, "_", " "), Retryable: s.ErrorCode == "verification_unavailable"}
	}
	return out
}

func blobToTransport(b uploads.BlobInfo) transport.Blob {
	return transport.Blob{BlobId: b.ID, SizeBytes: int(b.SizeBytes), Sha256: b.SHA256, MediaType: b.MediaType}
}

func (a *API) uploadRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
	path := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(path) >= 2 && path[0] == "v0" && path[1] == "uploads" {
		switch {
		case len(path) == 2 && r.Method == http.MethodPost:
			if !scope.Allows("blobs:write") {
				failure(w, 403, "forbidden")
				return true
			}
			raw, ok := decodeRequest(w, r, a.uploadSchema)
			if !ok {
				return true
			}
			b, err := json.Marshal(raw)
			if err != nil {
				failure(w, 422, "invalid_schema")
				return true
			}
			var wire transport.UploadRequest
			if err = json.Unmarshal(b, &wire); err != nil {
				failure(w, 422, "invalid_schema")
				return true
			}
			req := uploads.Request{
				Key:       wire.Sha256 + ":" + strconv.FormatInt(int64(wire.SizeBytes), 10) + ":" + wire.MediaType,
				SizeBytes: int64(wire.SizeBytes),
				SHA256:    wire.Sha256,
				MediaType: wire.MediaType,
			}
			session, err := a.Uploads.Create(r.Context(), scope.Organization, req)
			if err != nil {
				uploadError(w, err)
			} else {
				send(w, 201, sessionToTransport(session))
			}
			return true
		case len(path) == 3 && r.Method == http.MethodGet:
			if !scope.Allows("blobs:read") {
				failure(w, 403, "forbidden")
				return true
			}
			session, err := a.Uploads.Get(r.Context(), scope.Organization, path[2])
			if err != nil {
				uploadError(w, err)
			} else {
				send(w, 200, sessionToTransport(session))
			}
			return true
		case len(path) == 4 && path[3] == "confirm" && r.Method == http.MethodPost:
			if !scope.Allows("blobs:write") {
				failure(w, 403, "forbidden")
				return true
			}
			session, err := a.Uploads.Confirm(r.Context(), scope.Organization, path[2])
			if err != nil {
				uploadError(w, err)
			} else {
				send(w, 202, sessionToTransport(session))
			}
			return true
		}
		return false
	}
	if len(path) == 3 && path[0] == "v0" && path[1] == "blobs" && r.Method == http.MethodGet {
		if !scope.Allows("blobs:read") {
			failure(w, 403, "forbidden")
			return true
		}
		blob, err := a.Uploads.Blob(r.Context(), scope.Organization, path[2])
		if err != nil {
			uploadError(w, err)
		} else {
			send(w, 200, blobToTransport(blob))
		}
		return true
	}
	return false
}
