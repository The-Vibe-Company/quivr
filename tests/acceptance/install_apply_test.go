package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/online"
)

// Owns reconciliation against the real engine; no fake implements creation,
// replay, schedule changes, credential versions or the resulting change events.
func TestInstallApply(t *testing.T) {
	token := connectorToken(t)
	target, err := url.Parse(os.Getenv("QUIVR_TEST_URL"))
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	var loseCreationReply, rotateAfterSnapshot atomic.Bool
	var connectorID string
	proxy.ModifyResponse = func(response *http.Response) error {
		if rotateAfterSnapshot.Load() && response.Request.Method == "GET" && response.Request.URL.Path == "/v0/connectors/"+connectorID && rotateAfterSnapshot.CompareAndSwap(true, false) {
			// Another real public API actor writes after this snapshot was taken.
			request(t, "PUT", "/v0/connectors/"+connectorID+"/credential", token,
				map[string]any{"idempotency_key": "external-" + connectorRun,
					"secret": map[string]any{"token": "fixture-external-not-real"}}, 200)
		}
		if response.Request.Method == "POST" && response.Request.URL.Path == "/v0/connectors" && response.StatusCode == 201 && loseCreationReply.CompareAndSwap(true, false) {
			// Only corrupt the successful transport reply; the real engine committed the credentialed connector.
			response.Body.Close()
			body := "lost successful connector creation response"
			response.Body = io.NopCloser(strings.NewReader(body))
			response.ContentLength = int64(len(body))
			response.Header.Set("Content-Length", strconv.Itoa(len(body)))
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(502) }
	server := httptest.NewServer(proxy)
	defer server.Close()
	dir := t.TempDir()
	file := filepath.Join(dir, "sources.json")
	journal := filepath.Join(dir, "state.json")
	key := "install-" + connectorRun
	declaration := map[string]any{"version": 1, "corpora": []any{map[string]any{"idempotency_key": key, "name": "Install articles"}}, "connectors": []any{map[string]any{
		"idempotency_key": key + "-feed", "corpus": key, "source_namespace": "install-feed", "kind": "fixture", "config": map[string]any{"requires_credential": true, "script": []any{}},
		"credential_env": map[string]string{"token": "INSTALL_SOURCE_TOKEN"}, "schedule": map[string]any{"interval_seconds": 86400},
	}}}
	write := func() {
		b, _ := json.Marshal(declaration)
		if err := os.WriteFile(file, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	sourceSecret := connectorTestSecret
	invoke := func(confirm bool) (int, string) {
		t.Helper()
		var out, errw bytes.Buffer
		args := []string{"-f", file, "--state-file", journal}
		if confirm {
			args = append(args, "--confirm")
		}
		code := online.RunContext(context.Background(), "apply", args, online.Env{Getenv: func(k string) string {
			switch k {
			case online.EnvAPIURL:
				return server.URL
			case online.EnvAPIKey:
				return token
			case "QUIVR_OPERATOR_KEY":
				return os.Getenv("QUIVR_TEST_OPERATOR")
			case "INSTALL_SOURCE_TOKEN":
				return sourceSecret
			}
			return ""
		}, Stdout: &out, Stderr: &errw})
		if strings.Contains(out.String()+errw.String(), sourceSecret) {
			t.Fatal("credential leaked")
		}
		if code != 0 {
			t.Logf("apply: %s", errw.String())
		}
		return code, out.String()
	}
	write()
	code, out := invoke(false)
	if code != 0 || !strings.Contains(out, "+ corpus") || !strings.Contains(out, "+ connector") {
		t.Fatalf("preview: %d %s", code, out)
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatal("preview persisted replay state")
	}
	loseCreationReply.Store(true)
	code, out = invoke(true)
	if code == 0 {
		t.Fatal("lost successful creation reply was not detected")
	}
	code, out = invoke(true)
	if code != 0 {
		t.Fatalf("creation replay: %d %s", code, out)
	}
	page := request(t, "GET", "/v0/corpora", token, nil, 200)
	var corpusID string
	for _, v := range page["items"].([]any) {
		c := v.(map[string]any)
		if c["name"] == "Install articles" {
			corpusID = c["corpus_id"].(string)
		}
	}
	if corpusID == "" {
		t.Fatal("applied corpus missing")
	}
	connectorPage := request(t, "GET", "/v0/connectors?corpus_id="+corpusID, token, nil, 200)
	items := connectorPage["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("want one connector, got %d", len(items))
	}
	connectorID = items[0].(map[string]any)["connector_id"].(string)
	cursor := request(t, "GET", changesPath(corpusID, "", 0), token, nil, 200)["next_cursor"].(string)
	code, out = invoke(true)
	if code != 0 || !strings.Contains(out, "No changes.") {
		t.Fatalf("reapply: %d %s", code, out)
	}
	changes := request(t, "GET", changesPath(corpusID, cursor, 0), token, nil, 200)
	for _, v := range changes["items"].([]any) {
		kind := v.(map[string]any)["type"].(string)
		if kind == "connector.created" || kind == "connector.schedule_changed" || kind == "connector.credential_replaced" {
			t.Fatalf("unchanged apply changed %s", kind)
		}
	}
	declaration["corpora"].([]any)[0].(map[string]any)["name"] = "Renamed install articles"
	declaration["connectors"].([]any)[0].(map[string]any)["schedule"] = map[string]any{"interval_seconds": 43200}
	write()
	code, out = invoke(false)
	if code != 0 || !strings.Contains(out, "name:") || !strings.Contains(out, "interval_seconds: 86400 -> 43200") {
		t.Fatalf("changed preview: %d %s", code, out)
	}
	current := request(t, "GET", "/v0/connectors/"+connectorID, token, nil, 200)
	if current["schedule"].(map[string]any)["interval_seconds"] != float64(86400) {
		t.Fatal("preview changed schedule")
	}
	sourceSecret = "fixture-rotation-not-real"
	code, out = invoke(true)
	if code == 0 || !strings.Contains(out, "credential change requires a conditional version API") {
		t.Fatalf("credential change must be refused: %d %s", code, out)
	}
	current = request(t, "GET", "/v0/connectors/"+connectorID, token, nil, 200)
	if current["credential"].(map[string]any)["version"] != float64(1) || current["schedule"].(map[string]any)["interval_seconds"] != float64(86400) {
		t.Fatal("unsupported credential change modified the deployment")
	}
	sourceSecret = connectorTestSecret
	code, out = invoke(true)
	if code != 0 {
		t.Fatalf("changed apply: %d %s", code, out)
	}
	current = request(t, "GET", "/v0/connectors/"+connectorID, token, nil, 200)
	if current["credential"].(map[string]any)["version"] != float64(1) || current["schedule"].(map[string]any)["interval_seconds"] != float64(43200) {
		t.Fatal("schedule or unchanged credential not preserved")
	}
	code, out = invoke(true)
	if code != 0 || !strings.Contains(out, "No changes.") {
		t.Fatalf("changed reapply: %d %s", code, out)
	}
	declaration["connectors"].([]any)[0].(map[string]any)["work_queue"] = "bulk"
	write()
	code, out = invoke(true)
	if code == 0 || !strings.Contains(out, "unsupported") {
		t.Fatalf("immutable connector change: %d %s", code, out)
	}
	current = request(t, "GET", "/v0/connectors/"+connectorID, token, nil, 200)
	if current["work_queue"] != "live" {
		t.Fatal("unsupported change modified connector")
	}
	// Append a provider through the same journal: selecting an operator key later
	// must not change the organization/deployment binding. This runs last in the
	// connector lane because activation deliberately replaces its RSS pin.
	declaration["connectors"].([]any)[0].(map[string]any)["work_queue"] = "live"
	operator := os.Getenv("QUIVR_TEST_OPERATOR")
	pinned := request(t, "GET", "/v0/admin/plugins", operator, nil, 200)
	var endpoint string
	for _, raw := range pinned["items"].([]any) {
		item := raw.(map[string]any)
		if item["plugin_id"] == "connector.rss" && item["state"] == "active" {
			endpoint = item["endpoint"].(string)
		}
	}
	if endpoint == "" {
		t.Fatal("RSS provider missing from installation fixture")
	}
	providerURL, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	// Forward to the real provider at a unique transport address, so its
	// registration identity cannot collide with the existing fixture owner.
	provider := httptest.NewServer(httputil.NewSingleHostReverseProxy(providerURL))
	defer provider.Close()
	manifest, err := filepath.Abs("../../plugins/rss/quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	fixtures := map[string]string{}
	for name := range pluginFixtures(t, "../../plugins/rss") {
		path, err := filepath.Abs(filepath.Join("../../plugins/rss/fixtures", name))
		if err != nil {
			t.Fatal(err)
		}
		fixtures[name] = path
	}
	declaration["plugins"] = []any{map[string]any{"idempotency_key": key + "-rss-provider", "manifest_file": manifest,
		"endpoint": provider.URL, "fixture_files": fixtures}}
	write()
	code, out = invoke(false)
	if code != 0 || !strings.Contains(out, "+ plugin") {
		t.Fatalf("plugin preview: %d %s", code, out)
	}
	code, out = invoke(true)
	if code != 0 {
		t.Fatalf("plugin apply: %d %s", code, out)
	}
	code, out = invoke(true)
	if code != 0 || !strings.Contains(out, "No changes.") {
		t.Fatalf("plugin reapply: %d %s", code, out)
	}

	declaration["connectors"].([]any)[0].(map[string]any)["schedule"] = map[string]any{"interval_seconds": 21600}
	write()
	rotateAfterSnapshot.Store(true)
	code, out = invoke(true)
	if code == 0 {
		t.Fatalf("external rotation after inspection must be refused: %s", out)
	}
	current = request(t, "GET", "/v0/connectors/"+connectorID, token, nil, 200)
	if current["credential"].(map[string]any)["version"] != float64(2) || current["schedule"].(map[string]any)["interval_seconds"] != float64(43200) {
		t.Fatal("apply overwrote external credential or changed schedule after stale preview")
	}

}
