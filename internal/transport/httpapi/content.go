package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
	"log/slog"
	"net/http"
	"strings"
)

// contentFailure maps a content error to its public status and code; single
// requests and batch entries share it.
func contentFailure(err error) (int, string) {
	switch {
	case errors.Is(err, corpus.ErrForbidden):
		return 403, "forbidden"
	case errors.Is(err, corpus.ErrNotFound):
		return 404, "not_found"
	case errors.Is(err, content.ErrConflict):
		return 409, "idempotency_conflict"
	case errors.Is(err, content.ErrUnverifiedBlob):
		return 422, "unverified_blob"
	case errors.Is(err, content.ErrInvalid), errors.Is(err, content.ErrUnsupported):
		return 422, err.Error()
	default:
		return 503, "content_unavailable"
	}
}
func contentError(w http.ResponseWriter, err error) {
	status, code := contentFailure(err)
	failure(w, status, code)
}

// ingestCommand validates one raw IngestCommand and runs the acceptance path
// shared by single submission and every batch entry, so a key replays the same
// Receipt through either endpoint. A non-empty code reports the rejection.
func (a *API) ingestCommand(ctx context.Context, scope corpus.Scope, raw any) (content.Receipt, int, string) {
	if a.ingestSchema.Validate(raw) != nil {
		return content.Receipt{}, 422, "invalid_schema"
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return content.Receipt{}, 422, "invalid_schema"
	}
	var wire transport.IngestCommand
	if err = json.Unmarshal(b, &wire); err != nil {
		return content.Receipt{}, 422, "invalid_schema"
	}
	c, err := commandFromTransport(wire)
	if err != nil {
		return content.Receipt{}, 422, "invalid_schema"
	}
	if connectors.IsConnectorKey(c.Key) {
		return content.Receipt{}, 422, "reserved_idempotency_key"
	}
	receipt, err := a.Content.Accept(ctx, scope, c)
	if err != nil {
		status, code := contentFailure(err)
		return content.Receipt{}, status, code
	}
	return receipt, 202, ""
}

// batchEntry resolves one batch entry independently of its peers. Its index
// only correlates the outcome; the entry's own key carries its identity. An
// entry is held to the single-request bound on its raw bytes, so the same entry
// submitted alone is never refused for size.
func (a *API) batchEntry(ctx context.Context, scope corpus.Scope, index int, entry any, size int) transport.BatchItem {
	item := transport.BatchItem{Index: index}
	receipt, status, code := content.Receipt{}, 413, "entry_too_large"
	if size <= maxRequestBytes {
		receipt, status, code = a.ingestCommand(ctx, scope, entry)
	}
	if code != "" {
		e := apiError(status, code)
		item.Error = &e
		return item
	}
	wire := receiptToTransport(receipt)
	item.Receipt = &wire
	return item
}

func (a *API) contentRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
	if r.Method == "POST" && r.URL.Path == "/v0/records" {
		if !scope.Allows("content:write") {
			failure(w, 403, "forbidden")
			return true
		}
		raw, _, ok := readJSON(w, r, maxRequestBytes)
		if !ok {
			return true
		}
		receipt, status, code := a.ingestCommand(r.Context(), scope, raw)
		if code != "" {
			failure(w, status, code)
		} else {
			w.Header().Set("Location", "/v0/ingestion-receipts/"+receipt.ID)
			send(w, 202, receiptToTransport(receipt))
		}
		return true
	}
	if r.Method == "POST" && r.URL.Path == "/v0/records/batch" {
		if !scope.Allows("content:write") {
			failure(w, 403, "forbidden")
			return true
		}
		raw, payload, ok := readJSON(w, r, maxBatchBytes)
		if !ok {
			return true
		}
		// The envelope is bounded and rejected whole before any entry is attempted.
		envelope, _ := raw.(map[string]any)
		entries, _ := envelope["items"].([]any)
		if len(entries) > maxBatchEntries {
			failure(w, 413, "batch_too_large")
			return true
		}
		if a.batchSchema.Validate(raw) != nil {
			failure(w, 422, "invalid_schema")
			return true
		}
		var sized struct {
			Items []json.RawMessage `json:"items"`
		}
		if json.Unmarshal(payload, &sized) != nil || len(sized.Items) != len(entries) {
			failure(w, 400, "malformed_json")
			return true
		}
		result := transport.BatchResult{Items: make([]transport.BatchItem, 0, len(entries))}
		receipts, retryable := 0, 0
		for i, entry := range entries {
			item := a.batchEntry(r.Context(), scope, i, entry, len(sized.Items[i]))
			if item.Receipt != nil {
				receipts++
			} else if item.Error.Retryable {
				retryable++
			}
			result.Items = append(result.Items, item)
		}
		slog.Info("ingestion batch", "request_id", w.Header().Get("X-Request-ID"), "entries", len(entries), "receipts", receipts, "rejected", len(entries)-receipts, "retryable", retryable)
		send(w, 200, result)
		return true
	}
	if r.Method == "POST" && r.URL.Path == "/v0/records/withdrawals" {
		if !scope.Allows("content:write") {
			failure(w, 403, "forbidden")
			return true
		}
		raw, ok := decodeRequest(w, r, a.withdrawSchema)
		if !ok {
			return true
		}
		b, err := json.Marshal(raw)
		if err != nil {
			failure(w, 422, "invalid_schema")
			return true
		}
		var wire transport.WithdrawalCommand
		if err = json.Unmarshal(b, &wire); err != nil {
			failure(w, 422, "invalid_schema")
			return true
		}
		if connectors.IsConnectorKey(wire.IdempotencyKey) {
			failure(w, 422, "reserved_idempotency_key")
			return true
		}
		receipt, err := a.Content.Withdraw(r.Context(), scope, withdrawalFromTransport(wire))
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
	if r.URL.Path == "/v0/records" {
		a.listRecords(w, r, scope)
		return true
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
