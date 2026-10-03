package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ContentStore struct{ Pool *pgxpool.Pool }

func lockJournal(ctx context.Context, tx pgx.Tx, org string) error {
	if err := lockProjectionRouting(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO organization_journals(organization) VALUES($1) ON CONFLICT DO NOTHING", org); err != nil {
		return err
	}
	var seq int64
	return tx.QueryRow(ctx, "SELECT last_sequence FROM organization_journals WHERE organization=$1 FOR UPDATE", org).Scan(&seq)
}

// eventInput is one public journal fact. VersionID is internal: it names the
// Record Version a monitoring trigger event concerns and is never exposed.
type eventInput struct{ Organization, CorpusID, Kind, Resource, ResourceID, MutationID, VersionID string }

// eventID is the stable public identity of an event.
func eventID(event eventInput) string {
	identity := event.MutationID
	if identity == "" {
		identity = event.ResourceID
	}
	return content.StableID("event", event.Organization, event.Kind, event.Resource, identity)
}

func appendEvent(ctx context.Context, tx pgx.Tx, event eventInput) error {
	_, err := appendEventAt(ctx, tx, event)
	return err
}

// appendEventAt appends one event and returns its journal position. Callers
// hold the Organization journal lock, so positions commit in order.
func appendEventAt(ctx context.Context, tx pgx.Tx, event eventInput) (int64, error) {
	var sequence int64
	if err := tx.QueryRow(ctx, "UPDATE organization_journals SET last_sequence=last_sequence+1 WHERE organization=$1 RETURNING last_sequence", event.Organization).Scan(&sequence); err != nil {
		return 0, err
	}
	_, err := tx.Exec(ctx, `INSERT INTO change_events(organization,sequence,event_id,corpus_id,event_type,resource_type,resource_id,record_version_id) VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''))`, event.Organization, sequence, eventID(event), event.CorpusID, event.Kind, event.Resource, event.ResourceID, event.VersionID)
	return sequence, err
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return corpus.ErrNotFound
	}
	return err
}

func (s ContentStore) Work(ctx context.Context, org, id string) (content.Work, bool, error) {
	w := content.Work{Organization: org, ReceiptID: id}
	var command []byte
	var state string
	err := s.Pool.QueryRow(ctx, `SELECT r.record_id,a.command,r.state,r.slot,r.digest,a.version_id,a.acceptance_order,a.source_position,coalesce(a.predecessor_id,''),a.source_media_type FROM ingestion_receipts r JOIN accepted_revisions a ON (a.organization,a.record_id,a.slot)=(r.organization,r.record_id,r.slot) WHERE r.organization=$1 AND r.id=$2`, org, id).Scan(&w.RecordID, &command, &state, &w.Slot, &w.Digest, &w.VersionID, &w.Order, &w.Position, &w.PredecessorID, &w.Command.SourceMediaType)
	if err != nil {
		return w, false, err
	}
	err = json.Unmarshal(command, &w.Command)
	return w, state == "resolved", err
}
func (s ContentStore) Progress(ctx context.Context, org, id, state, code string) error {
	_, err := s.Pool.Exec(ctx, "UPDATE ingestion_receipts SET processing=$3,error_code=$4 WHERE organization=$1 AND id=$2 AND state='pending'", org, id, state, code)
	return err
}
func (s ContentStore) Publish(ctx context.Context, w content.Work, publication content.Publication) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, w.Organization); err != nil {
		return err
	}
	var state string
	if err = tx.QueryRow(ctx, "SELECT state FROM ingestion_receipts WHERE organization=$1 AND id=$2 FOR UPDATE", w.Organization, w.ReceiptID).Scan(&state); err != nil {
		return err
	}
	if state == "resolved" {
		return tx.Commit(ctx)
	}
	var reserved string
	var withdrawn bool
	err = tx.QueryRow(ctx, `SELECT a.digest,r.withdrawn FROM accepted_revisions a JOIN records r ON (r.organization,r.id)=(a.organization,a.record_id) WHERE a.organization=$1 AND a.record_id=$2 AND a.slot=$3`, w.Organization, w.RecordID, w.Slot).Scan(&reserved, &withdrawn)
	if err != nil {
		return err
	}
	outcome := "created"
	versionID := w.VersionID
	code := ""
	processing := "idle"
	if reserved != w.Digest || withdrawn {
		outcome = "conflict"
		versionID = ""
		code = "source_revision_conflict"
		processing = "blocked"
	} else {
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM record_versions WHERE organization=$1 AND id=$2)", w.Organization, w.VersionID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			outcome = "duplicate"
		} else {
			blobs := make([]content.Blob, 0, len(publication.Parts)+2)
			blobs = append(blobs, publication.Normalized, publication.Manifest)
			for _, part := range publication.Parts {
				blobs = append(blobs, part.Blob)
			}
			for _, b := range blobs {
				_, err = tx.Exec(ctx, "INSERT INTO content_blobs VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING", w.Organization, content.StableID("blob", w.Organization, b.SHA256), b.Key, b.SHA256, b.Size)
				if err != nil {
					return err
				}
			}
			provenance, err := json.Marshal(w.Command.Provenance)
			if err != nil {
				return err
			}
			extensions := w.Command.Extensions
			if extensions == nil {
				extensions = content.Extensions{}
			}
			extensionsJSON, err := json.Marshal(extensions)
			if err != nil {
				return err
			}
			// A quarantined publication is held in the same transaction: no
			// worker can observe it as materialized and process it.
			var quarantine []byte
			processing, code := "queued", ""
			if q := publication.Quarantine; q != nil {
				if quarantine, err = json.Marshal(q); err != nil {
					return err
				}
				processing, code = "blocked", q.Code
			}
			_, err = tx.Exec(ctx, `INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,source_position,predecessor_id,text_blob_id,manifest_blob_id,provenance,extensions,quarantined,processing,error_code,quarantine,materialized_at,quarantined_at,quarantine_stage) VALUES($1,$2,$3,$4,$5,$6,$7,nullif($8,''),$9,$10,$11,$12,$13,$14,$15,$16,clock_timestamp(),CASE WHEN $13 THEN clock_timestamp() END,CASE WHEN $13 THEN 'normalization' END)`, w.Organization, w.VersionID, w.RecordID, w.Slot, w.Digest, w.Order, w.Position, w.PredecessorID, content.StableID("blob", w.Organization, publication.Normalized.SHA256), content.StableID("blob", w.Organization, publication.Manifest.SHA256), provenance, extensionsJSON, publication.Quarantine != nil, processing, code, quarantine)
			if err != nil {
				return err
			}
			for _, part := range publication.Parts {
				if _, err = tx.Exec(ctx, "INSERT INTO version_parts VALUES($1,$2,$3,$4,$5)", w.Organization, w.VersionID, part.Key, part.Role, content.StableID("blob", w.Organization, part.Blob.SHA256)); err != nil {
					return err
				}
			}
			if err = appendEvent(ctx, tx, eventInput{Organization: w.Organization, CorpusID: w.Command.Source.CorpusID, Kind: "record.materialized", Resource: "record", ResourceID: w.RecordID, MutationID: w.VersionID}); err != nil {
				return err
			}
			if q := publication.Quarantine; q != nil {
				if err = appendEvent(ctx, tx, eventInput{Organization: w.Organization, CorpusID: w.Command.Source.CorpusID, Kind: "record.quarantined", Resource: "record", ResourceID: w.RecordID, MutationID: content.StableID("quarantine", w.VersionID, q.Code)}); err != nil {
					return err
				}
			}

		}
	}
	_, err = tx.Exec(ctx, "UPDATE ingestion_receipts SET state='resolved',outcome=$3,version_id=nullif($4,''),processing=$5,error_code=$6 WHERE organization=$1 AND id=$2", w.Organization, w.ReceiptID, outcome, versionID, processing, code)
	if err != nil {
		return err
	}
	if err = appendEvent(ctx, tx, eventInput{Organization: w.Organization, CorpusID: w.Command.Source.CorpusID, Kind: "receipt.resolved", Resource: "receipt", ResourceID: w.ReceiptID}); err != nil {
		return err
	}
	if versionID != "" {
		serves, err := pinnedOwnerServes(ctx, tx, w.Organization, versionID)
		if err != nil {
			return err
		}
		if !serves {
			if err = queueServingProjection(ctx, tx, w.Organization, versionID); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

// Claim leases a bounded batch in queue arrival order. A lease survives process
// restarts; a lost acknowledgement may retry the same durable workflow identity.
func (s ContentStore) Claim(ctx context.Context, limit int) ([]content.Dispatch, error) {
	rows, err := s.Pool.Query(ctx, `WITH pending AS (
 SELECT organization,receipt_id FROM ingestion_outbox
 WHERE NOT dispatched AND lease_until<now()
 ORDER BY enqueued_at,organization,receipt_id
 FOR UPDATE SKIP LOCKED LIMIT $1
 ), claimed AS (
 UPDATE ingestion_outbox o SET lease_until=now()+interval '5 seconds'
 FROM pending p WHERE (o.organization,o.receipt_id)=(p.organization,p.receipt_id)
 RETURNING o.organization,o.receipt_id,o.enqueued_at
 ) SELECT organization,receipt_id FROM claimed ORDER BY enqueued_at,organization,receipt_id`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var batch []content.Dispatch
	for rows.Next() {
		var d content.Dispatch
		if err := rows.Scan(&d.Organization, &d.ReceiptID); err != nil {
			return nil, err
		}
		batch = append(batch, d)
	}
	return batch, rows.Err()
}

func (s ContentStore) Dispatched(ctx context.Context, d content.Dispatch) error {
	// Receipts and the change journal hold audit facts; delivery intents can go
	// once Temporal has durably accepted their stable workflow identity.
	_, err := s.Pool.Exec(ctx, "DELETE FROM ingestion_outbox WHERE organization=$1 AND receipt_id=$2", d.Organization, d.ReceiptID)
	return err
}
