package postgres

import (
	"context"

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
	var code string
	err := database(ctx, s.Pool).QueryRow(ctx, `SELECT rc.id,rc.state,coalesce(rc.outcome,''),rc.record_id,coalesce(rc.version_id,''),r.corpus_id,r.namespace,r.record_key,rc.processing,rc.error_code FROM ingestion_receipts rc JOIN records r ON (r.organization,r.id)=(rc.organization,rc.record_id) WHERE rc.organization=$1 AND rc.id=$2`, org, id).Scan(&r.ID, &r.State, &r.Outcome, &r.RecordID, &r.VersionID, &r.Source.CorpusID, &r.Source.Namespace, &r.Source.RecordKey, &r.Processing.State, &code)
	if err != nil {
		return r, notFound(err)
	}
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
