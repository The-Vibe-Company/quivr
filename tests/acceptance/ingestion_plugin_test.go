package acceptance

import (
	"os"
	"testing"
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

const coreIngestSpace = "core.ingest.e5-small@1"

// ingestionPluginCorpus is the Corpus both phases of the ingestion plugin
// scenario use, named by the run scripts/ingestion_plugin.py gives them.
func ingestionPluginCorpus(t *testing.T) (string, string) {
	t.Helper()
	if os.Getenv("QUIVR_TEST_URL") == "" || os.Getenv("QUIVR_TEST_INGESTION_PLUGIN") == "" {
		t.Skip("make verify pins the sample ingestion plugin")
	}
	run := os.Getenv("QUIVR_TEST_INGESTION_RUN")
	return request(t, "POST", "/v0/corpora", os.Getenv("QUIVR_TEST_ADMIN"), map[string]any{"name": "Ingestion plugin", "idempotency_key": "ingestion-plugin-" + run}, 201)["corpus_id"].(string), run
}

// Before the swap the stack's own core.ingest serves the Corpus.
func TestIngestionPluginBefore(t *testing.T) {
	corpusID, run := ingestionPluginCorpus(t)
	spaces, _ := vectorSpaces(t, corpusID)
	owner, _ := spaces[coreIngestSpace]["owner"].(map[string]any)
	if len(spaces) != 1 || spaces[coreIngestSpace]["role"] != "served" || owner["plugin_id"] != "core.ingest" {
		t.Fatalf("core.ingest serves a new Corpus: %v", spaces)
	}
	ingestEnriched(t, corpusID, "harbour-"+run, "harbour", harbourText)
}

const harbourText = "Dockers stopped work at dawn.\n\nThe harbour stayed closed all day."

// The sample ingestion plugin (sdks/go/examples/hash-embedder) is then pinned
// in place of core.ingest, with its small space served and its large space
// for evaluation (scripts/ingestion_plugin.py). The Corpus keeps core.ingest's
// space until it is rebuilt; the rebuild carries its Records onto the
// plugin's named spaces, both covered, and search then encodes queries with
// the plugin; a new Record goes through the plugin directly. A Corpus created
// after the swap starts on the plugin's spaces with no rebuild (THE-787).
func TestIngestionPlugin(t *testing.T) {
	corpusID, run := ingestionPluginCorpus(t)
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	before, _ := vectorSpaces(t, corpusID)
	if owner, _ := before[coreIngestSpace]["owner"].(map[string]any); len(before) != 1 || owner["plugin_id"] != "core.ingest" {
		t.Fatalf("before the rebuild core.ingest's space serves the Corpus: %v", before)
	}
	semantic := func(corpusID, query string) []map[string]any {
		t.Helper()
		items := request(t, "POST", "/v0/search", admin, map[string]any{"query": query, "corpus_ids": []string{corpusID}, "mode": "semantic"}, 200)["items"].([]any)
		hits := []map[string]any{}
		for _, item := range items {
			hits = append(hits, item.(map[string]any))
		}
		return hits
	}
	fresh := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Ingestion plugin, new", "idempotency_key": "ingestion-plugin-new-" + run}, 201)["corpus_id"].(string)
	if spaces, _ := vectorSpaces(t, fresh); len(spaces) != 2 || spaces[pluginServedSpace]["role"] != "served" || spaces[pluginEvaluationSpace]["role"] != "evaluation" {
		t.Fatalf("a Corpus created after the swap starts on the plugin's spaces: %v", spaces)
	}
	tide := ingestEnriched(t, fresh, "tide-"+run, "tide", "The tide turned before noon at the harbour mouth.")
	if hits := semantic(fresh, "tide turned"); len(hits) == 0 || hits[0]["version_id"] != tide || hits[0]["vector_space_id"] != pluginServedSpace {
		t.Fatalf("the first Record of a new Corpus is found by semantic search on the plugin's space: %v", hits)
	}
	// Replaying the command names the Version ingested before the swap.
	harbour := awaitReceipt(t, request(t, "POST", "/v0/records", admin, inlineCommand(corpusID, "harbour-"+run, "harbour", harbourText), 202)["receipt_id"].(string))["version_id"].(string)
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
	hits := semantic(corpusID, "harbour closed")
	if len(hits) == 0 || hits[0]["version_id"] != harbour || hits[0]["vector_space_id"] != pluginServedSpace {
		t.Fatalf("semantic search encoded by the plugin: %v", hits)
	}

	// A new Record goes through the plugin directly.
	vineyard := ingestEnriched(t, corpusID, "vineyard-"+run, "vineyard", "The vineyard harvest started early this year.")
	hits = semantic(corpusID, "vineyard harvest")
	if len(hits) == 0 || hits[0]["version_id"] != vineyard || hits[0]["vector_space_id"] != pluginServedSpace {
		t.Fatalf("a Record ingested through the plugin: %v", hits)
	}
	after, total = vectorSpaces(t, corpusID)
	if coverage(after[pluginServedSpace]) != total || coverage(after[pluginEvaluationSpace]) != total {
		t.Fatalf("coverage after a new Record: %v of %v", after, total)
	}
}
