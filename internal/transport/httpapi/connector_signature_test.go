package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

// Storage is replaced here; PostgreSQL owns atomic reservation and expiry.
type replayMemory struct {
	mu      sync.Mutex
	entries map[string]struct {
		token string
		until time.Time
	}
	fail bool
}

func (s *replayMemory) ReserveReplay(_ context.Context, org, id, token string, keys []string, now, until time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return false, errors.New("storage unavailable")
	}
	if s.entries == nil {
		s.entries = map[string]struct {
			token string
			until time.Time
		}{}
	}
	for _, k := range keys {
		if s.entries[org+"/"+id+"/"+k].until.After(now) {
			return false, nil
		}
	}
	for _, k := range keys {
		s.entries[org+"/"+id+"/"+k] = struct {
			token string
			until time.Time
		}{token, until}
	}
	return true, nil
}
func (s *replayMemory) ReleaseReplay(_ context.Context, org, id, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.entries {
		if v.token == token {
			delete(s.entries, k)
		}
	}
	return nil
}

// Signed ingress owns guard ordering, plugin refusal/retry, receipts, and the
// shared legacy alias. Existing X plugin tests own the provider HMAC itself.
func TestSignatureRoutesRefuseStaleAndReplayedPushesBeforeReceive(t *testing.T) {
	calls := 0
	verdict := "accepted"
	var pin *plugins.Pin
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/discovery" {
			_ = json.NewEncoder(w).Encode(map[string]any{"plugin_api": pin.PluginAPI(), "plugin": map[string]string{"id": pin.Manifest.ID, "version": pin.Manifest.Version}, "manifest_digest": pin.ManifestDigest, "contributions": pin.Manifest.Contributions.Names()})
			return
		}
		calls++
		var req struct {
			Route   string `json:"route"`
			Request struct {
				Method string `json:"method"`
			} `json:"request"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		d := plugins.ConnectorDelivery{Verdict: verdict, Response: plugins.ReceiveAnswer{Status: 401, Body: "invalid provider signature"}}
		if verdict == "accepted" {
			d.Response = plugins.ReceiveAnswer{Status: 204}
			d.Items = []plugins.ConnectorItem{{RecordKey: "signed-event", Revision: "1", Content: json.RawMessage(`{"kind":"text","text":"Signed event"}`)}}
		}
		if req.Request.Method == "GET" {
			d.Response = plugins.ReceiveAnswer{Status: 200, Body: "CRC answer"}
			d.Items = nil
		}
		if req.Route != "receive" && req.Route != "challenge" && req.Route != "timed" {
			t.Errorf("route lost: %q", req.Route)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d)
	}))
	defer source.Close()
	manifest := []byte(`id: example-signed
version: 1.0.0
compatibility: {engine: ">=0.1.0 <0.2.0", plugin_api: ">=0.12.0 <0.13.0"}
contributions:
  connector:
    kinds:
      signed:
        config_schema: {type: object}
        default_interval_seconds: 300
        modes: [pull, push]
        api:
          routes:
            - {name: receive, method: POST, path: receive, auth: signature, signature: {header: X-Signature, window_seconds: 300}}
            - {name: challenge, method: GET, path: receive, auth: signature, signature: {header: X-Signature, window_seconds: 300}}
            - {name: timed, method: POST, path: timed, auth: signature, signature: {header: X-Signature, timestamp_header: X-Timestamp, window_seconds: 300}, request_schema: {type: object}}
`)
	var err error
	pin, err = plugins.LoadPinManifest(manifest, "signed test plugin", plugins.PinConfig{Endpoint: source.URL})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := connectors.NewRegistry(pluginhttp.Connector{Pin: pin, Name: "signed"})
	if err != nil {
		t.Fatal(err)
	}
	store := &onePushInstance{target: connectors.Target{Instance: connectors.Instance{Organization: "org_a", ID: "signed_1", CorpusID: "corpus_news", Namespace: "events", Kind: "signed", Config: json.RawMessage(`{}`), Enabled: true}}}
	port := &acceptancePort{}
	contents := content.Service{Repository: port}
	cache := &replayMemory{}
	handler, err := httpapi.New(nil, contents, retrieval.Service{}, uploads.Service{}, map[string]corpus.Scope{}, catalogCursorKey, httpapi.WithRelay(connectors.Relay{Store: store, Registry: registry, Ingest: contents, Replays: cache}))
	if err != nil {
		t.Fatal(err)
	}
	path := "/v0/connectors/signed_1/api/"
	legacy := "/v0/connector-webhooks/signed_1"
	send := func(method, url string, headers map[string][]string, raw ...string) *httptest.ResponseRecorder {
		body := `{}`
		if len(raw) > 0 {
			body = raw[0]
		}
		req := httptest.NewRequest(method, url, bytes.NewBufferString(body))
		for k, v := range headers {
			req.Header[k] = v
		}
		rec := httptest.NewRecorder()
		checkedAPI(t, handler).ServeHTTP(rec, req)
		return rec
	}
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	for _, tc := range []struct {
		name    string
		headers map[string][]string
	}{
		{"missing signature", nil},
		{"ambiguous signature", map[string][]string{"X-Signature": {"a", "b"}, "X-Timestamp": {stamp}}},
		{"missing timestamp", map[string][]string{"X-Signature": {"a"}}},
		{"invalid timestamp", map[string][]string{"X-Signature": {"a"}, "X-Timestamp": {"tomorrow"}}},
		{"stale timestamp", map[string][]string{"X-Signature": {"a"}, "X-Timestamp": {"1"}}},
		{"future timestamp", map[string][]string{"X-Signature": {"a"}, "X-Timestamp": {"9999999999"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls
			rec := send("POST", path+"timed", tc.headers)
			if rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("guard error content type: %q", rec.Header().Get("Content-Type"))
			}
			var envelope map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			conforms(t, "Error", envelope)
			if rec.Code != 401 || calls != before || len(port.accepted) != 0 {
				t.Fatalf("want 401 before receive/ingest: %d %s calls=%d", rec.Code, rec.Body.String(), calls-before)
			}
		})
	}
	for _, raw := range []string{"{", "[]"} {
		before := calls
		rec := send("POST", path+"timed", nil, raw)
		if rec.Code != 401 || calls != before {
			t.Fatalf("signature must precede JSON/schema: %d %s calls=%d", rec.Code, rec.Body.String(), calls-before)
		}
	}
	h := map[string][]string{"X-Signature": {"valid-1"}, "X-Timestamp": {stamp}, "Idempotency-Key": {"push-1"}}
	rec := send("POST", path+"timed", h)
	if rec.Code != 202 || len(port.accepted) != 1 {
		t.Fatalf("valid timed push: %d %s", rec.Code, rec.Body.String())
	}
	before := calls
	for _, headers := range []map[string][]string{h, {"X-Signature": {"valid-1"}, "X-Timestamp": {stamp}}, {"X-Signature": {"valid-2"}, "X-Timestamp": {stamp}, "Idempotency-Key": {"push-1"}}} {
		rec = send("POST", path+"timed", headers)
		if rec.Code != 409 || calls != before || len(port.accepted) != 1 {
			t.Fatalf("replay: %d %s calls=%d", rec.Code, rec.Body.String(), calls-before)
		}
	}
	rec = send("POST", path+"timed", h, "{")
	if rec.Code != 409 || calls != before {
		t.Fatalf("replay must precede JSON: %d %s calls=%d", rec.Code, rec.Body.String(), calls-before)
	}
	// Invalid fresh bodies release their reservation, so a corrected retry works.
	fresh := map[string][]string{"X-Signature": {"schema-retry"}, "X-Timestamp": {stamp}}
	rec = send("POST", path+"timed", fresh, "[]")
	if rec.Code != 422 {
		t.Fatalf("signed schema mismatch: %d %s", rec.Code, rec.Body.String())
	}
	rec = send("POST", path+"timed", fresh)
	if rec.Code != 202 {
		t.Fatalf("corrected schema retry: %d %s", rec.Code, rec.Body.String())
	}
	// A forged call with a captured signature must not poison the valid retry.
	verdict = "refused"
	retry := map[string][]string{"X-Signature": {"retry-signature"}}
	rec = send("POST", path+"receive", retry)
	if rec.Code != 401 {
		t.Fatalf("provider refusal: %d", rec.Code)
	}
	verdict = "accepted"
	rec = send("POST", legacy, retry)
	if rec.Code != 202 {
		t.Fatalf("valid legacy retry: %d %s", rec.Code, rec.Body.String())
	}
	before = calls
	rec = send("POST", path+"receive", retry)
	if rec.Code != 409 || calls != before {
		t.Fatalf("alias replay: %d calls=%d", rec.Code, calls-before)
	}
	for _, url := range []string{path + "receive?crc_token=abc", legacy + "?crc_token=abc"} {
		rec = send("GET", url, nil)
		if rec.Code != 200 || rec.Body.String() != "CRC answer" {
			t.Fatalf("challenge: %d %s", rec.Code, rec.Body.String())
		}
	}
	// Failed ingestion must release the reservation for a valid retry and
	// report the engine failure using the public JSON Error contract.
	for _, url := range []string{path + "receive", legacy} {
		port.failures = map[string]error{port.accepted[0]: errors.New("storage unavailable")}
		retryIngest := map[string][]string{"X-Signature": {"retry-ingestion-" + url}}
		rec = send("POST", url, retryIngest)
		if rec.Code != 503 || rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Quivr-Response-Origin") != "" || rec.Header().Get("Retry-After") != "30" {
			t.Fatalf("failed ingestion: %d %s headers=%v", rec.Code, rec.Body.String(), rec.Header())
		}
		var envelope map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		conforms(t, "Error", envelope)
		if envelope["code"] != "ingestion_unavailable" || envelope["retryable"] != true {
			t.Fatalf("engine ingestion error: %v", envelope)
		}
		port.failures = nil
		rec = send("POST", url, retryIngest)
		if rec.Code != 202 {
			t.Fatalf("ingestion retry reserved the signature: %d %s", rec.Code, rec.Body.String())
		}
	}
	cache.fail = true
	before = calls
	rec = send("POST", path+"receive", map[string][]string{"X-Signature": {"new"}})
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("storage error content type: %s", rec.Header().Get("Content-Type"))
	}
	if rec.Code != 503 || calls != before {
		t.Fatalf("storage failure must fail closed: %d calls=%d", rec.Code, calls-before)
	}
}
