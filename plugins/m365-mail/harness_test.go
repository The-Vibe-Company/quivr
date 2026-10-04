package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/tests/fakes/process"
)

// fakeGraph is a control/observation client of the canonical Graph binary.
type fakeGraph struct {
	t        *testing.T
	url      string
	clientID string
	mailbox  string
}
type failure struct {
	status     int
	code       string
	retryAfter string
}
type fakeAttachment struct {
	meta  map[string]any
	bytes string
}

func newFakeGraph(t *testing.T) *fakeGraph {
	g := &fakeGraph{t: t, url: process.Start(t, "graph").URL, clientID: "11111111-1111-1111-1111-111111111111", mailbox: "monitoring@example.org"}
	g.app(map[string]any{"secret": "test-secret-not-real"})
	g.control("mailbox", map[string]any{"page_size": 2})
	return g
}
func (g *fakeGraph) control(path string, body map[string]any) {
	g.t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	body["mailbox"] = g.mailbox
	b, err := json.Marshal(body)
	if err != nil {
		g.t.Fatal(err)
	}
	resp, err := http.Post(g.url+"/_fake/"+path, "application/json", bytes.NewReader(b))
	if err != nil {
		g.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		g.t.Fatalf("Graph control %s: HTTP%d", path, resp.StatusCode)
	}
}
func (g *fakeGraph) app(body map[string]any) { body["client_id"] = g.clientID; g.control("apps", body) }
func (g *fakeGraph) observations() struct {
	Tokens int        `json:"tokens_issued"`
	Form   url.Values `json:"token_form"`
} {
	g.t.Helper()
	var out struct {
		Tokens int        `json:"tokens_issued"`
		Form   url.Values `json:"token_form"`
	}
	q := url.Values{"mailbox": {g.mailbox}, "client_id": {g.clientID}}
	resp, err := http.Get(g.url + "/_fake/stats?" + q.Encode())
	if err != nil {
		g.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		g.t.Fatalf("Graph observations: HTTP%d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		g.t.Fatal(err)
	}
	return out
}
func (g *fakeGraph) connector() *Mail {
	c := New(&http.Client{Timeout: 5 * time.Second})
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	return c
}
func (g *fakeGraph) configuration() map[string]any {
	return map[string]any{"login_endpoint": g.url, "graph_endpoint": g.url + "/v1.0"}
}
func (g *fakeGraph) addMessage(id, received string, attachments ...fakeAttachment) {
	// Scenario input retains the frozen golden message headers and body.
	g.addRaw(map[string]any{"id": id, "internetMessageId": "<" + id + "@example.org>", "subject": "Subject " + id,
		"body":         map[string]any{"contentType": "html", "content": "<html><body><p>Hello <b>" + id + "</b></p><script>x()</script></body></html>"},
		"from":         map[string]any{"emailAddress": map[string]any{"name": "Desk", "address": "desk@example.org"}},
		"toRecipients": []any{map[string]any{"emailAddress": map[string]any{"name": "Monitor", "address": "monitoring@example.org"}}},
		"sentDateTime": received, "receivedDateTime": received, "conversationId": "conv-" + id, "hasAttachments": len(attachments) > 0}, attachments...)
}
func (g *fakeGraph) addRaw(msg map[string]any, attachments ...fakeAttachment) {
	files := []any{}
	for _, a := range attachments {
		files = append(files, map[string]any{"meta": a.meta, "data_b64": base64.StdEncoding.EncodeToString([]byte(a.bytes))})
	}
	g.control("messages", map[string]any{"message": msg, "attachments": files})
}
func fileAttachment(id, name, contentType, data string, size int) fakeAttachment {
	if size == 0 {
		size = len(data)
	}
	return fakeAttachment{meta: map[string]any{"@odata.type": "#microsoft.graph.fileAttachment", "id": id, "name": name, "contentType": contentType, "size": size, "isInline": false, "lastModifiedDateTime": "2026-09-28T10:00:00Z"}, bytes: data}
}
func (g *fakeGraph) failNext(fragment string, failures ...failure) {
	for _, f := range failures {
		body := map[string]any{"status": f.status, "code": f.code, "path": fragment}
		if f.retryAfter != "" {
			body["retry_after"] = f.retryAfter
		}
		g.control("fail", body)
	}
}
