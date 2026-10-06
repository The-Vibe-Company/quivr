package acceptance

import (
	"fmt"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"
)

// Fixture markers pinned in the Subscription's evaluator configuration.
const (
	markerMatch    = "ALERTE"
	markerNegative = "CALME"
	markerPending  = "ATTENTE"
	markerEnriched = "VECTEUR"
)

func fixtureSubscription(key string, query map[string]any) map[string]any {
	body := subscriptionCommand(key, query, destinationA)
	body["evaluator"].(map[string]any)["configuration"] = map[string]any{"decisions": map[string]any{
		markerMatch: "match", markerNegative: "no_match", markerPending: "not_ready", markerEnriched: "match_after_enrichment",
	}}
	return body
}

// monitoringWait bounds eventual processing. Monitoring acceptance runs after
// the timed scenarios and change/catalog tests, whose ingestion and enrichment
// backlog the shared worker is still draining.
const monitoringWait = 3 * time.Minute

// ingest submits inline text and returns its Receipt id.
func ingest(t *testing.T, corpusID, key, text string) string {
	t.Helper()
	return request(t, "POST", "/v0/records", os.Getenv("QUIVR_TEST_ADMIN"), inlineCommand(corpusID, key, key, text), 202)["receipt_id"].(string)
}

// awaitReady waits until a Receipt resolves and its Version is searchable.
func awaitReady(t *testing.T, receiptID string) map[string]any {
	t.Helper()
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	deadline := time.Now().Add(monitoringWait)
	for {
		r := request(t, "GET", "/v0/ingestion-receipts/"+receiptID, admin, nil, 200)
		if r["state"] == "resolved" {
			v := request(t, "GET", "/v0/records/"+r["record_id"].(string)+"/versions/"+r["version_id"].(string), admin, nil, 200)
			if v["availability"].(map[string]any)["searchable"] == true {
				return r
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Version never searchable: %v", r)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// ingestSearchable ingests inline text and waits until its Version is searchable.
func ingestSearchable(t *testing.T, corpusID, key, text string) map[string]any {
	t.Helper()
	return awaitReady(t, ingest(t, corpusID, key, text))
}

// awaitEvaluated waits for every Subscription asked about this Version to
// decide. A later Record's Match cannot order concurrent evaluations.
func awaitEvaluated(t *testing.T, receipt map[string]any) {
	t.Helper()
	path := "/v0/records/" + receipt["record_id"].(string) + "/versions/" + receipt["version_id"].(string)
	deadline := time.Now().Add(monitoringWait)
	for {
		version := request(t, "GET", path, os.Getenv("QUIVR_TEST_ADMIN"), nil, 200)
		if steps, ok := version["steps"].(map[string]any); ok && steps["evaluated_at"] != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("evaluation never decided for %s: %v", path, version)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// awaitEnriched waits for the Record's enrichment event in the feed after cursor.
func awaitEnriched(t *testing.T, token, corpusID, cursor, recordID string) {
	t.Helper()
	var seen []map[string]any
	deadline := time.Now().Add(monitoringWait)
	for typed(seen, "record.enrichment_available", recordID) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no enrichment for %s", recordID)
		}
		items, next := drain(t, token, corpusID, cursor, 0)
		seen, cursor = append(seen, items...), next
		time.Sleep(200 * time.Millisecond)
	}
}

func matchesPath(subscriptionID, pageCursor string, limit int) string {
	q := url.Values{"subscription_id": {subscriptionID}}
	if pageCursor != "" {
		q.Set("page_cursor", pageCursor)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	return "/v0/matches?" + q.Encode()
}

// awaitMatches polls the feed until n match.created events arrived.
func awaitMatches(t *testing.T, token, corpusID, cursor string, n int) ([]map[string]any, []map[string]any) {
	t.Helper()
	var seen, created []map[string]any
	deadline := time.Now().Add(monitoringWait)
	for {
		items, next := drain(t, token, corpusID, cursor, 0)
		seen, cursor = append(seen, items...), next
		created = created[:0]
		for _, item := range seen {
			if item["type"] == "match.created" {
				created = append(created, item)
			}
		}
		if len(created) >= n {
			return seen, created
		}
		if time.Now().After(deadline) {
			t.Fatalf("want %d match.created events; saw %v", n, seen)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func eventPosition(items []map[string]any, kind, id string) int {
	for i, item := range items {
		if item["type"] == kind && item["resource"].(map[string]any)["id"] == id {
			return i
		}
	}
	return -1
}

// TestMonitoringMatchesEvaluateLaterEligibleVersions drives the public
// evaluation journey: no backfill, positive/negative fixture
// decisions, an enrichment-gated Match, the shared notice identity in polling,
// SSE and the pending Delivery, and disable stopping new Matches.
func TestMonitoringMatchesEvaluateLaterEligibleVersions(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin, other, scoped := os.Getenv("QUIVR_TEST_ADMIN"), os.Getenv("QUIVR_TEST_OTHER"), os.Getenv("QUIVR_TEST_SCOPED")
	run := monitoringRun()
	c := changeCorpus(t, "matches-"+run)
	start := request(t, "GET", changesPath(c, "", 0), admin, nil, 200)["next_cursor"].(string)

	// Content made searchable and enriched before activation is never evaluated.
	before := ingestSearchable(t, c, "before-"+run, "Dépêche "+markerMatch+" antérieure")
	awaitEnriched(t, admin, c, start, before["record_id"].(string))

	query := request(t, "POST", "/v0/saved-queries", admin, savedQueryCommand("matches-"+run, c), 201)
	sub := request(t, "POST", "/v0/subscriptions", admin, fixtureSubscription("matches-"+run, query), 201)
	subID := sub["subscription_id"].(string)
	subVersion := sub["current_version"].(map[string]any)["version_id"].(string)

	positiveID := ingest(t, c, "positive-"+run, "Dépêche "+markerMatch+" séisme")
	negativeID := ingest(t, c, "negative-"+run, "Dépêche "+markerNegative+" météo")
	enrichedID := ingest(t, c, "enriched-"+run, "Dépêche "+markerEnriched+" portuaire")
	positive, negative, enriched := awaitReady(t, positiveID), awaitReady(t, negativeID), awaitReady(t, enrichedID)

	feed, created := awaitMatches(t, admin, c, start, 2)
	byRecord := map[string]map[string]any{}
	for _, event := range created {
		refs := event["monitoring"].(map[string]any)
		if event["resource"].(map[string]any)["kind"] != "match" || event["resource"].(map[string]any)["id"] != refs["match_id"] || refs["subscription_id"] != subID || refs["subscription_version_id"] != subVersion {
			t.Fatal("match.created must reference its Match and pinned Subscription Version", event)
		}
		byRecord[refs["record_id"].(string)] = event
	}
	for _, r := range []map[string]any{positive, enriched} {
		event := byRecord[r["record_id"].(string)]
		if event == nil || event["monitoring"].(map[string]any)["record_version_id"] != r["version_id"] {
			t.Fatal("missing Match for a positive Version", r, created)
		}
	}
	// The enrichment-gated decision can only match once enrichment has committed.
	enrichedRecord := enriched["record_id"].(string)
	enrichedMatch := byRecord[enrichedRecord]["resource"].(map[string]any)["id"].(string)
	if !(eventPosition(feed, "record.enrichment_available", enrichedRecord) >= 0 && eventPosition(feed, "record.enrichment_available", enrichedRecord) < eventPosition(feed, "match.created", enrichedMatch)) {
		t.Fatal("enrichment-gated Match committed before enrichment", feed)
	}

	// Wait for the negative Version's actual decision before asserting absence.
	// Permanent not_ready decisions are owned by the engine and SQL suites;
	// they intentionally have no public evaluated_at completion signal.
	awaitEnriched(t, admin, c, start, negative["record_id"].(string))
	awaitEvaluated(t, negative)
	// A third positive Version also exercises Match pagination below.
	sentinel := ingestSearchable(t, c, "sentinel-"+run, "Dépêche "+markerMatch+" sentinelle")
	awaitEnriched(t, admin, c, start, sentinel["record_id"].(string))
	awaitMatches(t, admin, c, start, 3)
	list := request(t, "GET", matchesPath(subID, "", 0), admin, nil, 200)["items"].([]any)
	if len(list) != 3 {
		t.Fatal("exactly the three positive Versions match, without duplicates or backfill", list)
	}
	for _, item := range list {
		m := item.(map[string]any)
		if id := m["record_id"]; id == before["record_id"] || id == negative["record_id"] {
			t.Fatal("non-positive or pre-activation Record matched", m)
		}
	}
	_, more := awaitMatches(t, admin, c, start, 3)
	if len(more) != 3 {
		t.Fatal("duplicate match.created facts", more)
	}
	for _, event := range more {
		byRecord[event["monitoring"].(map[string]any)["record_id"].(string)] = event
	}
	if byRecord[sentinel["record_id"].(string)] == nil {
		t.Fatal("sentinel Match missing", more)
	}

	// Stable keyset pages.
	first := request(t, "GET", matchesPath(subID, "", 2), admin, nil, 200)
	next, ok := first["next_page_cursor"].(string)
	if !ok || len(first["items"].([]any)) != 2 {
		t.Fatal("first page", first)
	}
	second := request(t, "GET", matchesPath(subID, next, 2), admin, nil, 200)
	if items := second["items"].([]any); len(items) != 1 || second["next_page_cursor"] != nil || reflect.DeepEqual(first["items"].([]any)[1], items[0]) {
		t.Fatal("second page", second)
	}

	// Match read, Delivery and SSE share the committed notice identity. The
	// destination does not listen, so the Delivery keeps retrying and is never
	// delivered.
	event := byRecord[positive["record_id"].(string)]
	refs := event["monitoring"].(map[string]any)
	match := request(t, "GET", "/v0/matches/"+refs["match_id"].(string), admin, nil, 200)
	evidence := match["evidence"].(map[string]any)
	if match["record_version_id"] != positive["version_id"] || match["saved_query_version_id"] != query["current_version"].(map[string]any)["version_id"] ||
		evidence["evaluator"].(map[string]any)["plugin_id"] != "quivr.fixture" || evidence["explanation"] == "" || !reflect.DeepEqual(evidence["part_keys"], []any{"body"}) {
		t.Fatal("Match provenance and bounded explanation", match)
	}
	delivery := request(t, "GET", "/v0/deliveries/"+refs["delivery_id"].(string), admin, nil, 200)
	notice := delivery["event"].(map[string]any)
	// Nothing listens on destinationA: the Delivery retries within the shortened
	// window and may already be exhausted, but it is never delivered.
	retrying := (delivery["state"] == "pending" || delivery["state"] == "delivering") && delivery["admission"].(map[string]any)["allowed"] == true
	exhausted := delivery["state"] == "exhausted" && delivery["admission"].(map[string]any)["reason"] == "terminal"
	if !(retrying || exhausted) || delivery["match_id"] != refs["match_id"] || delivery["destination_id"] != destinationA ||
		notice["event_id"] != event["event_id"] || notice["type"] != "match.created" ||
		notice["occurred_at"] != event["occurred_at"] || !reflect.DeepEqual(notice["references"], refs) {
		t.Fatal("pending Delivery must carry the feed notice identity", delivery, event)
	}
	// Resume SSE from the pre-activation cursor: it replays the same notices.
	stream := openChangeStream(t, os.Getenv("QUIVR_TEST_URL"), admin, "/v0/changes/stream?corpus_id="+url.QueryEscape(c), start)
	streamed := map[string]bool{}
	for len(streamed) < 3 {
		_, change := stream.nextChange(t)
		if change["type"] == "match.created" {
			if !reflect.DeepEqual(change["monitoring"], byRecord[change["monitoring"].(map[string]any)["record_id"].(string)]["monitoring"]) {
				t.Fatal("SSE and polling notice references differ", change)
			}
			streamed[change["event_id"].(string)] = true
		}
	}
	stream.close()
	if !streamed[event["event_id"].(string)] {
		t.Fatal("SSE did not deliver the polled notice identity", streamed)
	}

	// Other Organizations and ungranted Corpus scopes cannot see Match history.
	for _, token := range []string{other, scoped} {
		request(t, "GET", "/v0/matches/"+refs["match_id"].(string), token, nil, 404)
		request(t, "GET", "/v0/deliveries/"+refs["delivery_id"].(string), token, nil, 404)
		request(t, "GET", matchesPath(subID, "", 0), token, nil, 404)
	}
	request(t, "GET", "/v0/matches/"+refs["match_id"].(string), os.Getenv("QUIVR_TEST_READER"), nil, 403)

	// Disabling stops new Matches (later triggers are no longer dispatched; the
	// commit-time recheck is proven by the adapter test) and shows in the
	// Delivery admission view.
	request(t, "POST", "/v0/subscriptions/"+subID+"/disable", admin, map[string]any{"idempotency_key": "matches-disable-" + run}, 200)
	// An active control evaluates the same late Version, so the absence check
	// cannot pass merely because its trigger has not been dispatched yet.
	control := request(t, "POST", "/v0/subscriptions", admin, fixtureSubscription("matches-control-"+run, query), 201)
	late := ingestSearchable(t, c, "late-"+run, "Dépêche "+markerMatch+" tardive")
	awaitEnriched(t, admin, c, start, late["record_id"].(string))
	awaitEvaluated(t, late)
	controlMatches := request(t, "GET", matchesPath(control["subscription_id"].(string), "", 0), admin, nil, 200)["items"].([]any)
	if len(controlMatches) != 1 || controlMatches[0].(map[string]any)["record_version_id"] != late["version_id"] {
		t.Fatal("active control must match the late Version", controlMatches)
	}
	if items := request(t, "GET", matchesPath(subID, "", 0), admin, nil, 200)["items"].([]any); len(items) != 3 {
		t.Fatal("disabled Subscription committed a new Match", items)
	}
	delivery = request(t, "GET", "/v0/deliveries/"+refs["delivery_id"].(string), admin, nil, 200)
	// A Delivery already exhausted before the disable reports terminal instead.
	want := "subscription_disabled"
	if delivery["state"] == "exhausted" {
		want = "terminal"
	}
	if admission := delivery["admission"].(map[string]any); admission["allowed"] != false || admission["reason"] != want || delivery["state"] == "delivered" {
		t.Fatal("disabled admission view", delivery)
	}
}
