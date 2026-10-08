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
	"github.com/The-Vibe-Company/quivr/internal/quarantine"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// This adapter lifecycle owns same-plugin reconfiguration: real derivation and
// immutable storage must preserve served cuts until the rebuild switches routes.
func TestRebuildAfterIngestionSettingsChange(t *testing.T) {
	for _, scenario := range []struct {
		name, nextSpace string
		legacy          bool
		packed          bool
		tuning          bool
	}{
		{"legacy segmentation and new model", "2", true, false, false},
		{"new model", "2", false, false, false},
		{"segment settings only", "1", false, false, false},
		{"pack canonical Parts", "1", false, true, false},
		{"adopt execution tuning without rebuild", "1", false, false, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			pool := scratchDatabase(t, ctx)
			makePins := func(spaceVersion string, limit int, packed, declared bool) *plugins.PinSet {
				t.Helper()
				manifest := []byte(fmt.Sprintf(`id: example.rebuild_embedder
version: 0.1.0
compatibility:
  engine: ">=0.1.0 <0.3.0"
  plugin_api: ">=0.8.0 <0.18.0"
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
				concurrency := 4
				if declared {
					concurrency = 16
				}
				if scenario.tuning {
					declaration := ""
					if declared {
						declaration = "  execution_keys: [max_concurrent_requests]\n"
					}
					manifest = append(manifest, []byte(fmt.Sprintf("configuration:\n%s  schema:\n    type: object\n    properties:\n      max_concurrent_requests: {const: %d}\n", declaration, concurrency))...)
				}
				ring, err := plugins.NewSigningKeys()
				if err != nil {
					t.Fatal(err)
				}
				keys, _ := json.Marshal(map[string]plugins.SigningKeys{"example.rebuild_embedder": ring})
				t.Setenv(plugins.EnvSigningKeys, string(keys))
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
						Limit  int  `json:"segment_limit"`
						Packed bool `json:"packed"`
					}
					if err := json.Unmarshal(request.Configuration, &settings); err != nil || settings.Limit < 1 {
						http.Error(w, "invalid segment_limit", 400)
						return
					}
					segments := []any{}
					if settings.Packed {
						ranges := []plugins.SourceRange{}
						for _, part := range request.Parts {
							ranges = append(ranges, plugins.SourceRange{PartKey: part.Key, End: len([]rune(part.Text))})
						}
						vectors := map[string][]float32{}
						for _, space := range request.Spaces {
							vectors[space] = []float32{1, 0}
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"segments": []any{map[string]any{"part_key": ranges[0].PartKey, "start": 0, "end": ranges[0].End, "source_ranges": ranges, "source_separator": "\n\n", "vectors": vectors}}})
						return
					}
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
				configuration := json.RawMessage(fmt.Sprintf(`{"segment_limit":%d,"packed":%t}`, limit, packed))
				if scenario.tuning {
					configuration = json.RawMessage(fmt.Sprintf(`{"segment_limit":%d,"packed":%t,"max_concurrent_requests":%d}`, limit, packed, concurrency))
				}
				pin, err = plugins.LoadPinManifest(manifest, "rebuild embedder", plugins.PinConfig{Endpoint: server.URL, Configuration: configuration})
				if err != nil {
					t.Fatal(err)
				}
				set, err := plugins.NewPinSet([]*plugins.Pin{pin})
				if err != nil {
					t.Fatal(err)
				}
				return set
			}
			original := makePins("1", 12, false, false)
			if err := app.BootstrapDatabase(ctx, pool, app.Config{}.DeploymentSpaces(original)); err != nil {
				t.Fatal(err)
			}
			pluginStore := postgres.PluginStore{Pool: pool}
			originalPlan, err := pluginStore.ApplyConfiguration(ctx, registry.FromPins(original))
			if err != nil {
				t.Fatal(err)
			}
			live, err := plugins.NewLive(originalPlan.Plan, original)
			if err != nil {
				t.Fatal(err)
			}
			store := contentStores(pool)
			contents := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Baseline: store, Embeddings: store, Blobs: &objectMemory{objects: map[string][]byte{}}}
			scope := corpus.Scope{Organization: "rebuild-settings", Actions: []string{"corpora:write", "content:write", "content:read", "search:query"}, Corpora: []string{"*"}}
			c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "one", Name: "Settings change"})
			if err != nil {
				t.Fatal(err)
			}
			command := content.Command{Key: "one", Source: content.Source{CorpusID: c.ID, Namespace: "docs", RecordKey: "one"}, Content: content.Text{Kind: "text", Text: "alpha beta!!"}}
			if scenario.packed {
				command.Content = content.Text{Kind: "manifest"}
				command.Manifest = &content.Manifest{Kind: "manifest", Parts: []content.Part{{Key: "first", Role: "body", Content: content.Text{Kind: "text", Text: "één 🌌"}}, {Key: "second", Role: "body", Content: content.Text{Kind: "text", Text: "第二段"}}}}
			}
			receipt, err := contents.Accept(ctx, scope, command)
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
			if scenario.tuning {
				// Captured before the fix: neither the upgrade nor adoption may change it.
				const releasedRecipe = "plugin:example.rebuild_embedder@0.1.0#ingestion_34d702c4f0d0ffe347e9bfb5f478897ef556f44f6a61094c2915dc036bf7ad4e"
				if got := oldPlugin.Descriptor().Recipe; got != releasedRecipe {
					t.Fatalf("legacy recipe changed: got %s, want %s", got, releasedRecipe)
				}
			}
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
			if err = store.CommitEnrichment(ctx, scope.Organization, oldSeg, prior, rebuildArtifacts(oldData)); err != nil {
				t.Fatal(err)
			}
			accept := func(key string) content.Receipt {
				t.Helper()
				cmd := command
				cmd.Key, cmd.Source.RecordKey = key, key
				r, err := contents.Accept(ctx, scope, cmd)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			pinWork := func(r content.Receipt, plan string) context.Context {
				t.Helper()
				if _, _, err := pluginStore.PinWork(ctx, plugins.WorkIngestion, scope.Organization, r.ID, plan); err != nil {
					t.Fatal(err)
				}
				pinned, err := live.Pin(ctx, plugins.Work{Kind: plugins.WorkIngestion, Organization: scope.Organization, ID: r.ID, Plan: plan}, nil, 3)
				if err != nil {
					t.Fatal(err)
				}
				return pinned
			}
			oldPublication := &rebuildPublication{}
			oldIndex := retrieval.Service{Routing: store, Content: contents, Projection: oldPublication}
			oldPipeline := processing.Service{Content: contents, Retrieval: oldIndex, Enrichment: oldIndex, Routing: store, Plugin: &processing.PluginDeriver{Content: contents, Plugin: oldPlugin}}
			delayedEnrichment := accept("old-enrichment")
			delayedCtx := pinWork(delayedEnrichment, originalPlan.Plan)
			if err := oldPipeline.Run(delayedCtx, scope.Organization, delayedEnrichment.ID); err != nil {
				t.Fatal(err)
			}
			delayedBaseline := accept("old-baseline")
			baselineCtx := pinWork(delayedBaseline, originalPlan.Plan)
			nextLimit := 6
			if scenario.tuning {
				nextLimit = 12
			}
			next := makePins(scenario.nextSpace, nextLimit, scenario.packed, scenario.tuning)
			nextPlan, err := pluginStore.ApplyConfiguration(ctx, registry.FromPins(next))
			if err != nil {
				t.Fatal(err)
			}
			if scenario.tuning {
				plan, members, err := pluginStore.ActiveMembers(ctx)
				if err != nil {
					t.Fatal(err)
				}
				next, _, err = registry.Resolve(plan.Roles, members)
				if err != nil {
					t.Fatal(err)
				}
				tuned := pluginhttp.Ingestor{Pin: next.IngestionFor("text/plain")}
				if tuned.Descriptor().Recipe != oldPlugin.Descriptor().Recipe || string(tuned.Descriptor().Provenance) != string(oldPlugin.Descriptor().Provenance) {
					t.Fatalf("tuning changed immutable recipe/provenance: %+v -> %+v", oldPlugin.Descriptor(), tuned.Descriptor())
				}
				if err := live.Store(nextPlan.Plan, next); err != nil {
					t.Fatal(err)
				}
				index := retrieval.Service{Routing: store, Content: contents, Projection: &rebuildPublication{}}
				pipeline := processing.Service{Content: contents, Retrieval: index, Enrichment: index, Routing: store, Plugin: &processing.PluginDeriver{Content: contents, Plugin: tuned}}
				arrival := accept("retuned")
				arrivalCtx := pinWork(arrival, nextPlan.Plan)
				if err := pipeline.Run(arrivalCtx, scope.Organization, arrival.ID); err != nil {
					t.Fatal(err)
				}
				if err := pipeline.Enrich(arrivalCtx, scope.Organization, arrival.ID); err != nil {
					t.Fatal(err)
				}
				if err := oldPipeline.Enrich(delayedCtx, scope.Organization, delayedEnrichment.ID); err != nil {
					t.Fatal(err)
				}
				if err := oldPipeline.Run(baselineCtx, scope.Organization, delayedBaseline.ID); err != nil {
					t.Fatal(err)
				}
				if err := oldPipeline.Enrich(baselineCtx, scope.Organization, delayedBaseline.ID); err != nil {
					t.Fatal(err)
				}
				for _, r := range []content.Receipt{receipt, arrival, delayedEnrichment, delayedBaseline} {
					r, err = store.Receipt(ctx, scope.Organization, r.ID)
					if err != nil {
						t.Fatal(err)
					}
					v, err := contents.Version(ctx, scope, r.RecordID, r.VersionID)
					if err != nil || !v.Availability.Searchable || v.Processing.State != "idle" || len(v.Diagnostics) != 0 {
						t.Fatalf("tuning must keep documents served without rebuild_required: %+v %v", v, err)
					}
					seg, err := contents.PluginSegmentationOf(ctx, scope.Organization, v, tuned.Descriptor().Recipe)
					if err != nil {
						t.Fatal(err)
					}
					if h, err := hydrateOne(ctx, store, scope, content.Candidate{SegmentID: seg.Segments[0].ID, GenerationID: prior.ID}); err != nil || h.EmbeddingID == "" {
						t.Fatalf("served vectors after tuning: %+v %v", h, err)
					}
				}
				if seg, err := contents.PluginSegmentationOf(ctx, scope.Organization, v, oldSeg.Recipe); err != nil || content.SegmentationDigest(seg) != content.SegmentationDigest(oldSeg) {
					t.Fatalf("stored artifact changed: %+v %v", seg, err)
				}
				return
			}
			if err = live.Store(nextPlan.Plan, next); err != nil {
				t.Fatal(err)
			}
			if err = store.RegisterSpaces(ctx, app.Config{}.DeploymentSpaces(next)); err != nil {
				t.Fatal(err)
			}
			if _, err = store.AlignDefaultGeneration(ctx); err != nil {
				t.Fatal(err)
			}
			publication := &rebuildPublication{}
			newPlugin := pluginhttp.Ingestor{Pin: next.IngestionFor("text/plain")}
			livePublication := &rebuildPublication{}
			index := retrieval.Service{Routing: store, Content: contents, Projection: livePublication}
			pipeline := processing.Service{Content: contents, Retrieval: index, Enrichment: index, Routing: store, Plugin: &processing.PluginDeriver{Content: contents, Plugin: newPlugin}}
			before := accept("before")
			beforeCtx := pinWork(before, nextPlan.Plan)
			if err := pipeline.Run(beforeCtx, scope.Organization, before.ID); err != nil {
				t.Fatalf("live baseline before rebuild starts: %v", err)
			}
			beforeSeg := livePublication.segmentation
			if h, err := hydrateOne(ctx, store, scope, content.Candidate{SegmentID: beforeSeg.Segments[0].ID, GenerationID: prior.ID}); err != nil || h.VersionID != beforeSeg.VersionID {
				t.Fatalf("lexical search before rebuild starts: %+v %v", h, err)
			}
			if err := pipeline.Enrich(beforeCtx, scope.Organization, before.ID); err != nil {
				t.Fatalf("enrichment before rebuild must settle: %v", err)
			}
			held := accept("held")
			if err := contents.Materialize(ctx, scope.Organization, held.ID); err != nil {
				t.Fatal(err)
			}
			held, err = store.Receipt(ctx, scope.Organization, held.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := contents.QuarantineVersion(ctx, scope.Organization, held.VersionID, content.Diagnostic{Code: "operator_hold", Message: "An operator paused this Version during a recipe change."}); err != nil {
				t.Fatal(err)
			}
			op, err := store.AcceptRebuild(ctx, scope.Organization, c.ID, "settings", []byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			runner := retrieval.Rebuilder{Concurrency: 1, Store: store, Cancellation: store, Content: contents, Projection: publication, Plugin: processing.PluginDeriver{Content: contents, Plugin: newPlugin}, Routing: store}
			if done, err := runner.Step(ctx, scope.Organization, op.ID); err != nil || done {
				outcome, _ := store.Operation(ctx, scope.Organization, op.ID)
				t.Fatalf("build target: done=%v err=%v operation=%+v", done, err, outcome)
			}
			newSeg, err := contents.PluginSegmentationOf(ctx, scope.Organization, v, newPlugin.Descriptor().Recipe)
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if scenario.packed {
				want = 1
			}
			if len(newSeg.Segments) != want || newSeg.ID == oldSeg.ID {
				t.Fatalf("new segmentation %+v; want %d new cuts", newSeg, want)
			}
			if h, err := hydrateOne(ctx, store, scope, content.Candidate{SegmentID: oldSeg.Segments[0].ID, GenerationID: prior.ID}); err != nil || h.EmbeddingID != oldData[0].Artifact.ID {
				t.Fatalf("served generation changed before activation: %+v %v", h, err)
			}
			// A live arrival must finish on the old route while this rebuild runs.
			arrival := accept("during")
			arrivalCtx := pinWork(arrival, nextPlan.Plan)
			if err = pipeline.Run(arrivalCtx, scope.Organization, arrival.ID); err != nil {
				t.Fatalf("live baseline during recipe rebuild: %v", err)
			}
			liveSeg := livePublication.segmentation
			if h, err := hydrateOne(ctx, store, scope, content.Candidate{SegmentID: liveSeg.Segments[0].ID, GenerationID: prior.ID}); err != nil || h.VersionID != liveSeg.VersionID {
				t.Fatalf("live lexical search before cutover: %+v %v", h, err)
			}
			if err = pipeline.Enrich(arrivalCtx, scope.Organization, arrival.ID); err != nil {
				t.Fatalf("incompatible enrichment must settle without retry: %v", err)
			}
			pending := accept("pending-enrichment")
			pendingCtx := pinWork(pending, nextPlan.Plan)
			if err := pipeline.Run(pendingCtx, scope.Organization, pending.ID); err != nil {
				t.Fatal(err)
			}
			pendingSeg := livePublication.segmentation
			// Arrivals can land on either side of the durable rebuild cursor.
			// Finish the bounded forward pass and final gap sweep without sleeps.
			activated := false
			for step := 0; step < 4 && !activated; step++ {
				activated, err = runner.Step(ctx, scope.Organization, op.ID)
				if err != nil {
					t.Fatalf("catch up and activate target: %v", err)
				}
			}
			if !activated {
				t.Fatal("rebuild did not finish after catching up live arrivals")
			}
			if outcome, err := store.Operation(ctx, scope.Organization, op.ID); err != nil || outcome.State != operations.StateSucceeded {
				t.Fatalf("rebuild outcome: %+v %v", outcome, err)
			}
			if err := pipeline.Enrich(pendingCtx, scope.Organization, pending.ID); err != nil {
				t.Fatalf("delayed current-plan enrichment after cutover: %v", err)
			}
			if err = oldPipeline.Enrich(delayedCtx, scope.Organization, delayedEnrichment.ID); err != nil {
				t.Fatalf("delayed old-plan enrichment must settle: %v", err)
			}
			if err = oldPipeline.Run(baselineCtx, scope.Organization, delayedBaseline.ID); err != nil {
				t.Fatalf("delayed old-plan baseline must settle: %v", err)
			}
			jobs, err := store.ClaimServingProjections(ctx, 10)
			if err != nil || len(jobs) == 0 {
				t.Fatalf("historical work needs a bounded current-plan handoff: %+v %v", jobs, err)
			}
			for _, job := range jobs {
				if job.PlanID != nextPlan.Plan {
					t.Fatalf("serving job must use the current plan: %+v", job)
				}
				jobCtx, err := live.Pin(ctx, plugins.Work{Kind: "serving_projection", Organization: scope.Organization, ID: job.ID, Plan: job.PlanID}, nil, 3)
				if err != nil {
					t.Fatal(err)
				}
				evaluator := processing.Evaluator{Store: store, Serving: store, Content: contents, Plugin: &processing.PluginDeriver{Content: contents, Plugin: newPlugin}, Projection: publication}
				if err := evaluator.RunServing(jobCtx, scope.Organization, job.ID); err != nil {
					t.Fatal(err)
				}
			}
			for _, historical := range []content.Receipt{delayedEnrichment, delayedBaseline} {
				if pinned, _, err := pluginStore.PinWork(ctx, plugins.WorkIngestion, scope.Organization, historical.ID, nextPlan.Plan); err != nil || pinned != originalPlan.Plan {
					t.Fatalf("historical receipt pin changed during handoff: %q %v", pinned, err)
				}
				r, err := store.Receipt(ctx, scope.Organization, historical.ID)
				if err != nil {
					t.Fatal(err)
				}
				hv, err := contents.Version(ctx, scope, r.RecordID, r.VersionID)
				if err != nil || !hv.Availability.Searchable || hv.Processing.State != "idle" {
					t.Fatalf("historical receipt settled in serving recipe: %+v %v", hv, err)
				}
				hseg, err := contents.PluginSegmentationOf(ctx, scope.Organization, hv, newPlugin.Descriptor().Recipe)
				if err != nil {
					t.Fatal(err)
				}
				for _, segment := range hseg.Segments {
					if h, err := hydrateOne(ctx, store, scope, content.Candidate{SegmentID: segment.ID, GenerationID: op.TargetGenerationID}); err != nil || h.EmbeddingID == "" || h.SpaceID != "example.rebuild_embedder.text@"+scenario.nextSpace {
						t.Fatalf("historical receipt serves current cuts and vectors: %+v %v", h, err)
					}
				}
			}
			if scenario.nextSpace != "1" {
				arrival, err = store.Receipt(ctx, scope.Organization, arrival.ID)
				if err != nil || arrival.Processing.State != "idle" || len(arrival.Diagnostics) != 0 {
					t.Fatalf("rebuilt receipt: %+v %v", arrival, err)
				}
				ready, err := contents.Version(ctx, scope, arrival.RecordID, arrival.VersionID)
				if err != nil || ready.Processing.State != "idle" || len(ready.Diagnostics) != 0 {
					t.Fatalf("target vectors must settle the rebuild-required outcome: %+v %v", ready, err)
				}
			}
			for _, live := range []content.Segmentation{beforeSeg, liveSeg, pendingSeg} {
				for _, segment := range live.Segments {
					if h, err := hydrateOne(ctx, store, scope, content.Candidate{SegmentID: segment.ID, GenerationID: op.TargetGenerationID}); err != nil || h.EmbeddingID == "" || h.SpaceID != "example.rebuild_embedder.text@"+scenario.nextSpace {
						t.Fatalf("live arrival covered in target: %+v %v", h, err)
					}
				}
			}
			// Arbitrary operator quarantine codes remain reprocessable under the
			// active recipe after cutover, without editing historical artifacts.
			operator := scope
			operator.Actions = append(append([]string(nil), scope.Actions...), registry.Action)
			request := quarantine.Request{Key: "recover", Filter: quarantine.Filter{CorpusID: c.ID, Code: "operator_hold"}, DryRun: true}
			quarantines := quarantine.Service{Store: store}
			if _, _, err := quarantines.Request(ctx, operator, request); err != nil {
				t.Fatal(err)
			}
			request.DryRun = false
			_, recovery, err := quarantines.Request(ctx, operator, request)
			if err != nil {
				t.Fatal(err)
			}
			reprocessCtx, err := live.Pin(ctx, plugins.Work{Kind: plugins.WorkOperation, Organization: scope.Organization, ID: recovery.ID, Plan: nextPlan.Plan}, nil, 3)
			if err != nil {
				t.Fatal(err)
			}
			reprocessor := quarantine.Reprocessor{Store: store, Cancellation: store, Processor: pipeline}
			if progress, err := reprocessor.Step(reprocessCtx, scope.Organization, recovery.ID); err != nil || !progress.Done {
				t.Fatalf("operator quarantine recovery: %+v %v", progress, err)
			}
			if r, err := store.Receipt(ctx, scope.Organization, held.ID); err != nil || r.Availability == nil || !r.Availability.Searchable || len(r.Diagnostics) != 0 {
				t.Fatalf("reprocessed receipt: %+v %v", r, err)
			}
			after := accept("after")
			afterCtx := pinWork(after, nextPlan.Plan)
			if err := pipeline.Run(afterCtx, scope.Organization, after.ID); err != nil {
				t.Fatal(err)
			}
			if err := pipeline.Enrich(afterCtx, scope.Organization, after.ID); err != nil {
				t.Fatal(err)
			}
			for _, segment := range livePublication.segmentation.Segments {
				if h, err := hydrateOne(ctx, store, scope, content.Candidate{SegmentID: segment.ID, GenerationID: op.TargetGenerationID}); err != nil || h.EmbeddingID == "" {
					t.Fatalf("arrival after cutover: %+v %v", h, err)
				}
			}
			if scenario.packed {
				hydrated, err := contents.Hydrate(ctx, scope, []content.Candidate{{SegmentID: newSeg.Segments[0].ID, GenerationID: op.TargetGenerationID}, {SegmentID: newSeg.Segments[0].ID, GenerationID: op.TargetGenerationID}})
				if err != nil || hydrated[0].Segment.Text != "één 🌌\n\n第二段" || len(hydrated[0].Segment.SourceExcerpts) != 2 || hydrated[1].Segment.Text != hydrated[0].Segment.Text {
					t.Fatalf("packed canonical hydration: %+v %v", hydrated, err)
				}
				reloaded, err := contents.PluginSegmentationOf(ctx, scope.Organization, v, newSeg.Recipe)
				if err != nil || len(reloaded.Segments) != 1 || reloaded.Segments[0].Text != "één 🌌\n\n第二段" {
					t.Fatalf("packed recipe round trip: %+v %v", reloaded, err)
				}
			}
			for _, segment := range newSeg.Segments {
				h, err := hydrateOne(ctx, store, scope, content.Candidate{SegmentID: segment.ID, GenerationID: op.TargetGenerationID})
				if err != nil || h.VersionID != v.ID || h.EmbeddingID == "" || h.SpaceID != "example.rebuild_embedder.text@"+scenario.nextSpace {
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

func rebuildArtifacts(data []content.EmbeddingData) []content.Embedding {
	out := make([]content.Embedding, len(data))
	for i, d := range data {
		out[i] = d.Artifact
	}
	return out
}
