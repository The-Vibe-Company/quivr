package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// x_list webhook mode against the local fake X API (tests/fakes/cmd/x): the
// fake registers the instance's webhook_url, checks it with a CRC request the
// core relays to the plugin, and delivers signed Filtered Stream posts to it.
// The list's posts come from a member of its own, so no other test's
// webhook matches them.

// xDeliver changes the fake X state of one list and returns the HTTP status
// the core answered each delivery to instance id's webhook with. Like X, the
// fake delivers a matching post to every webhook linked to the app's stream,
// including those of other tests' instances.
func xDeliver(t *testing.T, base, list, id string, body map[string]any) []int {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(base+"/_control/lists/"+list, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Deliveries []struct {
			URL    string `json:"url"`
			Status int    `json:"status"`
		} `json:"deliveries"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&out) != nil {
		t.Fatalf("fake X control %d", resp.StatusCode)
	}
	var statuses []int
	for _, d := range out.Deliveries {
		if strings.HasSuffix(d.URL, "/v0/connector-webhooks/"+id) {
			statuses = append(statuses, d.Status)
		}
	}
	return statuses
}

func xControlPath(t *testing.T, base, path string, body map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(base+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("fake X control %s: %d", path, resp.StatusCode)
	}
}

func push(h map[string]any) map[string]any {
	p, _ := h["push"].(map[string]any)
	return p
}

func pushState(want string) func(map[string]any) bool {
	return func(h map[string]any) bool { return push(h) != nil && push(h)["state"] == want }
}

// A post published ago before now, by the list's own member, pushed or not.
func xAuthored(id, text, author string, ago time.Duration, pushed any) map[string]any {
	p := map[string]any{"id": id, "text": text, "author_id": author, "created_at": time.Now().Add(-ago).UTC().Format("2006-01-02T15:04:05.000Z")}
	if pushed != nil {
		p["push"] = pushed
	}
	return p
}

// Budget: about 12 s in CI, over the 10 s acceptance budget because recovery
// waits for a webhook resync (resync_interval_seconds 5, the plugin's floor).
func TestConnectorXListReceivesPostsThroughWebhooksWithPollingAsFallback(t *testing.T) {
	token := connectorToken(t)
	base := fakeX(t)
	list := fmt.Sprint(time.Now().UnixNano()%1e15 + 7)
	author := "5" + list
	corpusID, cursor := connectorCorpus(t, token, "x-push")
	xControl(t, base, list, map[string]any{"members": []string{author}})
	// The backfill puts the list's start point before the posts, which are
	// published two minutes back: before push became active, so pull never
	// holds them back.
	webhook := map[string]any{"backfill_since": time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339),
		"webhook": map[string]any{"enabled": true, "resync_interval_seconds": 5, "poll_interval_seconds": 3600}}
	created := request(t, "POST", "/v0/connectors", token, xConnector("x-push", corpusID, "x", list, webhook), 201)
	id := created["connector_id"].(string)
	if url, _ := created["webhook_url"].(string); !strings.HasSuffix(url, "/v0/connector-webhooks/"+id) {
		t.Fatalf("webhook_url %v", created["webhook_url"])
	}
	// Force a pull run now: the schedule pulls the next run in when it changes.
	interval := 1
	pullNow := func() {
		interval = 3 - interval
		request(t, "PUT", "/v0/connectors/"+id+"/schedule", token, map[string]any{"interval_seconds": interval}, 200)
	}

	// The first run registers the webhook (the fake's CRC check reaches the
	// plugin through the core) and links it; pull then relaxes to an hour.
	active := awaitXHealth(t, token, id, pushState("active"))
	if p := push(active["health"].(map[string]any)); p["poll_interval_seconds"] != 3600.0 {
		t.Fatalf("push %v", p)
	}

	// A signed delivery is relayed and accepted: the post arrives while no
	// pull run is due for an hour.
	if got := xDeliver(t, base, list, id, map[string]any{"posts": []any{xAuthored("1810000000000000001", "Pushed wire post", author, 2*time.Minute, true)}}); fmt.Sprint(got) != "[200]" {
		t.Fatalf("delivery answered %v", got)
	}
	recordsByKey(t, token, corpusID, cursor, map[string]int{"1810000000000000001": 1})
	delivered := awaitXHealth(t, token, id, func(h map[string]any) bool { return push(h)["last_delivery_at"] != nil })

	// A forged delivery is refused and changes nothing.
	if got := xDeliver(t, base, list, id, map[string]any{"posts": []any{xAuthored("1810000000000000002", "Forged", author, 2*time.Minute, "forged")}}); fmt.Sprint(got) != "[401]" {
		t.Fatalf("forged delivery answered %v", got)
	}

	// Pull reads the pushed post too: it replays its Receipt, so it is not a
	// new item, and push stays active.
	polled := delivered["health"].(map[string]any)["last_success_at"]
	pullNow()
	after := awaitXHealth(t, token, id, func(h map[string]any) bool { return h["last_success_at"] != polled })
	if pushState("active")(after["health"].(map[string]any)) != true {
		t.Fatalf("a post both paths saw counted as missed: %v", after["health"])
	}

	// The webhook is invalidated and its CRC checks fail: deliveries stop,
	// pull finds a post the webhook never brought (missed deliveries, pull
	// back at the instance's interval), and the resync of the same run finds
	// the webhook invalid: access_error, while pull collects.
	xControlPath(t, base, "/_control/app", map[string]any{"crc_fails": true})
	xControlPath(t, base, "/_control/webhooks", map[string]any{"invalidate": true})
	if got := xDeliver(t, base, list, id, map[string]any{"posts": []any{xAuthored("1810000000000000003", "Missed by the webhook", author, 2*time.Minute, true)}}); len(got) != 0 {
		t.Fatalf("an invalid webhook received %v", got)
	}
	pullNow()
	refused := awaitXHealth(t, token, id, func(h map[string]any) bool { return h["state"] == "access_error" })
	if e := push(refused["health"].(map[string]any))["error"].(map[string]any); push(refused["health"].(map[string]any))["state"] != "degraded" || e["code"] != "webhook_invalid" || e["class"] != "access" {
		t.Fatalf("push %v", push(refused["health"].(map[string]any)))
	}
	byKey, events := recordsByKey(t, token, corpusID, cursor, map[string]int{"1810000000000000001": 1, "1810000000000000003": 1})

	// CRC checks pass again: the next resync re-validates the webhook and
	// Connector Health recovers; a delivery with items clears the missed
	// deliveries, and push is active again.
	xControlPath(t, base, "/_control/app", map[string]any{"crc_fails": false})
	awaitXHealth(t, token, id, state("active"))
	if got := xDeliver(t, base, list, id, map[string]any{"posts": []any{xAuthored("1810000000000000004", "Delivered again", author, 2*time.Minute, true)}}); fmt.Sprint(got) != "[200]" {
		t.Fatalf("delivery after recovery answered %v", got)
	}
	awaitXHealth(t, token, id, pushState("active"))
	byKey, events = recordsByKey(t, token, corpusID, cursor, map[string]int{"1810000000000000004": 1})
	if typed(events, "record.materialized", byKey["1810000000000000001"]) != 1 {
		t.Fatal("the pushed post made a second Version when pull read it")
	}
	if _, forged := byKey["1810000000000000002"]; forged {
		t.Fatal("a forged delivery made a Record")
	}
	noXSecret(t, request(t, "GET", "/v0/connectors/"+id, token, nil, 200))
	request(t, "POST", "/v0/connectors/"+id+"/disable", token, map[string]any{"idempotency_key": "x-push-stop"}, 200)
}
