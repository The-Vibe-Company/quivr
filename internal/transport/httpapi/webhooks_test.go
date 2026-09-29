package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

// echoPush is a push kind that answers every relayed request with what it
// received, so the test sees exactly what the route relays.
type echoPush struct {
	connectors.Fixture
	seen *[]connectors.ReceiveRequest
}

func (echoPush) Kind() string { return "echo" }
func (echoPush) Pushes() bool { return true }
func (e echoPush) Receive(_ context.Context, r connectors.ReceiveRequest) (connectors.Delivery, error) {
	*e.seen = append(*e.seen, r)
	return connectors.Delivery{Accepted: true, Status: 202, ContentType: "application/json", Body: `{"ok":true}`}, nil
}

type onePushInstance struct{ target connectors.Target }

func (s onePushInstance) LoadDelivery(_ context.Context, id string) (connectors.Target, error) {
	if id != s.target.ID {
		return connectors.Target{}, corpus.ErrNotFound
	}
	return s.target, nil
}
func (onePushInstance) RecordDelivery(context.Context, string, string, connectors.DeliveryOutcome) error {
	return nil
}

func TestWebhookRouteRelaysABoundedRequestWithoutAnAPIKey(t *testing.T) {
	var seen []connectors.ReceiveRequest
	registry, err := connectors.NewRegistry(echoPush{seen: &seen})
	if err != nil {
		t.Fatal(err)
	}
	store := onePushInstance{connectors.Target{Instance: connectors.Instance{Organization: "org_a", ID: "connector_push", CorpusID: "corpus_news", Namespace: "echo", Kind: "echo", Config: json.RawMessage(`{}`), Enabled: true}}}
	handler, err := httpapi.New(nil, content.Service{}, retrieval.Service{}, uploads.Service{}, map[string]corpus.Scope{}, []byte("cursor-key-0123456789abcdef0123456789"),
		httpapi.WithRelay(connectors.Relay{Store: store, Registry: registry, Now: func() time.Time { return time.Unix(0, 0) }}))
	if err != nil {
		t.Fatal(err)
	}
	send := func(method, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	rec := send("POST", "/v0/connector-webhooks/connector_push?crc_token=abc", []byte("raw body"), map[string]string{"X-Signature": "sig-1", "Cookie": "session=1", "Connection": "close"})
	if rec.Code != 202 || rec.Body.String() != `{"ok":true}` || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	got := seen[0].Request
	if got.Method != "POST" || got.Query != "crc_token=abc" || string(got.Body) != "raw body" || got.Headers["x-signature"][0] != "sig-1" {
		t.Fatalf("relayed %+v", got)
	}
	if _, ok := got.Headers["cookie"]; ok || got.Headers["connection"] != nil {
		t.Fatalf("a cookie or hop-by-hop header was relayed: %v", got.Headers)
	}
	for name, c := range map[string]struct {
		method, path string
		body         []byte
		status       int
	}{
		"unknown instance":     {"POST", "/v0/connector-webhooks/connector_other", nil, 404},
		"nested path":          {"POST", "/v0/connector-webhooks/connector_push/x", nil, 404},
		"other method":         {"PUT", "/v0/connector-webhooks/connector_push", nil, 405},
		"body over 1 MiB":      {"POST", "/v0/connector-webhooks/connector_push", bytes.Repeat([]byte("a"), 1<<20+1), 413},
		"api routes keep auth": {"GET", "/v0/connectors/connector_push", nil, 401},
	} {
		t.Run(name, func(t *testing.T) {
			before := len(seen)
			if rec := send(c.method, c.path, c.body, nil); rec.Code != c.status || len(seen) != before {
				t.Fatalf("status %d %s, plugin called %d times", rec.Code, strings.TrimSpace(rec.Body.String()), len(seen)-before)
			}
		})
	}
}
