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

func TestInitWritesATemplateThatPassesInspect(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	code, out, errOut := run("init", "demo", "--dir", dir)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"Created plugin demo", "quivr plugin dev --fixture fixtures/sample.json", "sdks/python"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if code, out, _ := run("inspect", dir); code != 0 {
		t.Fatalf("template fails inspect: %s", out)
	}
	if code, _, errOut := run("init", "demo", "--dir", dir); code != 1 || !strings.Contains(errOut, "not empty") {
		t.Fatalf("second init: exit %d %s", code, errOut)
	}
	for _, args := range [][]string{{"init"}, {"init", "Bad_Name"}, {"init", "a", "b"}, {"init", "demo", "--dir"}} {
		if code, _, errOut := run(args...); code != 2 {
			t.Errorf("%v: exit %d %s", args, code, errOut)
		}
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
  engine: ">=0.1.0 <0.2.0"
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
	waitFor("first replay", func() bool { return strings.Count(stderr.String(), "response valid") == 1 })
	// Ignored paths do not restart the plugin.
	if err := os.MkdirAll(filepath.Join(dir, "__pycache__"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "__pycache__", "x.pyc"), []byte("x"))
	time.Sleep(1500 * time.Millisecond)
	if starts() != 1 {
		t.Fatalf("restarted on an ignored change: %d starts", starts())
	}
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
