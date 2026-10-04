package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testConsumerSecret = "x-test-consumer-secret"
	webhookConfigJSON  = `{"list_id":"77","webhook":{"enabled":true}}`
)

// receive relays one request to the plugin's receive route like the core,
// with the instance's credential (bearer token and consumer secret).
func (in *instance) receive(method, query string, headers map[string]string, body []byte) (int, map[string]any) {
	in.t.Helper()
	relayed := map[string][]string{}
	for k, v := range headers {
		relayed[strings.ToLower(k)] = []string{v}
	}
	checkpoint := in.checkpoint
	if checkpoint == nil {
		checkpoint = json.RawMessage("null")
	}
	connector := in.connector()
	delete(connector, "webhook_url") // Fetch-only metadata is outside the receive contract.
	raw, _ := json.Marshal(map[string]any{
		"invocation_id": "delivery", "contribution": "connector", "organization_id": "org_a",
		"configuration": map[string]any{"api_endpoint": in.api}, "connector": connector,
		"credential": map[string]any{"bearer_token": in.token, "consumer_secret": testConsumerSecret}, "checkpoint": checkpoint,
		"now": in.now.Format(time.RFC3339), "reads_today": in.reads,
		"request": map[string]any{"method": method, "query": query, "headers": relayed, "body_base64": base64.StdEncoding.EncodeToString(body)},
	})
	rec := httptest.NewRecorder()
	in.h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v0/contributions/connector/receive", bytes.NewReader(raw)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestTheCRCChallengeIsAnsweredWithTheConsumerSecret(t *testing.T) {
	_, srv := newFakeX(t)
	in := newInstance(t, srv, webhookConfigJSON)
	status, out := in.receive("GET", "crc_token=crc-challenge-42&nonce=1", nil, nil)
	response := out["response"].(map[string]any)
	// HMAC-SHA256("x-test-consumer-secret", "crc-challenge-42"), computed outside Go.
	if status != 200 || out["verdict"] != "accepted" || response["body"] != `{"response_token":"sha256=0AwmvShIN5NfmuVqq8lhYu5fP09cpCa2OJgwNOMUNww="}` || response["content_type"] != "application/json" {
		t.Fatalf("status %d answer %v", status, out)
	}
	if status, out := in.receive("GET", "", nil, nil); status != 200 || out["verdict"] != "refused" {
		t.Fatalf("a GET without crc_token: %d %v", status, out)
	}
}

func TestOnlySignedDeliveriesForThisInstanceBecomeItems(t *testing.T) {
	_, srv := newFakeX(t)
	in := newInstance(t, srv, webhookConfigJSON)
	body := []byte(`{"data":{"id":"7300000000000000001","text":"Pushed"}}`)
	// HMAC-SHA256 of body under the consumer secret, computed outside Go.
	signed := map[string]string{"X-Twitter-Webhooks-Signature": "sha256=M0WFpfBjWV1abJTwPa7R2ZG4vERJEKKy6prQC9mgVlA="}
	if status, out := in.receive("POST", "", signed, body); status != 200 || out["verdict"] != "accepted" || len(out["items"].([]any)) != 1 || out["reads"] != 1.0 {
		t.Fatalf("signed delivery: %d %v", status, out)
	}
	for name, headers := range map[string]map[string]string{
		"unsigned":        nil,
		"wrong signature": {"X-Twitter-Webhooks-Signature": "sha256=AAAA"},
		"other body":      signed,
	} {
		t.Run(name, func(t *testing.T) {
			payload := body
			if name == "other body" {
				payload = []byte(`{"data":{"id":"7300000000000000002","text":"Forged"}}`)
			}
			status, out := in.receive("POST", "", headers, payload)
			if status != 200 || out["verdict"] != "refused" || out["response"].(map[string]any)["status"] != 401.0 || out["items"] != nil {
				t.Fatalf("status %d answer %v", status, out)
			}
		})
	}
	// Another instance's rule matched: nothing for this one.
	other := []byte(`{"data":{"id":"7300000000000000003","text":"Elsewhere"},"matching_rules":[{"id":"1","tag":"quivr:connector_other"}]}`)
	if _, out := in.receive("POST", "", map[string]string{signatureHeader: sign(testConsumerSecret, other)}, other); out["verdict"] != "accepted" || out["items"] != nil {
		t.Fatalf("another instance's delivery: %v", out)
	}
	// Webhook mode off, or no consumer secret: nothing can be verified.
	off := newInstance(t, srv, `{"list_id":"77"}`)
	if _, out := off.receive("POST", "", signed, body); out["verdict"] != "refused" || out["response"].(map[string]any)["status"] != 404.0 {
		t.Fatalf("webhook off: %v", out)
	}
}

// A post delivered by the webhook and later read by a pull sweep is the same
// item: same Record Key, revision, content and extensions, so the core's
// idempotency key and Receipt are the same.
func TestADeliveredPostIsTheItemPullReturns(t *testing.T) {
	f, srv := newFakeX(t)
	in := newInstance(t, srv, `{"list_id":"77","backfill_since":"`+base.Add(-time.Hour).Format(time.RFC3339)+`","webhook":{"enabled":true}}`)
	f.post("7300000000000000010", "Harbour closed", 0, []string{"7300000000000000009", "7300000000000000010"}, map[string]any{"referenced_tweets": []any{map[string]any{"type": "quoted", "id": "7300000000000000001"}}})
	delivered := map[string]any{"data": f.state().Posts["7300000000000000010"], "includes": map[string]any{"users": []any{map[string]any{"id": "42", "username": "newsdesk", "name": "News Desk"}}},
		"matching_rules": []any{map[string]any{"id": "1", "tag": "quivr:connector_x"}}}
	body, _ := json.Marshal(delivered)
	_, out := in.receive("POST", "", map[string]string{signatureHeader: sign(testConsumerSecret, body)}, body)
	pushed := out["items"].([]any)[0]
	in.now = base.Add(10 * time.Minute)
	var pulled any
	for _, p := range in.run() {
		for _, item := range p.Items {
			pulled = item
		}
	}
	a, _ := json.Marshal(pushed)
	b, _ := json.Marshal(pulled)
	if string(a) != string(b) {
		t.Fatalf("pushed %s\npulled %s", a, b)
	}
}

func TestRulesStayWithinTheLimitsAndCoverEveryMember(t *testing.T) {
	var members []string
	for i := 0; i < 30; i++ {
		members = append(members, fmt.Sprintf("%d", 1000000+i*37))
	}
	rules := packRules(append(members, members[3]), 80)
	covered := map[string]bool{}
	for _, r := range rules {
		if len(r) > 80 {
			t.Fatalf("rule of %d characters: %s", len(r), r)
		}
		for _, term := range strings.Split(r, " OR ") {
			covered[strings.TrimPrefix(term, "from:")] = true
		}
	}
	if len(covered) != len(members) || len(rules) < 2 {
		t.Fatalf("%d rules cover %d of %d members", len(rules), len(covered), len(members))
	}
}

// The pull run sets push up at X page by page, and a second resync with the
// same members changes nothing at X.
func TestPullSetsPushUpAndResyncsIdempotently(t *testing.T) {
	f, srv := newFakeX(t)
	f.list(map[string]any{"members": []string{"11", "22", "33"}})
	in := newInstance(t, srv, `{"list_id":"77","webhook":{"enabled":true,"max_rule_length":64,"resync_interval_seconds":60}}`)
	in.webhookURL = in.callbackURL()
	pages := in.run()
	last := pages[len(pages)-1]
	state := f.state()
	if last.Push["state"] != "active" || last.Push["poll_interval_seconds"] != 900.0 || len(state.Rules) != 1 || state.Rules[0].Tag != "quivr:connector_x" ||
		state.Rules[0].Value != "from:11 OR from:22 OR from:33" || len(state.Webhooks) != 1 || !state.Linked[state.Webhooks[0].ID] || state.Webhooks[0].URL != in.webhookURL {
		t.Fatalf("push %v rules %+v webhooks %+v linked %v", last.Push, state.Rules, state.Webhooks, state.Linked)
	}
	f.take()
	in.now = in.now.Add(time.Minute)
	in.run()
	if writes := countWrites(f.take()); writes != 0 {
		t.Fatalf("a resync with the same members wrote %d times to X", writes)
	}
	// A member leaves and one joins: one rule replaced, nothing else.
	f.list(map[string]any{"members": []string{"11", "22", "44"}})
	in.now = in.now.Add(time.Minute)
	in.run()
	state = f.state()
	if writes := countWrites(f.take()); writes != 2 || len(state.Rules) != 1 || state.Rules[0].Value != "from:11 OR from:22 OR from:44" {
		t.Fatalf("writes %d rules %+v", writes, state.Rules)
	}
}

func countWrites(requests []string) int {
	n := 0
	for _, r := range requests {
		if strings.HasPrefix(r, "POST ") || strings.HasPrefix(r, "PUT ") || strings.HasPrefix(r, "DELETE ") {
			n++
		}
	}
	return n
}

func TestPushFailuresAreReportedAndPullCarriesOn(t *testing.T) {
	t.Run("invalidated webhook, then recovery", func(t *testing.T) {
		f, srv := newFakeX(t)
		f.list(map[string]any{"members": []string{"11"}})
		in := newInstance(t, srv, `{"list_id":"77","webhook":{"enabled":true,"resync_interval_seconds":60}}`)
		in.webhookURL = in.callbackURL()
		in.run()
		f.control("/_control/webhooks", map[string]any{"invalidate": true})
		f.control("/_control/app", map[string]any{"crc_fails": true})
		in.now = in.now.Add(time.Minute)
		pages := in.run()
		if p := pages[len(pages)-1]; p.Error != nil || p.Push["state"] != "failed" || p.Push["error_class"] != "access" || p.Push["code"] != "webhook_invalid" {
			t.Fatalf("pages %+v", pages)
		}
		f.control("/_control/app", map[string]any{"crc_fails": false})
		in.now = in.now.Add(time.Minute)
		if pages := in.run(); pages[len(pages)-1].Push["state"] != "active" || !f.state().Webhooks[0].Valid {
			t.Fatalf("recovery %+v", pages[len(pages)-1].Push)
		}
	})
	t.Run("too many members for the rule limit", func(t *testing.T) {
		f, srv := newFakeX(t)
		members := []string{"11", "22", "33", "44"}
		in := newInstance(t, srv, `{"list_id":"77","webhook":{"enabled":true,"max_rules":1,"max_rule_length":64}}`)
		in.webhookURL = in.callbackURL()
		members = append(members, strings.Repeat("9", 19), strings.Repeat("8", 19), strings.Repeat("7", 19))
		f.list(map[string]any{"members": members})
		pages := in.run()
		if p := pages[len(pages)-1]; p.Push["state"] != "failed" || p.Push["code"] != "rule_limit_exceeded" || len(f.state().Rules) != 0 {
			t.Fatalf("push %v rules %+v", p.Push, f.state().Rules)
		}
	})
	t.Run("no public URL", func(t *testing.T) {
		_, srv := newFakeX(t)
		in := newInstance(t, srv, webhookConfigJSON)
		pages := in.run()
		if p := pages[len(pages)-1]; p.Push["state"] != "pending" || p.Push["code"] != "webhook_url_missing" {
			t.Fatalf("push %v", p.Push)
		}
	})
}

// While push is active, a sweep holds back posts younger than the grace
// period that were published after push became active: the webhook may
// still bring them, and a later sweep reads them if it did not.
func TestPullHoldsBackPostsTheWebhookMayStillDeliver(t *testing.T) {
	f, srv := newFakeX(t)
	f.list(map[string]any{"members": []string{"42"}})
	in := newInstance(t, srv, `{"list_id":"77","webhook":{"enabled":true}}`)
	in.webhookURL = in.callbackURL()
	in.run()
	f.post("7400000000000000001", "Older than the grace", 0, nil, nil)
	f.post("7400000000000000002", "Just published", 50, nil, nil)
	in.now = base.Add(70 * time.Second)
	if got := keys(in.run()); fmt.Sprint(got) != "[7400000000000000001]" {
		t.Fatalf("first sweep %v", got)
	}
	in.now = base.Add(3 * time.Minute)
	if got := keys(in.run()); fmt.Sprint(got) != "[7400000000000000002]" {
		t.Fatalf("second sweep %v", got)
	}
}

// callbackURL relays actual CRC requests to the plugin's receive boundary.
func (in *instance) callbackURL() string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, out := in.receive(r.Method, r.URL.RawQuery, nil, nil)
		response, ok := out["response"].(map[string]any)
		if !ok {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", response["content_type"].(string))
		w.WriteHeader(int(response["status"].(float64)))
		_, _ = w.Write([]byte(response["body"].(string)))
	}))
	in.t.Cleanup(srv.Close)
	return srv.URL + "/v0/connector-webhooks/connector_x"
}
