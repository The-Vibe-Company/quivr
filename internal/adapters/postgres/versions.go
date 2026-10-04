package postgres

import (
	"context"
	"encoding/json"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/jackc/pgx/v5/pgxpool"
)

// VersionStore reads canonical Versions, availability and processing diagnostics.
type VersionStore struct{ Pool *pgxpool.Pool }

var _ content.VersionReader = VersionStore{}

func (s VersionStore) Version(ctx context.Context, org, recordID, id string) (content.StoredVersion, error) {
	v := content.StoredVersion{}
	var provenance, extensions []byte
	err := s.Pool.QueryRow(ctx, `SELECT v.record_id,v.id,r.corpus_id,t.object_key,t.sha256,t.byte_length,m.object_key,m.sha256,m.byte_length,v.provenance,v.extensions,rc.accepted_at,v.materialized_at,v.segmented_at,v.retrieval_ready_at,v.enriched_at,v.evaluated_at,v.quarantined_at,r.withdrawn_at,COALESCE(ar.source_media_type,'text/plain') FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) JOIN content_blobs t ON (t.organization,t.blob_id)=(v.organization,v.text_blob_id) JOIN content_blobs m ON (m.organization,m.blob_id)=(v.organization,v.manifest_blob_id) LEFT JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot) LEFT JOIN ingestion_receipts rc ON rc.organization=v.organization AND rc.record_id=v.record_id AND rc.acceptance_order=v.acceptance_order WHERE v.organization=$1 AND v.record_id=$2 AND v.id=$3`, org, recordID, id).Scan(&v.RecordID, &v.ID, &v.CorpusID, &v.TextBlob.Key, &v.TextBlob.SHA256, &v.TextBlob.Size, &v.ManifestBlob.Key, &v.ManifestBlob.SHA256, &v.ManifestBlob.Size, &provenance, &extensions, &v.AcceptedAt, &v.Steps.Materialized, &v.Steps.Segmented, &v.Steps.RetrievalReady, &v.Steps.Enriched, &v.Steps.Evaluated, &v.Steps.Quarantined, &v.Steps.Withdrawn, &v.SourceMediaType)
	v.Steps.Accepted = v.AcceptedAt
	v.Steps = utcSteps(v.Steps)
	v.AcceptedAt = v.Steps.Accepted
	if err == nil {
		err = json.Unmarshal(provenance, &v.Provenance)
	}
	if err == nil {
		err = json.Unmarshal(extensions, &v.Extensions)
	}
	var code string
	if err == nil {
		v.Availability, v.Processing, code, err = s.VersionStatus(ctx, org, id)
	}
	if err == nil {
		v.Diagnostics, err = s.versionDiagnostics(ctx, org, id, v.Availability.State == "quarantined", enrichmentBlocked(v.Processing), code)
	}
	return v, notFound(err)
}

// versionDiagnostics explains a Version: its structured quarantine reason
// (or, for quarantines that predate it, its code) or the reason its
// enrichment stopped with, then the failure a normalizer fallback records and
// a recorded normalizer conflict.
func (s VersionStore) versionDiagnostics(ctx context.Context, org, id string, quarantined, enrichmentStopped bool, code string) ([]content.Diagnostic, error) {
	out := []content.Diagnostic{}
	rows, err := s.Pool.Query(ctx, `SELECT diagnostic FROM ingestion_evaluations WHERE organization=$1 AND version_id=$2 AND state='failed' AND diagnostic IS NOT NULL ORDER BY plugin_id,created_at`, org, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw []byte
		var d content.Diagnostic
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal(raw, &d); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, d)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if enrichmentStopped {
		reason, err := s.enrichmentReason(ctx, org, id)
		if err != nil {
			return nil, err
		}
		if reason != nil {
			out = append(out, *reason)
		}
	}
	if quarantined {
		var raw []byte
		if err := s.Pool.QueryRow(ctx, `SELECT quarantine FROM record_versions WHERE organization=$1 AND id=$2`, org, id).Scan(&raw); err != nil {
			return nil, err
		}
		d := content.Diagnostic{Code: code, Message: "Processing could not complete safely; the Version is withheld from search."}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &d); err != nil {
				return nil, err
			}
		}
		if d.Code != "" {
			out = append(out, d)
		}
	}
	n, found, err := (NormalizationStore{Pool: s.Pool}).Normalized(ctx, org, id)
	if err != nil || !found || n.Failed() {
		return out, err
	}
	return append(out, n.Diagnostics()...), nil
}

// enrichmentBlocked reports a searchable Version whose enrichment stopped.
func enrichmentBlocked(p content.Processing) bool {
	return p.State == "blocked" && p.Phase == "enrichment"
}

// enrichmentReason reads the reason a Version's enrichment stopped with; nil
// when it stopped with a code only, such as derivation_conflict.
func (s VersionStore) enrichmentReason(ctx context.Context, org, id string) (*content.Diagnostic, error) {
	var raw []byte
	if err := s.Pool.QueryRow(ctx, `SELECT enrichment_reason FROM record_versions WHERE organization=$1 AND id=$2`, org, id).Scan(&raw); err != nil || len(raw) == 0 {
		return nil, err
	}
	var d content.Diagnostic
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return &d, nil
}
func (s VersionStore) VersionStatus(ctx context.Context, org, id string) (content.Availability, content.Processing, string, error) {
	var a content.Availability
	var p content.Processing
	var code string
	var baseline, quarantine, withdrawn bool
	err := s.Pool.QueryRow(ctx, `SELECT v.baseline_ready,v.quarantined,`+recordGoneSQL+`,coalesce(r.current_version_id=v.id,false),v.processing,v.error_code FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2`, org, id).Scan(&baseline, &quarantine, &withdrawn, &a.Current, &p.State, &code)
	a.State = "materialized"
	if p.State == "running" || p.State == "retrying" {
		a.State = "building_baseline"
	}
	if baseline {
		a.State = "retrieval_ready"
	}
	if quarantine {
		a.State = "quarantined"
	}
	a.Current = a.Current && !withdrawn && !quarantine
	a.Searchable = baseline && a.Current
	if baseline && !quarantine {
		if err = s.Pool.QueryRow(ctx, `SELECT enrichment_state,enrichment_error FROM record_versions WHERE organization=$1 AND id=$2`, org, id).Scan(&p.State, &code); err != nil {
			return a, p, code, err
		}
		if p.State != "idle" {
			p.Phase = "enrichment"
		}
		return a, p, code, err
	}
	if p.State != "idle" {
		p.Phase = "baseline"
	}
	return a, p, code, err
}
