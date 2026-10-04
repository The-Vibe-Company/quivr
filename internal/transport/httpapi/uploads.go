package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

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

func (a *API) handleCreateUpload(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	var wire transport.UploadRequest
	session, err := a.Uploads.CreateScoped(r.Context(), scope, uploads.Request{}, func() (uploads.Request, error) {
		raw, ok := decodeRequest(w, r, a.schemas["UploadRequest"])
		if !ok {
			return uploads.Request{}, errResponseWritten
		}
		b, err := json.Marshal(raw)
		if err != nil {
			writeError(w, publicerr.InvalidSchema, nil)
			return uploads.Request{}, errResponseWritten
		}
		if err = json.Unmarshal(b, &wire); err != nil {
			writeError(w, publicerr.InvalidSchema, nil)
			return uploads.Request{}, errResponseWritten
		}
		req := uploads.Request{
			Key:       wire.Sha256 + ":" + strconv.FormatInt(int64(wire.SizeBytes), 10) + ":" + wire.MediaType,
			SizeBytes: int64(wire.SizeBytes),
			SHA256:    wire.Sha256,
			MediaType: wire.MediaType,
		}

		return req, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
	} else {
		send(w, 201, sessionToTransport(session))
	}
}

func (a *API) handleGetUpload(w http.ResponseWriter, r *http.Request, scope corpus.Scope, uploadID string) {
	session, err := a.Uploads.GetScoped(r.Context(), scope, uploadID)
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
	} else {
		send(w, 200, sessionToTransport(session))
	}
}

func (a *API) handleConfirmUpload(w http.ResponseWriter, r *http.Request, scope corpus.Scope, uploadID string) {
	session, err := a.Uploads.ConfirmScoped(r.Context(), scope, uploadID)
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
	} else {
		a.Commands.Accepted(telemetry.CommandUploadConfirm, 1)
		send(w, 202, sessionToTransport(session))
	}
}

func (a *API) handleGetBlob(w http.ResponseWriter, r *http.Request, scope corpus.Scope, blobID string) {
	blob, err := a.Uploads.BlobScoped(r.Context(), scope, blobID)
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
	} else {
		send(w, 200, blobToTransport(blob))
	}
}
