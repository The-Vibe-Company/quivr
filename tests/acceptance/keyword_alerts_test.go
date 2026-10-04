package acceptance

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// keywordEvaluator is the first-party keyword alerts plugin plugins/alerts
// that scripts/subscription_plugin.py pins in every stack. The tests skip
// without it.
func keywordEvaluator(t *testing.T) (string, string) {
	t.Helper()
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	id, version, ok := strings.Cut(os.Getenv("QUIVR_TEST_KEYWORD_EVALUATOR"), "@")
	if !ok {
		t.Skip("QUIVR_TEST_KEYWORD_EVALUATOR is set by scripts/subscription_plugin.py")
	}
	return id, version
}

func keywordSubscription(t *testing.T, key, corpusID string, match map[string]any, destination string, want int) (map[string]any, map[string]any) {
	t.Helper()
	id, version := keywordEvaluator(t)
	return pinnedSubscription(t, id, version, key, corpusID, map[string]any{"kind": "keywords", "match": match}, nil, destination, want)
}

func term(text string) map[string]any { return map[string]any{"term": text} }

// TestKeywordAlertsExplainMatchedTerms saves the query
// `<word> AND (grève OR strike) NOT sport` through the public API. Of three
// articles, only the one that satisfies it alerts, although it writes GREVE
// without the accent and in capitals: exactly one signed webhook, and the
// Match evidence names the matched terms and their Parts. A malformed query
// tree is refused at creation.
func TestKeywordAlertsExplainMatchedTerms(t *testing.T) {
	id, _ := keywordEvaluator(t)
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	receiver := startReceiver(t)
	run := monitoringRun()
	c := changeCorpus(t, "keyword-alerts-"+run)
	start := request(t, "GET", changesPath(c, "", 0), admin, nil, 200)["next_cursor"].(string)

	_, refused := keywordSubscription(t, "empty-group-"+run, c, map[string]any{"all": []any{}}, destinationCapture, 422)
	if refused["code"] != "invalid_expression" || refused["field"] != "/saved_query_version_id" {
		t.Fatal("a malformed query tree must be refused", refused)
	}

	word := "kw" + run
	query := map[string]any{"all": []any{term(word), map[string]any{"any": []any{term("grève"), term("strike")}}, map[string]any{"not": term("sport")}}}
	_, sub := keywordSubscription(t, "query-"+run, c, query, destinationCapture, 201)
	subID := sub["subscription_id"].(string)

	awaitReady(t, ingest(t, c, "keyword-sport-"+run, "Grève au club de sport "+word))
	awaitReady(t, ingest(t, c, "keyword-other-"+run, "Réunion sans incident "+word))
	hit := awaitReady(t, ingest(t, c, "keyword-hit-"+run, "Les dockers ("+word+") votent la GREVE au port"))
	awaitMatches(t, admin, c, start, 1)
	// Let any late decision of the earlier, non-matching articles surface.
	time.Sleep(3 * time.Second)
	seen, _ := drain(t, admin, c, start, 0)
	created := matchCreatedFor(seen, subID)
	if len(created) != 1 || created[0]["monitoring"].(map[string]any)["record_id"] != hit["record_id"] {
		t.Fatalf("want exactly one Match, for the matching article, got %v", created)
	}
	refs := created[0]["monitoring"].(map[string]any)
	evidence := request(t, "GET", "/v0/matches/"+refs["match_id"].(string), admin, nil, 200)["evidence"].(map[string]any)
	explanation, _ := evidence["explanation"].(string)
	if evidence["evaluator"].(map[string]any)["plugin_id"] != id || !strings.Contains(explanation, `"`+word+`" in `) || !strings.Contains(explanation, `"grève" in `) ||
		strings.Contains(explanation, "strike") || strings.Contains(explanation, "sport") || len(evidence["part_keys"].([]any)) == 0 {
		t.Fatal("the evidence must name the matched terms and their Parts", evidence)
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

// TestKeywordAlertsFilterAloneAlerts proves "every new article from this
// source" is a valid keyword alert: a field filter with no term matches only
// the article of that Source Namespace, and the evidence names the field.
func TestKeywordAlertsFilterAloneAlerts(t *testing.T) {
	keywordEvaluator(t)
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := monitoringRun()
	c := changeCorpus(t, "keyword-filter-"+run)
	start := request(t, "GET", changesPath(c, "", 0), admin, nil, 200)["next_cursor"].(string)
	namespace := "wire-" + run
	_, sub := keywordSubscription(t, "source-"+run, c, map[string]any{"field": "source", "equals": namespace}, destinationA, 201)
	subID := sub["subscription_id"].(string)

	awaitReady(t, ingest(t, c, "keyword-feed-"+run, "Dépêche d'un autre fil"))
	ours := inlineCommand(c, "keyword-wire-"+run, "story-"+run, "Dépêche du fil suivi")
	ours["source"].(map[string]any)["namespace"] = namespace
	hit := awaitReady(t, request(t, "POST", "/v0/records", admin, ours, 202)["receipt_id"].(string))

	awaitMatches(t, admin, c, start, 1)
	time.Sleep(3 * time.Second)
	seen, _ := drain(t, admin, c, start, 0)
	created := matchCreatedFor(seen, subID)
	if len(created) != 1 || created[0]["monitoring"].(map[string]any)["record_id"] != hit["record_id"] {
		t.Fatal("the source filter must match only that source's article", created)
	}
	evidence := request(t, "GET", "/v0/matches/"+created[0]["monitoring"].(map[string]any)["match_id"].(string), admin, nil, 200)["evidence"].(map[string]any)
	if evidence["explanation"] != `Matched source "`+namespace+`".` {
		t.Fatal("the evidence must name the field and its value", evidence)
	}
}
