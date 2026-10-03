package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	s3store "github.com/The-Vibe-Company/quivr-v2/internal/adapters/s3"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/tei"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDurableEmbeddingConflictAndAtomicEnrichment(t *testing.T) {
	path := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if path == "" {
		t.Skip("make verify real adapters")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		DatabaseURL string         `json:"database_url"`
		TEIURL      string         `json:"tei_url"`
		S3          s3store.Config `json:"s3"`
	}
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if postgresOnly() {
		t.Skip("needs TEI and S3; runs inside make verify, not make adapter-postgres")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := contentStores(pool)
	service := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Baseline: store, Embeddings: store, Blobs: s3store.New(cfg.S3)}
	scope := corpus.Scope{Organization: "adapter-embeddings-" + strconv.FormatInt(time.Now().UnixNano(), 10), Actions: []string{"corpora:write", "content:write", "content:read", "search:query"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "e5", Name: "E5"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := service.Accept(ctx, scope, content.Command{Key: "e5", Source: content.Source{CorpusID: c.ID, Namespace: "adapter", RecordKey: "e5"}, Content: content.Text{Kind: "text", Text: "Les bateaux naviguent vers la Corse."}})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Materialize(ctx, scope.Organization, r.ID); err != nil {
		t.Fatal(err)
	}
	v, err := service.ProcessingVersion(ctx, scope.Organization, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	seg, err := wholeParts(scope.Organization, v)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.SaveSegmentation(ctx, scope.Organization, v, seg); err != nil {
		t.Fatal(err)
	}
	model := tei.Encoder{Endpoint: cfg.TEIURL}
	vector, err := model.Embed(ctx, "passage: "+seg.Segments[0].Text)
	if err != nil {
		t.Fatal(err)
	}
	g, err := store.Generation(ctx, scope.Organization, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The vectors of the space the Corpus's generation serves (the stack's core.ingest).
	space := content.VectorSpace{ID: g.SpaceID, Dimensions: len(vector)}
	if err = pool.QueryRow(ctx, `SELECT manifest FROM vector_spaces WHERE id=$1`, g.SpaceID).Scan(&space.Manifest); err != nil {
		t.Fatal(err)
	}
	input := content.EmbeddingInput(scope.Organization, c.ID, v, seg, seg.Segments[0], space, model.Producer())
	artifact, err := service.SaveEmbedding(ctx, input, space, vector)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.SaveEmbedding(ctx, input, space, vector)
	if err != nil || replay.ID != artifact.ID {
		t.Fatal("artifact replay", err)
	}
	loaded, recovered, err := service.LoadEmbedding(ctx, scope.Organization, input.DerivationID)
	if err != nil || loaded.ID != artifact.ID {
		t.Fatal("durable artifact", err)
	}
	original, _ := content.VectorBytes(vector)
	recoveredBytes, _ := content.VectorBytes(recovered)
	if string(original) != string(recoveredBytes) {
		t.Fatal("float32 bytes changed")
	}
	// A changed but normalized output under the same derivation must conflict.
	divergent := append([]float32(nil), vector...)
	divergent[0] = -divergent[0]
	if _, err = service.SaveEmbedding(ctx, input, space, divergent); !errors.Is(err, content.ErrConflict) {
		t.Fatal("divergence accepted", err)
	}
	if err = service.Promote(ctx, scope.Organization, seg, g); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_fixture_enrichment() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.organization LIKE 'adapter-embeddings-%' AND NEW.event_type='record.enrichment_available' THEN RAISE EXCEPTION 'synthetic enrichment interruption'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_fixture_enrichment BEFORE INSERT ON change_events FOR EACH ROW EXECUTE FUNCTION fail_fixture_enrichment()`)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), "DROP TRIGGER IF EXISTS fail_fixture_enrichment ON change_events; DROP FUNCTION IF EXISTS fail_fixture_enrichment()")
	if err = store.CommitEnrichment(ctx, scope.Organization, seg, g, []content.Embedding{artifact}); err == nil {
		t.Fatal("injection absent")
	}
	h, err := hydrateOne(ctx, store, scope, content.Candidate{SegmentID: seg.Segments[0].ID, GenerationID: g.ID})
	if err != nil || h.EmbeddingID != "" {
		t.Fatal("partial enrichment", h, err)
	}
	if _, err = pool.Exec(ctx, "DROP TRIGGER fail_fixture_enrichment ON change_events; DROP FUNCTION fail_fixture_enrichment()"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = store.CommitEnrichment(ctx, scope.Organization, seg, g, []content.Embedding{artifact}); err != nil {
			t.Fatal("enrichment retry failed", err)
		}
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM change_events WHERE organization=$1 AND event_type='record.enrichment_available'`, scope.Organization).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate enrichment event", count, err)
	}
	h, err = hydrateOne(ctx, store, scope, content.Candidate{SegmentID: seg.Segments[0].ID, GenerationID: g.ID})
	if err != nil || h.EmbeddingID != artifact.ID || h.SpaceID != g.SpaceID {
		t.Fatal("unpaired provenance", h, err)
	}
}

// Enrichment deadlines are counted per Version and survive the worker, so
// the bound on them holds across retries and restarts.
func TestEnrichmentTimeoutsCountPerVersion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := contentStores(rebuildAdapterPool(t, ctx))
	org := "adapter-timeouts-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	var got []int
	for _, version := range []string{"version_a", "version_a", "version_b", "version_a"} {
		n, err := store.CountEnrichmentTimeout(ctx, org, version)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	if want := []int{1, 2, 1, 3}; !slices.Equal(got, want) {
		t.Fatalf("counts %v, want %v", got, want)
	}
}
