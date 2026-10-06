package postgres

import (
	"context"
	"encoding/json"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReceiptStore reads ingestion and withdrawal outcomes and receipt identities.
type ReceiptStore struct{ Pool *pgxpool.Pool }

var _ content.ReceiptReader = ReceiptStore{}

// HasReceipt reports whether an ingestion idempotency key was accepted in the
// Organization; connectors use it to skip unchanged items before downloading.
func (s ReceiptStore) HasReceipt(ctx context.Context, org, key string) (bool, error) {
	var exists bool
	err := database(ctx, s.Pool).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ingestion_receipts WHERE organization=$1 AND id=$2)`, org, content.StableID("receipt", org, "ingestion", key)).Scan(&exists)
	return exists, err
}

func (s ReceiptStore) Receipt(ctx context.Context, org, id string) (content.Receipt, error) {
	r := content.Receipt{Diagnostics: []content.Diagnostic{}}
	var command []byte
	var code string
	err := database(ctx, s.Pool).QueryRow(ctx, `SELECT id,state,coalesce(outcome,''),record_id,coalesce(version_id,''),command,processing,error_code FROM ingestion_receipts WHERE organization=$1 AND id=$2`, org, id).Scan(&r.ID, &r.State, &r.Outcome, &r.RecordID, &r.VersionID, &command, &r.Processing.State, &code)
	if err != nil {
		return r, notFound(err)
	}
	var c content.Command
	if err = json.Unmarshal(command, &c); err != nil {
		return r, err
	}
	r.Source = c.Source
	if r.Processing.State != "idle" {
		r.Processing.Phase = "materialization"
	}
	if r.VersionID != "" {
		a, p, statusCode, statusErr := (VersionStore{Pool: s.Pool}).VersionStatus(ctx, org, r.VersionID)
		if statusErr != nil {
			return r, statusErr
		}
		r.Availability = &a
		r.Processing = p
		code = statusCode
		if a.State == "quarantined" {
			diagnostics, err := (VersionStore{Pool: s.Pool}).versionDiagnostics(ctx, org, r.VersionID, true, false, code)
			if err != nil {
				return r, err
			}
			if len(diagnostics) > 0 {
				d := diagnostics[0]
				r.Diagnostics = append(r.Diagnostics, content.Diagnostic{Code: d.Code, Message: d.Message, Retryable: false})
				return r, nil
			}
		}
		if enrichmentBlocked(p) {
			reason, err := (VersionStore{Pool: s.Pool}).enrichmentReason(ctx, org, r.VersionID)
			if err != nil {
				return r, err
			}
			if reason != nil {
				r.Diagnostics = append(r.Diagnostics, content.Diagnostic{Code: reason.Code, Message: reason.Message, Retryable: false})
				return r, nil
			}
		}
	}
	if code != "" {
		r.Diagnostics = append(r.Diagnostics, content.Diagnostic{Code: code, Message: "Processing requires attention or retry", Retryable: r.Processing.State == "retrying"})
	}
	return r, nil
}
