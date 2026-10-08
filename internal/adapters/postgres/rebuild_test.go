package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/app"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/quarantine"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"golang.org/x/sync/errgroup"
)

// The cutover guards public tests cannot isolate: coverage validation against
// promotions and enrichment landing on the old generation mid-build, atomic
// idempotent activation, stale work after cutover and neighbour Corpus routing.
func TestRebuildCoverageReconciliationAndAtomicCutover(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	if err := app.BootstrapDatabase(ctx, pool, nil); err != nil {
		t.Fatal(err)
	}
	org := fmt.Sprintf("adapter-rebuild-%d", time.Now().UnixNano())
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read", "search:query"}, Corpora: []string{"*"}}
	corpora := corpus.Service{Store: postgres.Store{Pool: pool}}
	a, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "a", Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := corpora.Create(ctx, scope, corpus.CreateInput{Key: "b", Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	store := contentStores(pool)
	var rebuild retrieval.RebuildStore = store
	segmented := func(corpusID, key string) content.Segmentation {
		return rebuildSegmentation(t, ctx, store, scope, corpusID, key)
	}
	promote := func(corpusID string, seg content.Segmentation) error {
		g, err := store.Generation(ctx, org, corpusID)
		if err != nil {
			t.Fatal(err)
		}
		return store.Promote(ctx, org, seg, g)
	}
	x1 := rebuildSegmentation(t, ctx, store, scope, a.ID, "x1",
		content.SegmentInput{Start: 0, End: 3}, content.SegmentInput{Start: 3, End: 6}, content.SegmentInput{Start: 6, End: 10})
	y1 := segmented(b.ID, "y1")
	if err = promote(a.ID, x1); err != nil {
		t.Fatal(err)
	}
	if err = promote(b.ID, y1); err != nil {
		t.Fatal(err)
	}
	// Model an already serving pre-upgrade generation; rebuilding, not an
	// ALTER default, is the only way this Corpus gains item keyword semantics.
	if _, err := pool.Exec(ctx, `UPDATE projection_generations SET item_keywords_projected=false WHERE id=(SELECT id FROM projection_generations WHERE active)`); err != nil {
		t.Fatal(err)
	}
	prior, err := store.Generation(ctx, org, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	op, err := store.AcceptRebuild(ctx, org, a.ID, "rebuild", []byte(`{"idempotency_key":"rebuild"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rebuild.ActivateRebuild(ctx, org, op.ID); !errors.Is(err, operations.ErrNotRunning) {
		t.Fatalf("queued Operation activated: %v", err)
	}
	target, err := rebuild.BeginRebuild(ctx, org, op.ID)
	if err != nil || target.Operation.State != operations.StateRunning || target.Generation.ID != op.TargetGenerationID || target.Generation.Collection != prior.Collection || target.Generation.SpaceID != prior.SpaceID || !target.Generation.ItemKeywordsProjected || prior.ItemKeywordsProjected {
		t.Fatalf("begin %+v %v", target, err)
	}
	candidates := func(want ...string) []retrieval.RebuildCandidate {
		t.Helper()
		got, err := rebuild.RebuildCandidates(ctx, org, op.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("candidates %+v, want versions %v", got, want)
		}
		for i := range want {
			if got[i].VersionID != want[i] {
				t.Fatalf("candidates %+v, want versions %v", got, want)
			}
		}
		return got
	}
	activate := func(want bool) {
		t.Helper()
		done, err := rebuild.ActivateRebuild(ctx, org, op.ID)
		if err != nil || done != want {
			t.Fatalf("activate = %v %v, want %v", done, err, want)
		}
	}
	candidates(x1.VersionID)
	activate(false) // Empty target coverage cannot be activated.
	tampered := x1
	tampered.Segments = []content.Segment{{ID: x1.Segments[0].ID, PartKey: "body", Start: 0, End: 3, Text: "Reb"}}
	if _, err = rebuild.CoverRebuild(ctx, org, op.ID, tampered, nil); !errors.Is(err, content.ErrConflict) {
		t.Fatalf("segmentation differing from the durable artifact: %v", err)
	}
	if covered, err := rebuild.CoverRebuild(ctx, org, op.ID, x1, nil); err != nil || !covered {
		t.Fatalf("cover %v %v", covered, err)
	}
	candidates()
	// A promotion committed on the still-routed generation during the build
	// reopens a gap that activation must detect.
	x2 := segmented(a.ID, "x2")
	if err = promote(a.ID, x2); err != nil {
		t.Fatal(err)
	}
	activate(false)
	candidates(x2.VersionID)
	if _, err = rebuild.CoverRebuild(ctx, org, op.ID, x2, nil); err != nil {
		t.Fatal(err)
	}
	// A terminal refusal of an already searchable version is held atomically.
	// Retrying the same observation must not duplicate the counter or sample.
	blocked := segmented(a.ID, "blocked")
	if err = promote(a.ID, blocked); err != nil {
		t.Fatal(err)
	}
	reason := content.Diagnostic{Code: "ingestion_refused", Message: "invalid provider output", Plugin: "example.paged", PluginVersion: "0.1.0"}
	for range 2 {
		if err = rebuild.QuarantineRebuild(ctx, org, op.ID, blocked.VersionID, reason); err != nil {
			t.Fatal(err)
		}
	}
	// A neighboring corpus must never be quarantined by this operation.
	if err = rebuild.QuarantineRebuild(ctx, org, op.ID, y1.VersionID, reason); err != nil {
		t.Fatal(err)
	}
	admin := scope
	admin.Actions = append(admin.Actions, operations.BackfillPermission)
	q := quarantine.Service{Store: store}
	entries, err := q.List(ctx, admin, quarantine.Filter{CorpusID: a.ID}, "", quarantine.MaxPage)
	if err != nil || len(entries) != 1 || entries[0].VersionID != blocked.VersionID || entries[0].Reason.Plugin != reason.Plugin || entries[0].Stage != content.QuarantineIngestion {
		t.Fatalf("rebuild refusal absent from quarantine: %+v %v", entries, err)
	}
	activeReprocessPlan(t, ctx, pool)
	estimate, _, err := q.Request(ctx, admin, quarantine.Request{Key: "repair", Filter: quarantine.Filter{CorpusID: a.ID}, DryRun: true})
	if err != nil || estimate.Versions != 1 {
		t.Fatalf("rebuild quarantine cannot be reprocessed: %+v %v", estimate, err)
	}
	// Enrichment committed on the old generation after lexical coverage requires
	// the target to reuse the stored vector artifact.
	var manifest []byte
	if err = pool.QueryRow(ctx, `SELECT manifest FROM vector_spaces WHERE id=$1`, prior.SpaceID).Scan(&manifest); err != nil {
		manifest = []byte(`{"fixture":true}`)
	}
	var artifacts []content.Embedding
	for _, passage := range x1.Segments {
		artifact := content.Embedding{ID: "artifact-" + passage.ID, DerivationID: "derivation-" + passage.ID, Organization: org, CorpusID: a.ID, VersionID: x1.VersionID, SegmentID: passage.ID, SegmentationID: x1.ID, SpaceID: prior.SpaceID}
		if err = store.SaveEmbedding(ctx, artifact, content.VectorSpace{ID: prior.SpaceID, Manifest: json.RawMessage(manifest)}); err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, artifact)
	}
	artifact := artifacts[0]
	if err = store.CommitEnrichment(ctx, org, x1, prior, artifacts); err != nil {
		t.Fatal(err)
	}
	activate(false)
	if got := candidates(x1.VersionID); !got[0].VectorsRequired {
		t.Fatalf("vector coverage not required: %+v", got)
	}
	// Concurrent workers and retries can submit the same coverage. Journal
	// and Operation locks serialize commits; duplicate submissions must count
	// each Version and embedding once.
	if _, err = rebuild.CoverRebuild(ctx, org, op.ID, x1, artifacts); err != nil {
		t.Fatal(err)
	}
	// A running operation from an older binary has only the compatibility keys.
	if _, err = pool.Exec(ctx, `UPDATE operations SET counters=counters-'versions_covered'-'passages_covered' WHERE organization=$1 AND id=$2`, org, op.ID); err != nil {
		t.Fatal(err)
	}
	var covers errgroup.Group
	covers.SetLimit(8)
	for range 8 {
		covers.Go(func() error {
			_, err := rebuild.CoverRebuild(ctx, org, op.ID, x1, artifacts)
			return err
		})
		covers.Go(func() error { _, err := rebuild.CoverRebuild(ctx, org, op.ID, x2, nil); return err })
	}
	if err = covers.Wait(); err != nil {
		t.Fatal(err)
	}
	activate(true)
	read, err := store.Operation(ctx, org, op.ID)
	if err != nil || read.State != operations.StateSucceeded || read.ResultGenerationID != op.TargetGenerationID || read.Counters["indexed"] != 2 || read.Counters["vectors_reused"] != 3 || read.Counters["versions_covered"] != 2 || read.Counters["passages_covered"] != 3 || read.Counters["versions_quarantined"] != 1 || len(read.Errors) != 1 || read.Errors[0].Code != reason.Code {
		t.Fatalf("succeeded operation %+v %v", read, err)
	}
	if g, _ := store.Generation(ctx, org, a.ID); g.ID != op.TargetGenerationID {
		t.Fatalf("Corpus A routed to %s", g.ID)
	}
	if g, _ := store.Generation(ctx, org, b.ID); g.ID != prior.ID {
		t.Fatalf("neighbour Corpus B rerouted to %s", g.ID)
	}
	// Hydration follows canonical routing, not whichever generation a candidate names.
	if _, err = hydrateOne(ctx, store, scope, content.Candidate{SegmentID: x1.Segments[0].ID, GenerationID: prior.ID}); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatalf("stale-generation candidate hydrated: %v", err)
	}
	h, err := hydrateOne(ctx, store, scope, content.Candidate{SegmentID: x1.Segments[0].ID, GenerationID: op.TargetGenerationID})
	if err != nil || h.GenerationID != op.TargetGenerationID || h.EmbeddingID != artifact.ID {
		t.Fatalf("target hydration %+v %v", h, err)
	}
	if _, err = hydrateOne(ctx, store, scope, content.Candidate{SegmentID: y1.Segments[0].ID, GenerationID: prior.ID}); err != nil {
		t.Fatalf("neighbour Corpus hydration: %v", err)
	}
	// Work still carrying the old generation after cutover must retry on the new route.
	x3 := segmented(a.ID, "x3")
	if err = store.Promote(ctx, org, x3, prior); !errors.Is(err, postgres.ErrGenerationChanged) {
		t.Fatalf("stale promotion after cutover: %v", err)
	}
	if err = promote(a.ID, x3); err != nil {
		t.Fatal(err)
	}
	// Recovery after activation recognizes the same target without new facts.
	activate(true)
	if err = rebuild.FailRebuild(ctx, org, op.ID, operations.Error{Code: "late_failure", Message: "late failure"}); err != nil {
		t.Fatal(err)
	}
	if read, _ = store.Operation(ctx, org, op.ID); read.State != operations.StateSucceeded || len(read.Errors) != 1 {
		t.Fatalf("terminal success overwritten: %+v", read)
	}
	var transitions int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM change_events WHERE organization=$1 AND event_type='operation.updated' AND resource_id=$2`, org, op.ID).Scan(&transitions); err != nil || transitions != 3 {
		t.Fatalf("operation.updated transitions = %d %v, want queued/running/succeeded", transitions, err)
	}
	// A terminal failure never installs its route.
	failed, err := store.AcceptRebuild(ctx, org, b.ID, "doomed", []byte(`{"idempotency_key":"doomed"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rebuild.BeginRebuild(ctx, org, failed.ID); err != nil {
		t.Fatal(err)
	}
	if err = rebuild.FailRebuild(ctx, org, failed.ID, operations.Error{Code: "embedding_artifact_unavailable", Message: "stored embedding artifact unavailable"}); err != nil {
		t.Fatal(err)
	}
	if _, err = rebuild.ActivateRebuild(ctx, org, failed.ID); !errors.Is(err, operations.ErrNotRunning) {
		t.Fatalf("failed Operation activated: %v", err)
	}
	if read, _ = store.Operation(ctx, org, failed.ID); read.State != operations.StateFailed || len(read.Errors) != 1 || read.Errors[0].Code != "embedding_artifact_unavailable" {
		t.Fatalf("failed operation %+v", read)
	}
	if g, _ := store.Generation(ctx, org, b.ID); g.ID != prior.ID {
		t.Fatalf("failed rebuild rerouted Corpus B to %s", g.ID)
	}
}

func rebuildSegmentation(t *testing.T, ctx context.Context, store fixtureContentStores, scope corpus.Scope, corpusID, key string, cuts ...content.SegmentInput) content.Segmentation {
	org := scope.Organization
	service := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Baseline: store}
	t.Helper()
	cmd := content.Command{Key: key, Source: content.Source{CorpusID: corpusID, Namespace: "rebuild", RecordKey: key}, Content: content.Text{Kind: "text", Text: "Rebuild " + key}}
	r, err := service.Accept(ctx, scope, cmd)
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := store.Work(ctx, org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	text := content.Blob{Key: "fixture/" + key, SHA256: "rebuild-text-" + key, Size: int64(len("Rebuild " + key))}
	if err = store.Publish(ctx, work, publication(text, content.Blob{Key: "fixture/m-" + key, SHA256: "rebuild-manifest-" + key, Size: 2})); err != nil {
		t.Fatal(err)
	}
	seg := content.Segmentation{ID: content.StableID("segmentation", org, work.VersionID, "fixture"), VersionID: work.VersionID, Recipe: "fixture", Provenance: json.RawMessage(`{}`), Segments: []content.Segment{{ID: content.StableID("segment", work.VersionID), PartKey: "body", Start: 0, End: len([]rune("Rebuild " + key)), Text: "Rebuild " + key}}}
	if len(cuts) > 0 {
		seg.Segments = nil
		text := []rune("Rebuild " + key)
		for i, cut := range cuts {
			seg.Segments = append(seg.Segments, content.Segment{ID: content.StableID("segment", work.VersionID, fmt.Sprint(i)), PartKey: "body", Start: cut.Start, End: cut.End, Text: string(text[cut.Start:cut.End])})
		}
	}
	if err = store.SaveSegmentation(ctx, org, seg); err != nil {
		t.Fatal(err)
	}
	return seg
}

// Progress survives a new store instance; changes behind it wait for the final
// reconciliation pass rather than slowing every forward candidate lookup.
func TestRebuildCursorResumesAndSweepsNewVersions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	if err := app.BootstrapDatabase(ctx, pool, app.DeploymentSpaces(nil)); err != nil {
		t.Fatal(err)
	}
	// Fixed domain identities in a fresh database make ordering reproducible.
	f := controlFixture{t: t, ctx: ctx, pool: pool, org: "adapter-rebuild-cursor", store: contentStores(pool)}
	corpusID := f.corpus("cursor")
	scope := corpus.Scope{Organization: f.org, Actions: []string{"content:write"}, Corpora: []string{"*"}}
	var versions []content.Segmentation
	for i := range 4 {
		versions = append(versions, rebuildSegmentation(t, f.ctx, f.store, scope, corpusID, fmt.Sprint(i)))
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].VersionID < versions[j].VersionID })
	prior, err := f.store.Generation(f.ctx, f.org, corpusID)
	if err != nil {
		t.Fatal(err)
	}
	for _, seg := range versions[1:] {
		if err := f.store.Promote(f.ctx, f.org, seg, prior); err != nil {
			t.Fatal(err)
		}
	}
	op := f.rebuild(corpusID, "cursor")
	if _, err := f.store.BeginRebuild(f.ctx, f.org, op.ID); err != nil {
		t.Fatal(err)
	}
	checkpoint := f.store.RebuildStore
	page, err := f.store.RebuildCandidates(f.ctx, f.org, op.ID, 2)
	if err != nil || len(page) != 2 || page[0].VersionID != versions[1].VersionID || page[1].VersionID != versions[2].VersionID {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	// Dispatch can move beyond an unfinished low Version without committing
	// that scan position. Forward exhaustion must not wrap into active work.
	forward, err := checkpoint.RebuildCandidatesAfter(f.ctx, f.org, op.ID, page[1].VersionID, 2)
	if err != nil || len(forward) != 1 || forward[0].VersionID != versions[3].VersionID {
		t.Fatalf("forward scan=%+v err=%v; want only highest Version", forward, err)
	}
	if exhausted, err := checkpoint.RebuildCandidatesAfter(f.ctx, f.org, op.ID, versions[3].VersionID, 2); err != nil || len(exhausted) != 0 {
		t.Fatalf("forward exhaustion wrapped into unfinished work: %+v %v", exhausted, err)
	}
	if _, err := checkpoint.CoverRebuild(f.ctx, f.org, op.ID, versions[2], nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		version string
		pending bool
	}{
		{versions[1].VersionID, true}, {versions[2].VersionID, false}, {"missing-version", false},
	} {
		if pending, err := checkpoint.RebuildCandidatePending(f.ctx, f.org, op.ID, tc.version); err != nil || pending != tc.pending {
			t.Fatalf("Version %s pending=%v err=%v, want %v", tc.version, pending, err, tc.pending)
		}
	}
	interrupted := postgres.RebuildStore{Pool: f.pool}
	target, err := interrupted.BeginRebuild(f.ctx, f.org, op.ID)
	if err != nil || target.Cursor != "" {
		t.Fatalf("dispatch changed durable cursor: %q %v", target.Cursor, err)
	}
	unfinished, err := interrupted.RebuildCandidates(f.ctx, f.org, op.ID, 2)
	if err != nil || len(unfinished) != 2 || unfinished[0].VersionID != versions[1].VersionID || unfinished[1].VersionID != versions[3].VersionID {
		t.Fatalf("resume skipped unfinished low Version: %+v %v", unfinished, err)
	}
	for _, seg := range versions[1:3] {
		if _, err := interrupted.CoverRebuild(f.ctx, f.org, op.ID, seg, nil); err != nil {
			t.Fatal(err)
		}
	}
	progress, err := f.store.Operation(f.ctx, f.org, op.ID)
	if err != nil || progress.Counters["versions_covered"] != 2 {
		t.Fatalf("out-of-order coverage replay counted twice: %+v %v", progress.Counters, err)
	}
	if err := checkpoint.CheckpointRebuild(f.ctx, f.org, op.ID, page[1].VersionID); err != nil {
		t.Fatal(err)
	}
	// Accept and promote a new Version behind the durable cursor. IDs are opaque
	// hashes, so select a lower one through bounded real acceptance attempts.
	var late content.Segmentation
	for i := range 64 {
		candidate := rebuildSegmentation(t, f.ctx, f.store, scope, corpusID, fmt.Sprintf("late-%d", i))
		if candidate.VersionID < page[1].VersionID {
			late = candidate
			break
		}
	}
	if late.ID == "" {
		t.Fatal("no newly accepted Version sorted behind the checkpoint")
	}
	versions[0] = late
	if err := f.store.Promote(f.ctx, f.org, versions[0], prior); err != nil {
		t.Fatal(err)
	}
	resumed := postgres.RebuildStore{Pool: f.pool}
	page, err = resumed.RebuildCandidates(f.ctx, f.org, op.ID, 2)
	if err != nil || len(page) != 1 || page[0].VersionID != versions[3].VersionID {
		t.Fatalf("resumed forward page=%+v err=%v; must exclude new Version behind cursor", page, err)
	}
	if done, err := resumed.ActivateRebuild(f.ctx, f.org, op.ID); err != nil || done {
		t.Fatalf("partial activation=%v err=%v", done, err)
	}
	if _, err := resumed.CoverRebuild(f.ctx, f.org, op.ID, versions[3], nil); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.CheckpointRebuild(f.ctx, f.org, op.ID, versions[3].VersionID); err != nil {
		t.Fatal(err)
	}
	page, err = resumed.RebuildCandidates(f.ctx, f.org, op.ID, 2)
	if err != nil || len(page) != 1 || page[0].VersionID != versions[0].VersionID {
		t.Fatalf("final sweep=%+v err=%v", page, err)
	}
	if _, err := resumed.CoverRebuild(f.ctx, f.org, op.ID, versions[0], nil); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.CheckpointRebuild(f.ctx, f.org, op.ID, versions[0].VersionID); err != nil {
		t.Fatal(err)
	}
	if done, err := resumed.ActivateRebuild(f.ctx, f.org, op.ID); err != nil || !done {
		t.Fatalf("complete activation=%v err=%v", done, err)
	}
	if err := checkpoint.CheckpointRebuild(f.ctx, f.org, op.ID, versions[3].VersionID); !errors.Is(err, operations.ErrNotRunning) {
		t.Fatalf("terminal checkpoint=%v", err)
	}
}
