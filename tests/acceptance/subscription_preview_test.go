package acceptance

import (
	"os"
	"strings"
	"testing"
)

// TestSubscriptionPreviewJudgesRecentRecordsWithoutSaving previews alerts on
// articles accepted before any Subscription exists. A keyword preview finds
// the one article with its word, with the evidence a Match would carry. A
// described preview limited to two articles asks the classifier about the two
// newest only, once each, and finds the one that fits. An expression the
// plugin refuses is 422, as at creation. Previews save nothing: the Corpus
// journal gets no Saved Query, Subscription or Match event.
func TestSubscriptionPreviewJudgesRecentRecordsWithoutSaving(t *testing.T) {
	fake := fakeSystemOne(t)
	id, version := keywordEvaluator(t)
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := monitoringRun()
	c := changeCorpus(t, "subscription-preview-"+run)
	awaitReady(t, ingest(t, c, "preview-en-"+run, "Harbour staff walk out ("+run+"). Dockers at the northern harbour began a walkout on Tuesday."))
	other := awaitReady(t, ingest(t, c, "preview-other-"+run, "League final ends in a draw ("+run+"). The football final ended without a goal."))
	french := awaitReady(t, ingest(t, c, "preview-fr-"+run, "Les dockers votent la grève au port ("+run+"). Au port de Portval, les dockers ont voté une grève de 48 heures."))
	start := request(t, "GET", changesPath(c, "", 0), admin, nil, 200)["next_cursor"].(string)

	preview := func(expression, configuration map[string]any, limit int, want int) map[string]any {
		t.Helper()
		if configuration == nil {
			configuration = map[string]any{}
		}
		body := map[string]any{
			"definition": map[string]any{"corpus_ids": []string{c}, "expression": expression, "retrieval_profile": "default", "temporal_policy": "from_activation"},
			"evaluator":  map[string]any{"plugin_id": id, "version": version, "configuration": configuration},
		}
		if limit > 0 {
			body["limit"] = limit
		}
		return request(t, "POST", "/v0/subscription-previews", admin, body, want)
	}
	matched := func(p map[string]any) []string {
		var out []string
		for _, m := range p["matches"].([]any) {
			out = append(out, m.(map[string]any)["record_id"].(string))
		}
		return out
	}

	keywords := preview(map[string]any{"kind": "keywords", "match": term("grève")}, nil, 0, 200)
	got := matched(keywords)
	if keywords["evaluated"] != float64(3) || keywords["complete"] != true || len(got) != 1 || got[0] != french["record_id"] {
		t.Fatalf("the keyword preview must judge the 3 articles and find only %v: %v", french["record_id"], keywords)
	}
	evidence := keywords["matches"].([]any)[0].(map[string]any)["evidence"].(map[string]any)
	if !strings.Contains(evidence["explanation"].(string), `"grève" in `) || evidence["evaluator"].(map[string]any)["plugin_id"] != id {
		t.Fatal("a previewed match carries the evidence a Match would", evidence)
	}

	// Articles are judged whether or not they are enriched yet.
	described := preview(described(strikeDescription), map[string]any{"wait_for_enrichment": false}, 2, 200)
	got = matched(described)
	if described["evaluated"] != float64(2) || len(got) != 1 || got[0] != french["record_id"] {
		t.Fatalf("the described preview of the 2 newest articles (%v, %v) must find only the French one: %v", french["record_id"], other["record_id"], described)
	}
	calls := fakeRequests(t, fake, run)
	if len(calls) != 2 || fakeRequestsFor(calls, "Harbour staff") != 0 {
		t.Fatalf("want one classifier call for each of the 2 newest articles and none for the oldest, got %d: %v", len(calls), calls)
	}

	refused := preview(map[string]any{"kind": "keywords", "match": map[string]any{"all": []any{}}}, nil, 0, 422)
	if refused["code"] != "invalid_expression" {
		t.Fatal("an expression the plugin refuses must be refused as at creation", refused)
	}

	events, _ := drain(t, admin, c, start, 0)
	for _, e := range events {
		kind := e["type"].(string)
		if strings.HasPrefix(kind, "match.") || strings.HasPrefix(kind, "saved_query.") || strings.HasPrefix(kind, "subscription.") {
			t.Fatalf("a preview must save nothing, got the event %v", e)
		}
	}
}

// fakeRequestsFor counts the classifier calls about an article containing word.
func fakeRequestsFor(calls []map[string]any, word string) int {
	n := 0
	for _, call := range calls {
		if strings.Contains(call["article"].(string), word) {
			n++
		}
	}
	return n
}
