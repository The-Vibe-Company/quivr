package weaviate_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// legacyIdentity is the object id the adapter derives from a stable name.
// The seeder repeats the derivation so that the persisted objects are the
// ones an adapter wrote before the upgrade.
func legacyIdentity(prefix string, values ...string) string {
	h := content.Hash([]byte(content.StableID(prefix, values...)))
	return h[:8] + "-" + h[8:12] + "-5" + h[13:16] + "-a" + h[17:20] + "-" + h[20:32]
}

// seedLegacyProjection writes, over plain REST, the collection and the
// objects the adapter wrote on the oldest supported server: the client
// library the adapter now uses cannot talk to that server. Each segment gets
// its lexical anchor and one enriched object carrying its vector. It returns
// every object's identity, by segment.
func seedLegacyProjection(t *testing.T, idx index, g content.Generation, org, corpusID string, texts map[string]string, vectors map[string][]float32) map[string][]string {
	t.Helper()
	properties := []any{}
	for _, name := range []string{"organization", "corpusId", "generationId", "segmentId", "versionId", "segmentationId", "projectionPlugin", "sourceMediaType", "sourceNamespace"} {
		properties = append(properties, map[string]any{"name": name, "dataType": []string{"text"}, "tokenization": "field", "indexFilterable": true, "indexSearchable": false})
	}
	for _, name := range []string{"title", "body", "lexicalText"} {
		properties = append(properties, map[string]any{"name": name, "dataType": []string{"text"}, "tokenization": "word", "indexSearchable": true})
	}
	vector := map[string]any{"vectorizer": map[string]any{"none": nil}, "vectorIndexType": "hnsw", "vectorIndexConfig": map[string]any{"distance": "cosine", "rq": map[string]any{"enabled": true, "bits": 8, "rescoreLimit": content.DefaultVectorIndex.RescoreLimit}}}
	class := map[string]any{"class": g.Collection, "properties": properties, "vectorConfig": map[string]any{"semantic_text_v1": vector}, "invertedIndexConfig": map[string]any{"stopwords": map[string]any{"preset": "none"}}, "replicationConfig": map[string]any{"factor": 1}}
	if status := idx.rest(t, http.MethodPost, "/v1/schema", class, nil); status != http.StatusOK {
		t.Fatalf("create the collection: HTTP %d", status)
	}
	ids := map[string][]string{}
	var objects []any
	for segment, text := range texts {
		raw, err := content.VectorBytes(vectors[segment])
		if err != nil {
			t.Fatal(err)
		}
		lexical := map[string]any{"organization": org, "corpusId": corpusID, "generationId": g.ID, "versionId": "version-" + segment, "segmentationId": "seg-" + segment, "segmentId": segment, "sourceNamespace": "example-feed", "body": text, "title": ""}
		anchor, enriched := legacyIdentity("projection", org, g.ID, segment), legacyIdentity("projection-embedding", org, g.ID, segment, content.Hash(raw))
		objects = append(objects,
			map[string]any{"class": g.Collection, "id": anchor, "properties": lexical},
			map[string]any{"class": g.Collection, "id": enriched, "properties": lexical, "vectors": map[string]any{"semantic_text_v1": vectors[segment]}})
		ids[segment] = []string{anchor, enriched}
	}
	var results []struct {
		ID     string `json:"id"`
		Result struct {
			Status string `json:"status"`
			Errors any    `json:"errors"`
		} `json:"result"`
	}
	if status := idx.rest(t, http.MethodPost, "/v1/batch/objects", map[string]any{"objects": objects, "fields": []string{"id"}}, &results); status != http.StatusOK || len(results) != len(objects) {
		t.Fatalf("write the objects: HTTP %d, %d results for %d objects", status, len(results), len(objects))
	}
	for _, r := range results {
		if r.Result.Status != "SUCCESS" || r.Result.Errors != nil {
			t.Fatalf("write object %s: %+v", r.ID, r.Result)
		}
	}
	return ids
}

// persistedObject is one object as the index's REST API returns it.
type persistedObject struct {
	Properties map[string]any       `json:"properties"`
	Vectors    map[string][]float32 `json:"vectors"`
	Updated    int64                `json:"lastUpdateTimeUnix"`
}

func readPersisted(t *testing.T, idx index, collection, id string) persistedObject {
	t.Helper()
	var stored persistedObject
	if status := idx.rest(t, http.MethodGet, "/v1/objects/"+collection+"/"+id+"?include=vector", nil, &stored); status != http.StatusOK {
		t.Fatalf("read persisted object %s: status %d", id, status)
	}
	return stored
}

// The lifecycle driver owns provisioning time; each invocation only exercises
// a ready server, within the adapter assertion budget. The seed runs on a
// server older than the client library supports, so it writes over REST what
// the adapter wrote there; verification on the newer servers goes through the
// adapter and, unlike fresh-collection tests, never bootstraps a schema or
// republishes an object.
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
	idx := realIndex(t)
	var snapshot struct {
		Generation             content.Generation
		Organization, CorpusID string
		Objects                map[string][]storedObject
	}
	texts := map[string]string{"beacon": "cobalt beacon", "meadow": "ochre meadow"}
	basis := func(segment string) []float32 {
		vector := make([]float32, 384)
		vector[map[string]int{"beacon": 0, "meadow": 1}[segment]] = 1
		return vector
	}
	if phase == "seed" {
		snapshot.Generation = content.Generation{ID: "generation-attach", Collection: "QuivrAttach" + strconv.FormatInt(time.Now().UnixNano(), 36), ProfileVersion: retrieval.ProfileVersion, SpaceID: "space-attach"}
		snapshot.Organization, snapshot.CorpusID = "adapter-attach", "corpus-attach"
		ids := seedLegacyProjection(t, idx, snapshot.Generation, snapshot.Organization, snapshot.CorpusID, texts, map[string][]float32{"beacon": basis("beacon"), "meadow": basis("meadow")})
		snapshot.Objects = map[string][]storedObject{}
		for segment, written := range ids {
			var objects []storedObject
			for _, id := range written {
				stored := readPersisted(t, idx, snapshot.Generation.Collection, id)
				objects = append(objects, storedObject{ID: id, Updated: strconv.FormatInt(stored.Updated, 10), Vector: len(stored.Vectors["semantic_text_v1"]) > 0})
			}
			sort.Slice(objects, func(i, j int) bool { return objects[i].ID < objects[j].ID })
			if lexical, enriched := shape(objects); lexical != 1 || enriched != 1 {
				t.Fatalf("seed %s: want one anchor and vector object, got %v", segment, objects)
			}
			snapshot.Objects[segment] = objects
		}
	} else {
		b, err := os.ReadFile(snapshotPath)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(b, &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.Generation.Collection == "" || len(snapshot.Objects) != 2 {
			t.Fatal("incomplete persisted upgrade fixture")
		}
	}
	g, org, corpusID := snapshot.Generation, snapshot.Organization, snapshot.CorpusID
	for segment, want := range snapshot.Objects {
		for _, object := range want {
			stored := readPersisted(t, idx, g.Collection, object.ID)
			for key, value := range map[string]string{"organization": org, "corpusId": corpusID, "generationId": g.ID, "segmentId": segment, "versionId": "version-" + segment, "body": texts[segment]} {
				if stored.Properties[key] != value {
					t.Fatalf("persisted %s property %s: want %q, got %v", object.ID, key, value, stored.Properties[key])
				}
			}
			if object.Vector && !reflect.DeepEqual(stored.Vectors["semantic_text_v1"], basis(segment)) {
				t.Fatalf("persisted %s named vector changed: want basis vector for %s, got %v", object.ID, segment, stored.Vectors)
			}
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
		return
	}

	f := &attachFixture{t: t, ctx: ctx, index: idx, raw: idx.client(t), store: idx.open(t), gen: g, org: org, corpusID: corpusID}
	for segment, want := range snapshot.Objects {
		if got := f.objects(g, segment); !reflect.DeepEqual(got, want) {
			t.Fatalf("persisted %s objects changed: want %v, got %v", segment, want, got)
		}
	}
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
		q := retrieval.Request{Query: check.query, Mode: check.mode, Profile: "default", Limit: 10, CorpusIDs: []string{corpusID}, Vector: basis("beacon")}
		if check.mode == "hybrid" {
			q.Hybrid = &retrieval.HybridOptions{Alpha: check.alpha, Fusion: retrieval.FusionRelativeScore}
		}
		got, err := f.store.Search(ctx, []retrieval.Route{{CorpusID: corpusID, Generation: g}}, corpus.Scope{Organization: org, Corpora: []string{"*"}}, q)
		if err != nil {
			t.Fatalf("%s persisted search: %v", check.mode, err)
		}
		if len(got) == 0 || got[0].SegmentID != check.want || got[0].GenerationID != g.ID {
			t.Fatalf("%s alpha %g: want original %s first in generation %s, got %v", check.mode, check.alpha, check.want, g.ID, got)
		}
	}
}
