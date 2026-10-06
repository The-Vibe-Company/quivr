package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/jackc/pgx/v5"
)

// ClaimRelated leases up to limit more due pending evaluation intents of
// first's Record Version whose Subscription Version pins the same evaluator
// plugin id and version. Intents leased by another worker or waiting for a
// retry are skipped; first itself is never returned.
func (s EvaluationStore) ClaimRelated(ctx context.Context, first monitoring.Intent, evaluator monitoring.Evaluator, limit int, lease time.Duration) ([]monitoring.Intent, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.Pool.Query(ctx, `UPDATE evaluation_intents i SET lease_until=now()+make_interval(secs => $7::double precision)
FROM (SELECT e.organization,e.subscription_version_id,e.sequence FROM evaluation_intents e
  JOIN subscription_versions v ON (v.organization,v.id)=(e.organization,e.subscription_version_id)
  WHERE e.organization=$1 AND e.record_version_id=$2 AND e.kind='evaluation' AND e.state='pending' AND e.available_at<=now() AND e.lease_until<now() AND NOT `+evaluatingSibling("e")+`
    AND NOT (e.subscription_version_id=$3 AND e.sequence=$4)
    AND v.evaluator->>'plugin_id'=$5 AND v.evaluator->>'version'=$6
  ORDER BY e.available_at,e.sequence LIMIT $8 FOR UPDATE OF e SKIP LOCKED) due
WHERE (i.organization,i.subscription_version_id,i.sequence)=(due.organization,due.subscription_version_id,due.sequence)
RETURNING i.kind,i.organization,i.subscription_id,i.subscription_version_id,i.sequence,i.corpus_id,i.record_id,i.record_version_id,i.attempts,i.trace_context`,
		first.Organization, first.VersionID, first.SubscriptionVersionID, first.Sequence, evaluator.PluginID, evaluator.Version, lease.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (monitoring.Intent, error) {
		var in monitoring.Intent
		return in, r.Scan(&in.Kind, &in.Organization, &in.SubscriptionID, &in.SubscriptionVersionID, &in.Sequence, &in.CorpusID, &in.RecordID, &in.VersionID, &in.Attempts, &in.TraceContext)
	})
}

// RecordMetadata reads what a rule may test besides the text of a Record
// Version: its Source Namespace, Record Key and Source Position, the
// acceptance time of the revision it publishes, its provenance and its
// extensions. The origin is connector only for a revision accepted under the
// idempotency-key family reserved to Connector Instances, which clients
// cannot use.
func (s EvaluationStore) RecordMetadata(ctx context.Context, org, recordID, versionID string) (monitoring.RecordMetadata, error) {
	var m monitoring.RecordMetadata
	var provenance, extensions []byte
	var acceptedAt *time.Time
	var requestKey *string
	err := s.Pool.QueryRow(ctx, `SELECT r.namespace,r.record_key,v.source_position,v.provenance,v.extensions,rc.accepted_at,rc.request_key
FROM record_versions v
JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
LEFT JOIN ingestion_receipts rc ON rc.organization=v.organization AND rc.record_id=v.record_id AND rc.acceptance_order=v.acceptance_order
WHERE v.organization=$1 AND v.record_id=$2 AND v.id=$3`, org, recordID, versionID).Scan(&m.Source.Namespace, &m.Source.RecordKey, &m.Source.Position, &provenance, &extensions, &acceptedAt, &requestKey)
	if err != nil {
		return m, notFound(err)
	}
	if acceptedAt != nil {
		m.AcceptedAt = acceptedAt.UTC()
	}
	var p map[string]any
	if len(provenance) > 0 {
		if err = json.Unmarshal(provenance, &p); err != nil {
			return m, err
		}
	}
	text := func(v map[string]any, key string) string { s, _ := v[key].(string); return s }
	m.Provenance = monitoring.RecordProvenance{Origin: monitoring.OriginClient, Producer: text(p, "producer"), ProducerVersion: text(p, "producer_version")}
	if requestKey != nil && connectors.IsConnectorKey(*requestKey) {
		kind, _, _ := strings.Cut(m.Provenance.ProducerVersion, "/")
		m.Provenance.Origin = monitoring.OriginConnector
		m.Provenance.Connector = &monitoring.ConnectorOrigin{InstanceID: m.Provenance.Producer, Kind: kind}
	}
	if n, ok := p["normalization"].(map[string]any); ok {
		_, fallback := n["fallback"].(map[string]any)
		m.Provenance.Normalization = &monitoring.NormalizationOrigin{PluginID: text(n, "plugin_id"), PluginVersion: text(n, "plugin_version"), Contribution: text(n, "contribution"), Fallback: fallback}
	}
	if len(extensions) > 0 {
		if err = json.Unmarshal(extensions, &m.Extensions); err != nil {
			return m, err
		}
		if len(m.Extensions) == 0 {
			m.Extensions = nil
		}
	}
	return m, nil
}
