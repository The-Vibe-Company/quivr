package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

// postRebuild initiates a rebuild and returns the Operation and its Location.
func postRebuild(t *testing.T, token, corpusID, key string, want int) (map[string]any, string) {
	t.Helper()
	return postAction(t, token, "/v0/corpora/"+corpusID+"/rebuilds", key, want)
}

// postAction sends an ActionRequest and returns the response and its Location.
func postAction(t *testing.T, token, path, key string, want int) (map[string]any, string) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"idempotency_key": key})
	req, err := http.NewRequest("POST", os.Getenv("QUIVR_TEST_URL")+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	if err = json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != want {
		t.Fatalf("%s: got %d want %d: %v", path, res.StatusCode, want, body)
	}
	if dir := os.Getenv("QUIVR_TEST_CAPTURES"); dir != "" {
		f, e := os.CreateTemp(dir, "response-*.json")
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		_ = json.NewEncoder(f).Encode(map[string]any{"path": req.URL.Path, "method": "POST", "status": res.StatusCode, "body": body})
	}
	return body, res.Header.Get("Location")
}

// awaitRetrievalReady tolerates the worker backlog left by earlier timed
// scenarios; readiness is still observed only through the public Receipt.
func awaitRetrievalReady(t *testing.T, receiptID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		r := request(t, "GET", "/v0/ingestion-receipts/"+receiptID, os.Getenv("QUIVR_TEST_ADMIN"), nil, 200)
		if availability, _ := r["availability"].(map[string]any); availability["searchable"] == true {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("Receipt never retrieval-ready: %v", r)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// ingestEnriched ingests one Record and returns its Version once enrichment
// has committed, so assertions see the Version with its vectors and a rebuild
// reuses them instead of racing the enrichment.
func ingestEnriched(t *testing.T, corpusID, key, title, text string) string {
	t.Helper()
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	start := request(t, "GET", changesPath(corpusID, "", 0), admin, nil, 200)["next_cursor"].(string)
	accepted := request(t, "POST", "/v0/records", admin, inlineCommand(corpusID, key, title, text), 202)
	ready := awaitRetrievalReady(t, accepted["receipt_id"].(string))
	awaitEnriched(t, admin, corpusID, start, ready["record_id"].(string))
	return ready["version_id"].(string)
}

func awaitOperation(t *testing.T, location string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		op := request(t, "GET", location, os.Getenv("QUIVR_TEST_ADMIN"), nil, 200)
		if op["state"] == "succeeded" || op["state"] == "failed" || op["state"] == "canceled" {
			return op
		}
		if time.Now().After(deadline) {
			t.Fatalf("Operation never terminal: %v", op)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func generationsByVersion(t *testing.T, corpora []string, query string) map[string]string {
	t.Helper()
	out := map[string]string{}
	result := request(t, "POST", "/v0/search", os.Getenv("QUIVR_TEST_ADMIN"), map[string]any{"query": query, "corpus_ids": corpora, "mode": "lexical"}, 200)
	for _, item := range result["items"].([]any) {
		hit := item.(map[string]any)
		out[hit["version_id"].(string)] = hit["projection_generation_id"].(string)
	}
	return out
}

// Public rebuild initiation, authorization, replay and read semantics, with
// the neighbouring Corpus left on its own generation. Uses its own Corpora.
func TestRebuildActivatesScopedGenerationAndReplays(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := fmt.Sprint(time.Now().UnixNano())
	a := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Rebuild A", "idempotency_key": "rebuild-a-" + run}, 201)["corpus_id"].(string)
	b := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Rebuild B", "idempotency_key": "rebuild-b-" + run}, 201)["corpus_id"].(string)
	versions := map[string]string{}
	for corpusID, text := range map[string]string{a: "Lanterne rouge du port", b: "Lanterne verte du quai"} {
		versions[corpusID] = ingestEnriched(t, corpusID, "rebuild-"+corpusID, "lanterne", text)
	}
	before := generationsByVersion(t, []string{a, b}, "lanterne")
	if len(before) != 2 {
		t.Fatalf("baseline hits %v", before)
	}
	cursor := request(t, "GET", changesPath(a, "", 0), admin, nil, 200)["next_cursor"].(string)

	postRebuild(t, os.Getenv("QUIVR_TEST_READER"), a, "denied", 403)
	postRebuild(t, os.Getenv("QUIVR_TEST_DENIED"), a, "denied", 403)
	postRebuild(t, os.Getenv("QUIVR_TEST_SCOPED"), a, "out-of-scope", 404)
	postRebuild(t, os.Getenv("QUIVR_TEST_OTHER"), a, "foreign", 404)
	postRebuild(t, admin, "corpus_absent_"+run, "absent", 404)
	rawRequest(t, "POST", "/v0/corpora/"+a+"/rebuilds", admin, []byte(`{"idempotency_key":"k","extra":1}`), 422)

	op, location := postRebuild(t, admin, a, "rebuild-1", 202)
	id, _ := op["operation_id"].(string)
	if id == "" || location != "/v0/operations/"+id || op["kind"] != "projection_rebuild" || op["corpus_id"] != a {
		t.Fatalf("accepted %v at %q", op, location)
	}
	if state := op["state"]; state != "queued" && state != "running" && state != "succeeded" {
		t.Fatalf("initial state %v", state)
	}
	if replay, again := postRebuild(t, admin, a, "rebuild-1", 202); replay["operation_id"] != id || again != location {
		t.Fatalf("replay diverged: %v %q", replay, again)
	}
	done := awaitOperation(t, location)
	result, _ := done["result"].(map[string]any)
	if done["state"] != "succeeded" || result == nil {
		t.Fatalf("rebuild outcome %v", done)
	}
	generation := result["projection_generation_id"].(string)
	if generation == before[versions[a]] {
		t.Fatalf("rebuild reused the prior generation %s", generation)
	}
	terminal, _ := postRebuild(t, admin, a, "rebuild-1", 202)
	if terminal["operation_id"] != id || terminal["state"] != "succeeded" || terminal["result"].(map[string]any)["projection_generation_id"] != generation {
		t.Fatalf("terminal replay %v", terminal)
	}
	request(t, "GET", location, os.Getenv("QUIVR_TEST_READER"), nil, 403)
	request(t, "GET", location, os.Getenv("QUIVR_TEST_SCOPED"), nil, 404)
	request(t, "GET", location, os.Getenv("QUIVR_TEST_OTHER"), nil, 404)

	after := generationsByVersion(t, []string{a, b}, "lanterne")
	if after[versions[a]] != generation || after[versions[b]] != before[versions[b]] {
		t.Fatalf("routing after rebuild %v, before %v, activated %s", after, before, generation)
	}
	// Operation state transitions join the Corpus's committed journal.
	deadline := time.Now().Add(15 * time.Second)
	var seen []map[string]any
	for typed(seen, "operation.updated", id) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("operation.updated transitions %v", seen)
		}
		var items []map[string]any
		items, cursor = drain(t, admin, a, cursor, 0)
		seen = append(seen, items...)
		time.Sleep(100 * time.Millisecond)
	}
	if n := typed(seen, "operation.updated", id); n != 3 {
		t.Fatalf("operation.updated count %d, want queued/running/succeeded", n)
	}
	for _, e := range seen {
		if e["type"] == "operation.updated" && e["resource"].(map[string]any)["kind"] != "operation" {
			t.Fatalf("operation event resource %v", e)
		}
	}
}

// Public Operation control on a terminal rebuild: cancel returns the existing
// outcome, rerun creates a new linked Operation that progresses to its own
// activated generation, and unauthorized callers cannot act. Uses its own Corpus.
// Cancellation of queued work before activation runs in scripts/operation_control.py,
// which can hold the worker stopped.
func TestRebuildCancelAndRerunTerminalOperation(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := fmt.Sprint(time.Now().UnixNano())
	c := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Operation control", "idempotency_key": "operation-control-" + run}, 201)["corpus_id"].(string)
	version := ingestEnriched(t, c, "control-"+run, "balise", "Balise cardinale du chenal")
	cursor := request(t, "GET", changesPath(c, "", 0), admin, nil, 200)["next_cursor"].(string)

	source, location := postRebuild(t, admin, c, "control-1", 202)
	sourceID := source["operation_id"].(string)
	done := awaitOperation(t, location)
	if done["state"] != "succeeded" {
		t.Fatalf("source rebuild %v", done)
	}
	first := done["result"].(map[string]any)["projection_generation_id"].(string)

	// Terminal cancel returns the existing outcome, whatever the key.
	for _, key := range []string{"cancel-1", "cancel-1", "cancel-2"} {
		got, _ := postAction(t, admin, location+"/cancel", key, 202)
		if got["operation_id"] != sourceID || got["state"] != "succeeded" || got["result"].(map[string]any)["projection_generation_id"] != first {
			t.Fatalf("terminal cancel %v", got)
		}
	}
	// Unauthorized control is rejected without revealing foreign Operations.
	for _, action := range []string{"/cancel", "/rerun"} {
		postAction(t, os.Getenv("QUIVR_TEST_READER"), location+action, "denied", 403)
		postAction(t, os.Getenv("QUIVR_TEST_DENIED"), location+action, "denied", 403)
		postAction(t, os.Getenv("QUIVR_TEST_SCOPED"), location+action, "out-of-scope", 404)
		postAction(t, os.Getenv("QUIVR_TEST_OTHER"), location+action, "foreign", 404)
		postAction(t, admin, "/v0/operations/operation_absent_"+run+action, "absent", 404)
	}

	// Rerun creates a new linked Operation with its own target and dispatch.
	rerun, rerunLocation := postAction(t, admin, location+"/rerun", "rerun-1", 202)
	rerunID, _ := rerun["operation_id"].(string)
	if rerunID == "" || rerunID == sourceID || rerun["previous_operation_id"] != sourceID || rerun["kind"] != "projection_rebuild" || rerun["corpus_id"] != c || rerunLocation != "/v0/operations/"+rerunID {
		t.Fatalf("rerun %v at %q", rerun, rerunLocation)
	}
	if replay, again := postAction(t, admin, location+"/rerun", "rerun-1", 202); replay["operation_id"] != rerunID || again != rerunLocation {
		t.Fatalf("rerun replay %v %q", replay, again)
	}
	// The rebuild route's own idempotency still names the source.
	if replay, _ := postRebuild(t, admin, c, "control-1", 202); replay["operation_id"] != sourceID {
		t.Fatalf("rebuild replay after rerun %v", replay)
	}
	finished := awaitOperation(t, rerunLocation)
	if finished["state"] != "succeeded" || finished["previous_operation_id"] != sourceID {
		t.Fatalf("rerun outcome %v", finished)
	}
	second := finished["result"].(map[string]any)["projection_generation_id"].(string)
	if second == first {
		t.Fatalf("rerun reused the source target generation %s", second)
	}
	if routed := generationsByVersion(t, []string{c}, "balise")[version]; routed != second {
		t.Fatalf("search served generation %s, want rerun target %s", routed, second)
	}
	if again, _ := postRebuild(t, admin, c, "control-1", 202); again["operation_id"] != sourceID || again["result"].(map[string]any)["projection_generation_id"] != first {
		t.Fatalf("source outcome changed after rerun %v", again)
	}

	// The journal records the rerun's transitions and nothing for terminal cancels.
	deadline := time.Now().Add(15 * time.Second)
	var seen []map[string]any
	for typed(seen, "operation.updated", sourceID) < 3 || typed(seen, "operation.updated", rerunID) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("operation.updated transitions %v", seen)
		}
		var items []map[string]any
		items, cursor = drain(t, admin, c, cursor, 0)
		seen = append(seen, items...)
		time.Sleep(100 * time.Millisecond)
	}
	if typed(seen, "operation.updated", sourceID) != 3 || typed(seen, "operation.updated", rerunID) != 3 {
		t.Fatalf("operation.updated counts source=%d rerun=%d, want 3 each", typed(seen, "operation.updated", sourceID), typed(seen, "operation.updated", rerunID))
	}
}
