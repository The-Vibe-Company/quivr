package acceptance

import (
	"encoding/json"
	"os"
	"testing"
)

// Marker the edited Subscription Version matches instead of markerCorrectionMatch.
const markerEdited = "NOUVEAU"

// webhooksOf counts the captured webhooks referencing a Subscription.
func webhooksOf(t *testing.T, receiver *captureReceiver, subscriptionID string) int {
	t.Helper()
	n := 0
	for _, capture := range receiver.snapshot() {
		var body struct {
			References map[string]any `json:"references"`
		}
		if json.Unmarshal(capture.Body, &body) == nil && body.References["subscription_id"] == subscriptionID {
			n++
		}
	}
	return n
}

// createdFor returns the match.created notice of a Subscription for a Record.
func createdFor(t *testing.T, notices []map[string]any, subscriptionID, recordID string) map[string]any {
	t.Helper()
	for _, n := range notices {
		if refs := refsOf(n); refs["subscription_id"] == subscriptionID && refs["record_id"] == recordID {
			return n
		}
	}
	t.Fatalf("no match.created for %s on %s in %v", subscriptionID, recordID, notices)
	return nil
}

// TestMonitoringEditAndDelete drives THE-724 through the public API. An
// alerted Record is corrected after its Subscription was edited so the old
// rule no longer applies: the new Version decides (match.no_longer_matches)
// while the old Match keeps its Version. A later Record matches under the new
// Version. After deletion no webhook arrives for the Subscription, while an
// untouched Subscription on the same Corpus keeps alerting, and the history
// stays readable. Replays return the same results.
func TestMonitoringEditAndDelete(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	receiver := startReceiver(t)
	s := newNoticeScenario(t, "edit", "Dépêche "+markerCorrectionMatch,
		map[string]any{markerCorrectionMatch: "match"},
		map[string]any{"default": "match"})
	edited, sentinel := s.subscriptions[0], s.subscriptions[1]
	for _, sub := range s.subscriptions {
		deliveredAsPolled(t, receiver, s.created[sub])
	}
	old := refsOf(s.created[edited])
	subscription := request(t, "GET", "/v0/subscriptions/"+edited, admin, nil, 200)
	first := subscription["current_version"].(map[string]any)
	queryID := first["saved_query_id"].(string)

	// Edit the Saved Query: the Subscription keeps its pinned Version.
	queryEdit := map[string]any{"idempotency_key": "edit-query-" + s.recordKey, "definition": map[string]any{
		"corpus_ids": []any{s.corpus}, "expression": map[string]any{"fixture": map[string]any{"decision": "match", "terms": []any{"édition"}}},
		"retrieval_profile": "default", "temporal_policy": "from_activation"}}
	queryV2 := request(t, "POST", "/v0/saved-queries/"+queryID+"/versions", admin, queryEdit, 201)
	if replay := request(t, "POST", "/v0/saved-queries/"+queryID+"/versions", admin, queryEdit, 201); replay["version_id"] != queryV2["version_id"] {
		t.Fatal("saved query edit replay", replay, queryV2)
	}
	if still := request(t, "GET", "/v0/subscriptions/"+edited, admin, nil, 200)["current_version"].(map[string]any); still["version_id"] != first["version_id"] {
		t.Fatal("a Saved Query edit moved the Subscription", still)
	}

	// Edit the Subscription: the old rule no longer matches, a new marker does.
	subEdit := map[string]any{"idempotency_key": "edit-subscription-" + s.recordKey, "saved_query_version_id": queryV2["version_id"],
		"evaluator":      map[string]any{"plugin_id": "quivr.fixture", "version": "1", "configuration": map[string]any{"decisions": map[string]any{markerEdited: "match", "default": "no_match"}}},
		"destination_id": destinationCapture}
	second := request(t, "POST", "/v0/subscriptions/"+edited+"/versions", admin, subEdit, 201)
	if replay := request(t, "POST", "/v0/subscriptions/"+edited+"/versions", admin, subEdit, 201); replay["version_id"] != second["version_id"] {
		t.Fatal("subscription edit replay", replay, second)
	}
	if second["version_id"] == first["version_id"] || second["saved_query_version_id"] != queryV2["version_id"] {
		t.Fatal("new Subscription Version", second)
	}
	feed, _ := drain(t, admin, s.corpus, s.cursor, 0)
	if typed(feed, "saved_query.updated", queryID) != 1 || typed(feed, "subscription.updated", edited) != 1 {
		t.Fatal("edits on the feed", feed)
	}

	// A correction of the alerted Record is judged by the new Version only.
	awaitReady(t, ingestCorrection(t, s.corpus, s.recordKey+"-2", s.recordKey, "Dépêche "+markerCorrectionMatch+" corrigée"))
	notices := s.awaitNotices(t, map[string]int{"match.no_longer_matches": 1, "match.corrected": 1})
	invalidation := refsOf(notices["match.no_longer_matches"][0])
	if invalidation["subscription_id"] != edited || invalidation["match_id"] != old["match_id"] || invalidation["subscription_version_id"] != first["version_id"] {
		t.Fatal("the new Version decides; the notice references the old Match and its Version", invalidation)
	}
	deliveredAsPolled(t, receiver, notices["match.no_longer_matches"][0])
	if corrected := refsOf(notices["match.corrected"][0]); corrected["subscription_id"] != sentinel {
		t.Fatal("only the untouched Subscription still matches the correction", corrected)
	}
	if m := request(t, "GET", "/v0/matches/"+old["match_id"].(string), admin, nil, 200); m["subscription_version_id"] != first["version_id"] {
		t.Fatal("the old Match keeps its Version", m)
	}
	request(t, "GET", "/v0/subscriptions/"+edited+"/versions/"+first["version_id"].(string), admin, nil, 200)

	// A later Record matches under the new Version.
	fresh := awaitReady(t, ingestCorrection(t, s.corpus, s.recordKey+"-fresh", s.recordKey+"-fresh", "Dépêche "+markerEdited))["record_id"].(string)
	created := createdFor(t, s.awaitNotices(t, map[string]int{"match.created": 4})["match.created"], edited, fresh)
	if refsOf(created)["subscription_version_id"] != second["version_id"] {
		t.Fatal("a later Match names the new Version", created)
	}
	deliveredAsPolled(t, receiver, created)

	// Delete: replayed, permanent, and no further webhook for it.
	del := map[string]any{"idempotency_key": "delete-" + s.recordKey}
	if deleted := request(t, "POST", "/v0/subscriptions/"+edited+"/delete", admin, del, 200); deleted["deleted"] != true || deleted["enabled"] != false {
		t.Fatal("delete", deleted)
	}
	if replay := request(t, "POST", "/v0/subscriptions/"+edited+"/delete", admin, del, 200); replay["deleted"] != true {
		t.Fatal("delete replay", replay)
	}
	if refused := request(t, "POST", "/v0/subscriptions/"+edited+"/enable", admin, map[string]any{"idempotency_key": "enable-" + s.recordKey}, 409); refused["code"] != "subscription_deleted" {
		t.Fatal("enable after delete", refused)
	}
	if refused := request(t, "POST", "/v0/saved-queries/"+queryID+"/delete", admin, map[string]any{"idempotency_key": "delete-query-" + s.recordKey}, 409); refused["code"] != "saved_query_in_use" {
		t.Fatal("delete of a Saved Query still in use", refused)
	}
	before := webhooksOf(t, receiver, edited)
	later := awaitReady(t, ingestCorrection(t, s.corpus, s.recordKey+"-later", s.recordKey+"-later", "Dépêche "+markerEdited+" suivante"))["record_id"].(string)
	// The untouched Subscription is dispatched on the same change and alerted.
	deliveredAsPolled(t, receiver, createdFor(t, s.awaitNotices(t, map[string]int{"match.created": 5})["match.created"], sentinel, later))
	if n := webhooksOf(t, receiver, edited); n != before {
		t.Fatalf("webhooks for the deleted Subscription: %d, then %d", before, n)
	}
	history := request(t, "GET", matchesPath(edited, "", 0), admin, nil, 200)["items"].([]any)
	if len(history) != 2 {
		t.Fatal("Match history of the deleted Subscription", history)
	}
	if read := request(t, "GET", "/v0/subscriptions/"+edited, admin, nil, 200); read["deleted"] != true {
		t.Fatal("deleted Subscription read", read)
	}
	request(t, "GET", "/v0/deliveries/"+refsOf(created)["delivery_id"].(string), admin, nil, 200)
	if feed, _ = drain(t, admin, s.corpus, s.cursor, 0); typed(feed, "subscription.deleted", edited) != 1 {
		t.Fatal("subscription.deleted on the feed", feed)
	}

	// Once no Subscription uses it, the Saved Query can be deleted.
	request(t, "POST", "/v0/subscriptions/"+sentinel+"/delete", admin, map[string]any{"idempotency_key": "delete-sentinel-" + s.recordKey}, 200)
	if deleted := request(t, "POST", "/v0/saved-queries/"+queryID+"/delete", admin, map[string]any{"idempotency_key": "delete-query-" + s.recordKey}, 200); deleted["deleted"] != true {
		t.Fatal("saved query delete", deleted)
	}
}
