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
	data, _ := json.Marshal(map[string]any{"idempotency_key": key})
	req, err := http.NewRequest("POST", os.Getenv("QUIVR_TEST_URL")+"/v0/corpora/"+corpusID+"/rebuilds", bytes.NewReader(data))
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
		t.Fatalf("rebuild %s: got %d want %d: %v", corpusID, res.StatusCode, want, body)
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

func awaitOperation(t *testing.T, location string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		op := request(t, "GET", location, os.Getenv("QUIVR_TEST_ADMIN"), nil, 200)
		if op["state"] == "succeeded" || op["state"] == "failed" {
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
		accepted := request(t, "POST", "/v0/records", admin, inlineCommand(corpusID, "rebuild-"+corpusID, "lanterne", text), 202)
		versions[corpusID] = awaitRetrievalReady(t, accepted["receipt_id"].(string))["version_id"].(string)
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
