package postgres

import (
	"context"
	"encoding/json"
	"errors"

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

// Generation returns the logical generation PostgreSQL routes the Corpus to.
func (s ProjectionStore) Generation(ctx context.Context, org, corpusID string) (content.Generation, error) {
	var g content.Generation
	var cfg []byte
	var spaces []byte
	err := database(ctx, s.Pool).QueryRow(ctx, `SELECT g.id,g.collection,g.profile_version,g.space_id,g.source_namespace_projected,g.spaces,g.spaces_projected,g.metadata_projected,g.item_keywords_projected,COALESCE(g.retrieval,c.retrieval) FROM `+effectiveGenerationsSQL+` g, corpora c WHERE c.organization=$1 AND c.id=$2 AND g.id=`+routedGenerationSQL("$1", "$2"), org, corpusID).Scan(&g.ID, &g.Collection, &g.ProfileVersion, &g.SpaceID, &g.SourceNamespaceProjected, &spaces, &g.SpacesProjected, &g.MetadataProjected, &g.ItemKeywordsProjected, &cfg)
	if err != nil {
		return g, err
	}
	if g.Spaces, err = scanSpaces(spaces); err != nil {
		return g, err
	}
	g.Fields, err = retrievalFields(cfg)
	if err == nil {
		err = loadGenerationIngestion(ctx, database(ctx, s.Pool), &g)
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

func prepareSegmentation(ctx context.Context, tx pgx.Tx, org string, result content.Segmentation) (journalFinish, error) {
	if journalGroupOf(ctx) == nil {
		if err := lockProjectionRouting(ctx, tx); err != nil {
			return nil, err
		}
		if err := lockJournalVersion(ctx, tx, org, result.VersionID); err != nil {
			return nil, err
		}
	}
	digest := content.SegmentationDigest(result)
	var existing string
	err := tx.QueryRow(ctx, `SELECT digest FROM segmentations WHERE organization=$1 AND id=$2`, org, result.ID).Scan(&existing)
	if err == nil {
		if existing != digest {
			return nil, content.ErrConflict
		}
		return nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO segmentations VALUES($1,$2,$3,$4,$5,$6)`, org, result.ID, result.VersionID, result.Recipe, digest, result.Provenance); err != nil {
		return nil, err
	}
	for _, p := range result.Segments {
		metadata, err := compactSegmentDerivation(p.Derivation)
		if err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO segments VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, org, p.ID, result.ID, result.VersionID, p.PartKey, p.Start, p.End, content.Hash([]byte(p.Text)), metadata); err != nil {
			return nil, err
		}
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
	segmentFields, generationFields := "", ""
	if response {
		segmentFields = ",sg.part_key,sg.start_offset,sg.end_offset,sg.text_sha256,sg.derivation"
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
 v.record_id,v.slot,v.baseline_ready,v.quarantined,r.corpus_id,r.current_version_id,r.withdrawn
FROM candidates c JOIN generations g ON g.id=c.generation_id
JOIN LATERAL (SELECT sg.organization,sg.id,sg.version_id,sg.segmentation_id` + segmentFields + `
 FROM segments sg WHERE (sg.organization,sg.id)>=(c.organization,c.segment_id)
 ORDER BY sg.organization,sg.id LIMIT 1) sg ON (sg.organization,sg.id)=(c.organization,c.segment_id)
JOIN LATERAL (SELECT v.organization,v.id,v.record_id,v.slot,v.baseline_ready,v.quarantined FROM record_versions v
 WHERE (v.organization,v.id)>=(c.organization,sg.version_id)
 ORDER BY v.organization,v.id LIMIT 1) v ON (v.organization,v.id)=(c.organization,sg.version_id)
JOIN LATERAL (SELECT r.organization,r.id,r.corpus_id,r.current_version_id,r.withdrawn FROM records r
 WHERE (r.organization,r.id)>=(c.organization,v.record_id)
 ORDER BY r.organization,r.id LIMIT 1) r ON (r.organization,r.id)=(c.organization,v.record_id)
), routes AS MATERIALIZED (
 SELECT requested.organization,requested.corpus_id,cp.archived,
 ` + routedGenerationSQL("requested.organization", "requested.corpus_id") + ` AS generation_id
 FROM (SELECT DISTINCT organization,corpus_id FROM located) requested
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

var hydrationFenceSQL = `
WHERE c.generation_id=route.generation_id AND r.current_version_id=v.id AND ` + eligibleVersionSQL + ` AND NOT route.archived`

var hydrateSQL = hydrationLookupsSQL(true) + `SELECT c.n,r.id,v.id,r.corpus_id,sg.segmentation_id,sg.id,c.part_key,c.start_offset,c.end_offset,c.text_sha256,b.object_key,b.sha256,b.byte_length,coalesce(e.id,''),coalesce(e.space_id,''),
 jsonb_build_object('source_ranges',c.derivation->'source_ranges','source_separator',c.derivation->'source_separator')
` + hydrationCandidatesSQL + `
JOIN LATERAL (SELECT p.organization,p.version_id,p.part_key,p.blob_id FROM version_parts p
 WHERE (p.organization,p.version_id,p.part_key)>=(c.organization,sg.version_id,c.part_key)
 ORDER BY p.organization,p.version_id,p.part_key LIMIT 1) p ON (p.organization,p.version_id,p.part_key)=(c.organization,sg.version_id,c.part_key)
JOIN LATERAL (SELECT b.organization,b.blob_id,b.object_key,b.sha256,b.byte_length FROM content_blobs b
 WHERE (b.organization,b.blob_id)>=(c.organization,p.blob_id)
 ORDER BY b.organization,b.blob_id LIMIT 1) b ON (b.organization,b.blob_id)=(c.organization,p.blob_id)
LEFT JOIN LATERAL (SELECT ec.artifact_id AS id,ec.space_id FROM ` + embeddingCoverageForSegmentSQL("c.organization", "c.segment_id") + ` ec WHERE ec.generation_id=g.id AND ((c.evaluation_plugin='' AND ` + selectedVectorSpaceSQL("pc", "g", "v") + `=ec.space_id) OR (c.evaluation_plugin<>'' AND ec.space_id=c.evaluation_space))
  AND ec.organization=c.organization AND ec.segment_id=c.segment_id AND ec.generation_id=c.generation_id ORDER BY ec.artifact_id LIMIT 1) e ON true
` + hydrationFenceSQL

func hydrationArgs(scope corpus.Scope, cs []content.Candidate) []any {
	orgs, segments, generations, evaluations, spaces := make([]string, len(cs)), make([]string, len(cs)), make([]string, len(cs)), make([]string, len(cs)), make([]string, len(cs))
	for i, c := range cs {
		orgs[i], segments[i], generations[i], evaluations[i], spaces[i] = scope.Organization, c.SegmentID, c.GenerationID, c.EvaluationPlugin, c.EvaluationSpace
	}
	return []any{orgs, segments, generations, evaluations, spaces}
}

// RecheckHydration checks only mutable serving fences after canonical bytes
// have been read. Immutable Part/blob/embedding metadata stays in the initial
// hydration; both paths use the same authorization and currentness predicates.
func (s ProjectionStore) RecheckHydration(ctx context.Context, scope corpus.Scope, cs []content.Candidate) (map[int]bool, error) {
	out := map[int]bool{}
	if len(cs) == 0 {
		return out, nil
	}
	rows, err := database(ctx, s.Pool).Query(ctx, hydrationLookupsSQL(false)+`SELECT c.n,r.corpus_id `+hydrationCandidatesSQL+hydrationFenceSQL, hydrationArgs(scope, cs)...)
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

// Hydrate looks a batch of candidates up with only response/source fields. A
// candidate outside the caller's Corpora is absent, as if it did not exist.
func (s ProjectionStore) Hydrate(ctx context.Context, scope corpus.Scope, cs []content.Candidate) (map[int]content.Located, error) {
	out := map[int]content.Located{}
	if len(cs) == 0 {
		return out, nil
	}
	rows, err := database(ctx, s.Pool).Query(ctx, hydrateSQL, hydrationArgs(scope, cs)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n int
		var l content.Located
		var corpusID string
		var derivation []byte
		h := &l.Hydrated
		if err = rows.Scan(&n, &h.RecordID, &h.VersionID, &corpusID, &h.SegmentationID, &h.Segment.ID, &h.Segment.PartKey, &h.Segment.Start, &h.Segment.End, &h.TextSHA256,
			&l.Blob.Key, &l.Blob.SHA256, &l.Blob.Size, &h.EmbeddingID, &h.SpaceID, &derivation); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(derivation, &h.Segment.Derivation); err != nil {
			return nil, err
		}
		if !scope.Contains(corpusID) {
			continue
		}
		h.GenerationID = cs[n-1].GenerationID
		h.Availability = content.Availability{State: "retrieval_ready", Current: true, Searchable: true}
		out[n-1] = l
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	// Resolve only already-authorized candidates, by segment and Part primary
	// keys. No scan of the Organization's canonical text is introduced.
	ids := []string{}
	at := map[string][]int{}
	for n, l := range out {
		if len(l.Segment.Derivation.SourceRanges) > 0 {
			if _, seen := at[l.Segment.ID]; !seen {
				ids = append(ids, l.Segment.ID)
			}
			at[l.Segment.ID] = append(at[l.Segment.ID], n)
		}
	}
	if len(ids) > 0 {
		sourceRows, err := database(ctx, s.Pool).Query(ctx, `SELECT sg.id,r.part_key,r.start,r.end,b.object_key,b.sha256,b.byte_length
FROM unnest($2::text[]) AS selected(id)
JOIN segments sg ON sg.organization=$1 AND sg.id=selected.id
CROSS JOIN LATERAL jsonb_array_elements(sg.derivation->'source_ranges') WITH ORDINALITY AS item(value,ordinality)
CROSS JOIN LATERAL (SELECT item.value->>'part_key' AS part_key,(item.value->>'start')::integer AS start,(item.value->>'end')::integer AS end,item.ordinality) r
JOIN version_parts p ON p.organization=sg.organization AND p.version_id=sg.version_id AND p.part_key=r.part_key
JOIN content_blobs b ON b.organization=p.organization AND b.blob_id=p.blob_id
ORDER BY sg.id,r.ordinality`, scope.Organization, ids)
		if err != nil {
			return nil, err
		}
		defer sourceRows.Close()
		for sourceRows.Next() {
			var id string
			var source content.LocatedSource
			if err = sourceRows.Scan(&id, &source.Range.PartKey, &source.Range.Start, &source.Range.End, &source.Blob.Key, &source.Blob.SHA256, &source.Blob.Size); err != nil {
				return nil, err
			}
			for _, n := range at[id] {
				l := out[n]
				l.Sources = append(l.Sources, source)
				out[n] = l
			}
		}
		if err = sourceRows.Err(); err != nil {
			return nil, err
		}
		for _, l := range out {
			if len(l.Sources) != len(l.Segment.Derivation.SourceRanges) {
				return nil, content.ErrConflict
			}
		}
	}
	return out, nil
}

// ProjectionStore persists baseline projection artifacts and Corpus routing.
type ProjectionStore struct{ Pool *pgxpool.Pool }

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
