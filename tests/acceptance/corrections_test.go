package acceptance

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

// Markers of the correction scenarios, checked in sorted order by the fixture
// evaluator: the first marker present in the Version's text decides.
const (
	markerCorrectionMatch = "ALERTE"
	markerCorrectionCalm  = "CALME"
)

// noticeScenario is one Corpus with a Saved Query whose Subscriptions deliver
// to the capture receiver, and one alerted Record.
type noticeScenario struct {
	corpus, cursor, record, recordKey string
	subscriptions                     []string
	created                           map[string]map[string]any // match.created feed event by Subscription
}

// newNoticeScenario creates one Subscription per fixture decision map on the
// capture destination and ingests the first Version of one Record, returning
// once every Subscription's match.created notice was delivered.
func newNoticeScenario(t *testing.T, name, text string, decisions ...map[string]any) *noticeScenario {
	t.Helper()
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := monitoringRun()
	s := &noticeScenario{corpus: changeCorpus(t, name+"-"+run), recordKey: name + "-" + run, created: map[string]map[string]any{}}
	s.cursor = request(t, "GET", changesPath(s.corpus, "", 0), admin, nil, 200)["next_cursor"].(string)
	query := request(t, "POST", "/v0/saved-queries", admin, savedQueryCommand(name+"-"+run, s.corpus), 201)
	for i, d := range decisions {
		body := subscriptionCommand(name+"-"+string(rune('a'+i))+"-"+run, query, destinationCapture)
		body["evaluator"].(map[string]any)["configuration"] = map[string]any{"decisions": d}
		s.subscriptions = append(s.subscriptions, request(t, "POST", "/v0/subscriptions", admin, body, 201)["subscription_id"].(string))
	}
	s.record = awaitReady(t, ingestCorrection(t, s.corpus, s.recordKey+"-1", s.recordKey, text))["record_id"].(string)
	_, created := awaitMatches(t, admin, s.corpus, s.cursor, len(decisions))
	for _, event := range created {
		s.created[event["monitoring"].(map[string]any)["subscription_id"].(string)] = event
	}
	return s
}

// ingestCorrection submits a Version of recordKey under its own request key.
func ingestCorrection(t *testing.T, corpusID, key, recordKey, text string) string {
	t.Helper()
	return request(t, "POST", "/v0/records", os.Getenv("QUIVR_TEST_ADMIN"), inlineCommand(corpusID, key, recordKey, text), 202)["receipt_id"].(string)
}

// awaitNotices polls the feed from the scenario start until want[kind]
// notices of each kind arrived, returning them by kind.
func (s *noticeScenario) awaitNotices(t *testing.T, want map[string]int) map[string][]map[string]any {
	t.Helper()
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	cursor := s.cursor
	var seen []map[string]any
	deadline := time.Now().Add(monitoringWait)
	for {
		items, next := drain(t, admin, s.corpus, cursor, 0)
		seen, cursor = append(seen, items...), next
		byKind := map[string][]map[string]any{}
		for _, item := range seen {
			if item["monitoring"] != nil {
				byKind[item["type"].(string)] = append(byKind[item["type"].(string)], item)
			}
		}
		done := true
		for kind, n := range want {
			done = done && len(byKind[kind]) >= n
		}
		if done {
			return byKind
		}
		if time.Now().After(deadline) {
			t.Fatalf("want notices %v; saw %v", want, byKind)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func refsOf(event map[string]any) map[string]any { return event["monitoring"].(map[string]any) }

// deliveredAsPolled waits until a notice's Delivery is delivered and checks
// that the authentic webhook bytes are the stored notice with the feed's
// identity, type and references.
func deliveredAsPolled(t *testing.T, receiver *captureReceiver, event map[string]any) map[string]any {
	t.Helper()
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	delivered := awaitDelivery(t, admin, refsOf(event)["delivery_id"].(string), func(d map[string]any) bool { return d["state"] == "delivered" })
	captures := receiver.capturesOf(event["event_id"].(string))
	if len(captures) == 0 {
		t.Fatal("no webhook for", event)
	}
	sameNotice(t, receiver, captures, delivered)
	var body map[string]any
	if err := json.Unmarshal(captures[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["type"] != event["type"] || body["event_id"] != event["event_id"] || body["occurred_at"] != event["occurred_at"] || !reflect.DeepEqual(body["references"], event["monitoring"]) {
		t.Fatal("webhook and feed disagree", body, event)
	}
	return body
}

// TestMonitoringWithdrawalNotices withdraws a Record alerted to two
// Subscriptions. Suppression is immediate. The enabled Subscription receives a
// linked match.withdrawn notice after one retried failure, with identical
// bytes. The one disabled before the withdrawal still gets its notice and
// Delivery (THE-696): no attempt while it is disabled, then the re-enable (on
// the change feed) delivers it once with the polled bytes. Both Matches stay
// inspectable and the Match history is unchanged. That the re-enable opens the
// notice's delivery window, even after a pause longer than the window, is
// owned by the PostgreSQL adapter test
// TestWithdrawalNoticeSurvivesDisableAndReenable.
func TestMonitoringWithdrawalNotices(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	receiver := startReceiver(t)
	s := newNoticeScenario(t, "withdrawal", "Dépêche retirée "+markerCorrectionMatch, map[string]any{"default": "match"}, map[string]any{"default": "match"})
	alerted, paused := s.subscriptions[0], s.subscriptions[1]
	receiver.scriptType(alerted, "match.withdrawn", reply{status: 503}, reply{status: 204})
	for _, sub := range s.subscriptions {
		deliveredAsPolled(t, receiver, s.created[sub])
	}
	if disabled := request(t, "POST", "/v0/subscriptions/"+paused+"/disable", admin, map[string]any{"idempotency_key": "withdrawal-disable-" + paused}, 200); disabled["enabled"] != false {
		t.Fatal("disable", disabled)
	}

	// Search after enrichment so the Version is observed in its final projected
	// shape (lexical anchor plus enriched object, THE-690).
	awaitEnriched(t, admin, s.corpus, s.cursor, s.record)
	query := map[string]any{"query": "retirée", "corpus_ids": []string{s.corpus}, "mode": "lexical"}
	if len(request(t, "POST", "/v0/search", admin, query, 200)["items"].([]any)) != 1 {
		t.Fatal("alerted Record not searchable")
	}
	accepted := request(t, "POST", "/v0/records/withdrawals", admin, withdrawalCommand(s.corpus, s.recordKey+"-withdraw", "example-feed", s.recordKey, "source retraction"), 202)
	if resolved := awaitReceipt(t, accepted["receipt_id"].(string)); resolved["outcome"] != "withdrawal_applied" {
		t.Fatal(resolved)
	}
	// Suppression never waits for the notification worker.
	if len(request(t, "POST", "/v0/search", admin, query, 200)["items"].([]any)) != 0 {
		t.Fatal("withdrawn Record still searchable")
	}

	// Both notices are committed and polled, the disabled Subscription's too.
	notices := map[string]map[string]any{}
	for _, notice := range s.awaitNotices(t, map[string]int{"match.withdrawn": 2})["match.withdrawn"] {
		notices[refsOf(notice)["subscription_id"].(string)] = notice
	}
	for _, sub := range s.subscriptions {
		if notices[sub] == nil {
			t.Fatal("no match.withdrawn for", sub, notices)
		}
		refs, match := refsOf(notices[sub]), refsOf(s.created[sub])
		if refs["match_id"] != match["match_id"] || refs["record_version_id"] != match["record_version_id"] || refs["record_id"] != s.record {
			t.Fatal("match.withdrawn references", sub, notices[sub])
		}
	}

	// The withdrawn Record does not block its own notice: retried, then delivered.
	delivery := refsOf(notices[alerted])["delivery_id"].(string)
	deliveredAsPolled(t, receiver, notices[alerted])
	if got := attemptOutcomes(t, delivery); !reflect.DeepEqual(got, []string{"retryable_error", "acknowledged"}) {
		t.Fatal("withdrawal notice attempts", got)
	}
	match := refsOf(s.created[alerted])
	if m := request(t, "GET", "/v0/matches/"+match["match_id"].(string), admin, nil, 200); m["record_version_id"] != match["record_version_id"] {
		t.Fatal("withdrawn Record's Match must stay inspectable", m)
	}

	// Meanwhile the disabled Subscription's notice was never attempted.
	delivery = refsOf(notices[paused])["delivery_id"].(string)
	d := request(t, "GET", "/v0/deliveries/"+delivery, admin, nil, 200)
	if admission := d["admission"].(map[string]any); d["state"] != "pending" || d["attempt_count"] != float64(0) || admission["allowed"] != false || admission["reason"] != "subscription_disabled" {
		t.Fatal("withdrawal notice while disabled", d)
	}
	if n := len(receiver.capturesOf(notices[paused]["event_id"].(string))); n != 0 || len(attemptOutcomes(t, delivery)) != 0 {
		t.Fatal("withdrawal notice attempted while disabled", n)
	}

	// Re-enable: announced on the feed, then the notice is delivered once.
	if enabled := request(t, "POST", "/v0/subscriptions/"+paused+"/enable", admin, map[string]any{"idempotency_key": "withdrawal-enable-" + paused}, 200); enabled["enabled"] != true {
		t.Fatal("enable", enabled)
	}
	match = refsOf(s.created[paused])
	if feed, _ := drain(t, admin, s.corpus, s.cursor, 0); eventPosition(feed, "subscription.enabled", paused) < eventPosition(feed, "match.withdrawn", match["match_id"].(string)) {
		t.Fatal("subscription.enabled must follow the committed notice on the feed", feed)
	}
	deliveredAsPolled(t, receiver, notices[paused])
	if got := attemptOutcomes(t, delivery); !reflect.DeepEqual(got, []string{"acknowledged"}) {
		t.Fatal("withdrawal notice attempts after re-enable", got)
	}
	if n := len(receiver.capturesOf(notices[paused]["event_id"].(string))); n != 1 {
		t.Fatal("withdrawal notice captures", n)
	}
	// The Match history is unchanged: the notice references the only Match.
	history := request(t, "GET", matchesPath(paused, "", 0), admin, nil, 200)["items"].([]any)
	if len(history) != 1 || history[0].(map[string]any)["match_id"] != match["match_id"] || history[0].(map[string]any)["record_version_id"] != match["record_version_id"] {
		t.Fatal("Match history after re-enable", history)
	}
}

// TestMonitoringSupersededNotice proves the superseded rule: a match.created
// notice still failing when a matching correction commits is never delivered
// afterwards (pending, admission superseded, no further attempt), while the
// self-sufficient match.corrected is delivered.
func TestMonitoringSupersededNotice(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	receiver := startReceiver(t)
	run := monitoringRun()
	c := changeCorpus(t, "superseded-"+run)
	start := request(t, "GET", changesPath(c, "", 0), admin, nil, 200)["next_cursor"].(string)
	query := request(t, "POST", "/v0/saved-queries", admin, savedQueryCommand("superseded-"+run, c), 201)
	sub := request(t, "POST", "/v0/subscriptions", admin, subscriptionCommand("superseded-"+run, query, destinationCapture), 201)["subscription_id"].(string)
	receiver.scriptType(sub, "match.created", reply{status: 503})
	receiver.scriptType(sub, "match.corrected", reply{status: 204})
	s := &noticeScenario{corpus: c, cursor: start, recordKey: "superseded-" + run}

	awaitReady(t, ingestCorrection(t, c, s.recordKey+"-1", s.recordKey, "Dépêche initiale"))
	created := s.awaitNotices(t, map[string]int{"match.created": 1})["match.created"][0]
	stale := refsOf(created)["delivery_id"].(string)
	awaitDelivery(t, admin, stale, func(d map[string]any) bool { return d["last_error"] != nil })

	awaitReady(t, ingestCorrection(t, c, s.recordKey+"-2", s.recordKey, "Dépêche corrigée"))
	corrected := s.awaitNotices(t, map[string]int{"match.corrected": 1})["match.corrected"][0]
	if refsOf(corrected)["previous_match_id"] != refsOf(created)["match_id"] {
		t.Fatal("match.corrected must link the undelivered Match", corrected)
	}
	deliveredAsPolled(t, receiver, corrected)
	superseded := awaitDelivery(t, admin, stale, func(d map[string]any) bool {
		return d["state"] == "pending" && d["next_attempt_at"] == nil && reflect.DeepEqual(d["admission"], map[string]any{"allowed": false, "reason": "superseded"})
	})
	if superseded["state"] != "pending" || superseded["next_attempt_at"] != nil {
		t.Fatal("superseded Delivery", superseded)
	}
	// The adapter owner exercises refusal and proves no attempt is created.
	// Nothing is lost: the undelivered Match stays readable by id.
	request(t, "GET", "/v0/matches/"+refsOf(created)["match_id"].(string), admin, nil, 200)
}

// TestMonitoringRevertNotices corrects an alerted Record back to its first
// Version's exact bytes (THE-712). The revert is a new Version that becomes
// current and searchable with the reverted text, and the Subscription that
// stopped matching on the intermediate Version hears match.corrected linking
// its invalidated Match. Replaying the revert request returns the same
// Receipt, and resubmitting the current content under a new key is a
// duplicate of the revert.
func TestMonitoringRevertNotices(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	receiver := startReceiver(t)
	original := "Dépêche initiale " + markerCorrectionMatch
	s := newNoticeScenario(t, "revert", original, map[string]any{markerCorrectionCalm: "no_match", "default": "match"})
	sub := s.subscriptions[0]
	deliveredAsPolled(t, receiver, s.created[sub])
	first := refsOf(s.created[sub])

	// v2 no longer matches.
	awaitReady(t, ingestCorrection(t, s.corpus, s.recordKey+"-2", s.recordKey, "Dépêche corrigée "+markerCorrectionCalm))
	invalidation := s.awaitNotices(t, map[string]int{"match.no_longer_matches": 1})["match.no_longer_matches"][0]
	if refsOf(invalidation)["match_id"] != first["match_id"] {
		t.Fatal("match.no_longer_matches must reference the first Match", invalidation)
	}
	deliveredAsPolled(t, receiver, invalidation)
	if hits := searchHits(t, s.corpus, "initiale", "lexical"); len(hits) != 0 {
		t.Fatal("superseded text still searchable", hits)
	}

	// v3 restores v1's exact bytes: a new current Version, created, not a duplicate.
	revert := inlineCommand(s.corpus, s.recordKey+"-3", s.recordKey, original)
	reverted := awaitReady(t, request(t, "POST", "/v0/records", admin, revert, 202)["receipt_id"].(string))
	v3 := reverted["version_id"]
	if reverted["outcome"] != "created" || v3 == first["record_version_id"] || reverted["record_id"] != s.record {
		t.Fatal("revert is not a new Version of the Record", reverted)
	}
	if record := request(t, "GET", "/v0/records/"+s.record, admin, nil, 200); record["current_version_id"] != v3 {
		t.Fatal("revert is not the current Version", record)
	}
	if hits := searchHits(t, s.corpus, "initiale", "lexical"); len(hits) != 1 || hits[0]["record_id"] != s.record || hits[0]["version_id"] != v3 {
		t.Fatal("reverted text not served by the revert Version", hits)
	}
	corrected := s.awaitNotices(t, map[string]int{"match.corrected": 1})["match.corrected"][0]
	refs := refsOf(corrected)
	if refs["previous_match_id"] != first["match_id"] || refs["record_version_id"] != v3 || refs["match_id"] == first["match_id"] {
		t.Fatal("revert match.corrected references", corrected)
	}
	deliveredAsPolled(t, receiver, corrected)
	// The successor Match links the invalidated one, which stays inspectable.
	if successor := request(t, "GET", "/v0/matches/"+refs["match_id"].(string), admin, nil, 200); successor["previous_match_id"] != first["match_id"] || successor["record_version_id"] != v3 {
		t.Fatal("successor Match", successor)
	}
	if prior := request(t, "GET", "/v0/matches/"+first["match_id"].(string), admin, nil, 200); prior["record_version_id"] != first["record_version_id"] {
		t.Fatal("prior Match", prior)
	}

	// The same request is the same Receipt; the same content under a new key converges on v3.
	if replay := request(t, "POST", "/v0/records", admin, revert, 202); replay["receipt_id"] != reverted["receipt_id"] {
		t.Fatal("replayed revert is a new Receipt", replay)
	}
	again := awaitReceipt(t, ingestCorrection(t, s.corpus, s.recordKey+"-4", s.recordKey, original))
	if again["outcome"] != "duplicate" || again["version_id"] != v3 {
		t.Fatal("repeat of the current content", again)
	}
	// The negative correction fabricated no Match: the prior one, then its successor.
	if history := request(t, "GET", matchesPath(sub, "", 0), admin, nil, 200)["items"].([]any); len(history) != 2 ||
		history[0].(map[string]any)["match_id"] != first["match_id"] || history[1].(map[string]any)["match_id"] != refs["match_id"] {
		t.Fatal("revert Match history", history)
	}
}
