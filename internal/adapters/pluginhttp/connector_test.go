package pluginhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

const sourceManifest = `id: acme.source
version: 1.0.0
compatibility:
  engine: ">=0.1.0 <0.2.0"
  plugin_api: ">=0.3.0 <0.4.0"
contributions:
  connector:
    kinds:
      feed:
        description: Items of a feed.
        config_schema: {type: object}
        credential_schema:
          type: object
          required: [token]
          properties:
            token: {type: string}
        default_interval_seconds: 900
        modes: [pull]
    timeout_ms: 2000
extensions:
  acme.source:
    "1": {type: object}
`

const token = "fixture-test-token-connector"

// sourcePlugin serves discovery and answers each connector route with answer,
// recording the requests it received.
type sourcePlugin struct {
	mu       sync.Mutex
	digest   string
	requests map[string][]map[string]any
	answer   func(route string) (int, any)
}

func (p *sourcePlugin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.URL.Path == "/v0/discovery" {
		_ = json.NewEncoder(w).Encode(map[string]any{"plugin_api": "0.3.0", "plugin": map[string]any{"id": "acme.source", "version": "1.0.0"}, "manifest_digest": p.digest, "contributions": []string{"connector"}})
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req map[string]any
	_ = json.Unmarshal(body, &req)
	p.requests[r.URL.Path] = append(p.requests[r.URL.Path], req)
	status, out := p.answer(r.URL.Path)
	if status == http.StatusTemporaryRedirect {
		http.Redirect(w, r, "/elsewhere", status)
		return
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(out)
}

func sourceConnector(t *testing.T, answer func(route string) (int, any)) (pluginhttp.Connector, *sourcePlugin, *httptest.Server) {
	t.Helper()
	path := filepath.Join(t.TempDir(), plugins.ManifestFile)
	if err := os.WriteFile(path, []byte(sourceManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	plugin := &sourcePlugin{requests: map[string][]map[string]any{}, answer: answer}
	server := httptest.NewServer(plugin)
	t.Cleanup(server.Close)
	set, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: path, Endpoint: server.URL, Configuration: json.RawMessage(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	kinds := pluginhttp.Connectors(set)
	if len(kinds) != 1 || kinds[0].Kind() != "feed" {
		t.Fatalf("kinds %+v", kinds)
	}
	c := kinds[0].(pluginhttp.Connector)
	plugin.digest = c.Pin.ManifestDigest
	return c, plugin, server
}

func fetchRequest() connectors.FetchRequest {
	return connectors.FetchRequest{Organization: "org_a", InstanceID: "connector_1", Config: json.RawMessage(`{"url":"https://source.example"}`),
		Credential: json.RawMessage(`{"token":"` + token + `"}`), Checkpoint: json.RawMessage(`{"offset":2}`),
		Now: time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC), PageInRun: 0, ReadsToday: 7}
}

const fetchRoute = "/v0/contributions/connector/fetch"

// A plugin page becomes the Acquirer's page: the request carries the instance,
// its config, the credential, the checkpoint and the day's reads; the answer's
// items, checkpoint, reads, diagnostics and notice come back unchanged.
func TestAPluginPageBecomesAnAcquisitionPage(t *testing.T) {
	c, plugin, _ := sourceConnector(t, func(string) (int, any) {
		return 200, map[string]any{"checkpoint": map[string]any{"offset": 4}, "more": true, "reads": 2, "diagnostics": map[string]any{"quota": 9}, "notice": "quota_low",
			"items": []any{
				map[string]any{"record_key": "a", "revision": "1", "source_position": "p1", "content": map[string]any{"kind": "text", "text": "Alpha"}},
				map[string]any{"record_key": "b", "content": map[string]any{"kind": "manifest", "parts": []any{map[string]any{"key": "body", "role": "body", "content": map[string]any{"kind": "text", "text": "Beta"}}}},
					"extensions": map[string]any{"acme.source": map[string]any{"schema_version": "1", "data": map[string]any{}}}},
				map[string]any{"record_key": "c", "withdraw": true},
			}}
	})
	if c.DefaultInterval() != 15*time.Minute || !c.CredentialRequired() || c.ExtensionOwner() != "acme.source" || c.Description() != "Items of a feed." {
		t.Fatalf("kind description %v %v %q", c.DefaultInterval(), c.CredentialRequired(), c.ExtensionOwner())
	}
	page, err := c.Fetch(context.Background(), fetchRequest())
	if err != nil {
		t.Fatal(err)
	}
	req := plugin.requests[fetchRoute][0]
	ref := req["connector"].(map[string]any)
	if req["organization_id"] != "org_a" || ref["instance_id"] != "connector_1" || ref["kind"] != "feed" || ref["config"].(map[string]any)["url"] != "https://source.example" ||
		req["credential"].(map[string]any)["token"] != token || req["checkpoint"].(map[string]any)["offset"] != 2.0 || req["reads_today"] != 7.0 || req["page_in_run"] != 0.0 || req["now"] != "2026-09-29T08:00:00Z" {
		t.Fatalf("request %v", req)
	}
	if len(page.Items) != 3 || page.Items[0].Content.Text != "Alpha" || page.Items[0].Revision != "1" || page.Items[0].Position != "p1" ||
		page.Items[1].Manifest == nil || page.Items[1].Manifest.Parts[0].Content.Text != "Beta" || page.Items[1].Extensions["acme.source"].SchemaVersion != "1" || !page.Items[2].Withdraw {
		t.Fatalf("items %+v", page.Items)
	}
	if string(page.Checkpoint) != `{"offset":4}` || !page.More || page.Reads != 2 || string(page.Diagnostics) != `{"quota":9}` || page.Notice != "quota_low" {
		t.Fatalf("page %+v", page)
	}
}

// Every failure is a typed connector error, so Connector Health reports it
// like a built-in kind's: declared classes keep the plugin's code, anything
// the engine cannot trust is its own code, and not_due skips the run.
func TestPluginFailuresMapToConnectorHealth(t *testing.T) {
	envelope := func(code, class string, retryable bool, extra map[string]any) map[string]any {
		out := map[string]any{"code": code, "message": "details", "retryable": retryable}
		if class != "" {
			out["error_class"] = class
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	for name, tc := range map[string]struct {
		status int
		answer any
		class  connectors.ErrorClass
		code   string
		retry  time.Duration
	}{
		"access":           {403, envelope("token_rejected", "access", false, nil), connectors.ClassAccess, "token_rejected", 0},
		"transient":        {429, envelope("rate_limited", "transient", true, map[string]any{"retry_after_seconds": 90}), connectors.ClassTransient, "rate_limited", 90 * time.Second},
		"source":           {502, envelope("feed_malformed", "source", false, nil), connectors.ClassSource, "feed_malformed", 0},
		"no class":         {500, envelope("boom", "", false, nil), connectors.ClassSource, pluginhttp.CodePluginInvalidError, 0},
		"wrong class":      {403, envelope("token_rejected", "access", true, nil), connectors.ClassSource, pluginhttp.CodePluginInvalidError, 0},
		"no envelope":      {503, map[string]any{"oops": true}, connectors.ClassTransient, pluginhttp.CodePluginUnavailable, 0},
		"redirect":         {307, nil, connectors.ClassTransient, pluginhttp.CodePluginUnavailable, 0},
		"invalid output":   {200, map[string]any{"items": []any{map[string]any{"record_key": "a"}}, "checkpoint": nil, "more": false}, connectors.ClassSource, pluginhttp.CodePluginInvalidResponse, 0},
		"credential echo":  {200, map[string]any{"items": []any{}, "checkpoint": nil, "more": false, "diagnostics": map[string]any{"seen": token}}, connectors.ClassSource, pluginhttp.CodeCredentialLeak, 0},
		"credential error": {403, envelope("bad_"+"token", "access", false, map[string]any{"message": "token " + token + " refused"}), connectors.ClassSource, pluginhttp.CodeCredentialLeak, 0},
		"attachments": {200, map[string]any{"checkpoint": nil, "more": false, "items": []any{map[string]any{"record_key": "a",
			"content":     map[string]any{"kind": "manifest", "parts": []any{map[string]any{"key": "body", "role": "body", "content": map[string]any{"kind": "text", "text": "x"}}}},
			"attachments": []any{map[string]any{"key": "photo", "role": "attachment", "media_type": "image/jpeg", "ref": "r1"}}}}}, connectors.ClassSource, pluginhttp.CodeAttachmentsUnsupported, 0},
	} {
		t.Run(name, func(t *testing.T) {
			c, _, _ := sourceConnector(t, func(string) (int, any) { return tc.status, tc.answer })
			_, err := c.Fetch(context.Background(), fetchRequest())
			var typed *connectors.Error
			if !errors.As(err, &typed) || typed.Class != tc.class || typed.Code != tc.code || typed.RetryAfter != tc.retry {
				t.Fatalf("got %v (%+v), want %s %s retry %s", err, typed, tc.class, tc.code, tc.retry)
			}
		})
	}
	t.Run("not due", func(t *testing.T) {
		c, _, _ := sourceConnector(t, func(string) (int, any) {
			return 200, map[string]any{"items": []any{}, "checkpoint": map[string]any{"offset": 2}, "more": false, "not_due": true}
		})
		if _, err := c.Fetch(context.Background(), fetchRequest()); !errors.Is(err, connectors.ErrNotDue) {
			t.Fatalf("not_due: %v", err)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		c, _, server := sourceConnector(t, func(string) (int, any) { return 200, nil })
		server.Close()
		_, err := c.Fetch(context.Background(), fetchRequest())
		var typed *connectors.Error
		if !errors.As(err, &typed) || typed.Class != connectors.ClassTransient || typed.Code != pluginhttp.CodePluginUnavailable {
			t.Fatalf("unreachable plugin: %v", err)
		}
	})
}

// check_credential sends the credential for the kind and maps a refusal like
// a fetch.
func TestCheckCredentialAsksThePlugin(t *testing.T) {
	status := 200
	c, plugin, _ := sourceConnector(t, func(string) (int, any) {
		if status == 200 {
			return 200, map[string]any{"status": "ok"}
		}
		return status, map[string]any{"code": "token_rejected", "message": "refused", "retryable": false, "error_class": "access"}
	})
	r := fetchRequest()
	request := connectors.CredentialRequest{Organization: r.Organization, InstanceID: r.InstanceID, Config: r.Config, Credential: r.Credential, Now: r.Now}
	if err := c.CheckCredential(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	sent := plugin.requests["/v0/contributions/connector/check_credential"][0]
	if sent["credential"].(map[string]any)["token"] != token || sent["connector"].(map[string]any)["kind"] != "feed" {
		t.Fatalf("request %v", sent)
	}
	status = 403
	var typed *connectors.Error
	if err := c.CheckCredential(context.Background(), request); !errors.As(err, &typed) || typed.Class != connectors.ClassAccess || typed.Code != "token_rejected" {
		t.Fatalf("refusal %v", err)
	}
}
