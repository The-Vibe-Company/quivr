package acceptance

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// Retry acceptance runs against the shortened, reported harness policy in
// scripts/local.py (DELIVERY_OVERRIDES): initial 2s, cap 2s, window 20s. A
// retry therefore waits at least 1s, so each attempt carries its own
// whole-second webhook-timestamp and signature (sameNotice).
// retryNotice is one Subscription's committed notice and logical Delivery.
type retryNotice struct {
	subscription, delivery, event, match string
}

// retryScenario creates its own Corpus, Saved Query and one Subscription per
// script on the capture destination, ingests one matching Record and returns
// each Subscription's notice.
func retryScenario(t *testing.T, name string, receiver *captureReceiver, scripts ...[]reply) []retryNotice {
	t.Helper()
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := monitoringRun()
	c := changeCorpus(t, name+"-"+run)
	start := request(t, "GET", changesPath(c, "", 0), admin, nil, 200)["next_cursor"].(string)
	query := request(t, "POST", "/v0/saved-queries", admin, savedQueryCommand(name+"-"+run, c), 201)
	subs := make([]string, len(scripts))
	for i, replies := range scripts {
		sub := request(t, "POST", "/v0/subscriptions", admin, subscriptionCommand(name+"-"+strconv.Itoa(i)+"-"+run, query, destinationCapture), 201)
		subs[i] = sub["subscription_id"].(string)
		if receiver != nil {
			receiver.script(subs[i], replies...)
		}
	}
	ingestSearchable(t, c, name+"-"+run, "Dépêche "+name+" "+run)
	_, created := awaitMatches(t, admin, c, start, len(scripts))
	out := make([]retryNotice, len(subs))
	for i, sub := range subs {
		for _, event := range created {
			refs := event["monitoring"].(map[string]any)
			if refs["subscription_id"] == sub {
				out[i] = retryNotice{subscription: sub, delivery: refs["delivery_id"].(string), event: event["event_id"].(string), match: refs["match_id"].(string)}
			}
		}
		if out[i].delivery == "" {
			t.Fatal("missing notice for Subscription", sub, created)
		}
	}
	return out
}

// attemptOutcomes lists a Delivery's attempt outcomes in number order.
func attemptOutcomes(t *testing.T, deliveryID string) []string {
	t.Helper()
	page := request(t, "GET", "/v0/deliveries/"+deliveryID+"/attempts?limit=100", os.Getenv("QUIVR_TEST_ADMIN"), nil, 200)
	var out []string
	for i, item := range page["items"].([]any) {
		a := item.(map[string]any)
		if a["number"] != float64(i+1) {
			t.Fatal("attempt history must be append-only and dense", page)
		}
		out = append(out, a["outcome"].(string))
	}
	return out
}

// sameNotice checks that every capture is an authentic copy of the same bytes
// with the same webhook-id, each attempt freshly signed.
func sameNotice(t *testing.T, receiver *captureReceiver, captures []webhookCapture, delivery map[string]any) {
	t.Helper()
	stored, _ := json.Marshal(delivery["event"])
	var storedEvent, first map[string]any
	_ = json.Unmarshal(stored, &storedEvent)
	signatures := map[string]bool{}
	for i, capture := range captures {
		if !bytes.Equal(capture.Body, captures[0].Body) || capture.Header.Get("webhook-id") != captures[0].Header.Get("webhook-id") {
			t.Fatal("retries must resend the exact notice bytes and event id", i)
		}
		if err := verifyWebhook(receiver.key, capture.Header.Get("webhook-id"), capture.Header.Get("webhook-timestamp"), capture.Header.Get("webhook-signature"), capture.Body, time.Now()); err != nil {
			t.Fatal("retry is not authentic", err)
		}
		signatures[capture.Header.Get("webhook-signature")+capture.Header.Get("webhook-timestamp")] = true
	}
	if err := json.Unmarshal(captures[0].Body, &first); err != nil || !reflect.DeepEqual(first, storedEvent) {
		t.Fatal("captured notice differs from the stored Delivery event", first, storedEvent)
	}
	if len(captures) > 1 && len(signatures) < 2 {
		t.Fatal("each attempt must carry its own timestamp and signature")
	}
}

// TestMonitoringDeliveryRecoversFromTemporaryFailure proves a receiver that
// answers 503 twice then 204 ends delivered on the same logical Delivery and
// Match, after three append-only attempts resending identical bytes.
func TestMonitoringDeliveryRecoversFromTemporaryFailure(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	receiver := startReceiver(t)
	n := retryScenario(t, "delivery-recovers", receiver, []reply{{status: 503}, {status: 503}, {status: 204}})[0]

	delivered := awaitDelivery(t, admin, n.delivery, func(d map[string]any) bool { return d["state"] == "delivered" })
	if delivered["attempt_count"] != float64(3) || delivered["last_error"] != nil || delivered["next_attempt_at"] != nil || delivered["match_id"] != n.match {
		t.Fatal("recovered Delivery", delivered)
	}
	if got := attemptOutcomes(t, n.delivery); !reflect.DeepEqual(got, []string{"retryable_error", "retryable_error", "acknowledged"}) {
		t.Fatal("attempt history", got)
	}
	captures := receiver.capturesOf(n.event)
	if len(captures) != 3 || captures[2].Status != 204 {
		t.Fatalf("receiver saw %d attempts", len(captures))
	}
	sameNotice(t, receiver, captures, delivered)
	// Recovery creates no new Match or Delivery.
	matches := request(t, "GET", matchesPath(n.subscription, "", 0), admin, nil, 200)["items"].([]any)
	if len(matches) != 1 || matches[0].(map[string]any)["match_id"] != n.match {
		t.Fatal("retries must not create Matches", matches)
	}
}

// restartState carries the pre-restart notice to the post-restart phase.
type restartState struct {
	Subscription, Delivery, Event, Match string
	Body                                 string
}

func restartStatePath() string {
	return filepath.Join(os.Getenv("QUIVR_TEST_CAPTURES"), "delivery-restart.json")
}

// TestDeliveryRestartBefore records one failed attempt, then scripts/local.py
// kills and restarts the worker before TestDeliveryRestartAfter runs. The
// receiver asks for an 8 s Retry-After so the retry falls after the restart,
// inside the delivery window.
func TestDeliveryRestartBefore(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	receiver := startReceiver(t)
	n := retryScenario(t, "delivery-restart", receiver, []reply{{status: 503, retryAfter: "8"}})[0]
	awaitDelivery(t, admin, n.delivery, func(d map[string]any) bool { return d["attempt_count"] == float64(1) && d["last_error"] != nil })
	captures := receiver.capturesOf(n.event)
	if len(captures) != 1 {
		t.Fatal("expected one failed attempt before restart", len(captures))
	}
	b, _ := json.Marshal(restartState{Subscription: n.subscription, Delivery: n.delivery, Event: n.event, Match: n.match, Body: base64.StdEncoding.EncodeToString(captures[0].Body)})
	if err := os.WriteFile(restartStatePath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestDeliveryRestartAfter proves the Delivery converges after a worker kill:
// the same Delivery and Match, the same event id and bytes, dense
// append-only history ending acknowledged, and no duplicate Match.
func TestDeliveryRestartAfter(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	raw, err := os.ReadFile(restartStatePath())
	if err != nil {
		t.Skip("runs after TestDeliveryRestartBefore and a worker restart (scripts/local.py)")
	}
	var s restartState
	if err = json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	receiver := startReceiver(t)
	receiver.script(s.Subscription, reply{status: 204})
	delivered := awaitDelivery(t, admin, s.Delivery, func(d map[string]any) bool { return d["state"] == "delivered" })
	outcomes := attemptOutcomes(t, s.Delivery)
	if outcomes[0] != "retryable_error" || outcomes[len(outcomes)-1] != "acknowledged" || delivered["attempt_count"] != float64(len(outcomes)) || delivered["match_id"] != s.Match {
		t.Fatal("post-restart history", outcomes, delivered)
	}
	captures := receiver.capturesOf(s.Event)
	body, _ := base64.StdEncoding.DecodeString(s.Body)
	if len(captures) == 0 || !bytes.Equal(captures[len(captures)-1].Body, body) || captures[len(captures)-1].Status != 204 {
		t.Fatal("post-restart notice must be the same event bytes", len(captures))
	}
	matches := request(t, "GET", matchesPath(s.Subscription, "", 0), admin, nil, 200)["items"].([]any)
	if len(matches) != 1 || matches[0].(map[string]any)["match_id"] != s.Match {
		t.Fatal("restart must not duplicate the Match or Delivery", matches)
	}
}
