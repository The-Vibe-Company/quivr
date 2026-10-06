package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"golang.org/x/sync/errgroup"
)

// The cutover guards public tests cannot isolate: coverage validation against
// promotions and enrichment landing on the old generation mid-build, atomic
// idempotent activation, stale work after cutover and neighbour Corpus routing.
func TestRebuildCoverageReconciliationAndAtomicCutover(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := rebuildAdapterPool(t, ctx)
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
	service := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Baseline: store}
	segmented := func(corpusID, key string) content.Segmentation {
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
		if err = store.SaveSegmentation(ctx, org, seg); err != nil {
			t.Fatal(err)
		}
		return seg
	}
	promote := func(corpusID string, seg content.Segmentation) error {
		g, err := store.Generation(ctx, org, corpusID)
		if err != nil {
			t.Fatal(err)
		}
		return store.Promote(ctx, org, seg, g)
	}
	x1, y1 := segmented(a.ID, "x1"), segmented(b.ID, "y1")
	if err = promote(a.ID, x1); err != nil {
		t.Fatal(err)
	}
	if err = promote(b.ID, y1); err != nil {
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
	if err != nil || target.Operation.State != operations.StateRunning || target.Generation.ID != op.TargetGenerationID || target.Generation.Collection != prior.Collection || target.Generation.SpaceID != prior.SpaceID {
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
	// Enrichment committed on the old generation after lexical coverage requires
	// the target to reuse the stored vector artifact.
	var manifest []byte
	if err = pool.QueryRow(ctx, `SELECT manifest FROM vector_spaces WHERE id=$1`, prior.SpaceID).Scan(&manifest); err != nil {
		manifest = []byte(`{"fixture":true}`)
	}
	artifact := content.Embedding{ID: "artifact-" + x1.Segments[0].ID, DerivationID: "derivation-" + x1.Segments[0].ID, Organization: org, CorpusID: a.ID, VersionID: x1.VersionID, SegmentID: x1.Segments[0].ID, SegmentationID: x1.ID, SpaceID: prior.SpaceID}
	if err = store.SaveEmbedding(ctx, artifact, content.VectorSpace{ID: prior.SpaceID, Manifest: json.RawMessage(manifest)}); err != nil {
		t.Fatal(err)
	}
	if err = store.CommitEnrichment(ctx, org, x1, prior, []content.Embedding{artifact}); err != nil {
		t.Fatal(err)
	}
	activate(false)
	if got := candidates(x1.VersionID); !got[0].VectorsRequired {
		t.Fatalf("vector coverage not required: %+v", got)
	}
	// Concurrent workers and retries can submit the same coverage. Journal
	// and Operation locks serialize commits; duplicate submissions must count
	// each Version and embedding once.
	var covers errgroup.Group
	covers.SetLimit(8)
	for range 8 {
		covers.Go(func() error {
			_, err := rebuild.CoverRebuild(ctx, org, op.ID, x1, []content.Embedding{artifact})
			return err
		})
		covers.Go(func() error { _, err := rebuild.CoverRebuild(ctx, org, op.ID, x2, nil); return err })
	}
	if err = covers.Wait(); err != nil {
		t.Fatal(err)
	}
	activate(true)
	read, err := store.Operation(ctx, org, op.ID)
	if err != nil || read.State != operations.StateSucceeded || read.ResultGenerationID != op.TargetGenerationID || read.Counters["indexed"] != 2 || read.Counters["vectors_reused"] != 1 {
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
	if read, _ = store.Operation(ctx, org, op.ID); read.State != operations.StateSucceeded || len(read.Errors) != 0 {
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
