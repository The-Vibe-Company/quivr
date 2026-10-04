package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/cli"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost/fakeplugin"
)

func TestMain(m *testing.M) {
	fakeplugin.MaybeRun()
	os.Exit(m.Run())
}

// TestInitWritesTheRequestedTemplate owns `quivr plugin init`: the kind (a
// normalizer by default), the directory and the next steps it prints, and its
// exit codes. The templates themselves are owned by the scaffold package.
func TestInitWritesTheRequestedTemplate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	code, out, errOut := run("init", "demo", "--dir", dir)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"Created normalizer plugin demo", "quivr plugin dev --fixture fixtures/sample.json", "sdks/python"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if code, out, _ := run("inspect", dir); code != 0 || !strings.Contains(out, "Contribution normalizer") {
		t.Fatalf("init wrote no normalizer in --dir: %s", out)
	}
	if code, _, errOut := run("init", "demo", "--dir", dir); code != 1 || !strings.Contains(errOut, "not empty") {
		t.Fatalf("second init: exit %d %s", code, errOut)
	}
	alerts := filepath.Join(t.TempDir(), "alerts")
	if code, out, errOut := run("init", "alerts", "--kind", "subscription", "--dir", alerts); code != 0 || !strings.Contains(out, "Created subscription plugin alerts") {
		t.Fatalf("init --kind subscription: exit %d %s %s", code, out, errOut)
	}
	if code, out, _ := run("inspect", alerts); code != 0 || !strings.Contains(out, "Contribution subscription") {
		t.Fatalf("init --kind subscription wrote no alert rule: %s", out)
	}
	for _, args := range [][]string{{"init"}, {"init", "Bad_Name"}, {"init", "a", "b"}, {"init", "demo", "--dir"}, {"init", "demo", "--kind"}, {"init", "demo", "--push"}, {"init", "demo", "--kind", "subscription", "--push"}} {
		if code, _, errOut := run(args...); code != 2 {
			t.Errorf("%v: exit %d %s", args, code, errOut)
		}
	}
	collector := filepath.Join(t.TempDir(), "collector")
	if code, out, errOut := run("init", "collector", "--kind=connector", "--dir", collector); code != 0 || !strings.Contains(out, "Created connector plugin collector") || strings.Contains(out, "plugin dev") {
		t.Fatalf("init --kind connector: exit %d %s %s", code, out, errOut)
	}
	if code, out, _ := run("inspect", collector); code != 0 {
		t.Fatalf("init --kind connector wrote no collector: %s", out)
	}
	push := filepath.Join(t.TempDir(), "push")
	if code, _, errOut := run("init", "push", "--kind", "connector", "--push", "--dir", push); code != 0 {
		t.Fatalf("init push: exit %d %s", code, errOut)
	}
	report := plugins.Inspect(push)
	if !report.Valid || !plugins.KindPushes(report.Manifest, "events") {
		t.Fatalf("push scaffold invalid: %+v", report)
	}

	if code, _, errOut := run("init", "demo", "--kind=unknown"); code != 2 || !strings.Contains(errOut, "connector") {
		t.Errorf("unknown kind: exit %d %s", code, errOut)
	}
}

// fakePluginDir writes a plugin whose run.command starts the fake plugin, and a
// fixture for it.
func fakePluginDir(t *testing.T, env ...string) string {
	t.Helper()
	t.Setenv(fakeplugin.EnvEnable, "1")
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	dir := t.TempDir()
	command, _ := json.Marshal(fakeplugin.Command())
	manifest := fmt.Sprintf(`id: fake
version: 1.0.0
compatibility:
  engine: ">=0.1.0 <0.3.0"
  plugin_api: ">=0.1.0 <0.2.0"
contributions:
  normalizer:
    media_types: [text/markdown]
run:
  command: %s
`, command)
	writeFile(t, filepath.Join(dir, plugins.ManifestFile), []byte(manifest))
	if err := os.MkdirAll(filepath.Join(dir, "fixtures"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "fixtures", "doc.md"), []byte("# Hello\n"))
	writeFile(t, filepath.Join(dir, "fixtures", "sample.json"), []byte(`{"input": {"path": "doc.md", "media_type": "text/markdown"}}`))
	return dir
}

func TestDevReplaysAFixtureAndPrintsTheValidatedResponse(t *testing.T) {
	dir := fakePluginDir(t)
	code, out, errOut := run("dev", "--fixture", filepath.Join(dir, "fixtures", "sample.json"), dir)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, errOut)
	}
	if issues := plugins.ValidateNormalizerResponse([]byte(out)); len(issues) != 0 {
		t.Fatalf("stdout is not a valid response: %+v\n%s", issues, out)
	}
	for _, want := range []string{"healthy", "discovery matches", "response valid"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut)
		}
	}
}

// subscriptionPluginDir writes a plugin declaring both Contributions and a
// subscription fixture with two batches (max_batch_size 1).
func subscriptionPluginDir(t *testing.T, env ...string) string {
	t.Helper()
	dir := fakePluginDir(t, env...)
	command, _ := json.Marshal(fakeplugin.Command())
	writeFile(t, filepath.Join(dir, plugins.ManifestFile), []byte(fmt.Sprintf(`id: fake
version: 1.0.0
compatibility:
  engine: ">=0.1.0 <0.3.0"
  plugin_api: ">=0.1.0 <0.3.0"
contributions:
  normalizer:
    media_types: [text/markdown]
  subscription:
    expression_schema: {type: object, required: [text], properties: {text: {type: string}}}
    max_batch_size: 1
run:
  command: %s
`, command)))
	writeFile(t, filepath.Join(dir, "fixtures", "alerts.json"), []byte(`{
  "record": {"parts": [{"key": "body", "role": "body", "text": "Dockers vote to strike."}]},
  "evaluations": [
    {"expression": {"text": "strike"}, "expect": "match"},
    {"expression": {"text": "election"}, "expect": "no_match"}
  ]
}`))
	return dir
}

func TestDevReplaysASubscriptionFixture(t *testing.T) {
	dir := subscriptionPluginDir(t)
	code, out, errOut := run("dev", "--fixture", filepath.Join(dir, "fixtures", "alerts.json"), dir)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, errOut)
	}
	var response struct {
		Decisions []plugins.SubscriptionDecision `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil || len(response.Decisions) != 2 ||
		response.Decisions[0].Decision != "match" || response.Decisions[1].Decision != "no_match" {
		t.Fatalf("stdout %s (%v)", out, err)
	}
	if !strings.Contains(errOut, "2 decisions in 2 batches (1 match, 1 no_match, 0 not_ready)") {
		t.Fatalf("stderr:\n%s", errOut)
	}
	// The normalizer fixture of the same plugin still replays.
	if code, _, errOut := run("dev", "--fixture", filepath.Join(dir, "fixtures", "sample.json"), dir); code != 0 {
		t.Fatalf("normalizer fixture: exit %d:\n%s", code, errOut)
	}
}

func TestDevReportsAnUnexpectedSubscriptionDecision(t *testing.T) {
	dir := subscriptionPluginDir(t, fakeplugin.EnvMode+"=never-match")
	code, out, errOut := run("dev", "--fixture", filepath.Join(dir, "fixtures", "alerts.json"), dir)
	if code != 1 || out != "" || !strings.Contains(errOut, "unexpected_decision") || !strings.Contains(errOut, "the fixture expects match") {
		t.Fatalf("exit %d, stdout %q:\n%s", code, out, errOut)
	}
}

func TestDevReportsADiscoveryDigestMismatch(t *testing.T) {
	dir := fakePluginDir(t, fakeplugin.EnvDigest+"=sha256:"+strings.Repeat("cd", 32))
	code, out, errOut := run("dev", "--fixture", filepath.Join(dir, "fixtures", "sample.json"), dir)
	if code != 1 || out != "" || !strings.Contains(errOut, "discovery_mismatch") || !strings.Contains(errOut, "/manifest_digest") {
		t.Fatalf("exit %d, stdout %q:\n%s", code, out, errOut)
	}
}

func TestDevFailsOnPluginErrorsAndInvalidResponses(t *testing.T) {
	for mode, want := range map[string]string{"terminal": "unreadable", "retry": "backend_busy", "invalid": plugins.CodeInvalidManifest} {
		t.Run(mode, func(t *testing.T) {
			dir := fakePluginDir(t, fakeplugin.EnvMode+"="+mode)
			code, out, errOut := run("dev", "--fixture", filepath.Join(dir, "fixtures", "sample.json"), dir)
			if code != 1 || out != "" || !strings.Contains(errOut, want) {
				t.Fatalf("exit %d, stdout %q:\n%s", code, out, errOut)
			}
		})
	}
}

func TestDevRejectsInvalidInputsBeforeLaunching(t *testing.T) {
	dir := fakePluginDir(t)
	writeFile(t, filepath.Join(dir, "fixtures", "bad.json"), []byte(`{"input": {"path": "doc.md"}}`))
	if code, _, errOut := run("dev", "--fixture", filepath.Join(dir, "fixtures", "bad.json"), dir); code != 1 || !strings.Contains(errOut, "schema_violation") {
		t.Fatalf("bad fixture: exit %d\n%s", code, errOut)
	}
	noRun := t.TempDir()
	raw, _ := readFixture(t, "valid/minimal.yaml")
	writeFile(t, filepath.Join(noRun, plugins.ManifestFile), raw)
	fixture := filepath.Join(dir, "fixtures", "sample.json")
	if code, _, errOut := run("dev", "--fixture", fixture, noRun); code != 1 || !strings.Contains(errOut, "run.command") {
		t.Fatalf("no run command: exit %d\n%s", code, errOut)
	}
	invalid := t.TempDir()
	raw, _ = readFixture(t, "invalid/reserved-contribution.yaml")
	writeFile(t, filepath.Join(invalid, plugins.ManifestFile), raw)
	if code, _, errOut := run("dev", "--fixture", fixture, invalid); code != 1 || !strings.Contains(errOut, "reserved_contribution") {
		t.Fatalf("invalid manifest: exit %d\n%s", code, errOut)
	}
	for _, args := range [][]string{{"dev", "--nope"}, {"dev", "a", "b"}, {"dev", "--fixture"}, {"dev", "--port", "x"}} {
		if code, _, _ := run(args...); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestDevWatchRestartsOnSourceChange(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "starts")
	dir := fakePluginDir(t, fakeplugin.EnvMarker+"="+marker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout, stderr syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- cli.RunContext(ctx, []string{"dev", "--watch", "--fixture", filepath.Join(dir, "fixtures", "sample.json"), dir}, &stdout, &stderr)
	}()
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s\nstderr:\n%s", what, stderr.String())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	starts := func() int {
		b, _ := os.ReadFile(marker)
		return strings.Count(string(b), "start")
	}
	waitFor("first replay and watching", func() bool {
		return starts() == 1 && strings.Count(stderr.String(), "response valid") == 1 && strings.Contains(stderr.String(), "watching")
	})
	// Which paths count as a change is owned by devhost.Watcher.
	writeFile(t, filepath.Join(dir, "plugin.py"), []byte("changed"))
	waitFor("restart and second replay", func() bool { return starts() == 2 && strings.Count(stderr.String(), "response valid") == 2 })
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("dev did not stop after cancellation")
	}
	if strings.Count(stdout.String(), `"parts"`) != 2 {
		t.Fatalf("stdout:\n%s", stdout.String())
	}
}
