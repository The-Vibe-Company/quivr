package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaterializationStore persists receipt work and canonical publication.
type MaterializationStore struct{ Pool *pgxpool.Pool }

func lockJournal(ctx context.Context, tx pgx.Tx, org string) error {
	// Keep the routing-before-journal lock order, sending its statements in
	// one round trip. This avoids extending every writer's critical section
	// with network waits and does not create no-op journal row versions.
	return tx.SendBatch(ctx, journalBatch(org)).Close()
}

func journalBatch(org string) *pgx.Batch {
	batch := &pgx.Batch{}
	batch.Queue("SELECT pg_advisory_xact_lock_shared($1)", projectionRoutingLock)
	batch.Queue("INSERT INTO organization_journals(organization) VALUES($1) ON CONFLICT DO NOTHING", org)
	batch.Queue("SELECT last_sequence FROM organization_journals WHERE organization=$1 FOR UPDATE", org)
	return batch
}

// readJournal sends the fence and its first guarded read together. They are
// separate statements: the read gets a fresh snapshot after the lock has been
// acquired, including the preceding writer's commit under READ COMMITTED.
func readJournal(ctx context.Context, tx pgx.Tx, org, query string, args []any, destinations ...any) error {
	batch := journalBatch(org)
	batch.Queue(query, args...)
	results := tx.SendBatch(ctx, batch)
	defer results.Close()
	for range 3 {
		if _, err := results.Exec(); err != nil {
			return err
		}
	}
	if err := results.QueryRow().Scan(destinations...); err != nil {
		return err
	}
	return results.Close()
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
const appendEventSQL = `WITH position AS (
UPDATE organization_journals SET last_sequence=last_sequence+1 WHERE organization=$1 RETURNING last_sequence
) INSERT INTO change_events(organization,sequence,event_id,corpus_id,event_type,resource_type,resource_id,record_version_id,trace_context)
SELECT $1,last_sequence,$2,$3,$4,$5,$6,NULLIF($7,''),$8 FROM position RETURNING sequence`

func eventArguments(ctx context.Context, event eventInput) []any {
	return []any{event.Organization, eventID(event), event.CorpusID, event.Kind, event.Resource, event.ResourceID, event.VersionID, telemetry.Encode(ctx)}
}

func appendEventAt(ctx context.Context, tx pgx.Tx, event eventInput) (int64, error) {
	var sequence int64
	err := tx.QueryRow(ctx, appendEventSQL, eventArguments(ctx, event)...).Scan(&sequence)
	return sequence, err
}

func queueEvent(ctx context.Context, batch *pgx.Batch, event eventInput) {
	batch.Queue(appendEventSQL, eventArguments(ctx, event)...)
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return corpus.ErrNotFound
	}
	return err
}

func (s MaterializationStore) Work(ctx context.Context, org, id string) (content.Work, bool, error) {
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
func (s MaterializationStore) Progress(ctx context.Context, org, id, state, code string) error {
	_, err := s.Pool.Exec(ctx, "UPDATE ingestion_receipts SET processing=$3,error_code=$4 WHERE organization=$1 AND id=$2 AND state='pending'", org, id, state, code)
	return err
}
func (s MaterializationStore) Publish(ctx context.Context, w content.Work, publication content.Publication) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var state string
	var reserved *string
	var withdrawn, exists bool
	err = readJournal(ctx, tx, w.Organization, `SELECT rc.state,
 (SELECT a.digest FROM accepted_revisions a WHERE a.organization=$1 AND a.record_id=$3 AND a.slot=$4),
 coalesce((SELECT r.withdrawn FROM records r WHERE r.organization=$1 AND r.id=$3),false),
 EXISTS(SELECT 1 FROM record_versions WHERE organization=$1 AND id=$5)
 FROM ingestion_receipts rc WHERE rc.organization=$1 AND rc.id=$2 FOR UPDATE OF rc`,
		[]any{w.Organization, w.ReceiptID, w.RecordID, w.Slot, w.VersionID}, &state, &reserved, &withdrawn, &exists)
	if err != nil {
		return err
	}
	if state == "resolved" {
		return tx.Commit(ctx)
	}
	if reserved == nil {
		return pgx.ErrNoRows
	}
	writes := &pgx.Batch{}
	outcome := "created"
	versionID := w.VersionID
	code := ""
	processing := "idle"
	if *reserved != w.Digest || withdrawn {
		outcome = "conflict"
		versionID = ""
		code = "source_revision_conflict"
		processing = "blocked"
	} else {
		if exists {
			outcome = "duplicate"
		} else {
			blobs := make([]content.Blob, 0, len(publication.Parts)+2)
			blobs = append(blobs, publication.Normalized, publication.Manifest)
			for _, part := range publication.Parts {
				blobs = append(blobs, part.Blob)
			}
			for _, b := range blobs {
				writes.Queue("INSERT INTO content_blobs VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING", w.Organization, content.StableID("blob", w.Organization, b.SHA256), b.Key, b.SHA256, b.Size)
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
			writes.Queue(`INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,source_position,predecessor_id,text_blob_id,manifest_blob_id,provenance,extensions,quarantined,processing,error_code,quarantine,materialized_at,quarantined_at,quarantine_stage) VALUES($1,$2,$3,$4,$5,$6,$7,nullif($8,''),$9,$10,$11,$12,$13,$14,$15,$16,clock_timestamp(),CASE WHEN $13 THEN clock_timestamp() END,CASE WHEN $13 THEN 'normalization' END)`, w.Organization, w.VersionID, w.RecordID, w.Slot, w.Digest, w.Order, w.Position, w.PredecessorID, content.StableID("blob", w.Organization, publication.Normalized.SHA256), content.StableID("blob", w.Organization, publication.Manifest.SHA256), provenance, extensionsJSON, publication.Quarantine != nil, processing, code, quarantine)
			for _, part := range publication.Parts {
				writes.Queue("INSERT INTO version_parts VALUES($1,$2,$3,$4,$5)", w.Organization, w.VersionID, part.Key, part.Role, content.StableID("blob", w.Organization, part.Blob.SHA256))
			}
			queueEvent(ctx, writes, eventInput{Organization: w.Organization, CorpusID: w.Command.Source.CorpusID, Kind: "record.materialized", Resource: "record", ResourceID: w.RecordID, MutationID: w.VersionID})
			if q := publication.Quarantine; q != nil {
				queueEvent(ctx, writes, eventInput{Organization: w.Organization, CorpusID: w.Command.Source.CorpusID, Kind: "record.quarantined", Resource: "record", ResourceID: w.RecordID, MutationID: content.StableID("quarantine", w.VersionID, q.Code)})
			}

		}
	}
	writes.Queue("UPDATE ingestion_receipts SET state='resolved',outcome=$3,version_id=nullif($4,''),processing=$5,error_code=$6 WHERE organization=$1 AND id=$2", w.Organization, w.ReceiptID, outcome, versionID, processing, code)
	queueEvent(ctx, writes, eventInput{Organization: w.Organization, CorpusID: w.Command.Source.CorpusID, Kind: "receipt.resolved", Resource: "receipt", ResourceID: w.ReceiptID})
	if err = tx.SendBatch(ctx, writes).Close(); err != nil {
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
