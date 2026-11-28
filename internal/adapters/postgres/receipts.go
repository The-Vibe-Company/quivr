package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/visibility"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReceiptStore reads ingestion and withdrawal outcomes and receipt identities.
type ReceiptStore struct{ Pool *pgxpool.Pool }

var _ content.ReceiptReader = ReceiptStore{}

// replayReceiptRow carries the receipt, its optional materialized Version
// status, request identity bytes, visibility check, and record/corpus
// existence flags from a single round trip.
type replayReceiptRow struct {
	replayID        *string
	previous        []byte
	previousDigest  []byte
	receipt         content.Receipt
	code            string
	visible         bool
	baseline        bool
	quarantined     bool
	withdrawn       bool
	current         bool
	vProcessing     string
	vErrorCode      string
	enrichmentState string
	enrichmentError string
	enrichmentRaw   []byte
	recordExists    bool
	corpusExists    bool
}

// buildAcceptLookupQuery constructs a single-query lookup for replay detection,
// receipt hydration, visibility enforcement, and record/corpus existence.
func buildAcceptLookupQuery(ctx context.Context, org, routeFamily, key, recordID, corpusID string) (string, []any) {
	args := []any{org, routeFamily, key, recordID, corpusID}
	visibleExpr := "true"
	if principals, enforced := visibility.Principals(ctx); enforced {
		args = append(args, principals)
		visibleExpr = "r.id IS NOT NULL AND " + recordVisibilitySQL("r", fmt.Sprintf("$%d", len(args)))
	}
	query := `SELECT rc.id,rc.canonical_request,rc.request_digest,
 coalesce(rc.state,''),coalesce(rc.outcome,''),coalesce(rc.record_id,''),coalesce(rc.version_id,''),
 coalesce(r.corpus_id,''),coalesce(r.namespace,''),coalesce(r.record_key,''),
 coalesce(rc.processing,''),coalesce(rc.error_code,''),` + visibleExpr + `,
 coalesce(v.baseline_ready,false),coalesce(v.quarantined,false),
 coalesce(r.withdrawn,false) OR (r.id IS NOT NULL AND EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id)),
 coalesce(r.current_version_id=v.id,false),
 coalesce(v.processing,''),coalesce(v.error_code,''),
 coalesce(v.enrichment_state,''),coalesce(v.enrichment_error,''),
 v.enrichment_reason,
 EXISTS(SELECT 1 FROM records rec WHERE rec.organization=$1 AND rec.id=$4),
 EXISTS(SELECT 1 FROM corpora cp WHERE cp.organization=$1 AND cp.id=$5)
 FROM (VALUES(1)) seed(n)
 LEFT JOIN ingestion_receipts rc ON rc.organization=$1 AND rc.route_family=$2 AND rc.request_key=$3
 LEFT JOIN records r ON (r.organization,r.id)=(rc.organization,rc.record_id)
 LEFT JOIN record_versions v ON rc.version_id IS NOT NULL AND (v.organization,v.id)=(rc.organization,rc.version_id)`
	return query, args
}

func scanReplayReceiptRow(row pgx.Row) (replayReceiptRow, error) {
	var out replayReceiptRow
	err := row.Scan(
		&out.replayID, &out.previous, &out.previousDigest,
		&out.receipt.State, &out.receipt.Outcome, &out.receipt.RecordID, &out.receipt.VersionID,
		&out.receipt.Source.CorpusID, &out.receipt.Source.Namespace, &out.receipt.Source.RecordKey,
		&out.receipt.Processing.State, &out.code, &out.visible,
		&out.baseline, &out.quarantined, &out.withdrawn, &out.current,
		&out.vProcessing, &out.vErrorCode,
		&out.enrichmentState, &out.enrichmentError, &out.enrichmentRaw,
		&out.recordExists, &out.corpusExists,
	)
	if err == nil && out.replayID != nil {
		out.receipt.ID = *out.replayID
	}
	return out, err
}

// hydrateReceipt assembles the public Receipt from a joined receipt/version row
// on the caller's existing querier, avoiding a second pool connection.
func (s ReceiptStore) hydrateReceipt(ctx context.Context, q querier, org string, row replayReceiptRow) (content.Receipt, error) {
	r := row.receipt
	r.Diagnostics = []content.Diagnostic{}
	code := row.code
	if r.Processing.State != "idle" {
		r.Processing.Phase = "materialization"
	}
	if r.VersionID != "" {
		a := content.Availability{State: "materialized", Current: row.current}
		p := content.Processing{State: row.vProcessing}
		code = row.vErrorCode
		if p.State == "running" || p.State == "retrying" {
			a.State = "building_baseline"
		}
		if row.baseline {
			a.State = "retrieval_ready"
		}
		if row.quarantined {
			a.State = "quarantined"
		}
		a.Current = a.Current && !row.withdrawn && !row.quarantined
		a.Searchable = row.baseline && a.Current
		if row.baseline && !row.quarantined {
			p.State, code = row.enrichmentState, row.enrichmentError
			if p.State == "blocked" && code == content.CodeRebuildRequired {
				var complete bool
				if err := q.QueryRow(ctx, `SELECT `+servedVectorsCompleteSQL+` FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2`, org, r.VersionID).Scan(&complete); err != nil {
					return r, err
				}
				if complete {
					p.State, code = "idle", ""
				}
			}
			if p.State != "idle" {
				p.Phase = "enrichment"
			}
		} else if p.State != "idle" {
			p.Phase = "baseline"
		}
		r.Availability = &a
		r.Processing = p
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
		if enrichmentBlocked(p) && len(row.enrichmentRaw) > 0 {
			var reason content.Diagnostic
			if err := json.Unmarshal(row.enrichmentRaw, &reason); err != nil {
				return r, err
			}
			r.Diagnostics = append(r.Diagnostics, content.Diagnostic{Code: reason.Code, Message: reason.Message, Retryable: false})
			return r, nil
		}
	}
	if code != "" {
		r.Diagnostics = append(r.Diagnostics, content.Diagnostic{Code: code, Message: "Processing requires attention or retry", Retryable: r.Processing.State == "retrying"})
	}
	return r, nil
}

// HasReceipt reports whether an ingestion idempotency key was accepted in the
// Organization; connectors use it to skip unchanged items before downloading.
func (s ReceiptStore) HasReceipt(ctx context.Context, org, key string) (bool, error) {
	var exists bool
	err := database(ctx, s.Pool).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ingestion_receipts WHERE organization=$1 AND id=$2)`, org, content.StableID("receipt", org, "ingestion", key)).Scan(&exists)
	return exists, err
}

func (s ReceiptStore) Receipt(ctx context.Context, org, id string) (content.Receipt, error) {
	var row replayReceiptRow
	args := []any{org, id}
	acl := visibilityPredicate(ctx, &args, "r")
	db := database(ctx, s.Pool)
	err := db.QueryRow(ctx, `SELECT rc.id,rc.state,coalesce(rc.outcome,''),rc.record_id,coalesce(rc.version_id,''),
 r.corpus_id,r.namespace,r.record_key,rc.processing,rc.error_code,
 coalesce(v.baseline_ready,false),coalesce(v.quarantined,false),`+recordGoneSQL+`,
 coalesce(r.current_version_id=v.id,false),coalesce(v.processing,''),coalesce(v.error_code,''),
 coalesce(v.enrichment_state,''),coalesce(v.enrichment_error,''),v.enrichment_reason
 FROM ingestion_receipts rc
 JOIN records r ON (r.organization,r.id)=(rc.organization,rc.record_id)
 LEFT JOIN record_versions v ON rc.version_id IS NOT NULL AND (v.organization,v.id)=(rc.organization,rc.version_id)
 WHERE rc.organization=$1 AND rc.id=$2`+acl, args...).Scan(
		&row.receipt.ID, &row.receipt.State, &row.receipt.Outcome, &row.receipt.RecordID, &row.receipt.VersionID,
		&row.receipt.Source.CorpusID, &row.receipt.Source.Namespace, &row.receipt.Source.RecordKey,
		&row.receipt.Processing.State, &row.code,
		&row.baseline, &row.quarantined, &row.withdrawn, &row.current,
		&row.vProcessing, &row.vErrorCode,
		&row.enrichmentState, &row.enrichmentError, &row.enrichmentRaw,
	)
	if err != nil {
		return content.Receipt{Diagnostics: []content.Diagnostic{}}, notFound(err)
	}
	return s.hydrateReceipt(ctx, db, org, row)
}
