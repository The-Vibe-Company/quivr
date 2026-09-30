package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/backfill"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// backfillFixture is a Corpus routed to a generation of its own that serves
// a space of the run, with Versions segmented by a plugin and, when
// enriched, a vector in the served space for every segment.
type backfillFixture struct {
	pool                  *pgxpool.Pool
	store                 postgres.ContentStore
	org, corpusID         string
	served, target        string
	generation            content.Generation
	registration, plan    string
	versions, unenriched  []string
	segments              map[string]content.Segmentation
	accepted              map[string]time.Time
	recordOf              map[string]string
	window                time.Time
	servedArtifactOfSegID map[string]string
}

func newBackfillFixture(t *testing.T, ctx context.Context, enriched, unenriched int) *backfillFixture {
	t.Helper()
	pool := rebuildAdapterPool(t, ctx)
	run := time.Now().UnixNano()
	f := &backfillFixture{pool: pool, store: postgres.ContentStore{Pool: pool}, org: fmt.Sprintf("adapter-backfill-%d", run),
		served: fmt.Sprintf("example.fill.small%d@1", run), target: fmt.Sprintf("example.fill.large%d@1", run),
		segments: map[string]content.Segmentation{}, accepted: map[string]time.Time{}, recordOf: map[string]string{}, servedArtifactOfSegID: map[string]string{}}
	for _, space := range []string{f.served, f.target} {
		// Spaces of the run only: the deployment's registry is left as it is.
		if _, err := pool.Exec(ctx, `INSERT INTO vector_spaces(id,manifest,role,metric) VALUES($1,'{}','evaluation','cosine')`, space); err != nil {
			t.Fatal(err)
		}
	}
	f.registration, f.plan = fmt.Sprintf("registration-%d", run), fmt.Sprintf("plan-%d", run)
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_registrations(id,plugin_id,version,endpoint,manifest_digest,contributions,roles,state) VALUES($1,'example.fill','0.1.0','http://127.0.0.1:1',$1,'{ingestion}','{ingestion}','inactive')`, f.registration); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO pipeline_plans(id) VALUES($1)`, f.plan); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO pipeline_plan_roles(plan_id,role,registration_id) VALUES($1,'ingestion',$2)`, f.plan, f.registration); err != nil {
		t.Fatal(err)
	}
	// A backfill is pinned to the active plan, which names its registration:
	// the fixture's plan is active for the test, then the deployment's again.
	var active *string
	if err := pool.QueryRow(ctx, `SELECT (SELECT plan_id FROM active_pipeline_plan)`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO active_pipeline_plan(plan_id) VALUES($1) ON CONFLICT(singleton) DO UPDATE SET plan_id=EXCLUDED.plan_id`, f.plan); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		restore, args := `DELETE FROM active_pipeline_plan`, []any{}
		if active != nil {
			restore, args = `UPDATE active_pipeline_plan SET plan_id=$1`, []any{*active}
		}
		if _, err := pool.Exec(context.Background(), restore, args...); err != nil {
			t.Errorf("restore the active plan: %v", err)
		}
	})
	scope := corpus.Scope{Organization: f.org, Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "fill", Name: "Fill"})
	if err != nil {
		t.Fatal(err)
	}
	f.corpusID = c.ID
	f.generation = content.Generation{ID: fmt.Sprintf("generation-fill-%d", run), Collection: fmt.Sprintf("Fill%d", run), ProfileVersion: "fill", SpaceID: f.served, SourceNamespaceProjected: true,
		Spaces: []content.GenerationSpace{{ID: f.served, Metric: "cosine"}}, SpacesProjected: true}
	if _, err = pool.Exec(ctx, `INSERT INTO projection_generations(id,collection,profile_version,active,space_id,source_namespace_projected,spaces,spaces_projected) VALUES($1,$2,'fill',false,$3,true,jsonb_build_array(jsonb_build_object('id',$3::text,'metric','cosine')),true)`, f.generation.ID, f.generation.Collection, f.served); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO corpus_projection_routes(organization,corpus_id,generation_id) VALUES($1,$2,$3)`, f.org, f.corpusID, f.generation.ID); err != nil {
		t.Fatal(err)
	}
	service := content.Service{Repository: f.store, Baseline: f.store}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < enriched+unenriched; i++ {
		text := fmt.Sprintf("Article %d about the harbour.", i)
		r, err := service.Accept(ctx, scope, content.Command{Key: fmt.Sprintf("a%d", i), Source: content.Source{CorpusID: f.corpusID, Namespace: "fill", RecordKey: fmt.Sprintf("a%d", i)}, Content: content.Text{Kind: "text", Text: text}})
		if err != nil {
			t.Fatal(err)
		}
		work, _, err := f.store.Work(ctx, f.org, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.store.Publish(ctx, work, publication(content.Blob{Key: "fixture/text", SHA256: fmt.Sprintf("fill-text-%d", i), Size: int64(len(text))}, content.Blob{Key: "fixture/manifest", SHA256: "fill-manifest", Size: 2})); err != nil {
			t.Fatal(err)
		}
		// One Version a day, so a window selects some of them.
		accepted := base.AddDate(0, 0, i)
		if _, err = pool.Exec(ctx, `UPDATE ingestion_receipts SET accepted_at=$3 WHERE organization=$1 AND record_id=$2`, f.org, work.RecordID, accepted); err != nil {
			t.Fatal(err)
		}
		v := content.Version{ID: work.VersionID, RecordID: work.RecordID, Manifest: content.Manifest{Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: text}}}}}
		seg, err := content.PluginSegmentation(f.org, v, "plugin:example.fill@0.1.0", json.RawMessage(`{"plugin_id":"example.fill"}`), []content.SegmentInput{{PartKey: "body", Start: 0, End: 10}, {PartKey: "body", Start: 11, End: len([]rune(text))}})
		if err != nil {
			t.Fatal(err)
		}
		if err = f.store.SaveSegmentation(ctx, f.org, seg); err != nil {
			t.Fatal(err)
		}
		if err = f.store.Promote(ctx, f.org, seg, f.generation); err != nil {
			t.Fatal(err)
		}
		f.segments[v.ID], f.accepted[v.ID], f.recordOf[v.ID] = seg, accepted, v.RecordID
		if i >= enriched {
			f.unenriched = append(f.unenriched, v.ID)
			continue
		}
		f.versions = append(f.versions, v.ID)
		for _, p := range seg.Segments {
			f.servedArtifactOfSegID[p.ID] = f.cover(t, ctx, v.ID, p.ID, f.served)
		}
	}
	return f
}

// cover stores an artifact of a segment in a space and records its coverage
// in the fixture's generation, as enrichment does.
func (f *backfillFixture) cover(t *testing.T, ctx context.Context, versionID, segmentID, space string) string {
	t.Helper()
	e := content.Embedding{ID: "artifact-" + segmentID + "-" + space, DerivationID: "derivation-" + segmentID + "-" + space, Organization: f.org, CorpusID: f.corpusID, VersionID: versionID, SegmentID: segmentID, SpaceID: space, Producer: "plugin:example.fill@0.1.0"}
	if err := f.store.SaveEmbedding(ctx, e, content.VectorSpace{ID: space, Manifest: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO embedding_coverage(organization,segment_id,generation_id,artifact_id,space_id) VALUES($1,$2,$3,$4,$5)`, f.org, segmentID, f.generation.ID, e.ID, space); err != nil {
		t.Fatal(err)
	}
	return e.ID
}

// fill fills a Version's target space the way a step does.
func (f *backfillFixture) fill(ctx context.Context, t *testing.T, opID string, g content.Generation, versionID string) error {
	t.Helper()
	var artifacts []content.Embedding
	for _, p := range f.segments[versionID].Segments {
		e := content.Embedding{ID: "artifact-" + p.ID + "-" + f.target, DerivationID: "derivation-" + p.ID + "-" + f.target, Organization: f.org, CorpusID: f.corpusID, VersionID: versionID, SegmentID: p.ID, SpaceID: f.target, Producer: "plugin:example.fill@0.2.0"}
		if err := f.store.SaveEmbedding(ctx, e, content.VectorSpace{ID: f.target, Manifest: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, e)
	}
	return f.store.CoverBackfill(ctx, f.org, opID, g, versionID, artifacts)
}

func (f *backfillFixture) spec(after *time.Time) operations.Backfill {
	return operations.Backfill{RegistrationID: f.registration, Spaces: []string{f.target}, AcceptedAfter: after, Estimate: operations.BackfillEstimate{Versions: 2}}
}

func (f *backfillFixture) events(t *testing.T, ctx context.Context) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM change_events WHERE organization=$1 AND event_type<>'operation.updated'`, f.org).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A backfill measures only the enriched Versions of its window, is accepted
// once per key and scope, adds its spaces to the Corpus's generation on its
// first step, fills Versions after its checkpoint without a content event,
// and stops committing as soon as it is paused or canceled.
func TestBackfillScopeCheckpointAndControl(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newBackfillFixture(t, ctx, 4, 1)
	store := f.store
	after := f.accepted[f.versions[1]]
	spec := f.spec(&after)

	// The window holds the 3 last enriched Versions; the unenriched one is
	// left to live enrichment.
	size, err := store.BackfillSize(ctx, f.org, spec, f.corpusID)
	if err != nil || size.Versions != 3 || size.Segments != 6 {
		t.Fatalf("size %+v %v", size, err)
	}
	if whole, err := store.BackfillSize(ctx, f.org, f.spec(nil), f.corpusID); err != nil || whole.Versions != 4 {
		t.Fatalf("whole Corpus %+v %v", whole, err)
	}
	// A dry run is kept under its key; another scope under the key conflicts.
	if err = store.RecordEstimate(ctx, f.org, f.corpusID, "k", []byte("scope-a"), operations.BackfillEstimate{Versions: 3}); err != nil {
		t.Fatal(err)
	}
	if err = store.RecordEstimate(ctx, f.org, f.corpusID, "k", []byte("scope-b"), operations.BackfillEstimate{}); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("another scope under the key: %v", err)
	}
	if canonical, e, err := store.BackfillEstimate(ctx, f.org, f.corpusID, "k"); err != nil || string(canonical) != "scope-a" || e.Versions != 3 {
		t.Fatalf("estimate %s %+v %v", canonical, e, err)
	}
	if _, err = store.AcceptBackfill(ctx, f.org, f.corpusID, "k0", []byte("scope-0"), operations.Backfill{RegistrationID: "registration-elsewhere", Spaces: spec.Spaces}); !errors.Is(err, backfill.ErrRegistrationNotActive) {
		t.Fatalf("a registration the active plan does not name: %v", err)
	}
	op, err := store.AcceptBackfill(ctx, f.org, f.corpusID, "k", []byte("scope-a"), spec)
	if err != nil || op.State != operations.StateQueued || op.Backfill == nil || op.Backfill.RegistrationID != f.registration || op.Backfill.PlanID != f.plan {
		t.Fatalf("accept %+v %v", op, err)
	}
	// It is pinned to that plan from acceptance on, before any worker step.
	var pinned string
	if err = f.pool.QueryRow(ctx, `SELECT plan_id FROM pipeline_plan_work WHERE kind='operation' AND organization=$1 AND work_id=$2`, f.org, op.ID).Scan(&pinned); err != nil || pinned != f.plan {
		t.Fatalf("pinned to %q %v, want %s", pinned, err, f.plan)
	}
	if again, err := store.AcceptBackfill(ctx, f.org, f.corpusID, "k", []byte("scope-a"), spec); err != nil || again.ID != op.ID {
		t.Fatalf("replay %+v %v", again, err)
	}
	if _, err = store.AcceptBackfill(ctx, f.org, f.corpusID, "k", []byte("scope-b"), spec); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("another scope under an accepted key: %v", err)
	}

	// The first step runs it, pins its plan, counts its scope and makes the
	// generation carry the target space.
	target, err := store.BeginBackfill(ctx, f.org, op.ID, f.plan)
	if err != nil || target.Operation.State != operations.StateRunning || target.Operation.Backfill.PlanID != f.plan || target.Generation.Carries(f.target) {
		t.Fatalf("begin %+v %+v %v", target.Operation, target.Generation, err)
	}
	g, err := store.CarryBackfillSpaces(ctx, f.org, op.ID)
	if err != nil || g.ID != f.generation.ID || g.SpaceID != f.served || !g.Carries(f.target) || len(g.Spaces) != 2 {
		t.Fatalf("generation %+v %v", g, err)
	}
	if again, err := store.CarryBackfillSpaces(ctx, f.org, op.ID); err != nil || len(again.Spaces) != 2 {
		t.Fatalf("carrying the spaces again %+v %v", again, err)
	}
	if counted, err := store.Operation(ctx, f.org, op.ID); err != nil || counted.Counters["versions_in_scope"] != 3 {
		t.Fatalf("scope counted once %+v %v", counted.Counters, err)
	}
	if routed, err := store.Generation(ctx, f.org, f.corpusID); err != nil || !routed.Carries(f.target) {
		t.Fatalf("routed generation %+v %v", routed, err)
	}
	candidates, err := store.BackfillCandidates(ctx, f.org, op.ID, g, 2)
	if err != nil || len(candidates) != 2 || candidates[0].Recipe != "plugin:example.fill@0.1.0" {
		t.Fatalf("candidates %+v %v", candidates, err)
	}
	events := f.events(t, ctx)
	if err = f.fill(ctx, t, op.ID, g, candidates[0].VersionID); err != nil {
		t.Fatal(err)
	}
	if f.events(t, ctx) != events {
		t.Fatal("a backfilled Version appended a change event")
	}
	// The checkpoint moves past it: it is never listed again.
	next, err := store.BackfillCandidates(ctx, f.org, op.ID, g, 10)
	if err != nil || len(next) != 2 || next[0].VersionID != candidates[1].VersionID {
		t.Fatalf("after the checkpoint %+v %v", next, err)
	}
	for _, c := range next {
		if f.accepted[c.VersionID].Before(after) {
			t.Fatalf("a Version outside the window is a candidate: %s", c.VersionID)
		}
	}
	if err = store.SkipBackfill(ctx, f.org, op.ID, next[0].VersionID, backfill.SkipSegmentationDiffers); err != nil {
		t.Fatal(err)
	}

	// Paused, it commits nothing until resumed.
	if paused, err := store.PauseOperation(ctx, f.org, op.ID); err != nil || paused.State != operations.StatePaused {
		t.Fatalf("pause %+v %v", paused, err)
	}
	if err = f.fill(ctx, t, op.ID, g, next[1].VersionID); !errors.Is(err, operations.ErrNotRunning) {
		t.Fatalf("fill while paused: %v", err)
	}
	if held, err := store.BeginBackfill(ctx, f.org, op.ID, f.plan); err != nil || held.Operation.State != operations.StatePaused {
		t.Fatalf("a step while paused %+v %v", held.Operation, err)
	}
	if resumed, err := store.ResumeOperation(ctx, f.org, op.ID); err != nil || resumed.State != operations.StateRunning {
		t.Fatalf("resume %+v %v", resumed, err)
	}
	if err = f.fill(ctx, t, op.ID, g, next[1].VersionID); err != nil {
		t.Fatal(err)
	}
	if rest, err := store.BackfillCandidates(ctx, f.org, op.ID, g, 10); err != nil || len(rest) != 0 {
		t.Fatalf("left %+v %v", rest, err)
	}
	read, err := store.Operation(ctx, f.org, op.ID)
	if err != nil || read.Counters["versions_done"] != 2 || read.Counters["versions_skipped"] != 1 || read.Counters["skipped_segmentation_differs"] != 1 || read.Counters["segments"] != 4 || read.Backfill.Checkpoint != next[1].VersionID {
		t.Fatalf("progress %+v %+v %v", read.Counters, read.Backfill, err)
	}
	// The window's first Version and the ones outside it hold no target vector.
	var covered int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM embedding_coverage WHERE organization=$1 AND generation_id=$2 AND space_id=$3`, f.org, g.ID, f.target).Scan(&covered); err != nil || covered != 4 {
		t.Fatalf("target coverage %d %v", covered, err)
	}
	if err = store.CompleteBackfill(ctx, f.org, op.ID, g.ID); err != nil {
		t.Fatal(err)
	}
	if done, err := store.Operation(ctx, f.org, op.ID); err != nil || done.State != operations.StateSucceeded || done.ResultGenerationID != g.ID {
		t.Fatalf("done %+v %v", done, err)
	}

	// A paused backfill is canceled once its worker confirms it stopped.
	other, err := store.AcceptBackfill(ctx, f.org, f.corpusID, "k2", []byte("scope-c"), f.spec(nil))
	if err != nil {
		t.Fatal(err)
	}
	// One backfill of a Corpus at a time.
	if _, err = store.AcceptBackfill(ctx, f.org, f.corpusID, "k3", []byte("scope-d"), f.spec(nil)); !errors.Is(err, backfill.ErrInProgress) {
		t.Fatalf("a second backfill of the Corpus: %v", err)
	}
	// After the target became the served space (a forced promotion), the
	// Versions enriched in the former served space only are still
	// candidates: the one outside the first window and the one it skipped.
	if _, err = f.pool.Exec(ctx, `UPDATE projection_generations SET space_id=$2 WHERE id=$1`, g.ID, f.target); err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginBackfill(ctx, f.org, other.ID, f.plan); err != nil {
		t.Fatal(err)
	}
	promoted, err := store.CarryBackfillSpaces(ctx, f.org, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	left, err := store.BackfillCandidates(ctx, f.org, other.ID, promoted, 10)
	if err != nil || len(left) != 2 || (left[0].VersionID != f.versions[0] && left[1].VersionID != f.versions[0]) {
		t.Fatalf("candidates after a promotion %+v %v, want %s and the skipped one", left, err, f.versions[0])
	}
	if _, err = store.PauseOperation(ctx, f.org, other.ID); err != nil {
		t.Fatal(err)
	}
	if canceled, err := store.CancelOperation(ctx, f.org, other.ID); err != nil || canceled.State != operations.StateCancelRequested {
		t.Fatalf("cancel a paused backfill %+v %v", canceled, err)
	}
	if err = store.ConfirmCancel(ctx, f.org, other.ID); err != nil {
		t.Fatal(err)
	}
	if canceled, err := store.Operation(ctx, f.org, other.ID); err != nil || canceled.State != operations.StateCanceled {
		t.Fatalf("canceled %+v %v", canceled, err)
	}
	// One canceled before any step releases the pin its acceptance took, so
	// its registration can stop draining.
	queued, err := store.AcceptBackfill(ctx, f.org, f.corpusID, "k4", []byte("scope-e"), f.spec(nil))
	if err != nil {
		t.Fatal(err)
	}
	if canceled, err := store.CancelOperation(ctx, f.org, queued.ID); err != nil || canceled.State != operations.StateCanceled {
		t.Fatalf("cancel a queued backfill %+v %v", canceled, err)
	}
	var pins int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_plan_work WHERE kind='operation' AND organization=$1 AND work_id=$2`, f.org, queued.ID).Scan(&pins); err != nil || pins != 0 {
		t.Fatalf("pins left by a queued backfill canceled: %d %v", pins, err)
	}
}
