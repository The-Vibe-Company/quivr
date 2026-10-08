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
	if group := journalGroupOf(ctx); group != nil {
		if !group.locked || group.organization != org {
			return content.ErrInvalid
		}
		return tx.QueryRow(ctx, query, args...).Scan(destinations...)
	}
	batch := journalBatch(org)
	batch.Queue(query, args...)
	results := tx.SendBatch(ctx, batch)
	defer results.Close()
	for range batch.Len() - 1 {
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

// The append, its Record's queue observation and the checkpoint acknowledgement
// travel in one round trip: the caller holds the journal lock until commit.
func appendEventAt(ctx context.Context, tx pgx.Tx, event eventInput) (int64, error) {
	var sequence int64
	if journalGroupOf(ctx) != nil {
		err := tx.QueryRow(ctx, appendEventSQL, eventArguments(ctx, event)...).Scan(&sequence)
		if err == nil && event.Resource == "record" {
			err = observeQueueRecords(ctx, tx, []string{event.Organization}, []string{event.ResourceID})
		}
		if err == nil {
			_, err = tx.Exec(ctx, acknowledgeQueueJournalSQL, event.Organization, 1)
		}
		return sequence, err
	}
	batch := &pgx.Batch{}
	queueEvent(ctx, batch, event)
	results := tx.SendBatch(ctx, batch)
	defer results.Close()
	if err := results.QueryRow().Scan(&sequence); err != nil {
		return 0, err
	}
	for range batch.Len() - 1 {
		if _, err := results.Exec(); err != nil {
			return 0, err
		}
	}
	return sequence, results.Close()
}

// queueEvent appends one event, observes its Record and acknowledges the
// observation checkpoint. Consecutive events in one batch share a single
// acknowledgement of their contiguous range: it holds exactly when the chain of
// one-event acknowledgements would. A trailing observation of the same Record
// moves after the next append, which changes no canonical state it reads.
func queueEvent(ctx context.Context, batch *pgx.Batch, event eventInput) {
	if group := journalGroupOf(ctx); group != nil {
		group.events = append(group.events, journalEvent{event, telemetry.Encode(ctx)})
		if event.Resource == "record" {
			group.records[event.ResourceID] = true
		}
		return
	}
	acknowledged := 0
	queued := batch.QueuedQueries
	if n := len(queued); n > 0 && queued[n-1].SQL == acknowledgeQueueJournalSQL && queued[n-1].Arguments[0] == event.Organization {
		if count, ok := queued[n-1].Arguments[1].(int); ok {
			acknowledged = count
			queued = queued[:n-1]
			if event.Resource == "record" && trailingObservation(queued, event.Organization, event.ResourceID) {
				queued = queued[:len(queued)-len(recordObservationSQL)]
			}
			batch.QueuedQueries = queued
		}
	}
	batch.Queue(appendEventSQL, eventArguments(ctx, event)...)
	if event.Resource == "record" {
		queueRecordObservations(batch, []string{event.Organization}, []string{event.ResourceID})
	}
	batch.Queue(acknowledgeQueueJournalSQL, event.Organization, acknowledged+1)
}

// trailingObservation reports whether queued ends with the observation of one Record.
func trailingObservation(queued []*pgx.QueuedQuery, org, record string) bool {
	if len(queued) < len(recordObservationSQL) {
		return false
	}
	for i, q := range queued[len(queued)-len(recordObservationSQL):] {
		if q.SQL != recordObservationSQL[i] || len(q.Arguments) != 2 {
			return false
		}
		orgs, okOrg := q.Arguments[0].([]string)
		records, okRecord := q.Arguments[1].([]string)
		if !okOrg || !okRecord || len(orgs) != 1 || len(records) != 1 || orgs[0] != org || records[0] != record {
			return false
		}
	}
	return true
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
	if publication.Quarantine == nil {
		if handled, err := content.EnqueueIngestion(ctx, content.IngestionCommit{Kind: content.CommitMaterialization, Organization: w.Organization, RecordID: w.RecordID, Publication: content.PublicationCommit{Work: w, Publication: publication}}); handled {
			return err
		}
	}
	err := retryJournalWrite(ctx, "Publish", func(ctx context.Context) error {
		return s.publishAttempt(ctx, w, publication)
	})
	return err
}

func (s MaterializationStore) publishAttempt(ctx context.Context, w content.Work, publication content.Publication) error {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	finish, err := preparePublication(ctx, tx, w, publication)
	if err != nil {
		return err
	}
	if finish == nil {
		return nil
	}
	if err = finish(); err != nil {
		if errors.Is(err, errJournalReplay) {
			return nil
		}
		return err
	}
	return tx.Commit(ctx)
}

func preparePublication(ctx context.Context, tx pgx.Tx, w content.Work, publication content.Publication) (journalFinish, error) {
	var err error

	var state string
	var reserved *string
	var withdrawn, exists, created bool
	guard := `SELECT rc.state,
 (SELECT a.digest FROM accepted_revisions a WHERE a.organization=$1 AND a.record_id=$3 AND a.slot=$4),
 coalesce((SELECT r.withdrawn FROM records r WHERE r.organization=$1 AND r.id=$3),false),
 EXISTS(SELECT 1 FROM record_versions WHERE organization=$1 AND id=$5)
 FROM ingestion_receipts rc WHERE rc.organization=$1 AND rc.id=$2`
	args := []any{w.Organization, w.ReceiptID, w.RecordID, w.Slot, w.VersionID}
	read := func(fence bool) error {
		if fence {
			err := readJournal(ctx, tx, w.Organization, guard+" FOR UPDATE OF rc", args, &state, &reserved, &withdrawn, &exists)
			if err == nil && created {
				exists = false
			}
			return err
		}
		return tx.QueryRow(ctx, guard, args...).Scan(&state, &reserved, &withdrawn, &exists)
	}
	if err = lockProjectionRouting(ctx, tx); err != nil {
		return nil, err
	}
	if err = read(false); err != nil {
		return nil, err
	}
	// Resolved receipts are immutable. A committed replay needs no writes or
	// journal acquisition, and must not validate another publication's blobs.
	if state == "resolved" {
		return nil, nil
	}
	if journalGroupOf(ctx) != nil && (reserved == nil || *reserved != w.Digest || withdrawn || exists) {
		return nil, ErrGenerationChanged
	}
	var stage pgx.Tx
	if reserved != nil && *reserved == w.Digest && !withdrawn && !exists {
		stage, err = prepareJournal(ctx, tx, func(stage pgx.Tx) error {
			var err error
			created, err = insertPublicationVersion(ctx, stage, w, publication)
			return err
		})
		if err != nil {
			return nil, err
		}
		if journalGroupOf(ctx) != nil && !created {
			return nil, ErrGenerationChanged
		}
	}
	return func() error {

		if err = read(true); err != nil {
			return err
		}
		if state == "resolved" {
			return errJournalReplay
		}
		// Discard prepared blobs on a duplicate or source conflict. Rolling back
		// releases the fence, so the guarded read must acquire a fresh one.
		if stage != nil && reserved != nil && (*reserved != w.Digest || withdrawn || exists) {
			if journalGroupOf(ctx) != nil {
				return ErrGenerationChanged
			}
			if err = stage.Rollback(ctx); err != nil {
				return err
			}
			created = false
			if err = read(true); err != nil {
				return err
			}
			if state == "resolved" {
				return nil
			}
			stage = nil
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
				if stage == nil {
					return ErrGenerationChanged
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
		return nil
	}, nil
}
