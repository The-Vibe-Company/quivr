package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Destinations provisioned by scripts/local.py; their URLs and signing
// secrets live only in deployment configuration.
const (
	destinationA = "local-receiver-org-a"
	destinationB = "local-receiver-org-b"
)

func monitoringRun() string { return fmt.Sprint(time.Now().UnixNano()) }

func savedQueryCommand(key string, corpora ...string) map[string]any {
	return map[string]any{"idempotency_key": key, "name": "Veille " + key, "definition": map[string]any{
		"corpus_ids": corpora, "expression": map[string]any{"fixture": map[string]any{"decision": "match", "terms": []any{"élection"}}},
		"retrieval_profile": "default", "temporal_policy": "from_activation",
	}}
}

func subscriptionCommand(key string, query map[string]any, destination string) map[string]any {
	return map[string]any{"idempotency_key": key, "name": "Alertes " + key, "saved_query_id": query["saved_query_id"],
		"saved_query_version_id": query["current_version"].(map[string]any)["version_id"],
		"evaluator":              map[string]any{"plugin_id": "quivr.fixture", "version": "1", "configuration": map[string]any{"decisions": map[string]any{"default": "match"}}},
		"destination_id":         destination}
}

func position(t *testing.T, items []map[string]any, kind, id string) int {
	t.Helper()
	for i, item := range items {
		if item["type"] == kind && item["resource"].(map[string]any)["id"] == id {
			return i
		}
	}
	t.Fatalf("no %s for %s in %v", kind, id, items)
	return -1
}

// TestMonitoringDefinitionsActivateFromCommittedBoundary covers pinned
// definitions, activation ordering in the shared feed, disable and replay.
func TestMonitoringDefinitionsActivateFromCommittedBoundary(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := monitoringRun()
	a, b := changeCorpus(t, "monitoring-a-"+run), changeCorpus(t, "monitoring-b-"+run)
	cursorA := request(t, "GET", changesPath(a, "", 0), admin, nil, 200)["next_cursor"].(string)
	cursorB := request(t, "GET", changesPath(b, "", 0), admin, nil, 200)["next_cursor"].(string)

	before := request(t, "POST", "/v0/records", admin, inlineCommand(a, "monitoring-before-"+run, "before-"+run, "Dépêche antérieure"), 202)["record_id"].(string)

	queryBody := savedQueryCommand("query-"+run, a, b)
	query := request(t, "POST", "/v0/saved-queries", admin, queryBody, 201)
	if replay := request(t, "POST", "/v0/saved-queries", admin, queryBody, 201); !reflect.DeepEqual(replay, query) {
		t.Fatal("saved query replay changed identity", replay, query)
	}
	conflicting := savedQueryCommand("query-"+run, a)
	request(t, "POST", "/v0/saved-queries", admin, conflicting, 409)
	queryID := query["saved_query_id"].(string)
	version := query["current_version"].(map[string]any)
	pinned := request(t, "GET", "/v0/saved-queries/"+queryID+"/versions/"+version["version_id"].(string), admin, nil, 200)
	if !reflect.DeepEqual(pinned["definition"], version["definition"]) || !reflect.DeepEqual(roundTrip(t, queryBody["definition"]), pinned["definition"]) {
		t.Fatal("pinned definition differs from the request", pinned)
	}
	request(t, "GET", "/v0/saved-queries/"+queryID, admin, nil, 200)

	subBody := subscriptionCommand("subscription-"+run, query, destinationA)
	created, raw := requestRaw(t, "POST", "/v0/subscriptions", admin, subBody, 201)
	if created["enabled"] != true || strings.Contains(raw, "whsec_") || strings.Contains(raw, "http://") {
		t.Fatal("subscription must be enabled and never expose destination URL or secret", raw)
	}
	subID := created["subscription_id"].(string)
	current := created["current_version"].(map[string]any)
	if current["evaluator"].(map[string]any)["plugin_id"] != "quivr.fixture" || current["destination_id"] != destinationA || current["saved_query_version_id"] != version["version_id"] {
		t.Fatal("subscription version did not pin its configuration", current)
	}

	after := request(t, "POST", "/v0/records", admin, inlineCommand(a, "monitoring-after-"+run, "after-"+run, "Dépêche postérieure"), 202)["record_id"].(string)

	// Activation is one committed position in the same ordered stream as content:
	// earlier content precedes it and later content follows it.
	feedA, cursorA := drain(t, admin, a, cursorA, 0)
	if !(position(t, feedA, "record.accepted", before) < position(t, feedA, "saved_query.created", queryID) &&
		position(t, feedA, "saved_query.created", queryID) < position(t, feedA, "subscription.created", subID) &&
		position(t, feedA, "subscription.created", subID) < position(t, feedA, "record.accepted", after)) {
		t.Fatal("activation is not ordered between earlier and later content", feedA)
	}
	feedB, cursorB := drain(t, admin, b, cursorB, 0)
	if typed(feedB, "saved_query.created", queryID) != 1 || typed(feedB, "subscription.created", subID) != 1 {
		t.Fatal("every pinned Corpus feed observes the monitoring facts once", feedB)
	}
	for _, item := range feedB {
		if item["resource"].(map[string]any)["corpus_id"] != b {
			t.Fatal("event escaped its Corpus", item)
		}
	}
	if subA, subB := feedA[position(t, feedA, "subscription.created", subID)], feedB[position(t, feedB, "subscription.created", subID)]; subA["event_id"] == subB["event_id"] || subA["resource"].(map[string]any)["kind"] != "subscription" {
		t.Fatal("per-Corpus events share the resource with distinct event IDs", subA, subB)
	}

	versionPath := "/v0/subscriptions/" + subID + "/versions/" + current["version_id"].(string)
	disabled := request(t, "POST", "/v0/subscriptions/"+subID+"/disable", admin, map[string]any{"idempotency_key": "disable-" + run}, 200)
	if disabled["enabled"] != false || !reflect.DeepEqual(disabled["current_version"], current) {
		t.Fatal("disable must change only enabled state", disabled)
	}
	request(t, "POST", "/v0/subscriptions/"+subID+"/disable", admin, map[string]any{"idempotency_key": "disable-" + run}, 200)
	request(t, "POST", "/v0/subscriptions/"+subID+"/disable", admin, map[string]any{"idempotency_key": "disable-again-" + run}, 200)
	replayed := request(t, "POST", "/v0/subscriptions", admin, subBody, 201)
	if replayed["subscription_id"] != subID || replayed["enabled"] != false {
		t.Fatal("replaying creation reenabled the Subscription", replayed)
	}
	if read := request(t, "GET", "/v0/subscriptions/"+subID, admin, nil, 200); read["enabled"] != false {
		t.Fatal("disabled state not durable", read)
	}
	if pinnedSub := request(t, "GET", versionPath, admin, nil, 200); !reflect.DeepEqual(pinnedSub, current) {
		t.Fatal("immutable Version changed", pinnedSub)
	}
	later, _ := drain(t, admin, a, cursorA, 0)
	if typed(later, "subscription.disabled", subID) != 1 || typed(later, "subscription.created", subID) != 0 {
		t.Fatal("disable and replay must commit exactly one disable fact", later)
	}
	if more, _ := drain(t, admin, b, cursorB, 0); typed(more, "subscription.disabled", subID) != 1 {
		t.Fatal("disable missing from a pinned Corpus feed", more)
	}
	changed := subscriptionCommand("subscription-"+run, query, destinationA)
	changed["name"] = "Renamed"
	request(t, "POST", "/v0/subscriptions", admin, changed, 409)
}

// TestMonitoringScopeAndConfigurationErrors covers action, Organization and
// Corpus scope plus evaluator and destination validation.
func TestMonitoringScopeAndConfigurationErrors(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin, other := os.Getenv("QUIVR_TEST_ADMIN"), os.Getenv("QUIVR_TEST_OTHER")
	scoped, reader := os.Getenv("QUIVR_TEST_SCOPED"), os.Getenv("QUIVR_TEST_READER")
	run := monitoringRun()
	a := changeCorpus(t, "monitoring-scope-"+run)
	query := request(t, "POST", "/v0/saved-queries", admin, savedQueryCommand("scope-"+run, a), 201)
	queryID := query["saved_query_id"].(string)
	sub := request(t, "POST", "/v0/subscriptions", admin, subscriptionCommand("scope-"+run, query, destinationA), 201)
	subID := sub["subscription_id"].(string)
	versionID := sub["current_version"].(map[string]any)["version_id"].(string)

	request(t, "POST", "/v0/saved-queries", reader, savedQueryCommand("reader-"+run, a), 403)
	request(t, "GET", "/v0/subscriptions/"+subID, reader, nil, 403)
	// The Corpus-scoped key never gains an ungranted Corpus, and cannot see definitions pinned to it.
	request(t, "POST", "/v0/saved-queries", scoped, savedQueryCommand("scoped-"+run, a), 403)
	request(t, "POST", "/v0/subscriptions", scoped, subscriptionCommand("scoped-"+run, query, destinationA), 403)
	for _, path := range []string{"/v0/saved-queries/" + queryID, "/v0/subscriptions/" + subID, "/v0/subscriptions/" + subID + "/versions/" + versionID} {
		request(t, "GET", path, scoped, nil, 404)
		request(t, "GET", path, other, nil, 404)
	}
	request(t, "POST", "/v0/subscriptions/"+subID+"/disable", other, map[string]any{"idempotency_key": "other-" + run}, 404)
	request(t, "POST", "/v0/subscriptions/"+subID+"/disable", scoped, map[string]any{"idempotency_key": "scoped-" + run}, 404)
	if still := request(t, "GET", "/v0/subscriptions/"+subID, admin, nil, 200); still["enabled"] != true {
		t.Fatal("unauthorized disable changed state", still)
	}

	for name, c := range map[string]struct {
		mutate func(map[string]any)
		code   string
	}{
		"other evaluator":       {func(m map[string]any) { m["evaluator"].(map[string]any)["plugin_id"] = "vendor.semantic" }, "unsupported_evaluator"},
		"other fixture version": {func(m map[string]any) { m["evaluator"].(map[string]any)["version"] = "2" }, "unsupported_evaluator"},
		"foreign destination":   {func(m map[string]any) { m["destination_id"] = destinationB }, "unknown_destination"},
		"unknown destination":   {func(m map[string]any) { m["destination_id"] = "unconfigured" }, "unknown_destination"},
		"unknown query version": {func(m map[string]any) { m["saved_query_version_id"] = "saved_query_version_missing" }, "unknown_saved_query"},
		"inline secret":         {func(m map[string]any) { m["signing_secret"] = "whsec_inline" }, "invalid_schema"},
	} {
		body := subscriptionCommand("invalid-"+run+"-"+name, query, destinationA)
		c.mutate(body)
		if got := request(t, "POST", "/v0/subscriptions", admin, body, 422); got["code"] != c.code {
			t.Errorf("%s: %v", name, got)
		}
	}
	profile := savedQueryCommand("profile-"+run, a)
	profile["definition"].(map[string]any)["retrieval_profile"] = "fast"
	if got := request(t, "POST", "/v0/saved-queries", admin, profile, 422); got["code"] != "unsupported_profile" {
		t.Error("undeclared profile", got)
	}
	backfill := savedQueryCommand("backfill-"+run, a)
	backfill["definition"].(map[string]any)["temporal_policy"] = "all_history"
	request(t, "POST", "/v0/saved-queries", admin, backfill, 422)
	request(t, "POST", "/v0/saved-queries", other, savedQueryCommand("foreign-"+run, a), 403)
}

func roundTrip(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err = json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// requestRaw is request plus the exact response bytes as re-encoded JSON.
func requestRaw(t *testing.T, method, path, token string, body any, want int) (map[string]any, string) {
	t.Helper()
	result := request(t, method, path, token, body, want)
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return result, string(b)
}
