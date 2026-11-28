package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/audit"
	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

const batchRefusalAuditBudget = 2 * time.Second

// ingestCommand validates one raw IngestCommand in a single pass and runs the
// acceptance path shared by single submission and every batch entry, so a key
// replays the same Receipt through either endpoint.
func (a *API) ingestCommand(ctx context.Context, submit content.Submitter, rawBytes []byte, checkSyntaxOnError bool) (content.Receipt, error) {
	c, err := decodeIngestCommand(rawBytes, checkSyntaxOnError)
	if err != nil {
		return content.Receipt{}, err
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
		receipt, err = a.ingestCommand(ctx, submit, rawBytes, false)
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
		payload, lease, ok := a.acquireIngestPayload(w, r, maxRequestBytes)
		if !ok {
			return nil
		}
		defer lease.Release()
		receipt, err := a.ingestCommand(r.Context(), submit, payload, true)
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

// auditBatchRefusal records one embedded batch refusal using at most budget of
// the batch's shared refusal-audit allowance. It returns budgetExhausted=true
// when the cleanup deadline expired rather than a non-budget storage error.
func (a *API) auditBatchRefusal(ctx context.Context, budget time.Duration, status int, code string) (budgetExhausted bool, err error) {
	if a.Audit == nil {
		return false, nil
	}
	event, ok := ctx.Value(auditEventKey{}).(audit.Event)
	if !ok {
		return false, nil
	}
	event.Action = refusalAuditAction(status, code, true)
	if event.Action == "" {
		return false, nil
	}
	event.Detail.Status = status
	event.Detail.ErrorCode = boundedAuditID(code)
	event.Outcome = "refused"
	work, cancel := lifecycle.CleanupContext(ctx, budget)
	defer cancel()
	if err := a.Audit.Append(work, &event); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(work.Err(), context.DeadlineExceeded) {
			return true, err
		}
		return false, err
	}
	audit.Log(work, event)
	return false, nil
}

// handleIngestBatch accepts POST /v0/records/batch.
func (a *API) handleIngestBatch(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	err := a.Content.Batch(scope, func(submit content.Submitter) error {
		payload, lease, ok := a.acquireIngestPayload(w, r, maxBatchBytes)
		if !ok {
			return nil
		}
		defer lease.Release()
		// The envelope is bounded and rejected whole before any entry is attempted.
		rawItems, err := parseBatchEnvelope(payload)
		if err != nil {
			writeError(w, err, nil)
			return nil
		}
		result := transport.BatchResult{Items: make([]transport.BatchItem, 0, len(rawItems))}
		receipts, retryable := 0, 0
		batchCtx := r.Context()
		auditBudgetRemaining := batchRefusalAuditBudget
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
				// Exhausting the audit budget never converts a refusal into StorageUnavailable.
				if a.Audit != nil &&
					!(status >= http.StatusInternalServerError && item.Error.Retryable) &&
					refusalAuditAction(status, item.Error.Code, true) != "" {
					if auditBudgetRemaining <= 0 {
						slog.WarnContext(batchCtx, "batch refusal audit budget exhausted",
							"request_id", w.Header().Get("X-Request-ID"), "entry_index", i,
							"refusal_code", item.Error.Code)
					} else {
						auditStart := time.Now()
						budgetExhausted, auditErr := a.auditBatchRefusal(batchCtx, auditBudgetRemaining, status, item.Error.Code)
						auditBudgetRemaining -= time.Since(auditStart)
						if auditErr != nil {
							if budgetExhausted {
								auditBudgetRemaining = 0
								slog.WarnContext(batchCtx, "batch refusal audit budget exhausted",
									"request_id", w.Header().Get("X-Request-ID"), "entry_index", i,
									"refusal_code", item.Error.Code, "error", auditErr)
							} else {
								slog.WarnContext(batchCtx, "batch refusal audit write failed",
									"request_id", w.Header().Get("X-Request-ID"), "entry_index", i,
									"refusal_code", item.Error.Code, "error", auditErr)
								_, failure := errorResponse(publicerr.StorageUnavailable, nil)
								correlateError(batchCtx, &failure)
								item.Error = &failure
							}
						}
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
