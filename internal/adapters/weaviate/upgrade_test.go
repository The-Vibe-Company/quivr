package weaviate_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// The lifecycle driver owns provisioning time; each invocation only exercises
// a ready server, within the adapter assertion budget. Unlike fresh-collection
// tests, verification never bootstraps a schema or republishes an object.
func TestPersistedProjectionAcrossWeaviateUpgrade(t *testing.T) {
	phase := os.Getenv("QUIVR_WEAVIATE_UPGRADE_PHASE")
	if phase == "" {
		t.Skip("scripts/weaviate_upgrade.py owns the old-volume lifecycle")
	}
	if phase != "seed" && phase != "verify" {
		t.Fatalf("invalid upgrade phase %q", phase)
	}
	snapshotPath := os.Getenv("QUIVR_WEAVIATE_UPGRADE_SNAPSHOT")
	if snapshotPath == "" || os.Getenv("QUIVR_ADAPTER_CONFIG") == "" {
		t.Fatal("upgrade lifecycle requires adapter config and snapshot paths")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var snapshot struct {
		Generation             content.Generation
		Organization, CorpusID string
		Objects                map[string][]storedObject
	}
	var f *attachFixture
	if phase == "seed" {
		f = newAttachFixture(t)
		f.ctx = ctx
		for i, text := range []string{"cobalt beacon", "ochre meadow"} {
			id := []string{"beacon", "meadow"}[i]
			f.publish(f.gen, f.segmentation(id, text))
			vector := make([]float32, 384)
			vector[i] = 1
			if err := f.store.PublishEmbeddings(ctx, f.gen, f.org, []content.EmbeddingData{f.embedding(f.gen, id, vector)}); err != nil {
				t.Fatal(err)
			}
		}
		snapshot.Generation, snapshot.Organization, snapshot.CorpusID = f.gen, f.org, f.corpusID
		snapshot.Objects = map[string][]storedObject{}
		for _, id := range []string{"beacon", "meadow"} {
			objects := f.objects(f.gen, id)
			if lexical, enriched := shape(objects); lexical != 1 || enriched != 1 {
				t.Fatalf("seed %s: want one anchor and vector object, got %v", id, objects)
			}
			snapshot.Objects[id] = objects
		}
	} else {
		read := func(path string, out any) {
			t.Helper()
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(b, out); err != nil {
				t.Fatal(err)
			}
		}
		read(snapshotPath, &snapshot)
		var config struct {
			WeaviateURL string `json:"weaviate_url"`
		}
		read(os.Getenv("QUIVR_ADAPTER_CONFIG"), &config)
		if snapshot.Generation.Collection == "" || len(snapshot.Objects) != 2 || config.WeaviateURL == "" {
			t.Fatal("incomplete persisted upgrade fixture")
		}
		f = &attachFixture{t: t, ctx: ctx, url: config.WeaviateURL, store: weaviate.New(config.WeaviateURL), gen: snapshot.Generation, org: snapshot.Organization, corpusID: snapshot.CorpusID}
	}
	for id, want := range snapshot.Objects {
		if got := f.objects(f.gen, id); !reflect.DeepEqual(got, want) {
			t.Fatalf("persisted %s objects changed: want %v, got %v", id, want, got)
		}
		for _, object := range want {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url+"/v1/objects/"+f.gen.Collection+"/"+object.ID+"?include=vector", nil)
			if err != nil {
				t.Fatal(err)
			}
			res, err := f.store.Client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var stored struct {
				Properties map[string]any       `json:"properties"`
				Vectors    map[string][]float32 `json:"vectors"`
			}
			err = json.NewDecoder(res.Body).Decode(&stored)
			res.Body.Close()
			if err != nil || res.StatusCode != http.StatusOK {
				t.Fatalf("read persisted object %s: status %d, %v", object.ID, res.StatusCode, err)
			}
			for key, value := range map[string]string{"organization": f.org, "corpusId": f.corpusID, "generationId": f.gen.ID, "segmentId": id, "versionId": "version-" + id, "body": map[string]string{"beacon": "cobalt beacon", "meadow": "ochre meadow"}[id]} {
				if stored.Properties[key] != value {
					t.Fatalf("persisted %s property %s: want %q, got %v", object.ID, key, value, stored.Properties[key])
				}
			}
			if object.Vector {
				wantVector := make([]float32, 384)
				wantVector[map[string]int{"beacon": 0, "meadow": 1}[id]] = 1
				if !reflect.DeepEqual(stored.Vectors["semantic_text_v1"], wantVector) {
					t.Fatalf("persisted %s named vector changed: want basis vector for %s, got %v", object.ID, id, stored.Vectors)
				}
			}
		}
	}
	vector := make([]float32, 384)
	vector[0] = 1
	for _, check := range []struct {
		mode, query, want string
		alpha             float64
	}{
		{"lexical", "cobalt", "beacon", 0},
		{"semantic", "meadow", "beacon", 0},
		{"hybrid", "meadow", "beacon", 0.9},
		{"hybrid", "meadow", "meadow", 0.1},
	} {
		// Opposing sparse/dense matches require both hybrid contributions to survive.
		q := retrieval.Request{Query: check.query, Mode: check.mode, Profile: "default", Limit: 10, CorpusIDs: []string{f.corpusID}, Vector: vector}
		if check.mode == "hybrid" {
			q.Hybrid = &retrieval.HybridOptions{Alpha: check.alpha, Fusion: retrieval.FusionRelativeScore}
		}
		got, err := f.store.Search(ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: f.gen}}, corpus.Scope{Organization: f.org, Corpora: []string{"*"}}, q)
		if err != nil {
			t.Fatalf("%s persisted search: %v", check.mode, err)
		}
		if len(got) == 0 || got[0].SegmentID != check.want || got[0].GenerationID != f.gen.ID {
			t.Fatalf("%s alpha %g: want original %s first in generation %s, got %v", check.mode, check.alpha, check.want, f.gen.ID, got)
		}
	}

	if phase == "seed" {
		b, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(snapshotPath, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
