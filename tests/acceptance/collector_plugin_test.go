package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A connector kind provided by a pinned plugin: the Go SDK sample
// (sdks/go/examples/static-source, kind "static") that scripts/connector_plugin.py
// pins and runs in the connectors part. Its items are listed in the instance
// config and returned page by page; the checkpoint is an offset.

// collectorPluginToken is the sample's test token, never a real secret: the
// harness scans the process logs for it afterwards.
const collectorPluginToken = "fixture-test-secret-collector-plugin"

func collectorPluginKey(t *testing.T) string {
	t.Helper()
	token := connectorToken(t)
	if os.Getenv("QUIVR_TEST_CAPTURES") == "" {
		t.Skip("scripts/connector_plugin.py runs these phases around plugin stops")
	}
	return token
}

// staticInstance creates a "static" instance of n items with one item per page.
func staticInstance(t *testing.T, token, key, corpusID string, n, interval int) (map[string]any, []string) {
	t.Helper()
	items := make([]any, n)
	keys := make([]string, n)
	for i := range items {
		keys[i] = fmt.Sprintf("%s-%02d", key, i)
		items[i] = map[string]any{"key": keys[i], "title": "Dispatch " + keys[i], "body": fmt.Sprintf("Harbourlight bulletin number %d.", i)}
	}
	created := request(t, "POST", "/v0/connectors", token, map[string]any{"idempotency_key": key + "-" + connectorRun, "corpus_id": corpusID, "source_namespace": key, "kind": "static",
		"config": map[string]any{"items": items, "page_size": 1}, "schedule": map[string]any{"interval_seconds": interval},
		"credential": map[string]any{"secret": map[string]any{"token": collectorPluginToken}}}, 201)
	noSecret(t, created)
	return created, keys
}

func itemsRead(h map[string]any) float64 {
	u, _ := h["usage"].(map[string]any)
	if u == nil {
		return 0
	}
	return u["items_read"].(float64) + u["previous_day_items_read"].(float64)
}

func versionsOnce(t *testing.T, seen []map[string]any, byKey map[string]string) {
	t.Helper()
	for key, id := range byKey {
		if n := typed(seen, "record.materialized", id); n != 1 {
			t.Fatalf("%s has %d Versions, want exactly 1", key, n)
		}
	}
}

// TestCollectorPluginCollectsAndResumes: the plugin kind is published with its
// schemas, an instance collects through it, its Records are searchable, and a
// subsequent runs resume from the committed checkpoint. Each run fetches at
// most 10 pages and may continue immediately, so observing its completion
// does not freeze collection. Exactly 25 source reads and one Version per
// item prove that resumed runs never start over, which idempotent Receipts
// alone would hide.
func TestCollectorPluginCollectsAndResumes(t *testing.T) {
	token := collectorPluginKey(t)
	catalog := request(t, "GET", "/v0/connector-kinds", token, nil, 200)
	var static map[string]any
	for _, raw := range catalog["items"].([]any) {
		if k := raw.(map[string]any); k["kind"] == "static" {
			static = k
		}
	}
	if static == nil || static["credential"] != "required" || static["description"] == nil || static["default_interval_seconds"].(float64) != 900 ||
		static["config_schema"].(map[string]any)["required"].([]any)[0] != "items" || static["credential_schema"].(map[string]any)["required"].([]any)[0] != "token" {
		t.Fatalf("the plugin kind is not published with its schemas: %v", static)
	}

	corpusID, cursor := connectorCorpus(t, token, "collector-plugin")
	created, keys := staticInstance(t, token, "collector", corpusID, 25, 3600)
	id := created["connector_id"].(string)
	first := map[string]int{}
	for _, key := range keys[:10] {
		first[key] = 1
	}
	awaitHealth(t, token, id, func(h map[string]any) bool { return h["last_success_at"] != nil && itemsRead(h) >= 10 })
	byKey, _ := recordsByKey(t, token, corpusID, cursor, first)
	// Materialization precedes indexing; wait for each Record's searchable
	// publication, replaying the original cursor so earlier events stay visible.
	for _, recordID := range byKey {
		awaitChange(t, token, corpusID, cursor, "record.retrieval_ready", recordID)
	}
	hits := request(t, "POST", "/v0/search", token, map[string]any{"query": "Harbourlight", "corpus_ids": []string{corpusID}, "mode": "lexical"}, 200)["items"].([]any)
	if len(hits) == 0 {
		t.Fatal("collected Records are not searchable")
	}

	// Pull any pending run forward; automatic continuations may already have advanced.
	request(t, "PUT", "/v0/connectors/"+id+"/schedule", token, map[string]any{"interval_seconds": 1}, 200)
	all := map[string]int{}
	for _, key := range keys {
		all[key] = 1
	}
	byKey, seen := recordsByKey(t, token, corpusID, cursor, all)
	if len(byKey) != 25 {
		t.Fatalf("collection produced %d Records, want exactly 25: %v", len(byKey), byKey)
	}
	versionsOnce(t, seen, byKey)
	c := awaitHealth(t, token, id, func(h map[string]any) bool { return itemsRead(h) >= 25 })
	if h := c["health"].(map[string]any); itemsRead(h) != 25 || h["state"] != "active" || h["last_error"] != nil {
		t.Fatalf("resumed collection read %v items, want exactly 25: %v", itemsRead(h), h)
	}
	disabled := request(t, "POST", "/v0/connectors/"+id+"/disable", token, map[string]any{"idempotency_key": "collector-stop-" + connectorRun}, 200)
	noSecret(t, disabled)
}

type collectorOutage struct {
	CorpusID  string   `json:"corpus_id"`
	Cursor    string   `json:"cursor"`
	Connector string   `json:"connector_id"`
	Keys      []string `json:"keys"`
}

func collectorOutagePath() string {
	return filepath.Join(os.Getenv("QUIVR_TEST_CAPTURES"), "collector-plugin-outage.json")
}

// TestCollectorPluginOutage runs while the plugin is stopped: a new instance
// reports the unreachable plugin as its last error (not access_error) and collects
// nothing; the platform keeps answering.
func TestCollectorPluginOutage(t *testing.T) {
	token := collectorPluginKey(t)
	corpusID, cursor := connectorCorpus(t, token, "collector-outage")
	created, keys := staticInstance(t, token, "outage", corpusID, 3, 1)
	id := created["connector_id"].(string)
	c := awaitHealth(t, token, id, func(h map[string]any) bool {
		e, _ := h["last_error"].(map[string]any)
		return e != nil && e["code"] == "plugin_unavailable"
	})
	// Unavailability is transient: it never reads as the source refusing access.
	if h := c["health"].(map[string]any); h["state"] != "active" || h["last_success_at"] != nil || itemsRead(h) != 0 {
		t.Fatalf("an unreachable plugin counted as a poll: %v", h)
	}
	if items, _ := drain(t, token, corpusID, cursor, 0); typedAny(items, "record.materialized") > 0 {
		t.Fatalf("Records appeared while the plugin was down: %v", items)
	}
	raw, _ := json.Marshal(collectorOutage{CorpusID: corpusID, Cursor: cursor, Connector: id, Keys: keys})
	if err := os.WriteFile(collectorOutagePath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestCollectorPluginRecovers runs after the plugin restarted: the instance
// created during the outage collects every item once and its health recovers.
func TestCollectorPluginRecovers(t *testing.T) {
	token := collectorPluginKey(t)
	raw, err := os.ReadFile(collectorOutagePath())
	if err != nil {
		t.Fatal("TestCollectorPluginOutage did not record its instance: ", err)
	}
	var outage collectorOutage
	if err := json.Unmarshal(raw, &outage); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{}
	for _, key := range outage.Keys {
		want[key] = 1
	}
	byKey, seen := recordsByKey(t, token, outage.CorpusID, outage.Cursor, want)
	versionsOnce(t, seen, byKey)
	// last_error keeps the outage on record; recovery is a successful poll after it.
	awaitHealth(t, token, outage.Connector, func(h map[string]any) bool {
		return h["state"] == "active" && itemsRead(h) == float64(len(outage.Keys)) && succeededAfterError(h)
	})
	request(t, "POST", "/v0/connectors/"+outage.Connector+"/disable", token, map[string]any{"idempotency_key": "collector-outage-stop-" + connectorRun}, 200)
}

// succeededAfterError reports a successful poll after the last recorded error.
func succeededAfterError(h map[string]any) bool {
	e, _ := h["last_error"].(map[string]any)
	success, _ := h["last_success_at"].(string)
	if e == nil || success == "" {
		return e == nil && success != ""
	}
	failed, err1 := time.Parse(time.RFC3339Nano, e["at"].(string))
	succeeded, err2 := time.Parse(time.RFC3339Nano, success)
	return err1 == nil && err2 == nil && succeeded.After(failed)
}

func typedAny(items []map[string]any, kind string) int {
	n := 0
	for _, item := range items {
		if item["type"] == kind {
			n++
		}
	}
	return n
}
