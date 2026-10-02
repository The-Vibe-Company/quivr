package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

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
func (a *API) ingestCommand(ctx context.Context, submit content.Submitter, raw any) (content.Receipt, error) {
	if a.schemas["IngestCommand"].Validate(raw) != nil {
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
	receipt, err := submit.Accept(ctx, c)
	if err != nil {
		return content.Receipt{}, err
	}
	return receipt, nil
}

// batchEntry resolves one batch entry independently of its peers. Its index
// only correlates the outcome; the entry's own key carries its identity. An
// entry is held to the single-request bound on its raw bytes, so the same entry
// submitted alone is never refused for size.
func (a *API) batchEntry(ctx context.Context, submit content.Submitter, index int, entry any, size int) transport.BatchItem {
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
			receipt, err = a.ingestCommand(ctx, submit, entry)
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

// handleIngestRecord accepts one record command after the strict dispatcher
// has matched POST /v0/records.
func (a *API) handleIngestRecord(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	err := a.Content.Submit(scope, func(submit content.Submitter) error {
		raw, _, ok := readJSON(w, r, maxRequestBytes)
		if !ok {
			return nil
		}
		receipt, err := a.ingestCommand(r.Context(), submit, raw)
		if err != nil {
			writeError(w, err, publicerr.ContentUnavailable)
		} else {
			a.Commands.Accepted(telemetry.CommandRecord, 1)
			slog.Info("command accepted", "component", "api", "command", telemetry.CommandRecord, "request_id", w.Header().Get("X-Request-ID"), "receipt_id", receipt.ID, "record_id", receipt.RecordID)
			w.Header().Set("Location", "/v0/ingestion-receipts/"+receipt.ID)
			send(w, 202, receiptToTransport(receipt))
		}
		return nil
	})
	if err != nil {
		writeError(w, err, publicerr.ContentUnavailable)
	}
}

// handleIngestBatch accepts POST /v0/records/batch.
func (a *API) handleIngestBatch(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	err := a.Content.Batch(scope, func(submit content.Submitter) error {
		raw, payload, ok := readJSON(w, r, maxBatchBytes)
		if !ok {
			return nil
		}
		// The envelope is bounded and rejected whole before any entry is attempted.
		envelope, _ := raw.(map[string]any)
		entries, _ := envelope["items"].([]any)
		if len(entries) > maxBatchEntries {
			writeError(w, publicerr.BatchTooLarge, nil)
			return nil
		}
		if a.schemas["BatchRequest"].Validate(raw) != nil {
			writeError(w, publicerr.InvalidSchema, nil)
			return nil
		}
		var sized struct {
			Items []json.RawMessage `json:"items"`
		}
		if json.Unmarshal(payload, &sized) != nil {
			writeError(w, publicerr.MalformedJson, nil)
			return nil
		}
		result := transport.BatchResult{Items: make([]transport.BatchItem, 0, len(entries))}
		receipts, retryable := 0, 0
		for i, entry := range entries {
			item := a.batchEntry(r.Context(), submit, i, entry, len(sized.Items[i]))
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
		return nil
	})
	if err != nil {
		writeError(w, err, publicerr.ContentUnavailable)
	}
}

// handleWithdrawRecord accepts POST /v0/records/withdrawals.
func (a *API) handleWithdrawRecord(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	var wire transport.WithdrawalCommand
	receipt, err := a.Content.Withdraw(r.Context(), scope, content.Withdrawal{}, func() (content.Withdrawal, error) {
		raw, ok := decodeRequest(w, r, a.schemas["WithdrawalCommand"])
		if !ok {
			return content.Withdrawal{}, errResponseWritten
		}
		b, err := json.Marshal(raw)
		if err != nil {
			writeError(w, publicerr.InvalidSchema, nil)
			return content.Withdrawal{}, errResponseWritten
		}
		if err = json.Unmarshal(b, &wire); err != nil {
			writeError(w, publicerr.InvalidSchema, nil)
			return content.Withdrawal{}, errResponseWritten
		}
		if connectors.IsConnectorKey(wire.IdempotencyKey) {
			writeError(w, publicerr.ReservedIdempotencyKey, nil)
			return content.Withdrawal{}, errResponseWritten
		}

		return withdrawalFromTransport(wire), nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.ContentUnavailable)
	} else {
		a.Commands.Accepted(telemetry.CommandWithdrawal, 1)
		slog.Info("command accepted", "component", "api", "command", telemetry.CommandWithdrawal, "request_id", w.Header().Get("X-Request-ID"), "receipt_id", receipt.ID, "record_id", receipt.RecordID)
		w.Header().Set("Location", "/v0/ingestion-receipts/"+receipt.ID)
		send(w, 202, receiptToTransport(receipt))
	}
}

func (a *API) handleGetReceipt(w http.ResponseWriter, r *http.Request, scope corpus.Scope, receiptID string) {
	receipt, err := a.Content.Receipt(r.Context(), scope, receiptID)
	if err != nil {
		writeError(w, err, publicerr.ContentUnavailable)
	} else {
		send(w, 200, receiptToTransport(receipt))
	}
}

func (a *API) handleGetRecord(w http.ResponseWriter, r *http.Request, scope corpus.Scope, recordID string) {
	record, err := a.Content.Record(r.Context(), scope, recordID)
	if err != nil {
		writeError(w, err, publicerr.ContentUnavailable)
	} else {
		send(w, 200, recordToTransport(record))
	}
}

func (a *API) handleGetVersion(w http.ResponseWriter, r *http.Request, scope corpus.Scope, recordID, versionID string) {
	version, err := a.Content.Version(r.Context(), scope, recordID, versionID)
	if err != nil {
		writeError(w, err, publicerr.ContentUnavailable)
		return
	}
	wire, err := versionToTransport(version)
	if err != nil {
		writeError(w, err, publicerr.ContentUnavailable)
		return
	}
	send(w, 200, wire)
}
