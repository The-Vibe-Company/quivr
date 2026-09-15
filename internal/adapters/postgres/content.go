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

type eventInput struct{ Organization, CorpusID, Kind, Resource, ResourceID, MutationID string }

func appendEvent(ctx context.Context, tx pgx.Tx, event eventInput) error {
	var sequence int64
	if err := tx.QueryRow(ctx, "UPDATE organization_journals SET last_sequence=last_sequence+1 WHERE organization=$1 RETURNING last_sequence", event.Organization).Scan(&sequence); err != nil {
		return err
	}
	identity := event.MutationID
	if identity == "" {
		identity = event.ResourceID
	}
	_, err := tx.Exec(ctx, `INSERT INTO change_events(organization,sequence,event_id,corpus_id,event_type,resource_type,resource_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, event.Organization, sequence, content.StableID("event", event.Organization, event.Kind, event.Resource, identity), event.CorpusID, event.Kind, event.Resource, event.ResourceID)
	return err
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
	err = tx.QueryRow(ctx, "SELECT id,canonical_request FROM ingestion_receipts WHERE organization=$1 AND request_key=$2", scope.Organization, c.Key).Scan(&receiptID, &previous)
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
	return content.Receipt{ID: receiptID, State: "pending", RecordID: recordID, Source: c.Source, Processing: content.Processing{State: "queued", Phase: "materialization"}, Diagnostics: []content.Diagnostic{}}, nil
}
func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return corpus.ErrNotFound
	}
	return err
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
func (s ContentStore) Version(ctx context.Context, org, recordID, id string) (content.StoredVersion, error) {
	v := content.StoredVersion{}
	var provenance []byte
	err := s.Pool.QueryRow(ctx, `SELECT v.record_id,v.id,r.corpus_id,t.object_key,t.sha256,t.byte_length,m.object_key,m.sha256,m.byte_length,v.provenance FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) JOIN content_blobs t ON (t.organization,t.blob_id)=(v.organization,v.text_blob_id) JOIN content_blobs m ON (m.organization,m.blob_id)=(v.organization,v.manifest_blob_id) WHERE v.organization=$1 AND v.record_id=$2 AND v.id=$3`, org, recordID, id).Scan(&v.RecordID, &v.ID, &v.CorpusID, &v.TextBlob.Key, &v.TextBlob.SHA256, &v.TextBlob.Size, &v.ManifestBlob.Key, &v.ManifestBlob.SHA256, &v.ManifestBlob.Size, &provenance)
	if err == nil {
		err = json.Unmarshal(provenance, &v.Provenance)
	}
	if err == nil {
		v.Availability, v.Processing, _, err = s.VersionStatus(ctx, org, id)
	}
	return v, notFound(err)
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
func (s ContentStore) Publish(ctx context.Context, w content.Work, text, manifest content.Blob) error {
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
			for _, b := range []content.Blob{text, manifest} {
				_, err = tx.Exec(ctx, "INSERT INTO content_blobs VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING", w.Organization, content.StableID("blob", w.Organization, b.SHA256), b.Key, b.SHA256, b.Size)
				if err != nil {
					return err
				}
			}
			provenance, err := json.Marshal(w.Command.Provenance)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,source_position,predecessor_id,text_blob_id,manifest_blob_id,provenance) VALUES($1,$2,$3,$4,$5,$6,$7,nullif($8,''),$9,$10,$11)`, w.Organization, w.VersionID, w.RecordID, w.Slot, w.Digest, w.Order, w.Position, w.PredecessorID, content.StableID("blob", w.Organization, text.SHA256), content.StableID("blob", w.Organization, manifest.SHA256), provenance)
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, "INSERT INTO version_parts VALUES($1,$2,'body','body',$3)", w.Organization, w.VersionID, content.StableID("blob", w.Organization, text.SHA256)); err != nil {
				return err
			}
			if err = appendEvent(ctx, tx, eventInput{Organization: w.Organization, CorpusID: w.Command.Source.CorpusID, Kind: "record.materialized", Resource: "record", ResourceID: w.RecordID, MutationID: w.VersionID}); err != nil {
				return err
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
