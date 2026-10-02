package httpapi_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

// The HTTP boundary owns authorization ordering, route dispatch, bounds and
// the receipt response. Real content admission runs; only durable storage and
// the remote source are replaced. A bypass must fail before either sees data.
func TestConnectorAPIRequiresScopedPushPermissionAndReturnsReceipts(t *testing.T) {
	var seen []struct {
		Route   string                 `json:"route"`
		Body    json.RawMessage        `json:"body"`
		Request devhost.RelayedRequest `json:"request"`
	}
	answer := &plugins.ConnectorDelivery{Verdict: "accepted", Response: plugins.ReceiveAnswer{Status: 204, Body: "plugin answer"}, Items: []plugins.ConnectorItem{{RecordKey: "event-7", Revision: "7", Content: json.RawMessage(`{"kind":"text","text":"Pushed event"}`)}}}
	var pin *plugins.Pin
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/discovery" {
			_ = json.NewEncoder(w).Encode(map[string]any{"plugin_api": pin.PluginAPI(), "plugin": map[string]string{"id": pin.Manifest.ID, "version": pin.Manifest.Version}, "manifest_digest": pin.ManifestDigest, "contributions": pin.Manifest.Contributions.Names()})
			return
		}
		if r.URL.Path != "/v0/contributions/connector/receive" {
			http.NotFound(w, r)
			return
		}
		var request struct {
			Route   string                 `json:"route"`
			Body    json.RawMessage        `json:"body"`
			Request devhost.RelayedRequest `json:"request"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		seen = append(seen, request)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer)
	}))
	defer server.Close()
	manifest := []byte(`id: example-push
version: 1.0.0
compatibility: {engine: ">=0.1.0 <0.2.0", plugin_api: ">=0.11.0 <0.12.0"}
contributions:
  connector:
    kinds:
      echo:
        config_schema: {type: object}
        default_interval_seconds: 300
        modes: [pull, push]
        api:
          routes:
            - {name: push, method: POST, path: "events/{category}", auth: quivr_key, request_schema: {type: object, required: [text], properties: {text: {type: string}}}}
            - {name: challenge, method: GET, path: challenge, auth: quivr_key}
            - {name: status, method: GET, path: events/status, auth: quivr_key}
`)
	var err error
	pin, err = plugins.LoadPinManifest(manifest, "test plugin", plugins.PinConfig{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := connectors.NewRegistry(pluginhttp.Connector{Pin: pin, Name: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	store := &onePushInstance{target: connectors.Target{Instance: connectors.Instance{Organization: "org_a", ID: "connector_push", CorpusID: "corpus_news", Namespace: "events", Kind: "echo", Config: json.RawMessage(`{}`), Enabled: true}}}
	port := &acceptancePort{}
	contents := content.Service{Submissions: port, Receipts: port, RecordStore: port, Versions: port, Materialization: port}
	keys := map[string]corpus.Scope{
		"push-key":     {Organization: "org_a", Actions: []string{"connector:push"}, Corpora: []string{"corpus_news"}},
		"wrong-action": {Organization: "org_a", Actions: []string{"content:write", "connectors:write"}, Corpora: []string{"*"}},
		"wrong-corpus": {Organization: "org_a", Actions: []string{"connector:push"}, Corpora: []string{"corpus_other"}},
		"wrong-org":    {Organization: "org_b", Actions: []string{"connector:push"}, Corpora: []string{"*"}},
	}
	handler, err := httpapi.New(nil, contents, retrieval.Service{}, uploads.Service{}, keys, catalogCursorKey, httpapi.WithRelay(connectors.Relay{Store: store, Registry: registry, Ingest: contents}))
	if err != nil {
		t.Fatal(err)
	}
	send := func(method, path, key string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Cookie", "secret=1")
		rec := httptest.NewRecorder()
		checkedAPI(t, handler).ServeHTTP(rec, req)
		return rec
	}
	path := "/v0/connectors/connector_push/api/events/news"
	for _, tc := range []struct {
		name, method, path, key string
		body                    []byte
		status                  int
		allow                   string
	}{
		{"no key", "POST", path, "", []byte(`{"text":"event"}`), 401, ""},
		{"unknown key", "POST", path, "unknown", []byte(`{"text":"event"}`), 401, ""},
		{"missing push permission", "POST", path, "wrong-action", []byte(`{"text":"event"}`), 403, ""},
		{"another collection", "POST", path, "wrong-corpus", []byte(`{"text":"event"}`), 404, ""},
		{"another organization", "POST", path, "wrong-org", []byte(`{"text":"event"}`), 404, ""},
		{"undeclared path", "POST", "/v0/connectors/connector_push/api/unknown", "push-key", []byte(`{}`), 404, ""},
		{"unknown instance", "POST", "/v0/connectors/unknown/api/events/news", "push-key", []byte(`{}`), 404, ""},
		{"undeclared method", "PUT", path, "push-key", []byte(`{}`), 405, "POST"},
		{"literal path precedence", "POST", "/v0/connectors/connector_push/api/events/status", "push-key", []byte(`{}`), 405, "GET"},
		{"malformed JSON", "POST", path, "push-key", []byte(`{`), 400, ""},
		{"schema mismatch", "POST", path, "push-key", []byte(`{"text":3}`), 422, ""},
		{"body limit", "POST", path, "push-key", bytes.Repeat([]byte("x"), 1<<20+1), 413, ""},
		{"query limit", "POST", path + "?" + strings.Repeat("x", 8193), "push-key", []byte(`{}`), 413, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(seen)
			rec := send(tc.method, tc.path, tc.key, tc.body)
			if rec.Code != tc.status || len(seen) != before || len(port.accepted) != 0 || rec.Header().Get("Allow") != tc.allow {
				t.Fatalf("want %d Allow=%q without plugin/ingest, got %d %s Allow=%q calls=%d accepted=%v", tc.status, tc.allow, rec.Code, rec.Body.String(), rec.Header().Get("Allow"), len(seen)-before, port.accepted)
			}
		})
	}
	raw := []byte(`{"text":"event"}`)
	rec := send("POST", path+"?tag=1", "push-key", raw)
	var response struct {
		Receipts []content.Receipt `json:"receipts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 202 || len(response.Receipts) != 1 || len(port.accepted) != 1 || response.Receipts[0].ID != "receipt_"+port.accepted[0] || response.Receipts[0].Source.CorpusID != "corpus_news" || response.Receipts[0].Source.Namespace != "events" || response.Receipts[0].Source.RecordKey != "event-7" {
		t.Fatalf("want 202 with ingested receipt, got %d %s accepted=%v", rec.Code, rec.Body.String(), port.accepted)
	}
	var receiptResponse map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &receiptResponse); err != nil {
		t.Fatal(err)
	}
	conforms(t, "ConnectorPushReceipts", receiptResponse)
	got := seen[0]
	if got.Route != "push" || string(got.Body) != string(raw) || got.Request.Path != "events/news" || got.Request.Query != "tag=1" || got.Request.BodyBase64 != base64.StdEncoding.EncodeToString(raw) || got.Request.Headers["authorization"] != nil || got.Request.Headers["cookie"] != nil {
		t.Fatalf("route/body/headers: %+v", got)
	}
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"rejected item", content.ErrInvalid, 422, "item_rejected"},
		{"ingestion unavailable", errors.New("storage unavailable"), 503, "ingestion_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port.failures = map[string]error{port.accepted[0]: tc.err}
			rec := send("POST", path, "push-key", raw)
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("engine error must be JSON: %v: %s", err, rec.Body.String())
			}
			if rec.Code != tc.status || body["code"] != tc.code || body["retryable"] != (tc.status == 503) || (tc.status == 503 && rec.Header().Get("Retry-After") != "30") || rec.Header().Get("Quivr-Response-Origin") != "" || strings.Contains(rec.Body.String(), "receipts") || len(port.accepted) != 1 {
				t.Fatalf("expected %d without success receipts, got %d %s", tc.status, rec.Code, rec.Body.String())
			}
		})
	}
	port.failures = nil
	*answer = plugins.ConnectorDelivery{Verdict: "accepted", Response: plugins.ReceiveAnswer{Status: 204}}
	rec = send("POST", path, "push-key", raw)
	if rec.Code != 202 || strings.TrimSpace(rec.Body.String()) != `{"receipts":[]}` {
		t.Fatalf("empty push: %d %s", rec.Code, rec.Body.String())
	}
	*answer = plugins.ConnectorDelivery{Verdict: "accepted", Response: plugins.ReceiveAnswer{Status: 200}, Items: []plugins.ConnectorItem{{RecordKey: "event-8", Content: json.RawMessage(`{"kind":"text","text":"invalid challenge item"}`)}}}
	rec = send("GET", "/v0/connectors/connector_push/api/challenge", "push-key", nil)
	var challengeError map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &challengeError); err != nil {
		t.Fatalf("challenge engine error must be JSON: %v: %s", err, rec.Body.String())
	}
	if rec.Code != 500 || challengeError["code"] != "challenge_has_items" || challengeError["retryable"] != false || rec.Header().Get("Quivr-Response-Origin") != "" || len(port.accepted) != 1 {
		t.Fatalf("challenge ingested items: %d %s", rec.Code, rec.Body.String())
	}
	*answer = plugins.ConnectorDelivery{Verdict: "accepted", Response: plugins.ReceiveAnswer{Status: 200, ContentType: "text/plain", Body: "challenge answer"}}
	rec = send("GET", "/v0/connectors/connector_push/api/challenge?challenge=abc", "push-key", nil)
	if rec.Code != 200 || rec.Body.String() != "challenge answer" || rec.Header().Get("Quivr-Response-Origin") != "plugin" || len(port.accepted) != 1 || seen[len(seen)-1].Route != "challenge" {
		t.Fatalf("GET challenge: %d %s calls=%v", rec.Code, rec.Body.String(), seen)
	}
	for _, response := range []struct {
		method, media, body string
		status              int
	}{
		{"POST", "text/plain", "provider rate limit", 429},
		{"POST", "text/plain", "provider method refused", 405},
		{"POST", "application/json", `{"provider":"refused"}`, 403},
		{"GET", "application/json", `{"challenge":"example"}`, 200},
	} {
		*answer = plugins.ConnectorDelivery{Verdict: "refused", Response: plugins.ReceiveAnswer{Status: response.status, ContentType: response.media, Body: response.body}}
		if response.method == "GET" {
			answer.Verdict = "accepted"
		}
		var rec *httptest.ResponseRecorder
		if response.method == "POST" {
			rec = send("POST", path, "push-key", raw)
		} else {
			rec = send("GET", "/v0/connectors/connector_push/api/challenge", "push-key", nil)
		}
		if rec.Code != response.status || rec.Body.String() != response.body || rec.Header().Get("Content-Type") != response.media || rec.Header().Get("Quivr-Response-Origin") != "plugin" || len(port.accepted) != 1 {
			t.Fatalf("plugin-defined reply changed: %d %s headers=%v", rec.Code, rec.Body.String(), rec.Header())
		}
	}
	store.fail = errors.New("storage unavailable")
	rec = send("POST", path, "push-key", raw)
	var storageError map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &storageError); err != nil || rec.Code != 503 || storageError["code"] != "storage_unavailable" || storageError["retryable"] != true || rec.Header().Get("Retry-After") != "30" || rec.Header().Get("Quivr-Response-Origin") != "" {
		t.Fatalf("storage engine error: %d %s headers=%v", rec.Code, rec.Body.String(), rec.Header())
	}
	store.fail = nil
	*answer = plugins.ConnectorDelivery{Verdict: "accepted", Response: plugins.ReceiveAnswer{Status: 204, Body: "invalid challenge body"}}
	rec = send("GET", "/v0/connectors/connector_push/api/challenge", "push-key", nil)
	var invalidChallenge map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &invalidChallenge); err != nil || rec.Code != 500 || invalidChallenge["code"] != "plugin_invalid_response" || rec.Header().Get("Quivr-Response-Origin") != "" {
		t.Fatalf("nonempty 204 challenge must be an engine error: %d %s", rec.Code, rec.Body.String())
	}
	answer.Response.Body = ""
	rec = send("GET", "/v0/connectors/connector_push/api/challenge", "push-key", nil)
	if rec.Code != 204 || rec.Body.Len() != 0 || rec.Header().Get("Quivr-Response-Origin") != "plugin" {
		t.Fatalf("bodyless challenge: %d %s headers=%v", rec.Code, rec.Body.String(), rec.Header())
	}
	before := len(seen)
	store.target.Enabled = false
	rec = send("POST", path, "push-key", raw)
	if rec.Code != 404 || len(seen) != before {
		t.Fatalf("disabled: %d calls=%d", rec.Code, len(seen))
	}
}
