package acceptance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// alertEvaluator is the alert-rule template scripts/subscription_plugin.py
// pins next to the normalizer ("<plugin id>@<version>"). The tests skip
// without it.
func alertEvaluator(t *testing.T) (string, string) {
	t.Helper()
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	id, version, ok := strings.Cut(os.Getenv("QUIVR_TEST_ALERT_EVALUATOR"), "@")
	if !ok {
		t.Skip("QUIVR_TEST_ALERT_EVALUATOR is set by scripts/subscription_plugin.py")
	}
	return id, version
}

// alertSubscription pins a Saved Query with the template's expression to the
// pinned alert-rule evaluator.
func alertSubscription(t *testing.T, key, corpusID string, expression, configuration map[string]any, destination string, want int) (map[string]any, map[string]any) {
	t.Helper()
	id, version := alertEvaluator(t)
	return pinnedSubscription(t, id, version, key, corpusID, expression, configuration, destination, want)
}

// pinnedSubscription creates a Saved Query with the expression and a
// Subscription pinned to the evaluator plugin id and version.
func pinnedSubscription(t *testing.T, id, version, key, corpusID string, expression, configuration map[string]any, destination string, want int) (map[string]any, map[string]any) {
	t.Helper()
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	query := request(t, "POST", "/v0/saved-queries", admin, map[string]any{"idempotency_key": "alert-query-" + key, "name": "Alert " + key, "definition": map[string]any{
		"corpus_ids": []string{corpusID}, "expression": expression, "retrieval_profile": "balanced", "temporal_policy": "from_activation"}}, 201)
	if configuration == nil {
		configuration = map[string]any{}
	}
	sub := request(t, "POST", "/v0/subscriptions", admin, map[string]any{"idempotency_key": "alert-subscription-" + key, "name": "Alert " + key,
		"saved_query_id": query["saved_query_id"], "saved_query_version_id": query["current_version"].(map[string]any)["version_id"],
		"evaluator": map[string]any{"plugin_id": id, "version": version, "configuration": configuration}, "destination_id": destination}, want)
	return query, sub
}

// matchCreatedFor lists the match.created notices of one Subscription in a feed.
func matchCreatedFor(items []map[string]any, subscriptionID string) []map[string]any {
	var out []map[string]any
	for _, item := range items {
		if item["type"] == "match.created" && item["monitoring"].(map[string]any)["subscription_id"] == subscriptionID {
			out = append(out, item)
		}
	}
	return out
}

// TestAlertPluginDecides drives alerts through the pinned alert-rule plugin:
// an invalid expression or configuration is refused at creation with 422, a
// matching article gives exactly one signed webhook with the plugin's
// evidence, and a non-matching article gives none.
func TestAlertPluginDecides(t *testing.T) {
	id, _ := alertEvaluator(t)
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	receiver := startReceiver(t)
	run := monitoringRun()
	c := changeCorpus(t, "alert-plugin-"+run)
	start := request(t, "GET", changesPath(c, "", 0), admin, nil, 200)["next_cursor"].(string)

	// The plugin's declared schemas judge what a Subscription pins.
	_, refused := alertSubscription(t, "empty-"+run, c, map[string]any{"kind": "substring", "text": ""}, nil, destinationCapture, 422)
	if refused["code"] != "invalid_expression" || refused["field"] != "/saved_query_version_id" || !strings.Contains(refused["message"].(string), "/expression") {
		t.Fatal("invalid expression", refused)
	}
	_, refused = alertSubscription(t, "config-"+run, c, map[string]any{"kind": "substring", "text": "x"}, map[string]any{"case_sensitive": "yes"}, destinationCapture, 422)
	if refused["code"] != "invalid_subscription_configuration" || refused["field"] != "/evaluator/configuration/case_sensitive" {
		t.Fatal("invalid configuration", refused)
	}

	phrase := "grève-" + run
	_, sub := alertSubscription(t, "phrase-"+run, c, map[string]any{"kind": "substring", "text": phrase}, nil, destinationCapture, 201)
	subID := sub["subscription_id"].(string)
	// A second Subscription with the same expression is decided by the same evaluation.
	_, twin := alertSubscription(t, "twin-"+run, c, map[string]any{"kind": "substring", "text": phrase}, nil, destinationA, 201)

	calm := awaitReady(t, ingest(t, c, "alert-calm-"+run, "Rien à signaler sur le port "+run))
	hit := awaitReady(t, ingest(t, c, "alert-hit-"+run, "Les dockers votent la "+strings.ToUpper(phrase[:1])+phrase[1:]+" au port"))
	seen, _ := awaitMatches(t, admin, c, start, 2)
	// Let any late decision of the earlier, non-matching article surface.
	time.Sleep(3 * time.Second)
	seen, _ = drain(t, admin, c, start, 0)
	for _, s := range []string{subID, twin["subscription_id"].(string)} {
		created := matchCreatedFor(seen, s)
		if len(created) != 1 || created[0]["monitoring"].(map[string]any)["record_id"] != hit["record_id"] {
			t.Fatalf("want exactly one Match for the matching article on %s, got %v (calm record %v)", s, created, calm["record_id"])
		}
	}
	notice := matchCreatedFor(seen, subID)[0]
	refs := notice["monitoring"].(map[string]any)
	match := request(t, "GET", "/v0/matches/"+refs["match_id"].(string), admin, nil, 200)
	evidence := match["evidence"].(map[string]any)
	if evidence["evaluator"].(map[string]any)["plugin_id"] != id || !strings.Contains(evidence["explanation"].(string), phrase) || len(evidence["part_keys"].([]any)) == 0 {
		t.Fatal("Match evidence comes from the plugin", evidence)
	}
	awaitDelivery(t, admin, refs["delivery_id"].(string), func(d map[string]any) bool { return d["state"] == "delivered" })
	webhooks := 0
	for _, capture := range receiver.snapshot() {
		var body map[string]any
		if json.Unmarshal(capture.Body, &body) == nil && body["type"] == "match.created" && body["references"].(map[string]any)["subscription_id"] == subID {
			webhooks++
			if err := verifyWebhook(receiver.key, capture.Header.Get("webhook-id"), capture.Header.Get("webhook-timestamp"), capture.Header.Get("webhook-signature"), capture.Body, time.Now()); err != nil {
				t.Fatal("webhook is not authentic", err)
			}
		}
	}
	if webhooks != 1 {
		t.Fatalf("want exactly one signed webhook, got %d", webhooks)
	}
}

// TestAlertPluginMetadataRuleMatches proves a rule can test article metadata:
// only the article whose provenance names the producer matches.
func TestAlertPluginMetadataRuleMatches(t *testing.T) {
	alertEvaluator(t)
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := monitoringRun()
	c := changeCorpus(t, "alert-metadata-"+run)
	start := request(t, "GET", changesPath(c, "", 0), admin, nil, 200)["next_cursor"].(string)
	producer := "desk-" + run
	_, sub := alertSubscription(t, "producer-"+run, c, map[string]any{"kind": "metadata", "pointer": "/provenance/producer", "equals": producer}, nil, destinationA, 201)
	subID := sub["subscription_id"].(string)

	other := inlineCommand(c, "alert-other-desk-"+run, "other-desk-"+run, "Communiqué d'un autre producteur")
	other["provenance"] = map[string]any{"producer": "another-desk"}
	awaitReady(t, request(t, "POST", "/v0/records", admin, other, 202)["receipt_id"].(string))
	ours := inlineCommand(c, "alert-our-desk-"+run, "our-desk-"+run, "Communiqué du producteur suivi")
	ours["provenance"] = map[string]any{"producer": producer}
	hit := awaitReady(t, request(t, "POST", "/v0/records", admin, ours, 202)["receipt_id"].(string))

	awaitMatches(t, admin, c, start, 1)
	time.Sleep(3 * time.Second)
	seen, _ := drain(t, admin, c, start, 0)
	created := matchCreatedFor(seen, subID)
	if len(created) != 1 || created[0]["monitoring"].(map[string]any)["record_id"] != hit["record_id"] {
		t.Fatal("the metadata rule must match only the producer's article", created)
	}
}

func alertOutagePath() string {
	return filepath.Join(os.Getenv("QUIVR_TEST_CAPTURES"), "alert-plugin-outage.json")
}

// TestAlertPluginOutageDelays runs while the alert-rule plugin is stopped:
// a Subscription can still be created, a matching article becomes searchable,
// and no Match appears, because unavailability is never a decision.
func TestAlertPluginOutageDelays(t *testing.T) {
	alertEvaluator(t)
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := monitoringRun()
	c := changeCorpus(t, "alert-outage-"+run)
	start := request(t, "GET", changesPath(c, "", 0), admin, nil, 200)["next_cursor"].(string)
	phrase := "panne-" + run
	_, sub := alertSubscription(t, "outage-"+run, c, map[string]any{"kind": "substring", "text": phrase}, nil, destinationCapture, 201)
	hit := awaitReady(t, ingest(t, c, "alert-outage-"+run, "Alerte "+phrase+" sur le réseau"))
	time.Sleep(5 * time.Second)
	seen, _ := drain(t, admin, c, start, 0)
	if created := matchCreatedFor(seen, sub["subscription_id"].(string)); len(created) != 0 {
		t.Fatal("a Match was decided while the plugin was down", created)
	}
	if items := request(t, "GET", matchesPath(sub["subscription_id"].(string), "", 0), admin, nil, 200)["items"].([]any); len(items) != 0 {
		t.Fatal("Match history during the outage", items)
	}
	state, _ := json.Marshal(map[string]any{"corpus_id": c, "cursor": start, "subscription_id": sub["subscription_id"], "record_id": hit["record_id"]})
	if err := os.WriteFile(alertOutagePath(), state, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestAlertPluginOutageRecovers runs after the plugin restarted: the delayed
// evaluation completes with exactly one Match and one signed webhook.
func TestAlertPluginOutageRecovers(t *testing.T) {
	alertEvaluator(t)
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	receiver := startReceiver(t)
	raw, err := os.ReadFile(alertOutagePath())
	if err != nil {
		t.Fatal("run TestAlertPluginOutageDelays first:", err)
	}
	var state map[string]string
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	awaitMatches(t, admin, state["corpus_id"], state["cursor"], 1)
	seen, _ := drain(t, admin, state["corpus_id"], state["cursor"], 0)
	created := matchCreatedFor(seen, state["subscription_id"])
	if len(created) != 1 || created[0]["monitoring"].(map[string]any)["record_id"] != state["record_id"] {
		t.Fatal("the delayed evaluation must complete with one Match", created)
	}
	delivery := created[0]["monitoring"].(map[string]any)["delivery_id"].(string)
	awaitDelivery(t, admin, delivery, func(d map[string]any) bool { return d["state"] == "delivered" })
	webhooks := 0
	for _, capture := range receiver.snapshot() {
		if capture.Header.Get("webhook-id") == created[0]["event_id"] && capture.Status == 204 {
			webhooks++
		}
	}
	if webhooks != 1 {
		t.Fatalf("want one acknowledged webhook after recovery, got %d", webhooks)
	}
}
