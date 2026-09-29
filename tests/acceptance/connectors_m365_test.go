package acceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// m365_mail acceptance runs against scripts/fake_graph.py (started by
// scripts/local.py), never against Microsoft. Each test owns its mailbox and
// application, so the fake's state cannot leak between tests.

const m365Tenant = "00000000-0000-0000-0000-000000000000"

type fakeGraph struct {
	t   *testing.T
	url string
}

func graph(t *testing.T) fakeGraph {
	t.Helper()
	u := os.Getenv("QUIVR_TEST_FAKE_GRAPH_URL")
	if u == "" {
		t.Skip("run make verify for m365_mail acceptance against the fake Graph")
	}
	return fakeGraph{t: t, url: u}
}

func (g fakeGraph) post(path string, body map[string]any) {
	g.t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(g.url+path, "application/json", bytes.NewReader(b))
	if err != nil {
		g.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		g.t.Fatalf("fake graph %s: %d", path, resp.StatusCode)
	}
}

func (g fakeGraph) stats(mailbox, clientID string) map[string]float64 {
	g.t.Helper()
	resp, err := http.Get(g.url + "/_fake/stats?" + url.Values{"mailbox": {mailbox}, "client_id": {clientID}}.Encode())
	if err != nil {
		g.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]float64{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func (g fakeGraph) mail(mailbox, id string, received time.Time, attachments ...map[string]any) {
	g.t.Helper()
	if attachments == nil {
		attachments = []map[string]any{}
	}
	g.post("/_fake/messages", map[string]any{"mailbox": mailbox, "id": id, "received": received.UTC().Format(time.RFC3339), "subject": "Alerte " + id,
		"html": "<html><head><style>p{}</style></head><body><p>Bonjour, voici la dépêche <b>" + id + "</b>.</p></body></html>", "attachments": attachments})
}

func m365Connector(key, corpusID, namespace, mailbox, clientID, secret string, extra map[string]any) map[string]any {
	body := map[string]any{"idempotency_key": key + "-" + connectorRun, "corpus_id": corpusID, "source_namespace": namespace, "kind": "m365_mail",
		"config":   map[string]any{"tenant_id": m365Tenant, "mailbox": mailbox, "backfill_since": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)},
		"schedule": map[string]any{"interval_seconds": 1},
		"credential": map[string]any{"secret": map[string]any{"client_id": clientID, "client_secret": secret},
			"expires_at": time.Now().Add(90 * 24 * time.Hour).UTC().Format(time.RFC3339)}}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

// noM365Secret fails when a response carries the deposited secret or a token.
func noM365Secret(t *testing.T, secret string, bodies ...map[string]any) {
	t.Helper()
	for _, b := range bodies {
		raw, _ := json.Marshal(b)
		if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "fake-access-") {
			t.Fatalf("secret leaked in response: %s", raw)
		}
	}
}

func currentVersion(t *testing.T, token, recordID string) map[string]any {
	t.Helper()
	for deadline := time.Now().Add(120 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		r := request(t, "GET", "/v0/records/"+recordID, token, nil, 200)
		if v, ok := r["current_version_id"].(string); ok {
			return request(t, "GET", "/v0/records/"+recordID+"/versions/"+v, token, nil, 200)
		}
		if time.Now().After(deadline) {
			t.Fatalf("record never current: %v", r)
		}
	}
}

func partsByKey(version map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, p := range version["manifest"].(map[string]any)["parts"].([]any) {
		part := p.(map[string]any)
		out[part["key"].(string)] = part
	}
	return out
}

func TestConnectorM365CollectsMailBodiesAndAttachments(t *testing.T) {
	token := connectorToken(t)
	g := graph(t)
	mailbox := "monitoring-collect-" + connectorRun + "@example.org"
	clientID, secret := "app-collect-"+connectorRun, "m365-acceptance-secret-not-real-collect"
	g.post("/_fake/apps", map[string]any{"client_id": clientID, "secret": secret})
	pdf := []byte("%PDF-1.7 pièce jointe de test")
	g.mail(mailbox, "before-backfill", time.Now().Add(-2*time.Hour))
	g.mail(mailbox, "m1", time.Now().Add(-30*time.Minute),
		map[string]any{"id": "a1", "name": "communique.pdf", "content_type": "application/pdf", "data_b64": base64.StdEncoding.EncodeToString(pdf)},
		map[string]any{"id": "a2", "name": "video.zip", "content_type": "application/zip", "size": 26 << 20},
		map[string]any{"id": "a3", "name": "shared link", "type": "reference", "size": 100})
	corpusID, cursor := connectorCorpus(t, token, "m365-collect")

	// A backfill older than 7 days is refused when collection starts: the
	// plugin reports invalid_config and reads nothing.
	old := m365Connector("m365-old", corpusID, "mail-old", mailbox, clientID, secret, nil)
	old["config"].(map[string]any)["backfill_since"] = time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339)
	oldID := request(t, "POST", "/v0/connectors", token, old, 201)["connector_id"].(string)
	awaitHealth(t, token, oldID, func(h map[string]any) bool {
		e, _ := h["last_error"].(map[string]any)
		return e != nil && e["code"] == "invalid_config"
	})
	request(t, "POST", "/v0/connectors/"+oldID+"/disable", token, map[string]any{"idempotency_key": "m365-old-stop"}, 200)
	// An invalid credential is refused before anything is stored.
	both := m365Connector("m365-both", corpusID, "mail-both", mailbox, clientID, secret, nil)
	both["credential"].(map[string]any)["secret"].(map[string]any)["certificate_pem"] = "-----BEGIN CERTIFICATE-----"
	if e := request(t, "POST", "/v0/connectors", token, both, 422); e["code"] != "invalid_credential" {
		t.Fatal(e)
	}

	created := request(t, "POST", "/v0/connectors", token, m365Connector("m365-collect", corpusID, "mail", mailbox, clientID, secret, nil), 201)
	noM365Secret(t, secret, created)
	id := created["connector_id"].(string)
	m1 := "<m1@example.org>"
	byKey, _ := recordsByKey(t, token, corpusID, cursor, map[string]int{m1: 1})
	if _, found := byKey["<before-backfill@example.org>"]; found {
		t.Fatal("a mail received before backfill_since was collected")
	}
	version := currentVersion(t, token, byKey[m1])
	parts := partsByKey(version)
	if parts["title"]["content"].(map[string]any)["text"] != "Alerte m1" {
		t.Fatalf("title %v", parts["title"])
	}
	if body := parts["body"]["content"].(map[string]any)["text"].(string); body != "Bonjour, voici la dépêche m1." {
		t.Fatalf("body text %q", body)
	}
	original := parts["original_body"]["content"].(map[string]any)
	if original["kind"] != "blob" || original["media_type"] != "text/html" {
		t.Fatalf("original body %v", original)
	}
	attachment := parts["attachment-01"]
	content := attachment["content"].(map[string]any)
	blob := request(t, "GET", "/v0/blobs/"+content["blob_id"].(string), token, nil, 200)
	sum := sha256.Sum256(pdf)
	if content["media_type"] != "application/pdf" || blob["sha256"] != hex.EncodeToString(sum[:]) || int(blob["size_bytes"].(float64)) != len(pdf) {
		t.Fatalf("attachment %v blob %v", attachment, blob)
	}
	if meta := attachment["extensions"].(map[string]any)["connector.m365_mail.attachment"].(map[string]any)["data"].(map[string]any); meta["name"] != "communique.pdf" {
		t.Fatalf("attachment metadata %v", meta)
	}
	headers := version["extensions"].(map[string]any)["connector.m365_mail"].(map[string]any)["data"].(map[string]any)
	if headers["internet_message_id"] != m1 || headers["conversation_id"] != "conv-m1" || headers["from"].(map[string]any)["address"] != "desk@example.org" {
		t.Fatalf("headers %v", headers)
	}
	reasons := map[string]bool{}
	for _, s := range headers["attachments_skipped"].([]any) {
		reasons[s.(map[string]any)["reason"].(string)] = true
	}
	if !reasons["too_large"] || !reasons["reference_attachment"] || len(reasons) != 2 {
		t.Fatalf("skipped %v", headers["attachments_skipped"])
	}
	if p, _ := version["provenance"].(map[string]any); p["producer"] != id || p["producer_version"] != "m365_mail/v1" {
		t.Fatalf("provenance %v", version["provenance"])
	}

	// Incremental delta: a new mail arrives; the first one is read, then deleted.
	g.mail(mailbox, "m2", time.Now())
	g.post("/_fake/update", map[string]any{"mailbox": mailbox, "id": "m1", "fields": map[string]any{"isRead": true}})
	byKey, events := recordsByKey(t, token, corpusID, cursor, map[string]int{m1: 1, "<m2@example.org>": 1})
	g.post("/_fake/delete", map[string]any{"mailbox": mailbox, "id": "m1"})
	// Let several scheduled polls observe the read flag and the deletion.
	before := g.stats(mailbox, clientID)["delta"]
	for deadline := time.Now().Add(60 * time.Second); g.stats(mailbox, clientID)["delta"] < before+3; time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the connector stopped polling")
		}
	}
	events, _ = drain(t, token, corpusID, cursor, 0)
	if n := typed(events, "record.materialized", byKey[m1]); n != 1 {
		t.Fatalf("reading a mail created %d versions", n)
	}
	if typed(events, "record.withdrawn", byKey[m1]) != 0 {
		t.Fatal("deleting a mail in the mailbox withdrew its Record")
	}
	if r := request(t, "GET", "/v0/records/"+byKey[m1], token, nil, 200); r["current_version_id"] != version["version_id"] {
		t.Fatalf("deleted mail's Record changed: %v", r)
	}
	stats := g.stats(mailbox, clientID)
	if stats["value"] != 1 {
		t.Fatalf("attachment downloaded %v times; an accepted revision must not be fetched again", stats["value"])
	}
	if stats["tokens_issued"] != 1 {
		t.Fatalf("%v tokens issued; the access token must be cached", stats["tokens_issued"])
	}
	healthy := awaitHealth(t, token, id, func(h map[string]any) bool { return h["state"] == "active" && h["last_item_at"] != nil })
	noM365Secret(t, secret, healthy)
	request(t, "POST", "/v0/connectors/"+id+"/disable", token, map[string]any{"idempotency_key": "m365-collect-stop"}, 200)
}

func TestConnectorM365ReportsAccessErrorsThrottlingAndResyncs(t *testing.T) {
	token := connectorToken(t)
	g := graph(t)
	mailbox := "monitoring-faults-" + connectorRun + "@example.org"
	clientID, secret := "app-faults-"+connectorRun, "m365-acceptance-secret-not-real-faults"
	g.post("/_fake/apps", map[string]any{"client_id": clientID, "secret": secret})
	g.mail(mailbox, "f1", time.Now().Add(-10*time.Minute))
	// A short throttle is waited within the run.
	g.post("/_fake/fail", map[string]any{"mailbox": mailbox, "status": 429, "code": "TooManyRequests", "retry_after": 1, "path": "/messages/delta"})
	corpusID, cursor := connectorCorpus(t, token, "m365-faults")
	c := request(t, "POST", "/v0/connectors", token, m365Connector("m365-faults", corpusID, "mail", mailbox, clientID, secret, nil), 201)
	id := c["connector_id"].(string)
	byKey, _ := recordsByKey(t, token, corpusID, cursor, map[string]int{"<f1@example.org>": 1})

	// Mailbox removed from the application's access scope: access_error, not silence.
	g.post("/_fake/fail", map[string]any{"mailbox": mailbox, "status": 403, "code": "ErrorAccessDenied", "count": 10000})
	denied := awaitHealth(t, token, id, state("access_error"))
	if e := denied["health"].(map[string]any)["last_error"].(map[string]any); e["code"] != "mailbox_access_denied" {
		t.Fatalf("last_error %v", e)
	}
	g.post("/_fake/clear-failures", map[string]any{"mailbox": mailbox})
	awaitHealth(t, token, id, state("active"))

	// Sustained throttling ends runs with the throttled code, then recovers.
	g.post("/_fake/fail", map[string]any{"mailbox": mailbox, "status": 429, "code": "TooManyRequests", "retry_after": 1, "count": 3, "path": "/messages/delta"})
	awaitHealth(t, token, id, func(h map[string]any) bool {
		e, _ := h["last_error"].(map[string]any)
		return e != nil && e["code"] == "throttled" && h["state"] == "active"
	})

	// An expired delta token resyncs the window without duplicating Records.
	g.post("/_fake/expire-delta", map[string]any{"mailbox": mailbox})
	g.mail(mailbox, "f2", time.Now())
	byKey, _ = recordsByKey(t, token, corpusID, cursor, map[string]int{"<f1@example.org>": 1, "<f2@example.org>": 1})
	if g.stats(mailbox, clientID)["resyncs"] < 1 {
		t.Fatal("the delta token never expired")
	}
	before := g.stats(mailbox, clientID)["delta"]
	for deadline := time.Now().Add(60 * time.Second); g.stats(mailbox, clientID)["delta"] < before+3; time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the connector stopped polling")
		}
	}
	events, _ := drain(t, token, corpusID, cursor, 0)
	if n := typed(events, "record.materialized", byKey["<f1@example.org>"]); n != 1 {
		t.Fatalf("resync duplicated f1 into %d versions", n)
	}
	request(t, "POST", "/v0/connectors/"+id+"/disable", token, map[string]any{"idempotency_key": "m365-faults-stop"}, 200)

	// An unknown mailbox is an access error with its own code.
	unknown := request(t, "POST", "/v0/connectors", token, m365Connector("m365-unknown", corpusID, "mail-unknown", "unknown-"+connectorRun+"@example.org", clientID, secret, nil), 201)
	missing := awaitHealth(t, token, unknown["connector_id"].(string), state("access_error"))
	if e := missing["health"].(map[string]any)["last_error"].(map[string]any); e["code"] != "mailbox_not_found" {
		t.Fatalf("last_error %v", e)
	}
	request(t, "POST", "/v0/connectors/"+unknown["connector_id"].(string)+"/disable", token, map[string]any{"idempotency_key": "m365-unknown-stop"}, 200)
}

func TestConnectorM365SecretExpiryWarnsThenCutsAccessUntilRotation(t *testing.T) {
	token := connectorToken(t)
	g := graph(t)
	mailbox := "monitoring-expiry-" + connectorRun + "@example.org"
	clientID, secret := "app-expiry-"+connectorRun, "m365-acceptance-secret-not-real-expiry"
	g.post("/_fake/apps", map[string]any{"client_id": clientID, "secret": secret})
	corpusID, _ := connectorCorpus(t, token, "m365-expiry")
	body := m365Connector("m365-expiry", corpusID, "mail", mailbox, clientID, secret, nil)
	body["credential"].(map[string]any)["expires_at"] = time.Now().Add(10 * 24 * time.Hour).UTC().Format(time.RFC3339)
	c := request(t, "POST", "/v0/connectors", token, body, 201)
	id := c["connector_id"].(string)
	awaitHealth(t, token, id, state("credential_expiring"))
	// The secret expires in Entra ID: the token endpoint refuses it.
	g.post("/_fake/apps", map[string]any{"client_id": clientID, "secret": secret, "expired": true})
	expired := awaitHealth(t, token, id, state("access_error"))
	if e := expired["health"].(map[string]any)["last_error"].(map[string]any); e["code"] != "secret_expired" {
		t.Fatalf("last_error %v", e)
	}
	// Rotation: a new secret is created in Entra ID and deposited.
	rotated := "m365-acceptance-secret-not-real-rotated"
	g.post("/_fake/apps", map[string]any{"client_id": clientID, "secret": rotated})
	replaced := request(t, "PUT", "/v0/connectors/"+id+"/credential", token, map[string]any{"idempotency_key": "m365-rotate",
		"secret": map[string]any{"client_id": clientID, "client_secret": rotated}, "expires_at": time.Now().Add(180 * 24 * time.Hour).UTC().Format(time.RFC3339)}, 200)
	noM365Secret(t, rotated, replaced)
	awaitHealth(t, token, id, state("active"))
	request(t, "POST", "/v0/connectors/"+id+"/disable", token, map[string]any{"idempotency_key": "m365-expiry-stop"}, 200)
}
