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

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	s3store "github.com/The-Vibe-Company/quivr/internal/adapters/s3"
	"github.com/The-Vibe-Company/quivr/internal/adapters/tei"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
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
	// The cuts and vectors belong to the same serving owner, including on replay.
	var cuts []content.SegmentInput
	for _, part := range v.Manifest.Parts {
		if part.Content.Kind == "text" && part.Content.Text != "" {
			cuts = append(cuts, content.SegmentInput{PartKey: part.Key, End: len([]rune(part.Content.Text))})
		}
	}
	seg, err := content.PluginSegmentation(scope.Organization, v, "plugin:core.ingest@1.0.0", json.RawMessage(`{"plugin_id":"core.ingest"}`), cuts)
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
	// The same owner verifies atomic publication after compact conversion,
	// using real S3 bytes and the original public artifact identity.
	packed, err := service.PackEmbeddingGroup(ctx, seg, space, []content.EmbeddingData{{Artifact: artifact, Vector: vector}})
	if err != nil || len(packed) != 1 || packed[0].Artifact.ID != artifact.ID {
		t.Fatalf("compact conversion %v %+v", err, packed)
	}
	artifact = packed[0].Artifact
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

// A late embedding commit settles withdrawn work without attaching vectors
// or claiming that enrichment became available.
func TestWithdrawnEnrichmentCompletionSettlesProgress(t *testing.T) {
	for _, completion := range []string{"commit", "discard", "discard after cutover", "reconcile historical cuts"} {
		t.Run(completion, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			pool := adapterPool(t, ctx)
			org := "adapter-withdrawn-enrichment-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
			c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "withdraw", Name: "Withdrawal"})
			if err != nil {
				t.Fatal(err)
			}
			store := contentStores(pool)
			service := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Baseline: store, Embeddings: store}
			cmd := content.Command{Key: "source", Source: content.Source{CorpusID: c.ID, Namespace: "adapter", RecordKey: "source"}, Content: content.Text{Kind: "text", Text: "Late enrichment"}}
			r, err := service.Accept(ctx, scope, cmd)
			if err != nil {
				t.Fatal(err)
			}
			work, _, err := store.Work(ctx, org, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Publish(ctx, work, publication(content.Blob{Key: "fixture/t", SHA256: "withdrawn-text", Size: 15}, content.Blob{Key: "fixture/m", SHA256: "withdrawn-manifest", Size: 2})); err != nil {
				t.Fatal(err)
			}
			// Keep fixture blobs, which are not in S3, away from a live worker.
			if _, err = pool.Exec(ctx, `UPDATE ingestion_outbox SET dispatched=true WHERE organization=$1`, org); err != nil {
				t.Fatal(err)
			}
			v := content.Version{ID: work.VersionID, RecordID: work.RecordID, Manifest: content.ManifestFor(cmd)}
			seg, err := wholeParts(org, v)
			if err != nil {
				t.Fatal(err)
			}
			if err = service.SaveSegmentation(ctx, org, v, seg); err != nil {
				t.Fatal(err)
			}
			g, err := store.Generation(ctx, org, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = service.Promote(ctx, org, seg, g); err != nil {
				t.Fatal(err)
			}
			if err = service.EnrichmentProgress(ctx, org, v.ID, "running", ""); err != nil {
				t.Fatal(err)
			}
			if _, err = service.Withdraw(ctx, scope, content.Withdrawal{Key: "withdraw", Source: cmd.Source}); err != nil {
				t.Fatal(err)
			}
			a, p, _, err := store.VersionStatus(ctx, org, v.ID)
			if err != nil || p.State != "running" || p.Phase != "enrichment" || a.Searchable {
				t.Fatalf("withdrawn Version %s before enrichment completion: availability=%+v processing=%+v error=%v", v.ID, a, p, err)
			}
			if err = service.BlockEnrichment(ctx, org, v.ID, content.Diagnostic{Code: "pinned_plugin_unavailable", Message: "The pinned plugin was unavailable."}); err != nil {
				t.Fatal(err)
			}
			space := content.VectorSpace{ID: g.SpaceID}
			if err = pool.QueryRow(ctx, `SELECT manifest FROM vector_spaces WHERE id=$1`, space.ID).Scan(&space.Manifest); err != nil {
				t.Fatal(err)
			}
			artifact := content.Embedding{Organization: org, ID: content.StableID("embedding", v.ID), DerivationID: content.StableID("derivation", v.ID), SegmentID: seg.Segments[0].ID, SpaceID: space.ID}
			if err = store.SaveEmbedding(ctx, artifact, space); err != nil {
				t.Fatal(err)
			}
			if completion == "discard after cutover" {
				live, err := plugins.NewLive("withdrawn-plan", nil)
				if err != nil {
					t.Fatal(err)
				}
				ctx, err = live.Pin(ctx, plugins.Work{Kind: plugins.WorkIngestion, Organization: org, ID: r.ID, Plan: "withdrawn-plan"}, nil, 1)
				if err != nil {
					t.Fatal(err)
				}
				// Route this Corpus to a new owner while its old pinned work finishes.
				generation := content.StableID("generation", org)
				if _, err = pool.Exec(ctx, `INSERT INTO projection_generations(id,collection,profile_version,space_id,source_namespace_projected,spaces,spaces_projected,ingestion_routing)
SELECT $1,collection,profile_version,space_id,source_namespace_projected,spaces,spaces_projected,'{"default":"example.replacement"}'::jsonb FROM projection_generations WHERE id=$2`, generation, g.ID); err != nil {
					t.Fatal(err)
				}
				if _, err = pool.Exec(ctx, `INSERT INTO corpus_projection_routes(organization,corpus_id,generation_id) VALUES($1,$2,$3)`, org, c.ID, generation); err != nil {
					t.Fatal(err)
				}
			}
			if completion == "reconcile historical cuts" {
				// The incoming snapshot belongs to the same owner but older cuts;
				// the record was withdrawn after the caller's eligibility read.
				g.IngestionRouting = &content.IngestionRouting{Default: "adapter.fixture"}
				g.Spaces = []content.GenerationSpace{{ID: g.SpaceID, OwnerPluginID: "adapter.fixture", Role: content.SpaceServed}}
				historical, err := content.PluginSegmentation(org, v, "plugin:adapter.fixture@0", seg.Provenance, []content.SegmentInput{{PartKey: v.Manifest.Parts[0].Key, End: len([]rune(v.Manifest.Parts[0].Content.Text))}})
				if err != nil {
					t.Fatal(err)
				}
				if err = service.SaveSegmentation(ctx, org, v, historical); err != nil {
					t.Fatal(err)
				}
				seg = historical
			}
			for i := 0; i < 2; i++ {
				if completion == "reconcile historical cuts" {
					var publish bool
					publish, err = service.ReconcileServingEnrichment(ctx, org, seg, g)
					if publish {
						t.Fatal("withdrawn historical cuts must not publish vectors")
					}
				} else if completion == "commit" {
					err = store.CommitEnrichment(ctx, org, seg, g, []content.Embedding{artifact})
				} else {
					err = service.EnrichmentProgress(ctx, org, v.ID, "idle", "")
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			a, p, code, err := store.VersionStatus(ctx, org, v.ID)
			if err != nil || p.State != "idle" || p.Phase != "" || code != "" || a.Current || a.Searchable {
				t.Fatalf("withdrawn Version %s after enrichment completion: availability=%+v processing=%+v code=%q error=%v; want idle without phase or diagnostic and no search eligibility", v.ID, a, p, code, err)
			}
			stored, err := store.Version(ctx, org, v.RecordID, v.ID)
			if err != nil || len(stored.Diagnostics) != 0 || stored.Steps.Enriched != nil {
				t.Fatalf("withdrawn Version %s after enrichment completion: %+v error=%v; want idle without phase, diagnostic or enrichment publication", v.ID, stored, err)
			}
			var coverage, events int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM embedding_coverage WHERE organization=$1`, org).Scan(&coverage); err != nil {
				t.Fatal(err)
			}
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM change_events WHERE organization=$1 AND event_type='record.enrichment_available'`, org).Scan(&events); err != nil || coverage != 0 || events != 0 {
				t.Fatalf("withdrawn enrichment published coverage=%d events=%d error=%v; want neither", coverage, events, err)
			}
		})
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
