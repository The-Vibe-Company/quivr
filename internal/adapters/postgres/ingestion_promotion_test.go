package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/app"
	"github.com/The-Vibe-Company/quivr/internal/backfill"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr/internal/processing"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/jackc/pgx/v5/pgxpool"
)

type rebuildPublication struct{ evaluationPublication }

func (*rebuildPublication) Search(context.Context, []retrieval.Route, corpus.Scope, retrieval.Request) ([]content.Candidate, error) {
	return nil, errors.New("unused")
}

// This storage lifecycle owns route cutover, concurrent old-plan publication
// and rollback. The existing evaluation owner owns optional call isolation;
// here its shared contract fixture supplies genuinely different owner cuts.
func TestEvaluationOwnerActivationKeepsSearchableVersions(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	manifest := "../../../tests/plugin-contract/ingestion-valid/quivr-plugin.yaml"
	aEndpoint := startIngestionFixture(t, ctx, hashEmbedder, "ingestion-valid")
	bEndpoint := startIngestionFixture(t, ctx, manifest, "ingestion-split")
	pins, err := plugins.LoadPins([]plugins.PinConfig{
		{Manifest: hashEmbedder, Endpoint: aEndpoint, Spaces: hashSpaces},
		{Manifest: manifest, Endpoint: bEndpoint, Spaces: map[string]string{"certified.ingestion-valid.small": "served", "certified.ingestion-valid.large": "evaluation"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = pins.ConfigureIngestion(plugins.IngestionRouting{Default: "example.hash_embedder", Evaluation: map[string][]string{"text/plain": {"certified.ingestion-valid"}}}); err != nil {
		t.Fatal(err)
	}
	if err = app.BootstrapDatabase(ctx, pool, app.DeploymentSpaces(pins)); err != nil {
		t.Fatal(err)
	}
	pluginsStore := postgres.PluginStore{Pool: pool}
	seeded, err := pluginsStore.ApplyConfiguration(ctx, registry.FromPins(pins))
	if err != nil {
		t.Fatal(err)
	}
	oldPlan, members, err := pluginsStore.PlanMembers(ctx, seeded.Plan)
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := registry.Resolve(oldPlan.Roles, members)
	if err != nil {
		t.Fatal(err)
	}
	live, err := plugins.NewLive(oldPlan.ID, original)
	if err != nil {
		t.Fatal(err)
	}
	live.Resolve = func(ctx context.Context, id string) (*plugins.PinSet, error) {
		p, m, err := pluginsStore.PlanMembers(ctx, id)
		if err != nil {
			return nil, err
		}
		set, _, err := registry.Resolve(p.Roles, m)
		return set, err
	}
	store := contentStores(pool)
	contents := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Baseline: store, Embeddings: store, Blobs: &objectMemory{objects: map[string][]byte{}}}
	scope := corpus.Scope{Organization: "promotion", Actions: []string{"corpora:write", "content:write", "content:read", "search:query"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "one", Name: "Promotion"})
	if err != nil {
		t.Fatal(err)
	}
	operator := corpus.Scope{Actions: []string{registry.Action}, Corpora: []string{"*"}}
	registryService := registry.Service{Store: pluginsStore, Spaces: app.DeploymentSpaces}
	a := original.IngestionFor("text/plain")
	b := original.EvaluationFor("text/plain")[0]
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
		artifact, err := contents.SaveEmbedding(ctx, content.EmbeddingInput(scope.Organization, c.ID, v, seg, seg.Segments[0], space, "fixture"), space, vector)
		if err != nil {
			t.Fatal(err)
		}
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
	// A previously promoted model must survive the owner becoming evaluation-only.
	saveLarge := func(v content.Version, seg content.Segmentation) content.Embedding {
		t.Helper()
		large, ok := (pluginhttp.Ingestor{Pin: a}).Descriptor().VectorSpace("example.hash_embedder.large@1")
		if !ok {
			t.Fatal("retained model missing")
		}
		vector := make([]float32, large.Dimensions)
		vector[0] = 1
		artifact, err := contents.SaveEmbedding(ctx, content.EmbeddingInput(scope.Organization, c.ID, v, seg, seg.Segments[0], large, "fixture"), large, vector)
		if err != nil {
			t.Fatal(err)
		}
		return artifact
	}
	g, err = store.PrepareEvaluation(ctx, scope.Organization, c.ID, []string{"example.hash_embedder.large@1"})
	if err != nil {
		t.Fatal(err)
	}
	firstLarge := saveLarge(firstV, firstSeg)
	if err = store.CoverEvaluation(ctx, scope.Organization, g, firstSeg, []content.Embedding{firstLarge}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.PromoteSpace(ctx, "example.hash_embedder.large@1", false); err != nil {
		t.Fatal(err)
	}
	assertCoverage := func(err error, versions, generations int64) {
		t.Helper()
		var coverage *registry.CoverageError
		if !errors.As(err, &coverage) || len(coverage.Gaps) != 1 || coverage.Gaps[0] != (registry.CoverageGap{Owner: b.Manifest.ID, Space: "certified.ingestion-valid.small@1", MissingVersions: versions, MissingGenerations: generations}) {
			t.Fatalf("coverage refusal: %v, want owner %s primary space with %d missing documents and %d missing generations", err, b.Manifest.ID, versions, generations)
		}
	}
	if _, err = registryService.Activate(ctx, operator, b.Registration); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("activation without target owner's projection: %v", err)
	}
	assertCoverage(err, 1, 0)
	if id, _ := pluginsStore.ActivePlanID(ctx); id != oldPlan.ID {
		t.Fatalf("incomplete activation replaced %s with %s", oldPlan.ID, id)
	}
	jobs, err := store.ClaimIngestionEvaluations(ctx, 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("evaluation preparation %+v %v", jobs, err)
	}
	publication := &evaluationPublication{}
	evaluator := processing.Evaluator{Store: store, Serving: store, Content: contents, Plugin: &processing.PluginDeriver{Content: contents, Plugin: pluginhttp.Ingestor{Pin: b}}, Projection: publication}
	if err = evaluator.Run(ctx, scope.Organization, jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	if len(publication.segmentation.Segments) != 2 {
		t.Fatalf("target fixture did not supply independent cuts: %+v", publication.segmentation)
	}
	// The target row alone is insufficient: every target cut needs its vector.
	if _, err = pool.Exec(ctx, `DELETE FROM embedding_coverage WHERE organization=$1 AND segment_id=$2 AND space_id=$3`, scope.Organization, publication.segmentation.Segments[1].ID, "certified.ingestion-valid.small@1"); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE compact_embedding_coverage c SET covered=set_bit(c.covered,e.ordinal,0)
 FROM compact_embeddings e JOIN storage_organizations o ON o.id=e.organization_id
 JOIN storage_segments k ON (k.organization_id,k.id)=(e.organization_id,e.segment_id)
 JOIN storage_spaces sp ON sp.id=e.space_id
 WHERE c.organization_id=e.organization_id AND c.file_id=e.file_id AND o.organization=$1 AND k.segment_id=$2 AND sp.space_id=$3`, scope.Organization, publication.segmentation.Segments[1].ID, "certified.ingestion-valid.small@1"); err != nil {
		t.Fatal(err)
	}
	if _, err = registryService.Activate(ctx, operator, b.Registration); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("activation with a target vector gap: %v", err)
	}
	assertCoverage(err, 1, 0)
	g, err = store.Generation(ctx, scope.Organization, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CoverEvaluation(ctx, scope.Organization, g, publication.segmentation, publication.artifacts); err != nil {
		t.Fatal(err)
	}
	// Coverage in an external space cannot serve a generation that does not carry it.
	var carried []byte
	if err = pool.QueryRow(ctx, `SELECT spaces FROM projection_generations WHERE id=$1`, g.ID).Scan(&carried); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE projection_generations SET spaces=(SELECT jsonb_agg(sp) FROM jsonb_array_elements(spaces) sp WHERE sp->>'id'<>'certified.ingestion-valid.small@1') WHERE id=$1`, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = registryService.Activate(ctx, operator, b.Registration); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("activation without carried target space: %v", err)
	}
	assertCoverage(err, 0, 1)
	if _, err = pool.Exec(ctx, `UPDATE projection_generations SET spaces=$2 WHERE id=$1`, g.ID, carried); err != nil {
		t.Fatal(err)
	}
	// Even an empty Corpus must carry the incoming primary, otherwise its
	// first new receipt would retry forever after a successful activation.
	empty, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "empty", Name: "Empty"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO projection_generations(id,collection,profile_version,space_id,source_namespace_projected,spaces,spaces_projected,ingestion_routing) SELECT 'empty-generation',collection,profile_version,space_id,source_namespace_projected,(SELECT jsonb_agg(sp) FROM jsonb_array_elements(spaces) sp WHERE sp->>'id'<>'certified.ingestion-valid.small@1'),spaces_projected,ingestion_routing FROM projection_generations WHERE id=$1`, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO corpus_projection_routes(organization,corpus_id,generation_id) VALUES($1,$2,'empty-generation')`, scope.Organization, empty.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = registryService.Activate(ctx, operator, b.Registration); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("activation without empty Corpus primary: %v", err)
	}
	assertCoverage(err, 0, 1)
	if _, err = store.PrepareEvaluation(ctx, scope.Organization, empty.ID, []string{"certified.ingestion-valid.small@1"}); err != nil {
		t.Fatal(err)
	}
	// An operation already pinned to A cannot revert a later owner cutover.
	older, err := store.AcceptRebuild(ctx, scope.Organization, c.ID, "older-owner", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = pluginsStore.PinWork(ctx, plugins.WorkOperation, scope.Organization, older.ID, oldPlan.ID); err != nil {
		t.Fatal(err)
	}
	olderCtx, err := live.Pin(ctx, plugins.Work{Kind: plugins.WorkOperation, Organization: scope.Organization, ID: older.ID, Plan: oldPlan.ID}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginRebuild(olderCtx, scope.Organization, older.ID); err != nil {
		t.Fatal(err)
	}
	if covered, err := store.CoverRebuild(olderCtx, scope.Organization, older.ID, firstSeg, []content.Embedding{firstArtifact, firstLarge}); err != nil || !covered {
		t.Fatalf("older owner coverage: %v %v", covered, err)
	}
	// One old pin has canonical Parts before activation. The other is still
	// unmaterialized, so both queuing boundaries must preserve the pin.
	pending := accept("pending", "delta epsilon zeta", "first")
	pendingCtx := pin(pending)
	pendingV, pendingSeg, pendingArtifact := materialize(pendingCtx, pending)
	late := accept("late", "eta theta iota")
	lateCtx := pin(late)
	promoted, err := registryService.Activate(ctx, operator, b.Registration)
	if err != nil {
		t.Fatal(err)
	}
	if activated, err := store.ActivateRebuild(olderCtx, scope.Organization, older.ID); !errors.Is(err, operations.ErrNotRunning) || activated {
		t.Fatalf("older pinned rebuild reverted owner cutover: %v %v", activated, err)
	}
	if roles(promoted)["ingestion-route:text/plain"] != "certified.ingestion-valid@0.1.0" || roles(promoted)["ingestion-evaluation:text/plain:example.hash_embedder"] != "example.hash_embedder@0.1.0" {
		t.Fatalf("promoted source roles %v", roles(promoted))
	}
	if replay, err := registryService.Activate(ctx, operator, b.Registration); err != nil || replay.ID != promoted.ID {
		t.Fatalf("activation replay %+v %v", replay, err)
	}
	// Simulate a later registry model role change before this old receipt
	// materializes: its new serving job must still name the immutable primary.
	if _, err = pool.Exec(ctx, `UPDATE vector_spaces SET role=CASE WHEN id='certified.ingestion-valid.small@1' THEN 'evaluation' ELSE 'served' END WHERE owner_plugin_id=$1`, b.Manifest.ID); err != nil {
		t.Fatal(err)
	}
	lateV, lateSeg, lateArtifact := materialize(lateCtx, late)
	if _, err = pool.Exec(ctx, `UPDATE vector_spaces SET role=CASE WHEN id='certified.ingestion-valid.small@1' THEN 'served' ELSE 'evaluation' END WHERE owner_plugin_id=$1`, b.Manifest.ID); err != nil {
		t.Fatal(err)
	}
	g, err = store.Generation(ctx, scope.Organization, c.ID)
	if err != nil || g.IngestionRouting.For("text/plain") != b.Manifest.ID {
		t.Fatalf("new serving route %+v %v", g, err)
	}
	for _, old := range []struct {
		ctx context.Context
		v   content.Version
		seg content.Segmentation
	}{{pendingCtx, pendingV, pendingSeg}, {lateCtx, lateV, lateSeg}} {
		if err = store.Promote(old.ctx, scope.Organization, old.seg, g); err != nil {
			t.Fatal(err)
		}
		if err = store.QuarantineVersion(old.ctx, scope.Organization, old.v.ID, content.Diagnostic{Code: "ingestion_refused", Message: "old owner refused"}); err != nil {
			t.Fatal(err)
		}
		status, _, _, err := store.VersionStatus(ctx, scope.Organization, old.v.ID)
		if err != nil || status.Searchable || status.Current || status.State == "quarantined" {
			t.Fatalf("late old owner advanced or blocked Version: %+v %v", status, err)
		}
	}
	// The already-current document remains readable through the target owner.
	normal, err := store.Hydrate(ctx, scope, []content.Candidate{{SegmentID: firstSeg.Segments[0].ID, GenerationID: g.ID}, {SegmentID: publication.segmentation.Segments[0].ID, GenerationID: g.ID}})
	if err != nil || len(normal) != 1 || normal[1].VersionID != firstV.ID {
		t.Fatalf("cutover lost current document or served old cuts: %+v %v", normal, err)
	}
	serving, err := store.ClaimServingProjections(ctx, 10)
	if err != nil || len(serving) != 2 {
		t.Fatalf("both old admission boundaries must queue target serving work: %+v %v", serving, err)
	}
	for _, job := range serving {
		if job.PlanID != promoted.ID || job.RegistrationID != b.Registration || len(job.Spaces) != 1 || job.Spaces[0] != "certified.ingestion-valid.small@1" {
			t.Fatalf("target work pin %+v", job)
		}
		if err = evaluator.RunServing(ctx, scope.Organization, job.ID); err != nil {
			t.Fatal(err)
		}
		status, _, _, err := store.VersionStatus(ctx, scope.Organization, job.VersionID)
		if err != nil || !status.Searchable || !status.Current {
			t.Fatalf("target projection did not publish Version: %+v %v", status, err)
		}
	}
	// Late A vectors become evaluation coverage and cannot undo B readiness.
	for _, old := range []struct {
		ctx      context.Context
		v        content.Version
		seg      content.Segmentation
		artifact content.Embedding
	}{{pendingCtx, pendingV, pendingSeg, pendingArtifact}, {lateCtx, lateV, lateSeg, lateArtifact}} {
		large := saveLarge(old.v, old.seg)
		if err = store.CommitEnrichment(old.ctx, scope.Organization, old.seg, g, []content.Embedding{old.artifact, large}); err != nil {
			t.Fatal(err)
		}
	}
	// An incompatible configuration replacement restores startup plan roles,
	// while existing Corpora retain their serving owner until rebuilt.
	newEndpoint := startIngestionFixture(t, ctx, manifest, "ingestion-split")
	configuredPins, err := plugins.LoadPins([]plugins.PinConfig{
		{Manifest: hashEmbedder, Endpoint: aEndpoint, Spaces: hashSpaces},
		{Manifest: manifest, Endpoint: newEndpoint, Spaces: map[string]string{"certified.ingestion-valid.small": "served", "certified.ingestion-valid.large": "evaluation"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = configuredPins.ConfigureIngestion(plugins.IngestionRouting{Default: a.Manifest.ID, Evaluation: map[string][]string{"text/plain": {b.Manifest.ID}}}); err != nil {
		t.Fatal(err)
	}
	reset, err := pluginsStore.ApplyConfiguration(ctx, registry.FromPins(configuredPins))
	if err != nil {
		t.Fatal(err)
	}
	resetPlan, resetMembers, err := pluginsStore.PlanMembers(ctx, reset.Plan)
	if err != nil || roles(resetPlan)["ingestion-default"] != "example.hash_embedder@0.1.0" || roles(resetPlan)["ingestion-route:text/plain"] != "" {
		t.Fatalf("expected startup plan roles after replacement: %+v %v", resetPlan, err)
	}
	fresh := accept("after-promotion", "new harbour document")
	freshCtx, err := live.Pin(ctx, plugins.Work{Kind: plugins.WorkIngestion, Organization: scope.Organization, ID: fresh.ID, Plan: reset.Plan}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = contents.Materialize(freshCtx, scope.Organization, fresh.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err = store.Receipt(ctx, scope.Organization, fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	freshV, err := contents.Version(ctx, scope, fresh.RecordID, fresh.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	freshServing, err := store.ClaimServingProjections(ctx, 10)
	if err != nil || len(freshServing) != 1 || freshServing[0].PluginID != b.Manifest.ID {
		t.Fatalf("retained Corpus serving work: %+v %v", freshServing, err)
	}
	resetSet, _, err := registry.Resolve(resetPlan.Roles, resetMembers)
	if err != nil {
		t.Fatal(err)
	}
	newB := resetSet.EvaluationFor("text/plain")[0]
	freshEvaluator := evaluator
	freshEvaluator.Plugin = &processing.PluginDeriver{Content: contents, Plugin: pluginhttp.Ingestor{Pin: newB}}
	if err = freshEvaluator.RunServing(ctx, scope.Organization, freshServing[0].ID); err != nil {
		t.Fatal(err)
	}
	// Restore the promoted plan before testing the returning-owner switch.
	promoted, err = registryService.Activate(ctx, operator, newB.Registration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registryService.Rollback(ctx, operator, registry.RollbackRequest{Key: "incomplete-return", Plan: oldPlan.ID, PinnedWork: registry.PinnedWorkDrain}); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("return before optional coverage: %v, want plugin_conflict", err)
	}
	var returningGap *registry.CoverageError
	if !errors.As(err, &returningGap) || len(returningGap.Gaps) != 1 || returningGap.Gaps[0] != (registry.CoverageGap{Owner: a.Manifest.ID, Space: "example.hash_embedder.large@1", MissingVersions: 1}) {
		t.Fatalf("returning-owner refusal must identify the one new document: %v", err)
	}
	optional, err := store.ClaimIngestionEvaluations(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	freshPublication := &evaluationPublication{}
	oldOwnerEvaluator := evaluator
	oldOwnerEvaluator.Projection = freshPublication
	oldOwnerEvaluator.Plugin = &processing.PluginDeriver{Content: contents, Plugin: pluginhttp.Ingestor{Pin: a}}
	for _, job := range optional {
		if job.VersionID == freshV.ID && job.PluginID == a.Manifest.ID {
			if err = oldOwnerEvaluator.Run(ctx, scope.Organization, job.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	stoppedReceipt := accept("stop-work", "stop this pending call")
	if _, _, err = pluginsStore.PinWork(ctx, plugins.WorkIngestion, scope.Organization, stoppedReceipt.ID, promoted.ID); err != nil {
		t.Fatal(err)
	}
	stopCtx, err := live.Pin(ctx, plugins.Work{Kind: plugins.WorkIngestion, Organization: scope.Organization, ID: stoppedReceipt.ID, Plan: promoted.ID, StopMarked: func(ctx context.Context) bool {
		stopped, err := pluginsStore.WorkStopped(ctx, plugins.WorkIngestion, scope.Organization, stoppedReceipt.ID)
		if err != nil {
			t.Fatal(err)
		}
		return stopped
	}}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	back, err := registryService.Rollback(ctx, operator, registry.RollbackRequest{Key: "restore-owner", Plan: oldPlan.ID, PinnedWork: registry.PinnedWorkStop})
	if err != nil {
		t.Fatal(err)
	}
	if !plugins.Stopped(stopCtx, b) {
		t.Fatal("rollback stop must stop the outgoing source owner although it remains an evaluation member")
	}
	reason, err := plugins.Unreachable(stopCtx, b, "ingestion")
	if err != nil || reason == nil || reason.Code != plugins.CodePinnedPlanStopped {
		t.Fatalf("retained outgoing owner stop diagnostic %+v %v", reason, err)
	}
	if roles(back)["ingestion-route:text/plain"] != "" || roles(back)["ingestion-default"] != "example.hash_embedder@0.1.0" {
		t.Fatalf("rollback source roles %v", roles(back))
	}
	restored, err := store.Generation(ctx, scope.Organization, c.ID)
	if err != nil || restored.IngestionRouting.For("text/plain") != a.Manifest.ID || restored.ServedFor(a.Manifest.ID) != "example.hash_embedder.large@1" {
		t.Fatalf("rollback route %+v %v", restored, err)
	}
	got, err := store.Hydrate(ctx, scope, []content.Candidate{{SegmentID: pendingSeg.Segments[0].ID, GenerationID: g.ID}, {SegmentID: lateSeg.Segments[0].ID, GenerationID: g.ID}})
	if err != nil || len(got) != 2 {
		t.Fatalf("rollback lost retained old-owner projections: %+v %v", got, err)
	}
	// Materialization admitted on B before rollback needs A's effective
	// promoted model, while its original receipt pin remains B.
	if err = contents.Materialize(stopCtx, scope.Organization, stoppedReceipt.ID); err != nil {
		t.Fatal(err)
	}
	returning, err := store.ClaimServingProjections(ctx, 10)
	if err != nil || len(returning) != 1 || len(returning[0].Spaces) != 1 || returning[0].Spaces[0] != "example.hash_embedder.large@1" {
		t.Fatalf("returning model pin %+v %v", returning, err)
	}
	returningEvaluator := evaluator
	returningEvaluator.Plugin = &processing.PluginDeriver{Content: contents, Plugin: pluginhttp.Ingestor{Pin: a}}
	// Pending jobs follow later model selection with new immutable identities,
	// including a return to the same space in the same generation and plan.
	if _, err = store.PromoteSpace(ctx, "example.hash_embedder.small@1", false); err != nil {
		t.Fatal(err)
	}
	if err = returningEvaluator.RunServing(ctx, scope.Organization, returning[0].ID); err != nil {
		t.Fatal(err)
	}
	smaller, err := store.ClaimServingProjections(ctx, 10)
	if err != nil || len(smaller) != 1 || smaller[0].Spaces[0] != "example.hash_embedder.small@1" {
		t.Fatalf("changed model job %+v %v", smaller, err)
	}
	if _, err = store.PromoteSpace(ctx, "example.hash_embedder.large@1", false); err != nil {
		t.Fatal(err)
	}
	// A deadline or refusal from the obsolete model cannot consume the new
	// primary's budget or quarantine the desired Version.
	if attempts, err := store.CountServingProjectionTimeout(ctx, smaller[0]); err != nil || attempts != 0 {
		t.Fatalf("obsolete model counted a deadline: %d %v", attempts, err)
	}
	if err = store.CompleteServingProjection(ctx, smaller[0], "failed", &content.Diagnostic{Code: "ingestion_refused", Message: "old model refused"}); err != nil {
		t.Fatal(err)
	}
	if obsolete, err := store.ServingProjection(ctx, scope.Organization, smaller[0].ID); err != nil || obsolete.State != "skipped" {
		t.Fatalf("obsolete refusal did not skip: %+v %v", obsolete, err)
	}
	returning, err = store.ClaimServingProjections(ctx, 10)
	if err != nil || len(returning) != 1 || returning[0].Spaces[0] != "example.hash_embedder.large@1" {
		t.Fatalf("returning model job %+v %v", returning, err)
	}
	// A rebuild may change the generation while a stopped original receipt
	// depends on its separately pinned serving job. The successor must retain
	// the prepared generation's source routing.
	op, err := store.AcceptRebuild(ctx, scope.Organization, c.ID, "pending-rebuild", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = pluginsStore.PinWork(ctx, plugins.WorkOperation, scope.Organization, op.ID, back.ID); err != nil {
		t.Fatal(err)
	}
	rebuildCtx, err := live.Pin(ctx, plugins.Work{Kind: plugins.WorkOperation, Organization: scope.Organization, ID: op.ID, Plan: back.ID}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.BeginRebuild(rebuildCtx, scope.Organization, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := store.RebuildCandidates(rebuildCtx, scope.Organization, op.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	retained := map[string]struct {
		seg       content.Segmentation
		artifacts []content.Embedding
	}{
		freshV.ID:   {freshPublication.segmentation, freshPublication.artifacts},
		firstV.ID:   {firstSeg, []content.Embedding{firstArtifact, firstLarge}},
		pendingV.ID: {pendingSeg, []content.Embedding{pendingArtifact, saveLarge(pendingV, pendingSeg)}},
		lateV.ID:    {lateSeg, []content.Embedding{lateArtifact, saveLarge(lateV, lateSeg)}},
	}
	for _, candidate := range candidates {
		projection, ok := retained[candidate.VersionID]
		if !ok {
			t.Fatalf("unexpected rebuild candidate: %+v", candidate)
		}
		if covered, err := store.CoverRebuild(rebuildCtx, scope.Organization, op.ID, projection.seg, projection.artifacts); err != nil || !covered {
			t.Fatalf("rebuild coverage: %v %v", covered, err)
		}
	}
	if activated, err := store.ActivateRebuild(rebuildCtx, scope.Organization, op.ID); err != nil || !activated {
		t.Fatalf("rebuild activation: %v %v", activated, err)
	}
	if err = returningEvaluator.RunServing(ctx, scope.Organization, returning[0].ID); err != nil {
		t.Fatal(err)
	}
	returning, err = store.ClaimServingProjections(ctx, 10)
	if err != nil || len(returning) != 1 || returning[0].GenerationID != op.TargetGenerationID {
		t.Fatalf("rebuilt generation lost pending successor: %+v %v", returning, err)
	}
	// Pause readiness publication after its primary preflight, then attempt
	// a model promotion. Real database locks provide the ordering; there are
	// no timing sleeps or production hooks. Promotion must recheck coverage
	// after the new current Version has committed.
	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_lock(918273)`); err != nil {
		t.Fatal(err)
	}
	defer blocker.Exec(context.Background(), `SELECT pg_advisory_unlock(918273)`)
	if _, err = pool.Exec(ctx, `CREATE FUNCTION pause_ready() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(918273); RETURN NEW; END $$;
 CREATE TRIGGER pause_ready BEFORE UPDATE ON record_versions FOR EACH ROW WHEN (NOT OLD.baseline_ready AND NEW.baseline_ready) EXECUTE FUNCTION pause_ready()`); err != nil {
		t.Fatal(err)
	}
	published := make(chan error, 1)
	go func() { published <- returningEvaluator.RunServing(ctx, scope.Organization, returning[0].ID) }()
	for {
		var paused bool
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=918273 AND NOT granted)`).Scan(&paused); err != nil {
			t.Fatal(err)
		}
		if paused {
			break
		}
		select {
		case err := <-published:
			t.Fatalf("publication ended before readiness barrier: %v", err)
		default:
		}
	}
	promotionConfig := pool.Config().Copy()
	promotionConfig.ConnConfig.RuntimeParams["application_name"] = "promotion-race"
	promotionPool, err := pgxpool.NewWithConfig(ctx, promotionConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer promotionPool.Close()
	promotedModel := make(chan error, 1)
	go func() {
		_, err := (postgres.BackfillStore{Pool: promotionPool}).PromoteSpace(ctx, "example.hash_embedder.small@1", false)
		promotedModel <- err
	}()
	var promotionErr error
	var finished bool
	for !finished {
		select {
		case promotionErr = <-promotedModel:
			finished = true
		default:
		}
		if finished {
			break
		}
		var waiting bool
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name='promotion-race' AND wait_event_type='Lock')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_unlock(918273)`); err != nil {
		t.Fatal(err)
	}
	if err = <-published; err != nil {
		t.Fatal(err)
	}
	if !finished {
		promotionErr = <-promotedModel
	}
	var incomplete *backfill.IncompleteError
	if !errors.As(promotionErr, &incomplete) {
		t.Fatalf("promotion bypassed the newly current Version's coverage: %v", promotionErr)
	}
	status, _, _, err := store.VersionStatus(ctx, scope.Organization, returning[0].VersionID)
	if err != nil || !status.Searchable || !status.Current {
		t.Fatalf("returning promoted model publication %+v %v", status, err)
	}
	if replay, err := registryService.Rollback(ctx, operator, registry.RollbackRequest{Key: "restore-owner", Plan: oldPlan.ID, PinnedWork: registry.PinnedWorkStop}); err != nil || replay.ID != back.ID {
		t.Fatalf("rollback replay %+v %v", replay, err)
	}
	// Configuration replacement keeps this Corpus on A until a new rebuild
	// prepared and pinned to the authoritative B plan is ready to replace it.
	if err = pins.ConfigureIngestion(plugins.IngestionRouting{Default: b.Manifest.ID, Evaluation: map[string][]string{"text/plain": {a.Manifest.ID}}}); err != nil {
		t.Fatal(err)
	}
	configured, err := pluginsStore.ApplyConfiguration(ctx, registry.FromPins(pins))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RegisterSpaces(ctx, app.DeploymentSpaces(pins)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.AlignDefaultGeneration(ctx); err != nil {
		t.Fatal(err)
	}
	migration, err := store.AcceptRebuild(ctx, scope.Organization, c.ID, "configured-owner", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = pluginsStore.PinWork(ctx, plugins.WorkOperation, scope.Organization, migration.ID, configured.Plan); err != nil {
		t.Fatal(err)
	}
	migrationCtx, err := live.Pin(ctx, plugins.Work{Kind: plugins.WorkOperation, Organization: scope.Organization, ID: migration.ID, Plan: configured.Plan}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	migrationTarget, err := store.BeginRebuild(migrationCtx, scope.Organization, migration.ID)
	if err != nil {
		t.Fatal(err)
	}
	migrationCandidates, err := store.RebuildCandidates(migrationCtx, scope.Organization, migration.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	bDeriver := processing.PluginDeriver{Content: contents, Plugin: pluginhttp.Ingestor{Pin: b}}
	if migrationTarget.Generation.SpaceID != "certified.ingestion-valid.small@1" {
		t.Fatalf("configured-owner served space: %+v", migrationTarget.Generation)
	}
	if len(migrationCandidates) == 0 {
		t.Fatal("configured-owner rebuild has no candidates")
	}
	runner := retrieval.Rebuilder{Store: store, Cancellation: store, Content: contents, Projection: &rebuildPublication{}, Plugin: bDeriver, Routing: store}
	for round := 0; round < 10; round++ {
		done, stepErr := runner.Step(migrationCtx, scope.Organization, migration.ID)
		if stepErr != nil {
			t.Fatal(stepErr)
		}
		if done {
			break
		}
	}
	if outcome, err := store.Operation(ctx, scope.Organization, migration.ID); err != nil || outcome.State != operations.StateSucceeded || outcome.Counters["vectors_reused"] == 0 {
		t.Fatalf("configured-owner rebuild did not finish with vectors: %+v %v", outcome, err)
	}
	if current, err := store.Generation(ctx, scope.Organization, c.ID); err != nil || current.IngestionRouting.For("text/plain") != b.Manifest.ID {
		t.Fatalf("configured-owner route: %+v %v", current, err)
	}

}
