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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

const sourceManifest = `id: acme.source
version: 1.0.0
compatibility:
  engine: ">=0.1.0 <0.3.0"
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
	served   string // Plugin API version discovery serves; default 0.3.0
	requests map[string][]map[string]any
	answer   func(route string) (int, any)
}

func (p *sourcePlugin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.URL.Path == "/v0/discovery" {
		served := p.served
		if served == "" {
			served = "0.3.0"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"plugin_api": served, "plugin": map[string]any{"id": "acme.source", "version": "1.0.0"}, "manifest_digest": p.digest, "contributions": []string{"connector"}})
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

// attachmentManifest is sourceManifest at Plugin API 0.4 with attachments.
var attachmentManifest = strings.Replace(strings.Replace(sourceManifest, `plugin_api: ">=0.3.0 <0.4.0"`, `plugin_api: ">=0.4.0 <0.5.0"`, 1),
	"    timeout_ms: 2000\n", "    timeout_ms: 2000\n    attachments: {max_bytes: 1024, timeout_ms: 1000}\n", 1)

func sourceConnector(t *testing.T, answer func(route string) (int, any)) (pluginhttp.Connector, *sourcePlugin, *httptest.Server) {
	return pinnedConnector(t, sourceManifest, answer)
}

func pinnedConnector(t *testing.T, manifest string, answer func(route string) (int, any)) (pluginhttp.Connector, *sourcePlugin, *httptest.Server) {
	t.Helper()
	path := filepath.Join(t.TempDir(), plugins.ManifestFile)
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	plugin := &sourcePlugin{requests: map[string][]map[string]any{}, answer: answer}
	if strings.Contains(manifest, ">=0.4.0") {
		plugin.served = "0.4.0"
	}
	server := httptest.NewServer(plugin)
	t.Cleanup(server.Close)
	set, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: path, Endpoint: server.URL, Configuration: json.RawMessage(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	kinds := pluginhttp.Connectors(set)
	if len(kinds) != 1 || kinds[0].Descriptor().Kind != "feed" {
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
	if c.Descriptor().DefaultInterval != 15*time.Minute || !c.Descriptor().CredentialRequired || c.Descriptor().ExtensionOwner != "acme.source" || c.Descriptor().Description != "Items of a feed." {
		t.Fatalf("kind description %v %v %q", c.Descriptor().DefaultInterval, c.Descriptor().CredentialRequired, c.Descriptor().ExtensionOwner)
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

// credential_required: false makes a declared credential optional: the kind
// is described as optional, so an instance without one runs.
func TestAnOptionalCredentialKindDoesNotRequireOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), plugins.ManifestFile)
	manifest := strings.Replace(sourceManifest, "        modes: [pull]", "        credential_required: false\n        modes: [pull]", 1)
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: path, Endpoint: "http://127.0.0.1:9", Configuration: json.RawMessage(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	c := pluginhttp.Connectors(set)[0].(pluginhttp.Connector)
	if c.Descriptor().CredentialRequired || c.Descriptor().CredentialSchema == nil {
		t.Fatalf("required %v schema %s", c.Descriptor().CredentialRequired, c.Descriptor().CredentialSchema)
	}
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

// The instance scope (corpus_id, source_namespace) reaches only a plugin whose
// discovery serves Plugin API 0.3.1 or later, on every page of a run: a plugin
// built for 0.3.0 may refuse unknown request fields.
func TestTheInstanceScopeReachesOnlyPluginsThatServeIt(t *testing.T) {
	for _, tc := range []struct {
		served string
		scoped bool
	}{{"0.3.0", false}, {"0.3.1", true}} {
		t.Run(tc.served, func(t *testing.T) {
			c, plugin, _ := sourceConnector(t, func(string) (int, any) {
				return 200, map[string]any{"checkpoint": map[string]any{"offset": 2}, "more": false, "items": []any{}}
			})
			plugin.served = tc.served
			for page := range 2 {
				r := fetchRequest()
				r.CorpusID, r.Namespace, r.PageInRun = "corpus_a", "feeds.example", page
				if _, err := c.Fetch(context.Background(), r); err != nil {
					t.Fatal(err)
				}
			}
			for i, req := range plugin.requests[fetchRoute] {
				ref := req["connector"].(map[string]any)
				_, hasCorpus := ref["corpus_id"]
				_, hasNamespace := ref["source_namespace"]
				if hasCorpus != tc.scoped || hasNamespace != tc.scoped || tc.scoped && (ref["corpus_id"] != "corpus_a" || ref["source_namespace"] != "feeds.example") {
					t.Fatalf("Plugin API %s, page %d: connector %v, want scope %v", tc.served, i, ref, tc.scoped)
				}
			}
		})
	}
}

const digestHex = "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"

func attachmentRequest() connectors.AttachmentRequest {
	r := fetchRequest()
	size := int64(11)
	return connectors.AttachmentRequest{Organization: r.Organization, InstanceID: r.InstanceID, Config: r.Config, Credential: r.Credential, Now: r.Now,
		RecordKey: "a", Revision: "rev-1", Extensions: content.Extensions{"acme.source": {SchemaVersion: "1", Data: map[string]any{}}},
		Attachment: connectors.Attachment{Key: "photo", Role: "attachment", MediaType: "image/jpeg", Ref: "media/1", SizeBytes: &size}}
}

func grant() connectors.UploadGrant {
	return connectors.UploadGrant{URL: "https://storage.invalid/object?X-Signature=grant-signature-0000", Headers: map[string]string{"X-Amz-Checksum-Sha256": "checksum-header-value"},
		SizeBytes: 11, SHA256: digestHex, MediaType: "image/jpeg", ExpiresAt: time.Date(2026, 9, 29, 8, 15, 0, 0, time.UTC)}
}

// Attachment pages map their descriptors, and the two attachment operations
// carry the item, the descriptor and the grant; their answers are judged like
// a fetch's.
func TestAttachmentsAreExchangedThroughDescribeAndUpload(t *testing.T) {
	answers := map[string]any{
		fetchRoute: map[string]any{"checkpoint": nil, "more": false, "items": []any{map[string]any{"record_key": "a", "revision": "rev-1",
			"content":     map[string]any{"kind": "manifest", "parts": []any{map[string]any{"key": "body", "role": "body", "content": map[string]any{"kind": "text", "text": "x"}}}},
			"attachments": []any{map[string]any{"key": "photo", "role": "attachment", "media_type": "image/jpeg", "size_bytes": 11, "sha256": digestHex, "ref": "media/1"}}}}},
		"/v0/contributions/connector/describe_attachment": map[string]any{"size_bytes": 11, "sha256": digestHex},
		"/v0/contributions/connector/upload_attachment":   map[string]any{"status": "uploaded"},
	}
	c, plugin, _ := pinnedConnector(t, attachmentManifest, func(route string) (int, any) { return 200, answers[route] })
	if c.Descriptor().MaxAttachmentBytes != 1024 {
		t.Fatalf("max bytes %d", c.Descriptor().MaxAttachmentBytes)
	}
	page, err := c.Fetch(context.Background(), fetchRequest())
	if err != nil {
		t.Fatal(err)
	}
	if at := page.Items[0].Attachments; len(at) != 1 || at[0].Ref != "media/1" || at[0].SHA256 != digestHex || *at[0].SizeBytes != 11 {
		t.Fatalf("attachments %+v", at)
	}
	d, err := c.DescribeAttachment(context.Background(), attachmentRequest())
	if err != nil || d.SizeBytes != 11 || d.SHA256 != digestHex {
		t.Fatalf("describe %+v %v", d, err)
	}
	sent := plugin.requests["/v0/contributions/connector/describe_attachment"][0]
	if sent["item"].(map[string]any)["record_key"] != "a" || sent["attachment"].(map[string]any)["ref"] != "media/1" || sent["credential"].(map[string]any)["token"] != token || sent["grant"] != nil {
		t.Fatalf("describe request %v", sent)
	}
	if err := c.UploadAttachment(context.Background(), attachmentRequest(), grant()); err != nil {
		t.Fatal(err)
	}
	g := plugin.requests["/v0/contributions/connector/upload_attachment"][0]["grant"].(map[string]any)
	if g["method"] != "PUT" || g["url"] != grant().URL || g["sha256"] != digestHex || g["media_type"] != "image/jpeg" || g["expires_at"] != "2026-09-29T08:15:00Z" {
		t.Fatalf("grant %v", g)
	}
}

func TestAttachmentAnswersAreJudgedBeforeUse(t *testing.T) {
	for name, tc := range map[string]struct {
		route  string
		answer any
		code   string
	}{
		"size above max_bytes": {"describe_attachment", map[string]any{"size_bytes": 4096, "sha256": digestHex}, pluginhttp.CodePluginInvalidResponse},
		"undeclared extension": {"describe_attachment", map[string]any{"skip": "too_large", "item_extensions": map[string]any{"other": map[string]any{"schema_version": "1", "data": map[string]any{}}}}, pluginhttp.CodePluginInvalidResponse},
		"credential echo":      {"describe_attachment", map[string]any{"skip": token}, pluginhttp.CodeCredentialLeak},
		"grant URL echo":       {"upload_attachment", map[string]any{"status": "uploaded", "url": grant().URL}, pluginhttp.CodeCredentialLeak},
		"grant header echo":    {"upload_attachment", map[string]any{"status": "checksum-header-value"}, pluginhttp.CodeCredentialLeak},
		"unknown status":       {"upload_attachment", map[string]any{"status": "stored"}, pluginhttp.CodePluginInvalidResponse},
	} {
		t.Run(name, func(t *testing.T) {
			c, _, _ := pinnedConnector(t, attachmentManifest, func(string) (int, any) { return 200, tc.answer })
			var err error
			if tc.route == "describe_attachment" {
				_, err = c.DescribeAttachment(context.Background(), attachmentRequest())
			} else {
				err = c.UploadAttachment(context.Background(), attachmentRequest(), grant())
			}
			var typed *connectors.Error
			if !errors.As(err, &typed) || typed.Class != connectors.ClassSource || typed.Code != tc.code {
				t.Fatalf("got %v, want source %s", err, tc.code)
			}
		})
	}
	t.Run("declared error", func(t *testing.T) {
		c, _, _ := pinnedConnector(t, attachmentManifest, func(string) (int, any) {
			return 422, map[string]any{"code": connectors.CodeAttachmentChanged, "message": "changed", "retryable": false, "error_class": "source"}
		})
		var typed *connectors.Error
		if err := c.UploadAttachment(context.Background(), attachmentRequest(), grant()); !errors.As(err, &typed) || typed.Code != connectors.CodeAttachmentChanged {
			t.Fatalf("got %v", err)
		}
	})
}
