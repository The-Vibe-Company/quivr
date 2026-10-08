package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/app"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/processing"
	"github.com/The-Vibe-Company/quivr/migrations"
)

func TestPackedVectorsReuseCanonicalPartialOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	if err := app.BootstrapDatabase(ctx, pool, app.Config{}.DeploymentSpaces(nil)); err != nil {
		t.Fatal(err)
	}
	contract, err := migrations.Files.ReadFile("20261008T1035Z_compact_storage_only.sql")
	if err != nil {
		t.Fatal(err)
	}
	var retiredTables []string
	for _, line := range strings.Split(string(contract), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "DROP" && fields[1] == "TABLE" {
			retiredTables = append(retiredTables, strings.TrimSuffix(fields[2], ";"))
		}
	}
	if len(retiredTables) != 4 {
		t.Fatalf("contract retires %d tables, want 4", len(retiredTables))
	}
	countTables := func() int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM unnest($1::text[]) AS t(name) WHERE to_regclass(t.name) IS NOT NULL`, retiredTables).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if got := countTables(); got != 4 {
		t.Fatalf("expand schema has %d contract targets, want 4", got)
	}
	if err := postgres.MigrateContracts(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if got := countTables(); got != 0 {
		t.Fatalf("contract left %d retired tables", got)
	}
	org := fmt.Sprintf("compact-vectors-%d-<>&\u2028\u2029", time.Now().UnixNano())
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "files", Name: "Vector files"})
	if err != nil {
		t.Fatal(err)
	}
	objects := &objectMemory{objects: map[string][]byte{}}
	stores := contentStores(pool)
	service := content.Service{Submissions: stores, Materialization: stores, Versions: stores, RecordStore: stores, Receipts: stores, Baseline: stores, Embeddings: stores, Blobs: objects}
	for _, scenario := range []struct {
		name          string
		partial, race bool
	}{{name: "fresh"}, {name: "partial", partial: true}, {name: "concurrent", race: true}} {
		partial := scenario.partial
		cmd := content.Command{Key: scenario.name, Source: content.Source{CorpusID: c.ID, Namespace: "example", RecordKey: scenario.name}, Content: content.Text{Kind: "text", Text: "First second"}}
		receipt, err := service.Accept(ctx, scope, cmd)
		if err != nil {
			t.Fatal(err)
		}
		if err = service.Materialize(ctx, org, receipt.ID); err != nil {
			t.Fatal(err)
		}
		v, err := service.ProcessingVersion(ctx, org, receipt.ID)
		if err != nil {
			t.Fatal(err)
		}
		seg, err := content.PluginSegmentation(org, v, "plugin:core.ingest@1.0.0", json.RawMessage(`{"producer":"example"}`), []content.SegmentInput{{PartKey: "body", Start: 0, End: 5, Provenance: json.RawMessage(`null`)}, {PartKey: "body", Start: 6, End: 12}})
		if err != nil {
			t.Fatal(err)
		}
		if err = service.SaveSegmentation(ctx, org, v, seg); err != nil {
			t.Fatal(err)
		}
		g, err := stores.Generation(ctx, org, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		space := content.VectorSpace{ID: g.SpaceID, Dimensions: 2}
		if err = pool.QueryRow(ctx, `SELECT manifest FROM vector_spaces WHERE id=$1`, space.ID).Scan(&space.Manifest); err != nil {
			t.Fatal(err)
		}
		data := []content.EmbeddingData{}
		for i, p := range seg.Segments {
			data = append(data, content.EmbeddingData{Artifact: content.EmbeddingInput(org, c.ID, v, seg, p, space, "plugin:core.ingest@1.0.0"), Vector: []float32{float32(1 + 2*i), float32(2 + 2*i)}})
		}
		originalID := ""
		if partial {
			original, err := service.SaveEmbeddingGroup(ctx, seg, space, data[:1])
			if err != nil {
				t.Fatal(err)
			}
			originalID = original[0].Artifact.ID
			data[0].Vector = []float32{9, 9}
		}
		before := len(objects.objects)
		var packed []content.EmbeddingData
		expectedVectors, expectedObjects := [][]float32{{1, 2}, {3, 4}}, 1
		if scenario.race {
			gated := &concurrentVectorBlobs{Blobs: objects, arrived: make(chan struct{}, 2), release: make(chan struct{})}
			service.Blobs = gated
			alternative := []content.EmbeddingData{{Artifact: data[0].Artifact, Vector: []float32{7, 8}}, {Artifact: data[1].Artifact, Vector: []float32{5, 6}}}
			type result struct {
				data []content.EmbeddingData
				err  error
			}
			results := make(chan result, 2)
			for _, candidate := range [][]content.EmbeddingData{data, alternative} {
				go func(candidate []content.EmbeddingData) {
					d, err := service.SaveEmbeddingGroup(ctx, seg, space, candidate)
					results <- result{d, err}
				}(candidate)
			}
			for range 2 {
				select {
				case <-gated.arrived:
				case <-ctx.Done():
					close(gated.release)
					t.Fatal(ctx.Err())
				}
			}
			close(gated.release)
			first, second := <-results, <-results
			if first.err != nil || second.err != nil {
				t.Fatalf("concurrent adoption %v %v", first.err, second.err)
			}
			packed = first.data
			if len(packed) != 2 || len(second.data) != 2 {
				t.Fatal("incomplete concurrent winner")
			}
			if packed[0].Vector[0] == 7 {
				expectedVectors = [][]float32{{7, 8}, {5, 6}}
			}
			for i := range packed {
				if packed[i].Artifact.ID != second.data[i].Artifact.ID || !slices.Equal(packed[i].Vector, second.data[i].Vector) {
					t.Fatal("concurrent writers did not adopt one canonical file")
				}
			}
			expectedObjects = 2 // The losing immutable upload is retained for guarded cleanup.
		} else {
			packed, err = service.SaveEmbeddingGroup(ctx, seg, space, data)
			if err != nil {
				t.Fatal(err)
			}
		}
		service.Blobs = objects
		var files int
		if err = pool.QueryRow(ctx, `SELECT count(*) FROM embedding_files f JOIN storage_organizations o ON o.id=f.organization_id WHERE o.organization=$1 AND f.segmentation_id=$2`, org, seg.ID).Scan(&files); err != nil {
			t.Fatal(err)
		}
		if files != 1 || len(objects.objects) != before+expectedObjects {
			t.Fatalf("files=%d additional objects=%d; want one current descriptor and bounded candidate uploads", files, len(objects.objects)-before)
		}
		if len(packed) != 2 || !slices.Equal(packed[0].Vector, expectedVectors[0]) || !slices.Equal(packed[1].Vector, expectedVectors[1]) {
			t.Fatalf("canonical partial output changed: %+v", packed)
		}
		if partial && packed[0].Artifact.ID != originalID {
			t.Fatal("conversion changed public artifact ID")
		}
		inputs := []content.Embedding{data[0].Artifact, data[1].Artifact}
		recovered, complete, err := service.LoadEmbeddingGroup(ctx, inputs)
		if err != nil || !complete || len(recovered) != 2 || !slices.Equal(recovered[0].Vector, expectedVectors[0]) || !slices.Equal(recovered[1].Vector, expectedVectors[1]) {
			t.Fatalf("stored group reuse: %+v complete=%v err=%v", recovered, complete, err)
		}
		// A new generation must recover the same float32s with the provider disabled.
		rebuildGeneration := g
		rebuildGeneration.ID = "rebuilt-generation"
		deriver := processing.PluginDeriver{Content: service, Plugin: storedVectorProvider{space: space, recipe: seg.Recipe}}
		_, reused, err := deriver.Derive(ctx, org, c.ID, v, rebuildGeneration)
		if err != nil || len(reused) != 2 || !slices.Equal(reused[0].Vector, expectedVectors[0]) || !slices.Equal(reused[1].Vector, expectedVectors[1]) || reused[0].Artifact.ID != packed[0].Artifact.ID {
			t.Fatalf("rebuild called disabled provider or changed vectors: %v %+v", err, reused)
		}

		if err = service.Promote(ctx, org, seg, g); err != nil {
			t.Fatal(err)
		}
		if err = service.CommitEnrichment(ctx, org, seg, g, packed); err != nil {
			t.Fatal(err)
		}
		if err = service.CommitEnrichment(ctx, org, seg, g, packed); err != nil {
			t.Fatal(err)
		}
		located, err := stores.Hydrate(ctx, scope, []content.Candidate{{SegmentID: seg.Segments[0].ID, GenerationID: g.ID}})
		if err != nil || located[0].EmbeddingID != packed[0].Artifact.ID {
			t.Fatalf("search hydration lost compact identity: %v %+v", err, located)
		}

		for _, reader := range []struct {
			name string
			load func() ([]content.Embedding, error)
		}{
			{"backfill", func() ([]content.Embedding, error) { return stores.CoveredEmbeddings(ctx, org, g.ID, seg) }},
			{"subscriptions", func() ([]content.Embedding, error) {
				_, artifacts, _, err := stores.SubscriptionEmbeddings(ctx, org, c.ID, v.ID)
				return artifacts, err
			}},
		} {
			artifacts, err := reader.load()
			if err != nil || len(artifacts) != 2 {
				t.Fatalf("%s metadata: %v %+v", reader.name, err, artifacts)
			}
			loaded, err := service.LoadEmbeddingData(ctx, artifacts)
			if err != nil {
				t.Fatalf("%s integrity: %v", reader.name, err)
			}
			for _, d := range loaded {
				i := slices.IndexFunc(packed, func(want content.EmbeddingData) bool { return want.Artifact.ID == d.Artifact.ID })
				if i < 0 || d.Artifact.DerivationID != packed[i].Artifact.DerivationID || !slices.Equal(d.Vector, packed[i].Vector) {
					t.Fatalf("%s identity/vector: %+v", reader.name, d)
				}
			}
		}

		var covered []byte
		if err = pool.QueryRow(ctx, `SELECT covered FROM compact_embedding_coverage WHERE file_id=$1 AND generation_id=$2`, packed[0].Artifact.File.ID, g.ID).Scan(&covered); err != nil {
			t.Fatal(err)
		}
		if !content.Present(covered, 0) || !content.Present(covered, 1) {
			t.Fatalf("coverage %v", covered)
		}
		if scenario.name == "fresh" {
			// Bitmap coverage counts passages, even though there is one file and
			// one Version. Replaying it must not count the file's bits twice.
			op, err := stores.AcceptRebuild(ctx, org, c.ID, "packed-rebuild", []byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = stores.BeginRebuild(ctx, org, op.ID); err != nil {
				t.Fatal(err)
			}
			artifacts := []content.Embedding{packed[0].Artifact, packed[1].Artifact}
			for range 2 {
				if _, err = stores.CoverRebuild(ctx, org, op.ID, seg, artifacts); err != nil {
					t.Fatal(err)
				}
			}
			progress, err := stores.Operation(ctx, org, op.ID)
			if err != nil || progress.Counters["versions_covered"] != 1 || progress.Counters["passages_covered"] != 2 || progress.Counters["indexed"] != 1 || progress.Counters["vectors_reused"] != 2 {
				t.Fatalf("compact rebuild counters: %+v %v", progress.Counters, err)
			}
		}

	}
}

// Only the external embedding boundary is disabled; storage/reuse is real.
type storedVectorProvider struct {
	space  content.VectorSpace
	recipe string
}

func (p storedVectorProvider) Descriptor() processing.IngestionDescriptor {
	return processing.IngestionDescriptor{Recipe: p.recipe, Producer: p.recipe, Spaces: []string{p.space.ID}, VectorSpaces: map[string]content.VectorSpace{p.space.ID: p.space}}
}
func (storedVectorProvider) SegmentAndEmbed(context.Context, string, string, content.Version, []string) ([]processing.PluginSegment, error) {
	return nil, errors.New("embedding provider disabled")
}

// The external object-store boundary holds both candidate uploads before SQL
// arbitration. This forces the race without sleeps or a production test seam.
type concurrentVectorBlobs struct {
	content.Blobs
	arrived chan struct{}
	release chan struct{}
}

func (b *concurrentVectorBlobs) Put(ctx context.Context, org string, raw []byte) (content.Blob, error) {
	blob, err := b.Blobs.Put(ctx, org, raw)
	if err == nil && bytes.HasPrefix(raw, []byte("QVEC")) {
		select {
		case b.arrived <- struct{}{}:
		default:
		}
		select {
		case <-b.release:
		case <-ctx.Done():
			return content.Blob{}, ctx.Err()
		}
	}
	return blob, err
}
