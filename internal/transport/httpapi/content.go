package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

// ingestCommand validates one raw IngestCommand and runs the acceptance path
// shared by single submission and every batch entry, so a key replays the same
// Receipt through either endpoint. A non-nil error reports the rejection.
func (a *API) ingestCommand(ctx context.Context, submit content.Submitter, raw any, rawBytes []byte) (content.Receipt, error) {
	if a.schemas["IngestCommand"].Validate(raw) != nil {
		return content.Receipt{}, publicerr.InvalidSchema
	}
	var wire transport.IngestCommand
	if err := json.Unmarshal(rawBytes, &wire); err != nil {
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

// decodeJSONNumber decodes a single JSON value preserving numeric fidelity for
// JSON Schema validation.
func decodeJSONNumber(rawBytes []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(rawBytes))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// parseBatchEnvelope validates the top-level BatchRequest envelope in a single
// pass without allocating decoded maps for every entry upfront.
func parseBatchEnvelope(payload []byte) ([]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	tok, err := dec.Token()
	if err != nil {
		return nil, publicerr.MalformedJson
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		// Valid non-object JSON (e.g. a bare array) violates BatchRequest's
		// object schema; malformed syntax remains malformed_json.
		var check any
		if json.Unmarshal(payload, &check) != nil {
			return nil, publicerr.MalformedJson
		}
		return nil, publicerr.InvalidSchema
	}
	seen := map[string]bool{}
	var itemsRaw json.RawMessage
	var extraKey bool
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, publicerr.MalformedJson
		}
		key, ok := keyTok.(string)
		if !ok || seen[key] {
			return nil, publicerr.MalformedJson
		}
		seen[key] = true
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, publicerr.MalformedJson
		}
		if key == "items" {
			itemsRaw = val
		} else {
			extraKey = true
		}
	}
	endTok, err := dec.Token()
	if err != nil || endTok != json.Delim('}') {
		return nil, publicerr.MalformedJson
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, publicerr.MalformedJson
	}
	if len(itemsRaw) == 0 {
		return nil, publicerr.InvalidSchema
	}
	var items []json.RawMessage
	if err := json.Unmarshal(itemsRaw, &items); err != nil {
		return nil, publicerr.InvalidSchema
	}
	if len(items) > maxBatchEntries {
		return nil, publicerr.BatchTooLarge
	}
	if extraKey || len(items) == 0 {
		return nil, publicerr.InvalidSchema
	}
	return items, nil
}

// batchEntry resolves one batch entry independently of its peers. Its index
// only correlates the outcome; the entry's own key carries its identity. An
// entry is held to the single-request bound on its raw bytes, so the same entry
// submitted alone is never refused for size.
func (a *API) batchEntry(ctx context.Context, submit content.Submitter, index int, rawBytes []byte) (transport.BatchItem, int) {
	item := transport.BatchItem{Index: index}
	if err := ctx.Err(); err != nil {
		status, e := errorResponse(err, publicerr.ContentUnavailable)
		correlateError(ctx, &e)
		item.Error = &e
		return item, status
	}
	// Each entry gets a fresh budget, bounded by the batch and caller deadlines.
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var receipt content.Receipt
	var err error
	if len(rawBytes) > maxRequestBytes {
		err = publicerr.EntryTooLarge
	} else {
		var raw any
		raw, err = decodeJSONNumber(rawBytes)
		if err != nil {
			err = publicerr.InvalidSchema
		} else {
			receipt, err = a.ingestCommand(ctx, submit, raw, rawBytes)
		}
	}
	if err != nil {
		status, e := errorResponse(err, publicerr.ContentUnavailable)
		correlateError(ctx, &e)
		item.Error = &e
		return item, status
	}
	wire := receiptToTransport(receipt)
	item.Receipt = &wire
	return item, http.StatusAccepted
}

// handleIngestRecord accepts one record command after the strict dispatcher
// has matched POST /v0/records.
func (a *API) handleIngestRecord(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	err := a.Content.Submit(scope, func(submit content.Submitter) error {
		raw, payload, ok := readJSON(w, r, maxRequestBytes)
		if !ok {
			return nil
		}
		receipt, err := a.ingestCommand(r.Context(), submit, raw, payload)
		if err != nil {
			writeError(w, err, publicerr.ContentUnavailable)
		} else {
			a.Commands.Accepted(telemetry.CommandRecord, 1)
			slog.InfoContext(r.Context(), "command accepted", "component", "api", "command", telemetry.CommandRecord, "request_id", w.Header().Get("X-Request-ID"), "receipt_id", receipt.ID, "record_id", receipt.RecordID)
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
		payload, ok := readPayload(w, r, maxBatchBytes)
		if !ok {
			return nil
		}
		// The envelope is bounded and rejected whole before any entry is attempted.
		rawItems, err := parseBatchEnvelope(payload)
		if err != nil {
			writeError(w, err, nil)
			return nil
		}
		result := transport.BatchResult{Items: make([]transport.BatchItem, 0, len(rawItems))}
		receipts, retryable := 0, 0
		batchCtx := r.Context()
		var auditDeadline time.Time
		for i, rawItem := range rawItems {
			if err := batchCtx.Err(); err != nil {
				_, e := errorResponse(err, publicerr.ContentUnavailable)
				correlateError(batchCtx, &e)
				result.Items = append(result.Items, transport.BatchItem{Index: i, Error: &e})
				retryable++
				continue
			}
			item, status := a.batchEntry(batchCtx, submit, i, rawItem)
			if item.Receipt != nil {
				receipts++
			} else {
				// Avoid passing an already-expired batch context into refusal auditing;
				// skip auditing for retryable deadline/availability failures and bound
				// all refusal audits across the batch to one shared 2-second budget.
				if !(status >= http.StatusInternalServerError && item.Error.Retryable) {
					if auditDeadline.IsZero() {
						auditDeadline = time.Now().Add(2 * time.Second)
					}
					auditCtx, cancelAudit := context.WithDeadline(context.WithoutCancel(batchCtx), auditDeadline)
					auditErr := a.auditEmbeddedRefusal(auditCtx, status, item.Error.Code)
					cancelAudit()
					if auditErr != nil {
						slog.WarnContext(batchCtx, "batch refusal audit write failed",
							"request_id", w.Header().Get("X-Request-ID"), "entry_index", i,
							"refusal_code", item.Error.Code, "error", auditErr)
						_, failure := errorResponse(publicerr.StorageUnavailable, nil)
						correlateError(batchCtx, &failure)
						item.Error = &failure
					}
				}
				if item.Error.Retryable {
					retryable++
				}
			}
			result.Items = append(result.Items, item)
		}
		a.Commands.Accepted(telemetry.CommandBatchEntry, receipts)
		slog.InfoContext(batchCtx, "ingestion batch", "request_id", w.Header().Get("X-Request-ID"), "entries", len(rawItems), "receipts", receipts, "rejected", len(rawItems)-receipts, "retryable", retryable)
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
		slog.InfoContext(r.Context(), "command accepted", "component", "api", "command", telemetry.CommandWithdrawal, "request_id", w.Header().Get("X-Request-ID"), "receipt_id", receipt.ID, "record_id", receipt.RecordID)
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
