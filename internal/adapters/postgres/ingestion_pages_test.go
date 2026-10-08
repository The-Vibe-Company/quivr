package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/app"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/processing"
	"github.com/The-Vibe-Company/quivr/internal/quarantine"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

type pageIngestor struct {
	descriptor processing.IngestionDescriptor
	failAt     int
	calls      []int
}

func (p *pageIngestor) Descriptor() processing.IngestionDescriptor { return p.descriptor }
func (p *pageIngestor) SegmentAndEmbed(context.Context, string, string, content.Version, []string) ([]processing.PluginSegment, error) {
	return nil, errors.New("paged ingestion fell back to an unbounded call")
}
func (p *pageIngestor) SegmentAndEmbedPage(_ context.Context, _, _ string, v content.Version, spaces []string, cursor json.RawMessage) (processing.PluginPage, error) {
	start := 0
	if len(cursor) > 0 {
		if err := json.Unmarshal(cursor, &start); err != nil {
			return processing.PluginPage{}, err
		}
	}
	p.calls = append(p.calls, start)
	if start == p.failAt {
		return processing.PluginPage{}, errors.New("provider unavailable")
	}
	vectors := map[string][]float32{}
	for _, s := range spaces {
		vectors[s] = []float32{1, 0, 0, 0}
	}
	page := processing.PluginPage{Segments: []processing.PluginSegment{{SegmentInput: content.SegmentInput{PartKey: v.Manifest.Parts[0].Key, Start: start, End: start + 2}, Vectors: vectors}}}
	if start+2 < 10 {
		page.Next, _ = json.Marshal(start + 2)
	}
	return page, nil
}

// The real store owns restart durability: negotiated passages are never
// recalculated and completeness is published only after the last page.
func TestPagedIngestionResumesCommittedPassages(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	store := contentStores(pool)
	manifest, err := os.ReadFile("../../../contracts/plugins/v0/fixtures/manifests/valid/ingestion-paged.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pin, err := plugins.LoadPinManifest(manifest, "paged", plugins.PinConfig{Endpoint: "http://127.0.0.1:9"})
	if err != nil {
		t.Fatal(err)
	}
	set, err := plugins.NewPinSet([]*plugins.Pin{pin})
	if err != nil {
		t.Fatal(err)
	}
	if err = app.BootstrapDatabase(ctx, pool, app.Config{}.DeploymentSpaces(set)); err != nil {
		t.Fatal(err)
	}

	descriptor := (pluginhttp.Ingestor{Pin: pin}).Descriptor()
	descriptor.Paged = true
	descriptor.SegmentsOnly = false
	space := descriptor.Spaces[0]
	scope := corpus.Scope{Organization: "paged-ingestion", Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "one", Name: "Pages"})
	if err != nil {
		t.Fatal(err)
	}
	contents := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Baseline: store, Embeddings: store, Blobs: &objectMemory{objects: map[string][]byte{}}}
	receipt, err := contents.Accept(ctx, scope, content.Command{Key: "one", Source: content.Source{CorpusID: c.ID, Namespace: "docs", RecordKey: "one"}, Content: content.Text{Kind: "text", Text: "abcdefghij"}})
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
	g := content.Generation{SpaceID: space}
	first := &pageIngestor{descriptor: descriptor, failAt: 2}
	if _, _, err = (processing.PluginDeriver{Content: contents, Plugin: first}).Derive(ctx, scope.Organization, c.ID, v, g); err == nil {
		t.Fatal("outage did not interrupt the unfinished item")
	}
	var inputKey string
	if err = pool.QueryRow(ctx, `SELECT spaces_key FROM ingestion_pages WHERE organization=$1 AND version_id=$2`, scope.Organization, v.ID).Scan(&inputKey); err != nil {
		t.Fatal(err)
	}
	pages := func(key string, want int) {
		t.Helper()
		var got int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM ingestion_pages WHERE organization=$1 AND version_id=$2 AND recipe=$3 AND spaces_key=$4`, scope.Organization, v.ID, descriptor.Recipe, key).Scan(&got); err != nil || got != want {
			t.Fatalf("saved pages for input %s: got=%d want=%d err=%v", key, got, want, err)
		}
	}
	pages(inputKey, 1)

	sweeper := processing.IngestionPageSweeper{Store: store, Content: contents, Batch: 2}
	sweep := func() {
		t.Helper()
		n, err := sweeper.Sweep(ctx)
		if err != nil || n > 2 {
			t.Fatalf("bounded page sweep: deleted=%d err=%v", n, err)
		}
	}

	// Normalization can replace the source Manifest before complete segments
	// exist while retaining the accepted Version ID. A new source must negotiate
	// its own page zero; identical text with a different Part key is also new.
	for _, change := range []func(*content.Version){
		func(v *content.Version) { v.Manifest.Parts[0].Content.Text = "klmnopqrst" },
		func(v *content.Version) { v.Manifest.Parts[0].Key = "renamed" },
	} {
		replacement := v
		replacement.Manifest.Parts = append([]content.Part(nil), v.Manifest.Parts...)
		change(&replacement)
		renegotiated := &pageIngestor{descriptor: descriptor, failAt: 2}
		if _, _, err := (processing.PluginDeriver{Content: contents, Plugin: renegotiated}).Derive(ctx, scope.Organization, c.ID, replacement, g); err == nil {
			t.Fatal("unfinished replacement unexpectedly published")
		}
		if len(renegotiated.calls) != 2 || renegotiated.calls[0] != 0 {
			t.Fatalf("replacement source reused old page: calls=%v", renegotiated.calls)
		}
	}
	// Refuse the first artifact write after segmentation commits. A restart
	// must retain every negotiated page until vectors are durable too.
	if _, err = pool.Exec(ctx, `CREATE FUNCTION refuse_page_vectors() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'vector write interrupted'; END $$;
CREATE TRIGGER refuse_page_vectors BEFORE INSERT ON embedding_files FOR EACH ROW EXECUTE FUNCTION refuse_page_vectors()`); err != nil {
		t.Fatal(err)
	}
	vectorInterrupted := &pageIngestor{descriptor: descriptor, failAt: -1}
	if _, _, err = (processing.PluginDeriver{Content: contents, Plugin: vectorInterrupted}).Derive(ctx, scope.Organization, c.ID, v, g); err == nil {
		t.Fatal("vector interruption unexpectedly succeeded")
	}
	if _, err = store.StoredSegmentation(ctx, scope.Organization, v.ID, descriptor.Recipe); err != nil {
		t.Fatalf("segmentation was not durable before vector interruption: %v", err)
	}
	pages(inputKey, 5)
	sweep()
	pages(inputKey, 5)
	if len(vectorInterrupted.calls) != 4 || vectorInterrupted.calls[0] != 2 {
		t.Fatalf("committed provider cut was called again: %v", vectorInterrupted.calls)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER refuse_page_vectors ON embedding_files; DROP FUNCTION refuse_page_vectors()`); err != nil {
		t.Fatal(err)
	}
	// A crash after the vector commit but before page deletion must be healed
	// by the cached-complete path, without renegotiating provider output.
	if _, err = pool.Exec(ctx, `CREATE FUNCTION refuse_page_cleanup() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'cleanup interrupted'; END $$;
CREATE TRIGGER refuse_page_cleanup BEFORE DELETE ON ingestion_pages FOR EACH ROW EXECUTE FUNCTION refuse_page_cleanup()`); err != nil {
		t.Fatal(err)
	}
	restarted := &pageIngestor{descriptor: descriptor, failAt: -1}
	seg, data, err := (processing.PluginDeriver{Content: contents, Plugin: restarted}).Derive(ctx, scope.Organization, c.ID, v, g)
	if err == nil {
		t.Fatal("cleanup interruption unexpectedly succeeded")
	}
	pages(inputKey, 5)
	if _, err = pool.Exec(ctx, `CREATE TABLE orphan_pages AS SELECT * FROM ingestion_pages WHERE organization=$1 AND version_id=$2 AND spaces_key=$3`, scope.Organization, v.ID, inputKey); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER refuse_page_cleanup ON ingestion_pages; DROP FUNCTION refuse_page_cleanup()`); err != nil {
		t.Fatal(err)
	}
	if _, err = (processing.PluginDeriver{Content: contents, Plugin: restarted}).Segment(ctx, scope.Organization, c.ID, v, g); err != nil {
		t.Fatalf("cached baseline did not retry cleanup: %v", err)
	}
	if len(seg.Segments) != 5 || len(data) != 5 || seg.Segments[4].Start != 8 || seg.Segments[4].End != 10 {
		t.Fatalf("incomplete coverage: segments=%+v vectors=%d", seg.Segments, len(data))
	}
	if len(restarted.calls) != 0 {
		t.Fatalf("committed provider cut was called again: %v", restarted.calls)
	}
	pages(inputKey, 0)
	// A provider already in flight may finish after cleanup. Its response is
	// usable by its caller, but must not resurrect completed durable pages.
	if _, err = store.SaveIngestionPage(ctx, scope.Organization, v.ID, descriptor.Recipe, inputKey, 0, content.IngestionPage{Segments: json.RawMessage(`[]`)}); err != nil {
		t.Fatal(err)
	}
	pages(inputKey, 0)
	var unrelated int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ingestion_pages WHERE organization=$1 AND version_id=$2 AND spaces_key!=$3`, scope.Organization, v.ID, inputKey).Scan(&unrelated); err != nil || unrelated != 2 {
		t.Fatalf("cleanup touched interrupted replacement sources: pages=%d err=%v", unrelated, err)
	}
	third := &pageIngestor{descriptor: descriptor, failAt: 0}
	if _, again, err := (processing.PluginDeriver{Content: contents, Plugin: third}).Derive(ctx, scope.Organization, c.ID, v, g); err != nil || len(again) != 5 || len(third.calls) != 0 {
		t.Fatalf("stored derivation not reused: vectors=%d calls=%v err=%v", len(again), third.calls, err)
	}

	// Simulate pages left by a previous worker that did not persist completion,
	// including a namespace missing its final page. The exact input hash and
	// every requested vector still prove this source finished durably.
	for _, missingFinal := range []bool{false, true} {
		if _, err = pool.Exec(ctx, `DELETE FROM ingestion_page_completions WHERE organization=$1 AND version_id=$2 AND recipe=$3 AND spaces_key=$4`, scope.Organization, v.ID, descriptor.Recipe, inputKey); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO ingestion_pages SELECT * FROM orphan_pages WHERE NOT $1 OR page_number<>4`, missingFinal); err != nil {
			t.Fatal(err)
		}
		drained := false
		for attempt := 0; attempt < 20; attempt++ {
			sweep()
			var remaining int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM ingestion_pages WHERE organization=$1 AND version_id=$2 AND spaces_key=$3`, scope.Organization, v.ID, inputKey).Scan(&remaining); err != nil {
				t.Fatal(err)
			}
			if remaining == 0 {
				drained = true
				break
			}
		}
		if !drained {
			t.Fatal("orphan pages did not drain through bounded sweeps")
		}
	}
	var replacementKey string
	if err = pool.QueryRow(ctx, `SELECT spaces_key FROM ingestion_pages WHERE organization=$1 AND version_id=$2 ORDER BY spaces_key LIMIT 1`, scope.Organization, v.ID).Scan(&replacementKey); err != nil {
		t.Fatal(err)
	}
	sweep()
	pages(replacementKey, 1)

	// Publish through the real processing/retrieval lifecycle, then hold this
	// formerly searchable and enriched Version during a generation rebuild.
	projection := &rebuildPublication{}
	indexer := retrieval.Service{Content: contents, Routing: store, Projection: projection}
	processor := processing.Service{Content: contents, Routing: store,
		Plugin: &processing.PluginDeriver{Content: contents, Plugin: third}, Retrieval: indexer, Enrichment: indexer}
	if err = processor.Run(ctx, scope.Organization, receipt.ID); err != nil {
		t.Fatal(err)
	}
	if err = processor.Enrich(ctx, scope.Organization, receipt.ID); err != nil {
		t.Fatal(err)
	}
	op, err := store.AcceptRebuild(ctx, scope.Organization, c.ID, "rebuild", []byte(`{"idempotency_key":"rebuild"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginRebuild(ctx, scope.Organization, op.ID); err != nil {
		t.Fatal(err)
	}
	reason := content.Diagnostic{Code: "ingestion_refused", Message: "Item requires recovery.", Plugin: pin.Manifest.ID, Contribution: "ingestion"}
	if err = store.QuarantineRebuild(ctx, scope.Organization, op.ID, v.ID, reason); err != nil {
		t.Fatal(err)
	}
	if complete, err := store.ActivateRebuild(ctx, scope.Organization, op.ID); err != nil || !complete {
		t.Fatalf("held item blocked cutover: complete=%v err=%v", complete, err)
	}
	held, err := contents.Version(ctx, scope, receipt.RecordID, receipt.VersionID)
	if err != nil || held.Availability.State != "quarantined" {
		t.Fatalf("held version is searchable: %+v %v", held.Availability, err)
	}
	activeReprocessPlan(t, ctx, pool)
	admin := scope
	admin.Actions = append(admin.Actions, operations.BackfillPermission)
	q := quarantine.Service{Store: store}
	request := quarantine.Request{Key: "repair", Filter: quarantine.Filter{CorpusID: c.ID}, DryRun: true}
	if estimate, _, err := q.Request(ctx, admin, request); err != nil || estimate.Versions != 1 {
		t.Fatalf("recovery estimate: %+v %v", estimate, err)
	}
	request.DryRun = false
	_, recovery, err := q.Request(ctx, admin, request)
	if err != nil {
		t.Fatal(err)
	}
	reprocessor := quarantine.Reprocessor{Store: store, Cancellation: store, Publisher: contents, Processor: processor, Settings: quarantine.Settings{Rate: 25}}
	if progress, err := reprocessor.Step(ctx, scope.Organization, recovery.ID); err != nil || !progress.Done {
		t.Fatalf("recovery unfinished: %+v %v", progress, err)
	}
	recovered, err := contents.Version(ctx, scope, receipt.RecordID, receipt.VersionID)
	if err != nil || recovered.Availability.State != "retrieval_ready" {
		t.Fatalf("recovery not searchable: %+v %v", recovered.Availability, err)
	}
	active, err := store.Generation(ctx, scope.Organization, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	candidates := make([]content.Candidate, len(seg.Segments))
	for i, segment := range seg.Segments {
		candidates[i] = content.Candidate{SegmentID: segment.ID, GenerationID: active.ID}
	}
	located, err := store.Hydrate(ctx, scope, candidates)
	if err != nil {
		t.Fatal(err)
	}
	covered := 0
	for _, item := range located {
		if item.EmbeddingID != "" {
			covered++
		}
	}

	if covered != 5 || projection.vectors != 5 || projection.segmentation.Segments[4].End != 10 || len(third.calls) != 0 {
		t.Fatalf("recovery lost active coverage: covered=%d projected=%d calls=%v", covered, projection.vectors, third.calls)
	}

	if _, err = contents.Withdraw(ctx, scope, content.Withdrawal{Key: "withdraw", Source: content.Source{CorpusID: c.ID, Namespace: "docs", RecordKey: "one"}}); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 20; attempt++ {
		sweep()
	}
	if _, err = store.SaveIngestionPage(ctx, scope.Organization, v.ID, descriptor.Recipe, inputKey, 0, content.IngestionPage{Segments: json.RawMessage(`[]`)}); err != nil {
		t.Fatal(err)
	}
	var retained int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM ingestion_pages WHERE organization=$1 AND version_id=$2)+(SELECT count(*) FROM ingestion_page_completions WHERE organization=$1 AND version_id=$2)`, scope.Organization, v.ID).Scan(&retained); err != nil || retained != 0 {
		t.Fatalf("dead Version retained pages/completions=%d err=%v", retained, err)
	}

}
