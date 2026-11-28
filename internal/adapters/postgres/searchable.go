package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BootstrapGeneration creates the default generation of a fresh install with
// the registry's served and evaluation spaces (spaceID alone when nothing is
// registered). It is source-namespace and space projected. An install that
// already has an active default keeps it; AlignDefaultGeneration moves it
// onto the registry's spaces.
func (s ProjectionStore) BootstrapGeneration(ctx context.Context, collection, spaceID string) error {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockProjectionRouting(ctx, tx); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO projection_generations(id,collection,profile_version,active,space_id,source_namespace_projected,spaces,spaces_projected,metadata_projected,item_keywords_projected)
SELECT $1,$2,$3,true,COALESCE(`+servedSpaceSQL+`,$4),true,COALESCE(`+deploymentSpacesSQL+`,jsonb_build_array(jsonb_build_object('id',$4::text,'metric','cosine'))),true,true,true
WHERE NOT EXISTS(SELECT 1 FROM `+effectiveGenerationsSQL+` WHERE active) ON CONFLICT DO NOTHING`, content.StableID("generation", collection, retrieval.ProfileVersion), collection, retrieval.ProfileVersion, spaceID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		if err = markRoutingGeneration(ctx, tx, content.StableID("generation", collection, retrieval.ProfileVersion)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// DefaultMove reports a default generation AlignDefaultGeneration replaced:
// Pinned Corpora were routed to Previous so it keeps serving them, and
// Corpora created afterwards follow Current. Current is empty when nothing
// moved.
type DefaultMove struct {
	Previous, Current string
	Pinned            int64
}

// AlignDefaultGeneration makes Corpora created from now on start on the
// registry's served and evaluation spaces. When the default generation, which
// a Corpus without a route follows, carries other spaces (the legacy E5 one
// after the move to an ingestion plugin, or a plugin's former spaces), every
// Corpus without a route is first routed to it, so it keeps its results until
// it is rebuilt. That default is then marked former, and a new default in the
// same collection and profile, carrying the registry's spaces, takes over.
// No existing Corpus changes generation, so in-flight work keeps the one it
// read. A changed index setting moves a default built with named spaces the
// same way, so new Corpora get the new index and existing ones keep theirs
// until rebuilt. A default that
// already matches, or a database with no default, is left
// as it is. Missing projection capabilities rotate the default even before a
// served space is registered, retaining its prior spaces. It runs after RegisterSpaces, in
// migrate and at api and worker startup.
func (s ProjectionStore) AlignDefaultGeneration(ctx context.Context) (DefaultMove, error) {
	var move DefaultMove
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return move, err
	}
	defer tx.Rollback(ctx)
	if err = lockProjectionRouting(ctx, tx); err != nil {
		return move, err
	}
	// Concurrent api, worker and migrate startups register and align in turn.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, spacesLock); err != nil {
		return move, err
	}
	// A default built before named spaces carries its one space.
	var matches bool
	err = tx.QueryRow(ctx, `SELECT d.id,d.metadata_projected AND d.item_keywords_projected AND (`+servedSpaceSQL+` IS NULL OR (d.space_id=`+servedSpaceSQL+` AND
 (SELECT array_agg(e->>'id' ORDER BY e->>'id') FROM jsonb_array_elements(CASE WHEN d.spaces_projected THEN d.spaces ELSE jsonb_build_array(jsonb_build_object('id',d.space_id)) END) e)
 =(SELECT array_agg(vs.id ORDER BY vs.id) FROM vector_spaces vs WHERE vs.role IN ('served','evaluation')) AND (NOT d.spaces_projected OR NOT EXISTS
 (SELECT 1 FROM vector_spaces vs WHERE vs.role IN ('served','evaluation') AND (NOT d.spaces @> jsonb_build_array(jsonb_build_object('id',vs.id,'role',vs.role,'owner_plugin_id',vs.owner_plugin_id))
 OR vs.vector_index IS DISTINCT FROM (SELECT e->'index' FROM jsonb_array_elements(d.spaces) e WHERE e->>'id'=vs.id LIMIT 1))))))
FROM `+effectiveGenerationsSQL+` d WHERE d.active`).Scan(&move.Previous, &matches)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && matches) {
		return DefaultMove{}, nil
	}
	if err != nil {
		return DefaultMove{}, err
	}
	// Corpus creation waits for the switch: a Corpus committed before it is
	// routed to the previous default, one created after follows the new one.
	if _, err = tx.Exec(ctx, `LOCK TABLE corpora IN SHARE MODE`); err != nil {
		return move, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO corpus_projection_routes(organization,corpus_id,generation_id)
SELECT c.organization,c.id,$1 FROM corpora c WHERE NOT EXISTS(SELECT 1 FROM corpus_projection_routes cr WHERE cr.organization=c.organization AND cr.corpus_id=c.id)`, move.Previous)
	if err != nil {
		return move, err
	}
	move.Pinned = tag.RowsAffected()
	var collection, profile string
	if err = tx.QueryRow(ctx, `UPDATE projection_generations SET active=false,default_until=now() WHERE id=$1 RETURNING collection,profile_version`, move.Previous).Scan(&collection, &profile); err != nil {
		return move, err
	}
	move.Current = content.StableID("generation", collection, profile, move.Previous)
	if _, err = tx.Exec(ctx, `INSERT INTO projection_generations(id,collection,profile_version,active,space_id,source_namespace_projected,spaces,spaces_projected,metadata_projected,item_keywords_projected)
SELECT $1,$2,$3,true,COALESCE(`+servedSpaceSQL+`,d.space_id),true,COALESCE(`+deploymentSpacesSQL+`,CASE WHEN d.spaces_projected THEN d.spaces ELSE jsonb_build_array(jsonb_build_object('id',d.space_id,'metric','cosine')) END),true,true,true FROM `+effectiveGenerationsSQL+` d WHERE d.id=$4`, move.Current, collection, profile, move.Previous); err != nil {
		return move, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO routing_dirty_generations(epoch,generation_id) SELECT o.id,g FROM routing_operations o CROSS JOIN unnest($1::text[]) g WHERE o.observing ON CONFLICT DO NOTHING`, []string{move.Previous, move.Current}); err != nil {
		return move, err
	}
	return move, tx.Commit(ctx)
}

// Generation returns the logical generation PostgreSQL routes the Corpus to,
// with its ingestion routing, from one statement and so one snapshot.
func (s ProjectionStore) Generation(ctx context.Context, org, corpusID string) (content.Generation, error) {
	var row generationRow
	if err := database(ctx, s.Pool).QueryRow(ctx, routedGenerationQuerySQL, org, corpusID).Scan(row.targets()...); err != nil {
		return row.g, err
	}
	return row.decode()
}

// generationColumnsSQL lists, for generation g of Corpus c, the columns
// generationRow scans; routedColumnsSQL lists them from a CTE named routed.
const generationColumnsSQL = `g.id,g.collection,g.profile_version,g.space_id,g.source_namespace_projected,g.spaces,g.spaces_projected,g.metadata_projected,g.item_keywords_projected,COALESCE(g.retrieval,c.retrieval) AS retrieval,g.ingestion_routing`
const routedColumnsSQL = `routed.id,routed.collection,routed.profile_version,routed.space_id,routed.source_namespace_projected,routed.spaces,routed.spaces_projected,routed.metadata_projected,routed.item_keywords_projected,routed.retrieval,routed.ingestion_routing`

// routedGenerationQuerySQL reads the generation routed for Corpus $2 of
// Organization $1.
var routedGenerationQuerySQL = `SELECT ` + generationColumnsSQL + ` FROM ` + effectiveGenerationsSQL + ` g, corpora c WHERE c.organization=$1 AND c.id=$2 AND g.id=` + routedGenerationSQL("$1", "$2")

// generationRow holds a routed generation as scanned, before JSON decoding.
type generationRow struct {
	g                    content.Generation
	spaces, cfg, routing []byte
}

func (r *generationRow) targets() []any {
	g := &r.g
	return []any{&g.ID, &g.Collection, &g.ProfileVersion, &g.SpaceID, &g.SourceNamespaceProjected, &r.spaces, &g.SpacesProjected, &g.MetadataProjected, &g.ItemKeywordsProjected, &r.cfg, &r.routing}
}

func (r *generationRow) decode() (content.Generation, error) {
	g := r.g
	var err error
	if g.Spaces, err = scanSpaces(r.spaces); err != nil {
		return g, err
	}
	if g.Fields, err = retrievalFields(r.cfg); err != nil {
		return g, err
	}
	if len(r.routing) > 0 {
		err = json.Unmarshal(r.routing, &g.IngestionRouting)
	}
	return g, err
}

func (s ProjectionStore) Authorize(ctx context.Context, scope corpus.Scope, ids []string) error {
	var count, archived int
	err := database(ctx, s.Pool).QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE archived) FROM corpora WHERE organization=$1 AND id=ANY($2)`, scope.Organization, ids).Scan(&count, &archived)
	if err != nil {
		return err
	}
	if count != len(ids) {
		return corpus.ErrForbidden
	}
	if archived > 0 {
		return corpus.ErrArchived
	}
	return nil
}

// ErrGenerationChanged means routing moved while work targeted an older
// generation; the caller retries against the current route.
var ErrGenerationChanged = errors.New("projection generation changed")

func (s ProjectionStore) SaveSegmentation(ctx context.Context, org string, result content.Segmentation) error {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockProcessingVersion(ctx, tx, org, result.VersionID); err != nil {
		return err
	}
	if err = insertSegmentation(ctx, tx, org, result); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func insertSegmentation(ctx context.Context, tx pgx.Tx, org string, result content.Segmentation) error {
	finish, err := prepareSegmentation(ctx, tx, org, result)
	if err != nil || finish == nil {
		return err
	}
	return finish()
}

// saveSegmentationSQL inserts the segmentation header and all of its segments
// in a single unnest statement. Ordering by (x.id, x.pos) enforces database
// collation primary-key lock order across concurrent writers while preserving
// first-input tie-breaking on ON CONFLICT DO NOTHING replay.
const saveSegmentationSQL = `WITH seg AS (
 INSERT INTO segmentations(organization,id,version_id,recipe,digest,provenance,segment_count)
 VALUES($1,$2,$3,$4,$5,$6,$7)
 ON CONFLICT(organization,id) DO NOTHING
 RETURNING id
), rows AS (
 INSERT INTO segments(organization,id,segmentation_id,version_id,part_key,start_offset,end_offset,text_sha256,derivation)
 SELECT $1,x.id,seg.id,$3,x.part_key,x.start_offset,x.end_offset,x.text_sha256,x.derivation
 FROM seg
 CROSS JOIN unnest($8::text[],$9::text[],$10::integer[],$11::integer[],$12::text[],$13::jsonb[])
  WITH ORDINALITY AS x(id,part_key,start_offset,end_offset,text_sha256,derivation,pos)
 ORDER BY x.id, x.pos
 ON CONFLICT(organization,id) DO NOTHING
)
SELECT EXISTS(SELECT 1 FROM seg),
 (SELECT digest FROM segmentations WHERE organization=$1 AND id=$2)`

type segmentInsertBatch struct {
	IDs         []string
	PartKeys    []string
	Starts      []int
	Ends        []int
	TextSHA256s []string
	Derivations []string
}

func prepareSegmentInsertBatch(segments []content.Segment) (segmentInsertBatch, error) {
	n := len(segments)
	if n == 0 {
		return segmentInsertBatch{
			IDs:         []string{},
			PartKeys:    []string{},
			Starts:      []int{},
			Ends:        []int{},
			TextSHA256s: []string{},
			Derivations: []string{},
		}, nil
	}
	batch := segmentInsertBatch{
		IDs:         make([]string, n),
		PartKeys:    make([]string, n),
		Starts:      make([]int, n),
		Ends:        make([]int, n),
		TextSHA256s: make([]string, n),
		Derivations: make([]string, n),
	}
	for i, p := range segments {
		metadata, err := compactSegmentDerivation(p.Derivation)
		if err != nil {
			return segmentInsertBatch{}, err
		}
		batch.IDs[i] = p.ID
		batch.PartKeys[i] = p.PartKey
		batch.Starts[i] = p.Start
		batch.Ends[i] = p.End
		batch.TextSHA256s[i] = content.Hash([]byte(p.Text))
		batch.Derivations[i] = string(metadata)
	}
	return batch, nil
}

func prepareSegmentation(ctx context.Context, tx pgx.Tx, org string, result content.Segmentation) (journalFinish, error) {
	if journalGroupOf(ctx) == nil {
		if err := lockProjectionRouting(ctx, tx); err != nil {
			return nil, err
		}
		if err := lockJournalVersion(ctx, tx, org, result.VersionID); err != nil {
			return nil, err
		}
	}
	batch, err := prepareSegmentInsertBatch(result.Segments)
	if err != nil {
		return nil, err
	}
	provenance := result.Provenance
	if len(provenance) == 0 {
		provenance = json.RawMessage("{}")
	}
	digest := content.SegmentationDigest(result)
	var inserted bool
	var existing *string
	if err = tx.QueryRow(ctx, saveSegmentationSQL, org, result.ID, result.VersionID, result.Recipe, digest, provenance, len(result.Segments),
		batch.IDs, batch.PartKeys, batch.Starts, batch.Ends, batch.TextSHA256s, batch.Derivations).Scan(&inserted, &existing); err != nil {
		return nil, err
	}
	if !inserted {
		var storedDigest string
		if existing != nil {
			storedDigest = *existing
		} else if err = tx.QueryRow(ctx, `SELECT digest FROM segmentations WHERE organization=$1 AND id=$2`, org, result.ID).Scan(&storedDigest); err != nil {
			return nil, err
		}
		if storedDigest != digest {
			return nil, content.ErrConflict
		}
		return nil, nil
	}
	if group := journalGroupOf(ctx); group != nil {
		group.segmented[result.VersionID] = true
	}
	return func() error {
		// Mutable progress follows the journal fence in prepared groups.
		if _, err := tx.Exec(ctx, `UPDATE record_versions SET segmented_at=clock_timestamp() WHERE organization=$1 AND id=$2 AND segmented_at IS NULL AND materialized_at IS NOT NULL`, org, result.VersionID); err != nil {
			return err
		}
		return enqueueProjectionPurge(ctx, tx, org, result.VersionID)
	}, nil
}

// StoredSegmentation reads a Version's segmentation of one recipe, its
// segments in order.
func (s ProjectionStore) StoredSegmentation(ctx context.Context, org, versionID, recipe string) (content.StoredSegmentation, error) {
	var out content.StoredSegmentation
	err := database(ctx, s.Pool).QueryRow(ctx, `SELECT id,digest,provenance FROM segmentations WHERE organization=$1 AND version_id=$2 AND recipe=$3`, org, versionID, recipe).Scan(&out.ID, &out.Digest, &out.Provenance)
	if err != nil {
		return out, notFound(err)
	}
	if string(out.Provenance) == "{}" {
		out.Provenance = nil
	}
	rows, err := database(ctx, s.Pool).Query(ctx, `SELECT id,part_key,start_offset,end_offset,derivation FROM segments WHERE organization=$1 AND segmentation_id=$2 ORDER BY coalesce((derivation->>'ordinal')::integer,0)`, org, out.ID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var p content.StoredSegment
		var derivation []byte
		if err = rows.Scan(&p.ID, &p.PartKey, &p.Start, &p.End, &derivation); err != nil {
			return out, err
		}
		if err = json.Unmarshal(derivation, &p.Derivation); err != nil {
			return out, err
		}
		out.Segments = append(out.Segments, p)
	}
	return out, rows.Err()
}

var _ content.SegmentationStore = ProjectionStore{}

func (s ProjectionStore) BaselineProgress(ctx context.Context, org, id, state, code string, quarantined bool) error {
	if !quarantined {
		return updatePinnedVersion(ctx, s.Pool, org, id, `UPDATE record_versions SET processing=$3,error_code=$4 WHERE organization=$1 AND id=$2 AND NOT baseline_ready AND NOT quarantined`, state, code)
	}
	return s.quarantine(ctx, org, id, state, code, nil)
}

// QuarantineVersion quarantines a Version that is not searchable yet, with
// its structured reason.
func (s ProjectionStore) QuarantineVersion(ctx context.Context, org, id string, reason content.Diagnostic) error {
	return s.quarantine(ctx, org, id, "blocked", reason.Code, &reason)
}

// quarantine holds a Version that is not searchable yet, announced by
// record.quarantined; a reason is listed in its diagnostics.
func (s ProjectionStore) quarantine(ctx context.Context, org, id, state, code string, reason *content.Diagnostic) error {
	err := retryJournalWrite(ctx, "quarantine", func(ctx context.Context) error {
		return s.quarantineAttempt(ctx, org, id, state, code, reason)
	})
	return err
}

func (s ProjectionStore) quarantineAttempt(ctx context.Context, org, id, state, code string, reason *content.Diagnostic) error {
	var raw []byte
	if reason != nil {
		var err error
		if raw, err = json.Marshal(reason); err != nil {
			return err
		}
	}
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
	}
	serves, err := pinnedOwnerServes(ctx, tx, org, id)
	if err != nil {
		return err
	}
	if !serves {
		return tx.Commit(ctx)
	}
	var recordID, corpusID string
	var ready, held bool
	err = tx.QueryRow(ctx, `SELECT v.record_id,r.corpus_id,v.baseline_ready,v.quarantined FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2 FOR UPDATE OF v,r`, org, id).Scan(&recordID, &corpusID, &ready, &held)
	if err != nil {
		return err
	}
	if ready || held {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `UPDATE record_versions SET processing=$3,error_code=$4,quarantined=true,quarantine=$5,quarantine_stage='ingestion',quarantined_at=`+firstStep("quarantined_at")+` WHERE organization=$1 AND id=$2`, org, id, state, code, raw); err != nil {
		return err
	}
	if err = quarantinedEvent(ctx, tx, org, corpusID, recordID, id, code); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// quarantinedEvent announces a Version quarantined with code. A Version
// reprocessed and quarantined again with the same code was announced
// already: consumers reread its current state.
func quarantinedEvent(ctx context.Context, tx pgx.Tx, org, corpusID, recordID, versionID, code string) error {
	event := eventInput{Organization: org, CorpusID: corpusID, Kind: "record.quarantined", Resource: "record", ResourceID: recordID, MutationID: content.StableID("quarantine", versionID, code)}
	var emitted bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM change_events WHERE organization=$1 AND event_id=$2)`, org, eventID(event)).Scan(&emitted); err != nil || emitted {
		return err
	}
	return appendEvent(ctx, tx, event)
}
func (s ProjectionStore) Promote(ctx context.Context, org string, seg content.Segmentation, g content.Generation) error {
	if content.IngestionBatchActive(ctx) {
		var record string
		if err := s.Pool.QueryRow(ctx, `SELECT record_id FROM record_versions WHERE organization=$1 AND id=$2`, org, seg.VersionID).Scan(&record); err != nil {
			return err
		}
		if handled, err := content.EnqueueIngestion(ctx, content.IngestionCommit{Kind: content.CommitBaseline, Organization: org, RecordID: record, Segmentation: seg, Generation: g}); handled {
			return err
		}
	}
	err := retryJournalWrite(ctx, "Promote", func(ctx context.Context) error {
		return s.promoteAttempt(ctx, org, seg, g)
	})
	return err
}

func (s ProjectionStore) promoteAttempt(ctx context.Context, org string, seg content.Segmentation, g content.Generation) error {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err = promoteCanonical(ctx, tx, org, seg, g); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func promoteCanonical(ctx context.Context, tx pgx.Tx, org string, seg content.Segmentation, g content.Generation) error {
	if err := lockProjectionRouting(ctx, tx); err != nil {
		return err
	}
	if err := lockJournalVersion(ctx, tx, org, seg.VersionID); err != nil {
		return err
	}
	var recordID, corpusID, desired string
	var withdrawn, quarantined, ready, active bool
	var digest, sourceMediaType *string
	var routing []byte
	err := readJournal(ctx, tx, org, `SELECT r.id,r.corpus_id,coalesce(r.desired_version_id,''),`+recordGoneSQL+`,v.quarantined,v.baseline_ready,
 $3=`+routedGenerationSQL("r.organization", "r.corpus_id")+`,
 (SELECT digest FROM segmentations WHERE organization=$1 AND id=$4 AND version_id=$2),
 (SELECT ingestion_routing FROM `+effectiveGenerationsSQL+` WHERE id=$3),
 (SELECT coalesce(nullif(ar.source_media_type,''),'text/plain') FROM accepted_revisions ar WHERE (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot))
 FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 WHERE v.organization=$1 AND v.id=$2 FOR UPDATE OF r,v`, []any{org, seg.VersionID, g.ID, seg.ID}, &recordID, &corpusID, &desired, &withdrawn, &quarantined, &ready, &active, &digest, &routing, &sourceMediaType)
	if err != nil {
		return err
	}
	if quarantined {
		return nil
	}
	if withdrawn {
		// Indexing has finished, but withdrawal prevents publishing its baseline.
		if _, err = tx.Exec(ctx, `UPDATE record_versions SET processing='idle',error_code='' WHERE organization=$1 AND id=$2`, org, seg.VersionID); err != nil {
			return err
		}
		return nil
	}
	if !active {
		return ErrGenerationChanged
	}
	if digest == nil {
		return pgx.ErrNoRows
	}
	if *digest != content.SegmentationDigest(seg) {
		return content.ErrConflict
	}
	if len(routing) > 0 {
		if err = json.Unmarshal(routing, &g.IngestionRouting); err != nil {
			return err
		}
	}
	if sourceMediaType == nil {
		return pgx.ErrNoRows
	}
	historicalRecipe := false
	if w, ok := plugins.WorkOf(ctx); ok && w.Kind == plugins.WorkIngestion {
		recipe, recipeErr := currentServingRecipe(ctx, tx, org, seg.VersionID, g)
		if recipeErr != nil {
			return recipeErr
		}
		historicalRecipe = recipe != "" && content.PluginOfRecipe(recipe) == content.PluginOfRecipe(seg.Recipe) && recipe != seg.Recipe
	}
	if historicalRecipe || (g.IngestionRouting != nil && g.IngestionRouting.For(*sourceMediaType) != "" && g.IngestionRouting.For(*sourceMediaType) != content.PluginOfRecipe(seg.Recipe)) {
		if err = coverOwnerProjection(ctx, tx, org, g, seg, nil); err != nil {
			return err
		}
		if err = queueServingProjection(ctx, tx, org, seg.VersionID); err != nil {
			return err
		}
		return nil
	}
	writes := &pgx.Batch{}
	writes.Queue(`INSERT INTO projection_coverage(organization,version_id,generation_id,segmentation_id,role)
 VALUES($1,$2,$3,$4,CASE WHEN EXISTS(SELECT FROM projection_coverage WHERE organization=$1 AND version_id=$2 AND generation_id=$3 AND role='served') THEN 'evaluation' ELSE 'served' END) ON CONFLICT DO NOTHING`, org, seg.VersionID, g.ID, seg.ID)
	writes.Queue(`UPDATE record_versions SET baseline_ready=true,processing='idle',error_code='',retrieval_ready_at=`+firstStep("retrieval_ready_at")+` WHERE organization=$1 AND id=$2`, org, seg.VersionID)
	if desired == seg.VersionID {
		writes.Queue(`UPDATE records SET current_version_id=$3 WHERE organization=$1 AND id=$2`, org, recordID, seg.VersionID)
	}
	if !ready {
		// The event observes its Record after these writes.
		queueEvent(ctx, writes, eventInput{Organization: org, CorpusID: corpusID, Kind: "record.retrieval_ready", Resource: "record", ResourceID: recordID, MutationID: content.StableID("baseline", seg.VersionID, g.ID), VersionID: seg.VersionID})
	} else {
		observeRecordInBatch(ctx, writes, org, recordID)
	}
	return tx.SendBatch(ctx, writes).Close()
}

// Hydration reaches each requested segment, Version and Record by a bounded
// composite-key seek. Empty statistics otherwise let the planner choose an
// Organization-only prefix of an unrelated index and filter every row for
// every candidate. OFFSET 0 on an equality lookup alone does not prevent it.
// Ordering and LIMIT preserve the full key seek; outer equality rejects the
// next row when the key is absent. The same serving predicates fence the
// initial metadata lookup and the post-blob-read recheck.
// Resolve routing overlays once per distinct requested generation, rather than
// repeating their control-table probes for every hit and embedding lookup.
func hydrationLookupsSQL(response bool) string {
	segmentFields, outerVersionFields, innerVersionFields, generationFields := "", "", "", ""
	if response {
		segmentFields = ",sg.part_key,sg.start_offset,sg.end_offset,sg.text_sha256,sg.derivation"
		outerVersionFields = ",v.manifest_blob_id,v.common_metadata"
		innerVersionFields = ",v.manifest_blob_id,v.extensions->'quivr.metadata'->'data' AS common_metadata"
		generationFields = ",g.spaces,g.space_id"
	}
	return `WITH candidates AS MATERIALIZED (
 SELECT * FROM unnest($1::text[],$2::text[],$3::text[],$4::text[],$5::text[]) WITH ORDINALITY AS c(organization,segment_id,generation_id,evaluation_plugin,evaluation_space,n)
), generations AS MATERIALIZED (
 SELECT g.id,g.coverage_routed,g.ingestion_routing` + generationFields + `
 FROM (SELECT DISTINCT generation_id FROM candidates) requested
 JOIN ` + effectiveGenerationsSQL + ` g ON g.id=requested.generation_id
), located AS MATERIALIZED (
 SELECT c.*,sg.version_id,sg.segmentation_id` + segmentFields + `,
 v.record_id,v.slot,v.baseline_ready,v.quarantined` + outerVersionFields + `,r.corpus_id,r.current_version_id,r.withdrawn
FROM candidates c JOIN generations g ON g.id=c.generation_id
JOIN LATERAL (SELECT sg.organization,sg.id,sg.version_id,sg.segmentation_id` + segmentFields + `
 FROM segments sg WHERE (sg.organization,sg.id)>=(c.organization,c.segment_id)
 ORDER BY sg.organization,sg.id LIMIT 1) sg ON (sg.organization,sg.id)=(c.organization,c.segment_id)
JOIN LATERAL (SELECT v.organization,v.id,v.record_id,v.slot,v.baseline_ready,v.quarantined` + innerVersionFields + ` FROM record_versions v
 WHERE (v.organization,v.id)>=(c.organization,sg.version_id)
 ORDER BY v.organization,v.id LIMIT 1) v ON (v.organization,v.id)=(c.organization,sg.version_id)
JOIN LATERAL (SELECT r.organization,r.id,r.corpus_id,r.current_version_id,r.withdrawn FROM records r
 WHERE (r.organization,r.id)>=(c.organization,v.record_id)
 ORDER BY r.organization,r.id LIMIT 1) r ON (r.organization,r.id)=(c.organization,v.record_id)
), routes AS MATERIALIZED (
 SELECT requested.organization,requested.corpus_id,cp.archived,
 ` + routedGenerationSQL("requested.organization", "requested.corpus_id") + ` AS generation_id
 FROM (SELECT DISTINCT organization,corpus_id FROM located WHERE $6::boolean OR corpus_id=ANY($7::text[])) requested
 JOIN LATERAL (SELECT cp.organization,cp.id,cp.archived FROM corpora cp
 WHERE (cp.organization,cp.id)>=(requested.organization,requested.corpus_id)
 ORDER BY cp.organization,cp.id LIMIT 1) cp ON (cp.organization,cp.id)=(requested.organization,requested.corpus_id)
)
`
}

var hydrationCandidatesSQL = `
FROM located c JOIN generations g ON g.id=c.generation_id
JOIN routes route ON (route.organization,route.corpus_id)=(c.organization,c.corpus_id)
CROSS JOIN LATERAL (SELECT c.organization,c.segment_id AS id,c.version_id,c.segmentation_id) sg
CROSS JOIN LATERAL (SELECT c.organization,c.version_id AS id,c.record_id,c.slot,c.baseline_ready,c.quarantined) v
CROSS JOIN LATERAL (SELECT c.organization,c.record_id AS id,c.corpus_id,c.current_version_id,c.withdrawn) r
CROSS JOIN LATERAL (SELECT pc.organization,pc.version_id,pc.plugin_id FROM projection_coverage pc WHERE pc.organization=c.organization AND pc.version_id=sg.version_id AND pc.generation_id=c.generation_id AND pc.segmentation_id=sg.segmentation_id AND ((c.evaluation_plugin='' AND ` + effectiveServingSQL("pc", "g", "v") + `) OR (c.evaluation_plugin<>'' AND pc.plugin_id=c.evaluation_plugin)) OFFSET 0) pc
`

func hydrationVisibility(ctx context.Context, scope corpus.Scope, cs []content.Candidate) (string, []any) {
	args := hydrationArgs(scope, cs)
	acl := visibilityPredicate(ctx, &args, "visible_record")
	if acl == "" {
		return "", args
	}
	return ` AND EXISTS (SELECT 1 FROM records visible_record
 WHERE (visible_record.organization,visible_record.id)=(c.organization,c.record_id)` + acl + ")", args
}

var hydrationFenceSQL = `
WHERE c.generation_id=route.generation_id AND r.current_version_id=v.id AND ` + eligibleVersionSQL + ` AND NOT route.archived AND ($6::boolean OR r.corpus_id=ANY($7::text[]))`

// hydrateSourcesSQL resolves packed segment source_ranges in the same round-trip
// as hydrateSQL, only for eligible authorized candidates whose derivation carries
// a source_ranges array and is not coalesced into a single legacy Manifest read.
// Ranges on the segment's primary part reuse the already joined blob; other
// parts use bounded composite-key seeks.
var hydrateSourcesSQL = `CASE WHEN jsonb_typeof(c.derivation->'source_ranges')='array' AND (p.pack_offset IS NOT NULL OR jsonb_array_length(CASE WHEN jsonb_typeof(c.derivation->'source_ranges')='array' THEN c.derivation->'source_ranges' ELSE '[]'::jsonb END)<=1 OR mb.blob_id IS NULL) THEN (
 SELECT coalesce(jsonb_agg(jsonb_build_object(
  'part_key',sr.part_key,
  'start',sr.start_offset,
  'end',sr.end_offset,
  'key',CASE WHEN sr.part_key=c.part_key THEN b.object_key ELSE sb.object_key END,
  'sha256',CASE WHEN sr.part_key=c.part_key THEN b.sha256 ELSE sb.sha256 END,
  'size',CASE WHEN sr.part_key=c.part_key THEN b.byte_length ELSE sb.byte_length END,
  'pack_offset',CASE WHEN sr.part_key=c.part_key THEN p.pack_offset ELSE sp.pack_offset END,
  'pack_length',CASE WHEN sr.part_key=c.part_key THEN p.pack_length ELSE sp.pack_length END
 ) ORDER BY sr.ordinality),'[]'::jsonb)
 FROM jsonb_array_elements(c.derivation->'source_ranges') WITH ORDINALITY AS item(value,ordinality)
 CROSS JOIN LATERAL (SELECT item.value->>'part_key' AS part_key,(item.value->>'start')::integer AS start_offset,(item.value->>'end')::integer AS end_offset,item.ordinality) sr
 LEFT JOIN LATERAL (SELECT vp.organization,vp.version_id,vp.part_key,vp.blob_id,vp.pack_offset,vp.pack_length FROM version_parts vp
  WHERE sr.part_key<>c.part_key AND (vp.organization,vp.version_id,vp.part_key)>=(c.organization,sg.version_id,sr.part_key)
  ORDER BY vp.organization,vp.version_id,vp.part_key LIMIT 1) sp ON (sp.organization,sp.version_id,sp.part_key)=(c.organization,sg.version_id,sr.part_key)
 LEFT JOIN LATERAL (SELECT cb.organization,cb.blob_id,cb.object_key,cb.sha256,cb.byte_length FROM content_blobs cb
  WHERE sr.part_key<>c.part_key AND sp.blob_id IS NOT NULL AND (cb.organization,cb.blob_id)>=(c.organization,sp.blob_id)
  ORDER BY cb.organization,cb.blob_id LIMIT 1) sb ON (sb.organization,sb.blob_id)=(c.organization,sp.blob_id)
 WHERE (sr.part_key=c.part_key AND b.blob_id IS NOT NULL) OR sb.blob_id IS NOT NULL
) END`

var hydrateSQL = hydrationLookupsSQL(true) + `SELECT c.n,r.id,v.id,r.corpus_id,sg.segmentation_id,sg.id,c.part_key,c.start_offset,c.end_offset,c.text_sha256,coalesce(b.object_key,''),coalesce(b.sha256,''),coalesce(b.byte_length,0),p.pack_offset,p.pack_length,coalesce(mb.object_key,''),coalesce(mb.sha256,''),coalesce(mb.byte_length,0),coalesce(e.id,''),coalesce(e.space_id,''),
 jsonb_build_object('source_ranges',c.derivation->'source_ranges','source_separator',c.derivation->'source_separator','region',c.derivation->'region','ordinal',coalesce((c.derivation->>'ordinal')::integer,0)),
 ` + hydrateSourcesSQL + `,
 coalesce(ar.title,''),c.common_metadata,coalesce(s.segment_count,0)
` + hydrationCandidatesSQL + `
LEFT JOIN LATERAL (SELECT ar.organization,ar.record_id,ar.slot,ar.title FROM accepted_revisions ar
 WHERE (ar.organization,ar.record_id,ar.slot)>=(c.organization,v.record_id,v.slot)
 ORDER BY ar.organization,ar.record_id,ar.slot LIMIT 1) ar ON (ar.organization,ar.record_id,ar.slot)=(c.organization,v.record_id,v.slot)
LEFT JOIN LATERAL (SELECT s.organization,s.id,s.segment_count FROM segmentations s
 WHERE (s.organization,s.id)>=(c.organization,sg.segmentation_id)
 ORDER BY s.organization,s.id LIMIT 1) s ON (s.organization,s.id)=(c.organization,sg.segmentation_id)
LEFT JOIN LATERAL (SELECT p.organization,p.version_id,p.part_key,p.blob_id,p.pack_offset,p.pack_length FROM version_parts p
 WHERE c.derivation->'region' IS NULL AND (p.organization,p.version_id,p.part_key)>=(c.organization,sg.version_id,c.part_key)
 ORDER BY p.organization,p.version_id,p.part_key LIMIT 1) p ON (p.organization,p.version_id,p.part_key)=(c.organization,sg.version_id,c.part_key)
LEFT JOIN LATERAL (SELECT mb.organization,mb.blob_id,mb.object_key,mb.sha256,mb.byte_length FROM content_blobs mb
 WHERE c.derivation->'region' IS NULL AND p.pack_offset IS NULL AND jsonb_typeof(c.derivation->'source_ranges')='array' AND jsonb_array_length(CASE WHEN jsonb_typeof(c.derivation->'source_ranges')='array' THEN c.derivation->'source_ranges' ELSE '[]'::jsonb END)>1 AND c.manifest_blob_id IS NOT NULL AND (mb.organization,mb.blob_id)>=(c.organization,c.manifest_blob_id)
 ORDER BY mb.organization,mb.blob_id LIMIT 1) mb ON (mb.organization,mb.blob_id)=(c.organization,c.manifest_blob_id)
LEFT JOIN LATERAL (SELECT b.organization,b.blob_id,b.object_key,b.sha256,b.byte_length FROM content_blobs b
 WHERE c.derivation->'region' IS NULL AND mb.blob_id IS NULL AND p.blob_id IS NOT NULL AND (b.organization,b.blob_id)>=(c.organization,p.blob_id)
 ORDER BY b.organization,b.blob_id LIMIT 1) b ON (b.organization,b.blob_id)=(c.organization,p.blob_id)
LEFT JOIN LATERAL (SELECT ec.artifact_id AS id,ec.space_id FROM ` + embeddingCoverageForSegmentSQL("c.organization", "c.segment_id") + ` ec WHERE ec.generation_id=g.id AND ((c.evaluation_plugin='' AND ` + selectedVectorSpaceSQL("pc", "g", "v") + `=ec.space_id) OR (c.evaluation_plugin<>'' AND ec.space_id=c.evaluation_space))
  AND ec.organization=c.organization AND ec.segment_id=c.segment_id AND ec.generation_id=c.generation_id ORDER BY ec.artifact_id LIMIT 1) e ON true
` + hydrationFenceSQL

func hydrationArgs(scope corpus.Scope, cs []content.Candidate) []any {
	orgs, segments, generations, evaluations, spaces := make([]string, len(cs)), make([]string, len(cs)), make([]string, len(cs)), make([]string, len(cs)), make([]string, len(cs))
	for i, c := range cs {
		orgs[i], segments[i], generations[i], evaluations[i], spaces[i] = scope.Organization, c.SegmentID, c.GenerationID, c.EvaluationPlugin, c.EvaluationSpace
	}
	allCorpora := scope.AllCorpora()
	corpora := scope.Corpora
	if allCorpora || corpora == nil {
		corpora = []string{}
	}
	return []any{orgs, segments, generations, evaluations, spaces, allCorpora, corpora}
}

type hydratedSourceRow struct {
	PartKey    string `json:"part_key"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
	Key        string `json:"key"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	PackOffset *int64 `json:"pack_offset"`
	PackLength *int64 `json:"pack_length"`
}

func decodeHydratedSources(expected int, raw []byte) ([]content.LocatedSource, error) {
	if expected == 0 {
		return nil, nil
	}
	if len(raw) == 0 {
		return nil, content.ErrConflict
	}
	var rows []hydratedSourceRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	if len(rows) != expected {
		return nil, content.ErrConflict
	}
	sources := make([]content.LocatedSource, len(rows))
	for i, row := range rows {
		sources[i] = content.LocatedSource{
			Range: content.SourceRange{PartKey: row.PartKey, Start: row.Start, End: row.End},
			Blob:  content.Blob{Key: row.Key, SHA256: row.SHA256, Size: row.Size},
			Pack:  content.PackRange{Offset: row.PackOffset, Length: row.PackLength},
		}
	}
	return sources, nil
}

// RecheckHydration checks only mutable serving fences after canonical bytes
// have been read. Immutable Part/blob/embedding metadata stays in the initial
// hydration; both paths use the same authorization and currentness predicates.
func (s ProjectionStore) RecheckHydration(ctx context.Context, scope corpus.Scope, cs []content.Candidate) (map[int]bool, error) {
	out := map[int]bool{}
	if len(cs) == 0 {
		return out, nil
	}
	acl, args := hydrationVisibility(ctx, scope, cs)
	rows, err := database(ctx, s.Pool).Query(ctx, hydrationLookupsSQL(false)+`SELECT c.n,r.corpus_id `+hydrationCandidatesSQL+hydrationFenceSQL+acl, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n int
		var corpusID string
		if err = rows.Scan(&n, &corpusID); err != nil {
			return nil, err
		}
		if scope.Contains(corpusID) {
			out[n-1] = true
		}
	}
	return out, rows.Err()
}

// hydrateShardMin is the smallest batch split across concurrent pool
// connections so cold random page reads overlap across storage I/O queues.
const hydrateShardMin = 12

// hydrateMaxShards caps per-search connection fan-out; pool sizing further
// bounds it to at most a quarter of MaxConns so concurrent searches do not
// starve the pool.
const hydrateMaxShards = 4

func hydrateShards(candidates int, maxConns int32) int {
	if candidates <= hydrateShardMin || maxConns < 4 {
		return 1
	}
	byPool := min(hydrateMaxShards, int(maxConns/4))
	bySize := (candidates + hydrateShardMin - 1) / hydrateShardMin
	return max(1, min(byPool, bySize))
}

// HydrateIsAtomic reports whether Hydrate for candidates executes under a
// single snapshot/connection rather than fanning out across independent shards.
func (s ProjectionStore) HydrateIsAtomic(ctx context.Context, candidates int) bool {
	if s.Pool == nil || database(ctx, s.Pool) != s.Pool {
		return true
	}
	return hydrateShards(candidates, s.Pool.Config().MaxConns) == 1
}

// Hydrate looks a batch of candidates up with response and packed-source fields.
// Large non-transactional batches fan out across a bounded slice of pool
// connections so cold random seeks execute concurrently. A candidate outside
// the caller's Corpora is absent, as if it did not exist.
func (s ProjectionStore) Hydrate(ctx context.Context, scope corpus.Scope, cs []content.Candidate) (map[int]content.Located, error) {
	if len(cs) == 0 {
		return map[int]content.Located{}, nil
	}
	shards := 1
	if s.Pool != nil && database(ctx, s.Pool) == s.Pool {
		shards = hydrateShards(len(cs), s.Pool.Config().MaxConns)
	}
	if shards == 1 {
		return s.hydrateBatch(ctx, scope, cs)
	}
	subs, offsets := make([][]content.Candidate, shards), make([]int, shards)
	base, rem := len(cs)/shards, len(cs)%shards
	offset := 0
	for i := range shards {
		size := base
		if i < rem {
			size++
		}
		subs[i], offsets[i] = cs[offset:offset+size], offset
		offset += size
	}
	results := make([]map[int]content.Located, shards)
	if err := runShards(ctx, shards, func(ctx context.Context, i int) error {
		rows, err := s.hydrateBatch(ctx, scope, subs[i])
		results[i] = rows
		return err
	}); err != nil {
		return nil, err
	}
	out := make(map[int]content.Located, len(cs))
	for i, rows := range results {
		for k, v := range rows {
			out[offsets[i]+k] = v
		}
	}
	return out, nil
}

// errShardPanicked reports a shard that panicked; the panic is logged.
var errShardPanicked = errors.New("postgres: hydrate shard panicked")

// runShards runs shards 0..n-1 concurrently and returns once every shard has
// returned. The first failure cancels the others' context and is returned.
// A shard that panics or exits abnormally fails like an error: only the
// panic's type and stack are logged, since its value may quote a query, an
// Organization or content.
func runShards(ctx context.Context, n int, run func(context.Context, int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
	}
	for i := range n {
		wg.Go(func() {
			// recover returns nil after runtime.Goexit: a shard that did
			// not return normally fails even without a panic value.
			panicking := true
			defer func() {
				if panicking {
					r := recover()
					slog.ErrorContext(ctx, "hydrate shard panicked", "panic_type", fmt.Sprintf("%T", r), "stack", string(debug.Stack()))
					fail(errShardPanicked)
				}
			}()
			if err := run(ctx, i); err != nil {
				fail(err)
			}
			panicking = false
		})
	}
	wg.Wait()
	return firstErr
}

func (s ProjectionStore) hydrateBatch(ctx context.Context, scope corpus.Scope, cs []content.Candidate) (map[int]content.Located, error) {
	out := map[int]content.Located{}
	if len(cs) == 0 {
		return out, nil
	}
	acl, args := hydrationVisibility(ctx, scope, cs)
	rows, err := database(ctx, s.Pool).Query(ctx, hydrateSQL+acl, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n int
		var l content.Located
		var corpusID string
		var derivation, sourcesRaw, commonMetadata []byte
		h := &l.Hydrated
		if err = rows.Scan(&n, &h.RecordID, &h.VersionID, &corpusID, &h.SegmentationID, &h.Segment.ID, &h.Segment.PartKey, &h.Segment.Start, &h.Segment.End, &h.TextSHA256,
			&l.Blob.Key, &l.Blob.SHA256, &l.Blob.Size, &l.Pack.Offset, &l.Pack.Length, &l.ManifestBlob.Key, &l.ManifestBlob.SHA256, &l.ManifestBlob.Size, &h.EmbeddingID, &h.SpaceID, &derivation, &sourcesRaw, &h.Title, &commonMetadata, &h.TotalPassages); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(derivation, &h.Segment.Derivation); err != nil {
			return nil, err
		}
		if len(commonMetadata) > 0 && commonMetadata[0] == '{' && string(commonMetadata) != "{}" {
			h.CommonMetadata = commonMetadata
		}
		if !scope.Contains(corpusID) {
			continue
		}
		if l.ManifestBlob.Key == "" {
			if l.Sources, err = decodeHydratedSources(len(h.Segment.Derivation.SourceRanges), sourcesRaw); err != nil {
				return nil, err
			}
		}
		h.GenerationID = cs[n-1].GenerationID
		h.Availability = content.Availability{State: "retrieval_ready", Current: true, Searchable: true}
		out[n-1] = l
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ProjectionStore persists baseline projection artifacts and Corpus routing.
type ProjectionStore struct{ Pool *pgxpool.Pool }

// LeadSegments resolves the opening (lowest-ordinal) text segment ID for each
// requested segmentation using the segments_by_version composite index.
func (s ProjectionStore) LeadSegments(ctx context.Context, org string, refs []content.LeadSegmentRef) (map[string]string, error) {
	out := map[string]string{}
	if len(refs) == 0 {
		return out, nil
	}
	orgs := make([]string, len(refs))
	versions := make([]string, len(refs))
	segmentations := make([]string, len(refs))
	for i, ref := range refs {
		orgs[i], versions[i], segmentations[i] = org, ref.VersionID, ref.SegmentationID
	}
	rows, err := database(ctx, s.Pool).Query(ctx, `SELECT req.segmentation_id, s.id
FROM unnest($1::text[],$2::text[],$3::text[]) AS req(organization,version_id,segmentation_id)
JOIN LATERAL (
 SELECT sg.id FROM segments sg
 WHERE sg.organization=req.organization AND sg.version_id=req.version_id AND sg.segmentation_id=req.segmentation_id
   AND sg.derivation->'region' IS NULL
 ORDER BY coalesce((sg.derivation->>'ordinal')::integer,0), sg.id
 LIMIT 1
) s ON true`, orgs, versions, segmentations)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var segID, leadID string
		if err = rows.Scan(&segID, &leadID); err != nil {
			return nil, err
		}
		out[segID] = leadID
	}
	return out, rows.Err()
}

var _ content.LeadSegmentLocator = ProjectionStore{}

// Storage omits empty derivation values without changing the domain encoding
// used by segment/segmentation identity or the packed source-range contract.
func compactSegmentDerivation(d content.SegmentDerivation) ([]byte, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	for key, value := range fields {
		if key != "provenance" && (string(value) == "0" || string(value) == "false" || string(value) == `""` || string(value) == "null") {
			delete(fields, key)
		}
	}
	return json.Marshal(fields)
}
