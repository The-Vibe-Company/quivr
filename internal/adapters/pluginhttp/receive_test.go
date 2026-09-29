package pluginhttp_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
)

// pushManifest is sourceManifest at Plugin API 0.5 with a kind that pushes.
var pushManifest = strings.Replace(strings.Replace(sourceManifest, `plugin_api: ">=0.3.0 <0.4.0"`, `plugin_api: ">=0.5.0 <0.6.0"`, 1),
	"modes: [pull]", "modes: [pull, push]", 1)

func pushConnector(t *testing.T, answer func(route string) (int, any)) (pluginhttp.Connector, *sourcePlugin) {
	t.Helper()
	c, plugin, _ := pinnedConnector(t, pushManifest, answer)
	plugin.served = "0.5.0"
	return c, plugin
}

func receiveRequest() connectors.ReceiveRequest {
	return connectors.ReceiveRequest{Organization: "org_a", InstanceID: "connector_1", CorpusID: "corpus_1", Namespace: "feeds", Config: json.RawMessage(`{}`),
		Credential: json.RawMessage(`{"token":"` + token + `"}`), Checkpoint: json.RawMessage(`{"offset":2}`), Now: time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC), ReadsToday: 7,
		Request: connectors.Relayed{Method: "POST", Query: "a=1", Headers: map[string][]string{"x-signature": {"sig-1"}}, Body: []byte("raw\x00body")}}
}

func TestReceiveRelaysTheRawRequestAndJudgesTheVerdict(t *testing.T) {
	item := map[string]any{"record_key": "item-9", "revision": "9", "content": map[string]any{"kind": "text", "text": "Pushed"}}
	for name, c := range map[string]struct {
		status   int
		answer   any
		accepted bool
		keys     []string
		class    connectors.ErrorClass
		code     string
	}{
		"accepted with items": {status: 200, answer: map[string]any{"verdict": "accepted", "response": map[string]any{"status": 204}, "items": []any{item}, "reads": 1}, accepted: true, keys: []string{"item-9"}},
		"refused":             {status: 200, answer: map[string]any{"verdict": "refused", "response": map[string]any{"status": 401, "body": "bad signature"}}},
		"refused with items":  {status: 200, answer: map[string]any{"verdict": "refused", "response": map[string]any{"status": 401}, "items": []any{item}}, class: connectors.ClassSource, code: pluginhttp.CodePluginInvalidResponse},
		"a checkpoint":        {status: 200, answer: map[string]any{"verdict": "accepted", "response": map[string]any{"status": 200}, "checkpoint": map[string]any{}}, class: connectors.ClassSource, code: pluginhttp.CodePluginInvalidResponse},
		"access error":        {status: 403, answer: map[string]any{"code": "consumer_secret_missing", "message": "no consumer secret", "retryable": false, "error_class": "access"}, class: connectors.ClassAccess, code: "consumer_secret_missing"},
		"credential echoed":   {status: 200, answer: map[string]any{"verdict": "accepted", "response": map[string]any{"status": 200, "body": token}}, class: connectors.ClassSource, code: pluginhttp.CodeCredentialLeak},
		"plugin down":         {status: 502, answer: "gateway", class: connectors.ClassTransient, code: pluginhttp.CodePluginUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			conn, plugin := pushConnector(t, func(string) (int, any) { return c.status, c.answer })
			d, err := conn.Receive(context.Background(), receiveRequest())
			var typed *connectors.Error
			if c.code != "" {
				if !errors.As(err, &typed) || typed.Class != c.class || typed.Code != c.code {
					t.Fatalf("err %v, want %s %s", err, c.class, c.code)
				}
				return
			}
			if err != nil || d.Accepted != c.accepted || len(d.Items) != len(c.keys) {
				t.Fatalf("delivery %+v err %v", d, err)
			}
			for i, key := range c.keys {
				if d.Items[i].RecordKey != key {
					t.Fatalf("items %+v", d.Items)
				}
			}
			sent := plugin.requests["/v0/contributions/connector/receive"][0]
			relayed := sent["request"].(map[string]any)
			body, _ := base64.StdEncoding.DecodeString(relayed["body_base64"].(string))
			connector := sent["connector"].(map[string]any)
			if string(body) != "raw\x00body" || relayed["query"] != "a=1" || relayed["headers"].(map[string]any)["x-signature"].([]any)[0] != "sig-1" ||
				connector["corpus_id"] != "corpus_1" || sent["checkpoint"].(map[string]any)["offset"] != 2.0 || sent["reads_today"] != 7.0 {
				t.Fatalf("receive request %v", sent)
			}
		})
	}
}

func TestFetchSendsTheWebhookURLAndMapsThePushReportOfAPushKindOnly(t *testing.T) {
	report := map[string]any{"state": "active", "poll_interval_seconds": 900}
	page := map[string]any{"items": []any{}, "checkpoint": map[string]any{"offset": 2}, "more": false, "push": report}
	push, plugin := pushConnector(t, func(string) (int, any) { return 200, page })
	r := fetchRequest()
	r.WebhookURL = "https://quivr.example.com/v0/connector-webhooks/connector_1"
	got, err := push.Fetch(context.Background(), r)
	if err != nil || got.Push == nil || got.Push.State != connectors.PushActive || got.Push.PollInterval != 15*time.Minute {
		t.Fatalf("page %+v err %v", got, err)
	}
	if sent := plugin.requests["/v0/contributions/connector/fetch"][0]["connector"].(map[string]any); sent["webhook_url"] != r.WebhookURL {
		t.Fatalf("connector %v", sent)
	}
	delete(page, "push")
	pull, plugin, _ := sourceConnector(t, func(string) (int, any) { return 200, page })
	if _, err := pull.Fetch(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if _, sent := plugin.requests["/v0/contributions/connector/fetch"][0]["connector"].(map[string]any)["webhook_url"]; sent || pull.Pushes() || !push.Pushes() {
		t.Fatal("a pull-only kind received a webhook_url")
	}
}
