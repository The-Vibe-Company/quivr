package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/jackc/pgx/v5"
)

// BootstrapGeneration creates the default generation of a fresh install with
// the registry's served and evaluation spaces (spaceID alone when nothing is
// registered). It is source-namespace and space projected. An install that
// already has an active default keeps it; AlignDefaultGeneration moves it
// onto the registry's spaces.
func (s ContentStore) BootstrapGeneration(ctx context.Context, collection, spaceID string) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO projection_generations(id,collection,profile_version,active,space_id,source_namespace_projected,spaces,spaces_projected)
SELECT $1,$2,$3,true,COALESCE(`+servedSpaceSQL+`,$4),true,COALESCE(`+deploymentSpacesSQL+`,jsonb_build_array(jsonb_build_object('id',$4::text,'metric','cosine'))),true
WHERE NOT EXISTS(SELECT 1 FROM projection_generations WHERE active) ON CONFLICT DO NOTHING`, content.StableID("generation", collection, retrieval.ProfileVersion), collection, retrieval.ProfileVersion, spaceID)
	return err
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
// read. A default that already matches, or a database with no default or no
// served space yet, is left as it is. It runs after RegisterSpaces, in
// migrate and at api and worker startup.
func (s ContentStore) AlignDefaultGeneration(ctx context.Context) (DefaultMove, error) {
	var move DefaultMove
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return move, err
	}
	defer tx.Rollback(ctx)
	// Concurrent api, worker and migrate startups register and align in turn.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, spacesLock); err != nil {
		return move, err
	}
	// A default built before named spaces carries its one space.
	var matches bool
	err = tx.QueryRow(ctx, `SELECT d.id,d.space_id=`+servedSpaceSQL+` AND
 (SELECT array_agg(e->>'id' ORDER BY e->>'id') FROM jsonb_array_elements(CASE WHEN d.spaces_projected THEN d.spaces ELSE jsonb_build_array(jsonb_build_object('id',d.space_id)) END) e)
 =(SELECT array_agg(vs.id ORDER BY vs.id) FROM vector_spaces vs WHERE vs.role IN ('served','evaluation'))
FROM projection_generations d WHERE d.active AND `+servedSpaceSQL+` IS NOT NULL`).Scan(&move.Previous, &matches)
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
	if _, err = tx.Exec(ctx, `INSERT INTO projection_generations(id,collection,profile_version,active,space_id,source_namespace_projected,spaces,spaces_projected)
SELECT $1,$2,$3,true,`+servedSpaceSQL+`,true,`+deploymentSpacesSQL+`,true`, move.Current, collection, profile); err != nil {
		return move, err
	}
	return move, tx.Commit(ctx)
}

// Generation returns the logical generation PostgreSQL routes the Corpus to.
func (s ContentStore) Generation(ctx context.Context, org, corpusID string) (content.Generation, error) {
	var g content.Generation
	var cfg []byte
	var spaces []byte
	err := s.Pool.QueryRow(ctx, `SELECT g.id,g.collection,g.profile_version,g.space_id,g.source_namespace_projected,g.spaces,g.spaces_projected,COALESCE(g.retrieval,c.retrieval) FROM projection_generations g, corpora c WHERE c.organization=$1 AND c.id=$2 AND g.id=`+routedGenerationSQL("$1", "$2"), org, corpusID).Scan(&g.ID, &g.Collection, &g.ProfileVersion, &g.SpaceID, &g.SourceNamespaceProjected, &spaces, &g.SpacesProjected, &cfg)
	if err != nil {
		return g, err
	}
	if g.Spaces, err = scanSpaces(spaces); err != nil {
		return g, err
	}
	g.Fields, err = retrievalFields(cfg)
	return g, err
}
func (s ContentStore) Authorize(ctx context.Context, scope corpus.Scope, ids []string) error {
	var count int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM corpora WHERE organization=$1 AND id=ANY($2)`, scope.Organization, ids).Scan(&count)
	if err != nil {
		return err
	}
	if count != len(ids) {
		return corpus.ErrForbidden
	}
	return nil
}

// ErrGenerationChanged means routing moved while work targeted an older
// generation; the caller retries against the current route.
var ErrGenerationChanged = errors.New("projection generation changed")

func (s ContentStore) SaveSegmentation(ctx context.Context, org string, result content.Segmentation) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
	}
	digest := content.SegmentationDigest(result)
	var existing string
	err = tx.QueryRow(ctx, `SELECT digest FROM segmentations WHERE organization=$1 AND id=$2`, org, result.ID).Scan(&existing)
	if err == nil {
		if existing != digest {
			return content.ErrConflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO segmentations VALUES($1,$2,$3,$4,$5,$6)`, org, result.ID, result.VersionID, result.Recipe, digest, result.Provenance); err != nil {
		return err
	}
	// The first segmentation of a Version finishes its segmented step.
	if _, err = tx.Exec(ctx, `UPDATE record_versions SET segmented_at=clock_timestamp() WHERE organization=$1 AND id=$2 AND segmented_at IS NULL AND materialized_at IS NOT NULL`, org, result.VersionID); err != nil {
		return err
	}
	for _, p := range result.Segments {
		metadata, err := json.Marshal(p.Derivation)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO segments VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, org, p.ID, result.ID, result.VersionID, p.PartKey, p.Start, p.End, content.Hash([]byte(p.Text)), metadata); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// StoredSegmentation reads a Version's segmentation of one recipe, its
// segments in order.
func (s ContentStore) StoredSegmentation(ctx context.Context, org, versionID, recipe string) (content.StoredSegmentation, error) {
	var out content.StoredSegmentation
	err := s.Pool.QueryRow(ctx, `SELECT id,digest,provenance FROM segmentations WHERE organization=$1 AND version_id=$2 AND recipe=$3`, org, versionID, recipe).Scan(&out.ID, &out.Digest, &out.Provenance)
	if err != nil {
		return out, notFound(err)
	}
	if string(out.Provenance) == "{}" {
		out.Provenance = nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,part_key,start_offset,end_offset,derivation FROM segments WHERE organization=$1 AND segmentation_id=$2 ORDER BY (derivation->>'ordinal')::integer`, org, out.ID)
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

var _ content.SegmentationStore = ContentStore{}

func (s ContentStore) BaselineProgress(ctx context.Context, org, id, state, code string, quarantined bool) error {
	if !quarantined {
		_, err := s.Pool.Exec(ctx, `UPDATE record_versions SET processing=$3,error_code=$4 WHERE organization=$1 AND id=$2 AND NOT baseline_ready AND NOT quarantined`, org, id, state, code)
		return err
	}
	return s.quarantine(ctx, org, id, state, code, nil)
}

// QuarantineVersion quarantines a Version that is not searchable yet, with
// its structured reason.
func (s ContentStore) QuarantineVersion(ctx context.Context, org, id string, reason content.Diagnostic) error {
	return s.quarantine(ctx, org, id, "blocked", reason.Code, &reason)
}

// quarantine holds a Version that is not searchable yet, announced by
// record.quarantined; a reason is listed in its diagnostics.
func (s ContentStore) quarantine(ctx context.Context, org, id, state, code string, reason *content.Diagnostic) error {
	var raw []byte
	if reason != nil {
		var err error
		if raw, err = json.Marshal(reason); err != nil {
			return err
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
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
func (s ContentStore) Promote(ctx context.Context, org string, seg content.Segmentation, g content.Generation) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
	}
	var recordID, corpusID, desired string
	var withdrawn, quarantined, ready, active bool
	err = tx.QueryRow(ctx, `SELECT r.id,r.corpus_id,coalesce(r.desired_version_id,''),r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id),v.quarantined,v.baseline_ready FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2 FOR UPDATE OF r,v`, org, seg.VersionID).Scan(&recordID, &corpusID, &desired, &withdrawn, &quarantined, &ready)
	if err != nil {
		return err
	}
	if withdrawn || quarantined {
		return tx.Commit(ctx)
	}
	if err = tx.QueryRow(ctx, `SELECT $3=`+routedGenerationSQL("$1", "$2"), org, corpusID, g.ID).Scan(&active); err != nil {
		return err
	}
	if !active {
		return ErrGenerationChanged
	}
	var digest string
	if err = tx.QueryRow(ctx, `SELECT digest FROM segmentations WHERE organization=$1 AND id=$2 AND version_id=$3`, org, seg.ID, seg.VersionID).Scan(&digest); err != nil {
		return err
	}
	if digest != content.SegmentationDigest(seg) {
		return content.ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO projection_coverage VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, org, seg.VersionID, g.ID, seg.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE record_versions SET baseline_ready=true,processing='idle',error_code='',retrieval_ready_at=`+firstStep("retrieval_ready_at")+` WHERE organization=$1 AND id=$2`, org, seg.VersionID); err != nil {
		return err
	}
	if desired == seg.VersionID {
		if _, err = tx.Exec(ctx, `UPDATE records SET current_version_id=$3 WHERE organization=$1 AND id=$2`, org, recordID, seg.VersionID); err != nil {
			return err
		}
	}
	if !ready {
		if err = appendEvent(ctx, tx, eventInput{Organization: org, CorpusID: corpusID, Kind: "record.retrieval_ready", Resource: "record", ResourceID: recordID, MutationID: content.StableID("baseline", seg.VersionID, g.ID), VersionID: seg.VersionID}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// hydrateSQL looks up a batch of candidates, given as parallel arrays of
// segment and generation ids with the Organization repeated for each, in one
// query: a candidate is returned, with its position in the batch, only while
// its segment belongs to the current eligible Version of its Record and to the
// generation the Corpus routes to. Its embedding in the generation's served
// space, if any, comes with it.
//
// Each row is reached by its primary key from the candidate's, in a LATERAL
// subquery that OFFSET 0 keeps the planner from flattening, so a batch costs a
// few index lookups per candidate whatever the Organization's size (THE-875).
// Until the statistics count an Organization's new rows, the planner takes it
// for one row: as plain joins, it then scanned every Record of the Organization
// for each of its Versions, and with the Organization as a constant it picked
// any index that starts with it. The Organization therefore comes from the
// candidate row, a value the planner cannot see.
var hydrateSQL = `SELECT c.n,r.id,v.id,r.corpus_id,sg.segmentation_id,sg.id,sg.part_key,sg.start_offset,sg.end_offset,sg.text_sha256,b.object_key,b.sha256,b.byte_length,coalesce(e.id,''),coalesce(e.space_id,'')
FROM unnest($1::text[],$2::text[],$3::text[]) WITH ORDINALITY AS c(organization,segment_id,generation_id,n)
CROSS JOIN LATERAL (SELECT sg.* FROM segments sg WHERE sg.organization=c.organization AND sg.id=c.segment_id OFFSET 0) sg
CROSS JOIN LATERAL (SELECT v.* FROM record_versions v WHERE v.organization=c.organization AND v.id=sg.version_id OFFSET 0) v
CROSS JOIN LATERAL (SELECT r.* FROM records r WHERE r.organization=c.organization AND r.id=v.record_id OFFSET 0) r
CROSS JOIN LATERAL (SELECT p.blob_id FROM version_parts p WHERE p.organization=c.organization AND p.version_id=sg.version_id AND p.part_key=sg.part_key OFFSET 0) p
CROSS JOIN LATERAL (SELECT b.object_key,b.sha256,b.byte_length FROM content_blobs b WHERE b.organization=c.organization AND b.blob_id=p.blob_id OFFSET 0) b
CROSS JOIN LATERAL (SELECT FROM projection_coverage pc WHERE pc.organization=c.organization AND pc.version_id=sg.version_id AND pc.generation_id=c.generation_id AND pc.segmentation_id=sg.segmentation_id OFFSET 0) pc
LEFT JOIN LATERAL (SELECT a.id,a.space_id FROM embedding_coverage ec JOIN embedding_artifacts a ON (a.organization,a.id)=(ec.organization,ec.artifact_id) JOIN projection_generations g ON g.id=ec.generation_id AND g.space_id=a.space_id
  WHERE ec.organization=c.organization AND ec.segment_id=c.segment_id AND ec.generation_id=c.generation_id ORDER BY a.id LIMIT 1) e ON true
WHERE c.generation_id=` + routedGenerationSQL("r.organization", "r.corpus_id") + ` AND r.current_version_id=v.id AND ` + eligibleVersionSQL

// Hydrate looks a batch of candidates up in one query (hydrateSQL). A
// candidate outside the caller's Corpora is absent, as if it did not exist.
func (s ContentStore) Hydrate(ctx context.Context, scope corpus.Scope, cs []content.Candidate) (map[int]content.Located, error) {
	out := map[int]content.Located{}
	if len(cs) == 0 {
		return out, nil
	}
	orgs, segments, generations := make([]string, len(cs)), make([]string, len(cs)), make([]string, len(cs))
	for i, c := range cs {
		orgs[i], segments[i], generations[i] = scope.Organization, c.SegmentID, c.GenerationID
	}
	rows, err := s.Pool.Query(ctx, hydrateSQL, orgs, segments, generations)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n int
		var l content.Located
		var corpusID string
		h := &l.Hydrated
		if err = rows.Scan(&n, &h.RecordID, &h.VersionID, &corpusID, &h.SegmentationID, &h.Segment.ID, &h.Segment.PartKey, &h.Segment.Start, &h.Segment.End, &h.TextSHA256,
			&l.Blob.Key, &l.Blob.SHA256, &l.Blob.Size, &h.EmbeddingID, &h.SpaceID); err != nil {
			return nil, err
		}
		if !scope.Contains(corpusID) {
			continue
		}
		h.GenerationID = cs[n-1].GenerationID
		h.Availability = content.Availability{State: "retrieval_ready", Current: true, Searchable: true}
		out[n-1] = l
	}
	return out, rows.Err()
}
func (s ContentStore) VersionStatus(ctx context.Context, org, id string) (content.Availability, content.Processing, string, error) {
	var a content.Availability
	var p content.Processing
	var code string
	var baseline, quarantine, withdrawn bool
	err := s.Pool.QueryRow(ctx, `SELECT v.baseline_ready,v.quarantined,r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id),coalesce(r.current_version_id=v.id,false),v.processing,v.error_code FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2`, org, id).Scan(&baseline, &quarantine, &withdrawn, &a.Current, &p.State, &code)
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
