package quivrplugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pusher is a push kind's connector whose Receive answer each test chooses.
type pusher struct {
	fake
	receive func(*ReceiveRequest) (*Delivery, error)
}

func (p pusher) Receive(_ context.Context, r *ReceiveRequest) (*Delivery, error) { return p.receive(r) }

func newPushPlugin(t *testing.T, fetch func(*FetchRequest) (*Page, error), receive func(*ReceiveRequest) (*Delivery, error)) *Plugin {
	t.Helper()
	p, err := New("testdata/push.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p.MustConnector("alerts", pusher{fake{fetch}, receive})
	return p
}

func receiveBody(method, query string, headers map[string][]string, body []byte) []byte {
	raw, _ := json.Marshal(map[string]any{
		"invocation_id": "inv-1", "contribution": "connector", "organization_id": "org-1", "configuration": map[string]any{},
		"connector":  map[string]any{"instance_id": "c-1", "kind": "alerts", "corpus_id": "corpus-1", "source_namespace": "alerts", "config": map[string]any{}},
		"credential": map[string]any{"token": secret}, "checkpoint": nil, "now": "2026-09-29T09:00:00Z", "reads_today": 3,
		"request": map[string]any{"method": method, "query": query, "headers": headers, "body_base64": base64.StdEncoding.EncodeToString(body)},
	})
	return raw
}

const receiveRoute = "/v0/contributions/connector/receive"

func TestReceiveHandsTheExactDeliveryAndReturnsTheVerdict(t *testing.T) {
	body := []byte("{\"alert\": \"fog\"}\n\x00binary")
	var seen *ReceiveRequest
	p := newPushPlugin(t, nil, func(r *ReceiveRequest) (*Delivery, error) {
		seen = r
		if r.Request.Header("X-Signature") != "sig-1" {
			return Refuse(401, "bad signature"), nil
		}
		d := Accept(Item{RecordKey: "alert-1", Revision: "1", Content: Text("Fog")})
		d.Reads = 1
		return d, nil
	})
	h, err := p.Handler()
	if err != nil {
		t.Fatal(err)
	}
	status, out, raw := call(h, receiveRoute, receiveBody("POST", "a=1", map[string][]string{"x-signature": {"sig-1"}}, body))
	if status != 200 || out["verdict"] != "accepted" || strings.Contains(raw, secret) {
		t.Fatalf("status %d: %s", status, raw)
	}
	if err := validate("plugins/v0/connector-receive-response.schema.json", []byte(raw)); err != nil {
		t.Fatal(err)
	}
	if string(seen.Request.Body()) != string(body) || seen.Request.QueryValues().Get("a") != "1" || seen.ReadsToday != 3 || seen.Connector.CorpusID != "corpus-1" {
		t.Fatalf("the plugin saw %+v body %q", seen.Request, seen.Request.Body())
	}
	status, out, raw = call(h, receiveRoute, receiveBody("POST", "", map[string][]string{"x-signature": {"forged"}}, body))
	if status != 200 || out["verdict"] != "refused" || out["response"].(map[string]any)["status"] != 401.0 || out["items"] != nil {
		t.Fatalf("status %d: %s", status, raw)
	}
}

func TestReceiveRefusesAnIncoherentDeliveryBeforeTheEngineDoes(t *testing.T) {
	item := Item{RecordKey: "alert-1", Content: Text("Fog")}
	for name, d := range map[string]*Delivery{
		"accepted with a 4xx": {Verdict: VerdictAccepted, Status: 400},
		"refused with items":  {Verdict: VerdictRefused, Status: 401, Items: []Item{item}},
		"refused with a 2xx":  {Verdict: VerdictRefused, Status: 204},
		"too many items":      Accept(item, item, item),
		"attachments":         Accept(Item{RecordKey: "a", Content: NewManifest(TextPart("b", "body", "x")), Attachments: []Attachment{{Key: "f", Role: "attachment", MediaType: "text/plain", Ref: "r"}}}),
		"no delivery":         nil,
	} {
		t.Run(name, func(t *testing.T) {
			p := newPushPlugin(t, nil, func(*ReceiveRequest) (*Delivery, error) { return d, nil })
			h, _ := p.Handler()
			status, out, raw := call(h, receiveRoute, receiveBody("GET", "", map[string][]string{}, nil))
			if status != 500 || out["code"] != "invalid_response" {
				t.Fatalf("status %d: %s", status, raw)
			}
		})
	}
}

func TestPushKindsNeedReceiverAndPluginAPI05(t *testing.T) {
	p, err := New("testdata/push.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p.MustConnector("alerts", fake{})
	if _, err := p.Handler(); err == nil || !strings.Contains(err.Error(), "Receiver") {
		t.Fatalf("a push kind without Receiver was served: %v", err)
	}
	raw, _ := os.ReadFile("testdata/push.yaml")
	old := filepath.Join(t.TempDir(), "quivr-plugin.yaml")
	_ = os.WriteFile(old, []byte(strings.Replace(string(raw), `">=0.5.0 <0.6.0"`, `">=0.4.0 <0.5.0"`, 1)), 0o600)
	if _, err := New(old); err == nil || !strings.Contains(err.Error(), "0.5.0") {
		t.Fatalf("push under Plugin API 0.4: %v", err)
	}
	// A pull-only plugin serves no receive route.
	h, _ := newTestPlugin(t, nil)
	if status, _, _ := call(h, receiveRoute, receiveBody("GET", "", nil, nil)); status != 404 && status != 405 {
		t.Fatalf("a pull-only plugin answered receive %d", status)
	}
}

func TestFetchReportsACoherentPushStatus(t *testing.T) {
	for name, c := range map[string]struct {
		push *PushStatus
		ok   bool
	}{
		"active, relaxed pull": {PushIsActive(15 * 60e9), true},
		"failed access":        {PushHasFailed(ClassAccess, "webhook_invalid"), true},
		"pending with reason":  {PushIsPending("public_url_missing"), true},
		"failed without class": {&PushStatus{State: PushFailed, Code: "x"}, false},
		"pending, relaxed":     {&PushStatus{State: PushPending, PollIntervalSeconds: 900}, false},
		"interval too short":   {PushIsActive(10e9), false},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPushPlugin(t, func(*FetchRequest) (*Page, error) {
				return &Page{Checkpoint: map[string]any{}, Push: c.push}, nil
			}, nil)
			h, _ := p.Handler()
			status, out, raw := call(h, "/v0/contributions/connector/fetch", fetchBody(func(m map[string]any) {
				m["connector"] = map[string]any{"instance_id": "c-1", "kind": "alerts", "config": map[string]any{}}
			}))
			if c.ok != (status == 200) {
				t.Fatalf("status %d: %s", status, raw)
			}
			if c.ok && out["push"].(map[string]any)["state"] != c.push.State {
				t.Fatalf("push status not sent: %s", raw)
			}
		})
	}
	// A pull-only plugin reports no push status.
	h, _ := newTestPlugin(t, func(*FetchRequest) (*Page, error) {
		return &Page{Checkpoint: map[string]any{}, Push: PushIsActive(0)}, nil
	})
	if status, _, raw := call(h, "/v0/contributions/connector/fetch", fetchBody(nil)); status != 500 {
		t.Fatalf("status %d: %s", status, raw)
	}
}
