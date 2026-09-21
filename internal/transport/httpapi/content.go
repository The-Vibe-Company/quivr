package httpapi

import (
	"encoding/json"
	"errors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
	"net/http"
	"strings"
)

func contentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, corpus.ErrForbidden):
		failure(w, 403, "forbidden")
	case errors.Is(err, corpus.ErrNotFound):
		failure(w, 404, "not_found")
	case errors.Is(err, content.ErrConflict):
		failure(w, 409, "idempotency_conflict")
	case errors.Is(err, content.ErrUnverifiedBlob):
		failure(w, 422, "unverified_blob")
	case errors.Is(err, content.ErrInvalid), errors.Is(err, content.ErrUnsupported):
		failure(w, 422, err.Error())
	default:
		failure(w, 503, "content_unavailable")
	}
}
func (a *API) contentRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
	if r.Method == "POST" && r.URL.Path == "/v0/records" {
		if !scope.Allows("content:write") {
			failure(w, 403, "forbidden")
			return true
		}
		raw, ok := decodeRequest(w, r, a.ingestSchema)
		if !ok {
			return true
		}
		b, err := json.Marshal(raw)
		if err != nil {
			failure(w, 422, "invalid_schema")
			return true
		}
		var wire transport.IngestCommand
		if err = json.Unmarshal(b, &wire); err != nil {
			failure(w, 422, "invalid_schema")
			return true
		}
		c, err := commandFromTransport(wire)
		if err != nil {
			failure(w, 422, "invalid_schema")
			return true
		}
		receipt, err := a.Content.Accept(r.Context(), scope, c)
		if err != nil {
			contentError(w, err)
		} else {
			w.Header().Set("Location", "/v0/ingestion-receipts/"+receipt.ID)
			send(w, 202, receiptToTransport(receipt))
		}
		return true
	}
	if r.Method != "GET" {
		return false
	}
	path := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(path) == 3 && path[0] == "v0" && path[1] == "ingestion-receipts" {
		receipt, err := a.Content.Receipt(r.Context(), scope, path[2])
		if err != nil {
			contentError(w, err)
		} else {
			send(w, 200, receiptToTransport(receipt))
		}
		return true
	}
	if len(path) >= 3 && path[0] == "v0" && path[1] == "records" {
		if len(path) == 3 {
			record, err := a.Content.Record(r.Context(), scope, path[2])
			if err != nil {
				contentError(w, err)
			} else {
				send(w, 200, recordToTransport(record))
			}
			return true
		}
		if len(path) == 5 && path[3] == "versions" {
			version, err := a.Content.Version(r.Context(), scope, path[2], path[4])
			if err != nil {
				contentError(w, err)
			} else {
				wire, err := versionToTransport(version)
				if err != nil {
					contentError(w, err)
				} else {
					send(w, 200, wire)
				}
			}
			return true
		}
	}
	return false
}
