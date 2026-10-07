package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/app"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

func pluginSpace(owner, name string, dimensions int, role string) content.RegisteredSpace {
	declared := plugins.VectorSpace{Version: "1", Model: "model-" + name, Dimensions: dimensions, Metric: "cosine", Indexes: []string{"text"}, QueryModalities: []string{"text"}}
	return content.RegisteredSpace{VectorSpace: content.VectorSpace{ID: plugins.SpaceKey(name, "1"), Manifest: plugins.SpaceManifest(owner, name, declared), Dimensions: dimensions},
		Name: name, Version: "1", OwnerPluginID: owner, OwnerPluginVersion: "0.1.0", Model: declared.Model, Metric: "cosine", Indexes: declared.Indexes, QueryModalities: declared.QueryModalities, Role: role}
}

// The registry keeps one owner per space and the deployment's roles; a
// rebuild target carries the served and evaluation spaces as named spaces;
// a plugin segmentation reads back identical after PostgreSQL stores it;
// coverage counts the current segments holding a vector in each space.
func TestVectorSpaceRegistryAndNamedSpaceCoverage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := rebuildAdapterPool(t, ctx)
	store := contentStores(pool)
	served, evaluation := pluginSpace("example.words", "example.words.small", 4, content.SpaceServed), pluginSpace("example.words", "example.words.large", 6, content.SpaceEvaluation)
	// The registry is the deployment's (the verify stack pins core.ingest):
	// restore every space's role as it was, and retire the test's spaces.
	roles := map[string]string{}
	rows, err := pool.Query(ctx, `SELECT id,role FROM vector_spaces`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, role string
		if err = rows.Scan(&id, &role); err != nil {
			t.Fatal(err)
		}
		roles[id] = role
	}
	rows.Close()
	t.Cleanup(func() {
		if len(roles) == 0 {
			if err := store.RegisterSpaces(context.Background(), app.DeploymentSpaces(nil)); err != nil {
				t.Errorf("restore the registry: %v", err)
			}
			return
		}
		if _, err := pool.Exec(context.Background(), `UPDATE vector_spaces SET role='retired'`); err != nil {
			t.Errorf("restore the registry: %v", err)
		}
		for id, role := range roles {
			if _, err := pool.Exec(context.Background(), `UPDATE vector_spaces SET role=$2 WHERE id=$1`, id, role); err != nil {
				t.Errorf("restore the registry: %v", err)
			}
		}
	})
	if err := store.RegisterSpaces(ctx, []content.RegisteredSpace{served, evaluation}); err != nil {
		t.Fatal(err)
	}
	// Registering again, under a newer plugin version, keeps the space.
	upgraded := served
	upgraded.OwnerPluginVersion = "0.2.0"
	if err := store.RegisterSpaces(ctx, []content.RegisteredSpace{upgraded, evaluation}); err != nil {
		t.Fatal(err)
	}
	stolen := pluginSpace("example.other", "example.words.small", 4, content.SpaceServed)
	if err := store.RegisterSpaces(ctx, []content.RegisteredSpace{stolen}); !errors.Is(err, content.ErrSpaceOwner) {
		t.Fatalf("another owner for a registered space: %v", err)
	}
	resized := pluginSpace("example.words", "example.words.small", 8, content.SpaceServed)
	if err := store.RegisterSpaces(ctx, []content.RegisteredSpace{resized}); !errors.Is(err, content.ErrSpaceChanged) {
		t.Fatalf("other dimensions under the same space version: %v", err)
	}

	org := fmt.Sprintf("adapter-spaces-%d", time.Now().UnixNano())
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read", "search:query"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "spaces", Name: "Spaces"})
	if err != nil {
		t.Fatal(err)
	}
	service := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Baseline: store}
	const text = "Harbour strike. Port closed."
	r, err := service.Accept(ctx, scope, content.Command{Key: "article", Source: content.Source{CorpusID: c.ID, Namespace: "spaces", RecordKey: "article"}, Content: content.Text{Kind: "text", Text: text}})
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := store.Work(ctx, org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Publish(ctx, work, publication(content.Blob{Key: "fixture/text", SHA256: "spaces-text", Size: int64(len(text))}, content.Blob{Key: "fixture/manifest", SHA256: "spaces-manifest", Size: 2})); err != nil {
		t.Fatal(err)
	}
	v := content.Version{ID: work.VersionID, RecordID: work.RecordID, Manifest: content.Manifest{Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: text}}}}}
	const recipe = "plugin:example.words@0.2.0"
	seg, err := content.PluginSegmentation(org, v, recipe, json.RawMessage(`{"plugin_version": "0.2.0", "plugin_id": "example.words"}`), []content.SegmentInput{
		{PartKey: "body", Start: 0, End: 15, LexicalText: "harbour strike", Provenance: json.RawMessage(`{"tokens": 3, "template": "passage"}`)},
		{PartKey: "body", Start: 16, End: 28},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveSegmentation(ctx, org, seg); err != nil {
		t.Fatal(err)
	}
	stored, err := service.PluginSegmentationOf(ctx, org, v, recipe)
	if err != nil || content.SegmentationDigest(stored) != content.SegmentationDigest(seg) || stored.Segments[0].Derivation.LexicalText != "harbour strike" {
		t.Fatalf("plugin segmentation read back %+v, %v", stored, err)
	}
	if _, err = service.PluginSegmentationOf(ctx, org, v, "plugin:example.words@0.3.0"); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatalf("another plugin version has no segmentation yet: %v", err)
	}
	// The Version becomes searchable on the Corpus's current generation first.
	prior, err := store.Generation(ctx, org, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Promote(ctx, org, seg, prior); err != nil {
		t.Fatal(err)
	}

	// A rebuild target is built with the registry's spaces, served first.
	op, err := store.AcceptRebuild(ctx, org, c.ID, "spaces", []byte(`{"idempotency_key":"spaces"}`))
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.BeginRebuild(ctx, org, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if g := target.Generation; !g.SpacesProjected || g.SpaceID != served.ID || fmt.Sprint(g.VectorSpaces()) != fmt.Sprint([]string{served.ID, evaluation.ID}) {
		t.Fatalf("target generation %+v", g)
	}
	artifacts := []content.Embedding{}
	for _, p := range seg.Segments {
		for _, space := range []content.RegisteredSpace{served, evaluation} {
			// Only the served space is required: the second segment has no evaluation vector.
			if space.ID == evaluation.ID && p.ID == seg.Segments[1].ID {
				continue
			}
			e := content.Embedding{ID: "artifact-" + space.Name + "-" + p.ID, DerivationID: "derivation-" + space.Name + "-" + p.ID, Organization: org, CorpusID: c.ID, VersionID: v.ID, SegmentID: p.ID, SegmentationID: seg.ID, SpaceID: space.ID}
			if err = store.SaveEmbedding(ctx, e, space.VectorSpace); err != nil {
				t.Fatal(err)
			}
			artifacts = append(artifacts, e)
		}
	}
	if covered, err := store.CoverRebuild(ctx, org, op.ID, seg, artifacts); err != nil || !covered {
		t.Fatalf("cover %v %v", covered, err)
	}
	if done, err := store.ActivateRebuild(ctx, org, op.ID); err != nil || !done {
		t.Fatalf("activate %v %v", done, err)
	}
	g, spaces, total, err := store.VectorSpaces(ctx, org, c.ID)
	if err != nil || g.ID != op.TargetGenerationID || total != 2 || len(spaces) != 2 {
		t.Fatalf("vector spaces of %s: %+v, %d segments, %v", g.ID, spaces, total, err)
	}
	if s := spaces[0]; s.ID != served.ID || s.GenerationRole != content.SpaceServed || s.OwnerPluginID != "example.words" || s.OwnerPluginVersion != "0.2.0" || s.Segments != 2 || s.VersionsCovered != 1 || s.TotalSegments == nil || *s.TotalSegments != 2 || s.ServingSegments == nil || *s.ServingSegments != 2 || s.Model != served.Model {
		t.Fatalf("served space %+v", s)
	}
	if s := spaces[1]; s.ID != evaluation.ID || s.GenerationRole != content.SpaceEvaluation || s.Segments != 1 || s.VersionsCovered != 0 || s.TotalSegments == nil || *s.TotalSegments != 2 || s.ServingSegments == nil || *s.ServingSegments != 2 || s.VectorSpace.Dimensions != 6 {
		t.Fatalf("evaluation space %+v", s)
	}
	h, err := hydrateOne(ctx, store, scope, content.Candidate{SegmentID: seg.Segments[0].ID, GenerationID: g.ID})
	if err != nil || h.SpaceID != served.ID {
		t.Fatalf("hydration serves the served space: %+v %v", h, err)
	}
}

// Independent evaluation cuts must coexist with the serving segmentation;
// canonical hydration keeps them out of normal searches and fences withdrawal.
func TestIndependentEvaluationProjectionCoverage(t *testing.T) {
	ctx := t.Context()
	pool := rebuildAdapterPool(t, ctx)
	store := contentStores(pool)
	org := fmt.Sprintf("adapter-evaluation-%d", time.Now().UnixNano())
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read", "search:query"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "evaluation", Name: "Evaluation"})
	if err != nil {
		t.Fatal(err)
	}
	service := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Baseline: store}
	cmd := content.Command{Key: "one", Source: content.Source{CorpusID: c.ID, Namespace: "evaluation", RecordKey: "one"}, Content: content.Text{Kind: "text", Text: "alpha beta gamma"}}
	receipt, err := service.Accept(ctx, scope, cmd)
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := store.Work(ctx, org, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Publish(ctx, work, publication(content.Blob{Key: "fixture/evaluation-text", SHA256: "evaluation-text", Size: 16}, content.Blob{Key: "fixture/evaluation-manifest", SHA256: "evaluation-manifest", Size: 2})); err != nil {
		t.Fatal(err)
	}
	v := content.Version{ID: work.VersionID, RecordID: work.RecordID, Manifest: content.ManifestFor(cmd)}
	g, err := store.Generation(ctx, org, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	served, err := content.PluginSegmentation(org, v, "plugin:example.served@1.0.0", json.RawMessage(`{}`), []content.SegmentInput{{PartKey: "body", Start: 0, End: 16}})
	if err != nil {
		t.Fatal(err)
	}
	evaluation, err := content.PluginSegmentation(org, v, "plugin:example.evaluation@1.0.0", json.RawMessage(`{}`), []content.SegmentInput{{PartKey: "body", Start: 0, End: 5}, {PartKey: "body", Start: 6, End: 16}})
	if err != nil {
		t.Fatal(err)
	}
	for _, seg := range []content.Segmentation{served, evaluation} {
		if err = store.SaveSegmentation(ctx, org, seg); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.Promote(ctx, org, served, g); err != nil {
		t.Fatal(err)
	}
	// A projection with different offsets records no served Version state.
	space := pluginSpace("example.evaluation", "example.evaluation.space", 2, content.SpaceEvaluation)
	all, err := store.RegisteredSpaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.RegisterSpaces(context.Background(), all); err != nil {
			t.Error(err)
		}
	})
	if err = store.RegisterSpaces(ctx, append(all, space)); err != nil {
		t.Fatal(err)
	}
	g, err = store.PrepareEvaluation(ctx, org, c.ID, []string{space.ID})
	if err != nil {
		t.Fatal(err)
	}
	var artifacts []content.Embedding
	for i, p := range evaluation.Segments {
		e := content.Embedding{ID: fmt.Sprintf("eval-artifact-%s-%d", v.ID, i), DerivationID: fmt.Sprintf("eval-derivation-%s-%d", v.ID, i), Organization: org, VersionID: v.ID, SegmentID: p.ID, SegmentationID: evaluation.ID, SpaceID: space.ID}
		if err = store.SaveEmbedding(ctx, e, space.VectorSpace); err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, e)
	}
	if err = store.CoverEvaluation(ctx, org, g, evaluation, artifacts); err != nil {
		t.Fatal(err)
	}
	_, coverage, _, err := store.VectorSpaces(ctx, org, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, covered := range coverage {
		if covered.OwnerPluginID == "example.evaluation" && (covered.ServingSegments == nil || *covered.ServingSegments != 0 || covered.TotalSegments == nil || *covered.TotalSegments != 2) {
			t.Fatalf("evaluation-only owner coverage must expose two independent cuts and zero serving cuts: %+v", covered)
		}
	}
	candidates := []content.Candidate{{SegmentID: served.Segments[0].ID, GenerationID: g.ID}, {SegmentID: evaluation.Segments[0].ID, GenerationID: g.ID}, {SegmentID: evaluation.Segments[1].ID, GenerationID: g.ID, EvaluationPlugin: "example.evaluation", EvaluationSpace: space.ID}}
	got, err := store.Hydrate(ctx, scope, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SegmentationID != served.ID || got[2].SegmentationID != evaluation.ID || got[2].EmbeddingID != artifacts[1].ID {
		t.Fatalf("independent coverage/hydration: %+v", got)
	}
	status, processing, code, err := store.VersionStatus(ctx, org, v.ID)
	if err != nil || !status.Searchable || processing.Phase != "enrichment" || code != "" {
		t.Fatalf("evaluation changed served state: %+v %+v %s %v", status, processing, code, err)
	}

	// Fresh bulk indexing can leave planner statistics far behind actual rows.
	// Clone this valid independent-owner fixture without ANALYZE, then check
	// real coverage at its serving budget rather than a wall-clock sleep.
	// At least one million segments: this is a fixed-budget regression for
	// THE-1231, not an optional load test. Bulk setup exceeds the usual small
	// adapter fixture because the bug only appears at segment scale.
	cuts := 1
	copies := 2000
	if os.Getenv("QUIVR_MEASURE") == "1" {
		cuts = 8
		copies = 100000
	}
	if _, err = pool.Exec(ctx, `INSERT INTO segments
 SELECT (jsonb_populate_record(NULL::segments,to_jsonb(s)||jsonb_build_object('id',s.id||'-cut-'||n))).*
 FROM segments s CROSS JOIN generate_series(1,$3::int) n WHERE s.organization=$1 AND s.id=$2`, org, served.Segments[0].ID, cuts-1); err != nil {
		t.Fatal(err)
	}
	for _, table := range []struct {
		name   string
		patch  string
		filter string
	}{
		{"records", "jsonb_build_object('id',r.id||'-'||n,'record_key',r.record_key||'-'||n,'current_version_id',r.current_version_id||'-'||n)", "r.id=$2"},
		{"record_versions", "jsonb_build_object('id',r.id||'-'||n,'record_id',r.record_id||'-'||n,'provenance','{}'::jsonb)", "r.id=$3"},
		{"version_parts", "jsonb_build_object('version_id',r.version_id||'-'||n)", "r.version_id=$3"},
		{"segmentations", "jsonb_build_object('id',r.id||'-'||n,'version_id',r.version_id||'-'||n)", "r.version_id=$3"},
		{"segments", "jsonb_build_object('id',r.id||'-'||n,'version_id',r.version_id||'-'||n,'segmentation_id',r.segmentation_id||'-'||n)", "r.version_id=$3"},
		{"projection_coverage", "jsonb_build_object('version_id',r.version_id||'-'||n,'segmentation_id',r.segmentation_id||'-'||n)", "r.version_id=$3"},
		{"embedding_artifacts", "jsonb_build_object('id',r.id||'-'||n,'segment_id',r.segment_id||'-'||n,'derivation_id',r.derivation_id||'-'||n)", "r.segment_id=ANY($4::text[])"},
		{"embedding_coverage", "jsonb_build_object('segment_id',r.segment_id||'-'||n,'artifact_id',r.artifact_id||'-'||n)", "r.segment_id=ANY($4::text[])"},
	} {
		query := "INSERT INTO " + table.name + " SELECT (jsonb_populate_record(NULL::" + table.name + ",to_jsonb(r)||" + table.patch + ")).* FROM " + table.name + " r CROSS JOIN generate_series(1,$5::int) n WHERE r.organization=$1 AND " + table.filter
		// Explicit casts keep all shared fixture parameters typed even when unused.
		query += " AND $2::text IS NOT NULL AND $3::text IS NOT NULL AND $4::text[] IS NOT NULL"
		if _, err = pool.Exec(ctx, query, org, v.RecordID, v.ID, []string{evaluation.Segments[0].ID, evaluation.Segments[1].ID}, copies); err != nil {
			t.Fatal(table.name, err)
		}
	}
	snapshots := retrieval.NewSpaceSnapshots(ctx, store, 10*time.Second)
	budget, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, described, total, err := snapshots.VectorSpaces(budget, org, c.ID)
	if err != nil || total != 0 || len(described) == 0 || !described[0].CoverageUnknown {
		t.Fatalf("cold coverage must return unknown promptly: %+v, total %d, error %v", described, total, err)
	}
	// Exercise authorization, routing, cold coverage and canonical hydration.
	// The external index and retrieval plugin are deterministic dependencies;
	// this guard isolates the database bottleneck rather than index capacity.
	search := retrieval.Service{Routing: postgres.ProjectionStore{Pool: pool}, Registry: snapshots,
		Projection: coverageIndex{candidate: candidates[0]}, Ranker: coverageRanker{}, Content: service}
	search.Content.Blobs = &objectMemory{objects: map[string][]byte{"fixture/evaluation-text": []byte("alpha beta gamma")}}
	var latencies []time.Duration
	for range 20 {
		started := time.Now()
		result, err := search.Search(ctx, scope, retrieval.Request{Query: "alpha", Mode: "lexical", CorpusIDs: []string{c.ID}})
		if err != nil || len(result.Hits) != 1 || result.Hits[0].Segment.ID != served.Segments[0].ID {
			t.Fatalf("search with large coverage: %+v, %v", result, err)
		}
		latencies = append(latencies, time.Since(started))
	}
	slices.Sort(latencies)
	p95 := latencies[18]
	t.Logf("%d segments, cold coverage enabled, lexical search p95 %s", (cuts+2)*(copies+1), p95)
	if p95 >= time.Second {
		t.Fatalf("search p95 %s, objective <1s", p95)
	}
	// The same PostgreSQL source must eventually publish real counts, not
	// leave every request unknown because the background query always fails.
	refresh, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, completed, total, err := snapshots.VectorSpaces(refresh, org, c.ID)
		if err != nil {
			t.Fatalf("completed coverage: %v", err)
		}
		if len(completed) > 0 && !completed[0].CoverageUnknown {
			if total != int64(cuts*(copies+1)) {
				t.Fatalf("snapshot serving total %d, want %d", total, cuts*(copies+1))
			}
			found := false
			for _, sp := range completed {
				if sp.ID == space.ID {
					found = true
					if sp.CoverageUnknown || sp.CoverageAgeMS == nil || sp.Segments != int64(2*(copies+1)) || sp.TotalSegments == nil || *sp.TotalSegments != int64(2*(copies+1)) || sp.VersionsCovered != int64(copies+1) {
						t.Fatalf("completed independent coverage: %+v", sp)
					}
				}
			}
			if !found {
				t.Fatal("completed snapshot lost the evaluation space")
			}
			break
		}
		select {
		case <-refresh.Done():
			t.Fatalf("background coverage did not complete: %v", refresh.Err())
		case <-ticker.C:
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tombstones(organization,record_id) VALUES($1,$2)`, org, v.RecordID); err != nil {
		t.Fatal(err)
	}
	got, err = store.Hydrate(ctx, scope, candidates)
	if err != nil || len(got) != 0 {
		t.Fatalf("withdrawn evaluation hydrated: %+v %v", got, err)
	}
}

type coverageIndex struct {
	retrieval.Projection
	candidate content.Candidate
}

func (p coverageIndex) Search(context.Context, []retrieval.Route, corpus.Scope, retrieval.Request) ([]content.Candidate, error) {
	return []content.Candidate{p.candidate}, nil
}

type coverageRanker struct{}

func (coverageRanker) Manifest() *plugins.Manifest {
	return &plugins.Manifest{ID: "example.coverage", Version: "0.1.0", Contributions: plugins.Contributions{Retrieval: &plugins.Retrieval{
		Profiles: map[string]plugins.RetrievalProfile{"default": {MaxLatencyMS: 1000}},
		Limits:   plugins.RetrievalLimits{MaxRounds: 2, MaxRequests: 1, MaxCandidates: 50},
	}}}
}

func (coverageRanker) Configuration() json.RawMessage { return json.RawMessage(`{}`) }

func (coverageRanker) Round(_ context.Context, request plugins.SearchRequest) ([]byte, error) {
	if request.Round == 1 {
		return []byte(`{"requests":[{"primitive":"bm25","query_text":"alpha","k":1}]}`), nil
	}
	return json.Marshal(map[string]any{"ranking": map[string]any{"hits": []any{map[string]any{"segment_id": request.Served[0].Candidates[0].SegmentID, "score": 1}}}})
}
