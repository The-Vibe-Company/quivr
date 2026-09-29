package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// x_list acceptance polls the local fake X API (scripts/fake_x.py) that
// scripts/local.py serves; each test owns a list id and a Corpus in org_c.

const xTestToken = "x-test-bearer-acceptance-not-real"

func fakeX(t *testing.T) string {
	t.Helper()
	u := os.Getenv("QUIVR_TEST_FAKE_X_URL")
	if u == "" {
		t.Skip("run make verify for x_list acceptance against the fake X API")
	}
	return u
}

// xControl changes the fake X state of one list.
func xControl(t *testing.T, base, list string, body map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(base+"/_control/lists/"+list, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("fake X control %d", resp.StatusCode)
	}
}

func xPost(id, text string) map[string]any {
	// X timestamps carry milliseconds; the instance's start point is sub-second.
	return map[string]any{"id": id, "text": text, "created_at": time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}
}

func xConnector(key, corpusID, namespace, list string, config map[string]any) map[string]any {
	cfg := map[string]any{"list_id": list}
	for k, v := range config {
		cfg[k] = v
	}
	return map[string]any{"idempotency_key": key + "-" + connectorRun, "corpus_id": corpusID, "source_namespace": namespace, "kind": "x_list",
		"config": cfg, "schedule": map[string]any{"interval_seconds": 1},
		"credential": map[string]any{"secret": map[string]any{"bearer_token": xTestToken, "consumer_secret": "x-test-consumer-not-real"}}}
}

func noXSecret(t *testing.T, bodies ...map[string]any) {
	t.Helper()
	for _, b := range bodies {
		raw, _ := json.Marshal(b)
		if strings.Contains(string(raw), "x-test-bearer") || strings.Contains(string(raw), "x-test-consumer") {
			t.Fatalf("secret leaked in response: %s", raw)
		}
	}
}

func awaitXHealth(t *testing.T, token, id string, ok func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		c := request(t, "GET", "/v0/connectors/"+id, token, nil, 200)
		noXSecret(t, c)
		if ok(c["health"].(map[string]any)) {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("health never reached expectation: %v", c)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func lastError(code string) func(map[string]any) bool {
	return func(h map[string]any) bool {
		e, _ := h["last_error"].(map[string]any)
		return e != nil && e["code"] == code
	}
}

func TestConnectorXListCollectsPostsCorrectsEditsAndWithdrawsDeletions(t *testing.T) {
	token := connectorToken(t)
	base := fakeX(t)
	list := fmt.Sprint(time.Now().UnixNano() % 1e15)
	corpusID, cursor := connectorCorpus(t, token, "x-list")
	// A post published before the instance exists is not collected (start now).
	xControl(t, base, list, map[string]any{"posts": []any{map[string]any{"id": "1800000000000000001", "text": "Before creation", "created_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}}})
	// A backfill beyond 7 days is refused at the first poll (the plugin checks it).
	badWindow := request(t, "POST", "/v0/connectors", token, xConnector("x-bad-window", corpusID, "x-bad", list, map[string]any{"backfill_since": time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339)}), 201)
	created := request(t, "POST", "/v0/connectors", token, xConnector("x-list", corpusID, "x", list, map[string]any{"recheck_interval_seconds": 60}), 201)
	noXSecret(t, created)
	id := created["connector_id"].(string)
	awaitXHealth(t, token, badWindow["connector_id"].(string), lastError("invalid_config"))
	awaitXHealth(t, token, id, func(h map[string]any) bool { return h["last_success_at"] != nil })

	// Initial page, then an incremental poll down to the watermark.
	xControl(t, base, list, map[string]any{"posts": []any{xPost("1800000000000000002", "First wire post"), xPost("1800000000000000003", "Second wire post")}})
	byKey, _ := recordsByKey(t, token, corpusID, cursor, map[string]int{"1800000000000000002": 1, "1800000000000000003": 1})
	var more []any
	for i := 4; i <= 9; i++ {
		more = append(more, xPost(fmt.Sprintf("180000000000000000%d", i), fmt.Sprintf("Post %d", i)))
	}
	xControl(t, base, list, map[string]any{"posts": more})
	want := map[string]int{"1800000000000000002": 1, "1800000000000000003": 1}
	for i := 4; i <= 9; i++ {
		want[fmt.Sprintf("180000000000000000%d", i)] = 1
	}
	byKey, events := recordsByKey(t, token, corpusID, cursor, want)
	if _, ok := byKey["1800000000000000001"]; ok {
		t.Fatal("a post from before creation was collected")
	}
	if typed(events, "record.materialized", byKey["1800000000000000002"]) != 1 {
		t.Fatal("re-polled post created another version")
	}
	version := func(recordID string) map[string]any {
		r := request(t, "GET", "/v0/records/"+recordID, token, nil, 200)
		return request(t, "GET", "/v0/records/"+recordID+"/versions/"+r["current_version_id"].(string), token, nil, 200)
	}
	first := version(byKey["1800000000000000002"])
	ext := first["extensions"].(map[string]any)["connector.x_list"].(map[string]any)["data"].(map[string]any)
	if ext["post_id"] != "1800000000000000002" || ext["author"].(map[string]any)["username"] != "example_desk" || first["provenance"].(map[string]any)["producer_version"] != "x_list/v1" {
		t.Fatalf("version %v", first)
	}

	// An edit (new post id, same edit chain) is a correction of the same Record.
	edit := xPost("1800000000000000010", "Second wire post, corrected")
	edit["edit_history_tweet_ids"] = []string{"1800000000000000003", "1800000000000000010"}
	xControl(t, base, list, map[string]any{"posts": []any{edit}})
	want["1800000000000000003"] = 2
	byKey, _ = recordsByKey(t, token, corpusID, cursor, want)
	if _, ok := byKey["1800000000000000010"]; ok {
		t.Fatal("an edit created a new Record")
	}
	for deadline := time.Now().Add(120 * time.Second); ; time.Sleep(250 * time.Millisecond) {
		v := version(byKey["1800000000000000003"])
		if v["manifest"].(map[string]any)["parts"].([]any)[0].(map[string]any)["content"].(map[string]any)["text"] == "Second wire post, corrected" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("edit never became current: %v", v)
		}
	}

	// Deleted and protected posts are withdrawn by the periodic recheck.
	xControl(t, base, list, map[string]any{"delete": []string{"1800000000000000004"}, "protect": []string{"1800000000000000005"}})
	// The recheck runs every 60 s (the configured minimum), so allow two intervals.
	for _, key := range []string{"1800000000000000004", "1800000000000000005"} {
		for deadline := time.Now().Add(150 * time.Second); ; time.Sleep(500 * time.Millisecond) {
			if r := request(t, "GET", "/v0/records/"+byKey[key], token, nil, 200); r["withdrawn"] == true {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("post %s was never withdrawn", key)
			}
		}
		awaitChange(t, token, corpusID, cursor, "record.withdrawn", byKey[key])
	}
	if r := request(t, "GET", "/v0/records/"+byKey["1800000000000000006"], token, nil, 200); r["withdrawn"] != false {
		t.Fatalf("a live post was withdrawn: %v", r)
	}

	// Health exposes daily reads and the recheck coverage.
	healthy := awaitXHealth(t, token, id, func(h map[string]any) bool { return h["state"] == "active" && h["diagnostics"] != nil })
	h := healthy["health"].(map[string]any)
	if u := h["usage"].(map[string]any); u["items_read"].(float64) < 9 || u["day"] != time.Now().UTC().Format(time.DateOnly) {
		t.Fatalf("usage %v", h)
	}
	if d := h["diagnostics"].(map[string]any); d["recheck_window_seconds"].(float64) != 86400 || d["last_recheck_at"] == nil {
		t.Fatalf("diagnostics %v", h)
	}
	request(t, "POST", "/v0/connectors/"+id+"/disable", token, map[string]any{"idempotency_key": "x-list-stop"}, 200)
}

func TestConnectorXListReportsRateLimitsAndExhaustedCredits(t *testing.T) {
	token := connectorToken(t)
	base := fakeX(t)
	list := fmt.Sprint(time.Now().UnixNano()%1e15 + 1)
	corpusID, _ := connectorCorpus(t, token, "x-failures")
	c := request(t, "POST", "/v0/connectors", token, xConnector("x-failures", corpusID, "x", list, nil), 201)
	id := c["connector_id"].(string)
	awaitXHealth(t, token, id, func(h map[string]any) bool { return h["last_success_at"] != nil })

	// 429: a transient failure visible in last_error, never access_error; polling resumes after the reset.
	xControl(t, base, list, map[string]any{"fail": map[string]any{"status": 429, "reset_in": 2, "times": 1}})
	limited := awaitXHealth(t, token, id, lastError("rate_limited"))
	if limited["health"].(map[string]any)["state"] == "access_error" {
		t.Fatalf("rate limit reported as access error: %v", limited)
	}
	resumed := limited["health"].(map[string]any)["last_success_at"]
	awaitXHealth(t, token, id, func(h map[string]any) bool { return h["last_success_at"] != resumed })

	// 402: exhausted credits are an access error with a distinct code, until a later success.
	xControl(t, base, list, map[string]any{"fail": map[string]any{"status": 402}})
	depleted := awaitXHealth(t, token, id, func(h map[string]any) bool { return h["state"] == "access_error" && lastError("credits_depleted")(h) })
	noXSecret(t, depleted)
	xControl(t, base, list, map[string]any{"fail": nil})
	awaitXHealth(t, token, id, state("active"))

	// A revoked token is refused as unauthorized.
	request(t, "PUT", "/v0/connectors/"+id+"/credential", token, map[string]any{"idempotency_key": "x-revoke", "secret": map[string]any{"bearer_token": "x-revoked-acceptance-token"}}, 200)
	awaitXHealth(t, token, id, func(h map[string]any) bool { return h["state"] == "access_error" && lastError("unauthorized")(h) })
	request(t, "POST", "/v0/connectors/"+id+"/disable", token, map[string]any{"idempotency_key": "x-failures-stop"}, 200)
}
