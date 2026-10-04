package acceptance

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	contract "github.com/The-Vibe-Company/quivr/contracts/http/v0"
)

// destinationCapture is the org_a destination whose URL points at the
// receiver this test runs (scripts/local.py). No other test uses it.
const destinationCapture = "local-receiver-capture"

// receiverBody is what the receiver answers; it must never reach the API.
const receiverBody = "receiver-private-acknowledgement"

// verifyWebhook is the receiver's independent Standard Webhooks v1 check: the
// MAC over id.timestamp.raw-body is compared in constant time before any
// parsing, and the timestamp must be within five minutes of now.
func verifyWebhook(key []byte, id, timestamp, signatures string, body []byte, now time.Time) error {
	if id == "" {
		return errors.New("missing webhook-id")
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errors.New("invalid webhook-timestamp")
	}
	if d := now.Sub(time.Unix(ts, 0)); d > 5*time.Minute || d < -5*time.Minute {
		return errors.New("stale webhook-timestamp")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + timestamp + "."))
	mac.Write(body)
	expected := mac.Sum(nil)
	for _, candidate := range strings.Fields(signatures) {
		version, sig, ok := strings.Cut(candidate, ",")
		decoded, err := base64.StdEncoding.DecodeString(sig)
		if ok && version == "v1" && err == nil && hmac.Equal(decoded, expected) {
			return nil
		}
	}
	return errors.New("signature mismatch")
}

type webhookCapture struct {
	Path   string
	Header http.Header
	Body   []byte
	Status int
}

// reply is one scripted receiver answer; retryAfter sets Retry-After when non-empty.
type reply struct {
	status     int
	retryAfter string
}

// captureReceiver verifies every request, records its raw bytes and answers
// according to the Subscription (and optionally notice type) the notice
// references: the n-th request for a script gets its n-th scripted reply, the
// last one repeating.
type captureReceiver struct {
	key      []byte
	mu       sync.Mutex
	captures []webhookCapture
	scripts  map[string][]reply
	served   map[string]int
}

// script sets the replies for one Subscription's notices.
func (c *captureReceiver) script(subscriptionID string, replies ...reply) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scripts[subscriptionID] = replies
}

// scriptType sets the replies for one Subscription's notices of one type; it
// takes precedence over the Subscription's script.
func (c *captureReceiver) scriptType(subscriptionID, eventType string, replies ...reply) {
	c.script(subscriptionID+" "+eventType, replies...)
}

func (c *captureReceiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	answer := reply{status: http.StatusNoContent}
	if verifyWebhook(c.key, r.Header.Get("webhook-id"), r.Header.Get("webhook-timestamp"), r.Header.Get("webhook-signature"), body, time.Now()) != nil {
		answer = reply{status: http.StatusUnauthorized}
	} else {
		// Parse only after authenticity was verified on the raw bytes.
		var event struct {
			Type       string `json:"type"`
			References struct {
				SubscriptionID string `json:"subscription_id"`
			} `json:"references"`
		}
		if json.Unmarshal(body, &event) == nil {
			c.mu.Lock()
			key := event.References.SubscriptionID + " " + event.Type
			if len(c.scripts[key]) == 0 {
				key = event.References.SubscriptionID
			}
			if replies := c.scripts[key]; len(replies) > 0 {
				n := c.served[key]
				answer = replies[min(n, len(replies)-1)]
				c.served[key] = n + 1
			}
			c.mu.Unlock()
		}
	}
	c.mu.Lock()
	c.captures = append(c.captures, webhookCapture{Path: r.URL.Path, Header: r.Header.Clone(), Body: body, Status: answer.status})
	c.mu.Unlock()
	if answer.status == http.StatusTemporaryRedirect {
		w.Header().Set("Location", "/capture/redirected")
	}
	if answer.retryAfter != "" {
		w.Header().Set("Retry-After", answer.retryAfter)
	}
	w.WriteHeader(answer.status)
	_, _ = w.Write([]byte(receiverBody))
}

// capturesOf returns the captured requests carrying one webhook-id.
func (c *captureReceiver) capturesOf(eventID string) []webhookCapture {
	var out []webhookCapture
	for _, capture := range c.snapshot() {
		if capture.Header.Get("webhook-id") == eventID {
			out = append(out, capture)
		}
	}
	return out
}

func (c *captureReceiver) snapshot() []webhookCapture {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]webhookCapture(nil), c.captures...)
}

func startReceiver(t *testing.T) *captureReceiver {
	t.Helper()
	addr, secret := os.Getenv("QUIVR_TEST_RECEIVER_ADDR"), os.Getenv("QUIVR_TEST_RECEIVER_SECRET")
	if addr == "" || secret == "" {
		t.Fatal("QUIVR_TEST_RECEIVER_ADDR and QUIVR_TEST_RECEIVER_SECRET are provided by scripts/local.py")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c := &captureReceiver{key: key, scripts: map[string][]reply{}, served: map[string]int{}}
	srv := &http.Server{Handler: c, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return c
}

// awaitDelivery polls a Delivery until done accepts it.
func awaitDelivery(t *testing.T, token, id string, done func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(monitoringWait)
	for {
		d := request(t, "GET", "/v0/deliveries/"+id, token, nil, 200)
		if done(d) {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("Delivery %s never settled: %v", id, d)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestMonitoringDeliverySignedNotifications drives signed webhook delivery
// through a real receiver: an acknowledged, authentic, reference-only notice
// with the poll/SSE identity; tampering and staleness rejection; an unfollowed
// redirect that ends exhausted after one attempt; bounded attempt history
// without secrets; and public delivery.updated transitions.
func TestMonitoringDeliverySignedNotifications(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	receiver := startReceiver(t)
	run := monitoringRun()
	c := changeCorpus(t, "delivery-"+run)
	start := request(t, "GET", changesPath(c, "", 0), admin, nil, 200)["next_cursor"].(string)
	query := request(t, "POST", "/v0/saved-queries", admin, savedQueryCommand("delivery-"+run, c), 201)
	subscribe := func(name string, status int) string {
		sub := request(t, "POST", "/v0/subscriptions", admin, subscriptionCommand("delivery-"+name+"-"+run, query, destinationCapture), 201)
		id := sub["subscription_id"].(string)
		receiver.script(id, reply{status: status})
		return id
	}
	acked, redirected := subscribe("acked", 204), subscribe("redirected", 307)

	ingestSearchable(t, c, "delivery-"+run, "Dépêche livrée "+run)
	_, created := awaitMatches(t, admin, c, start, 2)
	notices := map[string]map[string]any{}
	for _, event := range created {
		notices[event["monitoring"].(map[string]any)["subscription_id"].(string)] = event
	}
	deliveryOf := func(sub string) string {
		if notices[sub] == nil {
			t.Fatal("missing notice for Subscription", sub, created)
		}
		return notices[sub]["monitoring"].(map[string]any)["delivery_id"].(string)
	}

	// 2xx: delivered once, authentic, and identical to the polled notice.
	ackedID := deliveryOf(acked)
	delivered := awaitDelivery(t, admin, ackedID, func(d map[string]any) bool { return d["state"] == "delivered" })
	if delivered["attempt_count"] != float64(1) || !reflect.DeepEqual(delivered["admission"], map[string]any{"allowed": false, "reason": "terminal"}) || delivered["last_error"] != nil {
		t.Fatal("delivered Delivery", delivered)
	}
	feedEvent := notices[acked]
	var ackedCapture *webhookCapture
	for _, capture := range receiver.snapshot() {
		if capture.Header.Get("webhook-id") == feedEvent["event_id"] {
			if ackedCapture != nil {
				t.Fatal("acknowledged notice delivered twice")
			}
			ackedCapture = &capture
		}
	}
	if ackedCapture == nil || ackedCapture.Status != 204 || ackedCapture.Path != "/capture" {
		t.Fatal("receiver did not acknowledge the notice", ackedCapture)
	}
	key := receiver.key
	h := ackedCapture.Header
	if err := verifyWebhook(key, h.Get("webhook-id"), h.Get("webhook-timestamp"), h.Get("webhook-signature"), ackedCapture.Body, time.Now()); err != nil {
		t.Fatal("captured notice is not authentic", err)
	}
	var body map[string]any
	if err := json.Unmarshal(ackedCapture.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["event_id"] != feedEvent["event_id"] || body["type"] != "match.created" || body["occurred_at"] != feedEvent["occurred_at"] ||
		!reflect.DeepEqual(body["references"], feedEvent["monitoring"]) || !reflect.DeepEqual(body, delivered["event"]) {
		t.Fatal("webhook body must be the stored notice with the feed identity", body, feedEvent, delivered["event"])
	}
	for _, field := range []string{"title", "text", "content", "excerpt", "explanation", "cursor", "secret"} {
		if _, ok := body[field]; ok {
			t.Fatal("webhook is not reference-only", body)
		}
	}
	// Tampered bytes, a changed id and a stale timestamp are rejected; so is a forged request.
	ts := h.Get("webhook-timestamp")
	sig := h.Get("webhook-signature")
	tampered := bytes.Clone(ackedCapture.Body)
	tampered[len(tampered)-2] ^= 1
	if verifyWebhook(key, h.Get("webhook-id"), ts, sig, tampered, time.Now()) == nil ||
		verifyWebhook(key, h.Get("webhook-id")+"x", ts, sig, ackedCapture.Body, time.Now()) == nil ||
		verifyWebhook(key, h.Get("webhook-id"), ts, sig, ackedCapture.Body, time.Now().Add(6*time.Minute)) == nil {
		t.Fatal("receiver verification accepted tampered or stale input")
	}
	forged, _ := http.NewRequest("POST", "http://"+os.Getenv("QUIVR_TEST_RECEIVER_ADDR")+"/capture", bytes.NewReader(ackedCapture.Body))
	forged.Header.Set("webhook-id", h.Get("webhook-id"))
	forged.Header.Set("webhook-timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	forged.Header.Set("webhook-signature", "v1,"+base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if res, err := http.DefaultClient.Do(forged); err != nil || res.StatusCode != 401 {
		t.Fatal("forged notice accepted", res, err)
	} else {
		res.Body.Close()
	}
	attempts, raw := requestRaw(t, "GET", "/v0/deliveries/"+ackedID+"/attempts", admin, nil, 200)
	items := attempts["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["outcome"] != "acknowledged" || items[0].(map[string]any)["http_status"] != float64(204) || items[0].(map[string]any)["number"] != float64(1) {
		t.Fatal("acknowledged attempt history", attempts)
	}
	_, deliveredRaw := requestRaw(t, "GET", "/v0/deliveries/"+ackedID, admin, nil, 200)
	for _, leak := range []string{"whsec_", sig, strings.TrimPrefix(sig, "v1,"), receiverBody, "127.0.0.1", os.Getenv("QUIVR_TEST_RECEIVER_ADDR")} {
		if strings.Contains(raw, leak) || strings.Contains(deliveredRaw, leak) {
			t.Fatal("Delivery reads leak a secret, signature, receiver body or destination", leak)
		}
	}

	// A redirect is not followed and, being non-retryable, ends automatic
	// attempts at once: exhausted after its single attempt.
	moved := awaitDelivery(t, admin, deliveryOf(redirected), func(d map[string]any) bool { return d["state"] == "exhausted" })
	if last := moved["last_error"].(map[string]any); last["code"] != "webhook_redirect_refused" || last["retryable"] != false || moved["attempt_count"] != float64(1) ||
		!reflect.DeepEqual(moved["admission"], map[string]any{"allowed": false, "reason": "terminal"}) || moved["next_attempt_at"] != nil {
		t.Fatal("redirect failure", moved)
	}
	attempts = request(t, "GET", "/v0/deliveries/"+deliveryOf(redirected)+"/attempts", admin, nil, 200)
	if items := attempts["items"].([]any); len(items) != 1 || items[0].(map[string]any)["outcome"] != "permanent_error" || items[0].(map[string]any)["http_status"] != float64(307) {
		t.Fatal("redirect attempt", attempts)
	}

	// SSE carries the same notice identities as polling and the webhook.
	stream := openChangeStream(t, os.Getenv("QUIVR_TEST_URL"), admin, "/v0/changes/stream?corpus_id="+url.QueryEscape(c), start)
	streamed := map[string]map[string]any{}
	for len(streamed) < 2 {
		_, change := stream.nextChange(t)
		if change["type"] == "match.created" {
			streamed[change["event_id"].(string)] = change
		}
	}
	stream.close()
	if s := streamed[body["event_id"].(string)]; s == nil || !reflect.DeepEqual(s["monitoring"], body["references"]) {
		t.Fatal("SSE notice differs from the webhook", s, body)
	}

	// Both attempts completed, so their admission and outcome transitions are
	// committed. Wait for the public feed to expose those transitions.
	// The PostgreSQL adapter test owns the feed-only/no-outbox invariant.
	for _, id := range []string{ackedID, deliveryOf(redirected)} {
		awaitChange(t, admin, c, start, "delivery.updated", id)
	}
	feed, _ := drain(t, admin, c, start, 0)
	updates := 0
	for _, item := range feed {
		if item["type"] == "delivery.updated" {
			if item["resource"].(map[string]any)["kind"] != "delivery" || item["monitoring"] != nil {
				t.Fatal("delivery.updated shape", item)
			}
			if item["resource"].(map[string]any)["id"] == ackedID {
				updates++
			}
		}
	}
	if updates < 2 {
		t.Fatal("acknowledged Delivery transitions missing from the feed", feed)
	}
	captures := receiver.snapshot()
	ids := map[string]bool{}
	for _, capture := range captures {
		if capture.Path != "/capture" || capture.Status == 401 && capture.Header.Get("webhook-signature") != forged.Header.Get("webhook-signature") {
			t.Fatal("unexpected receiver request", capture.Path, capture.Status)
		}
		if capture.Header.Get("webhook-signature") != forged.Header.Get("webhook-signature") {
			ids[capture.Header.Get("webhook-id")] = true
		}
	}
	want := map[string]bool{}
	for _, event := range created {
		want[event["event_id"].(string)] = true
	}
	if !reflect.DeepEqual(ids, want) || len(captures) != 3 {
		t.Fatalf("receiver must see only the two notices (plus the forged probe): %d requests, ids %v", len(captures), ids)
	}
}

// The receiver's verifier accepts the independent contract vector and rejects
// its tampered or stale variants, so acceptance does not trust any shape.
func TestReceiverVerifierMatchesContractVector(t *testing.T) {
	var v map[string]string
	if err := json.Unmarshal(contract.WebhookVector, &v); err != nil {
		t.Fatal(err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v["secret"], "whsec_"))
	if err != nil {
		t.Fatal(err)
	}
	ts, _ := strconv.ParseInt(v["timestamp"], 10, 64)
	at := time.Unix(ts, 0).Add(4 * time.Minute)
	if err := verifyWebhook(key, v["event_id"], v["timestamp"], "v1,bm90LXRoaXM= "+v["signature"], []byte(v["body"]), at); err != nil {
		t.Fatal("vector rejected", err)
	}
	for name, err := range map[string]error{
		"body":      verifyWebhook(key, v["event_id"], v["timestamp"], v["signature"], []byte(v["body"]+" "), at),
		"id":        verifyWebhook(key, v["event_id"]+"x", v["timestamp"], v["signature"], []byte(v["body"]), at),
		"stale":     verifyWebhook(key, v["event_id"], v["timestamp"], v["signature"], []byte(v["body"]), at.Add(2*time.Minute)),
		"future":    verifyWebhook(key, v["event_id"], v["timestamp"], v["signature"], []byte(v["body"]), time.Unix(ts, 0).Add(-6*time.Minute)),
		"version":   verifyWebhook(key, v["event_id"], v["timestamp"], strings.Replace(v["signature"], "v1,", "v2,", 1), []byte(v["body"]), at),
		"other key": verifyWebhook(make([]byte, 32), v["event_id"], v["timestamp"], v["signature"], []byte(v["body"]), at),
	} {
		if err == nil {
			t.Fatal("accepted", name)
		}
	}
}
