package acceptance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// quivrBinary returns the built quivr binary: the one the harness started the
// stack with (QUIVR_TEST_BINARY), or a fresh build of ./cmd/quivr.
func quivrBinary(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("QUIVR_TEST_BINARY"); bin != "" {
		return bin
	}
	bin := filepath.Join(t.TempDir(), "quivr")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", bin, "./cmd/quivr")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build quivr: %v\n%s", err, out)
	}
	return bin
}

type cliRun struct {
	exit           int
	stdout, stderr string
}

// runCLI runs the binary with only the given environment, so no QUIVR_CONFIG
// or stack setting leaks in from the harness.
func runCLI(t *testing.T, bin string, env []string, dir string, args ...string) cliRun {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)
	cmd.Dir = dir
	var out, errw bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errw
	err := cmd.Run()
	var exitErr *exec.ExitError
	exit := 0
	if errors.As(err, &exitErr) {
		exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return cliRun{exit, out.String(), errw.String()}
}

// TestCLISearch runs `quivr search` from the built binary against the running
// stack: ranked hits with provenance, --json, and one exit code per failure class.
func TestCLISearch(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	bin := quivrBinary(t)
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := monitoringRun()
	c := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "CLI search", "idempotency_key": "cli-corpus-" + run}, 201)["corpus_id"].(string)
	text := "Aurora borealis 🌌 over the northern fjords."
	start := request(t, "GET", changesPath(c, "", 0), admin, nil, 200)["next_cursor"].(string)
	r := request(t, "POST", "/v0/records", admin, inlineCommand(c, "cli-first-"+run, "aurora", text), 202)
	r = awaitReceipt(t, r["receipt_id"].(string))
	awaitSearchable(t, r)
	// Enrichment attaches vector provenance to the hit once it lands. Wait for it
	// so every search below, from the CLI or the API, sees the same final answer
	// (THE-754): the --json comparison must not straddle that moment.
	awaitEnriched(t, admin, c, start, r["record_id"].(string))
	env := []string{"QUIVR_API_URL=" + os.Getenv("QUIVR_TEST_URL"), "QUIVR_API_KEY=" + admin}
	dir := t.TempDir()

	var out cliRun
	deadline := time.Now().Add(30 * time.Second)
	for {
		out = runCLI(t, bin, env, dir, "search", "--corpus", c, "--mode", "lexical", "aurora", "borealis")
		if out.exit != 0 {
			t.Fatalf("search exit %d: %s", out.exit, out.stderr)
		}
		if !strings.HasPrefix(out.stdout, "0 hits") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("searchable text never returned by quivr search", out.stdout)
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, want := range []string{
		"1 hit (profile default, version ",
		"1. record " + r["record_id"].(string) + "  version " + r["version_id"].(string) + fmt.Sprintf("  part body  [0,%d)", len([]rune(text))),
		"   " + text,
	} {
		if !strings.Contains(out.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, out.stdout)
		}
	}

	// --json prints the public response unchanged: the same document the API returns.
	js := runCLI(t, bin, env, dir, "search", "--json", "--corpus", c, "--mode", "lexical", "aurora borealis")
	if js.exit != 0 {
		t.Fatalf("search --json exit %d: %s", js.exit, js.stderr)
	}
	var fromCLI map[string]any
	if err := json.Unmarshal([]byte(js.stdout), &fromCLI); err != nil {
		t.Fatalf("--json is not JSON: %v\n%s", err, js.stdout)
	}
	fromAPI := request(t, "POST", "/v0/search", admin, map[string]any{"query": "aurora borealis", "corpus_ids": []string{c}, "mode": "lexical"}, 200)
	// Two searches take their own time: usage.elapsed_ms is the one field that may differ.
	for _, response := range []map[string]any{fromCLI, fromAPI} {
		if usage, ok := response["usage"].(map[string]any); ok {
			delete(usage, "elapsed_ms")
		}
	}
	if a, b := canonicalJSON(t, fromCLI), canonicalJSON(t, fromAPI); a != b {
		t.Fatalf("--json differs from the API response\ncli: %s\napi: %s", a, b)
	}
	if hit := fromCLI["items"].([]any)[0].(map[string]any); hit["embedding_artifact_id"] == nil || hit["vector_space_id"] == nil {
		t.Fatalf("compared a search taken before enrichment: %v", hit)
	}

	unused, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	down := "http://" + unused.Addr().String()
	unused.Close()
	for _, tc := range []struct {
		name   string
		env    []string
		args   []string
		exit   int
		stderr string
	}{
		{"unknown key", []string{"QUIVR_API_URL=" + os.Getenv("QUIVR_TEST_URL"), "QUIVR_API_KEY=not-a-key"}, nil, 3, "quivr: invalid_api_key:"},
		{"key outside the Corpus scope", []string{"QUIVR_API_URL=" + os.Getenv("QUIVR_TEST_URL"), "QUIVR_API_KEY=" + os.Getenv("QUIVR_TEST_SCOPED")}, nil, 3, "quivr: forbidden:"},
		{"invalid request", env, []string{"--limit", "0"}, 4, "quivr: invalid_schema:"},
		{"unsupported mode", env, []string{"--mode", "fuzzy"}, 4, "quivr: "},
		{"unreachable server", []string{"QUIVR_API_URL=" + down, "QUIVR_API_KEY=" + admin}, nil, 5, "quivr: unreachable:"},
		{"flag overrides environment", []string{"QUIVR_API_URL=" + os.Getenv("QUIVR_TEST_URL"), "QUIVR_API_KEY=" + admin}, []string{"--api-url", down}, 5, "quivr: unreachable:"},
		{"no server configured", nil, nil, 2, "QUIVR_API_URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"search", "--corpus", c}, tc.args...)
			got := runCLI(t, bin, tc.env, dir, append(args, "aurora")...)
			if got.exit != tc.exit || got.stdout != "" || !strings.Contains(got.stderr, tc.stderr) {
				t.Fatalf("exit %d (want %d) stdout %q stderr %q", got.exit, tc.exit, got.stdout, got.stderr)
			}
		})
	}

	help := runCLI(t, bin, nil, dir, "search", "--help")
	for _, want := range []string{"needs a running Quivr server", "QUIVR_API_URL", "QUIVR_API_KEY"} {
		if help.exit != 0 || !strings.Contains(help.stdout, want) {
			t.Fatalf("help exit %d missing %q:\n%s", help.exit, want, help.stdout)
		}
	}
}

// TestCLIPluginToolsNeedNoServer proves the offline plugin-author commands
// still run from the built binary with no server and no configuration.
func TestCLIPluginToolsNeedNoServer(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	bin := quivrBinary(t)
	dir := t.TempDir()
	if got := runCLI(t, bin, nil, dir, "plugin", "init", "offline-check", "--dir", "offline-check"); got.exit != 0 {
		t.Fatalf("plugin init exit %d: %s%s", got.exit, got.stdout, got.stderr)
	}
	got := runCLI(t, bin, nil, dir, "plugin", "inspect", "--json", "offline-check")
	var report map[string]any
	if got.exit != 0 || json.Unmarshal([]byte(got.stdout), &report) != nil || report["valid"] != true {
		t.Fatalf("plugin inspect exit %d: %s%s", got.exit, got.stdout, got.stderr)
	}
}

func canonicalJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
