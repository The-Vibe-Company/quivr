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
	markerCorrectionFault = "PANNE"
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

// TestMonitoringCorrectionNotices drives ordinary corrections of an alerted
// Record through three Subscriptions: one still matches (linked successor
// Match and a self-sufficient match.corrected), one no longer matches (one
// match.no_longer_matches for the prior Match, no Match) and one whose
// evaluator fails on the correction (no notice: a failure is never negative).
func TestMonitoringCorrectionNotices(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	receiver := startReceiver(t)
	s := newNoticeScenario(t, "correction", "Dépêche "+markerCorrectionMatch,
		map[string]any{markerCorrectionMatch: "match"},
		map[string]any{markerCorrectionCalm: "no_match", "default": "match"},
		map[string]any{markerCorrectionFault: "error", "default": "match"})
	positive, negative, failing := s.subscriptions[0], s.subscriptions[1], s.subscriptions[2]
	for _, sub := range s.subscriptions {
		deliveredAsPolled(t, receiver, s.created[sub])
	}

	correction := awaitReady(t, ingestCorrection(t, s.corpus, s.recordKey+"-2", s.recordKey, "Dépêche corrigée "+markerCorrectionMatch+" "+markerCorrectionCalm+" "+markerCorrectionFault))
	v1 := refsOf(s.created[positive])["record_version_id"]
	v2 := correction["version_id"]
	if correction["record_id"] != s.record || v1 == v2 {
		t.Fatal("correction is not a new Version of the alerted Record", correction)
	}
	notices := s.awaitNotices(t, map[string]int{"match.corrected": 1, "match.no_longer_matches": 1})

	// Positive: a linked successor Match; its notice carries every
	// match.created reference plus previous_match_id, so it stands alone.
	corrected := notices["match.corrected"][0]
	refs := refsOf(corrected)
	previous := refsOf(s.created[positive])["match_id"]
	if len(notices["match.corrected"]) != 1 || refs["subscription_id"] != positive || refs["record_id"] != s.record || refs["record_version_id"] != v2 ||
		refs["previous_match_id"] != previous || refs["match_id"] == previous || refs["subscription_version_id"] != refsOf(s.created[positive])["subscription_version_id"] {
		t.Fatal("match.corrected references", notices["match.corrected"])
	}
	body := deliveredAsPolled(t, receiver, corrected)
	for _, key := range []string{"match_id", "record_id", "record_version_id", "subscription_id", "subscription_version_id", "delivery_id", "previous_match_id"} {
		if body["references"].(map[string]any)[key] == nil {
			t.Fatal("match.corrected is not self-sufficient", body)
		}
	}
	successor := request(t, "GET", "/v0/matches/"+refs["match_id"].(string), admin, nil, 200)
	if successor["previous_match_id"] != previous || successor["record_version_id"] != v2 {
		t.Fatal("successor Match", successor)
	}
	history := request(t, "GET", matchesPath(positive, "", 0), admin, nil, 200)["items"].([]any)
	if len(history) != 2 || history[0].(map[string]any)["match_id"] != previous || history[1].(map[string]any)["match_id"] != refs["match_id"] {
		t.Fatal("positive Match history", history)
	}

	// Negative: one notice for the prior positive Match and the correction; no Match.
	invalidated := notices["match.no_longer_matches"][0]
	refs = refsOf(invalidated)
	prior := refsOf(s.created[negative])["match_id"]
	if len(notices["match.no_longer_matches"]) != 1 || refs["subscription_id"] != negative || refs["match_id"] != prior || refs["record_version_id"] != v2 || refs["previous_match_id"] != nil {
		t.Fatal("match.no_longer_matches references", notices["match.no_longer_matches"])
	}
	deliveredAsPolled(t, receiver, invalidated)
	if history := request(t, "GET", matchesPath(negative, "", 0), admin, nil, 200)["items"].([]any); len(history) != 1 || history[0].(map[string]any)["match_id"] != prior {
		t.Fatal("negative correction fabricated a Match", history)
	}
	// The earlier immutable Match stays inspectable.
	if m := request(t, "GET", "/v0/matches/"+prior.(string), admin, nil, 200); m["record_version_id"] != v1 {
		t.Fatal("prior Match", m)
	}
	if corrected["event_id"] == invalidated["event_id"] || corrected["event_id"] == s.created[positive]["event_id"] {
		t.Fatal("notice identities must be distinct")
	}

	// An evaluator failure is not a negative decision: no notice, no Match.
	for kind, events := range s.awaitNotices(t, nil) {
		for _, e := range events {
			if refsOf(e)["subscription_id"] == failing && kind != "match.created" {
				t.Fatal("failed evaluation produced a notice", e)
			}
		}
	}
	if history := request(t, "GET", matchesPath(failing, "", 0), admin, nil, 200)["items"].([]any); len(history) != 1 {
		t.Fatal("failed evaluation changed Match history", history)
	}
}

// TestMonitoringWithdrawalNotices withdraws an alerted Record: suppression is
// immediate; the enabled Subscription receives a linked match.withdrawn notice
// after one retried failure, with identical bytes; a Subscription disabled
// before the withdrawal receives none; the Match stays inspectable.
func TestMonitoringWithdrawalNotices(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	receiver := startReceiver(t)
	s := newNoticeScenario(t, "withdrawal", "Dépêche retirée "+markerCorrectionMatch,
		map[string]any{"default": "match"}, map[string]any{"default": "match"})
	alerted, disabled := s.subscriptions[0], s.subscriptions[1]
	receiver.scriptType(alerted, "match.withdrawn", reply{status: 503}, reply{status: 204})
	for _, sub := range s.subscriptions {
		deliveredAsPolled(t, receiver, s.created[sub])
	}
	request(t, "POST", "/v0/subscriptions/"+disabled+"/disable", admin, map[string]any{"idempotency_key": "withdrawal-disable-" + disabled}, 200)

	// Search only after enrichment: attaching an embedding briefly hides an
	// object from lexical search (THE-690).
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

	notice := s.awaitNotices(t, map[string]int{"match.withdrawn": 1})["match.withdrawn"][0]
	refs := refsOf(notice)
	match := refsOf(s.created[alerted])
	if refs["subscription_id"] != alerted || refs["match_id"] != match["match_id"] || refs["record_version_id"] != match["record_version_id"] || refs["record_id"] != s.record {
		t.Fatal("match.withdrawn references", notice)
	}
	// The withdrawn Record does not block its own notice: retried, then delivered.
	deliveredAsPolled(t, receiver, notice)
	if got := attemptOutcomes(t, refs["delivery_id"].(string)); !reflect.DeepEqual(got, []string{"retryable_error", "acknowledged"}) {
		t.Fatal("withdrawal notice attempts", got)
	}
	// Both Subscriptions' withdrawal work comes from the same dispatch step:
	// the disabled one received nothing.
	for _, e := range s.awaitNotices(t, nil)["match.withdrawn"] {
		if refsOf(e)["subscription_id"] == disabled {
			t.Fatal("disabled Subscription notified", e)
		}
	}
	if m := request(t, "GET", "/v0/matches/"+match["match_id"].(string), admin, nil, 200); m["record_version_id"] != match["record_version_id"] {
		t.Fatal("withdrawn Record's Match must stay inspectable", m)
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
		return d["state"] == "pending" && reflect.DeepEqual(d["admission"], map[string]any{"allowed": false, "reason": "superseded"})
	})
	if superseded["state"] != "pending" || superseded["next_attempt_at"] != nil {
		t.Fatal("superseded Delivery", superseded)
	}
	// No attempt after it is superseded, beyond the harness's longest retry wait.
	attempts := len(attemptOutcomes(t, stale))
	time.Sleep(7 * time.Second)
	if now := request(t, "GET", "/v0/deliveries/"+stale, admin, nil, 200); now["attempt_count"] != float64(attempts) || len(receiver.capturesOf(created["event_id"].(string))) != attempts {
		t.Fatal("superseded notice was attempted again", now)
	}
	// Nothing is lost: the undelivered Match stays readable by id.
	request(t, "GET", "/v0/matches/"+refsOf(created)["match_id"].(string), admin, nil, 200)
}

// TestMonitoringSupersededNoLongerMatches proves THE-694: a
// match.no_longer_matches still waiting for its retry when a later correction
// matches again is never delivered. The receiver asks for a 40 s Retry-After
// on it, so its first attempt has finished and its retry is not yet eligible
// when the match.corrected commits; the setup fails otherwise. Its only
// attempt therefore precedes the correction, and none is admitted after it.
func TestMonitoringSupersededNoLongerMatches(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	receiver := startReceiver(t)
	s := newNoticeScenario(t, "rematch", "Dépêche "+markerCorrectionMatch, map[string]any{markerCorrectionCalm: "no_match", "default": "match"})
	sub := s.subscriptions[0]
	receiver.scriptType(sub, "match.no_longer_matches", reply{status: 503, retryAfter: "40"})
	deliveredAsPolled(t, receiver, s.created[sub])

	// v2 no longer matches; its notice fails once and waits for its retry.
	awaitReady(t, ingestCorrection(t, s.corpus, s.recordKey+"-2", s.recordKey, "Dépêche "+markerCorrectionCalm))
	invalidation := s.awaitNotices(t, map[string]int{"match.no_longer_matches": 1})["match.no_longer_matches"][0]
	stale := refsOf(invalidation)["delivery_id"].(string)
	failed := awaitDelivery(t, admin, stale, func(d map[string]any) bool {
		return d["state"] == "pending" && d["attempt_count"] == float64(1) && d["next_attempt_at"] != nil
	})
	next, err := time.Parse(time.RFC3339Nano, failed["next_attempt_at"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(next) < 15*time.Second {
		t.Fatal("invalid setup: retry eligibility too close to commit the correction before it", failed)
	}

	// v3 matches again: a match.corrected linked to the invalidated Match.
	// Its text differs from v1, which would otherwise dedupe to that Version.
	awaitReady(t, ingestCorrection(t, s.corpus, s.recordKey+"-3", s.recordKey, "Dépêche corrigée "+markerCorrectionMatch))
	corrected := s.awaitNotices(t, map[string]int{"match.corrected": 1})["match.corrected"][0]
	if refsOf(corrected)["previous_match_id"] != refsOf(invalidation)["match_id"] {
		t.Fatal("match.corrected must link the invalidated Match", corrected)
	}
	committed, err := time.Parse(time.RFC3339Nano, corrected["occurred_at"].(string))
	if err != nil {
		t.Fatal(err)
	}
	// occurred_at is the transaction's start; a second covers its commit.
	if !committed.Add(time.Second).Before(next) {
		t.Fatal("invalid setup: the correction committed after the retry became eligible; suppression is not proven", committed, next)
	}
	deliveredAsPolled(t, receiver, corrected)
	superseded := request(t, "GET", "/v0/deliveries/"+stale, admin, nil, 200)
	if superseded["state"] != "pending" || superseded["next_attempt_at"] != nil ||
		!reflect.DeepEqual(superseded["admission"], map[string]any{"allowed": false, "reason": "superseded"}) {
		t.Fatal("superseded match.no_longer_matches", superseded)
	}

	// Observe beyond its former eligibility plus the worker's longest idle
	// backoff and retry wait: no attempt was admitted after the correction.
	time.Sleep(time.Until(next) + 6*time.Second)
	after := request(t, "GET", "/v0/deliveries/"+stale, admin, nil, 200)
	if after["state"] != "pending" || after["attempt_count"] != float64(1) || len(receiver.capturesOf(invalidation["event_id"].(string))) != 1 {
		t.Fatal("stale match.no_longer_matches attempted after the correction", after)
	}
	if got := attemptOutcomes(t, stale); !reflect.DeepEqual(got, []string{"retryable_error"}) {
		t.Fatal("attempt history of the superseded notice", got)
	}
	// Nothing is lost: the invalidated Match stays readable.
	request(t, "GET", "/v0/matches/"+refsOf(invalidation)["match_id"].(string), admin, nil, 200)
}
