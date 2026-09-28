package acceptance

import (
	"os"
	"testing"
	"time"
)

// TestKeylessCoreServesIngestionSearchAndPublicRSS runs only against the api
// and worker that scripts/local.py restarts without credential_key, in their
// own Organization.
func TestKeylessCoreServesIngestionSearchAndPublicRSS(t *testing.T) {
	token := os.Getenv("QUIVR_TEST_KEYLESS")
	if os.Getenv("QUIVR_TEST_KEYLESS_MODE") == "" || token == "" {
		t.Skip("make verify restarts the core without credential_key for this test")
	}
	corpusID, cursor := connectorCorpus(t, token, "keyless")
	if catalog := request(t, "GET", "/v0/connector-kinds", token, nil, 200); catalog["credential_deposits"] != "unavailable" {
		t.Fatalf("keyless catalog must announce unavailable deposits: %v", catalog)
	}

	// Ingestion and search work without a credential key.
	text := "Keyless deployment dispatch"
	accepted := request(t, "POST", "/v0/records", token, inlineCommand(corpusID, "keyless-note-"+connectorRun, "keyless-note", text), 202)
	var receipt map[string]any
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		receipt = request(t, "GET", "/v0/ingestion-receipts/"+accepted["receipt_id"].(string), token, nil, 200)
		if receipt["state"] == "resolved" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("receipt never resolved: %v", receipt)
		}
	}
	query := map[string]any{"query": "Keyless", "corpus_ids": []string{corpusID}, "mode": "lexical"}
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		hits := request(t, "POST", "/v0/search", token, query, 200)["items"].([]any)
		if len(hits) == 1 && hits[0].(map[string]any)["record_id"] == receipt["record_id"] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("record never searchable: %v", hits)
		}
	}

	// A public RSS instance needs no credential: created, replayed and collected.
	feed := newFakeFeed(t, rssDocument(`<item><guid>keyless-alpha</guid><title>Keyless headline</title><link>https://wire.example.org/keyless</link><description>Keyless feed body.</description></item>`))
	body := rssConnector("keyless-rss", corpusID, "wire", feed.URL+"/feed.xml", 86400)
	created := request(t, "POST", "/v0/connectors", token, body, 201)
	id := created["connector_id"].(string)
	if created["credential"] != nil {
		t.Fatalf("public RSS carries a credential: %v", created)
	}
	if replay := request(t, "POST", "/v0/connectors", token, body, 201); replay["connector_id"] != id {
		t.Fatalf("replay changed identity: %v", replay)
	}
	changed := rssConnector("keyless-rss", corpusID, "other-wire", feed.URL+"/feed.xml", 86400)
	if e := request(t, "POST", "/v0/connectors", token, changed, 409); e["code"] != "idempotency_conflict" {
		t.Fatalf("changed body under the same key: %v", e)
	}
	recordsByKey(t, token, corpusID, cursor, map[string]int{"keyless-alpha": 1})
	awaitHealth(t, token, id, state("active"))

	// Credential deposits and rotations are refused, bounded and not retryable.
	deposit := rssConnector("keyless-rss-auth", corpusID, "auth-wire", feed.URL+"/private.xml", 86400)
	deposit["credential"] = map[string]any{"secret": map[string]any{"token": "fixture-test-secret-keyless-not-real"}}
	if e := request(t, "POST", "/v0/connectors", token, deposit, 503); e["code"] != "credentials_unavailable" || e["retryable"] != false {
		t.Fatalf("credential deposit: %v", e)
	}
	noSecret(t, request(t, "PUT", "/v0/connectors/"+id+"/credential", token, map[string]any{"idempotency_key": "keyless-rotate", "secret": map[string]any{"token": "fixture-test-secret-keyless-not-real"}}, 503))
	if c := request(t, "GET", "/v0/connectors/"+id, token, nil, 200); c["credential"] != nil {
		t.Fatalf("refused rotation stored a credential: %v", c)
	}
	request(t, "POST", "/v0/connectors/"+id+"/disable", token, map[string]any{"idempotency_key": "keyless-disable"}, 200)
}
