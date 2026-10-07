package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/app"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr/internal/processing"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// This adapter lifecycle owns same-plugin reconfiguration: real derivation and
// immutable storage must preserve served cuts until the rebuild switches routes.
func TestRebuildAfterIngestionSettingsChange(t *testing.T) {
	for _, scenario := range []struct {
		name, nextSpace string
		legacy          bool
	}{
		{"legacy segmentation and new model", "2", true},
		{"new model", "2", false},
		{"segment settings only", "1", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			pool := scratchDatabase(t, ctx)
			makePins := func(spaceVersion string, limit int) *plugins.PinSet {
				t.Helper()
				manifest := []byte(fmt.Sprintf(`id: example.rebuild_embedder
version: 0.1.0
compatibility:
  engine: ">=0.1.0 <0.3.0"
  plugin_api: ">=0.6.0 <0.9.0"
contributions:
  ingestion:
    spaces:
      example.rebuild_embedder.text:
        version: %q
        model: example/model-%s
        dimensions: 2
        metric: cosine
        indexes: [text]
        query_modalities: [text]
`, spaceVersion, spaceVersion))
				var pin *plugins.Pin
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/v0/discovery" {
						_ = json.NewEncoder(w).Encode(map[string]any{"plugin_api": pin.PluginAPI(), "plugin": map[string]string{"id": pin.Manifest.ID, "version": pin.Manifest.Version}, "manifest_digest": pin.ManifestDigest, "contributions": pin.Manifest.Contributions.Names()})
						return
					}
					if r.URL.Path == plugins.EmbedQueryRoute {
						_ = json.NewEncoder(w).Encode(map[string]any{"vector": []float32{1, 0}})
						return
					}
					var request plugins.SegmentAndEmbedRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						http.Error(w, err.Error(), 400)
						return
					}
					var settings struct {
						Limit int `json:"segment_limit"`
					}
					if err := json.Unmarshal(request.Configuration, &settings); err != nil || settings.Limit < 1 {
						http.Error(w, "invalid segment_limit", 400)
						return
					}
					segments := []any{}
					for _, part := range request.Parts {
						runes := []rune(part.Text)
						for start := 0; start < len(runes); start += settings.Limit {
							end := min(start+settings.Limit, len(runes))
							vectors := map[string][]float32{}
							for _, space := range request.Spaces {
								vectors[space] = []float32{1, 0}
							}
							segments = append(segments, map[string]any{"part_key": part.Key, "start": start, "end": end, "lexical_text": string(runes[start:end]), "vectors": vectors})
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"segments": segments})
				}))
				t.Cleanup(server.Close)
				var err error
				pin, err = plugins.LoadPinManifest(manifest, "rebuild embedder", plugins.PinConfig{Endpoint: server.URL, Configuration: json.RawMessage(fmt.Sprintf(`{"segment_limit":%d}`, limit))})
				if err != nil {
					t.Fatal(err)
				}
				set, err := plugins.NewPinSet([]*plugins.Pin{pin})
				if err != nil {
					t.Fatal(err)
				}
				return set
			}
			original := makePins("1", 12)
			if err := app.BootstrapDatabase(ctx, pool, app.DeploymentSpaces(original)); err != nil {
				t.Fatal(err)
			}
			pluginStore := postgres.PluginStore{Pool: pool}
			if _, err := pluginStore.ApplyConfiguration(ctx, registry.FromPins(original)); err != nil {
				t.Fatal(err)
			}
			store := contentStores(pool)
			contents := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Baseline: store, Embeddings: store, Blobs: &objectMemory{objects: map[string][]byte{}}}
			scope := corpus.Scope{Organization: "rebuild-settings", Actions: []string{"corpora:write", "content:write", "content:read", "search:query"}, Corpora: []string{"*"}}
			c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "one", Name: "Settings change"})
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := contents.Accept(ctx, scope, content.Command{Key: "one", Source: content.Source{CorpusID: c.ID, Namespace: "docs", RecordKey: "one"}, Content: content.Text{Kind: "text", Text: "alpha beta!!"}})
			if err != nil {
				t.Fatal(err)
			}
			if err = contents.Materialize(ctx, scope.Organization, receipt.ID); err != nil {
				t.Fatal(err)
			}
			receipt, err = store.Receipt(ctx, scope.Organization, receipt.ID)
			if err != nil {
				t.Fatal(err)
			}
			v, err := contents.Version(ctx, scope, receipt.RecordID, receipt.VersionID)
			if err != nil {
				t.Fatal(err)
			}
			prior, err := store.Generation(ctx, scope.Organization, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			var oldPlugin processing.IngestionPlugin = pluginhttp.Ingestor{Pin: original.IngestionFor("text/plain")}
			if scenario.legacy {
				oldPlugin = legacyRebuildIngestor{oldPlugin.(pluginhttp.Ingestor)}
			}
			oldSeg, oldData, err := (processing.PluginDeriver{Content: contents, Plugin: oldPlugin}).Derive(ctx, scope.Organization, c.ID, v, prior)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Promote(ctx, scope.Organization, oldSeg, prior); err != nil {
				t.Fatal(err)
			}
			if err = store.CommitEnrichment(ctx, scope.Organization, oldSeg, prior, []content.Embedding{oldData[0].Artifact}); err != nil {
				t.Fatal(err)
			}
			next := makePins(scenario.nextSpace, 6)
			if _, err = pluginStore.ApplyConfiguration(ctx, registry.FromPins(next)); err != nil {
				t.Fatal(err)
			}
			if err = store.RegisterSpaces(ctx, app.DeploymentSpaces(next)); err != nil {
				t.Fatal(err)
			}
			if _, err = store.AlignDefaultGeneration(ctx); err != nil {
				t.Fatal(err)
			}
			op, err := store.AcceptRebuild(ctx, scope.Organization, c.ID, "settings", []byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			publication := &rebuildPublication{}
			newPlugin := pluginhttp.Ingestor{Pin: next.IngestionFor("text/plain")}
			runner := retrieval.Rebuilder{Store: store, Cancellation: store, Content: contents, Projection: publication, Plugin: processing.PluginDeriver{Content: contents, Plugin: newPlugin}, Routing: store}
			if done, err := runner.Step(ctx, scope.Organization, op.ID); err != nil || done {
				outcome, _ := store.Operation(ctx, scope.Organization, op.ID)
				t.Fatalf("build target: done=%v err=%v operation=%+v", done, err, outcome)
			}
			newSeg := publication.segmentation
			if len(newSeg.Segments) != 2 || newSeg.ID == oldSeg.ID || publication.vectors != 2 {
				t.Fatalf("new segmentation %+v, vectors=%d; want two new cuts", newSeg, publication.vectors)
			}
			if h, err := hydrateOne(ctx, store, scope, content.Candidate{SegmentID: oldSeg.Segments[0].ID, GenerationID: prior.ID}); err != nil || h.EmbeddingID != oldData[0].Artifact.ID {
				t.Fatalf("served generation changed before activation: %+v %v", h, err)
			}
			if done, err := runner.Step(ctx, scope.Organization, op.ID); err != nil || !done {
				t.Fatalf("activate target: done=%v err=%v", done, err)
			}
			if outcome, err := store.Operation(ctx, scope.Organization, op.ID); err != nil || outcome.State != operations.StateSucceeded {
				t.Fatalf("rebuild outcome: %+v %v", outcome, err)
			}
			for n, segment := range newSeg.Segments {
				h, err := hydrateOne(ctx, store, scope, content.Candidate{SegmentID: segment.ID, GenerationID: op.TargetGenerationID})
				if err != nil || h.VersionID != v.ID || h.EmbeddingID != publication.artifacts[n].ID {
					t.Fatalf("target search hydration: %+v %v", h, err)
				}
			}
			if _, err := hydrateOne(ctx, store, scope, content.Candidate{SegmentID: oldSeg.Segments[0].ID, GenerationID: prior.ID}); !errors.Is(err, corpus.ErrNotFound) {
				t.Fatalf("old route still served after activation: %v", err)
			}
			if stored, err := contents.PluginSegmentationOf(ctx, scope.Organization, v, oldSeg.Recipe); err != nil || stored.ID != oldSeg.ID {
				t.Fatalf("old immutable segmentation lost: %+v %v", stored, err)
			}
			if _, err := newPlugin.EncodeQuery(ctx, scope.Organization, "example.rebuild_embedder.text@"+scenario.nextSpace, "alpha"); err != nil {
				t.Fatalf("new model query: %v", err)
			}
		})
	}
}

// Reproduce segmentations written before installed settings joined the recipe.
type legacyRebuildIngestor struct{ pluginhttp.Ingestor }

func (i legacyRebuildIngestor) Descriptor() processing.IngestionDescriptor {
	d := i.Ingestor.Descriptor()
	d.Recipe = "plugin:" + d.PluginID + "@" + d.PluginVersion
	d.Producer = d.Recipe
	return d
}
