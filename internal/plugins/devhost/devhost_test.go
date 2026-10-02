package devhost_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost/fakeplugin"
)

func TestMain(m *testing.M) {
	fakeplugin.MaybeRun()
	os.Exit(m.Run())
}

const manifestYAML = `id: fake
version: 1.0.0
compatibility:
  engine: ">=0.1.0 <0.2.0"
  plugin_api: ">=0.1.0 <0.2.0"
contributions:
  normalizer:
    media_types: [text/markdown]
configuration:
  schema:
    type: object
    properties:
      level: {type: integer, minimum: 1}
`

func writePlugin(t *testing.T) (string, plugins.Report) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, plugins.ManifestFile)
	if err := os.WriteFile(path, []byte(manifestYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	report := plugins.Inspect(dir)
	if !report.Valid {
		t.Fatalf("test manifest invalid: %+v", report.Errors)
	}
	return dir, report
}

func start(t *testing.T, dir string, env ...string) *devhost.Process {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	var logs strings.Builder
	p, err := devhost.Start(devhost.Options{
		Dir:      dir,
		Command:  fakeplugin.Command(),
		Manifest: filepath.Join(dir, plugins.ManifestFile),
		Env:      append([]string{fakeplugin.EnvEnable + "=1"}, env...),
		Output:   &logs,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(5 * time.Second) })
	if err := p.WaitHealthy(ctx); err != nil {
		t.Fatalf("not healthy: %v\n%s", err, logs.String())
	}
	return p
}

// TestDiscoveryIsComparedWithTheManifest owns the discovery comparison: the
// manifest digest, the Plugin API version within the declared range, and the
// Contributions in any order.
func TestDiscoveryIsComparedWithTheManifest(t *testing.T) {
	raw := []byte(`id: both
version: 1.0.0
compatibility: {engine: ">=0.1.0 <0.2.0", plugin_api: ">=0.1.0 <0.3.0"}
contributions:
  normalizer: {media_types: [text/markdown]}
  subscription: {expression_schema: {type: object}}
`)
	report := plugins.Validate(raw)
	if !report.Valid {
		t.Fatalf("%+v", report.Errors)
	}
	stale := "sha256:" + strings.Repeat("ab", 32)
	for name, c := range map[string]struct {
		digest        string
		pluginAPI     string
		contributions []string
		wantPath      string
		says          string
	}{
		"any order":             {report.ManifestDigest, "0.2.0", []string{"subscription", "normalizer"}, "", ""},
		"stale manifest digest": {stale, "0.2.0", []string{"normalizer", "subscription"}, "/manifest_digest", report.ManifestDigest},
		"too old for its rules": {report.ManifestDigest, "0.1.0", []string{"normalizer", "subscription"}, "/plugin_api", ""},
		"missing contribution":  {report.ManifestDigest, "0.2.0", []string{"normalizer"}, "/contributions", ""},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"plugin_api": c.pluginAPI, "plugin": map[string]string{"id": "both", "version": "1.0.0"},
					"manifest_digest": c.digest, "contributions": c.contributions})
			}))
			defer server.Close()
			issues, err := devhost.CheckDiscovery(context.Background(), server.URL, report)
			if err != nil {
				t.Fatal(err)
			}
			if c.wantPath == "" && len(issues) != 0 || c.wantPath != "" && (len(issues) != 1 || issues[0].Code != devhost.CodeDiscoveryMismatch || issues[0].Path != c.wantPath || !strings.Contains(issues[0].Message, c.says)) {
				t.Fatalf("issues %+v, want one at %q naming %q", issues, c.wantPath, c.says)
			}
		})
	}
}

func TestWaitHealthyFailsWhenTheProcessExits(t *testing.T) {
	dir, _ := writePlugin(t)
	var logs strings.Builder
	p, err := devhost.Start(devhost.Options{Dir: dir, Command: fakeplugin.Command(), Manifest: filepath.Join(dir, plugins.ManifestFile),
		Env: []string{fakeplugin.EnvEnable + "=1", fakeplugin.EnvMode + "=exit"}, Output: &logs})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop(time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = p.WaitHealthy(ctx)
	if err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(logs.String(), "exiting on purpose") {
		t.Fatalf("plugin output not forwarded: %q", logs.String())
	}
}

// A plugin that keeps answering 503 is reported with its last answer once
// the wait ends. The context ends on the second health request, so the test
// waits on no deadline.
func TestWaitHealthyReportsTheLastAnswerWhileUnhealthy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 2 {
			cancel()
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code": "warming_up", "message": "not ready", "retryable": true}`))
	}))
	defer server.Close()
	if err := devhost.WaitHealthyAt(ctx, server.URL); err == nil || !strings.Contains(err.Error(), "warming_up") {
		t.Fatalf("err %v", err)
	}
}

func writeFixture(t *testing.T, dir string, config string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "fixtures"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixtures", "doc.md"), []byte("# Hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(dir, "fixtures", "sample.json")
	body := `{"input": {"path": "doc.md", "media_type": "text/markdown"}` + config + `}`
	if err := os.WriteFile(fixture, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestBuildFixtureRequest(t *testing.T) {
	dir, report := writePlugin(t)
	fixture := writeFixture(t, dir, `, "configuration": {"level": 2}`)
	request, issues, err := devhost.BuildFixtureRequest(fixture, report.Manifest)
	if err != nil || len(issues) != 0 {
		t.Fatalf("issues %+v err %v", issues, err)
	}
	if got := plugins.ValidateDocument("normalizer-request.schema.json", request); len(got) != 0 {
		t.Fatalf("request violates the schema: %+v", got)
	}
	var doc struct {
		Input struct {
			SizeBytes int    `json:"size_bytes"`
			SHA256    string `json:"sha256"`
			Reference struct {
				Kind string `json:"kind"`
				URL  string `json:"url"`
			} `json:"reference"`
		} `json:"input"`
		Configuration map[string]int `json:"configuration"`
		Source        struct {
			RecordKey string `json:"record_key"`
		} `json:"source"`
	}
	if err := json.Unmarshal(request, &doc); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("# Hello\n"))
	if doc.Input.SizeBytes != 8 || doc.Input.SHA256 != hex.EncodeToString(sum[:]) || doc.Input.Reference.Kind != "file" ||
		!strings.HasPrefix(doc.Input.Reference.URL, "file:///") || !strings.HasSuffix(doc.Input.Reference.URL, "/fixtures/doc.md") ||
		doc.Configuration["level"] != 2 || doc.Source.RecordKey != "doc.md" {
		t.Fatalf("request %s", request)
	}

	bad := writeFixture(t, dir, `, "configuration": {"level": 0}`)
	if _, issues, err := devhost.BuildFixtureRequest(bad, report.Manifest); err != nil || len(issues) != 1 || issues[0].Code != plugins.CodeInvalidConfiguration {
		t.Fatalf("issues %+v err %v", issues, err)
	}
	if err := os.WriteFile(bad, []byte(`{"input": {"path": "doc.md"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, issues, err := devhost.BuildFixtureRequest(bad, report.Manifest); err != nil || len(issues) == 0 || issues[0].Code != plugins.CodeSchema {
		t.Fatalf("issues %+v err %v", issues, err)
	}
}

func TestInvokeNormalizer(t *testing.T) {
	dir, report := writePlugin(t)
	fixture := writeFixture(t, dir, "")
	request, _, err := devhost.BuildFixtureRequest(fixture, report.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	limit := report.Manifest.Contributions.Normalizer.Limits.MaxResponseBytes
	for _, c := range []struct {
		mode, code string
		status     int
		retryable  *bool
	}{
		{mode: "ok", status: 200},
		{mode: "invalid", status: 200, code: plugins.CodeInvalidManifest},
		{mode: "large", status: 200, code: devhost.CodeResponseTooLarge},
		{mode: "retry", status: 503, retryable: ptr(true)},
		{mode: "terminal", status: 422, retryable: ptr(false)},
		{mode: "garbage", status: 502, code: devhost.CodeInvalidErrorEnvelope},
	} {
		t.Run(c.mode, func(t *testing.T) {
			p := start(t, dir, fakeplugin.EnvMode+"="+c.mode)
			result, err := devhost.InvokeNormalizer(context.Background(), p.BaseURL, request, limit)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != c.status {
				t.Fatalf("status %d", result.Status)
			}
			if c.code == "" && len(result.Issues) != 0 || c.code != "" && (len(result.Issues) == 0 || result.Issues[0].Code != c.code) {
				t.Fatalf("issues %+v, want %q", result.Issues, c.code)
			}
			if c.retryable != nil && (result.Error == nil || result.Error.Retryable != *c.retryable) {
				t.Fatalf("error %+v", result.Error)
			}
			if c.mode == "ok" && !strings.Contains(string(result.Body), "file:///") {
				t.Fatalf("body %s", result.Body)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestWatcherDetectsSourceChanges(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "pkg", "main.py")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := devhost.NewWatcher(dir)
	if w.Changed() {
		t.Fatal("changed without edits")
	}
	for _, ignored := range []string{"__pycache__/x.pyc", ".venv/lib/x.py", ".git/HEAD", "node_modules/x.js"} {
		path := filepath.Join(dir, ignored)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if w.Changed() {
		t.Fatal("ignored directories trigger a restart")
	}
	if err := os.WriteFile(src, []byte("ab"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !w.Changed() {
		t.Fatal("edit not detected")
	}
	if w.Changed() {
		t.Fatal("same change reported twice")
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "new.py"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !w.Changed() {
		t.Fatal("new file not detected")
	}
}

// A wide compatibility range cannot make an older discovery version support
// fields needed by declared routes. This belongs to the discovery boundary,
// separately from validating which versions the manifest range admits.
func TestDiscoveryRequiresProtocolForDeclaredRouteAuthentication(t *testing.T) {
	raw := []byte(`id: source
version: 1.0.0
compatibility: {engine: ">=0.1.0 <0.2.0", plugin_api: ">=0.5.0 <0.13.0"}
contributions:
  connector:
    kinds:
      events:
        config_schema: {type: object}
        default_interval_seconds: 300
        modes: [pull, push]
        api:
          routes: [{name: push, method: POST, path: events, auth: quivr_key}]
`)
	for _, auth := range []string{"quivr_key", "instance_token"} {
		report := plugins.Validate([]byte(strings.ReplaceAll(string(raw), "auth: quivr_key", "auth: "+auth)))
		if !report.Valid {
			t.Fatal(report.Errors)
		}
		for _, version := range []string{"0.10.0", "0.11.0", "0.12.0"} {
			t.Run(auth+"/"+version, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewEncoder(w).Encode(map[string]any{"plugin_api": version, "plugin": map[string]string{"id": "source", "version": "1.0.0"}, "manifest_digest": report.ManifestDigest, "contributions": []string{"connector"}})
				}))
				defer server.Close()
				issues, err := devhost.CheckDiscovery(context.Background(), server.URL, report)
				if err != nil {
					t.Fatal(err)
				}
				if version == "0.12.0" || (version == "0.11.0" && auth == "quivr_key") {
					if len(issues) != 0 {
						t.Fatal(issues)
					}
				} else if len(issues) != 1 || issues[0].Code != devhost.CodeDiscoveryMismatch || issues[0].Path != "/plugin_api" {
					t.Fatalf("old protocol accepted routes: %+v", issues)
				}
			})
		}
	}
}
