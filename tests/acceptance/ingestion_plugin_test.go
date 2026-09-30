package acceptance

import (
	"fmt"
	"os"
	"testing"
	"time"
)

const (
	pluginServedSpace     = "example.hash_embedder.small@1"
	pluginEvaluationSpace = "example.hash_embedder.large@1"
)

// vectorSpaces reads a Corpus's vector spaces, keyed by vector_space_id, and
// the current segments its generation projects.
func vectorSpaces(t *testing.T, corpusID string) (map[string]map[string]any, float64) {
	t.Helper()
	list := request(t, "GET", "/v0/corpora/"+corpusID+"/vector-spaces", os.Getenv("QUIVR_TEST_ADMIN"), nil, 200)
	out := map[string]map[string]any{}
	for _, item := range list["items"].([]any) {
		space := item.(map[string]any)
		out[space["vector_space_id"].(string)] = space
	}
	return out, list["segments"].(float64)
}

func coverage(space map[string]any) float64 {
	return space["coverage"].(map[string]any)["segments"].(float64)
}

// The sample ingestion plugin (sdks/go/examples/hash-embedder) is pinned with
// its small space served and its large space for evaluation
// (scripts/ingestion_plugin.py). A Corpus built before the pin keeps the
// built-in space until it is rebuilt; the rebuild carries its Records onto the
// plugin's named spaces, both covered, and search then encodes queries with
// the plugin; a new Record goes through the plugin directly.
func TestIngestionPlugin(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" || os.Getenv("QUIVR_TEST_INGESTION_PLUGIN") == "" {
		t.Skip("make verify pins the sample ingestion plugin")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := fmt.Sprint(time.Now().UnixNano())
	corpusID := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Ingestion plugin", "idempotency_key": "ingestion-plugin-" + run}, 201)["corpus_id"].(string)
	before, _ := vectorSpaces(t, corpusID)
	if len(before) != 1 {
		t.Fatalf("a Corpus built before the pin lists one space: %v", before)
	}
	for id, space := range before {
		if space["role"] != "served" || space["owner"].(map[string]any)["kind"] != "engine" || id == pluginServedSpace {
			t.Fatalf("before the rebuild the built-in space serves: %v", before)
		}
	}
	harbour := ingestEnriched(t, corpusID, "harbour-"+run, "harbour", "Dockers stopped work at dawn.\n\nThe harbour stayed closed all day.")

	_, location := postRebuild(t, admin, corpusID, "ingestion-plugin-"+run, 202)
	if done := awaitOperation(t, location); done["state"] != "succeeded" {
		t.Fatalf("rebuild onto the plugin's spaces: %v", done)
	}
	after, total := vectorSpaces(t, corpusID)
	served, evaluation := after[pluginServedSpace], after[pluginEvaluationSpace]
	if len(after) != 2 || served == nil || evaluation == nil || total < 2 {
		t.Fatalf("after the rebuild the plugin's two spaces: %v, %v segments", after, total)
	}
	owner := served["owner"].(map[string]any)
	if served["role"] != "served" || evaluation["role"] != "evaluation" || owner["kind"] != "plugin" || owner["plugin_id"] != "example.hash_embedder" || served["dimensions"].(float64) != 16 || evaluation["dimensions"].(float64) != 32 {
		t.Fatalf("plugin spaces %v %v", served, evaluation)
	}
	if coverage(served) != total || coverage(evaluation) != total {
		t.Fatalf("every segment holds a vector in both spaces: served %v, evaluation %v, of %v", coverage(served), coverage(evaluation), total)
	}
	semantic := func(query string) []map[string]any {
		t.Helper()
		items := request(t, "POST", "/v0/search", admin, map[string]any{"query": query, "corpus_ids": []string{corpusID}, "mode": "semantic"}, 200)["items"].([]any)
		hits := []map[string]any{}
		for _, item := range items {
			hits = append(hits, item.(map[string]any))
		}
		return hits
	}
	hits := semantic("harbour closed")
	if len(hits) == 0 || hits[0]["version_id"] != harbour || hits[0]["vector_space_id"] != pluginServedSpace {
		t.Fatalf("semantic search encoded by the plugin: %v", hits)
	}

	// A new Record goes through the plugin directly.
	vineyard := ingestEnriched(t, corpusID, "vineyard-"+run, "vineyard", "The vineyard harvest started early this year.")
	hits = semantic("vineyard harvest")
	if len(hits) == 0 || hits[0]["version_id"] != vineyard || hits[0]["vector_space_id"] != pluginServedSpace {
		t.Fatalf("a Record ingested through the plugin: %v", hits)
	}
	after, total = vectorSpaces(t, corpusID)
	if coverage(after[pluginServedSpace]) != total || coverage(after[pluginEvaluationSpace]) != total {
		t.Fatalf("coverage after a new Record: %v of %v", after, total)
	}
}
