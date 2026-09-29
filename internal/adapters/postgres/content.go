package postgres

import (
	"bytes"
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
func (s ContentStore) Accept(ctx context.Context, scope corpus.Scope, c content.Command) (content.Receipt, error) {
	canonical, err := json.Marshal(c)
	if err != nil {
		return content.Receipt{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return content.Receipt{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, scope.Organization); err != nil {
		return content.Receipt{}, err
	}
	var previous []byte
	var receiptID string
	err = tx.QueryRow(ctx, "SELECT id,canonical_request FROM ingestion_receipts WHERE organization=$1 AND route_family='ingestion' AND request_key=$2", scope.Organization, c.Key).Scan(&receiptID, &previous)
	if err == nil {
		if !bytes.Equal(previous, canonical) {
			return content.Receipt{}, content.ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return content.Receipt{}, err
		}
		return s.Receipt(ctx, scope.Organization, receiptID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return content.Receipt{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM corpora WHERE organization=$1 AND id=$2)", scope.Organization, c.Source.CorpusID).Scan(&exists); err != nil {
		return content.Receipt{}, err
	}
	if !exists {
		return content.Receipt{}, corpus.ErrNotFound
	}
	recordID := content.StableID("record", scope.Organization, c.Source.CorpusID, c.Source.Namespace, c.Source.RecordKey)
	_, err = tx.Exec(ctx, `INSERT INTO records(organization,id,corpus_id,namespace,record_key) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, scope.Organization, recordID, c.Source.CorpusID, c.Source.Namespace, c.Source.RecordKey)
	if err != nil {
		return content.Receipt{}, err
	}
	var order int64
	var position string
	var withdrawn bool
	err = tx.QueryRow(ctx, `UPDATE records SET acceptance_order=acceptance_order+1 WHERE organization=$1 AND id=$2 RETURNING acceptance_order,desired_position,withdrawn`, scope.Organization, recordID).Scan(&order, &position, &withdrawn)
	if err != nil {
		return content.Receipt{}, err
	}
	if withdrawn {
		return content.Receipt{}, content.ErrConflict
	}
	digest := content.Digest(c)
	slot := "digest:" + digest
	if c.Revision != "" {
		slot = "revision:" + c.Revision
	}
	versionID := content.StableID("version", scope.Organization, recordID, slot)
	// Reserve the original revision order and lineage once, before any worker can publish it.
	var predecessor string
	err = tx.QueryRow(ctx, `SELECT version_id FROM accepted_revisions WHERE organization=$1 AND record_id=$2 AND ($3='' OR source_position='' OR length(source_position)<length($3) OR (length(source_position)=length($3) AND source_position COLLATE "C" < $3 COLLATE "C")) ORDER BY acceptance_order DESC LIMIT 1`, scope.Organization, recordID, c.Position).Scan(&predecessor)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return content.Receipt{}, err
	}
	reservation, err := tx.Exec(ctx, `INSERT INTO accepted_revisions(organization,record_id,slot,digest,version_id,acceptance_order,source_position,predecessor_id,command) VALUES($1,$2,$3,$4,$5,$6,$7,nullif($8,''),$9) ON CONFLICT DO NOTHING`, scope.Organization, recordID, slot, digest, versionID, order, c.Position, predecessor, canonical)
	if err != nil {
		return content.Receipt{}, err
	}
	if reservation.RowsAffected() == 1 && content.NewerPosition(c.Position, position) {
		_, err = tx.Exec(ctx, "UPDATE records SET desired_order=$3,desired_position=$4,desired_version_id=$5 WHERE organization=$1 AND id=$2", scope.Organization, recordID, order, c.Position, versionID)
		if err != nil {
			return content.Receipt{}, err
		}
	}
	receiptID = content.StableID("receipt", scope.Organization, "ingestion", c.Key)
	_, err = tx.Exec(ctx, `INSERT INTO ingestion_receipts(organization,id,request_key,canonical_request,command,corpus_id,record_id,acceptance_order,slot,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, scope.Organization, receiptID, c.Key, canonical, canonical, c.Source.CorpusID, recordID, order, slot, digest)
	if err != nil {
		return content.Receipt{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO ingestion_outbox(organization,receipt_id) VALUES($1,$2)", scope.Organization, receiptID); err != nil {
		return content.Receipt{}, err
	}
	if err = appendEvent(ctx, tx, eventInput{Organization: scope.Organization, CorpusID: c.Source.CorpusID, Kind: "receipt.pending", Resource: "receipt", ResourceID: receiptID}); err != nil {
		return content.Receipt{}, err
	}
	// Catalog invalidation belongs to the same acceptance transaction, including a Record first seen before materialization.
	if err = appendEvent(ctx, tx, eventInput{Organization: scope.Organization, CorpusID: c.Source.CorpusID, Kind: "record.accepted", Resource: "record", ResourceID: recordID, MutationID: receiptID}); err != nil {
		return content.Receipt{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return content.Receipt{}, err
	}
	return content.Receipt{ID: receiptID, State: "pending", RecordID: recordID, Source: c.Source, Processing: content.Processing{State: "queued", Phase: "materialization"}, Diagnostics: []content.Diagnostic{}, NewRevision: reservation.RowsAffected() == 1}, nil
}

// Withdraw commits the absorbing fence atomically: an existing or first-seen
// Record identity is marked withdrawn, its mutation fence advances, a single
// Tombstone is inserted, the Record event is appended and a resolved
// withdrawal_applied Receipt is stored. No projection work is dispatched; stale
// projection objects are hidden by canonical hydration.
func (s ContentStore) Withdraw(ctx context.Context, scope corpus.Scope, w content.Withdrawal) (content.Receipt, error) {
	canonical, err := json.Marshal(w)
	if err != nil {
		return content.Receipt{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return content.Receipt{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, scope.Organization); err != nil {
		return content.Receipt{}, err
	}
	var previous []byte
	var receiptID string
	err = tx.QueryRow(ctx, "SELECT id,canonical_request FROM ingestion_receipts WHERE organization=$1 AND route_family='withdrawal' AND request_key=$2", scope.Organization, w.Key).Scan(&receiptID, &previous)
	if err == nil {
		if !bytes.Equal(previous, canonical) {
			return content.Receipt{}, content.ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return content.Receipt{}, err
		}
		return s.Receipt(ctx, scope.Organization, receiptID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return content.Receipt{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM corpora WHERE organization=$1 AND id=$2)", scope.Organization, w.Source.CorpusID).Scan(&exists); err != nil {
		return content.Receipt{}, err
	}
	if !exists {
		return content.Receipt{}, corpus.ErrNotFound
	}
	recordID := content.StableID("record", scope.Organization, w.Source.CorpusID, w.Source.Namespace, w.Source.RecordKey)
	if _, err = tx.Exec(ctx, `INSERT INTO records(organization,id,corpus_id,namespace,record_key) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, scope.Organization, recordID, w.Source.CorpusID, w.Source.Namespace, w.Source.RecordKey); err != nil {
		return content.Receipt{}, err
	}
	var priorWithdrawn bool
	if err = tx.QueryRow(ctx, "SELECT withdrawn FROM records WHERE organization=$1 AND id=$2 FOR UPDATE", scope.Organization, recordID).Scan(&priorWithdrawn); err != nil {
		return content.Receipt{}, err
	}
	var order int64
	if err = tx.QueryRow(ctx, "UPDATE records SET acceptance_order=acceptance_order+1,withdrawn=true WHERE organization=$1 AND id=$2 RETURNING acceptance_order", scope.Organization, recordID).Scan(&order); err != nil {
		return content.Receipt{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO tombstones(organization,record_id) VALUES($1,$2) ON CONFLICT DO NOTHING", scope.Organization, recordID); err != nil {
		return content.Receipt{}, err
	}
	digest := content.Hash(canonical)
	receiptID = content.StableID("receipt", scope.Organization, "withdrawal", w.Key)
	_, err = tx.Exec(ctx, `INSERT INTO ingestion_receipts(organization,id,route_family,request_key,canonical_request,command,corpus_id,record_id,acceptance_order,slot,digest,state,outcome,processing,error_code) VALUES($1,$2,'withdrawal',$3,$4,$5,$6,$7,$8,$9,$10,'resolved','withdrawal_applied','idle','')`, scope.Organization, receiptID, w.Key, canonical, canonical, w.Source.CorpusID, recordID, order, "withdrawal:"+w.Key, digest)
	if err != nil {
		return content.Receipt{}, err
	}
	// Emit the Record event exactly once per identity, even across repeat
	// withdrawals with different request keys.
	if !priorWithdrawn {
		if err = appendEvent(ctx, tx, eventInput{Organization: scope.Organization, CorpusID: w.Source.CorpusID, Kind: "record.withdrawn", Resource: "record", ResourceID: recordID, MutationID: content.StableID("withdrawal", recordID)}); err != nil {
			return content.Receipt{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return content.Receipt{}, err
	}
	return content.Receipt{ID: receiptID, State: "resolved", Outcome: "withdrawal_applied", RecordID: recordID, Source: w.Source, Processing: content.Processing{State: "idle"}, Diagnostics: []content.Diagnostic{}}, nil
}
func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return corpus.ErrNotFound
	}
	return err
}

// HasReceipt reports whether an ingestion idempotency key was accepted in the
// Organization; connectors use it to skip unchanged items before downloading.
func (s ContentStore) HasReceipt(ctx context.Context, org, key string) (bool, error) {
	var exists bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ingestion_receipts WHERE organization=$1 AND id=$2)`, org, content.StableID("receipt", org, "ingestion", key)).Scan(&exists)
	return exists, err
}

func (s ContentStore) Receipt(ctx context.Context, org, id string) (content.Receipt, error) {
	r := content.Receipt{Diagnostics: []content.Diagnostic{}}
	var command []byte
	var code string
	err := s.Pool.QueryRow(ctx, `SELECT id,state,coalesce(outcome,''),record_id,coalesce(version_id,''),command,processing,error_code FROM ingestion_receipts WHERE organization=$1 AND id=$2`, org, id).Scan(&r.ID, &r.State, &r.Outcome, &r.RecordID, &r.VersionID, &command, &r.Processing.State, &code)
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
		a, p, statusCode, statusErr := s.VersionStatus(ctx, org, r.VersionID)
		if statusErr != nil {
			return r, statusErr
		}
		r.Availability = &a
		r.Processing = p
		code = statusCode
		if a.State == "quarantined" {
			diagnostics, err := s.versionDiagnostics(ctx, org, r.VersionID, true, code)
			if err != nil {
				return r, err
			}
			if len(diagnostics) > 0 {
				d := diagnostics[0]
				r.Diagnostics = append(r.Diagnostics, content.Diagnostic{Code: d.Code, Message: d.Message, Retryable: false})
				return r, nil
			}
		}
	}
	if code != "" {
		r.Diagnostics = append(r.Diagnostics, content.Diagnostic{Code: code, Message: "Processing requires attention or retry", Retryable: r.Processing.State == "retrying"})
	}
	return r, nil
}
func (s ContentStore) Record(ctx context.Context, org, id string) (content.Record, error) {
	r := content.Record{}
	err := s.Pool.QueryRow(ctx, "SELECT id,corpus_id,namespace,record_key,withdrawn,coalesce(current_version_id,'') FROM records WHERE organization=$1 AND id=$2", org, id).Scan(&r.ID, &r.Source.CorpusID, &r.Source.Namespace, &r.Source.RecordKey, &r.Withdrawn, &r.CurrentVersionID)
	return r, notFound(err)
}

// Records reads one keyset page of a Corpus catalog in a single statement.
func (s ContentStore) Records(ctx context.Context, org, corpusID, after string, limit int) ([]content.Record, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,corpus_id,namespace,record_key,withdrawn,coalesce(current_version_id,'') FROM records WHERE organization=$1 AND corpus_id=$2 AND id > $3 COLLATE "C" ORDER BY id COLLATE "C" LIMIT $4`, org, corpusID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []content.Record{}
	for rows.Next() {
		var r content.Record
		if err = rows.Scan(&r.ID, &r.Source.CorpusID, &r.Source.Namespace, &r.Source.RecordKey, &r.Withdrawn, &r.CurrentVersionID); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}
func (s ContentStore) Version(ctx context.Context, org, recordID, id string) (content.StoredVersion, error) {
	v := content.StoredVersion{}
	var provenance, extensions []byte
	err := s.Pool.QueryRow(ctx, `SELECT v.record_id,v.id,r.corpus_id,t.object_key,t.sha256,t.byte_length,m.object_key,m.sha256,m.byte_length,v.provenance,v.extensions FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) JOIN content_blobs t ON (t.organization,t.blob_id)=(v.organization,v.text_blob_id) JOIN content_blobs m ON (m.organization,m.blob_id)=(v.organization,v.manifest_blob_id) WHERE v.organization=$1 AND v.record_id=$2 AND v.id=$3`, org, recordID, id).Scan(&v.RecordID, &v.ID, &v.CorpusID, &v.TextBlob.Key, &v.TextBlob.SHA256, &v.TextBlob.Size, &v.ManifestBlob.Key, &v.ManifestBlob.SHA256, &v.ManifestBlob.Size, &provenance, &extensions)
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
		v.Diagnostics, err = s.versionDiagnostics(ctx, org, id, v.Availability.State == "quarantined", code)
	}
	return v, notFound(err)
}

// versionDiagnostics explains a Version: its structured quarantine reason
// (or, for quarantines that predate it, its code), then the failure a
// normalizer fallback records and a recorded normalizer conflict.
func (s ContentStore) versionDiagnostics(ctx context.Context, org, id string, quarantined bool, code string) ([]content.Diagnostic, error) {
	out := []content.Diagnostic{}
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
	n, found, err := s.Normalized(ctx, org, id)
	if err != nil || !found || n.Failed() {
		return out, err
	}
	return append(out, n.Diagnostics()...), nil
}
func (s ContentStore) Work(ctx context.Context, org, id string) (content.Work, bool, error) {
	w := content.Work{Organization: org, ReceiptID: id}
	var command []byte
	var state string
	err := s.Pool.QueryRow(ctx, `SELECT r.record_id,a.command,r.state,r.slot,r.digest,a.version_id,a.acceptance_order,a.source_position,coalesce(a.predecessor_id,'') FROM ingestion_receipts r JOIN accepted_revisions a ON (a.organization,a.record_id,a.slot)=(r.organization,r.record_id,r.slot) WHERE r.organization=$1 AND r.id=$2`, org, id).Scan(&w.RecordID, &command, &state, &w.Slot, &w.Digest, &w.VersionID, &w.Order, &w.Position, &w.PredecessorID)
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
			_, err = tx.Exec(ctx, `INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,source_position,predecessor_id,text_blob_id,manifest_blob_id,provenance,extensions,quarantined,processing,error_code,quarantine) VALUES($1,$2,$3,$4,$5,$6,$7,nullif($8,''),$9,$10,$11,$12,$13,$14,$15,$16)`, w.Organization, w.VersionID, w.RecordID, w.Slot, w.Digest, w.Order, w.Position, w.PredecessorID, content.StableID("blob", w.Organization, publication.Normalized.SHA256), content.StableID("blob", w.Organization, publication.Manifest.SHA256), provenance, extensionsJSON, publication.Quarantine != nil, processing, code, quarantine)
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
	return tx.Commit(ctx)
}

// Resolve expands immutable Record-target Relations against canonical
// currentness, baseline availability and authorization. Missing, unready,
// withdrawn, quarantined and inaccessible targets all resolve to "unavailable"
// with no target IDs.
func (s ContentStore) Resolve(ctx context.Context, scope corpus.Scope, relations []content.Relation) ([]content.ResolvedRelation, error) {
	if !scope.Allows("content:read") {
		return nil, corpus.ErrForbidden
	}
	resolved := make([]content.ResolvedRelation, len(relations))
	for i, relation := range relations {
		resolved[i] = content.ResolvedRelation{Source: relation, Status: "unavailable"}
		if !scope.Contains(relation.Target.CorpusID) {
			continue
		}
		var recordID, versionID string
		err := s.Pool.QueryRow(ctx, `SELECT r.id,r.current_version_id FROM records r JOIN record_versions v ON (v.organization,v.id)=(r.organization,r.current_version_id) WHERE r.organization=$1 AND r.corpus_id=$2 AND r.namespace=$3 AND r.record_key=$4 AND `+eligibleVersionSQL, scope.Organization, relation.Target.CorpusID, relation.Target.Namespace, relation.Target.RecordKey).Scan(&recordID, &versionID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		resolved[i].Status = "available"
		resolved[i].TargetRecordID = recordID
		resolved[i].TargetVersionID = versionID
	}
	return resolved, nil
}

func (s ContentStore) Claim(ctx context.Context) (content.Dispatch, error) {
	var d content.Dispatch
	err := s.Pool.QueryRow(ctx, `UPDATE ingestion_outbox SET lease_until=now()+interval '5 seconds' WHERE (organization,receipt_id)=(SELECT organization,receipt_id FROM ingestion_outbox WHERE NOT dispatched AND lease_until<now() ORDER BY receipt_id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING organization,receipt_id`).Scan(&d.Organization, &d.ReceiptID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = content.ErrNoDispatch
	}
	return d, err
}
func (s ContentStore) Dispatched(ctx context.Context, d content.Dispatch) error {
	_, err := s.Pool.Exec(ctx, "UPDATE ingestion_outbox SET dispatched=true WHERE organization=$1 AND receipt_id=$2", d.Organization, d.ReceiptID)
	return err
}
