package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/app"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost/fakeplugin"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
)

// Publication is independently exercised by the projection adapter. This
// observer checks which independent segmentation reached that boundary.
type evaluationPublication struct {
	segmentation content.Segmentation
	vectors      int
	artifacts    []content.Embedding
}

func (p *evaluationPublication) Publish(_ context.Context, _ content.Generation, _, _, _ string, _ content.Version, seg content.Segmentation) error {
	p.segmentation = seg
	return nil
}
func (p *evaluationPublication) PublishEmbeddings(_ context.Context, _ content.Generation, _ string, data []content.EmbeddingData) error {
	p.vectors = len(data)
	p.artifacts = nil
	for _, d := range data {
		p.artifacts = append(p.artifacts, d.Artifact)
	}
	return nil
}

// This lifecycle owner uses the contract-validating fixture process and real
// PostgreSQL: optional work appears only after served commit, is pinned once,
// and either fills independent cuts or records its own failure without changing
// served readiness, coverage or event delivery.
func TestIngestionEvaluationRunsAfterServedCommit(t *testing.T) {
	for _, mode := range []string{"ingestion-split", "ingestion-offset"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			pool := scratchDatabase(t, ctx)
			manifest := "../../../tests/plugin-contract/ingestion-valid/quivr-plugin.yaml"
			endpoint := startIngestionFixture(t, ctx, manifest, mode)
			pins, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: hashEmbedder, Endpoint: "http://127.0.0.1:9960", Spaces: hashSpaces}, {Manifest: manifest, Spaces: map[string]string{"certified.ingestion-valid.small": "served", "certified.ingestion-valid.large": "evaluation"}, Endpoint: endpoint}})
			if err != nil {
				t.Fatal(err)
			}
			if err = pins.ConfigureIngestion(plugins.IngestionRouting{Default: "example.hash_embedder", Evaluation: map[string][]string{"text/plain": {"certified.ingestion-valid"}}}); err != nil {
				t.Fatal(err)
			}
			if err = app.BootstrapDatabase(ctx, pool, app.DeploymentSpaces(pins)); err != nil {
				t.Fatal(err)
			}
			plan, err := (postgres.PluginStore{Pool: pool}).ApplyConfiguration(ctx, registry.FromPins(pins))
			if err != nil {
				t.Fatal(err)
			}
			store := contentStores(pool)
			objects := &objectMemory{objects: map[string][]byte{}}
			contents := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Baseline: store, Embeddings: store, Blobs: objects}
			scope := corpus.Scope{Organization: "evaluation", Actions: []string{"corpora:write", "content:write", "content:read", "search:query"}, Corpora: []string{"*"}}
			c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "one", Name: "Evaluation"})
			if err != nil {
				t.Fatal(err)
			}
			r, err := contents.Accept(ctx, scope, content.Command{Key: "one", Source: content.Source{CorpusID: c.ID, Namespace: "docs", RecordKey: "one"}, Content: content.Text{Kind: "text", Text: "alpha beta gamma"}})
			if err != nil {
				t.Fatal(err)
			}
			if err = contents.Materialize(ctx, scope.Organization, r.ID); err != nil {
				t.Fatal(err)
			}
			r, err = store.Receipt(ctx, scope.Organization, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			v, err := contents.Version(ctx, scope, r.RecordID, r.VersionID)
			if err != nil {
				t.Fatal(err)
			}
			seg, err := content.PluginSegmentation(scope.Organization, v, "plugin:example.hash_embedder@0.1.0", json.RawMessage(`{}`), []content.SegmentInput{{PartKey: "body", Start: 0, End: 16}})
			if err != nil {
				t.Fatal(err)
			}
			if err = contents.SaveSegmentation(ctx, scope.Organization, v, seg); err != nil {
				t.Fatal(err)
			}
			g, err := store.Generation(ctx, scope.Organization, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Promote(ctx, scope.Organization, seg, g); err != nil {
				t.Fatal(err)
			}
			if jobs, err := store.ClaimIngestionEvaluations(ctx, 10); err != nil || len(jobs) != 0 {
				t.Fatalf("evaluation before served vectors: %+v %v", jobs, err)
			}
			sp, ok := (pluginhttp.Ingestor{Pin: pins.IngestionFor("text/plain")}).VectorSpace(g.SpaceID)
			if !ok {
				t.Fatal("served space missing")
			}
			vector := make([]float32, sp.Dimensions)
			vector[0] = 1
			artifact, err := contents.SaveEmbedding(ctx, content.EmbeddingInput(scope.Organization, c.ID, v, seg, seg.Segments[0], sp, "fixture"), sp, vector)
			if err != nil {
				t.Fatal(err)
			}
			evaluationPin := pins.EvaluationFor("text/plain")[0]
			live, err := plugins.NewLive(plan.Plan, pins)
			if err != nil {
				t.Fatal(err)
			}
			pinned, err := live.Pin(ctx, plugins.Work{Plan: plan.Plan}, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			// An upgrade can retire the live registry's old space keys while
			// this served call and its optional work still belong to the old plan.
			updatedManifest := strings.ReplaceAll(strings.Replace(string(evaluationPin.Source), "version: 0.1.0", "version: 0.2.0", 1), `version: "1"`, `version: "2"`)
			updatedPin, err := plugins.LoadPinManifest([]byte(updatedManifest), "upgraded evaluation fixture", plugins.PinConfig{Endpoint: evaluationPin.Endpoint, Spaces: evaluationPin.Spaces})
			if err != nil {
				t.Fatal(err)
			}
			updatedPins, err := plugins.NewPinSet([]*plugins.Pin{pins.IngestionFor("text/plain"), updatedPin})
			if err != nil {
				t.Fatal(err)
			}
			if err = updatedPins.ConfigureIngestion(plugins.IngestionRouting{Default: "example.hash_embedder", Evaluation: map[string][]string{"text/plain": {"certified.ingestion-valid"}}}); err != nil {
				t.Fatal(err)
			}
			if _, err = (postgres.PluginStore{Pool: pool}).ApplyConfiguration(ctx, registry.FromPins(updatedPins)); err != nil {
				t.Fatal(err)
			}
			if err = store.RegisterSpaces(ctx, app.DeploymentSpaces(updatedPins)); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err = store.CommitEnrichment(pinned, scope.Organization, seg, g, []content.Embedding{artifact}); err != nil {
					t.Fatal(err)
				}
			}
			jobs, err := store.ClaimIngestionEvaluations(ctx, 10)
			if err != nil || len(jobs) != 1 {
				t.Fatalf("durable evaluation jobs: %+v %v", jobs, err)
			}
			job := jobs[0]
			if job.PluginID != "certified.ingestion-valid" || job.PlanID != plan.Plan || len(job.Spaces) != 2 {
				t.Fatalf("evaluation target %+v", job)
			}
			for _, key := range job.Spaces {
				if !(pluginhttp.Ingestor{Pin: evaluationPin}).Owns(key) {
					t.Fatalf("old plan queued an upgraded space: %+v", job)
				}
			}
			// Optional dispatch can lag a configuration change. The generation
			// must retain the source route that published its served projection.
			if err = pins.ConfigureIngestion(plugins.IngestionRouting{Default: "example.hash_embedder", Routes: map[string]string{"text/plain": job.PluginID}, Evaluation: map[string][]string{"text/plain": {"example.hash_embedder"}}}); err != nil {
				t.Fatal(err)
			}
			if _, err = (postgres.PluginStore{Pool: pool}).ApplyConfiguration(ctx, registry.FromPins(pins)); err != nil {
				t.Fatal(err)
			}
			var eventCount int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM change_events`).Scan(&eventCount); err != nil {
				t.Fatal(err)
			}
			publication := &evaluationPublication{}
			evaluator := processing.Evaluator{Store: store, Content: contents, Plugin: &processing.PluginDeriver{Content: contents, Plugin: pluginhttp.Ingestor{Pin: evaluationPin}}, Projection: publication}
			if err = evaluator.Run(ctx, scope.Organization, job.ID); err != nil {
				t.Fatal(err)
			}
			retained, err := store.Generation(ctx, scope.Organization, c.ID)
			if err != nil || retained.IngestionRouting == nil || retained.IngestionRouting.For("text/plain") != "example.hash_embedder" {
				t.Fatalf("served route changed during optional dispatch: %+v %v", retained.IngestionRouting, err)
			}
			// A completed job replays without calling or republishing its plugin.
			if err = evaluator.Run(ctx, scope.Organization, job.ID); err != nil {
				t.Fatal(err)
			}
			completed, err := store.IngestionEvaluation(ctx, scope.Organization, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			got, err := contents.Version(ctx, scope, r.RecordID, r.VersionID)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Availability.Searchable || got.Processing.State != "idle" || got.Steps.Enriched == nil {
				t.Fatalf("optional work changed served Version: %+v", got)
			}
			var after int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM change_events`).Scan(&after); err != nil || after != eventCount {
				t.Fatalf("optional work emitted served events: %d -> %d %v", eventCount, after, err)
			}
			if mode == "ingestion-offset" {
				if completed.State != "failed" || len(got.Diagnostics) != 1 || got.Diagnostics[0].Plugin != job.PluginID || publication.vectors != 0 {
					t.Fatalf("isolated refusal: %+v %+v", completed, got.Diagnostics)
				}
			} else {
				if completed.State != "succeeded" || len(publication.segmentation.Segments) != 2 || publication.vectors != 4 {
					t.Fatalf("independent fill: %+v %+v", completed, publication)
				}
				_, spaces, _, err := store.VectorSpaces(ctx, scope.Organization, c.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, s := range spaces {
					if s.OwnerPluginID == job.PluginID && (s.Segments != 2 || s.TotalSegments == nil || *s.TotalSegments != 2 || s.VersionsCovered != 1) {
						t.Fatalf("owner coverage: %+v", s)
					}
				}
			}
		})
	}
}

// Both projection lifecycle owners share the contract-validating process;
// SQL setup never substitutes for its protocol responses.
func startIngestionFixture(t *testing.T, ctx context.Context, manifest, mode string) string {
	t.Helper()
	port, err := devhost.FreePort("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	proc, err := devhost.Start(devhost.Options{Command: fakeplugin.Command(), Manifest: manifest, Port: port, Env: []string{fakeplugin.EnvEnable + "=1", fakeplugin.EnvMode + "=" + mode}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.Stop(time.Second) })
	if err = proc.WaitHealthy(ctx); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}
