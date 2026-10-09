package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr/internal/processing"
	"github.com/The-Vibe-Company/quivr/internal/routing"
)

func TestAsyncRoutingRollbackKeepsReadableGapAndRecoversBothVersions(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	f := newIngestionOwnerFixture(t, ctx)
	pool, pluginsStore, oldPlan, live, store, contents, scope, c, registryService, a, b := f.pool, f.pluginsStore, f.oldPlan, f.live, f.store, f.contents, f.scope, f.corpus, f.registryService, f.a, f.b
	refreshes := 0
	registryService.Activated = func(ctx context.Context) {
		writer, err := pool.Begin(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		defer writer.Rollback(ctx)
		var acquired bool
		if err = writer.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock_shared(642003)`).Scan(&acquired); err != nil || !acquired {
			t.Errorf("registry refresh held exclusive routing lock: %v (%v)", acquired, err)
		}
		refreshes++
	}
	routingStore := postgres.RoutingStore{Pool: pool, Registry: registryService}
	if _, spaces, err := (postgres.SpaceStore{Pool: pool}).DescribeVectorSpaces(ctx, scope.Organization, c.ID); err != nil || len(spaces) == 0 || !spaces[0].CorpusEmpty {
		t.Fatalf("empty corpus descriptions %+v: %v", spaces, err)
	}
	pin := func(receipt content.Receipt) context.Context {
		t.Helper()
		if _, _, err := pluginsStore.PinWork(ctx, plugins.WorkIngestion, scope.Organization, receipt.ID, oldPlan.ID); err != nil {
			t.Fatal(err)
		}
		pinned, err := live.Pin(ctx, plugins.Work{Kind: plugins.WorkIngestion, Organization: scope.Organization, ID: receipt.ID, Plan: oldPlan.ID}, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		return pinned
	}
	accept := func(key, text string, recordKey ...string) content.Receipt {
		t.Helper()
		sourceKey := key
		if len(recordKey) > 0 {
			sourceKey = recordKey[0]
		}
		r, err := contents.Accept(ctx, scope, content.Command{Key: key, Source: content.Source{CorpusID: c.ID, Namespace: "docs", RecordKey: sourceKey}, Content: content.Text{Kind: "text", Text: text}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	materialize := func(pinned context.Context, r content.Receipt) (content.Version, content.Segmentation, content.Embedding) {
		t.Helper()
		if err := contents.Materialize(pinned, scope.Organization, r.ID); err != nil {
			t.Fatal(err)
		}
		receipt, err := store.Receipt(ctx, scope.Organization, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		v, err := contents.Version(ctx, scope, receipt.RecordID, receipt.VersionID)
		if err != nil {
			t.Fatal(err)
		}
		text := v.Manifest.Parts[0].Content.Text
		seg, err := content.PluginSegmentation(scope.Organization, v, "plugin:example.hash_embedder@0.1.0", json.RawMessage(`{}`), []content.SegmentInput{{PartKey: "body", Start: 0, End: len([]rune(text))}})
		if err != nil {
			t.Fatal(err)
		}
		if err = contents.SaveSegmentation(ctx, scope.Organization, v, seg); err != nil {
			t.Fatal(err)
		}
		space, ok := (pluginhttp.Ingestor{Pin: a}).Descriptor().VectorSpace("example.hash_embedder.small@1")
		if !ok {
			t.Fatal("fixture primary space is missing")
		}
		vector := make([]float32, space.Dimensions)
		vector[0] = 1
		packed, err := contents.SaveEmbeddingGroup(ctx, seg, space, []content.EmbeddingData{{Artifact: content.EmbeddingInput(scope.Organization, c.ID, v, seg, seg.Segments[0], space, "fixture"), Vector: vector}})
		if err != nil {
			t.Fatal(err)
		}
		artifact := packed[0].Artifact
		return v, seg, artifact
	}
	first := accept("first", "alpha beta gamma")
	firstCtx := pin(first)
	firstV, firstSeg, firstArtifact := materialize(firstCtx, first)
	g, err := store.Generation(ctx, scope.Organization, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Promote(firstCtx, scope.Organization, firstSeg, g); err != nil {
		t.Fatal(err)
	}
	if err = store.CommitEnrichment(firstCtx, scope.Organization, firstSeg, g, []content.Embedding{firstArtifact}); err != nil {
		t.Fatal(err)
	}

	jobs, err := store.ClaimIngestionEvaluations(ctx, 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("initial evaluation jobs %+v: %v", jobs, err)
	}
	publication := &evaluationPublication{}
	evaluator := processing.Evaluator{Store: store, Serving: store, Content: contents, Plugin: &processing.PluginDeriver{Content: contents, Plugin: pluginhttp.Ingestor{Pin: b}}, Projection: publication}
	if err = evaluator.Run(ctx, scope.Organization, jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	// Stage an unrouted rebuild first, then let it become a route during the
	// admin scan. Route invalidation must also restage its owner metadata.
	empty, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "concurrent-empty-rebuild", Name: "Empty rebuild"})
	if err != nil {
		t.Fatal(err)
	}
	emptyRebuild, err := (postgres.OperationStore{Pool: pool}).AcceptRebuild(ctx, scope.Organization, empty.ID, "concurrent-empty-rebuild", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	rebuildStore := postgres.RebuildStore{Pool: pool}
	if _, err = rebuildStore.BeginRebuild(ctx, scope.Organization, emptyRebuild.ID); err != nil {
		t.Fatal(err)
	}
	proveMetadata := true
	run := func(command routing.Command) string {
		t.Helper()
		op, err := routingStore.AcceptRouting(ctx, scope.Organization, command)
		if err != nil {
			t.Fatal(err)
		}
		for {
			var phase string
			if err = pool.QueryRow(ctx, `SELECT phase FROM routing_operations WHERE id=$1`, op.ID).Scan(&phase); err != nil {
				t.Fatal(err)
			}
			if proveMetadata && phase == "cutover" {
				proveMetadata = false
				blocked, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer blocked.Rollback(context.Background())
				if _, err = blocked.Exec(ctx, `SELECT id FROM vector_spaces WHERE id='certified.ingestion-valid.small@1' FOR UPDATE`); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { _, err := routingStore.StepRouting(ctx, scope.Organization, op.ID); done <- err }()
				for {
					var waiting bool
					if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%owner_plugin_id,model,dimensions%')`).Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting {
						break
					}
					select {
					case err := <-done:
						t.Fatalf("metadata phase finished before blocked row: %v", err)
					default:
					}
				}
				writer, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer writer.Rollback(context.Background())
				var shared bool
				start := time.Now()
				if err = writer.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock_shared(642003)`).Scan(&shared); err != nil || !shared || time.Since(start) > 100*time.Millisecond {
					t.Fatalf("shared writer blocked by metadata: %v (%v)", shared, err)
				}
				if err = blocked.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if err = <-done; err != nil {
					t.Fatal(err)
				}
				stillActive, err := pluginsStore.ActivePlan(ctx)
				if err != nil || stillActive.ID != oldPlan.ID {
					t.Fatalf("busy cutover published metadata: %+v (%v)", stillActive, err)
				}
				if err = writer.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if moved, err := rebuildStore.ActivateRebuild(ctx, scope.Organization, emptyRebuild.ID); err != nil || !moved {
					t.Fatalf("concurrent rebuild activation: %v (%v)", moved, err)
				}
			}
			progress, err := routingStore.StepRouting(ctx, scope.Organization, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			if progress.Done {
				break
			}
		}
		got, err := (postgres.OperationStore{Pool: pool}).Operation(ctx, scope.Organization, op.ID)
		if err != nil || got.State != "succeeded" {
			t.Fatalf("routing result %+v: %v", got, err)
		}
		return op.ID
	}
	activated := run(routing.Command{Kind: routing.KindActivation, Target: b.Registration, Key: "activate"})
	emptyGeneration, err := store.Generation(ctx, scope.Organization, empty.ID)
	if err != nil || emptyGeneration.IngestionRouting == nil || emptyGeneration.IngestionRouting.For("text/plain") != b.Manifest.ID {
		t.Fatalf("newly routed generation missed activation: %+v (%v)", emptyGeneration.IngestionRouting, err)
	}
	if refreshes != 1 {
		t.Fatalf("successful activation must refresh registry before its outcome: %d", refreshes)
	}
	got, err := store.Hydrate(ctx, scope, []content.Candidate{{SegmentID: firstSeg.Segments[0].ID, GenerationID: g.ID}, {SegmentID: publication.segmentation.Segments[0].ID, GenerationID: g.ID}})
	if err != nil || len(got) != 1 || got[1].VersionID != firstV.ID {
		t.Fatalf("activated projection %+v: %v", got, err)
	}
	_, spaces, _, err := (postgres.SpaceStore{Pool: pool}).VectorSpaces(ctx, scope.Organization, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, sp := range spaces {
		if sp.OwnerPluginID == b.Manifest.ID && sp.GenerationRole == content.SpaceServed && (sp.ServingSegments == nil || *sp.ServingSegments == 0) {
			t.Fatalf("activated owner lost serving counts: %+v", sp)
		}
	}
	// New canonical Parts are still materialized under their original pin. The
	// new serving owner creates its independent projection and becomes current.
	fresh := accept("fresh", "delta epsilon zeta")
	freshCtx := pin(fresh)
	freshV, _, _ := materialize(freshCtx, fresh)
	serving, err := store.ClaimServingProjections(ctx, 10)
	if err != nil || len(serving) != 1 {
		t.Fatalf("new serving job %+v: %v", serving, err)
	}
	if err = evaluator.RunServing(ctx, scope.Organization, serving[0].ID); err != nil {
		t.Fatal(err)
	}
	outgoing := publication.segmentation
	pending := accept("successor", "eta theta iota", "fresh")
	pendingCtx := pin(pending)
	pendingV, _, _ := materialize(pendingCtx, pending)
	rolled := run(routing.Command{Kind: routing.KindRollback, Target: oldPlan.ID, Key: "rollback"})
	result, err := (postgres.OperationStore{Pool: pool}).Operation(ctx, scope.Organization, rolled)
	if err != nil || result.Counters["versions_missing"] != 1 {
		t.Fatalf("rollback gap result %+v: %v", result, err)
	}
	if result.Admin.PlanID == "" || result.Admin.PlanID == oldPlan.ID {
		t.Fatalf("rollback must record a new plan: %+v", result.Admin)
	}
	// A later command carries this unresolved gap forward without treating it
	// as missing coverage for an unchanged format. Historical epoch cleanup
	// must not discard the exact outgoing fallback.
	later := run(routing.Command{Kind: routing.KindActivation, Target: a.Registration, Key: "retain-gap"})
	laterResult, err := (postgres.OperationStore{Pool: pool}).Operation(ctx, scope.Organization, later)
	if err != nil || laterResult.Counters["versions_missing"] != 1 || laterResult.Counters["required_missing"] != 0 {
		t.Fatalf("retained gap %+v: %v", laterResult, err)
	}
	// The exact outgoing projection stays readable despite a new preferred owner.
	got, err = store.Hydrate(ctx, scope, []content.Candidate{{SegmentID: outgoing.Segments[0].ID, GenerationID: g.ID}})
	if err != nil || len(got) != 1 || got[0].VersionID != freshV.ID {
		t.Fatalf("rollback lost gap projection %+v: %v", got, err)
	}
	status, _, _, err := store.VersionStatus(ctx, scope.Organization, freshV.ID)
	if err != nil || !status.Searchable || !status.Current {
		t.Fatalf("rollback lost availability %+v: %v", status, err)
	}
	_, vectors, total, err := store.SubscriptionEmbeddings(ctx, scope.Organization, c.ID, freshV.ID)
	if err != nil || len(vectors) != 2 || total != 2 || vectors[0].SpaceID != "certified.ingestion-valid.small@1" {
		t.Fatalf("fallback subscription vectors %+v total=%d: %v", vectors, total, err)
	}
	returning, err := store.ClaimServingProjections(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	var currentJob, pendingJob *content.IngestionEvaluation
	for i := range returning {
		job := &returning[i]
		if job.PluginID != a.Manifest.ID {
			continue
		}
		if job.VersionID == freshV.ID {
			currentJob = job
		}
		if job.VersionID == pendingV.ID {
			pendingJob = job
		}
	}
	if currentJob == nil || pendingJob == nil {
		t.Fatalf("rollback did not admit current and desired recovery: %+v", returning)
	}
	evaluator.Plugin = &processing.PluginDeriver{Content: contents, Plugin: pluginhttp.Ingestor{Pin: a}}
	if err = evaluator.RunServing(ctx, scope.Organization, currentJob.ID); err != nil {
		t.Fatal(err)
	}
	recovered := publication.segmentation
	got, err = store.Hydrate(ctx, scope, []content.Candidate{{SegmentID: outgoing.Segments[0].ID, GenerationID: g.ID}, {SegmentID: recovered.Segments[0].ID, GenerationID: g.ID}})
	if err != nil || len(got) != 1 || got[1].VersionID != freshV.ID {
		t.Fatalf("recovery projection %+v: %v", got, err)
	}
	if err = evaluator.RunServing(ctx, scope.Organization, pendingJob.ID); err != nil {
		t.Fatal(err)
	}
	status, _, _, err = store.VersionStatus(ctx, scope.Organization, pendingV.ID)
	if err != nil || !status.Current || !status.Searchable {
		t.Fatalf("successor recovery %+v: %v", status, err)
	}
	replay, err := routingStore.AcceptRouting(ctx, scope.Organization, routing.Command{Kind: routing.KindActivation, Target: b.Registration, Key: "activate"})
	if err != nil || replay.ID != activated || replay.State != "succeeded" {
		t.Fatalf("activation replay %+v: %v", replay, err)
	}
	activeBeforeNoop, err := pluginsStore.ActivePlan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	run(routing.Command{Kind: routing.KindRollback, Target: activeBeforeNoop.ID, Key: "noop-stop", PinnedWork: registry.PinnedWorkStop})
	stopped, err := pluginsStore.WorkStopped(ctx, plugins.WorkIngestion, scope.Organization, pending.ID)
	if err != nil || stopped {
		t.Fatalf("no-op rollback stopped active work: %v (%v)", stopped, err)
	}
	// A rollback restores exact registrations, including retained evaluation
	// members. Discovery refusal must finish without publishing their plan.
	unreachable := routingStore
	unreachable.Registry.Reach = func(_ context.Context, r registry.Registration) error {
		if r.ID == b.Registration {
			return errors.New("returning registration unavailable")
		}
		return nil
	}
	activatedPlan, err := (postgres.OperationStore{Pool: pool}).Operation(ctx, scope.Organization, activated)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := unreachable.AcceptRouting(ctx, scope.Organization, routing.Command{Kind: routing.KindRollback, Target: activatedPlan.Admin.PlanID, Key: "unreachable-returning"})
	if err != nil {
		t.Fatal(err)
	}
	for {
		progress, err := unreachable.StepRouting(ctx, scope.Organization, rejected.ID)
		if err != nil {
			t.Fatal(err)
		}
		if progress.Done {
			break
		}
	}
	rejectedResult, err := (postgres.OperationStore{Pool: pool}).Operation(ctx, scope.Organization, rejected.ID)
	if err != nil || rejectedResult.State != "failed" || len(rejectedResult.Errors) != 1 || rejectedResult.Errors[0].Code != "plugin_unreachable" {
		t.Fatalf("unreachable rollback published: %+v (%v)", rejectedResult, err)
	}
	stillActive, err := pluginsStore.ActivePlan(ctx)
	if err != nil || stillActive.ID != activeBeforeNoop.ID {
		t.Fatalf("unreachable rollback changed active plan: %+v (%v)", stillActive, err)
	}
	// An immutable registry conflict discovered at cutover is a terminal
	// refusal. Earlier metadata writes in that transaction must roll back.
	refusedStore := routingStore
	refusedStore.Registry.Spaces = func(set *plugins.PinSet) []content.RegisteredSpace {
		spaces := registryService.Spaces(set)
		spaces[len(spaces)-1].Model += "/incompatible"
		return spaces
	}
	beforePlan, err := pluginsStore.ActivePlan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	activatedResult, err := (postgres.OperationStore{Pool: pool}).Operation(ctx, scope.Organization, activated)
	if err != nil {
		t.Fatal(err)
	}
	refused, err := refusedStore.AcceptRouting(ctx, scope.Organization, routing.Command{Kind: routing.KindRollback, Target: activatedResult.Admin.PlanID, Key: "immutable-space-conflict"})
	if err != nil {
		t.Fatal(err)
	}
	for {
		progress, err := refusedStore.StepRouting(ctx, scope.Organization, refused.ID)
		if err != nil {
			t.Fatalf("immutable space conflict must terminate the Operation: %v", err)
		}
		if progress.Done {
			break
		}
	}
	refusedResult, err := (postgres.OperationStore{Pool: pool}).Operation(ctx, scope.Organization, refused.ID)
	if err != nil || refusedResult.State != "failed" || len(refusedResult.Errors) != 1 || refusedResult.Errors[0].Code != "plugin_conflict" {
		t.Fatalf("immutable conflict result %+v: %v", refusedResult, err)
	}
	afterPlan, err := pluginsStore.ActivePlan(ctx)
	if err != nil || afterPlan.ID != beforePlan.ID {
		t.Fatalf("refused cutover changed plan %+v: %v", afterPlan, err)
	}
	status, _, _, err = store.VersionStatus(ctx, scope.Organization, pendingV.ID)
	if err != nil || !status.Searchable || !status.Current {
		t.Fatalf("refused cutover changed availability %+v: %v", status, err)
	}

	// A configuration reload can change the installation's default without
	// rebuilding historical corpus routes. Activating that same owner must
	// preserve the older route, even while new imports keep changing records.
	if err = f.pins.ConfigureIngestion(plugins.IngestionRouting{Default: b.Manifest.ID, Evaluation: map[string][]string{"text/plain": {a.Manifest.ID}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = pluginsStore.ApplyConfiguration(ctx, registry.FromPins(f.pins)); err != nil {
		t.Fatal(err)
	}
	replacementPins, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: f.manifest, Endpoint: startIngestionFixture(t, ctx, f.manifest, "ingestion-split"), Spaces: map[string]string{"certified.ingestion-valid.small": "served", "certified.ingestion-valid.large": "evaluation"}}})
	if err != nil {
		t.Fatal(err)
	}
	candidate := registry.FromPins(replacementPins).Registrations[0]
	candidate.State = registry.StateRegistered
	if _, _, err = pluginsStore.RegisterPlugin(ctx, candidate, "same-owner-replacement"); err != nil {
		t.Fatal(err)
	}
	if err = pluginsStore.RecordCheck(ctx, candidate.ID, registry.CheckReport{Certified: true}); err != nil {
		t.Fatal(err)
	}
	unchanged, err := routingStore.AcceptRouting(ctx, scope.Organization, routing.Command{Kind: routing.KindActivation, Target: candidate.ID, Key: "historical-route"})
	if err != nil {
		t.Fatal(err)
	}
	finished := false
	for step := 0; step < 200; step++ {
		var phase string
		if err = pool.QueryRow(ctx, `SELECT phase FROM routing_operations WHERE id=$1`, unchanged.ID).Scan(&phase); err != nil {
			t.Fatal(err)
		}
		if phase == "cutover" {
			accept(fmt.Sprintf("concurrent-metadata-import-%d", step), "Content arriving during metadata activation")
		}
		progress, err := routingStore.StepRouting(ctx, scope.Organization, unchanged.ID)
		if err != nil {
			t.Fatal(err)
		}
		if progress.Done {
			finished = true
			break
		}
	}
	unchangedResult, err := (postgres.OperationStore{Pool: pool}).Operation(ctx, scope.Organization, unchanged.ID)
	if err != nil || !finished || unchangedResult.State != "succeeded" {
		t.Fatalf("unchanged owner activation under import %+v (finished=%v): %v", unchangedResult, finished, err)
	}
	historical, err := store.Generation(ctx, scope.Organization, c.ID)
	if err != nil || historical.IngestionRouting == nil || historical.IngestionRouting.For("text/plain") != a.Manifest.ID || historical.SpaceID != g.SpaceID {
		t.Fatalf("metadata activation rewrote historical route: %+v (%v)", historical, err)
	}
}
