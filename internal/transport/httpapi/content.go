package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	"github.com/The-Vibe-Company/quivr-v2/internal/telemetry"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// ingestCommand validates one raw IngestCommand and runs the acceptance path
// shared by single submission and every batch entry, so a key replays the same
// Receipt through either endpoint. A non-nil error reports the rejection.
func (a *API) ingestCommand(ctx context.Context, scope corpus.Scope, raw any) (content.Receipt, error) {
	if a.ingestSchema.Validate(raw) != nil {
		return content.Receipt{}, publicerr.InvalidSchema
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return content.Receipt{}, publicerr.InvalidSchema
	}
	var wire transport.IngestCommand
	if err = json.Unmarshal(b, &wire); err != nil {
		return content.Receipt{}, publicerr.InvalidSchema
	}
	c, err := commandFromTransport(wire)
	if err != nil {
		return content.Receipt{}, publicerr.InvalidSchema
	}
	if connectors.IsConnectorKey(c.Key) {
		return content.Receipt{}, publicerr.ReservedIdempotencyKey
	}
	receipt, err := a.Content.Accept(ctx, scope, c)
	if err != nil {
		return content.Receipt{}, err
	}
	return receipt, nil
}

// batchEntry resolves one batch entry independently of its peers. Its index
// only correlates the outcome; the entry's own key carries its identity. An
// entry is held to the single-request bound on its raw bytes, so the same entry
// submitted alone is never refused for size.
func (a *API) batchEntry(ctx context.Context, scope corpus.Scope, index int, entry any, size int) transport.BatchItem {
	// Each entry gets a fresh budget, bounded by the batch and caller deadlines.
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	item := transport.BatchItem{Index: index}
	var receipt content.Receipt
	err := ctx.Err()
	if err == nil {
		if size > maxRequestBytes {
			err = publicerr.EntryTooLarge
		} else {
			receipt, err = a.ingestCommand(ctx, scope, entry)
		}
	}
	if err != nil {
		_, e := errorResponse(err, publicerr.ContentUnavailable)
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
			writeError(w, publicerr.Forbidden, nil)
			return true
		}
		raw, _, ok := readJSON(w, r, maxRequestBytes)
		if !ok {
			return true
		}
		receipt, err := a.ingestCommand(r.Context(), scope, raw)
		if err != nil {
			writeError(w, err, publicerr.ContentUnavailable)
		} else {
			a.Commands.Accepted(telemetry.CommandRecord, 1)
			slog.Info("command accepted", "component", "api", "command", telemetry.CommandRecord, "request_id", w.Header().Get("X-Request-ID"), "receipt_id", receipt.ID, "record_id", receipt.RecordID)
			w.Header().Set("Location", "/v0/ingestion-receipts/"+receipt.ID)
			send(w, 202, receiptToTransport(receipt))
		}
		return true
	}
	if r.Method == "POST" && r.URL.Path == "/v0/records/batch" {
		if !scope.Allows("content:write") {
			writeError(w, publicerr.Forbidden, nil)
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
			writeError(w, publicerr.BatchTooLarge, nil)
			return true
		}
		if a.batchSchema.Validate(raw) != nil {
			writeError(w, publicerr.InvalidSchema, nil)
			return true
		}
		var sized struct {
			Items []json.RawMessage `json:"items"`
		}
		if json.Unmarshal(payload, &sized) != nil {
			writeError(w, publicerr.MalformedJson, nil)
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
		a.Commands.Accepted(telemetry.CommandBatchEntry, receipts)
		slog.Info("ingestion batch", "request_id", w.Header().Get("X-Request-ID"), "entries", len(entries), "receipts", receipts, "rejected", len(entries)-receipts, "retryable", retryable)
		send(w, 200, result)
		return true
	}
	if r.Method == "POST" && r.URL.Path == "/v0/records/withdrawals" {
		if !scope.Allows("content:write") {
			writeError(w, publicerr.Forbidden, nil)
			return true
		}
		raw, ok := decodeRequest(w, r, a.withdrawSchema)
		if !ok {
			return true
		}
		b, err := json.Marshal(raw)
		if err != nil {
			writeError(w, publicerr.InvalidSchema, nil)
			return true
		}
		var wire transport.WithdrawalCommand
		if err = json.Unmarshal(b, &wire); err != nil {
			writeError(w, publicerr.InvalidSchema, nil)
			return true
		}
		if connectors.IsConnectorKey(wire.IdempotencyKey) {
			writeError(w, publicerr.ReservedIdempotencyKey, nil)
			return true
		}
		receipt, err := a.Content.Withdraw(r.Context(), scope, withdrawalFromTransport(wire))
		if err != nil {
			writeError(w, err, publicerr.ContentUnavailable)
		} else {
			a.Commands.Accepted(telemetry.CommandWithdrawal, 1)
			slog.Info("command accepted", "component", "api", "command", telemetry.CommandWithdrawal, "request_id", w.Header().Get("X-Request-ID"), "receipt_id", receipt.ID, "record_id", receipt.RecordID)
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
			writeError(w, err, publicerr.ContentUnavailable)
		} else {
			send(w, 200, receiptToTransport(receipt))
		}
		return true
	}
	if len(path) >= 3 && path[0] == "v0" && path[1] == "records" {
		if len(path) == 3 {
			record, err := a.Content.Record(r.Context(), scope, path[2])
			if err != nil {
				writeError(w, err, publicerr.ContentUnavailable)
			} else {
				send(w, 200, recordToTransport(record))
			}
			return true
		}
		if len(path) == 5 && path[3] == "versions" {
			version, err := a.Content.Version(r.Context(), scope, path[2], path[4])
			if err != nil {
				writeError(w, err, publicerr.ContentUnavailable)
			} else {
				wire, err := versionToTransport(version)
				if err != nil {
					writeError(w, err, publicerr.ContentUnavailable)
				} else {
					send(w, 200, wire)
				}
			}
			return true
		}
	}
	return false
}
