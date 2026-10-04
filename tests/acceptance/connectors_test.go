package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Connector acceptance runs in its own Organization (org_c) after every timed
// scenario: scheduled acquisition keeps polling in the background.

const connectorTestSecret = "fixture-test-secret-acceptance-not-real"

// connectorRun keeps idempotency keys unique per process so the suite can be
// rerun against the same stack without replaying earlier instances.
var connectorRun = fmt.Sprint(time.Now().UnixNano())

func connectorToken(t *testing.T) string {
	t.Helper()
	if os.Getenv("QUIVR_TEST_URL") == "" || os.Getenv("QUIVR_TEST_CONNECTOR") == "" {
		t.Skip("run make verify for real connector acceptance")
	}
	return os.Getenv("QUIVR_TEST_CONNECTOR")
}

// noSecret fails when any response body carries a deposited secret.
func noSecret(t *testing.T, bodies ...map[string]any) {
	t.Helper()
	for _, b := range bodies {
		raw, _ := json.Marshal(b)
		if strings.Contains(string(raw), "fixture-test-secret") || strings.Contains(string(raw), "fixture-revoked") {
			t.Fatalf("secret leaked in response: %s", raw)
		}
	}
}

func connectorCorpus(t *testing.T, token, key string) (string, string) {
	t.Helper()
	id := request(t, "POST", "/v0/corpora", token, map[string]any{"name": "Connector " + key, "idempotency_key": "connector-corpus-" + key + "-" + connectorRun}, 201)["corpus_id"].(string)
	start := request(t, "GET", changesPath(id, "", 0), token, nil, 200)["next_cursor"].(string)
	return id, start
}

func fixtureConnector(key, corpusID, namespace string, script []any, extra map[string]any) map[string]any {
	body := map[string]any{"idempotency_key": key + "-" + connectorRun, "corpus_id": corpusID, "source_namespace": namespace, "kind": "fixture",
		"config": map[string]any{"requires_credential": true, "script": script}, "schedule": map[string]any{"interval_seconds": 1},
		"credential": map[string]any{"secret": map[string]any{"token": connectorTestSecret}}}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func step(items ...map[string]any) map[string]any {
	list := make([]any, len(items))
	for i, item := range items {
		list[i] = item
	}
	return map[string]any{"items": list}
}

func item(key, text string) map[string]any { return map[string]any{"record_key": key, "text": text} }

// awaitHealth polls a Connector until its health satisfies ok.
func awaitHealth(t *testing.T, token, id string, ok func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for {
		c := request(t, "GET", "/v0/connectors/"+id, token, nil, 200)
		noSecret(t, c)
		if ok(c["health"].(map[string]any)) {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("health never reached expectation: %v", c)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func state(want string) func(map[string]any) bool {
	return func(h map[string]any) bool { return h["state"] == want }
}

// recordsByKey follows the change feed until every expected key has the
// expected number of materialized versions, then returns Record IDs by key.
func recordsByKey(t *testing.T, token, corpusID, cursor string, versions map[string]int) (map[string]string, []map[string]any) {
	t.Helper()
	ids := map[string]string{}
	var seen []map[string]any
	// Materialization shares the CI worker with earlier load; allow for its backlog.
	deadline := time.Now().Add(150 * time.Second)
	for {
		items, next := drain(t, token, corpusID, cursor, 0)
		seen, cursor = append(seen, items...), next
		for _, e := range items {
			res := e["resource"].(map[string]any)
			if res["kind"] != "record" {
				continue
			}
			if _, known := ids[res["id"].(string)]; !known {
				r := request(t, "GET", "/v0/records/"+res["id"].(string), token, nil, 200)
				ids[res["id"].(string)] = r["source"].(map[string]any)["record_key"].(string)
			}
		}
		done := true
		byKey := map[string]string{}
		for id, key := range ids {
			byKey[key] = id
		}
		for key, n := range versions {
			if typed(seen, "record.materialized", byKey[key]) < n {
				done = false
			}
		}
		if done {
			return byKey, seen
		}
		if time.Now().After(deadline) {
			t.Fatalf("records incomplete: %v; saw %d events", ids, len(seen))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestConnectorCollectsThroughTheIngestionPathOnItsSchedule(t *testing.T) {
	token := connectorToken(t)
	corpusID, cursor := connectorCorpus(t, token, "lifecycle")
	script := []any{
		step(item("a", "Alpha dépêche"), item("b", "Beta dépêche")),
		step(item("a", "Alpha dépêche corrigée")),
		step(item("b", "Beta dépêche")), // re-polled unchanged item: its Receipt replays
		step(item("c", "Gamma dépêche")),
	}
	body := fixtureConnector("lifecycle", corpusID, "wire", script, nil)
	created := request(t, "POST", "/v0/connectors", token, body, 201)
	noSecret(t, created)
	id := created["connector_id"].(string)
	if created["corpus_id"] != corpusID || created["source_namespace"] != "wire" || created["enabled"] != true || created["credential"].(map[string]any)["version"].(float64) != 1 {
		t.Fatalf("created %v", created)
	}
	if replay := request(t, "POST", "/v0/connectors", token, body, 201); replay["connector_id"] != id {
		t.Fatalf("replay changed identity: %v", replay)
	}
	conflict := fixtureConnector("lifecycle", corpusID, "other", script, nil)
	if e := request(t, "POST", "/v0/connectors", token, conflict, 409); e["code"] != "idempotency_conflict" {
		t.Fatal(e)
	}
	if e := request(t, "POST", "/v0/connectors", token, fixtureConnector("lifecycle-2", corpusID, "wire", script, nil), 409); e["code"] != "source_namespace_in_use" {
		t.Fatal(e)
	}
	if e := request(t, "POST", "/v0/connectors", token, fixtureConnector("floor", corpusID, "floor", script, map[string]any{"schedule": map[string]any{"interval_seconds": 0}}), 422); e["code"] != "invalid_schema" {
		t.Fatal(e)
	}

	byKey, events := recordsByKey(t, token, corpusID, cursor, map[string]int{"a": 2, "b": 1, "c": 1})
	if typed(events, "record.materialized", byKey["b"]) != 1 {
		t.Fatalf("re-polled item created another version: %v", events)
	}
	if typed(events, "connector.created", id) != 1 {
		t.Fatalf("missing connector.created: %v", events)
	}
	// Materialization precedes promotion: wait until the correction is the current version.
	var current map[string]any
	for deadline := time.Now().Add(120 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		a := request(t, "GET", "/v0/records/"+byKey["a"], token, nil, 200)
		if a["source"].(map[string]any)["namespace"] != "wire" {
			t.Fatalf("record %v", a)
		}
		if versionID, ok := a["current_version_id"].(string); ok {
			current = request(t, "GET", "/v0/records/"+byKey["a"]+"/versions/"+versionID, token, nil, 200)
			if current["manifest"].(map[string]any)["parts"].([]any)[0].(map[string]any)["content"].(map[string]any)["text"] == "Alpha dépêche corrigée" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("correction never became current: %v", current)
		}
	}
	if p, _ := current["provenance"].(map[string]any); p["producer"] != id || p["producer_version"] != "fixture/v1" {
		t.Fatalf("provenance %v", current["provenance"])
	}
	healthy := awaitHealth(t, token, id, func(h map[string]any) bool {
		return h["state"] == "active" && h["last_item_at"] != nil && h["last_success_at"] != nil
	})
	if healthy["health"].(map[string]any)["last_error"] != nil {
		t.Fatalf("unexpected error %v", healthy)
	}

	// Isolation: another Organization and a key without connector actions see nothing.
	if e := request(t, "GET", "/v0/connectors/"+id, os.Getenv("QUIVR_TEST_OTHER"), nil, 404); e["code"] != "not_found" {
		t.Fatal(e)
	}
	if e := request(t, "GET", "/v0/connectors/"+id, os.Getenv("QUIVR_TEST_ADMIN"), nil, 403); e["code"] != "forbidden" {
		t.Fatal(e)
	}
	// Same Organization, key scoped to another Corpus: the instance does not exist for it.
	scoped := os.Getenv("QUIVR_TEST_CONNECTOR_SCOPED")
	if e := request(t, "GET", "/v0/connectors/"+id, scoped, nil, 404); e["code"] != "not_found" {
		t.Fatal(e)
	}
	if page := request(t, "GET", "/v0/connectors", scoped, nil, 200); len(page["items"].([]any)) != 0 {
		t.Fatalf("cross-Corpus list: %v", page)
	}
	if e := request(t, "POST", "/v0/connectors", scoped, fixtureConnector("scoped", corpusID, "scoped", script, nil), 404); e["code"] != "not_found" {
		t.Fatal(e)
	}
	if e := request(t, "POST", "/v0/connectors/"+id+"/disable", scoped, map[string]any{"idempotency_key": "scoped-disable"}, 404); e["code"] != "not_found" {
		t.Fatal(e)
	}
	if page := request(t, "GET", "/v0/connectors?corpus_id="+corpusID, os.Getenv("QUIVR_TEST_OTHER"), nil, 200); len(page["items"].([]any)) != 0 {
		t.Fatalf("cross-Organization list: %v", page)
	}
	page := request(t, "GET", "/v0/connectors?corpus_id="+corpusID, token, nil, 200)
	noSecret(t, page)
	if len(page["items"].([]any)) != 1 {
		t.Fatalf("list %v", page)
	}
	// The public API cannot pre-claim a connector idempotency key.
	reserved := map[string]any{"idempotency_key": "connector:item_forged", "source": map[string]any{"corpus_id": corpusID, "namespace": "wire", "record_key": "z"}, "content": map[string]any{"kind": "text", "text": "forged"}}
	if e := request(t, "POST", "/v0/records", token, reserved, 422); e["code"] != "reserved_idempotency_key" {
		t.Fatal(e)
	}

	disabled := request(t, "POST", "/v0/connectors/"+id+"/disable", token, map[string]any{"idempotency_key": "disable-1"}, 200)
	if disabled["enabled"] != false || disabled["health"].(map[string]any)["state"] != "disabled" || disabled["disabled_at"] == nil {
		t.Fatalf("disabled %v", disabled)
	}
	if again := request(t, "POST", "/v0/connectors/"+id+"/disable", token, map[string]any{"idempotency_key": "disable-2"}, 200); again["disabled_at"] != disabled["disabled_at"] {
		t.Fatalf("disable not idempotent: %v", again)
	}
	after, _ := awaitChange(t, token, corpusID, cursor, "connector.disabled", id)
	if typed(after, "connector.health_changed", id) < 1 {
		t.Fatalf("disable must commit a health change: %v", after)
	}
	if e := request(t, "PUT", "/v0/connectors/"+id+"/credential", token, map[string]any{"idempotency_key": "late", "secret": map[string]any{"token": connectorTestSecret}}, 409); e["code"] != "connector_disabled" {
		t.Fatal(e)
	}
	// Disabling released the Source Namespace for a replacement instance.
	replacement := request(t, "POST", "/v0/connectors", token, fixtureConnector("lifecycle-replacement", corpusID, "wire", []any{}, nil), 201)
	request(t, "POST", "/v0/connectors/"+replacement["connector_id"].(string)+"/disable", token, map[string]any{"idempotency_key": "disable-replacement"}, 200)
}

func TestConnectorHealthDistinguishesAccessErrorsExpiryAndSilence(t *testing.T) {
	token := connectorToken(t)
	corpusID, cursor := connectorCorpus(t, token, "health")

	// Cut access: a refused credential is access_error, never silence.
	revoked := fixtureConnector("health-access", corpusID, "access", []any{step(item("x", "Texte"))}, map[string]any{
		"credential":    map[string]any{"secret": map[string]any{"token": "fixture-revoked-acceptance-token"}},
		"health_policy": map[string]any{"silent_after_seconds": 1},
	})
	c := request(t, "POST", "/v0/connectors", token, revoked, 201)
	id := c["connector_id"].(string)
	denied := awaitHealth(t, token, id, state("access_error"))
	if e := denied["health"].(map[string]any)["last_error"].(map[string]any); e["code"] != "unauthorized" {
		t.Fatalf("last_error %v", e)
	}
	// Rotate to a valid credential expiring within the default 14-day warning.
	soon := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	replaced := request(t, "PUT", "/v0/connectors/"+id+"/credential", token, map[string]any{"idempotency_key": "rotate-1", "secret": map[string]any{"token": connectorTestSecret}, "expires_at": soon}, 200)
	noSecret(t, replaced)
	if replaced["credential"].(map[string]any)["version"].(float64) != 2 {
		t.Fatalf("rotation %v", replaced)
	}
	if again := request(t, "PUT", "/v0/connectors/"+id+"/credential", token, map[string]any{"idempotency_key": "rotate-1", "secret": map[string]any{"token": connectorTestSecret}, "expires_at": soon}, 200); again["credential"].(map[string]any)["version"].(float64) != 2 {
		t.Fatalf("replay rotated again: %v", again)
	}
	if e := request(t, "PUT", "/v0/connectors/"+id+"/credential", token, map[string]any{"idempotency_key": "rotate-1", "secret": map[string]any{"token": "fixture-test-secret-different"}}, 409); e["code"] != "idempotency_conflict" {
		t.Fatal(e)
	}
	awaitHealth(t, token, id, state("credential_expiring"))
	// A long-lived credential clears the warning at the replacement boundary.
	later := time.Now().Add(60 * 24 * time.Hour).UTC().Format(time.RFC3339)
	fresh := request(t, "PUT", "/v0/connectors/"+id+"/credential", token, map[string]any{"idempotency_key": "rotate-2", "secret": map[string]any{"token": connectorTestSecret}, "expires_at": later}, 200)
	if s := fresh["health"].(map[string]any)["state"]; s == "credential_expiring" || s == "access_error" {
		t.Fatalf("rotation did not clear the warning: %v", fresh)
	}
	// The one-step script is exhausted: after the 1 s threshold the source is silent.
	awaitHealth(t, token, id, state("silent"))
	seen, _ := awaitChange(t, token, corpusID, cursor, "connector.credential_replaced", id)
	for _, want := range []string{"connector.created", "connector.health_changed"} {
		if typed(seen, want, id) == 0 {
			t.Fatalf("missing %s: %v", want, seen)
		}
	}
	request(t, "POST", "/v0/connectors/"+id+"/disable", token, map[string]any{"idempotency_key": "health-stop"}, 200)
	fmt.Println("connector health transitions observed for", id)
}
